package workspaceops

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// WS-59d: `op` is an argument of the blast form only (PAR-IMP-03), it forwards as the
// core's --op flag, and a form that takes no occurrence read refuses it whole.
func TestImpactFlagsForwardsTheOccurrenceOperation(t *testing.T) {
	got, err := ImpactFlags(ImpactBlast, map[string]string{"op": "refs", "limit": "5"})
	if err != nil || !slices.Equal(got, []string{"--op=refs", "--limit=5"}) {
		t.Fatalf("got %v %v", got, err)
	}
	// each operation is its own core value
	for _, op := range OccurrenceOps {
		if f, err := ImpactFlags(ImpactBlast, map[string]string{"op": op}); err != nil || !slices.Equal(f, []string{"--op=" + op}) {
			t.Fatalf("%s: got %v %v", op, f, err)
		}
	}
	// op= and the tier paging are two different pages of the same request, so a
	// cursor with the blast form's own offset is refused rather than silently ignored.
	// A blank cursor is absent, not bad: ImpactFlags never sees one, because the query
	// reader drops empty values before the flags are built.
	for _, bad := range []map[string]string{
		{"op": "sideways"},
		{"op": "Refs"}, // the core's set is lower case
		{"op": "refs", "offset": "5", "cursor": "abc"},
	} {
		if _, err := ImpactFlags(ImpactBlast, bad); !IsInvalidInput(err) {
			t.Fatalf("%v: want invalid input, got %v", bad, err)
		}
	}
	// and an explicit blank is refused rather than read as a cursor of nothing
	if err := notBlank(" "); err == nil {
		t.Fatal("a blank cursor was accepted")
	}
	// A cursor alone pages whichever list the form walks: WS-59e gave the tiers one, so
	// refusing a cursor that carries no op= would refuse the tiers' own second page. The
	// pair is what cannot be honoured, because each cursor names a different list.
	if _, err := ImpactFlags(ImpactBlast, map[string]string{"cursor": "x"}); err != nil {
		t.Fatalf("a cursor alone pages the tiers (WS-59e): %v", err)
	}
	if _, err := ImpactFlags(ImpactBlast, map[string]string{"offset": "5", "cursor": "abc"}); !IsInvalidInput(err) {
		t.Fatalf("an offset beside a cursor: want invalid input, got %v", err)
	}
	for _, form := range []ImpactForm{ImpactTrace, ImpactFile, ImpactChanges, ImpactSinceIndex} {
		if _, err := ImpactFlags(form, map[string]string{"op": "refs"}); !IsInvalidInput(err) {
			t.Fatalf("%s took op=refs: %v", form, err)
		}
		if _, err := ImpactFlags(form, map[string]string{"cursor": "abc"}); !IsInvalidInput(err) {
			t.Fatalf("%s took cursor: %v", form, err)
		}
	}
}

// op= selects between the tier walk and the occurrence read, so it is part of the tier
// ranking key: a cursor signed for the tiers must not page an occurrence request, or one
// occurrence list's cursor would continue the other. The occurrence cursor has its own
// key and codec, so this binds only the tier side.
func TestATierCursorIsBoundToTheOperationItWasSignedFor(t *testing.T) {
	walk := url.Values{"symbol": {"Store"}, "limit": {"2"}}
	c := ImpactCursor("ws", walk)(4)
	if _, err := DecodeImpactCursor("ws", walk, c); err != nil {
		t.Fatalf("the cursor does not decode for the walk it was signed for: %v", err)
	}
	// the same request with op= is a different list, so the walk's cursor is refused
	occurrences := url.Values{"symbol": {"Store"}, "limit": {"2"}, "op": {"refs"}}
	if _, err := DecodeImpactCursor("ws", occurrences, c); !IsInvalidInput(err) {
		t.Fatalf("a walk cursor paged the occurrence read: %v", err)
	}
	// and one signed with op= does not page the walk, which is the reverse direction
	if n, err := DecodeImpactCursor("ws", occurrences, ImpactCursor("ws", occurrences)(4)); err != nil || n != 4 {
		t.Fatalf("an occurrence ranking does not sign its own cursor: %d %v", n, err)
	}
	if _, err := DecodeImpactCursor("ws", walk, ImpactCursor("ws", occurrences)(4)); !IsInvalidInput(err) {
		t.Fatalf("an occurrence cursor paged the walk: %v", err)
	}
}

// No occurrence operation walks the graph, so a tier-walk argument is refused for all
// three of them, refs included. The parameters a request carries are the whole set, and
// the pair a cursor forms with an offset is refused before the cursor is dropped, so one
// call cannot name two page positions.
func TestEveryOccurrenceOperationRefusesAWalkArgument(t *testing.T) {
	// the list read itself forwards no walk argument and is not refused
	if _, err := occurrenceCoreParams(map[string]string{"op": "refs", "limit": "5"}); err != nil {
		t.Fatalf("a plain occurrence read was refused: %v", err)
	}
	// a cursor beside an offset names two positions and is refused by the same check the
	// tier path uses, for every operation and whether or not the cursor is signed
	page, err := signOccurrencePage([]byte(`{"next_cursor":"6"}`), occurrenceRanking("ws", "Store", "refs"))
	if err != nil {
		t.Fatalf("sign a page for the walk-argument cases: %v", err)
	}
	var signedPage map[string]json.RawMessage
	if err := json.Unmarshal(page, &signedPage); err != nil {
		t.Fatal(err)
	}
	var signedCursor string
	if err := json.Unmarshal(signedPage["next_cursor"], &signedCursor); err != nil {
		t.Fatal(err)
	}
	for _, op := range OccurrenceOps {
		for _, cursor := range []string{"abc", signedCursor} {
			// through the helper the request actually takes, so the ordering it fixes is
			// what is under test and not the check in isolation. The refusal is the
			// contract: a request like this must never produce flags at all, because the
			// caller's offset and the cursor's own would both reach the core as
			// --offset and the second would silently win.
			p := map[string]string{"op": op, "offset": "2", "cursor": cursor}
			core, err := occurrenceCoreParams(p)
			if !IsInvalidInput(err) {
				t.Fatalf("op=%s took a cursor beside an offset (%q): %v", op, cursor, err)
			}
			if core != nil {
				t.Fatalf("op=%s was refused but still handed parameters: %v", op, core)
			}
		}
		// and the walk arguments are refused on the same path, for every operation
		for _, arg := range walkArgs {
			p := map[string]string{"op": op, arg: "1"}
			switch arg {
			case "direction", "relation_types", "summary_only", "include_tests":
				p[arg] = "upstream"
			}
			if _, err := occurrenceCoreParams(p); !IsInvalidInput(err) {
				t.Fatalf("op=%s took the walk argument %s: %v", op, arg, err)
			}
		}
	}
}

// The core sends next_cursor only while rows remain, so the last page carries none and
// the reconciler must not invent one: a client that follows a cursor onto the last page
// reads an empty page and is handed the same offset again, and never terminates.
func TestTheLastOccurrencePageKeepsNoCursor(t *testing.T) {
	const ws, symbol, op = "ws", "Store", "refs"
	last := `{"total":9,"offset":9,"occurrences":[]}`
	var page map[string]json.RawMessage
	if err := json.Unmarshal(ReconcileOccurrencePaging(ws, symbol, op, []byte(last)), &page); err != nil {
		t.Fatal(err)
	}
	if c, ok := page["next_cursor"]; ok {
		t.Fatalf("the last page grew a cursor %s, so a client would page it forever", c)
	}
	// a page the core said had more keeps one, at the offset it named
	middle := `{"total":9,"offset":4,"occurrences":[{"path":"a.go"},{"path":"b.go"}],"next_cursor":"6"}`
	page = nil
	if err := json.Unmarshal(ReconcileOccurrencePaging(ws, symbol, op, []byte(middle)), &page); err != nil {
		t.Fatal(err)
	}
	raw, ok := page["next_cursor"]
	if !ok {
		t.Fatalf("a page with more lost its cursor: %s", middle)
	}
	var cursor string
	if err := json.Unmarshal(raw, &cursor); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOccurrenceCursor(ws, symbol, op, cursor)
	if err != nil || got != 6 {
		t.Fatalf("the cursor continues at %d, want the page offset 4 plus its 2 rows: %v", got, err)
	}
}

// The last occurrence page carries no cursor, because the core had no more rows to
// offer. But if the budget cut rows off that page the client is still missing them, and
// the ladder promises that next_cursor continues at the first occurrence not returned, so
// a trimmed last page keeps a cursor at the position the cut reached
// (WS-59d-r5-1). Without the budget report the case is indistinguishable from an uncut
// last page, which is why the report is read rather than guessed.
func TestATrimmedLastOccurrencePageStillCarriesACursor(t *testing.T) {
	const ws, symbol, op = "ws", "Store", "refs"
	trimmedLast := `{"total":9,"offset":6,"occurrences":[{"path":"a.go"}],` +
		`"output_budget":{"max_chars":200,"used_chars":180,"degraded_stage":"trimmed",` +
		`"lists":{"occurrences":{"state":"trimmed","kept":1,"total":3}}}}`
	var page map[string]json.RawMessage
	if err := json.Unmarshal(ReconcileOccurrencePaging(ws, symbol, op, []byte(trimmedLast)), &page); err != nil {
		t.Fatal(err)
	}
	raw, ok := page["next_cursor"]
	if !ok {
		t.Fatalf("a trimmed last page dropped its cursor, so 2 rows are unreachable: %s", trimmedLast)
	}
	var cursor string
	if err := json.Unmarshal(raw, &cursor); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOccurrenceCursor(ws, symbol, op, cursor)
	if err != nil {
		t.Fatalf("the cursor does not decode: %v", err)
	}
	// the page held one row of the three the budget counted, so it resumes at 7
	if got != 7 {
		t.Fatalf("the cursor continues at %d, want the page offset 6 plus the 1 row delivered: %s", got, raw)
	}
	// and a budget report that did not cut this list leaves the last page alone
	full := `{"total":9,"offset":6,"occurrences":[{"path":"a.go"}],` +
		`"output_budget":{"degraded_stage":"full","lists":{"occurrences":{"kept":1,"total":1}}}}`
	page = nil
	if err := json.Unmarshal(ReconcileOccurrencePaging(ws, symbol, op, []byte(full)), &page); err != nil {
		t.Fatal(err)
	}
	if c, ok := page["next_cursor"]; ok {
		t.Fatalf("an uncut last page grew a cursor %s: %s", c, full)
	}
}

// The page the core answers carries a bare offset; what a client receives is a signed
// cursor, and the last page carries none at all. This is the only place the core's own
// next_cursor becomes a cursor, so it is tested directly rather than only through the
// route, which needs a live core.
func TestSignOccurrencePageSignsTheCoresOffsetAndDropsTheLastPage(t *testing.T) {
	ranking := occurrenceRanking("ws", "Store", "refs")

	signed, err := signOccurrencePage([]byte(`{"total":9,"occurrences":[{"path":"a.go"}],"next_cursor":"4"}`), ranking)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal(signed, &page); err != nil {
		t.Fatalf("the signed page is not a JSON object: %v", err)
	}
	var cursor string
	if err := json.Unmarshal(page["next_cursor"], &cursor); err != nil {
		t.Fatalf("next_cursor is not a string: %v", err)
	}
	got, err := DecodeOccurrenceCursor("ws", "Store", "refs", cursor)
	if err != nil || got != 4 {
		t.Fatalf("the signed cursor does not decode to the core's offset: got %d %v", got, err)
	}
	if string(page["total"]) != "9" {
		t.Fatalf("signing changed another member: %s", signed)
	}

	// the last page: the core's next_cursor is absent, and nothing is added
	last, err := signOccurrencePage([]byte(`{"total":9,"occurrences":[]}`), ranking)
	if err != nil {
		t.Fatalf("sign the last page: %v", err)
	}
	var lastPage map[string]json.RawMessage
	if err := json.Unmarshal(last, &lastPage); err != nil {
		t.Fatal(err)
	}
	if _, ok := lastPage["next_cursor"]; ok {
		t.Fatalf("a last page grew a cursor: %s", last)
	}

	// an offset of zero signs to nothing, so no cursor is left behind
	zero, err := signOccurrencePage([]byte(`{"total":0,"occurrences":[],"next_cursor":"0"}`), ranking)
	if err != nil {
		t.Fatalf("sign a zero offset: %v", err)
	}
	var zeroPage map[string]json.RawMessage
	if err := json.Unmarshal(zero, &zeroPage); err != nil {
		t.Fatal(err)
	}
	if _, ok := zeroPage["next_cursor"]; ok {
		t.Fatalf("a zero offset left a cursor: %s", zero)
	}
}

// The core's next_cursor is an offset, so anything else is a core contract break and is
// reported rather than signed as a position.
func TestSignOccurrencePageRefusesACursorThatIsNotAnOffset(t *testing.T) {
	_, err := signOccurrencePage([]byte(`{"next_cursor":"not a number"}`), occurrenceRanking("ws", "S", "refs"))
	if err == nil {
		t.Fatal("a malformed core cursor was signed")
	}
	if _, err := signOccurrencePage([]byte(`[1,2]`), occurrenceRanking("ws", "S", "refs")); err == nil {
		t.Fatal("a page that is not an object was signed")
	}
}

// The occurrence cursor belongs to the occurrence read: a request naming a symbol with
// no op= keeps paging the tiers with offset, and one naming op= pages occurrences.
func TestCheckImpactRequestKeepsOpWithTheBlastFormOnly(t *testing.T) {
	form, rest, err := CheckImpactRequest(map[string]string{"symbol": "S", "op": "definition"})
	if err != nil || form != ImpactBlast || rest["op"] != "definition" {
		t.Fatalf("got %s %v %v", form, rest, err)
	}
	if _, _, err := CheckImpactRequest(map[string]string{"path": "a.go", "op": "refs"}); !IsInvalidInput(err) {
		t.Fatalf("op with the file form: %v", err)
	}
	// op= without a symbol names no form, so it is refused by the form check
	if _, _, err := CheckImpactRequest(map[string]string{"op": "refs"}); !IsInvalidInput(err) {
		t.Fatalf("op without a target: %v", err)
	}
}

// An occurrence cursor is bound to its symbol and operation, so a client can neither
// edit the offset nor continue another occurrence list with it.
func TestOccurrenceCursorIsBoundToItsSymbolAndOperation(t *testing.T) {
	sign := OccurrenceCursor("ws", "Store", "refs")
	cursor := sign(7)
	if cursor == "" {
		t.Fatal("no cursor was signed for a positive offset")
	}
	got, err := DecodeOccurrenceCursor("ws", "Store", "refs", cursor)
	if err != nil || got != 7 {
		t.Fatalf("decode: got %d %v", got, err)
	}
	for _, other := range []struct{ ws, symbol, op string }{
		{"other", "Store", "refs"},    // another workspace
		{"ws", "Other", "refs"},       // another symbol
		{"ws", "Store", "definition"}, // another operation
	} {
		if _, err := DecodeOccurrenceCursor(other.ws, other.symbol, other.op, cursor); !IsInvalidInput(err) {
			t.Fatalf("a cursor from %v was accepted: %v", other, err)
		}
	}
	// an edited cursor and one over the length bound are refused; a bare number is not
	// a cursor at all
	for _, bad := range []string{"not-a-cursor", cursor + "A", "Nw=="} {
		if _, err := DecodeOccurrenceCursor("ws", "Store", "refs", bad); !IsInvalidInput(err) {
			t.Fatalf("%q was accepted as a cursor: %v", bad, err)
		}
	}
	// an absent cursor is the first page, not an error
	if off, err := DecodeOccurrenceCursor("ws", "Store", "refs", ""); off != 0 || err != nil {
		t.Fatalf("an absent cursor: got %d %v", off, err)
	}
	// a non-positive offset is the first page, so no cursor is signed for it and a
	// caller can never be handed one that decodes back to the start
	if blank := sign(0); blank != "" {
		t.Fatalf("offset zero was signed as %q", blank)
	}
}

// A budget rung that removed occurrence rows leaves the handler's cursor describing the
// core's page, so following it skips rows the caller never received. The reconciler
// re-signs it at the first row the caller did not receive, and when the rung removed the
// list outright there is nothing to continue from, so the cut is named instead.
func TestReconcileOccurrencePagingFollowsWhatTheCallerReceivedAndNamesARemovedList(t *testing.T) {
	const ws, symbol, op = "ws", "Store", "refs"
	// the core signed page offset 4 of 9; the budget kept two of the five rows it held
	signed, err := signOccurrencePage(
		[]byte(`{"total":9,"offset":4,"occurrences":[{"path":"a.go"},{"path":"b.go"},{"path":"c.go"},{"path":"d.go"},{"path":"e.go"}],"next_cursor":"9"}`),
		occurrenceRanking(ws, symbol, op))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	trimmed := strings.Replace(string(signed), `"occurrences":[{"path":"a.go"},{"path":"b.go"},{"path":"c.go"},{"path":"d.go"},{"path":"e.go"}]`,
		`"occurrences":[{"path":"a.go"},{"path":"b.go"}]`, 1)
	if trimmed == string(signed) {
		t.Fatal("the fixture did not trim the list, so it proves nothing")
	}

	out := ReconcileOccurrencePaging(ws, symbol, op, []byte(trimmed))
	var page map[string]json.RawMessage
	if err := json.Unmarshal(out, &page); err != nil {
		t.Fatalf("the reconciled page is not an object: %v", err)
	}
	var cursor string
	if err := json.Unmarshal(mustCursor(t, page), &cursor); err != nil {
		t.Fatalf("next_cursor is not a string: %v", err)
	}
	got, err := DecodeOccurrenceCursor(ws, symbol, op, cursor)
	if err != nil {
		t.Fatalf("the reconciled cursor does not decode for this list: %v", err)
	}
	if got != 6 {
		t.Fatalf("the cursor continues at %d, want the page offset 4 plus the 2 rows delivered: %s", got, out)
	}
	if _, ok := page["total"]; !ok {
		t.Fatalf("reconciling dropped total: %s", out)
	}

	// the counts rung replaced the rows: there is no position to continue from, so the
	// answer carries no cursor at all and names the cut
	counts := `{"total":9,"offset":4,"occurrences":[],"occurrences_by_path":{"a.go":2},"next_cursor":"9"}`
	out = ReconcileOccurrencePaging(ws, symbol, op, []byte(counts))
	// a fresh map, because Unmarshal merges into a map that already holds keys and the
	// cursor from the case above would read as this page's own
	var countsPage map[string]json.RawMessage
	if err := json.Unmarshal(out, &countsPage); err != nil {
		t.Fatal(err)
	}
	if c, ok := countsPage["next_cursor"]; ok {
		t.Fatalf("a counts-only page kept a cursor %s: %s", c, out)
	}
	if string(countsPage["truncated"]) != `"occurrences_removed"` {
		t.Fatalf("a counts-only page does not name its cut: %s", out)
	}

	// a result that is not an occurrence page is left to the tier reconciler untouched
	tier := `{"by_depth":[{"depth":1,"items":[]}],"next_cursor":"9"}`
	if got := string(ReconcileOccurrencePaging(ws, symbol, op, []byte(tier))); got != tier {
		t.Fatalf("a tier page was rewritten: %s", got)
	}
}

func mustCursor(t *testing.T, page map[string]json.RawMessage) []byte {
	t.Helper()
	raw, ok := page["next_cursor"]
	if !ok {
		t.Fatal("the reconciled page carries no next_cursor")
	}
	return raw
}

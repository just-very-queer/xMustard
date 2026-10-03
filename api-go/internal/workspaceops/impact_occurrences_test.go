package workspaceops

import (
	"encoding/json"
	"slices"
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
	// a cursor pages the occurrence list, so it only travels with op=
	if _, err := ImpactFlags(ImpactBlast, map[string]string{"cursor": "x"}); !IsInvalidInput(err) {
		t.Fatalf("cursor without op: want invalid input, got %v", err)
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

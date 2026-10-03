package workspaceops

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// An impact cursor names one tier page of one ranking: it round trips, and every
// argument that selects or shapes that page changes the signature, so two calls that
// differ in any of them refuse each other's cursor.
func TestImpactCursorsAreBoundToTheirTierPage(t *testing.T) {
	q := url.Values{"symbol": {"Serve"}, "limit": {"2"}, "direction": {"upstream"}}
	c := ImpactCursor("ws", q)(4)
	if c == "" {
		t.Fatal("a positive offset signs a cursor")
	}
	if n, err := DecodeImpactCursor("ws", q, c); err != nil || n != 4 {
		t.Fatalf("round trip: %d %v", n, err)
	}
	for name, bad := range map[string]func() (int, error){
		"other symbol":    func() (int, error) { return DecodeImpactCursor("ws", with(q, "symbol", "Stop"), c) },
		"other limit":     func() (int, error) { return DecodeImpactCursor("ws", with(q, "limit", "3"), c) },
		"other direction": func() (int, error) { return DecodeImpactCursor("ws", with(q, "direction", "downstream"), c) },
		"other depth":     func() (int, error) { return DecodeImpactCursor("ws", with(q, "depth", "2"), c) },
		"other relations": func() (int, error) {
			return DecodeImpactCursor("ws", with(q, "relation_types", "CALLS"), c)
		},
		"other confidence": func() (int, error) { return DecodeImpactCursor("ws", with(q, "min_confidence", "0.5"), c) },
		"other tests":      func() (int, error) { return DecodeImpactCursor("ws", with(q, "include_tests", "true"), c) },
		"other scope":      func() (int, error) { return DecodeImpactCursor("ws", with(q, "scope", "workspace"), c) },
		"other timeout":    func() (int, error) { return DecodeImpactCursor("ws", with(q, "timeout_ms", "5000"), c) },
		"other summary":    func() (int, error) { return DecodeImpactCursor("ws", with(q, "summary_only", "true"), c) },
		"other workspace":  func() (int, error) { return DecodeImpactCursor("ws2", q, c) },
		"search cursor": func() (int, error) {
			return DecodeImpactCursor("ws", q, SearchCursor("ws", url.Values{"q": {"Serve"}})(4))
		},
		"explain cursor": func() (int, error) {
			return DecodeImpactCursor("ws", q, ExplainCursor("ws", url.Values{"path": {"svc"}})(4))
		},
		"garbage": func() (int, error) { return DecodeImpactCursor("ws", q, "nope") },
		"over-long": func() (int, error) {
			return DecodeImpactCursor("ws", q, strings.Repeat("a", maxSearchCursorLen+1))
		},
	} {
		if _, err := bad(); !IsInvalidInput(err) {
			t.Errorf("%s: want invalid input, got %v", name, err)
		}
	}
	if n, err := DecodeImpactCursor("ws", q, ""); err != nil || n != 0 {
		t.Fatalf("no cursor: %d %v", n, err)
	}
	// A client can edit a cursor's numbers or its signature but cannot re-sign one, so
	// a cursor whose offset no longer matches its signature is refused.
	if _, err := DecodeImpactCursor("ws", q, editCursorOffset(t, c, 9)); !IsInvalidInput(err) {
		t.Error("a cursor edited to a different offset: want invalid input")
	}
	// The signer never hands out a cursor for the first page: a cursor names a position
	// after the last item returned, so offset 0 signs nothing.
	if got := ImpactCursor("ws", q)(0); got != "" {
		t.Errorf("the first page signs no cursor, got %q", got)
	}
}

// editCursorOffset returns cursor with the offset it carries replaced by want, keeping
// its original signature. A client can produce exactly this, which is why the decoder
// checks the signature over the numbers rather than trusting them.
func editCursorOffset(t *testing.T, cursor string, want int) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("the cursor is base64url: %v", err)
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 3 || parts[0] != impactCursors.kind {
		t.Fatalf("the cursor carries a kind, its numbers and a signature: %q", raw)
	}
	parts[1] = strconv.Itoa(want)
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, ".")))
}

// with returns q plus one argument, so a ranking key's coverage is asserted one field
// at a time rather than by a rebuilt query that could drop a field by accident.
func with(q url.Values, name, value string) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = v
	}
	out.Set(name, value)
	return out
}

// A blast result whose tier was cut reports has_more and a cursor that continues it at
// the tier's own next_offset; a tier that returned its last item reports neither, and
// the trace and no-argument forms keep today's shape.
func TestWithImpactPagingReportsHasMoreAndNextCursor(t *testing.T) {
	cut := json.RawMessage(`{"symbol":"Serve","impacted_count":5,"by_depth":[
		{"depth":1,"total":5,"offset":0,"items":[{"name":"a"},{"name":"b"}],"next_offset":2}]}`)
	got := WithImpactPaging("ws", url.Values{"symbol": {"Serve"}}, cut)
	var res struct {
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(got, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !res.HasMore || res.NextCursor == "" {
		t.Fatalf("cut tier: has_more=%v next_cursor=%q, want true and a cursor", res.HasMore, res.NextCursor)
	}
	if n, err := DecodeImpactCursor("ws", url.Values{"symbol": {"Serve"}}, res.NextCursor); err != nil || n != 2 {
		t.Fatalf("cursor continues at the tier's next_offset: %d %v", n, err)
	}

	// The last tier item is the end of the ranking: no has_more, no cursor. The
	// cursor is checked by absence, so an implementation that always signs one fails.
	// Each case decodes into its own value: Unmarshal does not clear a field the
	// result omits, so reusing one would carry the previous case's members over.
	whole := json.RawMessage(`{"symbol":"Serve","impacted_count":2,"by_depth":[
		{"depth":1,"total":2,"offset":0,"items":[{"name":"a"},{"name":"b"}]}]}`)
	var last struct {
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	got = WithImpactPaging("ws", url.Values{"symbol": {"Serve"}}, whole)
	if err := json.Unmarshal(got, &last); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if last.HasMore || last.NextCursor != "" {
		t.Fatalf("complete tier: has_more=%v next_cursor=%q, want false and none", last.HasMore, last.NextCursor)
	}
	// A result that returned its last item under a limit still pages when a deeper
	// tier was cut: the cursor continues the tier that has a successor, not the one
	// that finished.
	mixed := json.RawMessage(`{"symbol":"Serve","by_depth":[
		{"depth":1,"total":2,"offset":0,"items":[{"name":"a"}]},
		{"depth":2,"total":9,"offset":0,"items":[{"name":"c"}],"next_offset":1}]}`)
	var deep struct {
		NextCursor string `json:"next_cursor"`
	}
	got = WithImpactPaging("ws", url.Values{"symbol": {"Serve"}}, mixed)
	if err := json.Unmarshal(got, &deep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n, err := DecodeImpactCursor("ws", url.Values{"symbol": {"Serve"}}, deep.NextCursor); err != nil || n != 1 {
		t.Fatalf("the cursor continues the cut tier: %d %v", n, err)
	}

	// A result that is not a graph answer (found=false, no by_depth) is returned as it
	// is: paging members are never invented for a form that has no tier.
	none := json.RawMessage(`{"found":false}`)
	if out := string(WithImpactPaging("ws", url.Values{"path": {"svc"}}, none)); out != string(none) {
		t.Fatalf("a result with no tiers is unchanged: %s", out)
	}

	// A malformed result is returned unchanged rather than dropped.
	broken := json.RawMessage(`{"by_depth":`)
	if out := string(WithImpactPaging("ws", url.Values{"symbol": {"Serve"}}, broken)); out != string(broken) {
		t.Fatalf("a malformed result is unchanged: %s", out)
	}
}

// A diff-scope result pages its affected walk: the members sit at the top level beside
// the changed symbols, and the cursor belongs to the scope ranking, so the same cursor
// under a different scope is refused.
func TestWithImpactPagingPagesTheAffectedTiersOfADiffScope(t *testing.T) {
	raw := json.RawMessage(`{"changed_symbols":[{"name":"Serve"}],"affected":{"impacted_count":4,"by_depth":[
		{"depth":1,"total":4,"offset":1,"items":[{"name":"c"},{"name":"d"}],"next_offset":3}]}}`)
	q := url.Values{"scope": {"workspace"}}
	var res struct {
		ChangedSymbols []struct {
			Name string `json:"name"`
		} `json:"changed_symbols"`
		Affected struct {
			ImpactedCount int `json:"impacted_count"`
		} `json:"affected"`
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(WithImpactPaging("ws", q, raw), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !res.HasMore || res.NextCursor == "" {
		t.Fatalf("affected tiers: has_more=%v next_cursor=%q, want true and a cursor", res.HasMore, res.NextCursor)
	}
	if n, err := DecodeImpactCursor("ws", q, res.NextCursor); err != nil || n != 3 {
		t.Fatalf("cursor continues at next_offset: %d %v", n, err)
	}
	// The members are added, not substituted: the changed symbols and the walk survive.
	if len(res.ChangedSymbols) != 1 || res.Affected.ImpactedCount != 4 {
		t.Errorf("the result keeps its own members: %+v", res)
	}
	if _, err := DecodeImpactCursor("ws", url.Values{"scope": {"staged"}}, res.NextCursor); !IsInvalidInput(err) {
		t.Error("another scope's ranking refuses it, want invalid input")
	}
}

// A page that carried no items gets no cursor: summary_only drops every tier's items
// while the core keeps next_offset, so a cursor there would page through a ranking the
// caller cannot read.
func TestWithImpactPagingIssuesNoCursorForAPageWithNoItems(t *testing.T) {
	summary := json.RawMessage(`{"symbol":"Serve","by_depth":[
		{"depth":1,"total":9,"offset":0,"next_offset":2}]}`) // no items: summary_only
	var res struct {
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(WithImpactPaging("ws", url.Values{"symbol": {"Serve"}}, summary), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.HasMore || res.NextCursor != "" {
		t.Fatalf("summary_only page: has_more=%v next_cursor=%q, want false and none", res.HasMore, res.NextCursor)
	}
}

// depth is the query parameter the API receives (max_depth is translated to it), so
// the ranking key binds depth and not the tool's argument name. A key reading max_depth
// would be empty on the wire and every depth would share one signature.
func TestImpactCursorBindsTheDepthTheAPIRecieves(t *testing.T) {
	q := url.Values{"symbol": {"Serve"}, "limit": {"2"}, "depth": {"3"}}
	c := ImpactCursor("ws", q)(4)
	if n, err := DecodeImpactCursor("ws", q, c); err != nil || n != 4 {
		t.Fatalf("round trip: %d %v", n, err)
	}
	if _, err := DecodeImpactCursor("ws", with(q, "depth", "5"), c); !IsInvalidInput(err) {
		t.Error("another depth's ranking refuses it, want invalid input")
	}
	// max_depth is not a query parameter the API ever receives, so the key must ignore
	// it: adding it must not change the signature, which is what proves the key is not
	// reading the tool's argument name by accident.
	if n, err := DecodeImpactCursor("ws", with(q, "max_depth", "5"), c); err != nil || n != 4 {
		t.Errorf("a max_depth the API never received changes nothing: %d %v", n, err)
	}
}

// Two successive pages cover a tier with no overlap and no gap, checked against the
// tier's own total: the row's acceptance criterion (2), which round 1 found untested.
// The page sequence is the core's own rule (offset, then limit, next_offset is the
// first item the next page returns), so the assertion does not depend on this row's
// cursor code to produce it.
func TestImpactPagesATierWithNoOverlapAndNoGap(t *testing.T) {
	const total, limit = 5, 2
	seen := map[string]bool{}
	offset := 0
	for page := 0; page < 4; page++ {
		tier := tierPage(offset, limit, total)
		names := tierItemNames(tier)
		if len(names) == 0 {
			t.Fatalf("page %d returned no items at offset %d", page, offset)
		}
		for _, n := range names {
			if seen[n] {
				t.Fatalf("page %d repeats %q: pages overlap", page, n)
			}
			seen[n] = true
		}
		next := nextOffsetOf(tier)
		if next == 0 {
			break
		}
		offset = next
	}
	if len(seen) != total {
		t.Fatalf("covered %d of %d items: a gap or a lost page", len(seen), total)
	}
}

// tierPage is the core's paging for one tier: the items from offset, at most limit of
// them, and next_offset when the page does not reach total.
func tierPage(offset, limit, total int) json.RawMessage {
	items := make([]string, 0, limit)
	for i := offset; i < min(offset+limit, total); i++ {
		items = append(items, `{"name":"i`+strconv.Itoa(i)+`"}`)
	}
	tier := `{"depth":1,"total":` + strconv.Itoa(total) + `,"offset":` + strconv.Itoa(offset) +
		`,"items":[` + strings.Join(items, ",") + `]`
	if end := offset + len(items); end < total {
		tier += `,"next_offset":` + strconv.Itoa(end)
	}
	return json.RawMessage(tier + `}`)
}

// tierItemNames reads a tier's item names; nil when the shape cannot be read.
func tierItemNames(tier json.RawMessage) []string {
	var page struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(tier, &page); err != nil {
		return nil
	}
	out := make([]string, 0, len(page.Items))
	for _, it := range page.Items {
		out = append(out, it.Name)
	}
	return out
}

// nextOffsetOf reads a tier's next_offset, 0 when it has none.
func nextOffsetOf(tier json.RawMessage) int {
	var page struct {
		NextOffset *int `json:"next_offset"`
	}
	if json.Unmarshal(tier, &page) != nil || page.NextOffset == nil {
		return 0
	}
	return *page.NextOffset
}

// A tier page carries no gap or overlap even when the tiers are of different lengths,
// which is the shape round 1 raised against one shared offset: the core skips q.offset
// and takes q.limit for every tier, so a tier short enough to end early has already
// delivered everything it holds, and advancing by the longest tier's next_offset skips
// nothing. The cursor is the largest next_offset among the tiers that have one.
func TestImpactPagesTiersOfDifferentLengthsWithoutAGap(t *testing.T) {
	// tier 1 holds 3 items, tier 2 holds 8, limit 2: both start at offset 0.
	totals := []int{3, 8}
	seen := map[int]map[string]bool{0: {}, 1: {}}
	offset := 0
	for page := 0; page < 6; page++ {
		next := 0
		for tier, total := range totals {
			pageItems := tierPage(offset, 2, total)
			for _, n := range tierItemNames(pageItems) {
				if seen[tier][n] {
					t.Fatalf("page %d tier %d repeats %q", page, tier, n)
				}
				seen[tier][n] = true
			}
			if o := nextOffsetOf(pageItems); o > next {
				next = o
			}
		}
		if next == 0 {
			break
		}
		offset = next
	}
	for tier, total := range totals {
		if len(seen[tier]) != total {
			t.Fatalf("tier %d covered %d of %d items", tier, len(seen[tier]), total)
		}
	}
}

// A page the output budget cut is re-signed: the cursor continues at the first item the
// caller did not receive, so following it skips nothing and needs no hand-computed
// offset. When a rung removed the tiers outright there is nothing to sign, so the cursor
// goes and has_more stands.
func TestABudgetTrimmedPageIsResignedToWhatWasDelivered(t *testing.T) {
	q := url.Values{"symbol": {"Serve"}}
	// The core returned items 0-1 of 5 at offset 0 and the budget delivered one.
	trimmed := []byte(`{"symbol":"Serve","has_more":true,"next_cursor":"i2.2.abcdef0123456789",` +
		`"by_depth":[{"depth":1,"total":5,"offset":0,"items":[{"name":"i0"}],"next_offset":2}],` +
		`"output_budget":{"max_chars":2000,"used_chars":1990,"degraded_stage":"tiers_trimmed","stages":["tiers_trimmed"]}}`)
	res := decodeMembers(t, ReconcileImpactPaging("ws", q, trimmed))
	if got := string(res["has_more"]); got != "true" {
		t.Errorf("has_more = %s, want true", got)
	}
	// The cursor continues after the one delivered item, not at the core's next_offset
	// of 2, which would skip item 1. It arrives as a JSON string, so it is decoded before
	// it goes back through the decoder.
	var cursor string
	if err := json.Unmarshal(res["next_cursor"], &cursor); err != nil {
		t.Fatalf("the re-signed cursor is a JSON string: %v", err)
	}
	if n, err := DecodeImpactCursor("ws", q, cursor); err != nil || n != 1 {
		t.Errorf("the re-signed cursor continues at %d (%v), want 1", n, err)
	}
	if _, ok := res["output_budget"]; !ok {
		t.Error("the budget report is dropped, so the trim becomes silent")
	}

	// A rung that removed the tiers leaves nothing to count or sign: has_more stands and
	// no cursor is issued, which is the codex-spec:3 case.
	removed := []byte(`{"symbol":"Serve","by_depth":[{"depth":1,"total":5,"offset":2,"items":[{"name":"i2"}]}],` +
		`"output_budget":{"max_chars":2000,"used_chars":900,"degraded_stage":"tiers_total","stages":["tiers_total"]}}`)
	removed = stripMember(t, removed, "by_depth")
	res = decodeMembers(t, ReconcileImpactPaging("ws", q, removed))
	if got := string(res["has_more"]); got != "true" {
		t.Errorf("a result whose tiers were removed reports has_more = %s, want true", got)
	}
	if _, ok := res["next_cursor"]; ok {
		t.Error("a result with no tiers left signs no cursor")
	}

	// An untrimmed result is untouched: the route calls this on every impact answer,
	// so the common case must survive it.
	whole := []byte(`{"symbol":"Serve","has_more":true,"next_cursor":"i2.2.abcdef0123456789",` +
		`"by_depth":[{"depth":1,"total":5,"offset":0,"items":[{"name":"i0"},{"name":"i1"}],"next_offset":2}]}`)
	if got := string(ReconcileImpactPaging("ws", q, whole)); got != string(whole) {
		t.Errorf("an untrimmed result is returned unchanged: %s", got)
	}
}

// stripMember removes one member from a JSON object, so a fixture can stand for a budget
// rung that dropped the tier list.
func stripMember(t *testing.T, out []byte, name string) []byte {
	t.Helper()
	res := decodeMembers(t, out)
	delete(res, name)
	stripped, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return stripped
}

// decodeMembers unmarshals a JSON object into its members, failing the test on a shape
// it cannot read.
func decodeMembers(t *testing.T, out []byte) map[string]json.RawMessage {
	t.Helper()
	var res map[string]json.RawMessage
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	return res
}

// An absent cursor is the first page; one that is present but empty is refused, so a
// malformed cursor never silently answers with the wrong page. The same holds for
// offset= alongside a cursor: an empty offset names no position, and letting the cursor
// supply one would answer a request the caller wrote twice.
func TestAnEmptyCursorIsRefusedAndAnAbsentOneIsNot(t *testing.T) {
	if err := ResolveImpactCursor("ws", url.Values{"symbol": {"Serve"}}, map[string]string{}); err != nil {
		t.Fatalf("an absent cursor is the first page: %v", err)
	}
	given := url.Values{"symbol": {"Serve"}, "cursor": {""}}
	if err := ResolveImpactCursor("ws", given, map[string]string{"cursor": ""}); !IsInvalidInput(err) {
		t.Error("an explicitly empty cursor: want invalid input")
	}
	q := url.Values{"symbol": {"Serve"}}
	c := ImpactCursor("ws", q)(4)
	bothEmpty := url.Values{"symbol": {"Serve"}, "cursor": {c}, "offset": {""}}
	if err := ResolveImpactCursor("ws", bothEmpty, map[string]string{"cursor": c, "offset": ""}); !IsInvalidInput(err) {
		t.Error("a cursor with an explicitly empty offset: want invalid input")
	}
	// offset= on its own is still the caller's page position.
	offsetOnly := url.Values{"symbol": {"Serve"}, "offset": {"3"}}
	if err := ResolveImpactCursor("ws", offsetOnly, map[string]string{"offset": "3"}); err != nil {
		t.Errorf("offset alone names the page: %v", err)
	}
}

// cursor= and offset= name the same page position, so giving both is refused rather
// than one silently winning; cursor= alone becomes the offset the core takes, and
// offset= alone keeps working.
func TestImpactCursorAndOffsetAreRefusedTogether(t *testing.T) {
	q := url.Values{"symbol": {"Serve"}}
	// The cursor is resolved before the form check, so the refusal is asserted through
	// the resolver every impact request goes through.
	params := map[string]string{"cursor": ImpactCursor("ws", q)(4), "offset": "2"}
	if err := ResolveImpactCursor("ws", q, params); !IsInvalidInput(err) {
		t.Fatalf("cursor and offset together: want invalid input, got %v", err)
	}
	// A cursor with no offset becomes that offset, and is not left for the core as a
	// second page position.
	params = map[string]string{"cursor": ImpactCursor("ws", q)(4)}
	if err := ResolveImpactCursor("ws", q, params); err != nil {
		t.Fatalf("cursor alone: %v", err)
	}
	if params["offset"] != "4" || params["cursor"] != "" {
		t.Fatalf("cursor resolves to the offset it names: %v", params)
	}
	flags, err := ImpactFlags(ImpactBlast, params)
	if err != nil {
		t.Fatalf("the resolved offset reaches the core: %v", err)
	}
	if !slices.Contains(flags, "--offset=4") {
		t.Errorf("the core takes one page position: %v", flags)
	}
	// A cursor signed for another ranking is refused before it can move the page.
	if err := ResolveImpactCursor("ws", url.Values{"symbol": {"Other"}}, map[string]string{
		"cursor": ImpactCursor("ws", q)(4),
	}); !IsInvalidInput(err) {
		t.Error("another ranking's cursor: want invalid input")
	}
}

// The trace and file forms take no tier page, so a cursor is refused there rather than
// parsed and ignored (an argument that parses and does nothing is a dead knob, A7).
func TestImpactTraceAndFileFormsRefuseACursor(t *testing.T) {
	for name, params := range map[string]map[string]string{
		"trace": {"from": "A", "to": "B", "cursor": "i2.4.deadbeef"},
		"file":  {"path": "svc.go", "cursor": "i2.4.deadbeef"},
	} {
		if _, _, err := CheckImpactRequest(params); !IsInvalidInput(err) {
			t.Errorf("%s: want invalid input, got %v", name, err)
		}
	}
	// The blast and diff-scope forms are the ones that page, so they accept one.
	for name, params := range map[string]map[string]string{
		"blast": {"symbol": "Serve", "cursor": "i2.4.deadbeef"},
		"scope": {"scope": "workspace", "cursor": "i2.4.deadbeef"},
	} {
		if _, _, err := CheckImpactRequest(params); err != nil {
			t.Errorf("%s: the form accepts a cursor argument, got %v", name, err)
		}
	}
}

// countArray counts elements without materialising them, so its edge cases are the
// counts it must get right: an empty array, brackets and commas inside strings and
// inside nested arrays (an item holds both), a missing or non-array member, and several
// tiers in one page.
func TestTierItemCountsAreReadWithoutDecodingTheItems(t *testing.T) {
	for name, tc := range map[string]struct {
		tiers string
		want  []int
	}{
		"one tier": {
			tiers: `[{"depth":1,"total":3,"offset":0,"items":[{"name":"a"},{"name":"b"}],"next_offset":2}]`,
			want:  []int{2},
		},
		"empty items": {
			tiers: `[{"depth":1,"total":0,"offset":0,"items":[]}]`,
			want:  []int{0},
		},
		"absent items": {
			tiers: `[{"depth":1,"total":3,"offset":0,"next_offset":3}]`,
			want:  []int{0},
		},
		"brackets and commas inside an item's strings": {
			tiers: `[{"items":[{"path":"a[0],b"},{"name":"x, y"},{"name":"q\"quoted, and [bracketed]"},{"name":"esc\\\\"}],"total":4}]`,
			want:  []int{4},
		},
		"nested arrays inside an item": {
			tiers: `[{"items":[{"refs":[1,2,3],"total":3},{"refs":[]}],"total":2}]`,
			want:  []int{2},
		},
		"several tiers": {
			tiers: `[{"items":[{"n":1},{"n":2}],"total":2},{"items":[{"n":1}],"total":1}]`,
			want:  []int{2, 1},
		},
		"items is not an array": {
			tiers: `[{"items":5,"total":1}]`,
			want:  []int{0},
		},
		// A tier carries user-controlled text, and that text can hold the member's own
		// name and an array. Counting the first match instead of the top-level one would
		// count what a symbol name or a label said.
		"a name holding the member name and an array": {
			tiers: `[{"label":"\"items\":[99,98,97,96]","depth":1,"total":4,"offset":0,"items":[{"name":"a"},{"name":"b"}],"next_offset":2}]`,
			want:  []int{2},
		},
		"a nested object holding the member name": {
			tiers: `[{"meta":{"items":[1,2,3,4,5]},"depth":1,"total":2,"offset":0,"items":[{"name":"a"}]}]`,
			want:  []int{1},
		},
	} {
		var page []tierReading
		if err := json.Unmarshal([]byte(tc.tiers), &page); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(page) != len(tc.want) {
			t.Errorf("%s: %d tiers, want %d", name, len(page), len(tc.want))
			continue
		}
		for i, tier := range page {
			if tier.items != tc.want[i] {
				t.Errorf("%s: tier %d counted %d items, want %d", name, i, tier.items, tc.want[i])
			}
		}
	}
}

// A tier at a non-zero offset can have delivered everything it holds even though the
// page carried fewer items than the tier's total. Such a tier is finished and must not
// bind the cursor: if it did, its stale position would hold the cursor behind a tier
// that still has items, and the next page would repeat what this one already sent.
func TestAFinishedTierAtANonZeroOffsetDoesNotBindTheCursor(t *testing.T) {
	// Tier one is finished: offset 3 plus two items is its whole total of 5. Tier two
	// delivered through position 6 and still holds more.
	res := map[string]json.RawMessage{"by_depth": json.RawMessage(`[{"depth":1,"offset":3,"total":5,"items":[{"n":4},{"n":5}]},{"depth":2,"offset":4,"total":9,"items":[{"n":5},{"n":6}],"next_offset":6}]`)}
	next, ok := deliveredTierOffset(res)
	if !ok {
		t.Fatal("a tier list survived, so the offset is readable")
	}
	// Tier two binds at 6; the finished tier must not pull the cursor back to 5.
	if next != 6 {
		t.Errorf("cursor continues at %d, want 6: a finished tier bound it", next)
	}
	// The same result read through the core's own next_offset path.
	if got := nextTierOffset(res["by_depth"]); got != 6 {
		t.Errorf("nextTierOffset %d, want 6", got)
	}
}

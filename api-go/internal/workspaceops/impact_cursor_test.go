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
		"other depth":     func() (int, error) { return DecodeImpactCursor("ws", with(q, "max_depth", "2"), c) },
		"other relations": func() (int, error) {
			return DecodeImpactCursor("ws", with(q, "relation_types", "CALLS"), c)
		},
		"other confidence": func() (int, error) { return DecodeImpactCursor("ws", with(q, "min_confidence", "0.5"), c) },
		"other tests":      func() (int, error) { return DecodeImpactCursor("ws", with(q, "include_tests", "true"), c) },
		"other scope":      func() (int, error) { return DecodeImpactCursor("ws", with(q, "scope", "workspace"), c) },
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

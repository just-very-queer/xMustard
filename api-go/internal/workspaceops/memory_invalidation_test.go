package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/govstore"
)

// WS-28: change-driven invalidation, as_of, graph-proximity recall and memories on
// explain and impact.

const srvSource = "package server\n\nfunc ListenPort() int {\n\treturn 8081\n}\n\nfunc Other() int {\n\treturn 1\n}\n"

// stubOutline reads the top-level funcs of a Go file the way the code index would: a name
// and the lines from its func line to the closing brace.
func stubOutline(root string) func(context.Context, string, string, string) (symbolSpans, error) {
	return func(_ context.Context, _, _, rel string) (symbolSpans, error) {
		text, ok := readWorkspaceText(root, rel, 1<<20)
		if !ok {
			return nil, fmt.Errorf("%s is not readable", rel)
		}
		var out symbolSpans
		lines := strings.Split(text, "\n")
		for i, l := range lines {
			if name, ok := strings.CutPrefix(l, "func "); ok {
				name, _, _ = strings.Cut(name, "(")
				end := i + 1
				for end < len(lines) && lines[end] != "}" {
					end++
				}
				out = append(out, symbolSpan{Name: name, Start: i + 1, End: end + 1})
			}
		}
		return out, nil
	}
}

type invalidationFixture struct {
	dir, ws, root string
	git           func(args ...string) string
	memory        *ContextEntry
}

// newInvalidationFixture registers a git workspace whose srv.go declares ListenPort and
// Other, and promotes a memory that names `ListenPort` (a symbol anchor, no declared
// path) with a body-hash baseline.
func newInvalidationFixture(t *testing.T) invalidationFixture {
	t.Helper()
	dir, ws, root, git := reviewRepo(t)
	writeRepoFile(t, root, "srv.go", srvSource)
	git("add", ".")
	git("commit", "-q", "-m", "srv")
	origResolve, origOutline := resolveSymbols, symbolOutline
	t.Cleanup(func() { resolveSymbols, symbolOutline = origResolve, origOutline })
	resolveSymbols = func(_ context.Context, _, _ string, names []string) ([]symbolMatch, error) {
		var out []symbolMatch
		for _, n := range names {
			out = append(out, symbolMatch{Name: n, Count: 1, Definitions: []symbolDefinition{{Path: "srv.go", Line: 3}}})
		}
		return out, nil
	}
	symbolOutline = stubOutline(root)
	e := approvedBy(t, dir, ws, "author", "approver", "agent", "`ListenPort` returns the service port 8081.")
	return invalidationFixture{dir: dir, ws: ws, root: root, git: git, memory: e}
}

func (f invalidationFixture) anchors(t *testing.T) []govstore.Anchor {
	t.Helper()
	var out []govstore.Anchor
	err := memoryView(context.Background(), f.dir, f.ws, func(r govstore.Reader) (err error) {
		out, err = r.ListAnchors(context.Background(), f.memory.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f invalidationFixture) entry(t *testing.T) govstore.Entry {
	t.Helper()
	var e govstore.Entry
	err := memoryView(context.Background(), f.dir, f.ws, func(r govstore.Reader) (err error) {
		e, err = r.GetEntry(context.Background(), f.memory.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// An edit to an anchored symbol's body marks the memory at risk; an unrelated edit in the
// same file does not. The stale_observed event carries the commit and the anchors.
func TestSymbolBodyEditMarksMemoryAtRisk(t *testing.T) {
	f := newInvalidationFixture(t)
	var symbol govstore.Anchor
	for _, a := range f.anchors(t) {
		if a.Kind == govstore.AnchorSymbol {
			symbol = a
		}
	}
	if symbol.SymbolUID != "srv.go#ListenPort" || symbol.BaselineState != govstore.BaselineHash || symbol.BaselineKind != baselineSymbolBody {
		t.Fatalf("the promotion did not baseline the symbol body: %+v", symbol)
	}
	ctx := context.Background()

	writeRepoFile(t, f.root, "srv.go", strings.Replace(srvSource, "return 1", "return 2", 1)) // Other, not ListenPort
	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil || len(rep.Marked) != 0 || rep.Checked != 1 {
		t.Fatalf("an unrelated edit: %+v, %v", rep, err)
	}
	if f.entry(t).NeedsReverify {
		t.Fatal("an edit outside the anchored symbol marked the memory")
	}

	writeRepoFile(t, f.root, "srv.go", strings.Replace(srvSource, "return 8081", "return 9090", 1))
	head := f.git("rev-parse", "HEAD")
	rep, err = InvalidateMemory(ctx, f.dir, f.ws, []string{"./srv.go", "../escape.go"})
	if err != nil || !slices.Equal(rep.Marked, []string{f.memory.ID}) || rep.Paths != 1 {
		t.Fatalf("an edit to the anchored body: %+v, %v", rep, err)
	}
	if e := f.entry(t); !e.NeedsReverify || e.StaleSince == "" {
		t.Fatalf("entry not at risk: %+v", e)
	}

	var events []govstore.Event
	if err := memoryView(ctx, f.dir, f.ws, func(r govstore.Reader) (err error) {
		events, err = r.ListEvents(ctx, govstore.EventFilter{WorkspaceID: f.ws, EntryID: f.memory.ID, Types: []string{govstore.EventStaleObserved}})
		return err
	}); err != nil || len(events) != 1 {
		t.Fatalf("stale_observed events %v, %v", events, err)
	}
	var data struct {
		Commit  string
		Anchors []govstore.AnchorRef
	}
	if err := json.Unmarshal(events[0].Data, &data); err != nil || data.Commit != head ||
		!slices.Equal(data.Anchors, []govstore.AnchorRef{{Kind: govstore.AnchorSymbol, Value: "ListenPort"}}) {
		t.Fatalf("event data %s (want commit %s): %v", events[0].Data, head, err)
	}

	// once marked, a second look records nothing more
	if rep, _ = InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"}); len(rep.Marked) != 0 {
		t.Fatalf("marked again: %+v", rep)
	}
}

// A symbol that no longer exists is drift; an outline the index cannot give is unknown,
// and unknown is never stale.
func TestUnknownOutlineIsNotStale(t *testing.T) {
	f := newInvalidationFixture(t)
	symbolOutline = func(context.Context, string, string, string) (symbolSpans, error) {
		return nil, fmt.Errorf("index busy")
	}
	writeRepoFile(t, f.root, "srv.go", "package server\n")
	rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"})
	if err != nil || len(rep.Marked) != 0 || f.entry(t).NeedsReverify {
		t.Fatalf("an unknown outline marked the memory: %+v, %v", rep, err)
	}
	symbolOutline = stubOutline(f.root)
	if rep, _ = InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"}); len(rep.Marked) != 1 {
		t.Fatalf("a removed symbol is drift: %+v", rep)
	}
}

// ground lists the memories the caller's changes reached, before they act: its working
// changes are looked up in the anchor index and the memory is marked and listed.
func TestGroundListsAtRiskMemories(t *testing.T) {
	f := newInvalidationFixture(t)
	orig := groundChangedFiles
	t.Cleanup(func() { groundChangedFiles = orig })
	groundChangedFiles = func(context.Context, string, string) ([]string, error) { return []string{"srv.go"}, nil }

	scan, err := scanGroundMemory(f.dir, f.ws, "caller", groundStaleWindow, noSince, groundMemoryOptions{invalidate: true})
	if err != nil || len(scan.atRisk) != 0 {
		t.Fatalf("nothing changed yet: %+v, %v", scan.atRisk, err)
	}
	writeRepoFile(t, f.root, "srv.go", strings.Replace(srvSource, "8081", "8082", 1))
	scan, err = scanGroundMemory(f.dir, f.ws, "caller", groundStaleWindow, noSince, groundMemoryOptions{invalidate: true})
	if err != nil || len(scan.atRisk) != 1 || scan.invalidationErr != nil {
		t.Fatalf("at risk %+v, %v, %v", scan.atRisk, err, scan.invalidationErr)
	}
	r := scan.atRisk[0]
	if r.ID != f.memory.ID || r.Since == "" || r.SinceCommit != f.git("rev-parse", "HEAD") || r.Trust != VerificationPeer ||
		!slices.Equal(r.Anchors, []string{"symbol:ListenPort"}) || r.Title == "" {
		t.Fatalf("at-risk memory %+v", r)
	}

	// recall labels it stale, with the persisted anchors, though it declares no path
	res, err := RecallWith(context.Background(), f.dir, f.ws, RecallRequest{Query: "service port", Explain: true})
	if err != nil {
		t.Fatal(err)
	}
	entries := res["entries"].([]ContextEntry)
	if len(entries) != 1 || !entries[0].Stale || !entries[0].NeedsReverify || entries[0].StaleSince == "" ||
		!slices.Equal(entries[0].StaleAnchors, []string{"symbol:ListenPort"}) || entries[0].ScoreDetails.StalePenalty >= 0 {
		t.Fatalf("recall of an at-risk memory: %+v", entries)
	}
}

// A distinct principal's approval re-baselines an at-risk memory and clears it; the
// author's own approval does not.
func TestReverificationClearsDrift(t *testing.T) {
	f := newInvalidationFixture(t)
	writeRepoFile(t, f.root, "srv.go", strings.Replace(srvSource, "8081", "8083", 1))
	if rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"}); err != nil || len(rep.Marked) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, err := VerifyContext(f.dir, f.ws, f.memory.ID, "author", true, ""); err != nil {
		t.Fatal(err)
	}
	if !f.entry(t).NeedsReverify {
		t.Fatal("the author's own approval cleared the drift")
	}
	e, err := VerifyContext(f.dir, f.ws, f.memory.ID, "peer-9", true, "checked against the new body")
	if err != nil || e.NeedsReverify {
		t.Fatalf("a distinct approval: %+v, %v", e, err)
	}
	if got := f.entry(t); got.NeedsReverify || got.StaleSince != "" {
		t.Fatalf("still at risk: %+v", got)
	}
	for _, a := range f.anchors(t) {
		if a.StaleSince != "" {
			t.Fatalf("anchor still stale: %+v", a)
		}
	}
	// the new body is the baseline: looking again finds nothing, and a further edit does
	if rep, _ := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"}); len(rep.Marked) != 0 {
		t.Fatalf("re-baselined memory marked again: %+v", rep)
	}
	writeRepoFile(t, f.root, "srv.go", strings.Replace(srvSource, "8081", "8084", 1))
	if rep, _ := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"}); len(rep.Marked) != 1 {
		t.Fatalf("a later edit: %+v", rep)
	}
}

// A HEAD move is looked up too: the paths between the HEAD ground last saw and HEAD now.
func TestHeadMovePathsAreInvalidated(t *testing.T) {
	f := newInvalidationFixture(t)
	if got := headMovedPaths(context.Background(), f.dir, f.ws, f.root); len(got) != 0 {
		t.Fatalf("the first look sees no move: %v", got)
	}
	writeRepoFile(t, f.root, "srv.go", strings.Replace(srvSource, "8081", "8085", 1))
	f.git("commit", "-aqm", "port")
	moved := headMovedPaths(context.Background(), f.dir, f.ws, f.root)
	if !slices.Equal(moved, []string{"srv.go"}) {
		t.Fatalf("moved paths %v", moved)
	}
	if rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, moved); err != nil || len(rep.Marked) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
}

// as_of at an older commit returns the facts valid then; the ancestry check is cached.
func TestRecallAsOfCommitAndTime(t *testing.T) {
	f := newInvalidationFixture(t)
	c1 := f.git("rev-parse", "HEAD")
	time.Sleep(20 * time.Millisecond)
	before := time.Now().UTC().Format(time.RFC3339Nano)
	time.Sleep(20 * time.Millisecond)
	writeRepoFile(t, f.root, "notes.txt", "second\n")
	f.git("add", ".")
	f.git("commit", "-qm", "second")
	c2 := f.git("rev-parse", "HEAD")

	req := ProposeContextRequest{Title: "port", Content: "The service port moved to 9000.", Permission: "readwrite", Supersedes: []string{f.memory.ID}}
	b, err := Remember(f.dir, f.ws, RememberRequest{ProposeContextRequest: req}, ContextActor{ID: "author"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyContext(f.dir, f.ws, b.ID, "peer-1", true, ""); err != nil {
		t.Fatal(err)
	}
	if b, err = VerifyContextOutcome(f.dir, f.ws, b.ID, ContextActor{ID: "approver", Kind: "agent", Verifier: true}, VerifyRequest{}); err != nil || !b.Promoted {
		t.Fatalf("%+v %v", b, err)
	}
	var stored govstore.Entry
	if err := memoryView(context.Background(), f.dir, f.ws, func(r govstore.Reader) (err error) {
		stored, err = r.GetEntry(context.Background(), b.ID)
		return err
	}); err != nil || stored.ValidFromCommit != c2 || stored.ValidFrom == "" {
		t.Fatalf("the newer memory is valid from %q at %q, want %s: %v", stored.ValidFromCommit, stored.ValidFrom, c2, err)
	}

	ids := func(asOf string, valid bool) []string {
		t.Helper()
		res, err := RecallWith(context.Background(), f.dir, f.ws, RecallRequest{Query: "service port", AsOf: asOf, ValidOnly: valid, Render: "compact"})
		if err != nil {
			t.Fatalf("as_of %q: %v", asOf, err)
		}
		var out []string
		for _, l := range res["entries"].([]RecallLine) {
			out = append(out, l.ID)
		}
		slices.Sort(out)
		return out
	}
	old, cur := f.memory.ID, b.ID
	for _, c := range []struct {
		asOf  string
		valid bool
		want  []string
	}{
		{"", false, []string{cur}},
		{c1, false, []string{old}}, // superseded since, but valid then
		{c2, false, []string{cur}}, // the supersession is at c2
		{c1[:10], false, []string{old}},
		{before, false, []string{old}},
		{time.Now().UTC().Add(time.Hour).Format(time.RFC3339), false, []string{cur}},
		{c1, true, nil}, // valid then, no longer valid now
		{time.Now().UTC().Add(time.Hour).Format(time.RFC3339), true, []string{cur}},
		{time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339), false, nil}, // before either existed
	} {
		if got := ids(c.asOf, c.valid); !slices.Equal(got, c.want) {
			t.Errorf("as_of %q valid_only=%v: %v, want %v", c.asOf, c.valid, got, c.want)
		}
	}
	if _, err := RecallWith(context.Background(), f.dir, f.ws, RecallRequest{Query: "x", AsOf: "not-a-commit"}); !IsInvalidInput(err) {
		t.Fatalf("a bad as_of: %v", err)
	}

	// the ancestry answers are cached: commits never change
	ancestryCache.Lock()
	cached := len(ancestryCache.known)
	ancestryCache.Unlock()
	if cached == 0 {
		t.Fatal("no ancestry answer was cached")
	}
	if ok, err := isAncestor(context.Background(), f.root, c1, c2); err != nil || !ok {
		t.Fatalf("c1 is an ancestor of c2: %v %v", ok, err)
	}
	if ok, _ := isAncestor(context.Background(), f.root, c2, c1); ok {
		t.Fatal("c2 is not an ancestor of c1")
	}
}

// seed ranks by graph distance from a symbol, weighted 1/(1+d), and drops memory outside
// the neighbourhood.
func TestRecallSeedRanksByGraphDistance(t *testing.T) {
	dir, ws, root, _ := reviewRepo(t)
	_ = root
	orig, origResolve := seedNeighbours, resolveSymbols
	t.Cleanup(func() { seedNeighbours, resolveSymbols = orig, origResolve })
	resolveSymbols = func(context.Context, string, string, []string) ([]symbolMatch, error) { return nil, nil }
	var walked []string
	seedNeighbours = func(_ context.Context, _, _, seed string) (seedHood, error) {
		walked = append(walked, seed)
		return seedHood{Found: true, Files: map[string]int{"p/a.go": 0, "q/b.go": 1, "r/c.go": 3},
			Symbols: map[string]int{"p/a.go#A": 0}}, nil
	}
	ids := map[string]string{}
	for path, text := range map[string]string{"r/c.go": "far away note", "q/b.go": "one hop note", "p/a.go": "on the seed note", "elsewhere.go": "unrelated note"} {
		e := approvedBy(t, dir, ws, "author", "approver", "agent", text+" for "+path)
		// approvedBy declares no paths: attach one through the store as an anchor set
		storeUpdate(t, dir, ws, func(tx govstore.Tx) error {
			return tx.ReplaceAnchors(context.Background(), e.ID, []string{path}, nil, govstore.Actor{Principal: "test"})
		})
		ids[path] = e.ID
	}
	res, err := RecallWith(context.Background(), dir, ws, RecallRequest{Seed: "A", Explain: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	entries := res["entries"].([]ContextEntry)
	var order []string
	for _, e := range entries {
		order = append(order, e.ID)
		if e.ScoreDetails.GraphDistance == nil {
			t.Fatalf("no graph distance: %+v", e.ScoreDetails)
		}
	}
	want := []string{ids["p/a.go"], ids["q/b.go"], ids["r/c.go"]}
	if !slices.Equal(order, want) {
		t.Fatalf("order %v, want %v (walked %v)", order, want, walked)
	}
	for i, hops := range []int{0, 1, 3} {
		if d := entries[i].ScoreDetails; *d.GraphDistance != hops || d.Graph != round3(1/float64(1+hops)) {
			t.Fatalf("entry %d: %+v", i, d)
		}
	}
	if seed := res["seed"].(map[string]any); seed["found"] != true || seed["memories_near"] != 3 {
		t.Fatalf("seed report %v", seed)
	}
	// a different seed is a different ranking: its cursor cannot continue this one
	if _, err := RecallWith(context.Background(), dir, ws, RecallRequest{Seed: "B", Cursor: "x"}); !IsInvalidInput(err) {
		t.Fatalf("a foreign cursor: %v", err)
	}
}

// The diversity pass reorders at most seedCandidates entries and never builds a pairwise
// matrix: a repeated topic pays for the pick, what lies past the window keeps its place.
func TestDiversifyIsBoundedMMR(t *testing.T) {
	mk := func(id, topic string, total float64) scoredEntry {
		return scoredEntry{rank: govstore.RankEntry{ID: id, Topic: topic}, details: ScoreDetails{Total: total}}
	}
	ranked := []scoredEntry{mk("a1", "a", 1.0), mk("a2", "a", 0.95), mk("a3", "a", 0.9), mk("b1", "b", 0.85)}
	diversify(ranked)
	var got []string
	for _, s := range ranked {
		got = append(got, s.rank.ID)
	}
	if want := []string{"a1", "b1", "a2", "a3"}; !slices.Equal(got, want) {
		t.Fatalf("diversified %v, want %v", got, want)
	}
	long := make([]scoredEntry, 250)
	for i := range long {
		long[i] = mk(fmt.Sprintf("e%03d", i), fmt.Sprintf("t%d", i%3), 5-float64(i)/100)
	}
	diversify(long)
	for i := seedCandidates; i < len(long); i++ {
		if long[i].rank.ID != fmt.Sprintf("e%03d", i) {
			t.Fatalf("position %d past the window moved to %s", i, long[i].rank.ID)
		}
	}
	seen := map[string]bool{}
	for _, s := range long[:seedCandidates] {
		if seen[s.rank.ID] {
			t.Fatalf("%s picked twice", s.rank.ID)
		}
		seen[s.rank.ID] = true
	}
}

// explain and impact carry the memories anchored to what they describe, with trust and
// stale labels.
func TestImpactAndExplainCarryMemories(t *testing.T) {
	f := newInvalidationFixture(t)
	writeRepoFile(t, f.root, "srv.go", strings.Replace(srvSource, "8081", "8086", 1))
	if rep, _ := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"}); len(rep.Marked) != 1 {
		t.Fatalf("%+v", rep)
	}
	storeUpdate(t, f.dir, f.ws, func(tx govstore.Tx) error {
		return tx.ReplaceAnchors(context.Background(), f.memory.ID, []string{"srv.go"}, []govstore.AnchorInput{
			{Kind: govstore.AnchorSymbol, Value: "ListenPort", SymbolUID: "srv.go#ListenPort", Extracted: true}}, govstore.Actor{Principal: "test"})
	})
	raw := json.RawMessage(`{"symbol":"ListenPort","defined_in":["srv.go"],"impacted_count":1,"by_depth":[{"depth":1,"items":[{"path":"srv.go"}]}]}`)
	out := WithImpactMemories(context.Background(), f.dir, f.ws, "reader", "ListenPort", nil, raw)
	var res struct {
		Symbol   string       `json:"symbol"`
		Count    int          `json:"impacted_count"`
		Memories []RecallLine `json:"memories"`
	}
	if err := json.Unmarshal(out, &res); err != nil || res.Symbol != "ListenPort" || res.Count != 1 || len(res.Memories) != 1 {
		t.Fatalf("impact %s: %v", out, err)
	}
	if m := res.Memories[0]; m.ID != f.memory.ID || m.Trust != VerificationPeer || m.State != govstore.RankServed || m.Text == "" || !m.Stale {
		t.Fatalf("attached memory %+v", m)
	}
	f2 := WithImpactMemories(context.Background(), f.dir, f.ws, "reader", "", []string{"srv.go"}, json.RawMessage(`{"found":true}`))
	if !strings.Contains(string(f2), `"memories"`) {
		t.Fatalf("path impact %s", f2)
	}
	if got := WithImpactMemories(context.Background(), f.dir, f.ws, "reader", "", nil, json.RawMessage(`[1]`)); string(got) != `[1]` {
		t.Fatalf("a result that is not an object changed: %s", got)
	}
}

// The diversified order is what recall returns: the per-block re-sort keeps it.
func TestRecallSeedPageIsDiversified(t *testing.T) {
	dir, ws, _, _ := reviewRepo(t)
	orig, origResolve := seedNeighbours, resolveSymbols
	t.Cleanup(func() { seedNeighbours, resolveSymbols = orig, origResolve })
	resolveSymbols = func(context.Context, string, string, []string) ([]symbolMatch, error) { return nil, nil }
	files := []string{"x/a.go", "x/b.go", "x/c.go", "y/d.go"}
	hood := seedHood{Found: true, Files: map[string]int{}}
	ids := map[string]string{}
	for _, path := range files {
		hood.Files[path] = 0
		e := approvedBy(t, dir, ws, "author", "approver", "agent", "note for "+path)
		storeUpdate(t, dir, ws, func(tx govstore.Tx) error {
			return tx.ReplaceAnchors(context.Background(), e.ID, []string{path}, nil, govstore.Actor{Principal: "test"})
		})
		ids[e.ID] = path
	}
	seedNeighbours = func(context.Context, string, string, string) (seedHood, error) { return hood, nil }
	res, err := RecallWith(context.Background(), dir, ws, RecallRequest{Seed: "A", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	entries := res["entries"].([]ContextEntry)
	if len(entries) != 4 {
		t.Fatalf("%d entries", len(entries))
	}
	pos := slices.IndexFunc(entries, func(e ContextEntry) bool { return ids[e.ID] == "y/d.go" })
	if pos == 3 {
		t.Fatalf("the lone directory came last: the diversity order was lost")
	}
}

// An outline the index cut short cannot say a symbol is gone: unknown, never stale. A
// mark leaves updated_at alone.
func TestTruncatedOutlineIsNotStaleAndMarkKeepsUpdatedAt(t *testing.T) {
	f := newInvalidationFixture(t)
	full := stubOutline(f.root)
	symbolOutline = func(ctx context.Context, a, b, rel string) (symbolSpans, error) {
		_, err := full(ctx, a, b, rel)
		if err != nil {
			return nil, err
		}
		return nil, errOutlineIncomplete // the symbol is not among those kept
	}
	writeRepoFile(t, f.root, "srv.go", "package server\n")
	if rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"}); err != nil || len(rep.Marked) != 0 {
		t.Fatalf("a truncated outline marked the memory: %+v, %v", rep, err)
	}
	before := f.entry(t).UpdatedAt
	symbolOutline = full
	if rep, _ := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"}); len(rep.Marked) != 1 {
		t.Fatalf("%+v", rep)
	}
	if after := f.entry(t).UpdatedAt; after != before {
		t.Fatalf("marking stale moved updated_at %s -> %s", before, after)
	}
}

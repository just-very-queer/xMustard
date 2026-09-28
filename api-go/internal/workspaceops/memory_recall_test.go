package workspaceops

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/govstore"
)

// singleAgentDir promotes every proposal on its author's word.
func singleAgentDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	off := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &off})
	return dir
}

func propose(t *testing.T, dir, ws, author, title, content string, extra ...func(*ProposeContextRequest)) *ContextEntry {
	t.Helper()
	req := RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: title, Content: content, Permission: "readwrite"}}
	for _, f := range extra {
		f(&req.ProposeContextRequest)
	}
	e, err := Remember(dir, ws, req, ContextActor{ID: author})
	if err != nil {
		t.Fatalf("propose %q: %v", title, err)
	}
	return e
}

func recallWith(t *testing.T, dir, ws string, req RecallRequest) map[string]any {
	t.Helper()
	res, err := RecallWith(context.Background(), dir, ws, req)
	if err != nil {
		t.Fatalf("recall %+v: %v", req, err)
	}
	return res
}

func ids(res map[string]any) []string {
	var out []string
	switch es := res["entries"].(type) {
	case []ContextEntry:
		for _, e := range es {
			out = append(out, e.ID)
		}
	case []RecallLine:
		for _, e := range es {
			out = append(out, e.ID)
		}
	case []RecallName:
		for _, e := range es {
			out = append(out, e.ID)
		}
	}
	return out
}

func entriesOf(res map[string]any) []ContextEntry { return res["entries"].([]ContextEntry) }

// BM25 with porter stemming finds a memory that shares no literal token with the
// query, and IDF ranks the memory holding the rare term above those holding only a
// common one: two things token overlap could not do. score_details sum to the total.
func TestRecallRankingStemsWeighsRareTermsAndExplains(t *testing.T) {
	dir, ws := singleAgentDir(t), "wsRank"
	stemmed := propose(t, dir, ws, "a", "parser", "the parser caches tokenized results")
	propose(t, dir, ws, "a", "workflow", "notes about the release workflow")
	for i := 0; i < 3; i++ {
		propose(t, dir, ws, "a", "deploy", fmt.Sprintf("deploy steps for staging service %d", i))
	}
	for i := 0; i < 6; i++ {
		propose(t, dir, ws, "a", "filler", fmt.Sprintf("unrelated note number %d", i))
	}
	rare := propose(t, dir, ws, "a", "cluster", "kubernetes manifests live in ops")

	res := recallWith(t, dir, ws, RecallRequest{Query: "caching parsers"})
	if got := ids(res); len(got) != 1 || got[0] != stemmed.ID {
		t.Fatalf("stemmed query returned %v, want only %s", got, stemmed.ID)
	}
	res = recallWith(t, dir, ws, RecallRequest{Query: "deploy kubernetes", Explain: true, Limit: 10})
	got := entriesOf(res)
	if len(got) != 4 || got[0].ID != rare.ID {
		t.Fatalf("IDF: want the rare-term memory first of 4, got %v", ids(res))
	}
	for _, e := range got {
		d := e.ScoreDetails
		if d == nil {
			t.Fatalf("%s: explain=true returned no score_details", e.ID)
		}
		sum := d.BM25 + d.Path + d.Anchor + d.Trust + d.Recency + d.Feedback + d.StalePenalty
		if math.Abs(sum-d.Total) > 0.002 || len(d.Reasons) == 0 {
			t.Fatalf("%s: score_details %+v do not sum to the total", e.ID, d)
		}
	}
	if got[0].ScoreDetails.BM25 <= got[1].ScoreDetails.BM25 {
		t.Fatalf("rare term bm25 %v not above common %v", got[0].ScoreDetails.BM25, got[1].ScoreDetails.BM25)
	}
	if plain := entriesOf(recallWith(t, dir, ws, RecallRequest{Query: "deploy"})); plain[0].ScoreDetails != nil {
		t.Fatal("score_details returned without explain")
	}
}

// helpful, misleading and stale_harm outcomes move an entry in the ranking.
func TestRecallRanksFeedback(t *testing.T) {
	dir, ws := multiAgentDir(t), "wsFeedback"
	good := promoted(t, dir, ws, "author", "the cache is warmed at boot")
	bad := promoted(t, dir, ws, "author", "the cache is warmed lazily")
	for _, peer := range []string{"peer-1", "peer-2"} {
		if _, err := VerifyContextOutcome(dir, ws, bad.ID, ContextActor{ID: peer}, VerifyRequest{Outcome: OutcomeStaleHarm}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := VerifyContextOutcome(dir, ws, good.ID, ContextActor{ID: "peer-3"}, VerifyRequest{Outcome: OutcomeHelpful}); err != nil {
		t.Fatal(err)
	}
	got := entriesOf(recallWith(t, dir, ws, RecallRequest{Query: "cache warmed", Explain: true}))
	if len(got) != 2 || got[0].ID != good.ID || got[1].ScoreDetails.Feedback >= 0 || got[0].ScoreDetails.Feedback <= 0 {
		t.Fatalf("feedback not ranked: %v %+v %+v", ids(map[string]any{"entries": got}), got[0].ScoreDetails, got[1].ScoreDetails)
	}
}

// Superseded memory is hidden by default and returned, labelled, with include_superseded.
func TestRecallSupersededOnlyWhenIncluded(t *testing.T) {
	dir, ws := multiAgentDir(t), "wsSuperseded"
	old := promoted(t, dir, ws, "author", "the build uses make")
	newer := promoted(t, dir, ws, "author", "the build uses just", func(r *RememberRequest) {
		r.Op, r.Supersedes = "supersede", []string{old.ID}
	})
	if got := ids(recallWith(t, dir, ws, RecallRequest{Query: "build"})); len(got) != 1 || got[0] != newer.ID {
		t.Fatalf("default recall returned %v", got)
	}
	got := entriesOf(recallWith(t, dir, ws, RecallRequest{Query: "build", IncludeSuperseded: true}))
	if len(got) != 2 || got[0].ID != newer.ID || got[1].ID != old.ID || got[1].State != govstore.RankSuperseded ||
		got[1].SupersededBy != newer.ID || got[0].State != govstore.RankServed {
		t.Fatalf("include_superseded: %+v", got)
	}
}

// A pending entry is returned only with include_pending or status=pending, labelled
// unverified with the votes it still needs; awaiting_me leaves out the caller's own
// entries and those the caller already voted on.
func TestRecallVerificationQueue(t *testing.T) {
	dir, ws := multiAgentDir(t), "wsQueue"
	served := promoted(t, dir, ws, "author", "the queue drains nightly")
	mine := propose(t, dir, ws, "me", "mine", "the queue drains hourly")
	theirs := propose(t, dir, ws, "author", "theirs", "the queue drains weekly")
	voted := propose(t, dir, ws, "author", "voted", "the queue drains monthly")
	if _, err := VerifyContext(dir, ws, voted.ID, "me", true, ""); err != nil {
		t.Fatal(err)
	}
	if got := ids(recallWith(t, dir, ws, RecallRequest{Query: "queue drains"})); len(got) != 1 || got[0] != served.ID {
		t.Fatalf("default recall must be verified-only, got %v", got)
	}
	withPending := entriesOf(recallWith(t, dir, ws, RecallRequest{Query: "queue drains", IncludePending: true, Limit: 10}))
	if len(withPending) != 4 {
		t.Fatalf("include_pending returned %v", ids(map[string]any{"entries": withPending}))
	}
	for _, e := range withPending {
		pending := e.ID != served.ID
		if pending != (e.Trust == "unverified" && e.State == govstore.RankPending && e.VotesNeeded != nil) {
			t.Fatalf("%s mislabelled: trust=%s state=%s votes_needed=%v", e.ID, e.Trust, e.State, e.VotesNeeded)
		}
		if e.ID == voted.ID && *e.VotesNeeded != 1 || e.ID == theirs.ID && *e.VotesNeeded != 2 {
			t.Fatalf("%s votes_needed = %d", e.ID, *e.VotesNeeded)
		}
	}
	queue := ids(recallWith(t, dir, ws, RecallRequest{Status: "pending", Limit: 10}))
	if len(queue) != 3 {
		t.Fatalf("status=pending returned %v", queue)
	}
	awaiting := ids(recallWith(t, dir, ws, RecallRequest{Status: "awaiting_me", Caller: "Me", Limit: 10}))
	if len(awaiting) != 1 || awaiting[0] != theirs.ID {
		t.Fatalf("awaiting_me for me returned %v, want only %s (not %s, %s)", awaiting, theirs.ID, mine.ID, voted.ID)
	}
	if !(RecallRequest{Status: "awaiting_me"}).ReadsUnverified() || (RecallRequest{}).ReadsUnverified() {
		t.Fatal("ReadsUnverified must flag exactly the requests that read pending text")
	}
	if _, err := RecallWith(context.Background(), dir, ws, RecallRequest{Status: "mine"}); !IsInvalidInput(err) {
		t.Fatalf("unknown status: %v", err)
	}

	var g groundingMemory
	if unknown := g.build(dir, ws, "me"); len(unknown) != 0 {
		t.Fatal(unknown)
	}
	if *g.PendingForYou != 1 || g.MemoryPressure.PendingCount != 3 || g.MemoryPressure.CorePct != 0 || *g.StaleMemoryTotal != 1 {
		t.Fatalf("ground pending_for_you=%d memory_pressure=%+v total=%d", *g.PendingForYou, *g.MemoryPressure, *g.StaleMemoryTotal)
	}
	if g.build(dir, ws, "author"); *g.PendingForYou != 1 {
		t.Fatalf("author: pending_for_you=%d, want 1 (mine)", *g.PendingForYou)
	}
}

// The bm25 signal is scaled over the entries the caller can see: a pending entry that
// matches better does not shrink a served entry's score or name itself in the reasons.
func TestRecallBM25ScaleIgnoresInvisibleEntries(t *testing.T) {
	dir, ws := multiAgentDir(t), "wsScale"
	served := promoted(t, dir, ws, "author", "the cache is warmed at boot")
	propose(t, dir, ws, "author", "cache", "cache cache cache: the cache cache")
	got := entriesOf(recallWith(t, dir, ws, RecallRequest{Query: "cache", Explain: true}))
	if len(got) != 1 || got[0].ID != served.ID || got[0].ScoreDetails.BM25 != bm25Weight ||
		!slices.Contains(got[0].ScoreDetails.Reasons, "text match 100% of the best") {
		t.Fatalf("served entry scaled against an invisible one: %+v", got[0].ScoreDetails)
	}
}

// Under the owner-distinct policy a sibling token of the author's owner cannot verify
// the entry, so neither awaiting_me nor ground's pending_for_you offers it; a token
// of another owner still sees it, and the token policy offers it to the sibling.
func TestAwaitingMeHonoursTheOwnerDistinctPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy  string
		sibling int
	}{{DistinctOwner, 0}, {DistinctToken, 1}} {
		dir, ws := ownerPolicyDir(t, tc.policy), "wsOwnerQueue"
		for id, owner := range map[string]string{"alice-1": "alice", "alice-2": "alice", "bob": "bob"} {
			if _, err := MintIdentityToken(dir, id, "agent", 0, nil, TokenIdentity{Owner: owner}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "queue text"}},
			ContextActor{ID: "alice-1", Owner: "alice"}); err != nil {
			t.Fatal(err)
		}
		for caller, want := range map[string]int{"alice-2": tc.sibling, "bob": 1, "alice-1": 0} {
			got := ids(recallWith(t, dir, ws, RecallRequest{Status: "awaiting_me", Caller: caller}))
			var g groundingMemory
			g.build(dir, ws, caller)
			if len(got) != want || *g.PendingForYou != want {
				t.Fatalf("%s policy, %s: awaiting_me %v, pending_for_you %d, want %d", tc.policy, caller, got, *g.PendingForYou, want)
			}
		}
	}
}

// kind, tags, topic, path_prefix, since/until and by combine with AND, lists with OR.
func TestRecallFilters(t *testing.T) {
	dir, ws := singleAgentDir(t), "wsFilters"
	a := propose(t, dir, ws, "alice", "a", "alpha fact", func(r *ProposeContextRequest) {
		r.Kind, r.Topic, r.Tags, r.Paths = "decision", "api/auth", []string{"security", "tokens"}, []string{"api-go/auth.go"}
	})
	b := propose(t, dir, ws, "bob", "b", "beta fact", func(r *ProposeContextRequest) {
		r.Kind, r.Topic, r.Tags, r.Paths = "gotcha", "api", []string{"perf"}, []string{"rust-core/src/lib.rs"}
	})
	cases := []struct {
		req  RecallRequest
		want []string
	}{
		{RecallRequest{Kinds: []string{"decision"}}, []string{a.ID}},
		{RecallRequest{Kinds: []string{"decision", "gotcha"}}, []string{b.ID, a.ID}},
		{RecallRequest{Tags: []string{"perf", "tokens"}}, []string{b.ID, a.ID}},
		{RecallRequest{Tags: []string{"perf"}, Kinds: []string{"decision"}}, nil},
		{RecallRequest{Topic: "api"}, []string{b.ID, a.ID}},
		{RecallRequest{Topic: "api/auth"}, []string{a.ID}},
		{RecallRequest{Topic: "ap"}, nil},
		{RecallRequest{Topic: "api/"}, []string{b.ID, a.ID}}, // normalized as remember stores topics
		{RecallRequest{Topic: " /api/auth/ "}, []string{a.ID}},
		{RecallRequest{PathPrefix: "rust-core/"}, []string{b.ID}},
		{RecallRequest{By: "ALICE"}, []string{a.ID}},
		{RecallRequest{Since: "2000-01-01", Until: "2999-01-01"}, []string{b.ID, a.ID}},
		{RecallRequest{Until: "2000-01-01"}, nil},
	}
	for _, c := range cases {
		if got := ids(recallWith(t, dir, ws, c.req)); !equalIDs(got, c.want) {
			t.Errorf("%+v: got %v, want %v", c.req, got, c.want)
		}
	}
	if _, err := RecallWith(context.Background(), dir, ws, RecallRequest{Since: "yesterday"}); !IsInvalidInput(err) {
		t.Fatalf("bad since: %v", err)
	}
	if _, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: "x", Kind: "rumour"}},
		ContextActor{ID: "a"}); !IsInvalidInput(err) {
		t.Fatalf("unknown kind: %v", err)
	}
	// the tag count is bounded: recall and ground decode every ranked entry's tags
	tags := make([]string, govstore.MaxTags+1)
	for i := range tags {
		tags[i] = fmt.Sprintf("t%d", i)
	}
	for n, ok := range map[int]bool{govstore.MaxTags: true, govstore.MaxTags + 1: false} {
		_, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: "tagged", Tags: tags[:n]}}, ContextActor{ID: "a"})
		if (err == nil) != ok || (!ok && !IsInvalidInput(err)) {
			t.Fatalf("%d tags: %v", n, err)
		}
	}
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Pages concatenate to the one-shot ranking with no repeats, next_cursor and omitted
// describe what is left, and a cursor from another workspace, query or caller, a
// search cursor, an edited one and one past the end of a shrunken ranking are rejected.
func TestRecallCursorIsStableAcrossPages(t *testing.T) {
	dir, ws := t.TempDir(), "wsPages"
	seeded := seedPromoted(t, dir, ws, 11)
	all := ids(recallWith(t, dir, ws, RecallRequest{Limit: 11}))
	var paged []string
	cursor, first := "", ""
	for page := 0; page < 6; page++ {
		res := recallWith(t, dir, ws, RecallRequest{Limit: 4, Cursor: cursor, Caller: "me"})
		paged = append(paged, ids(res)...)
		if res["omitted"] != 11-len(paged) {
			t.Fatalf("page %d omitted=%v after %d returned", page, res["omitted"], len(paged))
		}
		next, ok := res["next_cursor"].(string)
		if !ok {
			break
		}
		cursor = next
		first = cmp.Or(first, next)
	}
	if !equalIDs(paged, all) {
		t.Fatalf("pages %v != one-shot %v", paged, all)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(first)
	parts := strings.Split(string(raw), ".")
	parts[1] = "1000000"
	edited := base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, ".")))
	for name, bad := range map[string]RecallRequest{
		"another query":  {Query: "entry", Cursor: first, Caller: "me"},
		"another caller": {Cursor: first, Caller: "you"},
		"edited offset":  {Cursor: edited, Caller: "me"},
		"not a cursor":   {Cursor: "r2.4.16.00", Caller: "me"},
		// signed over this very ranking, but a search cursor
		"search cursor": {Cursor: searchCursors.encode(RecallRequest{Caller: "me"}.rankingKey(ws), 4), Caller: "me"},
	} {
		if _, err := RecallWith(context.Background(), dir, ws, bad); !IsInvalidInput(err) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// the same arguments on a larger workspace: the cursor names its workspace
	other := t.TempDir() // seeded ids are unique per data dir
	seedPromoted(t, other, "wsPagesOther", 30)
	if _, err := RecallWith(context.Background(), other, "wsPagesOther", RecallRequest{Limit: 4, Cursor: first, Caller: "me"}); !IsInvalidInput(err) {
		t.Fatalf("another workspace's cursor: %v", err)
	}
	// the page-2 cursor points at offset 4; retire 8 of 11 and the ranking ends at 3
	for _, e := range seeded[:8] {
		if _, err := RetireContext(dir, ws, e.ID, "shrink", ContextActor{Admin: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RecallWith(context.Background(), dir, ws, RecallRequest{Limit: 4, Cursor: first, Caller: "me"}); !IsInvalidInput(err) ||
		!strings.Contains(err.Error(), "past the 3 ranked entries") {
		t.Fatalf("out-of-range cursor: %v", err)
	}
}

// With stale entries the stale penalty re-orders each block of the ranking, and every
// page reads whole blocks: pages never repeat or skip an entry, even when the page
// size changes between pages, and a deep page loads a bounded window, not O(offset):
// the ranking is several times the bound, so a window that grows with the offset fails.
func TestRecallPagesAreStableWithStaleEntriesAndBounded(t *testing.T) {
	dir, ws, root := t.TempDir(), "wsStalePages", t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)
	recallChangedFiles = func(context.Context, string, string) []string { return nil }
	defer func() { recallChangedFiles = currentChangedFiles }()
	file := filepath.Join(root, "f.go")
	if err := os.WriteFile(file, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	const n = 200
	stale := map[string]bool{}
	seedPromotedWith(t, dir, ws, n, func(e *ContextEntry) {
		if e.ID[len(e.ID)-1]%3 == 0 { // ids ending 0, 3, 6 or 9, the newest (e0199) among them
			e.Paths = []string{"f.go"}
			e.PathHashes = capturePathHashes(root, e.Paths)
			stale[e.ID] = true
		}
	})
	if err := os.WriteFile(file, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	block := recallCandidateWindow(4) // the first page's limit fixes the block
	for page := 0; ; page++ {
		limit := []int{4, 7, 2}[page%3]
		res := recallWith(t, dir, ws, RecallRequest{Limit: limit, Cursor: cursor})
		if dc, bound := res["drift_checked"].(int), 2*block+max(block, recallCandidateWindow(limit)); dc > bound {
			t.Fatalf("page %d loaded %d entries, over the %d bound: the window grows with the offset", page, dc, bound)
		}
		for _, e := range entriesOf(res) {
			if seen[e.ID] {
				t.Fatalf("page %d repeats %s", page, e.ID)
			}
			seen[e.ID] = true
			if page == 0 && e.Stale {
				t.Fatalf("the first page returned stale %s over fresh entries of its block", e.ID)
			}
		}
		next, ok := res["next_cursor"].(string)
		if !ok {
			break
		}
		if cursor = next; page > n {
			t.Fatal("paging does not end")
		}
	}
	if len(seen) != n {
		t.Fatalf("pages returned %d of %d entries", len(seen), n)
	}
}

// Equal scores order by the newest first, then by id, so the same store renders the
// same page every time.
func TestRecallEqualScoresOrderByStableKey(t *testing.T) {
	dir, ws := t.TempDir(), "wsTies"
	seedPromotedWith(t, dir, ws, 6, func(e *ContextEntry) { e.CreatedAt, e.UpdatedAt = "2026-06-01T00:00:00Z", "2026-06-01T00:00:00Z" })
	want := []string{"e0000", "e0001", "e0002", "e0003", "e0004", "e0005"}
	for range 3 {
		if got := ids(recallWith(t, dir, ws, RecallRequest{Limit: 6})); !equalIDs(got, want) {
			t.Fatalf("ties ordered %v, want %v", got, want)
		}
	}
}

// max_chars bounds the whole result: what does not fit is dropped or cut, reported in
// output_budget, and the cursor resumes at the first entry not returned in full.
func TestRecallMaxCharsIsHonoured(t *testing.T) {
	dir, ws := t.TempDir(), "wsBudget"
	seedPromotedWith(t, dir, ws, 12, func(e *ContextEntry) {
		for len(e.Content) < 900 {
			e.Content += " padding text for the budget test"
		}
	})
	for _, render := range []string{"full", "compact"} {
		res := recallWith(t, dir, ws, RecallRequest{Limit: 12, MaxChars: RecallMinMaxChars * 2, Render: render})
		b, _ := json.Marshal(res)
		if len(b) > RecallMinMaxChars*2 {
			t.Fatalf("%s: result is %d chars, over max_chars %d", render, len(b), RecallMinMaxChars*2)
		}
		rep := res["output_budget"].(map[string]any)
		if rep["returned"] != res["returned"] || rep["dropped"].(int) == 0 {
			t.Fatalf("%s: budget report %v with %v returned", render, rep, res["returned"])
		}
		if _, ok := res["next_cursor"]; !ok {
			t.Fatalf("%s: no next_cursor after dropping entries", render)
		}
	}
	full := recallWith(t, dir, ws, RecallRequest{Limit: 12, MaxChars: RecallMinMaxChars * 2})
	es := entriesOf(full)
	if last := es[len(es)-1]; full["output_budget"].(map[string]any)["truncated"] != 1 || !last.ContentTruncated {
		t.Fatalf("full render should cut the last entry: %v", full["output_budget"])
	}
	next := recallWith(t, dir, ws, RecallRequest{Limit: 12, Cursor: full["next_cursor"].(string)})
	if ids(next)[0] != es[len(es)-1].ID {
		t.Fatalf("cursor must resume at the cut entry %s, got %v", es[len(es)-1].ID, ids(next))
	}
	names := recallWith(t, dir, ws, RecallRequest{Limit: 12, NamesOnly: true})
	if len(names["entries"].([]RecallName)) != 12 || names["render"] != "names" {
		t.Fatalf("names_only: %v", names["entries"])
	}
	for _, bad := range []RecallRequest{{MaxChars: RecallMinMaxChars - 1}, {MaxChars: RecallMaxMaxChars + 1}, {Render: "names"}} {
		if _, err := RecallWith(context.Background(), dir, ws, bad); !IsInvalidInput(err) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}

// fitRecallBudget keeps the prefix a linear scan keeps, sizes each entry once, builds
// O(log n) times and never builds more than limit bytes of entries.
func TestFitRecallBudgetBuildsLogarithmically(t *testing.T) {
	entries := make([]ContextEntry, 50)
	for i := range entries {
		entries[i] = ContextEntry{ID: fmt.Sprintf("e%02d", i), Content: strings.Repeat("x", 100+i*37%400)}
	}
	size := func(e ContextEntry) int { return len(e.ID) + len(e.Content) + 20 }
	result := func(kept []ContextEntry) (total, bytes int) {
		total = 100 // the envelope
		for _, e := range kept {
			total += size(e) + 1
			bytes += size(e)
		}
		return total, bytes
	}
	for _, limit := range []int{50, 500, 3000, 9000, 1 << 20} {
		want := 0
		for want < len(entries) {
			if total, _ := result(entries[:want+1]); total > limit {
				break
			}
			want++
		}
		builds, built := 0, -1
		build := func(kept []ContextEntry, n int) int {
			builds, built = builds+1, len(kept)
			total, bytes := result(kept)
			if bytes > limit {
				t.Errorf("limit %d: built %d bytes of entries", limit, bytes)
			}
			return total
		}
		kept, cut := fitRecallBudget(entries, size, false, limit, build)
		if len(kept) != want || cut != 0 || built != want {
			t.Fatalf("limit %d: kept %d (built %d, cut %d), want %d", limit, len(kept), built, cut, want)
		}
		if most := 2 + bits.Len(uint(len(entries))); builds > most {
			t.Fatalf("limit %d: %d builds, want at most %d", limit, builds, most)
		}
	}
}

// A session is not shown the same entry twice until it changes; names_only and compact
// lines do not count as shown (a later full recall still returns the entry), and
// another session or caller is unaffected.
func TestRecallSessionSeenSuppression(t *testing.T) {
	dir, ws := singleAgentDir(t), "wsSeen"
	a := propose(t, dir, ws, "a", "a", "the api listens on 8042")
	b := propose(t, dir, ws, "a", "b", "the api logs to stderr")
	req := RecallRequest{Query: "api", SessionID: "s1", Caller: "me"}
	for _, partial := range []RecallRequest{{NamesOnly: true}, {Render: "compact"}, {Render: "compact"}} {
		partial.Query, partial.SessionID, partial.Caller = req.Query, req.SessionID, req.Caller
		if got := ids(recallWith(t, dir, ws, partial)); len(got) != 2 {
			t.Fatalf("%+v: %v", partial, got)
		}
	}
	if got := ids(recallWith(t, dir, ws, req)); len(got) != 2 {
		t.Fatalf("first recall: %v", got)
	}
	again := recallWith(t, dir, ws, req)
	if len(ids(again)) != 0 || again["already_shown"] != 2 {
		t.Fatalf("second recall: %v already_shown=%v", ids(again), again["already_shown"])
	}
	for _, other := range []RecallRequest{{Query: "api", SessionID: "s2", Caller: "me"}, {Query: "api", SessionID: "s1", Caller: "you"}, {Query: "api"}} {
		if got := ids(recallWith(t, dir, ws, other)); len(got) != 2 {
			t.Fatalf("%+v suppressed by another session: %v", other, got)
		}
	}
	if _, err := UpdateContextContent(dir, ws, a.ID, "the api listens on 9000", ContextActor{Admin: true}); err != nil {
		t.Fatal(err)
	}
	if e, err := VerifyContext(dir, ws, a.ID, "peer", true, ""); err != nil || !e.Promoted {
		t.Fatalf("re-verify the edit: %v %+v", err, e)
	}
	if got := ids(recallWith(t, dir, ws, req)); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("changed content must be shown again, got %v (b=%s)", got, b.ID)
	}
	// a changed stale flag re-shows too
	c := scoredEntry{rank: govstore.RankEntry{ContentDigest: "d", State: govstore.RankServed}}
	fresh := c.seenPrint()
	c.entry.Stale = true
	if c.seenPrint() == fresh {
		t.Fatal("the seen-print ignores the stale flag")
	}
}

// Seen-sets expire after recallSeenTTL without use.
func TestRecallSeenSetsExpire(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newSeenSets(func() time.Time { return now })
	k, _ := recallSeenKey("ws", "me", "s1")
	e := []scoredEntry{{rank: govstore.RankEntry{ID: "e"}}}
	s.mark(k, map[string]seenPrint{"e": {1}})
	if s.shown(k, e)["e"] != (seenPrint{1}) {
		t.Fatal("mark not recorded")
	}
	now = now.Add(recallSeenTTL + time.Second)
	if s.shown(k, e) != nil || len(s.sets) != 0 || s.entries != 0 {
		t.Fatal("expired set not evicted")
	}
}

// A session id is bounded and the sets are keyed by a digest of it, and the sets hold
// at most maxSeenEntries entries across every session: marking past it drops the least
// recently used other sessions, and a session that alone would pass it starts over.
func TestRecallSeenSetsAreBoundedGlobally(t *testing.T) {
	dir, ws := t.TempDir(), "wsSeenBound"
	for n, ok := range map[int]bool{RecallMaxSessionID: true, RecallMaxSessionID + 1: false} {
		_, err := RecallWith(context.Background(), dir, ws, RecallRequest{SessionID: strings.Repeat("s", n)})
		if (err == nil) != ok || (!ok && !IsInvalidInput(err)) {
			t.Fatalf("a %d-byte session_id: %v", n, err)
		}
	}
	a, _ := recallSeenKey("ws", "me\x00s", "1")
	b, _ := recallSeenKey("ws", "me", "s\x001")
	if a == b {
		t.Fatal("the key must not confuse caller and session")
	}

	now := time.Unix(1_000_000, 0)
	s := newSeenSets(func() time.Time { return now })
	prints := func(prefix string, n int) map[string]seenPrint {
		out := make(map[string]seenPrint, n)
		for i := range n {
			out[fmt.Sprintf("%s-%d", prefix, i)] = seenPrint{1}
		}
		return out
	}
	check := func(step string, sets, entries int) {
		t.Helper()
		sum := 0
		for _, set := range s.sets {
			sum += len(set.shown)
		}
		if len(s.sets) != sets || s.entries != entries || sum != entries {
			t.Fatalf("%s: %d sets, %d entries (%d counted), want %d and %d", step, len(s.sets), s.entries, sum, sets, entries)
		}
	}
	quarter := maxSeenEntries / 4
	keys := make([]seenKey, 5)
	for i := range keys {
		keys[i], _ = recallSeenKey("ws", "me", fmt.Sprint(i))
		now = now.Add(time.Second)
		s.mark(keys[i], prints(fmt.Sprint(i), quarter))
	}
	check("five quarter sets", 4, maxSeenEntries)
	if _, ok := s.sets[keys[0]]; ok {
		t.Fatal("the least recently used set must go first")
	}
	now = now.Add(time.Second)
	s.mark(keys[4], prints("big", maxSeenEntries-quarter+1))
	check("one set past the cap", 1, maxSeenEntries-quarter+1)
	if _, ok := s.sets[keys[4]]; !ok {
		t.Fatal("the set being marked is never the one dropped")
	}

	s = newSeenSets(func() time.Time { return now })
	for i := range maxSeenSessions + 1 {
		k, _ := recallSeenKey("ws", "me", fmt.Sprint(i))
		now = now.Add(time.Second)
		s.mark(k, prints("e", 1))
	}
	check("one set per session past the session cap", maxSeenSessions, maxSeenSessions)
}

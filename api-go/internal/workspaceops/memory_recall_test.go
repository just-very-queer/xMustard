package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
// describe what is left, and a cursor from another query is rejected.
func TestRecallCursorIsStableAcrossPages(t *testing.T) {
	dir, ws := t.TempDir(), "wsPages"
	seedPromoted(t, dir, ws, 11)
	all := ids(recallWith(t, dir, ws, RecallRequest{Limit: 11}))
	var paged []string
	cursor := ""
	for page := 0; page < 6; page++ {
		res := recallWith(t, dir, ws, RecallRequest{Limit: 4, Cursor: cursor})
		paged = append(paged, ids(res)...)
		if res["omitted"] != 11-len(paged) {
			t.Fatalf("page %d omitted=%v after %d returned", page, res["omitted"], len(paged))
		}
		next, ok := res["next_cursor"].(string)
		if !ok {
			break
		}
		cursor = next
	}
	if !equalIDs(paged, all) {
		t.Fatalf("pages %v != one-shot %v", paged, all)
	}
	if _, err := RecallWith(context.Background(), dir, ws, RecallRequest{Query: "entry", Cursor: cursor}); !IsInvalidInput(err) {
		t.Fatalf("foreign cursor: %v", err)
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

// A session is not shown the same entry twice until it changes; names_only does not
// count as shown, and another session or caller is unaffected.
func TestRecallSessionSeenSuppression(t *testing.T) {
	dir, ws := singleAgentDir(t), "wsSeen"
	a := propose(t, dir, ws, "a", "a", "the api listens on 8042")
	b := propose(t, dir, ws, "a", "b", "the api logs to stderr")
	req := RecallRequest{Query: "api", SessionID: "s1", Caller: "me"}
	if got := ids(recallWith(t, dir, ws, RecallRequest{Query: "api", SessionID: "s1", Caller: "me", NamesOnly: true})); len(got) != 2 {
		t.Fatalf("names_only: %v", got)
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
	s := &seenSets{now: func() time.Time { return now }, sets: map[string]*seenSet{}}
	s.mark("k", map[string]string{"e": "p"})
	if s.shown("k")["e"] != "p" {
		t.Fatal("mark not recorded")
	}
	now = now.Add(recallSeenTTL + time.Second)
	if s.shown("k") != nil || len(s.sets) != 0 {
		t.Fatal("expired set not evicted")
	}
}

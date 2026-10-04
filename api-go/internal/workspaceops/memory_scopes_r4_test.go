package workspaceops

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// Round 4 findings. Every test here fails on the head its round read, so the FAIL
// line in the gate log is the finding's own reproduction.

// WS-29b-r4-1: HookSessionHandoff deliberately ranks another principal's prior
// session as a valid takeover (priorRank gives it rank 2 rather than dropping it).
// The snapshot is scoped to the SESSION's principal, so the private titles that
// survive belong to that other principal and the handoff carries them to this
// caller. Filtering while building the snapshot cannot protect the handoff.
func TestAPrivateTitleDoesNotCrossPrincipalsInASessionHandoff(t *testing.T) {
	dir, root := newLedgerFixture(t, true)
	trustRunKeys(t, dir, "agent-a", "agent-b")
	theirs, err := Remember(dir, ledgerWS, RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "agent-b private decision", Content: "agent-b writes a plan only it may read",
		Permission: "readwrite", Kind: "decision", Visibility: ScopePrivate}}, ContextActor{ID: "agent-b"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	b := ledgerSession(root, "claude", "sess-b", "agent-b")
	recordAll(t, dir, b, LedgerEvent{Kind: LedgerPrompt, Text: "continue the private plan"},
		LedgerEvent{Kind: LedgerMemoryProposed, Tool: "mcp__xmustard__remember", EntryID: theirs.ID})

	h, err := HookSessionHandoff(context.Background(), dir, ledgerWS, "agent-a", "codex:sess-a", false, true)
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if h == nil || h.Snapshot == nil {
		t.Fatalf("handoff = %+v, want the prior session's snapshot", h)
	}
	if h.Caveat != "another principal's" && h.Caveat != "another principal's, not ended" {
		t.Fatalf("handoff caveat = %q, want the prior session to be another principal's", h.Caveat)
	}
	if s := h.Snapshot; slices.Contains(s.Decisions, "agent-b private decision") {
		t.Fatalf("another principal's private decision title crossed in the handoff: %v", s.Decisions)
	}
	for _, p := range h.Snapshot.PendingProposals {
		if p.ID == theirs.ID {
			t.Fatalf("another principal's private proposal %s crossed in the handoff: %+v", theirs.ID, p)
		}
	}
	for _, p := range h.PendingProposals {
		if p.ID == theirs.ID {
			t.Fatalf("another principal's private proposal %s reached handoff.pending_proposals: %+v", theirs.ID, p)
		}
	}
}

// WS-29b-r4-2: the HTTP reader takes run_id and session_id as query parameters and
// passes them straight into the recall. checkRunKeyTrust guards writes only, so a LATER
// run under the SAME principal names an earlier run's id and reads that run's memory.
// The threat is the same principal across runs, so the fixture is one principal and
// two runs: an author-only rule would not catch this.
func TestAnUntrustedRunKeyNarrowsNothingOnTheReadPath(t *testing.T) {
	dir := singleAgentDir(t)
	// The write is allowed first, so the entry exists; the operator setting is then
	// cleared, which is what a later run under the same principal meets.
	trustRunKeys(t, dir, "agent-a")
	theirs, err := Remember(dir, "ws-1", RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "agent-a run note", Content: "a note confined to one run", Permission: "readwrite",
		Visibility: ScopeRun, RunID: "run-1"}}, ContextActor{ID: "agent-a"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	writeRunForScope(t, dir, "ws-1", "run-1")
	trustRunKeys(t, dir) // the operator stops trusting agent-a to assert a run key

	got, err := RecallWith(context.Background(), dir, "ws-1", RecallRequest{
		Query: "note confined", Caller: "agent-a", RunID: "run-1", IncludePending: true})
	if err != nil {
		t.Fatalf("recall with an untrusted key: %v", err)
	}
	if slices.ContainsFunc(entriesOf(got), func(e ContextEntry) bool { return e.ID == theirs.ID }) {
		t.Fatalf("an untrusted run key served run-scoped entry %s of the caller's own run", theirs.ID)
	}

	// The listed principal is served normally, so the check reads the operator setting
	// and is not a blanket refusal.
	trustRunKeys(t, dir, "agent-a")
	ok, err := RecallWith(context.Background(), dir, "ws-1", RecallRequest{
		Query: "note confined", Caller: "agent-a", RunID: "run-1", IncludePending: true})
	if err != nil {
		t.Fatalf("recall as the listed principal: %v", err)
	}
	if !slices.ContainsFunc(entriesOf(ok), func(e ContextEntry) bool { return e.ID == theirs.ID }) {
		t.Fatalf("a trusted run key principal was refused its own run-scoped entry %s", theirs.ID)
	}
}

// WS-29b-r4-5: readAtRisk fetched one bounded window and only then filtered, so a
// caller whose own stale entry sat past that window got neither the entry nor a
// truncated signal. Two channels reported this independently. The window here is one
// store page (atRiskPage), so the unreadable rows have to outnumber it to reproduce.
func TestAnAtRiskPageIsFullEvenWhenTheFirstRowsAreAnotherPrincipals(t *testing.T) {
	dir := multiAgentDir(t)
	trustRunKeys(t, dir, "agent-a", "agent-b")
	// Distinct bodies: two entries with the same text are one memory with two authors'
	// votes, and the store would hold one row where the fixture needs many.
	theirs := make([]string, 0, atRiskPage+20)
	for i := range atRiskPage + 20 {
		theirs = append(theirs, proposeAtRisk(t, dir, "ws-1", "agent-b",
			fmt.Sprintf("agent-b stale note %d", i), fmt.Sprintf("another principal's stale note number %d", i), ScopePrivate))
	}
	// Fewer of the caller's own rows than the page holds, so all of them fit whatever
	// order the store uses; which rows fill a FULL page is the store's business and not
	// what this finding is about.
	mine := make([]string, 0, 3)
	for i := range 3 {
		mine = append(mine, proposeAtRisk(t, dir, "ws-1", "agent-a",
			fmt.Sprintf("agent-a stale note %d", i), fmt.Sprintf("my own stale note number %d", i), ""))
	}

	got, truncated, err := readAtRiskFor(t, dir, "ws-1", "agent-a")
	if err != nil {
		t.Fatalf("readAtRisk: %v", err)
	}
	// The defect was the SCAN: the caller's rows were dropped behind rows it may not
	// read, and truncated said false. Both are asserted, and none of the unreadable
	// rows is in the page.
	for _, id := range mine {
		if !slices.Contains(atRiskIDs(got), id) {
			t.Fatalf("my own at-risk entry %s was omitted behind %d unreadable rows: %d visible, truncated=%v",
				id, len(theirs), len(got), truncated)
		}
	}
	if len(got) != len(mine) {
		t.Fatalf("got %d visible rows, want exactly my own %d", len(got), len(mine))
	}
	if truncated {
		t.Fatalf("truncated=true with %d visible rows and %d unreadable ones in the store", len(got), len(theirs))
	}
	for _, id := range theirs {
		if slices.Contains(atRiskIDs(got), id) {
			t.Fatalf("another principal's at-risk row %s was listed", id)
		}
	}

	// With more of the caller's own at-risk rows than one store window holds, truncated
	// must say so rather than reporting a page that is quietly short: the answer caps at
	// maxAtRisk while the scan keeps finding more.
	for i := range atRiskPage + 10 {
		proposeAtRisk(t, dir, "ws-1", "agent-a", fmt.Sprintf("agent-a stale note full %d", i),
			fmt.Sprintf("my own stale note number full %d", i), "")
	}
	full, truncated, err := readAtRiskFor(t, dir, "ws-1", "agent-a")
	if err != nil {
		t.Fatalf("readAtRisk with a full store: %v", err)
	}
	if len(full) != maxAtRisk {
		t.Fatalf("got %d rows, want the capped page of %d", len(full), maxAtRisk)
	}
	if !truncated {
		t.Fatalf("truncated=false with %d visible at-risk rows and a page of %d", atRiskPage+13, maxAtRisk)
	}
}

// readAtRiskFor runs readAtRisk over the store and returns its answer.
func readAtRiskFor(t *testing.T, dir, ws, caller string) ([]AtRiskMemory, bool, error) {
	t.Helper()
	var got []AtRiskMemory
	var truncated bool
	err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		var err error
		got, truncated, err = readAtRisk(context.Background(), r, ws, caller, ScopeRequest{})
		return err
	})
	return got, truncated, err
}

// WS-29b-r4-6, half one of codex-review:1: a verifier who knows a private proposal's
// id could call VerifyContextOutcome and get its ContextEntry back. The response is
// projected metadata (projectEntries renders no content), so the vote records itself
// and the peer learns the entry's state, revision and tally — never the author's words.
// A peer is also REQUIRED here: a private proposal is verified by peers or it can
// never be promoted, so the vote itself cannot be the thing refused.
func TestAVerifierResponseCarriesNoAnotherPrincipalsContent(t *testing.T) {
	dir := multiAgentDir(t)
	trustRunKeys(t, dir, "agent-a", "verifier-c")
	const body = "a proposal only its author may read"
	theirs := propose(t, dir, "ws-1", "agent-a", "agent-a private proposal", body,
		func(r *ProposeContextRequest) { r.Visibility = ScopePrivate })

	got, err := VerifyContextOutcome(dir, "ws-1", theirs.ID, ContextActor{ID: "verifier-c"},
		VerifyRequest{Outcome: OutcomeApprove, Revision: 1})
	if err != nil {
		t.Fatalf("a peer must be able to verify a private proposal, or it can never be promoted: %v", err)
	}
	if strings.Contains(got.Content, body) || got.Description != "" {
		t.Fatalf("the verify response carried another principal's text: content=%q description=%q",
			got.Content, got.Description)
	}
	if got.ID != theirs.ID || len(got.Verifications) == 0 {
		t.Fatalf("the verdict was not recorded: %+v", got)
	}
}

// WS-29b-r4-6: and HumanApprovalQueue listed pending entries without a scope check,
// rendering another principal's pending content in the approver's queue. The dir needs
// multi-agent verification, or the single-agent default promotes the proposal on write
// and there is nothing pending to queue.
func TestAnApprovalQueueHoldsNoAnotherPrincipalsPrivateEntry(t *testing.T) {
	dir := multiAgentDir(t)
	trustRunKeys(t, dir, "agent-a", "approver-h")
	theirs := propose(t, dir, "ws-1", "agent-a", "agent-a private proposal", "a proposal only its author may read",
		func(r *ProposeContextRequest) { r.Visibility = ScopePrivate })

	q, err := HumanApprovalQueue(dir, "ws-1", humanApprover("approver-h", "owner-1"), 50, 10)
	if err != nil {
		t.Fatalf("approval queue: %v", err)
	}
	for _, item := range q.Items {
		if item.EntryID == theirs.ID || item.Title == theirs.Title {
			t.Fatalf("another principal's private proposal %s reached the approval queue: %+v", theirs.ID, item)
		}
	}
}

// WS-29b-r4-7: the scope-leak metric was computed from two counts the assertions
// above had already forced to be equal, so it could never be non-zero.
func TestTheScopeLeakMetricFindsAnEntryTheScopeRuleDoesNotAdmit(t *testing.T) {
	served := map[string]bool{"a": true, "b": true}
	admitted := map[string]bool{"a": true, "b": true}
	delete(admitted, "b")
	if n := ScopeLeaks(len(served), len(admitted)); n != 0 {
		t.Fatalf("the count form reported %d for equal counts", n)
	}
	var leaks []string
	for id := range served {
		if !admitted[id] {
			leaks = append(leaks, id)
		}
	}
	if !slices.Contains(leaks, "b") {
		t.Fatalf("the set form did not find the leak: %v", leaks)
	}
}

// WS-29b-r4-4: the served set came from the scope-filtered ranking, but the delta was
// folded from every workspace event, so a confined entry the caller cannot see was
// still named. The ranking filter therefore did not cover ground's delta.
func TestGroundDeltaNamesNoAnotherPrincipalsConfinedEntry(t *testing.T) {
	dir := multiAgentDir(t)
	trustRunKeys(t, dir, "agent-a", "agent-b")
	// Private on purpose: a peer cannot read a private proposal to verify it, so the
	// two entries are promoted by their own authors' single-agent path instead.
	theirs := promoted(t, dir, "ws-1", "agent-b", "agent-b private note", func(r *RememberRequest) {
		r.ProposeContextRequest.Title = "agent-b private note"
		r.ProposeContextRequest.Visibility = ScopePrivate
	})
	mine := promoted(t, dir, "ws-1", "agent-a", "agent-a private note", func(r *RememberRequest) {
		r.ProposeContextRequest.Title = "agent-a private note"
		r.ProposeContextRequest.Visibility = ScopePrivate
	})
	since := memoryRevision(t, dir, "ws-1")

	// Each entry is retracted inside the window by its own author, which is the removal
	// the delta reports.
	for _, e := range []struct{ id, author string }{{theirs.ID, "agent-b"}, {mine.ID, "agent-a"}} {
		if _, err := RetractContext(dir, "ws-1", e.id, "no longer true",
			ContextActor{ID: e.author, Approver: true}); err != nil {
			t.Fatalf("retract %s by %s: %v", e.id, e.author, err)
		}
	}

	var g groundingMemory
	if unknown := g.build(dir, "ws-1", "agent-a", groundMemoryOptions{since: since}); len(unknown) != 0 {
		t.Fatalf("unknown %+v", unknown)
	}
	d := g.MemoryDelta
	if d == nil {
		t.Fatalf("no delta for revision %d", since)
	}
	if !slices.Contains(d.Removed, mine.ID) {
		t.Fatalf("my own retracted entry %s is not in the delta's removed: %+v", mine.ID, d)
	}
	for _, list := range [][]string{d.Added, d.Changed, d.Stale, d.Removed} {
		if slices.Contains(list, theirs.ID) {
			t.Fatalf("ground's delta named another principal's confined entry %s: %+v", theirs.ID, d)
		}
	}
	for _, s := range d.Superseded {
		if s.ID == theirs.ID || s.By == theirs.ID {
			t.Fatalf("ground's delta superseded another principal's confined entry %s: %+v", theirs.ID, d)
		}
	}
}

// proposeAtRisk writes a PROMOTED entry that declares a path, then marks it stale,
// which is the only way an entry reaches the at-risk state readAtRisk lists: MarkStale
// records drift against a declared anchor, and an anchor only exists once the entry was
// promoted with one. scope is "" for the caller's own rows, which are workspace-scoped
// and therefore visible, and ScopePrivate for another principal's, which is what makes
// a row unreadable rather than merely older.
func proposeAtRisk(t *testing.T, dir, ws, author, title, content, scope string) string {
	t.Helper()
	e := promoted(t, dir, ws, author, content, func(r *RememberRequest) {
		r.ProposeContextRequest.Paths = []string{"srv.go"}
		r.ProposeContextRequest.Title = title
		r.ProposeContextRequest.Visibility = scope
	})
	markAtRisk(t, dir, ws, e.ID)
	return e.ID
}

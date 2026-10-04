package workspaceops

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// Round 3 found that a filter added to one read path does not filter the others: the
// ranking carried the scope keys while readAtRisk, the settle-and-report path, the
// handoff projection and the proposal review each list entries on their own. Every
// test here pins one of those surfaces, and each fixture holds two principals so an
// unfiltered listing is observably wrong rather than merely broader.

func scopeActor(id string) ContextActor { return ContextActor{ID: id} }

// markAtRisk persists drift on the entry's declared path, which is the only way an
// entry reaches the at-risk state readAtRisk lists.
func markAtRisk(t *testing.T, dir, ws, id string) {
	t.Helper()
	err := memoryUpdate(context.Background(), dir, ws, func(tx govstore.Tx) error {
		n, err := tx.MarkStale(context.Background(), id,
			[]govstore.AnchorRef{{Kind: govstore.AnchorPath, Value: "srv.go"}}, "c0ffee1",
			govstore.Actor{Principal: "drift-bot"})
		if err == nil && n == 0 {
			err = errNoDriftMarked
		}
		return err
	})
	if err != nil {
		t.Fatalf("mark %s at risk: %v", id, err)
	}
}

// errNoDriftMarked says MarkStale flagged nothing, so the fixture's path anchor never
// reached the entry and the rest of the test would read a green path for the wrong reason.
var errNoDriftMarked = errNoDrift("MarkStale flagged no anchor")

type errNoDrift string

func (e errNoDrift) Error() string { return string(e) }

// atRiskWithPath proposes as author a memory anchored to srv.go, so markAtRisk can
// flag it, and returns it.
func atRiskWithPath(t *testing.T, dir, ws, author, title, content string, extra ...func(*ProposeContextRequest)) *ContextEntry {
	t.Helper()
	extra = append([]func(*ProposeContextRequest){
		func(r *ProposeContextRequest) { r.Paths = []string{"srv.go"} }}, extra...)
	return propose(t, dir, ws, author, title, content, extra...)
}

// trustRunKeys is the operator's answer to "which agents may assert a run key", written
// through the settings file loadSettings reads. The visibility tests in
// memory_scopes_test.go exercise the SCOPE rule, so they need the principal trusted to get
// past the assertion check first; the trust rule itself has its own tests in this file.
func trustRunKeys(t *testing.T, dir string, principals ...string) {
	t.Helper()
	settings, err := loadSettings(dir)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	settings.TrustedRunKeyPrincipals = cleanRunKeyPrincipals(principals)
	if err := writeJSON(filepath.Join(dir, "settings.json"), settings); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func atRiskIDs(atRisk []AtRiskMemory) []string {
	out := make([]string, 0, len(atRisk))
	for _, m := range atRisk {
		out = append(out, m.ID)
	}
	return out
}

// A private entry marked needs_reverify must not reach another principal's
// at_risk_memories with its id, title, paths and anchors (round 3, r3-1). The surface is
// ground's memory scan, which is where at_risk_memories is built.
func TestAStalePrivateMemoryIsNotAnotherPrincipalsAtRisk(t *testing.T) {
	dir := singleAgentDir(t)
	private := atRiskWithPath(t, dir, "ws-1", "author-a", "staging vault credentials",
		"the staging credentials live in the staging vault",
		func(r *ProposeContextRequest) { r.Visibility = ScopePrivate })
	open := atRiskWithPath(t, dir, "ws-1", "author-b", "shared note", "everyone may read this note")
	markAtRisk(t, dir, "ws-1", private.ID)
	markAtRisk(t, dir, "ws-1", open.ID)

	scan, err := scanGroundMemory(dir, "ws-1", "author-b", groundStaleWindow, noSince, groundMemoryOptions{})
	if err != nil {
		t.Fatalf("ground scan: %v", err)
	}
	seen := atRiskIDs(scan.atRisk)
	if slices.Contains(seen, private.ID) {
		t.Fatalf("another principal's at_risk_memories named the private entry %s: %v", private.ID, seen)
	}
	if !slices.Contains(seen, open.ID) {
		t.Fatalf("at_risk_memories = %v, want the workspace entry %s among them", seen, open.ID)
	}
}

// The author's own key is the permitted case beside the refused one: the guard refuses
// other principals, not everyone (round 1's correction, re-asserted on the at-risk path).
func TestTheAuthorsOwnStalePrivateMemoryIsStillAtRiskForThem(t *testing.T) {
	dir := singleAgentDir(t)
	private := atRiskWithPath(t, dir, "ws-1", "author-a", "private drift note",
		"a private note that has drifted",
		func(r *ProposeContextRequest) { r.Visibility = ScopePrivate })
	markAtRisk(t, dir, "ws-1", private.ID)

	scan, err := scanGroundMemory(dir, "ws-1", "author-a", groundStaleWindow, noSince, groundMemoryOptions{})
	if err != nil {
		t.Fatalf("ground scan: %v", err)
	}
	if seen := atRiskIDs(scan.atRisk); len(seen) != 1 || seen[0] != private.ID {
		t.Fatalf("the author's at_risk_memories = %v, want only %s", seen, private.ID)
	}
}

// ground's scan is the surface the ranking filter was added for, and it is the one the
// at-risk list feeds: a private entry must not appear in the scan another principal drives.
func TestGroundDoesNotListAnotherPrincipalsStalePrivateMemory(t *testing.T) {
	dir := singleAgentDir(t)
	private := atRiskWithPath(t, dir, "ws-1", "author-a", "staging vault credentials",
		"the staging credentials live in the staging vault",
		func(r *ProposeContextRequest) { r.Visibility = ScopePrivate })
	markAtRisk(t, dir, "ws-1", private.ID)

	scan, err := scanGroundMemory(dir, "ws-1", "author-b", groundStaleWindow, noSince, groundMemoryOptions{})
	if err != nil {
		t.Fatalf("ground scan: %v", err)
	}
	for _, m := range scan.atRisk {
		if m.ID == private.ID {
			t.Fatalf("ground named another principal's private entry %s: %+v", private.ID, scan.atRisk)
		}
	}
	if scan.total != 0 {
		t.Fatalf("ground counted %d served memories for a principal with none, want 0", scan.total)
	}
}

// A scoped proposal's duplicate and conflict candidates must not reach another
// principal's entries of the same scope, and its report must not name them (r3-7).
// A scoped proposal's duplicate and conflict candidates must not reach another
// principal's entries of the same scope, and its report must not name them (r3-7).
//
// Both entries declare the SAME path, so they are anchor neighbours and the review really
// compares them. Without the shared anchor the candidate list is empty and the filter is
// never reached: a fixture that cannot fail, which is how this test was first written and
// why its mutation survived.
func TestAProposalIsNotComparedAgainstAnotherPrincipalsScopedEntries(t *testing.T) {
	dir := singleAgentDir(t)
	theirPrivate := propose(t, dir, "ws-1", "author-b", "their private parser refactor",
		"their private refactor of the request parser",
		func(r *ProposeContextRequest) {
			r.Visibility = ScopePrivate
			r.Description = "their private parser refactor"
			r.Paths = []string{"parser.go"}
		})
	mine, err := Remember(dir, "ws-1", RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "my private parser refactor", Content: "my private refactor of the request parser",
		Permission: "readwrite", Visibility: ScopePrivate, Description: "my private parser refactor",
		Paths: []string{"parser.go"}}},
		scopeActor("author-a"))
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	for _, d := range mine.PossibleDuplicates {
		if d.ID == theirPrivate.ID {
			t.Fatalf("possible_duplicates named another principal's private entry %s: %+v", theirPrivate.ID, mine.PossibleDuplicates)
		}
	}
	for _, c := range mine.PossibleConflicts {
		if slices.Contains(c.EntryIDs, theirPrivate.ID) {
			t.Fatalf("possible_conflicts named another principal's private entry %s: %+v", theirPrivate.ID, mine.PossibleConflicts)
		}
	}
	if slices.Contains(mine.SuggestedSupersedes, theirPrivate.ID) {
		t.Fatalf("suggested_supersedes named another principal's private entry %s: %v",
			theirPrivate.ID, mine.SuggestedSupersedes)
	}
}

// The author's own scoped entries stay reachable: the fix adds the author condition
// beside the scope match rather than replacing it.
func TestAProposalStillCitesItsAuthorsOwnScopedEntries(t *testing.T) {
	dir := singleAgentDir(t)
	first := propose(t, dir, "ws-1", "author-a", "the request parser is hand rolled",
		"the request parser is hand rolled in this module",
		func(r *ProposeContextRequest) {
			r.Visibility = ScopePrivate
			r.Description = "the request parser"
		})
	second, err := Remember(dir, "ws-1", RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "the request parser is hand rolled here too", Content: "the request parser is hand rolled in this module",
		Permission: "readwrite", Visibility: ScopePrivate, Description: "the request parser again"}},
		scopeActor("author-a"))
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	named := second.DuplicateOf == first.ID
	for _, e := range second.PossibleDuplicates {
		named = named || e.ID == first.ID
	}
	if !named {
		t.Fatalf("the author's own private entry %s was not reachable: duplicate_of=%q duplicates=%+v",
			first.ID, second.DuplicateOf, second.PossibleDuplicates)
	}
}

// A run key is a caller-asserted scope key, so which principals may name one is an
// operator SETTING (owner decision 2026-10-03), not a hard-coded rule. Unconfigured is
// not trusted: the derivation refuses an asserted key from an unlisted principal.
func TestAnUnconfiguredRunKeyIsNotTrusted(t *testing.T) {
	dir := singleAgentDir(t)
	writeRunForScope(t, dir, "ws-1", "run-1")
	err := proposeRunScoped(t, dir, "ws-1", "author-a", "run-1")
	if err == nil {
		t.Fatal("an unconfigured run key was trusted: want a refusal")
	}
	if !strings.Contains(err.Error(), "run") {
		t.Fatalf("error = %v, want it to name what was refused", err)
	}
}

// The operator names the principals that may assert a run key; a listed one is served.
func TestAListedPrincipalMayAssertARunKey(t *testing.T) {
	dir := singleAgentDir(t)
	writeRunForScope(t, dir, "ws-1", "run-1")
	trustRunKeys(t, dir, "author-a")
	e := propose(t, dir, "ws-1", "author-a", "run scoped note", "a note confined to one run",
		func(r *ProposeContextRequest) { r.Visibility = ScopeRun; r.RunID = "run-1" })
	if e.Scope != ScopeRun || e.ScopeKey != "run-1" {
		t.Fatalf("stored scope %q/%q, want %q/%q", e.Scope, e.ScopeKey, ScopeRun, "run-1")
	}
}

// A principal the operator did not list is refused, and the refusal is audited, so an
// operator can see an agent trying to name a run it does not own (owner decision:
// refused or ignored AND audited).
func TestAnUnlistedPrincipalIsRefusedAndAudited(t *testing.T) {
	dir := singleAgentDir(t)
	writeRunForScope(t, dir, "ws-1", "run-1")
	trustRunKeys(t, dir, "author-b")
	if err := proposeRunScoped(t, dir, "ws-1", "author-a", "run-1"); err == nil {
		t.Fatal("an unlisted principal named a run key: want a refusal")
	}
	// The audit record is read through the no-guard loader, because the refusal is audited
	// before anything else has registered the workspace: the operator needs the event even
	// when the write was the first thing that touched the data dir.
	events, err := loadAuditEvents(dir, "ws-1")
	if err != nil {
		t.Fatalf("load audit events: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Action == AuditRunKeyRefused && strings.Contains(e.TargetID, "run-1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refused run key is not in the audit log (%d events): %+v", len(events), events)
	}
}

// proposeRunScoped proposes a run-scoped memory as author, and returns the refusal when
// there is one.
func proposeRunScoped(t *testing.T, dir, ws, author, runID string) error {
	t.Helper()
	_, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "run scoped note", Content: "a note confined to one run", Permission: "readwrite",
		Visibility: ScopeRun, RunID: runID}}, scopeActor(author))
	return err
}

// writeRunForScope writes the run record a run-scoped key names, so the write side has
// a run to check the asserted key against.
func writeRunForScope(t *testing.T, dir, ws, runID string) {
	t.Helper()
	if err := saveRunRecord(dir, runRecord{RunID: runID, WorkspaceID: ws, Status: "succeeded"}); err != nil {
		t.Fatalf("write run %s: %v", runID, err)
	}
}

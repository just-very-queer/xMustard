package workspaceops

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// rememberAs proposes content as who.
func rememberAs(t *testing.T, dir, ws string, who ContextActor, content string, extra ...func(*RememberRequest)) *ContextEntry {
	t.Helper()
	req := RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: content, Permission: "readwrite"}}
	for _, f := range extra {
		f(&req)
	}
	e, err := Remember(dir, ws, req, who)
	if err != nil {
		t.Fatalf("remember %q: %v", content, err)
	}
	return e
}

func activeCount(t *testing.T, dir, ws string) int {
	t.Helper()
	all, err := loadContextEntries(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

func dupIDs(e *ContextEntry) map[string]string {
	out := map[string]string{}
	for _, d := range e.PossibleDuplicates {
		out[d.ID] = d.Match
	}
	return out
}

const portFact = "The payments service listens on port 8081 because the metrics sidecar owns 8080."

// An exact restatement of promoted memory (after case and whitespace are normalized)
// corroborates it instead of adding an entry; each principal is counted once and the
// history records the corroboration.
func TestExactDuplicateOfPromotedMemoryIsCorroborated(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	served := promoted(t, dir, ws, "author", portFact)
	restated := "  the PAYMENTS service listens on port 8081 because the metrics   sidecar owns 8080. "
	for i, who := range []string{"agent-b", "agent-b", "agent-c"} {
		got := rememberAs(t, dir, ws, ContextActor{ID: who}, restated)
		if got.Outcome != OutcomeCorroborated || got.DuplicateOf != served.ID || got.ID != served.ID {
			t.Fatalf("restatement %d: %+v", i, got)
		}
		if got.Content != portFact || !got.Promoted {
			t.Fatalf("a corroboration returns the served entry: %+v", got)
		}
		if want := []int{1, 1, 2}[i]; got.Corroborations != want {
			t.Fatalf("restatement %d counts %d principals, want %d", i, got.Corroborations, want)
		}
	}
	if n := activeCount(t, dir, ws); n != 1 {
		t.Fatalf("%d entries after restatements, want 1", n)
	}
	_, res := entryView(t, dir, ws, served.ID, true)
	events := res["events"].([]govstore.Event)
	if n := countEvents(events, govstore.EventCorroborate); n != 2 {
		t.Fatalf("%d corroborate events, want one per principal", n)
	}
}

func countEvents(events []govstore.Event, typ string) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// A near duplicate is created, reported with its similarity, and suggested as the
// entry it may supersede; an exact duplicate of a pending entry is created and reported.
func TestNearDuplicatesAreReportedWithSimilarity(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	served := promoted(t, dir, ws, "author", portFact)
	near := rememberAs(t, dir, ws, ContextActor{ID: "agent-b"}, strings.Replace(portFact, "payments", "payment", 1))
	if near.Outcome != OutcomeCreated || near.ID == served.ID || len(near.PossibleDuplicates) != 1 {
		t.Fatalf("near duplicate: %+v", near)
	}
	d := near.PossibleDuplicates[0]
	if d.ID != served.ID || d.Match != MatchNear || d.Similarity < 0.9 || d.Similarity >= 1 || d.State != govstore.RankServed {
		t.Fatalf("near duplicate reported as %+v", d)
	}
	if !slices.Contains(near.SuggestedSupersedes, served.ID) {
		t.Fatalf("near duplicate of served memory not suggested for supersession: %v", near.SuggestedSupersedes)
	}
	similar := rememberAs(t, dir, ws, ContextActor{ID: "agent-c"},
		"The payments service listens on port 8081; the metrics sidecar owns port 8080 and the admin UI 8082.")
	if dupIDs(similar)[served.ID] != MatchSimilar {
		t.Fatalf("mid-similarity restatement: %+v", similar.PossibleDuplicates)
	}
	pending := rememberAs(t, dir, ws, ContextActor{ID: "agent-d"}, "Integration tests use the fake gateway on port 9099.")
	again := rememberAs(t, dir, ws, ContextActor{ID: "agent-e", Verifier: true}, "integration tests use the fake gateway on port 9099.")
	if again.Outcome != OutcomeCreated || dupIDs(again)[pending.ID] != MatchExact || again.PossibleDuplicates[0].State != govstore.RankPending {
		t.Fatalf("exact duplicate of a pending entry: %+v", again)
	}
	// a proposer that may not read unverified memory is told nothing about it
	blind := rememberAs(t, dir, ws, ContextActor{ID: "agent-f"}, "Integration tests use the fake gateway on port 9099!")
	if _, ok := dupIDs(blind)[pending.ID]; ok {
		t.Fatalf("pending text leaked to a proposer without the verifier role: %+v", blind.PossibleDuplicates)
	}
}

// The entropy gate keeps short generic text out of fuzzy matching: a different short
// text is not a duplicate, though its shingles match, while an exact restatement is.
func TestEntropyGatePreventsFalseMergesOfShortGenericText(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	served := promoted(t, dir, ws, "author", "ok ok ok ok")
	other := rememberAs(t, dir, ws, ContextActor{ID: "agent-b"}, "ok ok ok ok ok")
	if other.Outcome != OutcomeCreated || len(other.PossibleDuplicates) != 0 {
		t.Fatalf("short generic text merged fuzzily: %+v", other)
	}
	same := rememberAs(t, dir, ws, ContextActor{ID: "agent-c"}, "OK ok  ok ok")
	if same.Outcome != OutcomeCorroborated || same.DuplicateOf != served.ID {
		t.Fatalf("exact restatement of short text: %+v", same)
	}
}

// A proposal that states intent (supersedes, claims, relations) or names other files
// is more than a restatement: it is created, and the note says why.
func TestIntentOrOtherPathsBlockCorroboration(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	served := promoted(t, dir, ws, "author", portFact, func(r *RememberRequest) { r.Paths = []string{"svc/config.go"} })
	for name, extra := range map[string]func(*RememberRequest){
		"supersedes": func(r *RememberRequest) { r.Supersedes = []string{served.ID} },
		"claims": func(r *RememberRequest) {
			r.Claims = []MemoryClaim{{Subject: "payments", Predicate: "configured_as", Object: "port 8081"}}
		},
		"other paths": func(r *RememberRequest) { r.Paths = []string{"svc/other.go"} },
	} {
		got := rememberAs(t, dir, ws, ContextActor{ID: "agent-" + strings.ReplaceAll(name, " ", "-")}, portFact, extra)
		if got.Outcome != OutcomeCreated || dupIDs(got)[served.ID] != MatchExact || !notCorroborated(got) {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	if same := rememberAs(t, dir, ws, ContextActor{ID: "agent-same-path"}, portFact, withPaths("svc/config.go")); same.Outcome != OutcomeCorroborated {
		t.Fatalf("a restatement declaring the memory's own path: %+v", same)
	}
	// a corroboration records no paths, so declaring any path a pathless memory lacks
	// creates the proposal
	fact := "Nightly exports land in the exports bucket under a date prefix."
	pathless := promoted(t, dir, ws, "author", fact)
	got := rememberAs(t, dir, ws, ContextActor{ID: "agent-paths"}, fact, withPaths("jobs/export.go"))
	if got.Outcome != OutcomeCreated || dupIDs(got)[pathless.ID] != MatchExact || !slices.Equal(got.Paths, []string{"jobs/export.go"}) ||
		!notCorroborated(got) {
		t.Fatalf("paths declared against a pathless memory: %+v", got)
	}
}

func notCorroborated(e *ContextEntry) bool {
	return slices.ContainsFunc(e.Warnings, func(w string) bool { return strings.Contains(w, "was not corroborated") })
}

// Dedupe never launders quarantine: a write citing an untrusted capture is not a
// corroboration of clean memory, and a clean restatement of quarantined memory leaves
// it quarantined.
func TestDedupeNeverLaundersQuarantine(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	clean := promoted(t, dir, ws, "author", portFact)
	tainted := ContextActor{ID: "agent-web", Quarantine: "untrusted_capture:webfetch"}
	got := rememberAs(t, dir, ws, tainted, portFact)
	if got.Outcome != OutcomeCreated || got.ID == clean.ID || got.Quarantine == "" || dupIDs(got)[clean.ID] != MatchExact {
		t.Fatalf("a quarantined write corroborated clean memory: %+v", got)
	}
	if view, _ := entryView(t, dir, ws, clean.ID, false); view.Quarantine != "" {
		t.Fatalf("the clean entry took the quarantine: %+v", view)
	}
	fact := "Nightly exports land in the exports bucket under a date prefix."
	q := rememberAs(t, dir, ws, tainted, fact)
	for _, peer := range []string{"peer-1", "peer-2"} {
		if _, err := VerifyContext(dir, ws, q.ID, peer, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	restated := rememberAs(t, dir, ws, ContextActor{ID: "agent-clean"}, fact)
	if restated.Outcome != OutcomeCorroborated || restated.DuplicateOf != q.ID || restated.Quarantine == "" {
		t.Fatalf("a clean restatement of quarantined memory: %+v", restated)
	}
}

// verify(duplicate_of) merges once the gate's number of principals name one target: the
// merged entry is kept and hidden, recall(include_merged) shows it labeled, and a
// quarantined duplicate passes its mark to the survivor.
func TestDuplicateOfQuorumMergesAndCarriesQuarantine(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	survivor := promoted(t, dir, ws, "author", portFact)
	dup := rememberAs(t, dir, ws, ContextActor{ID: "agent-web", Quarantine: "untrusted_capture:webfetch"},
		"Payments listens on 8081 (the sidecar has 8080).")
	for _, peer := range []string{"peer-1", "peer-2"} {
		if _, err := VerifyContext(dir, ws, dup.ID, peer, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	vote := func(who string) *ContextEntry {
		out, err := VerifyContextOutcome(dir, ws, dup.ID, ContextActor{ID: who, Verifier: true},
			VerifyRequest{Outcome: OutcomeDuplicateOf, Target: survivor.ID, Note: "same port fact"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if first := vote("peer-1"); first.Lifecycle != "" {
		t.Fatalf("one duplicate_of verdict merged a two-peer entry: %+v", first)
	}
	if second := vote("peer-2"); second.Lifecycle != govstore.LifecycleMerged {
		t.Fatalf("the quorum did not merge: %+v", second)
	}
	if ids := recalledIDs(t, dir, ws); ids[dup.ID] || !ids[survivor.ID] {
		t.Fatalf("recall after the merge: %v", ids)
	}
	res, err := RecallWith(context.Background(), dir, ws, RecallRequest{IncludeMerged: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, e := range res["entries"].([]ContextEntry) {
		states[e.ID] = e.State
	}
	if states[dup.ID] != govstore.RankMerged {
		t.Fatalf("include_merged states %v", states)
	}
	if view, _ := entryView(t, dir, ws, survivor.ID, false); view.Quarantine != "untrusted_capture:webfetch" {
		t.Fatalf("the survivor of a quarantined duplicate is clean: %+v", view)
	}
}

// A duplicate_of verdict must name served memory: merging a verified entry into a
// pending one would leave recall serving neither, so the verdict is refused and nothing
// moves. The other direction merges.
func TestDuplicateOfNeedsAServedTarget(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	served := promoted(t, dir, ws, "author", portFact)
	pending := rememberAs(t, dir, ws, ContextActor{ID: "agent-b"}, "Payments listens on 8081 (the sidecar has 8080).")
	_, err := VerifyContextOutcome(dir, ws, served.ID, ContextActor{ID: "peer-1", Verifier: true},
		VerifyRequest{Outcome: OutcomeDuplicateOf, Target: pending.ID})
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "is pending, not served memory") {
		t.Fatalf("duplicate_of a pending target: %v", err)
	}
	if view, _ := entryView(t, dir, ws, served.ID, false); view.Lifecycle != "" || !recalledIDs(t, dir, ws)[served.ID] {
		t.Fatalf("the refused verdict moved the served entry: %+v", view)
	}
	mergeFixture(t, dir, ws, pending.ID, served.ID)
}

// A core entry may not hold a quarantine, so a quarantined duplicate cannot merge into
// one: the merge fails closed and changes nothing.
func TestMergeIntoCoreEntryRefusesQuarantinedDuplicate(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	core := promoted(t, dir, ws, "author", portFact)
	storeUpdate(t, dir, ws, func(tx govstore.Tx) error {
		_, err := tx.SetTier(context.Background(), core.ID, govstore.TierCore, govstore.Actor{Principal: "admin"})
		return err
	})
	dup := rememberAs(t, dir, ws, ContextActor{ID: "agent-web", Quarantine: "untrusted_capture:webfetch"}, "Payments listens on 8081.")
	req := VerifyRequest{Outcome: OutcomeDuplicateOf, Target: core.ID}
	if _, err := VerifyContextOutcome(dir, ws, dup.ID, ContextActor{ID: "peer-1", Verifier: true}, req); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyContextOutcome(dir, ws, dup.ID, ContextActor{ID: "peer-2", Verifier: true}, req); err == nil {
		t.Fatal("a quarantined duplicate merged into a core entry")
	}
	if view, _ := entryView(t, dir, ws, dup.ID, false); view.Lifecycle != "" {
		t.Fatalf("the refused merge moved the duplicate: %+v", view)
	}
	if view, _ := entryView(t, dir, ws, core.ID, false); view.Quarantine != "" {
		t.Fatalf("the core entry took the mark: %+v", view)
	}
}

// Quotas: a principal's pending proposals and the workspace's active memories are
// capped; a corroboration adds nothing and passes at the cap.
func TestPendingQuotaAndActiveEnvelope(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	t.Setenv("XMUSTARD_MEMORY_PENDING_QUOTA", "2")
	t.Setenv("XMUSTARD_MEMORY_ACTIVE_ENVELOPE", "4")
	served := promoted(t, dir, ws, "author", portFact)
	rememberAs(t, dir, ws, ContextActor{ID: "looper"}, "first proposal of the looping agent about builds")
	rememberAs(t, dir, ws, ContextActor{ID: "looper"}, "second proposal of the looping agent about tests")
	_, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: "third proposal about lint"}},
		ContextActor{ID: "looper"})
	var de *DomainError
	if !errors.As(err, &de) || de.Class != ClassConflict || !strings.Contains(err.Error(), "XMUSTARD_MEMORY_PENDING_QUOTA") {
		t.Fatalf("pending quota: %v", err)
	}
	if got := rememberAs(t, dir, ws, ContextActor{ID: "looper"}, portFact); got.Outcome != OutcomeCorroborated {
		t.Fatalf("a corroboration was refused at the quota: %+v", got)
	}
	rememberAs(t, dir, ws, ContextActor{ID: "other"}, "a proposal of another principal about docs")
	_, err = Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: "one memory past the envelope"}},
		ContextActor{ID: "other"})
	if !errors.As(err, &de) || !strings.Contains(err.Error(), "XMUSTARD_MEMORY_ACTIVE_ENVELOPE") {
		t.Fatalf("active envelope: %v", err)
	}
	if n := activeCount(t, dir, ws); n != 4 || served == nil {
		t.Fatalf("%d active entries, want the envelope of 4", n)
	}
}

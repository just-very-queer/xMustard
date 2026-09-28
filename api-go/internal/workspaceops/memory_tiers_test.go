package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
)

// humanActor is a human approver writing over HTTP or MCP.
func humanActor(id string) ContextActor {
	return ContextActor{ID: id, Kind: PrincipalHuman, Approver: true, Verifier: true}
}

// tiersDir is a multi-agent data dir with a human approver "hum" (a verifier too).
func tiersDir(t *testing.T) string {
	t.Helper()
	dir := multiAgentDir(t)
	mintApprover(t, dir, "hum", "verifier+human-approver", PrincipalHuman)
	return dir
}

// coreEntry is served memory a human approved, moved to the core tier by that human.
func coreEntry(t *testing.T, dir, ws, content string) *ContextEntry {
	t.Helper()
	e := approvedBy(t, dir, ws, "author", "hum", PrincipalHuman, content)
	e, err := SetMemoryTier(dir, ws, e.ID, "core", "standing rule", humanActor("hum"))
	if err != nil {
		t.Fatalf("set tier: %v", err)
	}
	if e.Tier != govstore.TierCore {
		t.Fatalf("tier not set: %+v", e)
	}
	return e
}

func projection(t *testing.T, dir, ws string, budget int) *MemoryProjection {
	t.Helper()
	p, err := CoreProjection(context.Background(), dir, ws, budget)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	return p
}

func coreIDs(p *MemoryProjection) []string {
	var out []string
	for _, m := range p.Core {
		out = append(out, m.ID)
	}
	return out
}

func withheldOf(p *MemoryProjection) map[string]string {
	out := map[string]string{}
	for _, w := range p.Withheld {
		out[w.ID] = w.Reason
	}
	return out
}

// The projection pushes only promoted, human-approved core memory, each entry pinned to
// its served revision, lists the rest of served memory in the deferred index, never
// shows pending memory, and stays within its character budget at every size.
func TestCoreProjectionRespectsBudgetAndUsesOnlyPromotedEntries(t *testing.T) {
	dir, ws := tiersDir(t), "ws"
	short := coreEntry(t, dir, ws, "releases are tagged from main only")
	long := coreEntry(t, dir, ws, strings.Repeat("the long rule holds ", 80))
	agentsOnly := promoted(t, dir, ws, "author", "agents alone approved this core rule")
	if _, err := SetMemoryTier(dir, ws, agentsOnly.ID, "core", "promote", humanActor("hum")); err != nil {
		t.Fatal(err)
	}
	pending, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "pending core",
		Content: "not yet verified", Tier: "core"}}, humanActor("hum"))
	if err != nil || pending.Promoted || pending.Tier != govstore.TierCore {
		t.Fatalf("pending core proposal: %+v %v", pending, err)
	}
	desc := "the API port"
	deferred := promoted(t, dir, ws, "author", "the api listens on 8042", func(r *RememberRequest) {
		r.Kind, r.Description = "convention", &desc
	})

	p := projection(t, dir, ws, ProjectionMaxChars)
	if got := coreIDs(p); !slices.Equal(got, []string{long.ID, short.ID}) {
		t.Fatalf("core pushed %v, want the two human-approved entries newest first", got)
	}
	if w := withheldOf(p); w[agentsOnly.ID] != injection.ReasonNeedsHumanApproval || len(w) != 1 {
		t.Fatalf("withheld %v", w)
	}
	for _, m := range p.Core {
		if !strings.Contains(p.Text, `id="`+m.ID+`" rev="`+strconv.FormatInt(m.Revision, 10)+`" trust="human_approved"`) {
			t.Fatalf("%s is not pinned to revision %d:\n%s", m.ID, m.Revision, p.Text)
		}
	}
	if strings.Contains(p.Text, pending.ID) || strings.Contains(p.Text, "not yet verified") {
		t.Fatalf("pending memory leaked into the projection:\n%s", p.Text)
	}
	// the deferred index lists the other served entries by title and description only
	if !strings.Contains(p.Text, "- "+deferred.ID+": t — the API port") || strings.Contains(p.Text, "listens on 8042") ||
		!strings.Contains(p.Text, "- "+agentsOnly.ID+": t") || p.Deferred.Entries != 2 || p.Deferred.Groups["convention"] != 1 {
		t.Fatalf("deferred index %+v:\n%s", p.Deferred, p.Text)
	}
	if p.Revision == 0 || !strings.Contains(p.Text, "memory revision "+strconv.FormatInt(p.Revision, 10)) {
		t.Fatalf("projection not stamped with its revision: %d\n%s", p.Revision, p.Text)
	}

	for _, budget := range []int{ProjectionMinChars, 700, 900, 1200, 2000, ProjectionDefaultChars, ProjectionMaxChars} {
		p := projection(t, dir, ws, budget)
		if p.Chars > budget || len([]rune(p.Text)) != p.Chars {
			t.Fatalf("budget %d: %d characters", budget, p.Chars)
		}
		for _, m := range p.Core {
			if m.ID == agentsOnly.ID || m.ID == pending.ID {
				t.Fatalf("budget %d pushed %s", budget, m.ID)
			}
		}
		if w := withheldOf(p); len(p.Core) < 2 && w[long.ID] != withheldTooLarge && w[short.ID] != withheldTooLarge {
			t.Fatalf("budget %d: a core entry was dropped without a too_large reason: %v", budget, w)
		}
	}
	if p := projection(t, dir, ws, 1200); !slices.Equal(coreIDs(p), []string{short.ID}) || withheldOf(p)[long.ID] != withheldTooLarge {
		t.Fatalf("at 1200 characters the long entry must be withheld too_large: %v %v", coreIDs(p), withheldOf(p))
	}
	if _, err := CoreProjection(context.Background(), dir, ws, ProjectionMinChars-1); !IsInvalidInput(err) {
		t.Fatalf("a budget under the minimum must be rejected, not clamped: %v", err)
	}
}

// The projection is cached by revision and pins the approved revision: when an edit
// is accepted by agents, the served revision has no human approval and is withheld
// until a human approves that revision; any write changes the revision.
func TestProjectionPinsTheApprovedRevision(t *testing.T) {
	dir, ws := tiersDir(t), "ws"
	mintApprover(t, dir, "root", "admin", PrincipalHuman)
	core := coreEntry(t, dir, ws, "the build runs make all")
	first := projection(t, dir, ws, 0)
	if !slices.Equal(coreIDs(first), []string{core.ID}) || first.Core[0].Revision != 1 || first.Cached {
		t.Fatalf("first projection: %+v", first)
	}
	if again := projection(t, dir, ws, 0); !again.Cached || again.Revision != first.Revision || again.Text != first.Text {
		t.Fatalf("an unchanged workspace must reuse the projection: %+v", again)
	}
	// only a human approver may edit a core entry; an admin human edits, agents accept
	root := ContextActor{ID: "root", Kind: PrincipalHuman, Admin: true, Approver: true}
	if _, err := Remember(dir, ws, RememberRequest{Op: "edit", EntryID: core.ID, BaseRevision: 1, Reason: "rename",
		OldString: "make all", NewString: "make build"}, ContextActor{ID: "author"}); !isPolicyError(err, RuleProtectedField) {
		t.Fatalf("an agent edited a core entry: %v", err)
	}
	if _, err := Remember(dir, ws, RememberRequest{Op: "edit", EntryID: core.ID, BaseRevision: 1, Reason: "rename",
		OldString: "make all", NewString: "make build"}, root); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []string{"peer-1", "peer-2"} {
		if _, err := VerifyContextOutcome(dir, ws, core.ID, ContextActor{ID: peer, Verifier: true},
			VerifyRequest{Outcome: OutcomeApprove, Revision: 2}); err != nil {
			t.Fatal(err)
		}
	}
	edited := projection(t, dir, ws, 0)
	if edited.Cached || edited.Revision <= first.Revision || len(edited.Core) != 0 ||
		withheldOf(edited)[core.ID] != injection.ReasonNeedsHumanApproval {
		t.Fatalf("revision 2 has no human approval and must be withheld: %+v", edited)
	}
	if _, err := VerifyContextOutcome(dir, ws, core.ID, humanActor("hum"), VerifyRequest{Outcome: OutcomeApprove}); err != nil {
		t.Fatal(err)
	}
	approved := projection(t, dir, ws, 0)
	if len(approved.Core) != 1 || approved.Core[0].Revision != 2 || !strings.Contains(approved.Text, `rev="2"`) ||
		!strings.Contains(approved.Text, "make build") || strings.Contains(approved.Text, "make all") {
		t.Fatalf("the approved revision 2 must be pushed: %+v\n%s", approved.Core, approved.Text)
	}
}

func isPolicyError(err error, rule string) bool {
	var pv *MemoryPolicyError
	return errors.As(err, &pv) && slices.ContainsFunc(pv.Violations, func(v MemoryPolicyViolation) bool { return v.Rule == rule })
}

// memoryRevision reads the workspace's memory revision as ground reports it.
func memoryRevision(t *testing.T, dir, ws string) int64 {
	t.Helper()
	var g groundingMemory
	if unknown := g.build(dir, ws, "me", groundMemoryOptions{since: noSince}); len(unknown) != 0 || g.MemoryRevision == nil {
		t.Fatalf("ground memory: %v", unknown)
	}
	return *g.MemoryRevision
}

// ground(since_revision) returns only what changed in served memory after that
// revision: added, changed, superseded, stale and removed entries, each once under its
// newest change; entries that did not change are not listed.
func TestSinceRevisionReturnsOnlyChangedEntries(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	steady := promoted(t, dir, ws, "author", "steady fact")
	edited := promoted(t, dir, ws, "author", "the timeout is 30s")
	replaced := promoted(t, dir, ws, "author", "the old way")
	retracted := promoted(t, dir, ws, "author", "wrong fact")
	drifted := promoted(t, dir, ws, "author", "the parser lives in x.go", func(r *RememberRequest) { r.Paths = []string{"x.go"} })
	since := memoryRevision(t, dir, ws)

	added := promoted(t, dir, ws, "author", "a new fact")
	if _, err := Remember(dir, ws, RememberRequest{Op: "edit", EntryID: edited.ID, BaseRevision: 1, Reason: "measured",
		OldString: "30s", NewString: "45s"}, ContextActor{ID: "author"}); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []string{"peer-1", "peer-2"} {
		if _, err := VerifyContextOutcome(dir, ws, edited.ID, ContextActor{ID: peer, Verifier: true},
			VerifyRequest{Outcome: OutcomeApprove, Revision: 2}); err != nil {
			t.Fatal(err)
		}
	}
	replacement := promoted(t, dir, ws, "author", "the new way", func(r *RememberRequest) {
		r.Op, r.Supersedes = "supersede", []string{replaced.ID}
	})
	if _, err := RetractContext(dir, ws, retracted.ID, "it was wrong", ContextActor{ID: "hum", Approver: true}); err != nil {
		t.Fatal(err)
	}
	if err := memoryUpdate(context.Background(), dir, ws, func(tx govstore.Tx) error {
		_, err := tx.MarkStale(context.Background(), drifted.ID, []govstore.AnchorRef{{Kind: govstore.AnchorPath, Value: "x.go"}},
			"", govstore.Actor{Principal: "watcher"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// a pending proposal is not served: it is no change to served memory
	if _, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "p", Content: "pending"}},
		ContextActor{ID: "author"}); err != nil {
		t.Fatal(err)
	}

	var g groundingMemory
	if unknown := g.build(dir, ws, "me", groundMemoryOptions{since: since}); len(unknown) != 0 {
		t.Fatal(unknown)
	}
	d := g.MemoryDelta
	if d == nil || d.Since != since || d.Revision != *g.MemoryRevision || d.Revision <= since || d.Truncated {
		t.Fatalf("delta header: %+v", d)
	}
	sorted := func(ids ...string) []string { return slices.Sorted(slices.Values(ids)) }
	want := map[string][]string{
		"added": sorted(added.ID, replacement.ID), "changed": {edited.ID}, "stale": {drifted.ID}, "removed": {retracted.ID},
	}
	got := map[string][]string{"added": sorted(d.Added...), "changed": d.Changed, "stale": d.Stale, "removed": d.Removed}
	for k, ids := range want {
		if !slices.Equal(got[k], ids) {
			t.Errorf("%s = %v, want %v", k, got[k], ids)
		}
	}
	if len(d.Superseded) != 1 || d.Superseded[0] != (SupersededMemory{ID: replaced.ID, By: replacement.ID}) {
		t.Errorf("superseded = %+v", d.Superseded)
	}
	if raw := mustJSONString(t, d); strings.Contains(raw, steady.ID) {
		t.Errorf("an unchanged entry is listed: %s", raw)
	}
	// from the current revision nothing changed
	var again groundingMemory
	again.build(dir, ws, "me", groundMemoryOptions{since: *g.MemoryRevision})
	if d := again.MemoryDelta; len(d.Added)+len(d.Changed)+len(d.Superseded)+len(d.Removed) != 0 {
		t.Fatalf("nothing changed after the current revision: %+v", d)
	}
	// without since_revision there is no delta
	if again.build(dir, ws, "me", groundMemoryOptions{since: noSince}); again.MemoryDelta != nil {
		t.Fatalf("a delta without since_revision: %+v", again.MemoryDelta)
	}
}

// adminHuman authenticates an admin who is a human approver, from a token file.
func adminHuman(t *testing.T, dir, ws, id string) HumanApprover {
	t.Helper()
	raw, err := MintIdentityToken(dir, id, RoleAdmin, 0, nil, TokenIdentity{Kind: PrincipalHuman})
	if err != nil {
		t.Fatal(err)
	}
	h, err := AuthorizeHumanApprover(dir, ws, raw, TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func setPolicy(t *testing.T, dir, ws string, p MemoryPolicy) {
	t.Helper()
	if _, err := SetMemoryPolicy(dir, ws, p, adminHuman(t, dir, ws, "root")); err != nil {
		t.Fatalf("set policy: %v", err)
	}
}

// A write that breaks the policy is refused with every violation as {field, rule,
// limit, actual}; protected fields and patterns need a human-approver principal.
func TestPolicyViolationsAreStructuredAndProtectedMemoryNeedsAHumanApprover(t *testing.T) {
	dir, ws := tiersDir(t), "ws"
	p := DefaultMemoryPolicy()
	p.MaxEntryChars, p.MaxTopicDepth = 256, 2
	p.RequiredFields, p.AllowedKinds = []string{"title", "kind"}, []string{"convention", "decision"}
	p.ProtectedPatterns = []ProtectedPattern{{Name: "deploy", Field: "path", Pattern: `^deploy/`},
		{Name: "credentials", Field: "content", Pattern: `(?i)production credentials`}}
	setPolicy(t, dir, ws, p)

	_, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Content: strings.Repeat("x", 300), Kind: "gotcha", Topic: "a/b/c"}}, ContextActor{ID: "agent"})
	var pv *MemoryPolicyError
	if !errors.As(err, &pv) || !IsInvalidInput(err) || pv.NeedsHumanApprover() {
		t.Fatalf("want a structured policy refusal, got %v", err)
	}
	got := map[string]MemoryPolicyViolation{}
	for _, v := range pv.Violations {
		got[v.Field+"/"+v.Rule] = v
	}
	want := map[string][2]any{
		"title/required": {true, false}, "kind/allowed": {"convention,decision", "gotcha"},
		"content/max_chars": {256, 300}, "topic/max_depth": {2, 3},
	}
	if len(got) != len(want) {
		t.Fatalf("violations %+v, want %v", pv.Violations, want)
	}
	for k, lv := range want {
		if v, ok := got[k]; !ok || v.Limit != lv[0] || v.Actual != lv[1] {
			t.Fatalf("violation %s = %+v, want limit %v actual %v", k, v, lv[0], lv[1])
		}
	}
	if b := mustJSONString(t, pv); !strings.Contains(b, `{"field":"content","rule":"max_chars","limit":256,"actual":300}`) {
		t.Fatalf("violation JSON: %s", b)
	}

	ok := ProposeContextRequest{Title: "deploys", Content: "run the deploy script", Kind: "convention"}
	for name, mutate := range map[string]func(*RememberRequest){
		"protected path":    func(r *RememberRequest) { r.Paths = []string{"deploy/prod.yaml"} },
		"protected content": func(r *RememberRequest) { r.Content = "rotate the Production Credentials weekly" },
		"protected tier":    func(r *RememberRequest) { r.Tier = "core" },
	} {
		req := RememberRequest{ProposeContextRequest: ok}
		mutate(&req)
		_, err := Remember(dir, ws, req, ContextActor{ID: "agent", Kind: PrincipalAgent})
		if !errors.As(err, &pv) || !pv.NeedsHumanApprover() {
			t.Fatalf("%s: an agent wrote protected memory: %v", name, err)
		}
		// a human who is not a human approver is refused too; a human approver writes it
		if _, err := Remember(dir, ws, req, ContextActor{ID: "bob", Kind: PrincipalHuman}); !errors.As(err, &pv) {
			t.Fatalf("%s: a human without the role wrote protected memory: %v", name, err)
		}
		if _, err := Remember(dir, ws, req, humanActor("hum")); err != nil {
			t.Fatalf("%s: the human approver was refused: %v", name, err)
		}
	}
	// open mode has no human approver: its write of protected memory is refused
	if _, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "c",
		Kind: "decision", Tier: "core"}}, ContextActor{ID: OpenModeIdentity, OpenMode: true, Admin: true, Approver: true}); !errors.As(err, &pv) {
		t.Fatalf("open mode wrote a core entry: %v", err)
	}
}

// The policy config changes only under an admin's human token; an invalid config is
// refused; each change is an event with the approval label; an unreadable stored
// policy refuses writes and pushes (fail closed).
func TestPolicyConfigChangesNeedAnAdminHumanApprover(t *testing.T) {
	dir, ws := tiersDir(t), "ws"
	p := DefaultMemoryPolicy()
	p.MaxEntryChars = 1000
	raw, err := MintIdentityToken(dir, "carol", RoleHumanApprover, 0, nil, TokenIdentity{Kind: PrincipalHuman})
	if err != nil {
		t.Fatal(err)
	}
	approver, err := AuthorizeHumanApprover(dir, ws, raw, TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetMemoryPolicy(dir, ws, p, approver); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("a human approver who is not an admin changed the policy: %v", err)
	}
	agentAdmin := HumanApprover{Principal: Principal{ID: "bot", Kind: PrincipalAgent, Role: RoleAdmin, Roles: []string{RoleAdmin}}, Assurance: AssuranceAdvisory}
	if _, err := SetMemoryPolicy(dir, ws, p, agentAdmin); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("an agent admin changed the policy: %v", err)
	}
	root := adminHuman(t, dir, ws, "root")
	bad := DefaultMemoryPolicy()
	bad.ProtectedFields, bad.CoreBudgetChars = []string{"permission"}, 100000
	bad.PushApproval = map[string]string{"core": "anyone"}
	if _, err := SetMemoryPolicy(dir, ws, bad, root); !isPolicyError(err, RuleRequired) || !isPolicyError(err, "range") ||
		!isPolicyError(err, RuleAllowed) {
		t.Fatalf("an invalid policy was accepted: %v", err)
	}
	change, err := SetMemoryPolicy(dir, ws, p, root)
	if err != nil || !change.Changed || change.Seq == 0 || change.Approval != "ops/advisory" {
		t.Fatalf("admin change: %+v %v", change, err)
	}
	if loaded, err := LoadMemoryPolicy(dir, ws); err != nil || loaded.MaxEntryChars != 1000 || loaded.Digest() != change.Digest {
		t.Fatalf("stored policy: %+v %v", loaded, err)
	}
	if again, err := SetMemoryPolicy(dir, ws, p, root); err != nil || again.Changed {
		t.Fatalf("an identical policy is no change: %+v %v", again, err)
	}
	var events []govstore.Event
	if err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		events, err = r.ListEvents(context.Background(), govstore.EventFilter{WorkspaceID: ws, Types: []string{govstore.EventMemoryPolicy}})
		return err
	}); err != nil || len(events) != 1 || events[0].Principal != "root" || !strings.Contains(string(events[0].Data), `"approval":"ops/advisory"`) {
		t.Fatalf("policy events: %+v %v", events, err)
	}
	// fail closed on a policy that cannot be read
	if err := os.WriteFile(memoryPolicyPath(dir, ws), []byte(`{"max_entry_chars":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "c"}},
		ContextActor{ID: "agent"}); !errors.Is(err, ErrMemoryPolicyUnreadable) {
		t.Fatalf("a write passed an unreadable policy: %v", err)
	}
	if _, err := AdmitMemory(context.Background(), dir, ws, injection.SurfaceCore, nil); !errors.Is(err, ErrMemoryPolicyUnreadable) {
		t.Fatalf("a push passed an unreadable policy: %v", err)
	}
}

// Onboarding appears only while the workspace has no promoted memory: pending
// proposals do not end it, the first promoted memory does.
func TestOnboardingAppearsOnlyWithoutPromotedMemory(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	check := func(want bool) {
		t.Helper()
		var g groundingMemory
		if unknown := g.build(dir, ws, "me", groundMemoryOptions{since: noSince, budget: true}); len(unknown) != 0 {
			t.Fatal(unknown)
		}
		if got := g.Onboarding != nil; got != want {
			t.Fatalf("onboarding = %v, want %v", g.Onboarding, want)
		}
		if want && (!g.Onboarding.Needed || len(g.Onboarding.Propose) != 4 || g.Onboarding.Propose[0].Category != "build_and_test") {
			t.Fatalf("onboarding checklist: %+v", g.Onboarding)
		}
		if p := projection(t, dir, ws, 0); p.Onboarding != want || strings.Contains(p.Text, "no promoted memory yet") != want {
			t.Fatalf("projection onboarding = %v:\n%s", p.Onboarding, p.Text)
		}
		if g.MemoryBudget == nil || g.MemoryBudget.TokenEstimator == "" || g.MemoryBudget.ProjectionChars == 0 {
			t.Fatalf("memory_budget: %+v", g.MemoryBudget)
		}
	}
	check(true)
	if _, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "pending"}},
		ContextActor{ID: "author"}); err != nil {
		t.Fatal(err)
	}
	check(true)
	promoted(t, dir, ws, "author", "the first promoted fact")
	check(false)
}

// Tier changes are governed: only a human approver changes a tier, only served memory
// enters the core tier, a quarantined entry never does, the core budget holds, and the
// reason is recorded on the tier_change event.
func TestSetMemoryTierIsGoverned(t *testing.T) {
	dir, ws := tiersDir(t), "ws"
	p := DefaultMemoryPolicy()
	p.CoreBudgetChars = 500
	setPolicy(t, dir, ws, p)
	served := promoted(t, dir, ws, "author", "a served fact")
	if _, err := SetMemoryTier(dir, ws, served.ID, "core", "promote", ContextActor{ID: "peer-1", Kind: PrincipalAgent, Verifier: true}); !isPolicyError(err, RuleProtectedField) {
		t.Fatalf("an agent changed a tier: %v", err)
	}
	pending, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "pending"}},
		ContextActor{ID: "author"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetMemoryTier(dir, ws, pending.ID, "core", "promote", humanActor("hum")); !IsInvalidInput(err) {
		t.Fatalf("a pending entry entered the core tier: %v", err)
	}
	big := promoted(t, dir, ws, "author", strings.Repeat("y", 600))
	if _, err := SetMemoryTier(dir, ws, big.ID, "core", "promote", humanActor("hum")); !isPolicyError(err, RuleCoreBudget) {
		t.Fatalf("the core budget did not hold: %v", err)
	}
	web := capturedEvidence(t, dir, ws, "author", "WebFetch")
	quarantined, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "q", Content: "from the web"},
		Evidence: []string{web}}, ContextActor{ID: "author"})
	if err != nil || quarantined.Quarantine == "" {
		t.Fatalf("quarantined proposal: %+v %v", quarantined, err)
	}
	for _, peer := range []string{"peer-1", "peer-2"} {
		if _, err := VerifyContext(dir, ws, quarantined.ID, peer, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SetMemoryTier(dir, ws, quarantined.ID, "core", "promote", humanActor("hum")); !IsInvalidInput(err) {
		t.Fatalf("a quarantined entry entered the core tier: %v", err)
	}
	for _, bad := range []struct{ tier, reason string }{{"", "r"}, {"hot", "r"}, {"core", " "}} {
		if _, err := SetMemoryTier(dir, ws, served.ID, bad.tier, bad.reason, humanActor("hum")); !IsInvalidInput(err) {
			t.Fatalf("tier %q reason %q: %v", bad.tier, bad.reason, err)
		}
	}
	e, err := SetMemoryTier(dir, ws, served.ID, "core", "standing rule for every session", humanActor("hum"))
	if err != nil || e.Tier != govstore.TierCore {
		t.Fatalf("human approver: %+v %v", e, err)
	}
	// replacing a core entry retires it from the core tier: a human approver's write
	replace := RememberRequest{Op: "supersede", ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "the new fact",
		Supersedes: []string{served.ID}}}
	if _, err := Remember(dir, ws, replace, ContextActor{ID: "author", Kind: PrincipalAgent}); !isPolicyError(err, RuleProtectedField) {
		t.Fatalf("an agent superseded a core entry: %v", err)
	}
	if _, err := Remember(dir, ws, replace, humanActor("hum")); err != nil {
		t.Fatalf("a human approver's replacement of a core entry: %v", err)
	}
	if e, err = Remember(dir, ws, RememberRequest{Op: "tier", EntryID: served.ID, Reason: "demote", ProposeContextRequest: ProposeContextRequest{Tier: "deferred"}},
		humanActor("hum")); err != nil || e.Tier != "" {
		t.Fatalf("remember op=tier: %+v %v", e, err)
	}
	var events []govstore.Event
	if err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		events, err = r.ListEvents(context.Background(), govstore.EventFilter{EntryID: served.ID, Types: []string{govstore.EventTierChange}})
		return err
	}); err != nil || len(events) != 2 || events[0].Note != "standing rule for every session" || events[0].Principal != "hum" {
		t.Fatalf("tier events: %+v %v", events, err)
	}
	// recall filters by tier; an entry stored with no tier is deferred
	res, err := RecallWith(context.Background(), dir, ws, RecallRequest{Tier: "deferred", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if entries := res["entries"].([]ContextEntry); len(entries) == 0 || slices.ContainsFunc(entries, func(e ContextEntry) bool { return e.Tier == govstore.TierCore }) {
		t.Fatalf("recall tier=deferred: %+v", entries)
	}
	if _, err := RecallWith(context.Background(), dir, ws, RecallRequest{Tier: "hot"}); !IsInvalidInput(err) {
		t.Fatalf("an unknown tier must be rejected: %v", err)
	}
}

// A policy may ask more of a pushed surface than any human approval: user_presence
// admits only an approval cast with a presence-only token typed at the ops terminal
// (ops/user_presence); an advisory human approval is withheld.
func TestUserPresencePushApproval(t *testing.T) {
	dir, ws := tiersDir(t), "ws"
	p := DefaultMemoryPolicy()
	p.PushApproval = map[string]string{"core": PushApprovalUserPresence}
	setPolicy(t, dir, ws, p)
	advisory := coreEntry(t, dir, ws, "advisory approval only")

	raw, err := MintIdentityToken(dir, "erin", RoleHumanApprover, 0, nil, TokenIdentity{Kind: PrincipalHuman, PresenceOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	present, err := AuthorizeHumanApprover(dir, ws, raw, TokenTyped)
	if err != nil || present.Assurance != AssuranceUserPresence {
		t.Fatalf("presence-only approver: %+v %v", present, err)
	}
	e, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "typed approval"}},
		ContextActor{ID: "author"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyContext(dir, ws, e.ID, "peer-1", true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := HumanVerdict(dir, ws, e.ID, present, OutcomeApprove, 0, "read it"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetMemoryTier(dir, ws, e.ID, "core", "standing", present.TierActor()); err != nil {
		t.Fatal(err)
	}
	pr := projection(t, dir, ws, 0)
	if !slices.Equal(coreIDs(pr), []string{e.ID}) || withheldOf(pr)[advisory.ID] != injection.ReasonNeedsHumanApproval {
		t.Fatalf("user_presence admitted %v, withheld %v", coreIDs(pr), withheldOf(pr))
	}
	// the hook surface keeps the default level: any human approval
	if res := admit(t, dir, ws, injection.SurfaceHook, advisory.ID); len(res.Admitted) != 1 {
		t.Fatalf("hook surface at human_approver: %+v", res)
	}
}

// The token report accounts for what the projection pushes.
func TestMemoryTokenReport(t *testing.T) {
	dir, ws := tiersDir(t), "ws"
	core := coreEntry(t, dir, ws, "the build runs make all")
	rep, err := MemoryTokens(context.Background(), dir, ws, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Core) != 1 || rep.Core[0].ID != core.ID || rep.CoreTokensEst == 0 || rep.CoreChars != memoryChars("deploys", "the build runs make all") ||
		rep.Projection.TokensEst == 0 || rep.Projection.Chars > rep.Projection.Budget || rep.TokenEstimator == "" {
		t.Fatalf("token report: %+v", rep)
	}
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), `"content"`) || strings.Contains(string(b), `"frame"`) {
		t.Fatalf("the report carries memory text: %s", b)
	}
}

// projectionBenchDir holds eight human-approved core memories of about 400 bytes and
// deferred single-agent memories, n in all.
func projectionBenchDir(b *testing.B, deferred int) (string, string) {
	b.Helper()
	dir, ws := b.TempDir(), "ws"
	single := false
	if err := writeJSON(dir+"/settings.json", appSettings{RequireMultiAgentVerification: &single}); err != nil {
		b.Fatal(err)
	}
	if _, err := MintIdentityToken(dir, "hum", "verifier+human-approver", 0, nil, TokenIdentity{Kind: PrincipalHuman}); err != nil {
		b.Fatal(err)
	}
	body := strings.Repeat("Deploys go through make release after the checks pass on main. ", 6)
	for i := range 8 {
		e, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "deploy rule " + strconv.Itoa(i),
			Content: body + strings.Repeat("x", i)}}, ContextActor{ID: "author"})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: "hum", Kind: PrincipalHuman, Verifier: true}, VerifyRequest{}); err != nil {
			b.Fatal(err)
		}
		if _, err := SetMemoryTier(dir, ws, e.ID, "core", "bench", humanActor("hum")); err != nil {
			b.Fatal(err)
		}
	}
	desc := "one line that says what the entry holds"
	for i := range deferred {
		if _, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "fact " + strconv.Itoa(i),
			Content: "the service " + strconv.Itoa(i) + " listens on its own port", Kind: MemoryKinds[i%len(MemoryKinds)]},
			Description: &desc}, ContextActor{ID: "author"}); err != nil {
			b.Fatal(err)
		}
	}
	return dir, ws
}

// BenchmarkCoreProjectionCompile measures compiling the projection at the default
// budget (8 core entries admitted, 500 deferred entries indexed): the cold path after
// every memory write.
func BenchmarkCoreProjectionCompile(b *testing.B) {
	dir, ws := projectionBenchDir(b, 500)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		projectionCache.Lock()
		clear(projectionCache.m)
		projectionCache.Unlock()
		p, err := CoreProjection(ctx, dir, ws, 0)
		if err != nil || len(p.Core) != 8 || p.Cached {
			b.Fatalf("projection: %v %+v", err, p)
		}
	}
}

// BenchmarkCoreProjectionCached measures the warm path: the revision read that proves
// the cached projection current.
func BenchmarkCoreProjectionCached(b *testing.B) {
	dir, ws := projectionBenchDir(b, 500)
	ctx := context.Background()
	if _, err := CoreProjection(ctx, dir, ws, 0); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		p, err := CoreProjection(ctx, dir, ws, 0)
		if err != nil || !p.Cached {
			b.Fatalf("projection: %v %+v", err, p)
		}
	}
}

// BenchmarkSinceRevisionDelta measures ground's memory section with a delta over 200
// changes.
func BenchmarkSinceRevisionDelta(b *testing.B) {
	dir, ws := projectionBenchDir(b, 0)
	var g groundingMemory
	g.build(dir, ws, "me", groundMemoryOptions{since: noSince})
	since := *g.MemoryRevision
	projectionBenchSingle(b, dir, ws, 200)
	b.ReportAllocs()
	for b.Loop() {
		var g groundingMemory
		if unknown := g.build(dir, ws, "me", groundMemoryOptions{since: since}); len(unknown) != 0 || len(g.MemoryDelta.Added) != 200 {
			b.Fatalf("delta: %v %+v", unknown, g.MemoryDelta)
		}
	}
}

// projectionBenchSingle adds n single-agent memories.
func projectionBenchSingle(b *testing.B, dir, ws string, n int) {
	b.Helper()
	for i := range n {
		if _, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "added " + strconv.Itoa(i),
			Content: "a fact added after the revision " + strconv.Itoa(i)}}, ContextActor{ID: "author"}); err != nil {
			b.Fatal(err)
		}
	}
}

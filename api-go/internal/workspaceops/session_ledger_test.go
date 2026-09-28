package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/govstore"
)

const ledgerWS = "wsLedger"

// newLedgerFixture is a data dir with a registered workspace whose root holds two files.
// The ledger writer is flushed before the data dir is removed.
func newLedgerFixture(t testing.TB, requireMulti bool) (dir, root string) {
	t.Helper()
	dir = t.TempDir()
	t.Cleanup(func() { FlushSessionLedger(dir, 10*time.Second) })
	root = canonicalRoot(t.TempDir())
	for _, rel := range []string{"src/a.go", "src/b.go"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package src // "+rel+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registerRoots(t, dir, map[string]string{ledgerWS: root})
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &requireMulti, ContextVerificationThreshold: 2})
	return dir, root
}

func ledgerSession(root, client, session, principal string) LedgerSession {
	return LedgerSession{WorkspaceID: ledgerWS, Root: root, Client: client, Session: session, Principal: principal}
}

// recordAll queues the events for one session and waits until they are written.
func recordAll(t testing.TB, dir string, s LedgerSession, evs ...LedgerEvent) {
	t.Helper()
	for _, ev := range evs {
		ev.LedgerSession = s
		if !RecordSession(dir, ev) {
			t.Fatalf("event %s dropped", ev.Kind)
		}
	}
	flushLedger(t, dir)
}

func flushLedger(t testing.TB, dir string) {
	t.Helper()
	if !FlushSessionLedger(dir, 10*time.Second) {
		t.Fatal("the ledger did not settle")
	}
}

// ledgerRows reads every row of a session, oldest first.
func ledgerRows(t *testing.T, dir, key string) []govstore.SessionEvent {
	t.Helper()
	var out []govstore.SessionEvent
	err := memoryView(context.Background(), dir, ledgerWS, func(r govstore.Reader) error {
		after := int64(0)
		for {
			page, err := r.ListSessionEvents(context.Background(), govstore.SessionEventFilter{SessionID: key, AfterSeq: after, Limit: 1000})
			if err != nil {
				return err
			}
			out = append(out, page...)
			if len(page) < 1000 {
				return nil
			}
			after = page[len(page)-1].Seq
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func countKinds(rows []govstore.SessionEvent, kinds ...string) int {
	n := 0
	for _, r := range rows {
		if slices.Contains(kinds, r.Kind) {
			n++
		}
	}
	return n
}

func future() string { return time.Now().Add(time.Hour).UTC().Format(time.RFC3339) }

func TestExecutionKey(t *testing.T) {
	for _, c := range []struct{ client, session, agent, want string }{
		{"claude", "sess-7f3a", "", "claude:sess-7f3a"},
		{"claude", "sess-7f3a", "a1", "claude:sess-7f3a:agent:a1"},
		{"codex", "019a-b2", "", "codex:019a-b2"},
	} {
		if got := ExecutionKey(c.client, c.session, c.agent); got != c.want || !executionKeyPattern.MatchString(got) {
			t.Errorf("ExecutionKey(%q, %q, %q) = %q, want %q", c.client, c.session, c.agent, got, c.want)
		}
	}
	// a component that is not a plain id becomes a digest of it: never a delimiter, a
	// path or a traversal, and distinct inputs stay distinct
	for _, bad := range []string{"a:b", "../x", "a..b", "with space", strings.Repeat("x", 65), ""} {
		key := ExecutionKey("claude", bad, "")
		if !executionKeyPattern.MatchString(key) || strings.Contains(key, "..") || !strings.HasPrefix(key, "claude:h-") {
			t.Errorf("session %q gave key %q", bad, key)
		}
	}
	if ExecutionKey("claude", "a:b", "") == ExecutionKey("claude", "a:c", "") {
		t.Fatal("distinct sessions share a key")
	}
}

// The snapshot is at most 2 KB rendered, keeps a prefix of the priority order (goal,
// failures, files, proposals, decisions, next step) and counts what it left out, and
// every handle it names expands; an expired handle is dropped.
func TestSnapshotIsBoundedPrioritizedAndExpandable(t *testing.T) {
	dir, root := newLedgerFixture(t, true)
	handle := retainedEvidence(t, dir, ledgerWS, "agent-a")
	s := ledgerSession(root, "claude", "s1", "agent-a")
	evs := []LedgerEvent{{Kind: LedgerPrompt, Text: strings.Repeat("fix the rounding bug in billing ", 20)}}
	for i := range 12 {
		evs = append(evs, LedgerEvent{Kind: LedgerToolCall, Tool: "Bash", Args: fmt.Sprintf("d%02d", i), Status: LedgerError,
			Text: fmt.Sprintf("go test ./pkg%02d/... -run TestRounding%02d", i, i), Signature: "--- FAIL: TestRounding",
			Handle: handle, ExpiresAt: future()})
	}
	for i := range 30 {
		evs = append(evs, LedgerEvent{Kind: LedgerFileModified, Tool: "Edit", Path: fmt.Sprintf("src/file_%02d.go", i)})
	}
	recordAll(t, dir, s, evs...)
	snap, err := BuildSessionSnapshot(context.Background(), dir, ledgerWS, "claude:s1")
	if err != nil {
		t.Fatal(err)
	}
	text := snap.Render()
	if len(text) > SnapshotMaxBytes {
		t.Fatalf("snapshot is %d bytes, over %d:\n%s", len(text), SnapshotMaxBytes, text)
	}
	// every item is kept or counted, and the goal and the newest failures come first
	if snap.Goal == "" || len(snap.OpenFailures)+snap.Omitted["failure"] != 12 || len(snap.ActiveFiles)+snap.Omitted["file"] != 30 ||
		len(snap.OpenFailures) == 0 || len(snap.Omitted) == 0 {
		t.Fatalf("snapshot %+v\n%s", snap, text)
	}
	cut := false
	for _, tier := range snapshotTiers {
		kept := len(tier.lines(snap))
		if cut && kept > 0 {
			t.Fatalf("tier %s kept %d items after a higher tier was cut", tier.name, kept)
		}
		cut = cut || snap.Omitted[tier.name] > 0
	}
	if want := "go test ./pkg11/"; !strings.Contains(snap.OpenFailures[0].What, want) {
		t.Fatalf("failures are newest first: %+v", snap.OpenFailures[0])
	}
	if !strings.Contains(text, "expand a handle: resources/read xmustard://evidence/") || !strings.Contains(text, "omitted for size: ") {
		t.Fatalf("snapshot text:\n%s", text)
	}
	store := evidence.NewStore(dir, evidence.DefaultLimits())
	for _, f := range snap.OpenFailures {
		if f.Handle == "" || !strings.Contains(text, f.Handle) {
			t.Fatalf("failure without its handle: %+v", f)
		}
		if _, err := store.Read(context.Background(), evidence.ReadRequest{WorkspaceID: ledgerWS, Handle: f.Handle,
			Actor: "agent-a", AuthEnforced: true, Length: 16}); err != nil {
			t.Fatalf("handle %s does not expand: %v", f.Handle, err)
		}
	}
	// an expired original is not offered
	recordAll(t, dir, ledgerSession(root, "claude", "s2", "agent-a"), LedgerEvent{Kind: LedgerToolCall, Tool: "Bash",
		Args: "x", Status: LedgerError, Text: "make", Handle: handle, ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)})
	old, err := BuildSessionSnapshot(context.Background(), dir, ledgerWS, "claude:s2")
	if err != nil || len(old.OpenFailures) != 1 || old.OpenFailures[0].Handle != "" || strings.Contains(old.Render(), "expand a handle") {
		t.Fatalf("expired handle kept: %v %+v", err, old)
	}
}

// Whatever the tiers hold, the fitted snapshot stays within the bound, keeps a prefix of
// the priority order and counts every item it left out.
func TestSnapshotFitKeepsAPrefixWithinTheBound(t *testing.T) {
	long := func(c string, n int) string { return strings.Repeat(c, n) }
	for n := 0; n <= 60; n += 4 {
		s := &SessionSnapshot{Schema: SnapshotSchema, SessionID: "claude:" + long("s", 64) + ":agent:" + long("a", 64),
			ThroughSeq: 1 << 40, At: time.Now().UTC().Format(time.RFC3339), WorkspaceID: long("w", 64),
			Goal: long("g", snapGoalMax), NextStep: long("n", snapGoalMax)}
		for i := range n {
			s.OpenFailures = append(s.OpenFailures, SnapshotFailure{Tool: "Bash", What: long("c", snapItemMax),
				Signature: long("e", snapItemMax), Handle: "xm1." + long("h", 43)})
			s.ActiveFiles = append(s.ActiveFiles, SnapshotFile{Path: fmt.Sprintf("%s%04d", long("p", snapItemMax-4), i), Modified: i%2 == 0})
			s.PendingProposals = append(s.PendingProposals, SnapshotProposal{ID: "ctx_0123456789ab", Title: long("t", snapTitleMax), VotesNeeded: 2})
			s.Decisions = append(s.Decisions, long("d", snapItemMax))
		}
		total := map[string]int{}
		for _, tier := range snapshotTiers {
			total[tier.name] = len(tier.lines(s))
		}
		s.fit(SnapshotMaxBytes)
		if text := s.Render(); len(text) > SnapshotMaxBytes {
			t.Fatalf("n=%d: %d bytes", n, len(text))
		}
		cut := false
		for _, tier := range snapshotTiers {
			kept := len(tier.lines(s))
			if kept+s.Omitted[tier.name] != total[tier.name] || cut && kept > 0 {
				t.Fatalf("n=%d: tier %s kept %d, omitted %d of %d (a higher tier cut: %v)", n, tier.name, kept, s.Omitted[tier.name], total[tier.name], cut)
			}
			cut = cut || s.Omitted[tier.name] > 0
		}
	}
}

// A failure followed by a passing run of the same call is not open.
func TestSnapshotFailureClosedByAPassingRun(t *testing.T) {
	dir, root := newLedgerFixture(t, true)
	recordAll(t, dir, ledgerSession(root, "claude", "s1", "agent-a"),
		LedgerEvent{Kind: LedgerToolCall, Tool: "Bash", Args: "t", Status: LedgerError, Text: "go test ./..."},
		LedgerEvent{Kind: LedgerFileModified, Tool: "Edit", Path: "src/a.go"},
		LedgerEvent{Kind: LedgerToolCall, Tool: "Bash", Args: "t", Status: LedgerOK, Text: "go test ./..."},
		LedgerEvent{Kind: LedgerFileRead, Tool: "Read", Path: "src/a.go"},
		LedgerEvent{Kind: LedgerFileRead, Tool: "Read", Path: "src/b.go"})
	snap, err := BuildSessionSnapshot(context.Background(), dir, ledgerWS, "claude:s1")
	if err != nil {
		t.Fatal(err)
	}
	want := []SnapshotFile{{Path: "src/a.go", Modified: true}, {Path: "src/b.go"}}
	if len(snap.OpenFailures) != 0 || !slices.Equal(snap.ActiveFiles, want) {
		t.Fatalf("snapshot %+v", snap)
	}
}

func validCapsule(extra string) string {
	return `{"goals":["fix the rounding bug"],"what_happened":["found the half-even rule in src/a.go"` + extra + `],` +
		`"identifiers":["src/a.go","roundHalfEven"],"errors_and_fixes":[{"error":"TestRound failed","fix":"use banker's rounding"}],` +
		`"current_state":"tests pass locally","next_step":"run the integration suite","lookup_hints":["recall q=rounding"]}`
}

// A capsule is schema-checked (every violation named), its secrets redacted, its paths
// hashed; ground flags the paths that changed since, and a session of another
// principal is refused.
func TestHandoffCapsuleIsSchemaCheckedAndFlagsStalePaths(t *testing.T) {
	dir, root := newLedgerFixture(t, true)
	for _, c := range []struct{ name, content, want string }{
		{"not json", "handed off", "not a capsule object"},
		{"unknown member", `{"goals":["g"],"current_state":"s","next_step":"n","summary":"x"}`, `unknown field "summary"`},
		{"wrong type", `{"goals":"g","current_state":"s","next_step":"n"}`, "not a capsule object"},
		{"missing", `{"what_happened":["x"]}`, "goals is required; current_state is required; next_step is required"},
		{"schema", `{"schema":"other/v1","goals":["g"],"current_state":"s","next_step":"n"}`, "schema must be " + HandoffSchema},
		{"too many items", `{"goals":[` + strings.Repeat(`"g",`, 20) + `"g"],"current_state":"s","next_step":"n"}`, "goals has 21 items"},
		{"too long", `{"goals":["` + strings.Repeat("w ", 501) + `"],"current_state":"s","next_step":"n"}`, "text field is 1002 bytes"},
		{"too many words", `{"goals":["` + strings.Repeat("w ", 300) + `","` + strings.Repeat("w ", 300) + `"],"current_state":"s","next_step":"n"}`, "602 words (at most 500)"},
		{"trailing", validCapsule("") + `{}`, "trailing data"},
	} {
		_, err := RememberHandoff(dir, ledgerWS, RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: c.content}},
			"claude:s1", ContextActor{ID: "agent-a"})
		if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := RememberHandoff(dir, ledgerWS, RememberRequest{Op: "edit", ProposeContextRequest: ProposeContextRequest{Content: validCapsule("")}},
		"claude:s1", ContextActor{ID: "agent-a"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a capsule edit: %v", err)
	}
	secret := "ghp_" + strings.Repeat("aB3dE5", 6)
	req := RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: validCapsule(`,"token ` + secret + `"`),
		Paths: []string{"src/a.go", "src/b.go"}}}
	rec, err := RememberHandoff(dir, ledgerWS, req, "claude:s1", ContextActor{ID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rec.ID, "handoff:") || rec.RecordClass != govstore.RecordRaw || rec.SessionID != "claude:s1" ||
		rec.Redactions == nil || strings.Contains(rec.Capsule.Render(), secret) || rec.Words == 0 || rec.Status != "stored" ||
		!slices.Equal(rec.Capsule.Identifiers, []string{"src/a.go", "roundHalfEven"}) {
		t.Fatalf("record %+v", rec)
	}
	if _, err := RememberHandoff(dir, ledgerWS, req, "claude:s1", ContextActor{ID: "agent-b"}); err == nil ||
		!strings.Contains(err.Error(), "another workspace or principal") {
		t.Fatalf("another principal wrote into the session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var recs []HandoffRecord
	if err := memoryView(context.Background(), dir, ledgerWS, func(r govstore.Reader) error {
		recs, err = latestHandoffs(context.Background(), r, root, ledgerWS, 3)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || !slices.Equal(recs[0].StalePaths, []string{"src/a.go"}) || !slices.Equal(recs[0].Paths, []string{"src/a.go", "src/b.go"}) {
		t.Fatalf("handoffs %+v", recs)
	}
	// a session id that is not an execution key goes under that MCP session
	if key, err := HandoffSessionKey("", "mcp-7"); err != nil || key != "mcp:mcp-7" {
		t.Fatalf("transport session: %q %v", key, err)
	}
	if _, err := HandoffSessionKey("claude:../x", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a malformed execution key: %v", err)
	}
}

// A new session in another client receives the prior session's snapshot, unexpired
// handles and the proposals waiting for a distinct verifier; its own session id
// restores its own snapshot instead.
func TestNewSessionInAnotherClientGetsPriorSnapshotAndProposals(t *testing.T) {
	dir, root := newLedgerFixture(t, true)
	ctx := context.Background()
	handle := retainedEvidence(t, dir, ledgerWS, "agent-a")
	pending := func(title string) string {
		e, err := Remember(dir, ledgerWS, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: title,
			Content: title + " holds", Permission: "readwrite", Kind: "decision"}}, ContextActor{ID: "agent-a"})
		if err != nil || e.Promoted {
			t.Fatalf("pending proposal: %v %+v", err, e)
		}
		return e.ID
	}
	recorded, other := pending("round half even"), pending("amounts in cents")
	a := ledgerSession(root, "claude", "sess-a", "agent-a")
	recordAll(t, dir, a,
		LedgerEvent{Kind: LedgerPrompt, Text: "fix the rounding bug"},
		LedgerEvent{Kind: LedgerToolCall, Tool: "Bash", Args: "t", Status: LedgerError, Text: "go test ./...",
			Signature: "--- FAIL: TestRound", Handle: handle, ExpiresAt: future()},
		LedgerEvent{Kind: LedgerFileModified, Tool: "Edit", Path: "src/a.go"},
		LedgerEvent{Kind: LedgerMemoryProposed, Tool: "mcp__xmustard__remember", EntryID: recorded})
	RecordBoundary(dir, BoundaryPreCompact, LedgerEvent{LedgerSession: a, Text: "auto"})
	flushLedger(t, dir)

	var g SessionGrounding
	if unknown := g.groundingSession.buildSessions(ctx, dir, ledgerWS, "codex:sess-b", nil); len(unknown) != 0 {
		t.Fatalf("unknown %+v", unknown)
	}
	h := g.SessionHandoff
	if h == nil || h.FromSession != "claude:sess-a" || h.Restore || h.Client != "claude" || h.Label != SessionHandoffLabel {
		t.Fatalf("handoff %+v", h)
	}
	if s := h.Snapshot; s == nil || s.Goal != "fix the rounding bug" || len(s.OpenFailures) != 1 || s.OpenFailures[0].Handle != handle ||
		len(s.PendingProposals) != 1 || s.PendingProposals[0].ID != recorded || !slices.Equal(s.Decisions, []string{"round half even"}) {
		t.Fatalf("snapshot %+v", h.Snapshot)
	}
	var ids []string
	for _, p := range h.PendingProposals {
		ids = append(ids, p.ID)
		if p.VotesNeeded != 2 {
			t.Fatalf("proposal %+v", p)
		}
	}
	if !slices.Equal(ids, []string{recorded, other}) {
		t.Fatalf("proposals awaiting a verifier %v, want %v", ids, []string{recorded, other})
	}
	if len(h.Handles) != 1 || h.Handles[0].Handle != handle {
		t.Fatalf("handles %+v", h.Handles)
	}
	if len(g.LastSessions) != 1 || g.LastSessions[0].ID != "claude:sess-a" || g.Continuity == nil ||
		len(g.Continuity.RecentActionable) != 1 || len(g.Continuity.OpenItems) != 1 || g.Continuity.OpenItems[0].EntryID != recorded {
		t.Fatalf("last sessions %+v, continuity %+v", g.LastSessions, g.Continuity)
	}
	// the snapshot handed over is the one stored at the compaction boundary
	stored := countKinds(ledgerRows(t, dir, "claude:sess-a"), LedgerSnapshot)
	if stored != 1 || h.Snapshot.ThroughSeq == 0 {
		t.Fatalf("%d snapshots stored, through %d", stored, h.Snapshot.ThroughSeq)
	}
	// the SessionStart hook's view is the same, and a session's own id restores it
	hook, err := HookSessionHandoff(ctx, dir, ledgerWS, "codex:sess-b", false, true)
	if err != nil || hook == nil || hook.FromSession != "claude:sess-a" {
		t.Fatalf("hook handoff %+v %v", hook, err)
	}
	var own SessionGrounding
	own.groundingSession.buildSessions(ctx, dir, ledgerWS, "claude:sess-a", nil)
	if own.SessionHandoff == nil || !own.SessionHandoff.Restore || own.SessionHandoff.FromSession != "claude:sess-a" {
		t.Fatalf("restore %+v", own.SessionHandoff)
	}
	if none, err := HookSessionHandoff(ctx, dir, ledgerWS, "claude:sess-z", true, false); err != nil || none != nil {
		t.Fatalf("a session with no ledger restored %+v %v", none, err)
	}
}

// Mining proposes each completed finding once, as a pending candidate that neither
// single-agent nor open mode promotes; an attempt that never passed is not mined.
func TestCandidateMiningProposesPendingCandidatesOnly(t *testing.T) {
	for _, c := range []struct {
		name, principal string
	}{{"single-agent mode", "agent-a"}, {"open mode", ""}} {
		t.Run(c.name, func(t *testing.T) {
			dir, root := newLedgerFixture(t, false) // single-agent mode: proposals self-assert unless peers are asked
			s := ledgerSession(root, "claude", "s1", c.principal)
			recordAll(t, dir, s,
				LedgerEvent{Kind: LedgerToolCall, Tool: "Bash", Args: "gt", Status: LedgerError, Text: "go test ./...", Signature: "--- FAIL: TestRound"},
				LedgerEvent{Kind: LedgerFileModified, Tool: "Edit", Path: "src/a.go"},
				LedgerEvent{Kind: LedgerToolCall, Tool: "Bash", Args: "gt", Status: LedgerOK, Text: "go test ./..."},
				LedgerEvent{Kind: LedgerToolCall, Tool: "Bash", Args: "gt", Status: LedgerOK, Text: "go test ./..."},
				LedgerEvent{Kind: LedgerToolCall, Tool: "Bash", Args: "ml", Status: LedgerError, Text: "make lint"},
				LedgerEvent{Kind: LedgerFileModified, Tool: "Edit", Path: "src/b.go"})
			for range 2 { // a second pass proposes nothing new
				RecordBoundary(dir, BoundaryTurnEnd, LedgerEvent{LedgerSession: s})
				flushLedger(t, dir)
			}
			var cands []govstore.Entry
			if err := memoryView(context.Background(), dir, ledgerWS, func(r govstore.Reader) error {
				var err error
				cands, err = r.ListEntries(context.Background(), govstore.EntryFilter{WorkspaceID: ledgerWS, Kind: "candidate", Lifecycles: []string{"*"}})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var titles []string
			for _, e := range cands {
				titles = append(titles, e.Title)
				if e.Promoted || e.Status != "pending" || !e.RequireVerification || !slices.Contains(e.Tags, "mined") ||
					e.Source != ledgerPrincipal(c.principal) || e.SessionID != "claude:s1" {
					t.Fatalf("candidate %+v", e)
				}
			}
			slices.Sort(titles)
			if want := []string{"Fix: `go test ./...` failure", "Workflow: `go test ./...`"}; !slices.Equal(titles, want) {
				t.Fatalf("candidates %v, want %v", titles, want)
			}
			rows := ledgerRows(t, dir, "claude:s1")
			if n := countKinds(rows, LedgerMemoryProposed); n != 2 {
				t.Fatalf("%d memory_proposed rows", n)
			}
		})
	}
}

// SessionEnd ends the session with its summary, stores its final snapshot and an
// automatic capsule (derived), and hashes the client's transcript as an upstream
// reference.
func TestSessionEndClosesTheSession(t *testing.T) {
	dir, root := newLedgerFixture(t, true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	transcript := filepath.Join(home, ".claude", "projects", "-repo", "s1.jsonl")
	_ = os.MkdirAll(filepath.Dir(transcript), 0o700)
	body := []byte(`{"type":"user"}` + "\n")
	if err := os.WriteFile(transcript, body, 0o600); err != nil {
		t.Fatal(err)
	}
	s := ledgerSession(root, "claude", "s1", "agent-a")
	s.Transcript = transcript
	recordAll(t, dir, s,
		LedgerEvent{Kind: LedgerPrompt, Text: "fix the rounding bug"},
		LedgerEvent{Kind: LedgerToolCall, Tool: "Bash", Args: "t", Status: LedgerError, Text: "go test ./...", Signature: "--- FAIL: TestRound"},
		LedgerEvent{Kind: LedgerFileModified, Tool: "Edit", Path: "src/a.go"})
	RecordBoundary(dir, BoundarySessionEnd, LedgerEvent{LedgerSession: s, Text: "logout"})
	flushLedger(t, dir)
	h, err := HookSessionHandoff(context.Background(), dir, ledgerWS, "claude:s1", true, false)
	if err != nil || h == nil || h.Status != govstore.SessionEnded || h.Capsule == nil || h.Capsule.RecordClass != govstore.RecordDerived ||
		!h.Capsule.Auto || !strings.Contains(h.Capsule.Capsule.CurrentState, "ended (logout) with 1 open failure") ||
		!slices.Equal(h.Capsule.Capsule.Identifiers, []string{"src/a.go"}) || h.Capsule.Trust() != "derived" {
		t.Fatalf("handoff %+v %v", h, err)
	}
	rows := ledgerRows(t, dir, "claude:s1")
	sum := sha256.Sum256(body)
	var ref *govstore.SessionEvent
	for i := range rows {
		if rows[i].Kind == LedgerTranscript {
			ref = &rows[i]
		}
	}
	if countKinds(rows, LedgerSnapshot) != 1 || ref == nil || ref.ArgsDigest != "sha256:"+hex.EncodeToString(sum[:]) || ref.Path != transcript {
		t.Fatalf("rows %+v", rows)
	}
	var sess govstore.Session
	_ = memoryView(context.Background(), dir, ledgerWS, func(r govstore.Reader) error {
		sess, err = r.GetSession(context.Background(), "claude:s1")
		return err
	})
	if !strings.Contains(sess.Summary, "1 tool call(s) (1 failed), 1 file(s) modified") || sess.ExecutionKey != "claude:s1" {
		t.Fatalf("session %+v", sess)
	}
}

// A transcript is hashed only as the client's own: an absolute .jsonl regular file that
// resolves inside the client's transcript root.
func TestTranscriptRefIsConfined(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	projects := filepath.Join(home, ".claude", "projects", "p")
	_ = os.MkdirAll(projects, 0o700)
	inside := filepath.Join(projects, "s.jsonl")
	_ = os.WriteFile(inside, []byte("{}\n"), 0o600)
	outside := filepath.Join(t.TempDir(), "s.jsonl")
	_ = os.WriteFile(outside, []byte("secret\n"), 0o600)
	link := filepath.Join(projects, "link.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	notJSONL := filepath.Join(projects, "s.txt")
	_ = os.WriteFile(notJSONL, []byte("x"), 0o600)
	if _, n, truncated, err := transcriptRef("claude", inside); err != nil || n != 3 || truncated {
		t.Fatalf("inside: %d %v %v", n, truncated, err)
	}
	for name, c := range map[string]struct{ client, path string }{
		"outside": {"claude", outside}, "symlink out": {"claude", link}, "not jsonl": {"claude", notJSONL},
		"relative": {"claude", "p/s.jsonl"}, "unknown client": {"pi", inside},
	} {
		if _, _, _, err := transcriptRef(c.client, c.path); !errors.Is(err, errNotTranscript) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The ledger is bounded: a full queue drops and counts; a session keeps its newest
// activity rows and snapshots within the bounds once the writer is idle; rows carry the
// session retention class that age retention prunes.
func TestLedgerBoundsHold(t *testing.T) {
	dir, root := newLedgerFixture(t, true)
	s := ledgerSession(root, "claude", "s1", "agent-a")
	release := make(chan struct{})
	RunSessionJob(dir, func(context.Context) { <-release })
	dropped := ledgerStats.dropped.Load()
	for i := range ledgerQueueDepth + 50 {
		RecordSession(dir, LedgerEvent{LedgerSession: s, Kind: LedgerFileRead, Tool: "Read", Path: fmt.Sprintf("f%d", i)})
	}
	if n := ledgerStats.dropped.Load() - dropped; n < 50 {
		t.Fatalf("%d dropped past a full queue", n)
	}
	close(release)
	flushLedger(t, dir)
	for i := range 1200 {
		RecordSession(dir, LedgerEvent{LedgerSession: s, Kind: LedgerToolCall, Tool: "Grep", Args: fmt.Sprintf("g%d", i), Status: LedgerOK})
		if i%200 == 0 {
			RecordBoundary(dir, BoundaryPreCompact, LedgerEvent{LedgerSession: s, Text: "auto"})
			flushLedger(t, dir) // no drops in this phase: the bound, not the queue, must remove rows
		}
	}
	flushLedger(t, dir)
	rows := ledgerRows(t, dir, "claude:s1")
	activity, snaps := countKinds(rows, activityKinds...), countKinds(rows, LedgerSnapshot)
	if activity != ledgerBounds[0].keep || snaps != ledgerBounds[1].keep {
		t.Fatalf("%d activity rows (bound %d), %d snapshots (bound %d)", activity, ledgerBounds[0].keep, snaps, ledgerBounds[1].keep)
	}
	if last := rows[len(rows)-1]; last.RetentionClass != "session" {
		t.Fatalf("retention class %q", last.RetentionClass)
	}
}

// BenchmarkSessionHandoff measures what a session start and ground pay for the session
// section: a prior session with a full ledger tail (512 rows), a stored snapshot, a
// capsule naming two paths, and two pending proposals.
func BenchmarkSessionHandoff(b *testing.B) {
	dir, root := newLedgerFixture(b, true)
	handle := retainedEvidence(b, dir, ledgerWS, "agent-a")
	s := ledgerSession(root, "claude", "prior", "agent-a")
	evs := []LedgerEvent{{Kind: LedgerPrompt, Text: "fix the rounding bug in billing"}}
	for i := range 600 {
		evs = append(evs, LedgerEvent{Kind: LedgerToolCall, Tool: "Bash", Args: fmt.Sprintf("d%d", i%40), Status: []string{LedgerOK, LedgerError}[i%2],
			Text: fmt.Sprintf("go test ./pkg%02d/...", i%40), Handle: handle, ExpiresAt: future()},
			LedgerEvent{Kind: LedgerFileRead, Tool: "Read", Path: fmt.Sprintf("src/f%03d.go", i%100)})
	}
	for i := 0; i < len(evs); i += 500 {
		recordAll(b, dir, s, evs[i:min(i+500, len(evs))]...)
	}
	for range 2 {
		if _, err := Remember(dir, ledgerWS, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "pending",
			Permission: "readwrite"}}, ContextActor{ID: "agent-a"}); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := RememberHandoff(dir, ledgerWS, RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: validCapsule(""),
		Paths: []string{"src/a.go", "src/b.go"}}}, "claude:prior", ContextActor{ID: "agent-a"}); err != nil {
		b.Fatal(err)
	}
	RecordBoundary(dir, BoundaryPreCompact, LedgerEvent{LedgerSession: s, Text: "auto"})
	flushLedger(b, dir)
	ctx := context.Background()
	b.Run("hook-handoff", func(b *testing.B) {
		for b.Loop() {
			if h, err := HookSessionHandoff(ctx, dir, ledgerWS, "claude:next", false, true); err != nil || h == nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ground-session-section", func(b *testing.B) {
		for b.Loop() {
			var g SessionGrounding
			if u := g.groundingSession.buildSessions(ctx, dir, ledgerWS, "", nil); u != nil || g.SessionHandoff == nil {
				b.Fatal(u)
			}
		}
	})
	b.Run("build-snapshot", func(b *testing.B) {
		for b.Loop() {
			if _, err := BuildSessionSnapshot(ctx, dir, ledgerWS, "claude:prior"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

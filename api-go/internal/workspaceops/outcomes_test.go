package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/rustcore"
)

// seedOutcomeWorkspace registers a workspace whose fake core reports changed as the
// working-tree changes. It returns the data dir, workspace id and root.
func seedOutcomeWorkspace(t *testing.T, changed ...string) (string, string, string) {
	t.Helper()
	dataDir, ws, root := t.TempDir(), "wsOutcome", t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", ws, "snapshot.json"), workspaceSnapshot{
		ScannerVersion: scannerVersion, Workspace: workspaceRecord{WorkspaceID: ws, Name: "outcome", RootPath: root},
	}); err != nil {
		t.Fatal(err)
	}
	files := make([]string, 0, len(changed))
	for _, c := range changed {
		files = append(files, fmt.Sprintf(`{"path":%q}`, c))
	}
	core := filepath.Join(t.TempDir(), "xmustard-core")
	script := "#!/bin/sh\ncase \"$1 $2\" in\n\"changetrack working-changes\") echo '{\"changed_files\":[" +
		strings.Join(files, ",") + "]}' ;;\n*) echo '{}' ;;\nesac\n"
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	return dataDir, ws, root
}

// stubRunner replaces the bounded runner for one test and records its calls; bare
// program names resolve to /stub/bin/<name>.
type runnerCall struct {
	dir     string
	timeout int
	argv    []string
	env     []string
}

func stubRunner(t *testing.T, res *rustcore.ManagedCommandResult) *[]runnerCall {
	t.Helper()
	var calls []runnerCall
	stubRunnerFunc(t, func(_ context.Context, dir string, timeout int, argv, env []string) (*rustcore.ManagedCommandResult, error) {
		calls = append(calls, runnerCall{dir, timeout, slices.Clone(argv), env})
		return res, nil
	})
	return &calls
}

func stubRunnerFunc(t *testing.T, run func(ctx context.Context, dir string, timeout int, argv, env []string) (*rustcore.ManagedCommandResult, error)) {
	t.Helper()
	prevRun, prevLook := runCheckCommand, lookPath
	runCheckCommand = run
	lookPath = func(name string) (string, error) { return "/stub/bin/" + name, nil }
	t.Cleanup(func() { runCheckCommand, lookPath = prevRun, prevLook })
}

func feedbackRunFail(t *testing.T, dataDir, ws, path string) int {
	t.Helper()
	m, err := loadFeedback(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if e := m[path]; e != nil {
		return e.RunFail
	}
	return 0
}

var agent = ContextActor{ID: "agent-a", SessionID: "s1"}

// A command runs through the bounded runner in the confined directory with its
// timeout, and its outcome is recorded, explained and listed by ground.
func TestWhyFailedCommandRecordsTheOutcome(t *testing.T) {
	dataDir, ws, root := seedOutcomeWorkspace(t, "pkg/sub/thing.go", "other.go")
	one := 1
	calls := stubRunner(t, &rustcore.ManagedCommandResult{ExitCode: &one, StdoutExcerpt: "--- FAIL: TestThing (0.00s)\n    thing_test.go:9: pkg/sub/thing.go:12: want 2, got 3\nFAIL\n", StderrExcerpt: "exit status 1"})
	exp, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{Command: `go test "./pkg/sub/..."`, Cwd: "pkg", TimeoutSeconds: 20, Actor: agent})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("runner calls = %d", len(*calls))
	}
	c := (*calls)[0]
	if want, _ := filepath.EvalSymlinks(filepath.Join(root, "pkg")); c.dir != want || c.timeout != 20 || !slices.Equal(c.argv, []string{"/stub/bin/go", "test", "./pkg/sub/..."}) {
		t.Fatalf("runner got dir %q timeout %d argv %q", c.dir, c.timeout, c.argv)
	}
	if !exp.Failed || exp.Status != govstore.RunFailed || exp.Source != govstore.RunSourceCommand || !IsRunOutcomeID(exp.RunID) ||
		exp.Created == nil || !*exp.Created || exp.Cwd != "pkg" || exp.Command != "go test ./pkg/sub/..." {
		t.Fatalf("explanation = %+v", exp)
	}
	if !slices.Equal(exp.ImplicatedPaths, []string{"pkg/sub/thing.go"}) || !slices.Contains(exp.Signals, "exited with code 1") ||
		!slices.Contains(exp.Signals, "Go test failure") || len(exp.ErrorLines) == 0 {
		t.Fatalf("analysis = implicated %v signals %v lines %v", exp.ImplicatedPaths, exp.Signals, exp.ErrorLines)
	}
	if n := feedbackRunFail(t, dataDir, ws, "pkg/sub/thing.go"); n != 1 {
		t.Fatalf("run_fail feedback = %d, want 1 at record time", n)
	}
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.RecentFailedRuns, []string{exp.RunID}) || g.BlockedByFailingVerification == nil || !*g.BlockedByFailingVerification {
		t.Fatalf("ground in core-only mode: runs %v blocked %v unknown %+v", g.RecentFailedRuns, g.BlockedByFailingVerification, g.Unknown)
	}
	// the same command passing resolves the failure: ground stops listing it
	*calls = nil
	zero := 0
	runCheckCommand = func(_ context.Context, dir string, timeout int, argv, env []string) (*rustcore.ManagedCommandResult, error) {
		return &rustcore.ManagedCommandResult{ExitCode: &zero, Success: true, StdoutExcerpt: "ok  pkg/sub 0.1s"}, nil
	}
	pass, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{Argv: []string{"go", "test", "./pkg/sub/..."}, Cwd: "pkg", Actor: agent})
	if err != nil || pass.Failed || pass.Status != govstore.RunPassed {
		t.Fatalf("pass = %+v, %v", pass, err)
	}
	if g, _ := BuildSessionGrounding(dataDir, ws); len(g.RecentFailedRuns) != 0 || *g.BlockedByFailingVerification {
		t.Fatalf("a later pass resolves the failure: %v", g.RecentFailedRuns)
	}
	if old, _ := ExplainRunFailureCtx(context.Background(), dataDir, ws, exp.RunID); old.ResolvedBy != pass.RunID {
		t.Fatalf("the failure names the outcome that resolved it: %+v", old)
	}
}

// The real bounded runner kills the command's whole process group at the timeout,
// including a grandchild, and the outcome records the timeout.
func TestWhyFailedCommandTimeoutKillsTheProcessGroup(t *testing.T) {
	dataDir, ws, pidFile := realCoreGrandchildWorkspace(t, "\t@echo '--- FAIL: TestSlow (0.00s)'\n")
	start := time.Now()
	exp, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{Argv: []string{"make", "test"}, TimeoutSeconds: 1, Actor: agent})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("a 1 s timeout took %s", took)
	}
	if !exp.Failed || exp.Status != govstore.RunTimedOut || !exp.TimedOut || !slices.Contains(exp.Signals, "timed out after 1s; the command's process group was terminated") {
		t.Fatalf("timeout outcome = %+v", exp)
	}
	waitGrandchildGone(t, pidFile)
	if got, err := ExplainRunFailureCtx(context.Background(), dataDir, ws, exp.RunID); err != nil || got.Status != govstore.RunTimedOut {
		t.Fatalf("recorded outcome = %+v, %v", got, err)
	}
}

// A cancelled request (a client disconnect, an MCP cancellation, the stdio backend's
// HTTP timeout) does not kill the runner: that would kill only the core and orphan the
// command, in its own process group, without its timeout. The core still ends the
// group at the timeout, the caller gets its cancellation, and nothing is recorded.
func TestWhyFailedCancelledCommandStillEndsItsProcessGroup(t *testing.T) {
	dataDir, ws, pidFile := realCoreGrandchildWorkspace(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(1500*time.Millisecond, cancel)
	start := time.Now()
	_, err := RecordFailureOutcome(ctx, dataDir, ws, FailureRequest{Argv: []string{"make", "test"}, TimeoutSeconds: 3, Actor: agent})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled request: %v", err)
	}
	if took := time.Since(start); took < 2*time.Second || took > 20*time.Second {
		t.Fatalf("the call returned after %s: it must wait for the core to end the command (3 s timeout)", took)
	}
	waitGrandchildGone(t, pidFile)
	if all, err := ListRunOutcomes(context.Background(), dataDir, ws, false, 0); err != nil || len(all) != 0 {
		t.Fatalf("a cancelled command recorded %+v, %v", all, err)
	}
	if n := len(commandSlots); n != 0 {
		t.Fatalf("%d command slot(s) still held", n)
	}
}

// With the real core, the repository's code runs without the daemon's configuration
// and secrets in its environment.
func TestWhyFailedCommandEnvironmentHasNoDaemonSecrets(t *testing.T) {
	dataDir, ws, _ := realCoreGrandchildWorkspace(t, "")
	root := contextRoot(dataDir, ws)
	makefile := "test:\n\t@echo \"tokens=[$${XMUSTARD_AUTH_TOKENS}] deploy=[$${DEPLOY_API_TOKEN}] visible=[$${XM_TEST_VISIBLE}]\"; exit 1\n"
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_AUTH_TOKENS", "root:admin:"+strings.Repeat("s3cr3tT0k3n", 3))
	t.Setenv("DEPLOY_API_TOKEN", strings.Repeat("Zq9", 12))
	t.Setenv("XM_TEST_VISIBLE", "visible")
	exp, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{Argv: []string{"make", "test"}, TimeoutSeconds: 20, Actor: agent})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exp.Output.Tail, "tokens=[] deploy=[] visible=[visible]") {
		t.Fatalf("the command saw %q", exp.Output.Tail)
	}
}

// realCoreGrandchildWorkspace seeds a workspace run by the built core whose `make test`
// runs the recipe lines in prefix, then backgrounds a 30 s sleep (its pid written to the returned file) and
// waits for it. It skips without make, the core or process groups.
func realCoreGrandchildWorkspace(t *testing.T, prefix string) (dataDir, ws, pidFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process groups are POSIX")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	core := realCoreForOutcomes(t)
	dataDir, ws, root := seedOutcomeWorkspace(t)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	pidFile = filepath.Join(root, "grandchild.pid")
	makefile := "test:\n" + prefix + "\t@sleep 30 & echo $$! > " + pidFile + "; wait\n"
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatal(err)
	}
	return dataDir, ws, pidFile
}

// waitGrandchildGone waits up to 5 s for the pid in pidFile to exit.
func waitGrandchildGone(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the grandchild never started: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d outlived the command", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// realCoreForOutcomes returns a built xmustard-core, or skips.
func realCoreForOutcomes(t *testing.T) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	for _, bin := range []string{os.Getenv("XMUSTARD_CORE_BIN"), filepath.Join(filepath.Dir(here), "..", "..", "..", "rust-core", "target", "release", "xmustard-core")} {
		if info, err := os.Stat(bin); bin != "" && err == nil && !info.IsDir() {
			return bin
		}
	}
	t.Skip("no xmustard-core built (cd rust-core && cargo build --release)")
	return ""
}

// why_failed(evidence_handle) reads the bounded tail of the retained original, as
// the caller; a second report of the same original returns the first outcome and
// feeds nothing back again.
func TestWhyFailedEvidenceReadsTheTail(t *testing.T) {
	dataDir, ws, _ := seedOutcomeWorkspace(t, "pkg/sub/thing.go")
	store := evidence.NewStore(dataDir, evidence.DefaultLimits())
	var b strings.Builder
	b.WriteString("pkg/sub/early.go: an early line past the analyzed window\n")
	for b.Len() < 3<<20 {
		b.WriteString("=== RUN TestCase\n--- PASS: TestCase (0.00s)\n")
	}
	b.WriteString("--- FAIL: TestThing (0.01s)\n    pkg/sub/thing.go:4: boom\nFAIL\n")
	raw := b.String()
	sp, err := store.NewSpool(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	d, err := store.Capture(context.Background(), sp, evidence.CaptureRequest{WorkspaceID: ws, Actor: "agent-a", AuthEnforced: true,
		Tool: "Bash", ContentType: "text/plain", Status: 1, IsError: true})
	if err != nil || d.Handle == "" {
		t.Fatalf("capture: %v %+v", err, d)
	}
	var asked int64
	tail := func(ctx context.Context, handle string, maxBytes int64) (*evidence.Tail, error) {
		asked = maxBytes
		return store.Tail(ctx, evidence.ReadRequest{WorkspaceID: ws, Handle: handle, Actor: "agent-a", AuthEnforced: true}, maxBytes)
	}
	exp, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{EvidenceHandle: d.Handle, Evidence: tail, Actor: agent})
	if err != nil {
		t.Fatal(err)
	}
	if asked != outcomeTailBytes || exp.Output.AnalyzedBytes != outcomeTailBytes || exp.Output.TotalBytes != int64(len(raw)) || !exp.Output.Truncated {
		t.Fatalf("window = asked %d, %+v", asked, exp.Output)
	}
	if !exp.Failed || exp.EvidenceHandle != d.Handle || !slices.Equal(exp.ImplicatedPaths, []string{"pkg/sub/thing.go"}) ||
		!strings.HasSuffix(exp.Output.Tail, "FAIL\n") || len(exp.Output.Tail) > outcomeStoredTail {
		t.Fatalf("explanation = %+v", exp)
	}
	if slices.Contains(exp.ImplicatedPaths, "pkg/sub/early.go") {
		t.Fatal("a path before the analyzed tail cannot be implicated")
	}
	again, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{EvidenceHandle: d.Handle, Evidence: tail, Actor: agent})
	if err != nil || *again.Created || again.RunID != exp.RunID {
		t.Fatalf("a second report returns the first outcome: %+v, %v", again, err)
	}
	if n := feedbackRunFail(t, dataDir, ws, "pkg/sub/thing.go"); n != 1 {
		t.Fatalf("run_fail = %d after two reports, want 1", n)
	}
	// another principal cannot read the original through why_failed
	other := func(ctx context.Context, handle string, maxBytes int64) (*evidence.Tail, error) {
		return store.Tail(ctx, evidence.ReadRequest{WorkspaceID: ws, Handle: handle, Actor: "agent-b", AuthEnforced: true}, maxBytes)
	}
	if _, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{EvidenceHandle: d.Handle, Evidence: other, Actor: agent}); err == nil {
		t.Fatal("another principal's evidence must not be readable")
	} else if de, ok := AsDomainError(err); !ok || de.Class != ClassNotFound {
		t.Fatalf("unreadable evidence: %v", err)
	}
}

// Reading an outcome or a platform run (GET why-failed) never writes: the feedback
// counters stay where the recording left them.
func TestWhyFailedReadsNeverChangeFeedback(t *testing.T) {
	dataDir, ws, root := seedOutcomeWorkspace(t, "pkg/sub/thing.go")
	exp, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{Log: "pkg/sub/thing.go:3: undefined: Foo\nFAIL", Actor: agent})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "run.out")
	if err := os.WriteFile(out, []byte("error: pkg/sub/thing.go:9 broke\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", ws, "runs", "run_x.json"), map[string]any{
		"run_id": "run_x", "workspace_id": ws, "status": "failed", "output_path": out, "created_at": "2026-09-28T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	before := feedbackRunFail(t, dataDir, ws, "pkg/sub/thing.go")
	for range 3 {
		for _, id := range []string{exp.RunID, "run_x"} {
			got, err := ExplainRunFailureCtx(context.Background(), dataDir, ws, id)
			if err != nil || !got.Failed || !slices.Equal(got.ImplicatedPaths, []string{"pkg/sub/thing.go"}) {
				t.Fatalf("%s: %+v, %v", id, got, err)
			}
		}
	}
	if after := feedbackRunFail(t, dataDir, ws, "pkg/sub/thing.go"); after != before || before != 1 {
		t.Fatalf("run_fail %d -> %d: a GET must not write", before, after)
	}
	// the same log again is the same outcome
	if again, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{Log: "pkg/sub/thing.go:3: undefined: Foo\nFAIL", Actor: agent}); err != nil || *again.Created {
		t.Fatalf("second log report: %+v, %v", again, err)
	}
	if n := feedbackRunFail(t, dataDir, ws, "pkg/sub/thing.go"); n != 1 {
		t.Fatalf("run_fail = %d, want 1", n)
	}
}

// A 16 MiB run output is read as its last MiB: why_failed allocates about the window,
// never the whole output (PAR-RT-11).
func TestWhyFailedNeverLoadsALargeRunOutput(t *testing.T) {
	dataDir, ws, root := seedOutcomeWorkspace(t)
	out := filepath.Join(root, "big.out")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("--- PASS: TestCase (0.00s) and some padding to make the line longer\n", (1<<20)/68)
	for range 16 {
		_, _ = f.WriteString(chunk)
	}
	_, _ = f.WriteString("panic: runtime error: index out of range\n")
	f.Close()
	if err := writeJSON(filepath.Join(dataDir, "workspaces", ws, "runs", "run_big.json"), map[string]any{
		"run_id": "run_big", "workspace_id": ws, "status": "failed", "output_path": out,
	}); err != nil {
		t.Fatal(err)
	}
	// warm the registry and store caches so the measurement is the read itself
	if _, err := ExplainRunFailureCtx(context.Background(), dataDir, ws, "run_big"); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	exp, err := ExplainRunFailureCtx(context.Background(), dataDir, ws, "run_big")
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if exp.Output.TotalBytes < 15<<20 || exp.Output.AnalyzedBytes != outcomeTailBytes || !exp.Output.Truncated {
		t.Fatalf("output window = %+v", exp.Output)
	}
	if !slices.Contains(exp.Signals, "Panic detected in output") {
		t.Fatalf("the tail's panic is found: %v", exp.Signals)
	}
	grew := after.TotalAlloc - before.TotalAlloc
	if grew > 6<<20 {
		t.Fatalf("why_failed on a 16 MiB output allocated %d bytes; the output was loaded, not its tail", grew)
	}
	t.Logf("why_failed on a %d-byte output allocated %d KiB", exp.Output.TotalBytes, grew>>10)
}

// A pasted log longer than the window is analyzed from its end.
func TestWhyFailedLogAnalyzesItsTail(t *testing.T) {
	dataDir, ws, _ := seedOutcomeWorkspace(t)
	log := "error: an early error that falls outside the window\n" + strings.Repeat("ok\n", (2<<20)/3) + "FAIL: the late failure\n"
	exp, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{Log: log, Actor: agent})
	if err != nil {
		t.Fatal(err)
	}
	if exp.Output.AnalyzedBytes != outcomeTailBytes || exp.Output.TotalBytes != int64(len(log)) {
		t.Fatalf("window = %+v", exp.Output)
	}
	if len(exp.ErrorLines) != 1 || !strings.Contains(exp.ErrorLines[0], "late failure") {
		t.Fatalf("error lines = %v", exp.ErrorLines)
	}
}

// Secrets in a command's output are redacted before anything is analyzed or stored.
func TestWhyFailedRedactsOutputBeforeStoring(t *testing.T) {
	dataDir, ws, _ := seedOutcomeWorkspace(t)
	secret := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	one := 1
	stubRunner(t, &rustcore.ManagedCommandResult{ExitCode: &one, StdoutExcerpt: "error: auth failed for token " + secret + "\nFAIL"})
	exp, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{Command: "go test ./...", Actor: agent})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := ExplainRunFailureCtx(context.Background(), dataDir, ws, exp.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []*FailureExplanation{exp, stored} {
		all := fmt.Sprint(e.ErrorLines, e.Output.Tail, e.Summary)
		if strings.Contains(all, secret) || e.Output.Redacted == 0 {
			t.Fatalf("secret reached the outcome: redacted=%d %s", e.Output.Redacted, all)
		}
	}
}

// A captured failing test output becomes an outcome ground lists; a captured pass of
// the same command resolves it; other families are ignored. No Rust runs on the
// capture path.
func TestCapturedTestOutputsBecomeOutcomes(t *testing.T) {
	dataDir, ws, _ := seedOutcomeWorkspace(t)
	t.Setenv("XMUSTARD_CORE_BIN", filepath.Join(t.TempDir(), "no-core-on-the-capture-path"))
	ctx := context.Background()
	fail := CapturedOutcome{Family: evidence.FamilyTest, Command: "go test ./pkg/...", Failed: true, Text: "--- FAIL: TestX\nFAIL",
		Tool: "bash", Session: "s1", Call: "c1", Actor: agent}
	if err := RecordCapturedOutcome(ctx, dataDir, ws, fail); err != nil {
		t.Fatal(err)
	}
	// the same call re-captured (a retention or a retry) is the same outcome
	if err := RecordCapturedOutcome(ctx, dataDir, ws, fail); err != nil {
		t.Fatal(err)
	}
	open, _, err := recentRunOutcomes(ctx, dataDir, ws)
	if err != nil || len(open) != 1 || open[0].Source != govstore.RunSourceCapture || open[0].Command != "go test ./pkg/..." {
		t.Fatalf("open = %+v, %v", open, err)
	}
	if err := RecordCapturedOutcome(ctx, dataDir, ws, CapturedOutcome{Family: evidence.FamilyRead, Failed: true, Text: "no such file", Call: "c2", Actor: agent}); err != nil {
		t.Fatal(err)
	}
	passOther := CapturedOutcome{Family: evidence.FamilyTest, Command: "go test ./other/...", Text: "ok", Call: "c3", Actor: agent}
	pass := CapturedOutcome{Family: evidence.FamilyTest, Command: "go test ./pkg/...", Text: "ok", Call: "c4", Actor: agent}
	for _, c := range []CapturedOutcome{passOther, pass} {
		if err := RecordCapturedOutcome(ctx, dataDir, ws, c); err != nil {
			t.Fatal(err)
		}
	}
	if open, _, _ := recentRunOutcomes(ctx, dataDir, ws); len(open) != 0 {
		t.Fatalf("the pass resolves the capture's failure: %+v", open)
	}
	all, err := ListRunOutcomes(ctx, dataDir, ws, false, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("outcomes = %+v, %v (a pass with no open failure and a read are not recorded)", all, err)
	}
}

// The runner keeps a head and a tail of each stream; the outcome reports the streams'
// true size from its dropped-bytes marker, so a partial analysis is never shown as whole.
func TestWhyFailedCommandReportsTheRunnerWindow(t *testing.T) {
	dataDir, ws, _ := seedOutcomeWorkspace(t)
	one := 1
	stdout := "head\n...[dropped 4096 bytes of 5000 total]\ntail\n--- FAIL: TestX\n"
	stubRunner(t, &rustcore.ManagedCommandResult{ExitCode: &one, StdoutExcerpt: stdout, StderrExcerpt: "exit status 1"})
	exp, err := RecordFailureOutcome(context.Background(), dataDir, ws, FailureRequest{Command: "go test ./...", Actor: agent})
	if err != nil {
		t.Fatal(err)
	}
	if exp.Output.TotalBytes != 5000+int64(len("exit status 1")) || !exp.Output.Truncated {
		t.Fatalf("output window = %+v", exp.Output)
	}
}

// BenchmarkAnalyzedOutput measures one 1 MiB tail analysis (redaction, signals, error
// lines, mentioned paths): the work a failing capture adds to the capture path.
func BenchmarkAnalyzedOutput(b *testing.B) {
	line := "=== RUN TestCase\n--- PASS: TestCase (0.00s) pkg/sub/case_test.go:12\n"
	text := strings.Repeat(line, (1<<20)/len(line)) + "--- FAIL: TestThing\nFAIL\n"
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		analyzedOutput(text, int64(len(text)))
	}
}

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the fake agent and the stub MCP server: the harness
// launches os.Executable() with XMUSTARD_EVAL_AS_MAIN=1.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == survivorArg {
		os.Exit(runSurvivor(os.Args[2:]))
	}
	if os.Getenv(envAsMain) == "1" {
		os.Exit(realMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

const fixPatch = `diff --git a/value.txt b/value.txt
--- a/value.txt
+++ b/value.txt
@@ -1 +1 @@
-old
+new
`

// writeFixtureCorpus builds a tiny corpus whose commands are POSIX sh, so the
// harness paths run in milliseconds. The visible verify step passes only when the
// oracle file is absent, proving it ran before the oracle was staged.
func writeFixtureCorpus(t *testing.T, extra func(dir string) string) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "fixtures/app/value.txt"), "old\n")
	mustWrite(t, filepath.Join(dir, "fixtures/app/notes.txt"), "timeout is 5s\n")
	mustWrite(t, filepath.Join(dir, "oracles/check.sh"), "grep -qx new value.txt\n")
	mustWrite(t, filepath.Join(dir, "references/fix.patch"), fixPatch)
	tasks := `
  - id: flip
    class: bugfix
    repo: {fixture: fixtures/app}
    prompt: Change value.txt from old to new.
    verify: {cmd: [sh, -c, "test ! -e oracle/check.sh"]}
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, oracle/check.sh]
    reference: {patch: references/fix.patch}
    gold_files: [value.txt]
  - id: flip-mem
    class: memory_lifecycle
    repo: {fixture: fixtures/app}
    prompt: Change value.txt to the value the team agreed.
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, oracle/check.sh]
    reference: {patch: references/fix.patch}
    gold_files: [value.txt]
    memory:
      seed:
        - {key: agreed, label: current, content: "The agreed value is new.", paths: [value.txt]}
        - {key: timeout, label: stale, content: "notes.txt says the timeout is 5s.", paths: [notes.txt]}
        - {key: elsewhere, label: foreign_scope, content: "The agreed value is other."}
        - {key: draft, label: pending, content: "Maybe the value is draft."}
      drift:
        - {path: notes.txt, replace: "timeout is 30s\n"}
`
	if extra != nil {
		tasks += extra(dir)
	}
	path := filepath.Join(dir, "corpus.yaml")
	mustWrite(t, path, "schema: xmustard.eval/v1\nname: fixture\ntasks:"+tasks)
	return path
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadTestCorpus(t *testing.T, path string) *Corpus {
	t.Helper()
	c, err := LoadCorpus(path)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	return c
}

func prepared(t *testing.T, cfg *RunConfig, c *Corpus, out string) *RunConfig {
	t.Helper()
	if cfg.Containment == "" {
		cfg.Containment = ContainNone
	}
	if err := cfg.prepare(c, out); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return cfg
}

func readRecords(t *testing.T, out string) []RunRecord {
	t.Helper()
	recs, err := readRuns(filepath.Join(out, "runs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func findRec(recs []RunRecord, task, arm string) *RunRecord {
	for i := range recs {
		if recs[i].TaskID == task && recs[i].Arm == arm {
			return &recs[i]
		}
	}
	return nil
}

// TestFakeDriverEndToEnd runs arms through the whole pipeline with each client
// emulation: canned stream output is parsed, xMustard tools are reached through the
// arm's wiring, worktrees are detached and removed, and the report is written. The
// full arm matrix runs under claude; codex and pi run the arms whose wiring differs.
func TestFakeDriverEndToEnd(t *testing.T) {
	corpusPath := writeFixtureCorpus(t, nil)
	for _, drv := range driverNames {
		t.Run(drv, func(t *testing.T) {
			c := loadTestCorpus(t, corpusPath)
			out := filepath.Join(t.TempDir(), "out")
			cfg := &RunConfig{Driver: "fake:" + drv, FakeFailArms: []string{ArmBaseline}}
			wantRecs := len(c.Tasks) * len(DefaultArms)
			if drv != "claude" {
				cfg.Arms = []string{ArmBaseline, ArmBaselineNoMCP, ArmXmustardMemory}
				cfg.Tasks = []string{"flip-mem"}
				wantRecs = 3
			}
			prepared(t, cfg, c, out)
			if _, err := Execute(context.Background(), cfg, c); err != nil {
				t.Fatalf("execute: %v", err)
			}
			recs := readRecords(t, out)
			if len(recs) != wantRecs {
				t.Fatalf("got %d records, want %d", len(recs), wantRecs)
			}
			for _, r := range recs {
				switch {
				case r.Arm == ArmXmustardMCPHooks:
					if r.Status != StatusSkipped || !strings.HasPrefix(r.Reason, "placeholder:") {
						t.Fatalf("hooks arm must be a named placeholder skip, got %s %q", r.Status, r.Reason)
					}
					continue
				case r.Arm == ArmXmustardMemory && r.TaskID == "flip":
					if r.Status != StatusSkipped {
						t.Fatalf("memory arm without fixture must skip, got %s", r.Status)
					}
					continue
				}
				if r.Status != StatusCompleted {
					t.Fatalf("%s/%s: status %s (%s)", r.TaskID, r.Arm, r.Status, r.Reason)
				}
				if !r.Isolation.WorktreeDetached || !r.Isolation.WorktreeRemoved || r.Isolation.OracleLeakScan != "clean" || !r.Isolation.OracleStagedPost {
					t.Fatalf("%s/%s: isolation %+v", r.TaskID, r.Arm, *r.Isolation)
				}
				tr := r.Transcript
				if tr == nil || !tr.FinalEvent || tr.Usage.Total == 0 || tr.Usage.CacheRead != 4000 {
					t.Fatalf("%s/%s: transcript %+v", r.TaskID, r.Arm, tr)
				}
				wantResolved := r.Arm != ArmBaseline
				if r.Resolved != wantResolved {
					t.Fatalf("%s/%s: resolved %v, want %v", r.TaskID, r.Arm, r.Resolved, wantResolved)
				}
				if wantResolved && (r.Churn == nil || r.Churn.FilesChanged != 1 || r.Localization == nil || r.Localization.Recall != 1) {
					t.Fatalf("%s/%s: churn %+v localization %+v", r.TaskID, r.Arm, r.Churn, r.Localization)
				}
				usesStack := r.Arm == ArmXmustardMCP || r.Arm == ArmXmustardMemory
				if got := len(tr.XmResults); usesStack && got != 2 || !usesStack && got != 0 {
					t.Fatalf("%s/%s: %d xMustard results", r.TaskID, r.Arm, got)
				}
			}
			mem := findRec(recs, "flip-mem", ArmXmustardMemory)
			if mem.Memory == nil || mem.Memory.ScopeLeakage != 0 || mem.Memory.PendingServed != 0 || *mem.Memory.CurrentFactRecall != 1 || mem.Memory.StaleServedFlagged != 1 {
				t.Fatalf("memory metrics: %+v", mem.Memory)
			}
			for _, f := range []string{"eval.json", "report.json", "report.md"} {
				if _, err := os.Stat(filepath.Join(out, f)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// TestSeedCorpusDryRunAllArms is the acceptance dry run: every arm over the seed
// corpus with the fake driver and the stub stack. The hooks arm gets stand-in hook
// arguments (the fake client ignores them), so every arm completes on the seed tasks;
// only the memory arm skips the one task without a memory fixture.
func TestSeedCorpusDryRunAllArms(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test inside the seed fixtures")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	c := loadTestCorpus(t, "../../../eval/tasks/seed.yaml")
	out := filepath.Join(t.TempDir(), "out")
	cfg := prepared(t, &RunConfig{Driver: "fake:claude", Hooks: map[string][]string{"claude": {"--settings", "/nonexistent/hooks.json"}}}, c, out)
	rep, err := Execute(context.Background(), cfg, c)
	if err != nil {
		t.Fatal(err)
	}
	recs := readRecords(t, out)
	if len(recs) != len(c.Tasks)*len(DefaultArms) {
		t.Fatalf("%d records", len(recs))
	}
	for _, r := range recs {
		if r.Status == StatusSkipped {
			if r.Arm != ArmXmustardMemory || r.TaskID != "calc-mean-fraction" || r.Reason == "" {
				t.Fatalf("unexpected skip %s/%s: %q", r.TaskID, r.Arm, r.Reason)
			}
			continue
		}
		if r.Status != StatusCompleted || !r.Resolved {
			t.Fatalf("%s/%s: %s resolved=%v (%s)", r.TaskID, r.Arm, r.Status, r.Resolved, r.Reason)
		}
	}
	if rep.Counts[StatusCompleted] != 14 || rep.Counts[StatusSkipped] != 1 || len(rep.NotCompleted) != 1 {
		t.Fatalf("counts %v", rep.Counts)
	}
	for _, a := range rep.ArmSummaries {
		if a.Completed == 0 {
			t.Fatalf("arm %s completed no task", a.Arm)
		}
	}
	for _, cmp := range rep.Comparisons {
		if cmp.PairedTasks < 2 || cmp.Decision == "NO-DATA" {
			t.Fatalf("comparison %s rests on %d task(s): %s", cmp.ExperimentArm, cmp.PairedTasks, cmp.Decision)
		}
	}
	if len(rep.Memory) != 1 || rep.Memory[0].Runs != 2 || rep.Memory[0].ScopeLeakage != 0 || rep.Memory[0].PendingServed != 0 {
		t.Fatalf("memory summary %+v", rep.Memory)
	}
}

func TestHiddenOracleIsolation(t *testing.T) {
	corpusPath := writeFixtureCorpus(t, nil)
	c := loadTestCorpus(t, corpusPath)
	oracleSrc := filepath.Join(c.Dir, "oracles/check.sh")

	t.Run("absent during agent and visible verify", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "out")
		cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"flip"}}, c, out)
		cfg.fakeProbe = []string{"oracle/check.sh"} // relative to the worktree
		if _, err := Execute(context.Background(), cfg, c); err != nil {
			t.Fatal(err)
		}
		r := readRecords(t, out)[0]
		b, _ := os.ReadFile(filepath.Join(out, r.Artifacts, "transcript.jsonl"))
		if !strings.Contains(string(b), "probe oracle/check.sh: denied") {
			t.Fatalf("oracle destination was readable during the agent run:\n%s", b)
		}
		if r.Verify == nil || !r.Verify.Passed {
			t.Fatalf("visible verify saw the oracle file: %+v", r.Verify)
		}
		if !r.Resolved || !r.Isolation.OracleStagedPost {
			t.Fatalf("oracle must run after the agent: %+v", r.Isolation)
		}
	})

	t.Run("sources unreadable under containment", func(t *testing.T) {
		mode, _ := resolveContainment(ContainAuto)
		if mode == ContainNone {
			t.Skip("no containment available on this host")
		}
		out := filepath.Join(t.TempDir(), "out")
		cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"flip"}, Containment: mode}, c, out)
		cfg.fakeProbe = []string{oracleSrc, c.Path, filepath.Join(out, "eval.json")}
		if _, err := Execute(context.Background(), cfg, c); err != nil {
			t.Fatal(err)
		}
		r := readRecords(t, out)[0]
		b, _ := os.ReadFile(filepath.Join(out, r.Artifacts, "transcript.jsonl"))
		for _, p := range cfg.fakeProbe {
			if !strings.Contains(string(b), "probe "+p+": denied") {
				t.Fatalf("%s readable under %s:\n%s", p, mode, b)
			}
		}
		if r.Isolation.Containment != mode || !r.Resolved {
			t.Fatalf("containment %s resolved %v", r.Isolation.Containment, r.Resolved)
		}
		// control: without containment the same probe succeeds, so "denied" above is
		// the sandbox, not a probe bug.
		out2 := filepath.Join(t.TempDir(), "out")
		cfg2 := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"flip"}}, c, out2)
		cfg2.fakeProbe = []string{oracleSrc}
		if _, err := Execute(context.Background(), cfg2, c); err != nil {
			t.Fatal(err)
		}
		b2, _ := os.ReadFile(filepath.Join(out2, readRecords(t, out2)[0].Artifacts, "transcript.jsonl"))
		if !strings.Contains(string(b2), "probe "+oracleSrc+": readable") {
			t.Fatalf("control probe failed:\n%s", b2)
		}
	})

	t.Run("leaked oracle fails the run", func(t *testing.T) {
		leak := writeFixtureCorpus(t, func(dir string) string {
			mustWrite(t, filepath.Join(dir, "fixtures/leaky/value.txt"), "old\n")
			mustWrite(t, filepath.Join(dir, "fixtures/leaky/tests/copy.sh"), "grep -qx new value.txt\n")
			return `
  - id: leaky
    class: bugfix
    repo: {fixture: fixtures/leaky}
    prompt: Change value.txt.
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, oracle/check.sh]
`
		})
		lc := loadTestCorpus(t, leak)
		out := filepath.Join(t.TempDir(), "out")
		cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"leaky"}}, lc, out)
		if _, err := Execute(context.Background(), cfg, lc); err != nil {
			t.Fatal(err)
		}
		r := readRecords(t, out)[0]
		if r.Status != StatusError || !strings.Contains(r.Reason, "oracle_visible") || r.Isolation.OracleLeakScan != "leaked" || r.Client != nil {
			t.Fatalf("leak not caught before the agent: %s %q %+v", r.Status, r.Reason, r.Isolation)
		}
		if !r.Isolation.WorktreeRemoved {
			t.Fatal("worktree kept after a failed run")
		}
	})
}

func TestWorktreeDetachedAndRemoved(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	cache := t.TempDir()
	repo, sha, err := prepareRepo(c, &c.Tasks[0], cache)
	if err != nil {
		t.Fatal(err)
	}
	// fixtures commit deterministically
	repo2, sha2, err := prepareRepo(c, &c.Tasks[0], t.TempDir())
	if err != nil || sha2 != sha || repo2 == repo {
		t.Fatalf("fixture sha not deterministic: %s vs %s (%v)", sha, sha2, err)
	}
	dir := filepath.Join(t.TempDir(), "wt")
	w, err := addDetachedWorktree(repo, sha, dir)
	if err != nil {
		t.Fatal(err)
	}
	porcelain, _ := git(repo, "worktree", "list", "--porcelain")
	if !strings.Contains(porcelain, "detached") {
		t.Fatalf("worktree not detached:\n%s", porcelain)
	}
	if ok, _ := registeredWorktree(repo, dir); !ok {
		t.Fatal("worktree not registered")
	}
	if err := w.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("worktree directory survived removal")
	}
	if ok, _ := registeredWorktree(repo, dir); ok {
		t.Fatal("worktree still registered")
	}
}

// TestPathRepoHistoryStopsAtRef: a path repository is fetched only up to its ref, so
// the future fix commit is unreachable from the agent's worktree.
func TestPathRepoHistoryStopsAtRef(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "value.txt"), "old\n")
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base"}} {
		if _, err := git(src, a...); err != nil {
			t.Fatal(err)
		}
	}
	base, _ := git(src, "rev-parse", "HEAD")
	mustWrite(t, filepath.Join(src, "value.txt"), "new\n")
	if _, err := git(src, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qam", "fix"); err != nil {
		t.Fatal(err)
	}
	fix, _ := git(src, "rev-parse", "HEAD")
	if _, err := git(src, "branch", "-q", "future", fix); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "o.sh"), "true\n")
	mustWrite(t, filepath.Join(dir, "c.yaml"), "schema: xmustard.eval/v1\nname: p\ntasks:\n  - id: p\n    class: bugfix\n    repo: {path: "+src+", ref: "+base+"}\n    prompt: x\n    oracle: {cmd: [sh, -c, 'true']}\n")
	c := loadTestCorpus(t, filepath.Join(dir, "c.yaml"))
	repo, sha, err := prepareRepo(c, &c.Tasks[0], t.TempDir())
	if err != nil || sha != base {
		t.Fatalf("materialize: %s %v", sha, err)
	}
	if _, err := git(repo, "cat-file", "-e", fix+"^{commit}"); err == nil {
		t.Fatal("future fix commit is reachable from the scratch repository")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "FETCH_HEAD")); !os.IsNotExist(err) {
		t.Fatal("FETCH_HEAD (naming the source repository) was left behind")
	}
	if !slices.Contains(c.hiddenPaths(), src) {
		t.Fatal("the original path repository must be hidden from the agent")
	}
}

// TestRunsDoNotShareRepositoryState: what one run's agent commits, branches or
// configures stays in its own per-run repository.
func TestRunsDoNotShareRepositoryState(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	scratch, sha, err := prepareRepo(c, &c.Tasks[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	w1, err := newRunWorktree(scratch, sha, filepath.Join(root, "r1"), filepath.Join(root, "w1"), "app", filepath.Join(root, "h1"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(w1.Dir, "value.txt"), "leaked answer\n")
	for _, a := range [][]string{{"add", "-A"}, {"-c", "user.name=a", "-c", "user.email=a@a", "commit", "-qm", "agent"}, {"branch", "agent-branch"}, {"config", "alias.x", "log"}} {
		if _, err := git(w1.Dir, a...); err != nil {
			t.Fatal(err)
		}
	}
	agentCommit, _ := git(w1.Dir, "rev-parse", "HEAD")
	if err := w1.Remove(); err != nil || !w1.gone() {
		t.Fatalf("remove: %v", err)
	}
	w2, err := newRunWorktree(scratch, sha, filepath.Join(root, "r2"), filepath.Join(root, "w2"), "app", filepath.Join(root, "h2"))
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Remove()
	if refs, _ := git(w2.Dir, "for-each-ref"); refs != "" {
		t.Fatalf("refs leaked into the next run: %s", refs)
	}
	if _, err := git(w2.Dir, "cat-file", "-e", agentCommit); err == nil {
		t.Fatal("the previous run's commit is readable from the next run")
	}
	if v, _ := git(w2.Dir, "config", "alias.x"); v != "" {
		t.Fatal("config leaked into the next run")
	}
	if b, _ := os.ReadFile(filepath.Join(w2.Dir, "value.txt")); string(b) != "old\n" {
		t.Fatalf("next run starts from %q", b)
	}
	if head, _ := git(w2.Dir, "rev-parse", "HEAD"); head != sha {
		t.Fatal("next run is not at the task commit")
	}
}

func TestCleanupOnFailureAndClientError(t *testing.T) {
	failing := writeFixtureCorpus(t, func(string) string {
		return `
  - id: bad-setup
    class: bugfix
    repo: {fixture: fixtures/app}
    setup: [{cmd: [sh, -c, "exit 3"]}]
    prompt: x
    oracle: {cmd: [sh, -c, "true"]}
`
	})
	c := loadTestCorpus(t, failing)
	out := filepath.Join(t.TempDir(), "out")
	cfg := prepared(t, &RunConfig{Driver: "fake:codex", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"bad-setup", "flip"}}, c, out)
	cfg.fakeExit = 7
	if _, err := Execute(context.Background(), cfg, c); err != nil {
		t.Fatal(err)
	}
	recs := readRecords(t, out)
	bad, flip := findRec(recs, "bad-setup", ArmBaselineNoMCP), findRec(recs, "flip", ArmBaselineNoMCP)
	if bad.Status != StatusError || !strings.Contains(bad.Reason, "setup step 0 failed") || !bad.Isolation.WorktreeRemoved {
		t.Fatalf("setup failure: %s %q %+v", bad.Status, bad.Reason, bad.Isolation)
	}
	// a client that exits non-zero is an agent outcome (completed, judged by the
	// oracle), not a harness error; its worktree is still removed
	if flip.Status != StatusCompleted || flip.Client.ExitCode != 7 || !flip.Isolation.WorktreeRemoved {
		t.Fatalf("client failure: %s exit %d %+v", flip.Status, flip.Client.ExitCode, flip.Isolation)
	}
}

func TestInterruptByContextRemovesWorktree(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	out := filepath.Join(t.TempDir(), "out")
	cfg := prepared(t, &RunConfig{Driver: "fake:pi", Arms: []string{ArmXmustardMCP, ArmBaselineNoMCP}, Tasks: []string{"flip"}}, c, out)
	cfg.fakeSleepMS = 60_000
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if m, _ := filepath.Glob(filepath.Join(out, "runs/flip/*/r0/transcript.jsonl")); len(m) > 0 {
				time.Sleep(300 * time.Millisecond)
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	_, err := Execute(ctx, cfg, c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("interrupt did not stop the fake agent promptly")
	}
	recs := readRecords(t, out)
	if len(recs) != 1 || recs[0].Status != StatusInterrupted || !recs[0].Isolation.WorktreeRemoved || recs[0].Oracle != nil {
		t.Fatalf("records after interrupt: %+v", recs)
	}
	if c := recs[0].Client; c == nil || groupAlive(c.PID) {
		t.Fatalf("the agent's process group survived the interrupt: %+v", c)
	}
	if _, err := os.Stat(cfg.workRoot); !os.IsNotExist(err) {
		t.Fatalf("work root survived: %v", err)
	}
	var m Manifest
	b, _ := os.ReadFile(filepath.Join(out, "eval.json"))
	if json.Unmarshal(b, &m) != nil || !m.Interrupted {
		t.Fatal("manifest does not record the interrupt")
	}
}

// TestInterruptBySignal sends SIGINT to a real executor process mid-run.
func TestInterruptBySignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signals")
	}
	corpusPath := writeFixtureCorpus(t, nil)
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out")
	self, _ := os.Executable()
	cmd := exec.Command(self, "run", "--corpus", corpusPath, "--out", out, "--driver", "fake:claude",
		"--arms", ArmBaselineNoMCP, "--tasks", "flip", "--containment", "none", "--fake-sleep-ms", "60000")
	cmd.Env = append(os.Environ(), envAsMain+"=1", "TMPDIR="+tmp)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if m, _ := filepath.Glob(filepath.Join(out, "runs/flip/*/r0/transcript.jsonl")); len(m) > 0 {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("agent never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 130 {
		t.Fatalf("want exit 130, got %v", err)
	}
	recs := readRecords(t, out)
	if len(recs) != 1 || recs[0].Status != StatusInterrupted || !recs[0].Isolation.WorktreeRemoved {
		t.Fatalf("records: %+v", recs)
	}
	left, _ := filepath.Glob(filepath.Join(tmp, "xmustard-eval-*"))
	if len(left) != 0 {
		t.Fatalf("work directories left behind: %v", left)
	}
	if c := recs[0].Client; c == nil || !c.Canceled || groupAlive(c.PID) {
		t.Fatalf("the agent's process group survived the interrupt: %+v", c)
	}
}

func TestRunConfigValidation(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	out := t.TempDir()
	cases := []struct {
		name string
		cfg  RunConfig
		want string
	}{
		{"real driver needs model", RunConfig{Driver: "claude", Stack: StackConfig{Kind: StackNone}}, "fixed --model"},
		{"unknown driver", RunConfig{Driver: "gemini"}, "driver must be one of"},
		{"stub stack only for fake", RunConfig{Driver: "claude", Model: "m", Stack: StackConfig{Kind: StackStub}}, "stub stack"},
		{"bad arm", RunConfig{Driver: "fake:claude", Arms: []string{"everything"}}, "unknown arm"},
		{"reference arm not run", RunConfig{Driver: "fake:claude", Arms: []string{ArmXmustardMCP}, ReferenceArm: ArmBaseline}, "reference_arm"},
		{"noncommercial peer needs owner decision", RunConfig{Driver: "fake:claude", Peers: []PeerConfig{{Name: "gitnexus", Command: "gitnexus", License: "PolyForm-Noncommercial-1.0.0"}}}, "owner_decision"},
		{"pi xmustard arms need the adapter", RunConfig{Driver: "pi", Model: "m", Stack: StackConfig{Kind: StackReal, APIBin: "/bin/sh", MCPBin: "/bin/sh", CoreBin: "/bin/sh"}}, "clients.pi.extension"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.Containment = ContainNone
			err := cfg.prepare(c, out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// TestPeerArmWiring: a peer arm hands the client exactly one MCP server, the peer.
func TestPeerArmWiring(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	self, _ := os.Executable()
	for _, drv := range []string{"claude", "codex"} {
		t.Run(drv, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out")
			cfg := prepared(t, &RunConfig{Driver: "fake:" + drv, Arms: []string{ArmBaselineNoMCP, "peer:stubpeer"}, Tasks: []string{"flip"},
				Peers: []PeerConfig{{Name: "stubpeer", License: "MIT", Command: self, Args: []string{"stub-mcp"}, Env: map[string]string{envAsMain: "1"}}}}, c, out)
			if _, err := Execute(context.Background(), cfg, c); err != nil {
				t.Fatal(err)
			}
			r := findRec(readRecords(t, out), "flip", "peer:stubpeer")
			if r == nil || r.Status != StatusCompleted {
				t.Fatalf("peer run: %+v", r)
			}
			b, _ := os.ReadFile(filepath.Join(out, r.Artifacts, "transcript.jsonl"))
			var cfgEv map[string]any
			sc := bufio.NewScanner(strings.NewReader(string(b)))
			for sc.Scan() {
				var ev map[string]any
				if json.Unmarshal(sc.Bytes(), &ev) == nil && ev["type"] == "xm.fake.config" {
					cfgEv = ev
				}
			}
			servers := arr(cfgEv["mcp_servers"])
			if len(servers) != 1 || servers[0] != "stubpeer" {
				t.Fatalf("peer arm servers %v", servers)
			}
			if r.Transcript.XmResults != nil {
				t.Fatal("peer arm reached xMustard")
			}
		})
	}
}

func TestCorpusDefaultsAndHiddenPaths(t *testing.T) {
	c := loadTestCorpus(t, "../../../eval/tasks/seed.yaml")
	hidden := c.hiddenPaths()
	for _, want := range []string{c.Path, c.resolve("oracles/calc_mean_hidden_test.go"), c.resolve("references/service-port.patch")} {
		if !slices.Contains(hidden, want) {
			t.Fatalf("%s not hidden: %v", want, hidden)
		}
	}
	for _, p := range hidden {
		if isWithin(p, c.resolve("fixtures")) {
			t.Fatalf("fixture path %s would be hidden from the agent", p)
		}
	}
	if c.timeoutSec(&c.Tasks[0]) != 900 {
		t.Fatal("default timeout")
	}
}

// TestExampleRunConfigParses keeps eval/tasks/run-config.example.yaml in step with
// the RunConfig schema (strict decoding rejects unknown keys).
func TestExampleRunConfigParses(t *testing.T) {
	var cfg RunConfig
	if err := loadYAMLStrict("../../../eval/tasks/run-config.example.yaml", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Driver != "claude" || cfg.Stack.Kind != StackReal || len(cfg.Arms) != len(DefaultArms) || cfg.Pricing["gpt-5-codex"].OutputPerMTok != 10 ||
		cfg.Clients["pi"].Extension == "" || len(cfg.Hooks["claude"]) != 2 || cfg.Thresholds.BootstrapIters != 10000 {
		t.Fatalf("example config decoded to %+v", cfg)
	}
	for _, p := range cfg.Peers {
		if err := p.validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range cfg.Arms {
		if _, err := parseArm(a); err != nil {
			t.Fatal(err)
		}
	}
}

// TestHooksArmRunsWhenConfigured: with hook arguments for the driver the hooks arm is
// xmustard_mcp plus those arguments.
func TestHooksArmRunsWhenConfigured(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	out := filepath.Join(t.TempDir(), "out")
	cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmXmustardMCPHooks}, Tasks: []string{"flip"},
		Hooks: map[string][]string{"claude": {"--settings", "/nonexistent/hooks.json"}}}, c, out)
	if _, err := Execute(context.Background(), cfg, c); err != nil {
		t.Fatal(err)
	}
	r := readRecords(t, out)[0]
	if r.Status != StatusCompleted || len(r.Transcript.XmResults) != 2 {
		t.Fatalf("hooks arm: %s %q %+v", r.Status, r.Reason, r.Transcript)
	}
	b, _ := os.ReadFile(filepath.Join(out, r.Artifacts, "transcript.jsonl"))
	if !strings.Contains(string(b), `"--settings"`) || !strings.Contains(string(b), `"--strict-mcp-config"`) {
		t.Fatalf("hook arguments not passed:\n%s", b)
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Regression tests for the round-1 review: code the agent wrote runs again after the
// agent (verify, the oracle, git config the harness's git would honour, detached
// processes), and every such path must stay inside the run's containment.

func availableContainment(t *testing.T) string {
	t.Helper()
	mode, _ := resolveContainment(ContainAuto)
	if mode == ContainNone {
		t.Skip("no containment available on this host")
	}
	return mode
}

// verifyEscapeCorpus is the reviewer's reproduction: the agent's "fix" rewrites the
// visible verify script so that, when the harness runs it after the agent, it reads
// the hidden oracle and the reference patch and writes the answer into the tree.
func verifyEscapeCorpus(t *testing.T) (corpusPath, oracleSrc, refPatch string) {
	t.Helper()
	dir := t.TempDir()
	oracleSrc = filepath.Join(dir, "oracles/check.sh")
	refPatch = filepath.Join(dir, "references/fix.patch")
	mustWrite(t, filepath.Join(dir, "fixtures/app/value.txt"), "old\n")
	mustWrite(t, filepath.Join(dir, "fixtures/app/verify.sh"), "true\n")
	mustWrite(t, oracleSrc, "grep -qx new value.txt # SECRET-ORACLE\n")
	mustWrite(t, refPatch, `diff --git a/verify.sh b/verify.sh
--- a/verify.sh
+++ b/verify.sh
@@ -1 +1 @@
-true
+cat `+oracleSrc+` `+refPatch+` > leaked.txt 2>&1; cat leaked.txt; echo new > value.txt
`)
	corpusPath = filepath.Join(dir, "corpus.yaml")
	mustWrite(t, corpusPath, `schema: xmustard.eval/v1
name: escape
tasks:
  - id: escape
    class: bugfix
    repo: {fixture: fixtures/app}
    prompt: Change value.txt.
    verify: {cmd: [sh, verify.sh]}
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, -c, "cat `+refPatch+`; sh oracle/check.sh"]
    reference: {patch: references/fix.patch}
`)
	return corpusPath, oracleSrc, refPatch
}

func TestVerifyAndOracleRunContainedAndCannotChangeTheJudgedTree(t *testing.T) {
	corpusPath, _, _ := verifyEscapeCorpus(t)
	c := loadTestCorpus(t, corpusPath)
	for _, mode := range []string{ContainNone, ContainAuto} {
		t.Run(mode, func(t *testing.T) {
			if mode == ContainAuto {
				mode = availableContainment(t)
			}
			out := filepath.Join(t.TempDir(), "out")
			cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Containment: mode}, c, out)
			if _, err := Execute(context.Background(), cfg, c); err != nil {
				t.Fatal(err)
			}
			r := readRecords(t, out)[0]
			// the agent's diff touched only verify.sh; verify then wrote value.txt, which
			// must be undone before the oracle judges the tree
			if r.Status != StatusCompleted || !slices.Equal(r.Churn.Files, []string{"verify.sh"}) || !r.Isolation.VerifyChangedTree || r.Resolved {
				t.Fatalf("%s: status %s churn %v isolation %+v resolved %v", mode, r.Status, r.Churn.Files, r.Isolation, r.Resolved)
			}
			if mode == ContainNone {
				return
			}
			for _, log := range []string{"verify.log", "oracle.log"} {
				b, _ := os.ReadFile(filepath.Join(out, r.Artifacts, log))
				if strings.Contains(string(b), "SECRET-ORACLE") || strings.Contains(string(b), "+cat ") {
					t.Fatalf("%s read a hidden file under %s:\n%s", log, mode, b)
				}
			}
			if !hasWarning(buildReportFromDir(t, out), "visible verify step changed the tree") {
				t.Fatal("report does not flag the verify-time change")
			}
		})
	}
}

func buildReportFromDir(t *testing.T, out string) *Report {
	t.Helper()
	rep, err := writeReport(out)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestOracleStagedFromLoadTimeBytes: a corpus file changed on disk after loading can
// change neither what is staged nor go unnoticed.
func TestOracleStagedFromLoadTimeBytes(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	src := c.resolve("oracles/check.sh")
	mustWrite(t, src, "true # tampered\n")
	wt := t.TempDir()
	if err := stageOracle(c, &c.Tasks[0], wt); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "oracle/check.sh")); string(b) != "grep -qx new value.txt\n" {
		t.Fatalf("staged %q, want the load-time bytes", b)
	}
	if err := c.checkIntegrity(); err == nil || !strings.Contains(err.Error(), "oracles/check.sh") {
		t.Fatalf("integrity check: %v", err)
	}
}

// TestCorpusTamperAbortsTheRun: without containment, code run by verify can rewrite a
// hidden oracle source. The run fails, no further run starts, and the report says so.
func TestCorpusTamperAbortsTheRun(t *testing.T) {
	dir := t.TempDir()
	oracle := filepath.Join(dir, "oracles/check.sh")
	mustWrite(t, filepath.Join(dir, "fixtures/app/value.txt"), "old\n")
	mustWrite(t, filepath.Join(dir, "fixtures/app/verify.sh"), "true\n")
	mustWrite(t, oracle, "grep -qx new value.txt\n")
	mustWrite(t, filepath.Join(dir, "references/fix.patch"), `diff --git a/verify.sh b/verify.sh
--- a/verify.sh
+++ b/verify.sh
@@ -1 +1 @@
-true
+echo true > `+oracle+`
`)
	corpusPath := filepath.Join(dir, "corpus.yaml")
	mustWrite(t, corpusPath, `schema: xmustard.eval/v1
name: tamper
tasks:
  - id: tamper
    class: bugfix
    repo: {fixture: fixtures/app}
    prompt: x
    verify: {cmd: [sh, verify.sh]}
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, oracle/check.sh]
    reference: {patch: references/fix.patch}
`)
	c := loadTestCorpus(t, corpusPath)
	out := filepath.Join(t.TempDir(), "out")
	cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaseline, ArmBaselineNoMCP}}, c, out)
	_, err := Execute(context.Background(), cfg, c)
	if err == nil || !strings.Contains(err.Error(), "corpus_changed") {
		t.Fatalf("want an abort, got %v", err)
	}
	recs := readRecords(t, out)
	if len(recs) != 1 || recs[0].Status != StatusError || !strings.Contains(recs[0].Reason, "corpus_changed") || recs[0].Oracle != nil {
		t.Fatalf("records %+v", recs)
	}
	if rep := buildReportFromDir(t, out); !hasWarning(rep, "ABORTED") {
		t.Fatalf("warnings %v", rep.Warnings)
	}
}

// TestHarnessGitIgnoresAgentConfig: the agent can write its run repository's config,
// the worktree's .git file and .gitattributes. None of the configured commands may
// run when the harness snapshots, diffs, restores or removes the worktree.
func TestHarnessGitIgnoresAgentConfig(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	scratch, sha, err := prepareRepo(c, &c.Tasks[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	marks := t.TempDir()
	w, err := newRunWorktree(scratch, sha, filepath.Join(root, "r"), filepath.Join(root, "w"), filepath.Join(root, "h"))
	if err != nil {
		t.Fatal(err)
	}
	base, err := w.SnapshotTree()
	if err != nil {
		t.Fatal(err)
	}
	touch := func(name string) string { return "sh -c 'touch " + filepath.Join(marks, name) + "; cat'" }
	for _, kv := range [][2]string{
		{"core.fsmonitor", "sh -c 'touch " + filepath.Join(marks, "FSMONITOR") + "'"},
		{"filter.pwn.clean", touch("CLEAN")},
		{"filter.pwn.smudge", touch("SMUDGE")},
		{"diff.external", touch("EXTDIFF")},
		{"diff.pwn.textconv", touch("TEXTCONV")},
		{"core.hooksPath", marks},
	} {
		if out, err := exec.Command("git", "-C", w.Dir, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("agent git config %s: %v %s", kv[0], err, out)
		}
	}
	mustWrite(t, filepath.Join(marks, "post-index-change"), "#!/bin/sh\ntouch "+filepath.Join(marks, "HOOK")+"\n")
	_ = os.Chmod(filepath.Join(marks, "post-index-change"), 0o755)
	mustWrite(t, filepath.Join(w.Dir, ".gitattributes"), "* filter=pwn diff=pwn\n")
	mustWrite(t, filepath.Join(w.Dir, "value.txt"), "new\n")
	// and point the worktree's .git file at a repository the agent controls
	evil := filepath.Join(root, "evil")
	if out, err := exec.Command("git", "init", "-q", evil).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if out, err := exec.Command("git", "-C", evil, "config", "core.fsmonitor", "sh -c 'touch "+filepath.Join(marks, "EVIL")+"'").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	mustWrite(t, filepath.Join(w.Dir, ".git"), "gitdir: "+filepath.Join(evil, ".git")+"\n")

	final, err := w.SnapshotTree()
	if err != nil {
		t.Fatal(err)
	}
	ch, err := diffChurn(w, base, final, filepath.Join(root, "diff.patch"))
	if err != nil || !slices.Equal(ch.Files, []string{".gitattributes", "value.txt"}) {
		t.Fatalf("churn %+v %v", ch, err)
	}
	mustWrite(t, filepath.Join(w.Dir, "value.txt"), "changed by verify\n")
	if err := w.RestoreTree(final); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(w.Dir, "value.txt")); string(b) != "new\n" {
		t.Fatalf("restore left %q", b)
	}
	if err := w.Remove(); err != nil || !w.gone() {
		t.Fatalf("remove: %v", err)
	}
	if ran, _ := filepath.Glob(filepath.Join(marks, "[A-Z]*")); len(ran) > 0 {
		t.Fatalf("harness git ran agent-configured commands: %v", ran)
	}
}

// TestCorpusInsideRepositoryIsHidden: a corpus committed to a repository is readable
// through that repository's objects and through other worktrees' copies unless those
// are hidden too.
func TestCorpusInsideRepositoryIsHidden(t *testing.T) {
	mode := availableContainment(t)
	corpusPath := writeFixtureCorpus(t, nil)
	repo := filepath.Dir(corpusPath)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "corpus"}} {
		if _, err := git(repo, a...); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(t.TempDir(), "other")
	if _, err := git(repo, "worktree", "add", "-q", "--detach", other, "HEAD"); err != nil {
		t.Fatal(err)
	}
	c := loadTestCorpus(t, corpusPath)
	probes := []string{"git-show:" + repo + ":HEAD:oracles/check.sh", filepath.Join(other, "oracles/check.sh"), filepath.Join(other, "references/fix.patch")}
	run := func(mode string) (string, *RunConfig) {
		out := filepath.Join(t.TempDir(), "out")
		cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"flip"}, Containment: mode}, c, out)
		cfg.fakeProbe = probes
		if _, err := Execute(context.Background(), cfg, c); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(out, readRecords(t, out)[0].Artifacts, "transcript.jsonl"))
		return string(b), cfg
	}
	got, cfg := run(mode)
	for _, p := range probes {
		if !strings.Contains(got, "probe "+p+": denied") {
			t.Fatalf("%s readable under %s:\n%s", p, mode, got)
		}
	}
	realGit, _ := filepath.EvalSymlinks(filepath.Join(repo, ".git"))
	if !slices.Contains(cfg.hiddenRepos, realGit) {
		t.Fatalf("hidden repository paths %v lack %s", cfg.hiddenRepos, realGit)
	}
	// control: the same probes succeed without containment
	if got, _ := run(ContainNone); !strings.Contains(got, "probe "+probes[0]+": readable") || !strings.Contains(got, "probe "+probes[1]+": readable") {
		t.Fatalf("control probes failed:\n%s", got)
	}
}

// TestEscapedProcessesAreKilled: processes that leave the client's session (setsid)
// are found by the run marker or their working directory and killed before the tree
// is judged, whether they kept the environment or cleared it.
func TestEscapedProcessesAreKilled(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	out := filepath.Join(t.TempDir(), "out")
	cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"flip"}}, c, out)
	cfg.fakeDetach = true
	if _, err := Execute(context.Background(), cfg, c); err != nil {
		t.Fatal(err)
	}
	r := readRecords(t, out)[0]
	b, _ := os.ReadFile(filepath.Join(out, r.Artifacts, "transcript.jsonl"))
	var pids []int
	for _, m := range regexp.MustCompile(`detached (\d+)`).FindAllStringSubmatch(string(b), -1) {
		pid, _ := strconv.Atoi(m[1])
		if !slices.Contains(pids, pid) {
			pids = append(pids, pid)
		}
	}
	if len(pids) != 2 {
		t.Fatalf("fake agent did not detach two children:\n%s", b)
	}
	if r.Status != StatusCompleted || !r.Resolved || r.Isolation.EscapedKilled < 2 {
		t.Fatalf("status %s isolation %+v", r.Status, r.Isolation)
	}
	for _, pid := range pids {
		deadline := time.Now().Add(5 * time.Second)
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("detached process %d survived the run", pid)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// TestKeepWorktreesKeepsAndHides: kept worktrees survive Execute with the oracle
// removed, and a later run's agent cannot read an earlier run's tree.
func TestKeepWorktreesKeepsAndHides(t *testing.T) {
	mode := availableContainment(t)
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	arms := []string{ArmBaselineNoMCP, ArmBaseline}
	var probes []string
	for _, a := range arms {
		probes = append(probes, "../../"+opaqueRunID("flip", a, 0)+"/app/value.txt")
	}
	out := filepath.Join(t.TempDir(), "out")
	cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: arms, Tasks: []string{"flip"}, Containment: mode, KeepWorktrees: true}, c, out)
	cfg.fakeProbe = probes
	if _, err := Execute(context.Background(), cfg, c); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cfg.workRoot)
	recs := readRecords(t, out)
	slices.SortFunc(recs, func(a, b RunRecord) int { return strings.Compare(a.StartedAt, b.StartedAt) })
	for _, r := range recs {
		iso := r.Isolation
		if r.Status != StatusCompleted || iso.WorktreeRemoved || iso.KeptAt == "" {
			t.Fatalf("%s: %s %+v", r.Arm, r.Status, iso)
		}
		if b, err := os.ReadFile(filepath.Join(iso.KeptAt, "value.txt")); err != nil || string(b) != "new\n" {
			t.Fatalf("kept worktree %s: %q %v", iso.KeptAt, b, err)
		}
		if _, err := os.Stat(filepath.Join(iso.KeptAt, "oracle/check.sh")); !os.IsNotExist(err) {
			t.Fatal("the oracle was left staged in a kept worktree")
		}
	}
	first := "../../" + opaqueRunID("flip", recs[0].Arm, 0) + "/app/value.txt"
	b, _ := os.ReadFile(filepath.Join(out, recs[1].Artifacts, "transcript.jsonl"))
	if !strings.Contains(string(b), "probe "+first+": denied") {
		t.Fatalf("the second run could read the first run's kept worktree:\n%s", b)
	}
	if rep := buildReportFromDir(t, out); !hasWarning(rep, "keep_worktrees") {
		t.Fatalf("warnings %v", rep.Warnings)
	}
}

// TestExecutorKilledCleansUp: when the executor dies without cleaning up (SIGKILL),
// its watchdog kills the agent's process group and removes the work root.
func TestExecutorKilledCleansUp(t *testing.T) {
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
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var state string
	deadline := time.Now().Add(30 * time.Second)
	groupRE := regexp.MustCompile(`(?m)^group (\d+)$`)
	var groups [][]string
	for {
		if m, _ := filepath.Glob(filepath.Join(tmp, "xmustard-eval-*", "harness", "watchdog.state")); len(m) == 1 {
			state = m[0]
			b, _ := os.ReadFile(state)
			if groups = groupRE.FindAllStringSubmatch(string(b), -1); len(groups) > 0 {
				if m, _ := filepath.Glob(filepath.Join(out, "runs/flip/*/r0/transcript.jsonl")); len(m) > 0 {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	workRoot := filepath.Dir(filepath.Dir(state))
	deadline = time.Now().Add(15 * time.Second)
	for {
		alive := false
		for _, g := range groups {
			pid, _ := strconv.Atoi(g[1])
			alive = alive || groupAlive(pid)
		}
		_, statErr := os.Stat(workRoot)
		if !alive && errors.Is(statErr, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the executor died: groups alive %v, work root %v", alive, statErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestManifestRecordsCorpusDigests: eval.json binds the run to the oracle and
// reference bytes it used.
func TestManifestRecordsCorpusDigests(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	out := filepath.Join(t.TempDir(), "out")
	cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"flip"}}, c, out)
	if _, err := Execute(context.Background(), cfg, c); err != nil {
		t.Fatal(err)
	}
	var m Manifest
	b, _ := os.ReadFile(filepath.Join(out, "eval.json"))
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want, _ := fileSHA256(c.resolve("oracles/check.sh"))
	if m.CorpusFiles["oracles/check.sh"] != want || m.CorpusFiles["references/fix.patch"] == "" {
		t.Fatalf("corpus digests %v", m.CorpusFiles)
	}
}

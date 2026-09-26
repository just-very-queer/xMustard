package main

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Regression tests for the round-3 review: the oracle must see setup's absolute paths
// resolve into the judged copy, and nothing the harness does with the operator's
// rights may follow a link the agent planted.

// modes is containment none plus the strongest mode this host has, if any.
func modes(t *testing.T) []string {
	out := []string{ContainNone}
	if m, _ := resolveContainment(ContainAuto); m != ContainNone {
		out = append(out, m)
	}
	return out
}

// absolutePathCorpus: setup writes a script that names the worktree by its absolute
// path (as a virtualenv's entry scripts or an editable install do), in an ignored
// directory, and the oracle runs that script by the recorded path. In "abs" the
// agent's verify step then rewrites value.txt, so only the judged copy holds the
// agent's answer.
func absolutePathCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "fixtures/app/value.txt"), "old\n")
	mustWrite(t, filepath.Join(dir, "fixtures/app/verify.sh"), "true\n")
	mustWrite(t, filepath.Join(dir, "fixtures/app/.gitignore"), ".tools/\n")
	mustWrite(t, filepath.Join(dir, "fixtures/app/setup.sh"), `mkdir -p .tools
root=$(pwd -P)
printf '#!/bin/sh\ncat %s/value.txt\n' "$root" > .tools/show
chmod +x .tools/show
printf '%s\n' "$root" > .tools/root
`)
	mustWrite(t, filepath.Join(dir, "oracles/check.sh"), `"$(cat .tools/root)/.tools/show" | grep -qx new
`)
	mustWrite(t, filepath.Join(dir, "references/value.patch"), fixPatch)
	mustWrite(t, filepath.Join(dir, "references/fix.patch"), fixPatch+`diff --git a/verify.sh b/verify.sh
--- a/verify.sh
+++ b/verify.sh
@@ -1 +1 @@
-true
+echo broken > value.txt
`)
	path := filepath.Join(dir, "corpus.yaml")
	mustWrite(t, path, `schema: xmustard.eval/v1
name: abs
tasks:
  - id: abs
    class: bugfix
    repo: {fixture: fixtures/app}
    setup: [{cmd: [sh, setup.sh]}]
    prompt: Change value.txt.
    verify: {cmd: [sh, verify.sh]}
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, oracle/check.sh]
    reference: {patch: references/fix.patch}
  - id: abs-validate
    class: bugfix
    repo: {fixture: fixtures/app}
    setup: [{cmd: [sh, setup.sh]}]
    prompt: Change value.txt.
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, oracle/check.sh]
    reference: {patch: references/value.patch}
  - id: needs-hidden
    class: bugfix
    repo: {fixture: fixtures/app}
    prompt: Change value.txt.
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, -c, "cat `+path+` >/dev/null && grep -qx new value.txt"]
    reference: {patch: references/value.patch}
`)
	return path
}

// TestOracleRunsAtTheWorktreePath is the round-3 reproduction: a setup output that
// names the worktree by its absolute path must, while the oracle runs, reach the
// judged copy. Before, it was denied under containment (exit 126, silently
// unresolved) and, without containment, it read the worktree as verify left it.
func TestOracleRunsAtTheWorktreePath(t *testing.T) {
	c := loadTestCorpus(t, absolutePathCorpus(t))
	for _, mode := range modes(t) {
		t.Run(mode, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out")
			cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaseline, ArmBaselineNoMCP}, Tasks: []string{"abs"}, Containment: mode}, c, out)
			if _, err := Execute(context.Background(), cfg, c); err != nil {
				t.Fatal(err)
			}
			for _, r := range readRecords(t, out) {
				log, _ := os.ReadFile(filepath.Join(out, r.Artifacts, "oracle.log"))
				if r.Status != StatusCompleted || !r.Resolved || !r.Isolation.VerifyChangedTree || !r.Isolation.WorktreeRemoved {
					t.Fatalf("%s: status %s (%s) resolved %v isolation %+v oracle %+v\n%s", r.Arm, r.Status, r.Reason, r.Resolved, r.Isolation, r.Oracle, log)
				}
			}
			if rep := buildReportFromDir(t, out); hasWarning(rep, "exited 12") {
				t.Fatalf("warnings %v", rep.Warnings)
			}
		})
	}
	// validate judges the same way, under the same containment as a run
	for _, mode := range modes(t) {
		res, err := validateOracles(context.Background(), c, []string{"abs-validate", "needs-hidden"}, mode, "")
		if err != nil {
			t.Fatal(err)
		}
		if !res[0].Valid || len(res[0].Warnings) > 0 {
			t.Fatalf("%s: %+v", mode, res[0])
		}
		// an oracle that needs a hidden path passes only where nothing is hidden
		if res[1].Valid != (mode == ContainNone) {
			t.Fatalf("%s: an oracle reading the corpus validates as %v", mode, res[1].Valid)
		}
	}
}

// TestUnrunnableOracleIsFlagged: an oracle that exits 126 or 127 most likely never ran
// its check; the report and validate say so.
func TestUnrunnableOracleIsFlagged(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, func(string) string {
		return `
  - id: missing
    class: bugfix
    repo: {fixture: fixtures/app}
    prompt: Change value.txt.
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, -c, "./not-installed/bin/tool && sh oracle/check.sh"]
    reference: {patch: references/fix.patch}
`
	}))
	out := filepath.Join(t.TempDir(), "out")
	cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"missing"}}, c, out)
	if _, err := Execute(context.Background(), cfg, c); err != nil {
		t.Fatal(err)
	}
	if r := readRecords(t, out)[0]; r.Status != StatusCompleted || r.Resolved || r.Oracle.ExitCode != 127 {
		t.Fatalf("status %s resolved %v oracle %+v", r.Status, r.Resolved, r.Oracle)
	}
	if rep := buildReportFromDir(t, out); !hasWarning(rep, "exited 127") {
		t.Fatalf("warnings %v", rep.Warnings)
	}
	res, err := validateOracles(context.Background(), c, []string{"missing"}, ContainNone, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Warnings) != 2 || !strings.Contains(res[0].Warnings[0], "exited 127") {
		t.Fatalf("validate: %+v", res[0])
	}
}

// replacePatch rewrites verify.sh, which the harness runs after the agent, to run
// script.
func replacePatch(script string) string {
	return `diff --git a/verify.sh b/verify.sh
--- a/verify.sh
+++ b/verify.sh
@@ -1 +1 @@
-true
+` + script + `
`
}

// TestReplacedRunDirectoryIsNotFollowed: code the agent wrote (here the verify step)
// replaces its worktree, or its whole run directory, with a link to a directory
// outside. The harness has the operator's rights: it must neither kill processes
// working there (the sweep matches working directories), nor read, copy or delete
// what the link names. The run fails as worktree_replaced, or, where the run's
// profile pins its run directory (sandbox-exec), the replacement itself is denied.
func TestReplacedRunDirectoryIsNotFollowed(t *testing.T) {
	for _, mode := range modes(t) {
		for _, what := range []string{"worktree", "run directory"} {
			t.Run(mode+"/"+what, func(t *testing.T) {
				victim, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				mustWrite(t, filepath.Join(victim, "app", "secret.txt"), "operator data\n")
				// a process of the operator's, working in the directory the link names
				bystander := exec.Command("sleep", "120")
				bystander.Dir = filepath.Join(victim, "app")
				if err := bystander.Start(); err != nil {
					t.Fatal(err)
				}
				exited := make(chan struct{})
				go func() { _ = bystander.Wait(); close(exited) }()
				defer func() { _ = bystander.Process.Kill(); <-exited }()
				script := `here=$(pwd -P); cd /; mv "$here" "$here.moved" && ln -s '` + filepath.Join(victim, "app") + `' "$here"`
				if what == "run directory" {
					moved := filepath.Join(t.TempDir(), "moved")
					script = `run=$(cd .. && pwd -P); cd /; mv "$run" '` + moved + `' && ln -s '` + victim + `' "$run"`
				}
				corpusPath := writeFixtureCorpus(t, func(dir string) string {
					mustWrite(t, filepath.Join(dir, "fixtures/app/verify.sh"), "true\n")
					mustWrite(t, filepath.Join(dir, "references/replace.patch"), replacePatch(script))
					return `
  - id: replace
    class: bugfix
    repo: {fixture: fixtures/app}
    prompt: Change value.txt.
    verify: {cmd: [sh, verify.sh]}
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, oracle/check.sh]
    reference: {patch: references/replace.patch}
`
				})
				c := loadTestCorpus(t, corpusPath)
				out := filepath.Join(t.TempDir(), "out")
				cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"replace"}, Containment: mode}, c, out)
				if _, err := Execute(context.Background(), cfg, c); err != nil {
					t.Fatal(err)
				}
				r := readRecords(t, out)[0]
				if mode == ContainSandbox && what == "run directory" {
					if r.Status != StatusCompleted || r.Verify.Passed || r.Resolved {
						t.Fatalf("status %s reason %q verify %+v: the run replaced its pinned run directory", r.Status, r.Reason, r.Verify)
					}
				} else if r.Status != StatusError || !strings.Contains(r.Reason, "worktree_replaced") || r.Resolved {
					t.Fatalf("status %s reason %q resolved %v", r.Status, r.Reason, r.Resolved)
				}
				select {
				case <-exited:
					t.Fatal("the sweep followed the agent's link and killed a process working where it points")
				default:
				}
				if b, err := os.ReadFile(filepath.Join(victim, "app", "secret.txt")); err != nil || string(b) != "operator data\n" {
					t.Fatalf("the directory the link named was changed: %q %v", b, err)
				}
				if !r.Isolation.WorktreeRemoved {
					t.Fatal("the run directory was not removed")
				}
			})
		}
	}
}

// linksPatch adds links to /etc and to the home directory, and fixes value.txt.
func linksPatch(home string) string {
	link := func(name, target string) string {
		return "diff --git a/" + name + " b/" + name + "\nnew file mode 120000\n--- /dev/null\n+++ b/" + name +
			"\n@@ -0,0 +1 @@\n+" + target + "\n\\ No newline at end of file\n"
	}
	return link("etc", "/etc") + link("home", home) + fixPatch
}

// TestLinksOutOfTheTreeStayLinks: a tree whose links name /etc and the home directory
// is judged with the links as links; nothing they name is copied.
func TestLinksOutOfTheTreeStayLinks(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	corpusPath := writeFixtureCorpus(t, func(dir string) string {
		mustWrite(t, filepath.Join(dir, "references/links.patch"), linksPatch(home))
		return `
  - id: links
    class: bugfix
    repo: {fixture: fixtures/app}
    prompt: Change value.txt.
    oracle:
      files: [{src: oracles/check.sh, dest: oracle/check.sh}]
      cmd: [sh, -c, 'test -L etc && test -L home && test "$(readlink etc)" = /etc && sh oracle/check.sh']
    reference: {patch: references/links.patch}
`
	})
	c := loadTestCorpus(t, corpusPath)
	for _, mode := range modes(t) {
		out := filepath.Join(t.TempDir(), "out")
		cfg := prepared(t, &RunConfig{Driver: "fake:claude", Arms: []string{ArmBaselineNoMCP}, Tasks: []string{"links"}, Containment: mode}, c, out)
		if _, err := Execute(context.Background(), cfg, c); err != nil {
			t.Fatal(err)
		}
		if r := readRecords(t, out)[0]; r.Status != StatusCompleted || !r.Resolved {
			log, _ := os.ReadFile(filepath.Join(out, r.Artifacts, "oracle.log"))
			t.Fatalf("%s: status %s (%s) resolved %v\n%s", mode, r.Status, r.Reason, r.Resolved, log)
		}
	}

	// the copy itself, both ways: clonefile where there is one, and the walk
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "sub", "file.txt"), "inside\n")
	for name, target := range map[string]string{"etc": "/etc", "home": home, "rel": "sub", "sub/up": ".."} {
		if err := os.Symlink(target, filepath.Join(src, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	for how, copyFn := range map[string]func(string, string) error{"copyNoFollow": copyNoFollow, "walkCopy": walkCopy} {
		dst := filepath.Join(t.TempDir(), "copy")
		if err := copyFn(src, dst); err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		for name, target := range map[string]string{"etc": "/etc", "home": home, "rel": "sub", "sub/up": ".."} {
			fi, err := os.Lstat(filepath.Join(dst, name))
			got, _ := os.Readlink(filepath.Join(dst, name))
			if err != nil || fi.Mode()&fs.ModeSymlink == 0 || got != target {
				t.Fatalf("%s: %s is %v %q %v, want a link to %s", how, name, fi, got, err, target)
			}
		}
		if b, _ := os.ReadFile(filepath.Join(dst, "sub", "file.txt")); string(b) != "inside\n" {
			t.Fatalf("%s: sub/file.txt %q", how, b)
		}
		if _, err := os.Lstat(filepath.Join(dst, "pipe")); !os.IsNotExist(err) {
			t.Fatalf("%s: the pipe was copied: %v", how, err)
		}
	}
}

// TestWalkCopyRefusesEntriesSwappedMidWalk: the entry-by-entry copy runs with the
// operator's rights. An entry swapped between the listing and the open (for a link
// out of the tree, another directory, or a pipe) fails the copy; nothing outside the
// tree is read.
func TestWalkCopyRefusesEntriesSwappedMidWalk(t *testing.T) {
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.txt"), "SECRET\n")
	mustWrite(t, filepath.Join(outside, "dir", "secret.txt"), "SECRET\n")
	for _, tc := range []struct {
		name, entry string
		swap        func(t *testing.T, p string)
	}{
		{"directory for a link out", "sub", func(t *testing.T, p string) {
			mustRename(t, p, p+".old")
			mustSymlink(t, filepath.Join(outside, "dir"), p)
		}},
		{"directory for another directory", "sub", func(t *testing.T, p string) {
			mustRename(t, p, p+".old")
			other := filepath.Join(filepath.Dir(p), "other")
			mustWrite(t, filepath.Join(other, "secret.txt"), "SECRET\n")
			mustRename(t, other, p)
		}},
		{"file for a link out", "sub/b.txt", func(t *testing.T, p string) {
			mustRename(t, p, p+".old")
			mustSymlink(t, filepath.Join(outside, "secret.txt"), p)
		}},
		{"file for a pipe", "sub/b.txt", func(t *testing.T, p string) {
			mustRename(t, p, p+".old")
			if err := unix.Mkfifo(p, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := t.TempDir()
			mustWrite(t, filepath.Join(src, "a.txt"), "a\n")
			mustWrite(t, filepath.Join(src, "sub", "b.txt"), "b\n")
			swapped := false
			copyHook = func(rel string) {
				if rel == tc.entry && !swapped {
					swapped = true
					tc.swap(t, filepath.Join(src, rel))
				}
			}
			defer func() { copyHook = nil }()
			dst := filepath.Join(t.TempDir(), "copy")
			err := walkCopy(src, dst)
			if !swapped {
				t.Fatal("the walk never reached the entry")
			}
			if err == nil {
				t.Fatal("the copy succeeded after an entry was swapped mid-walk")
			}
			_ = filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() {
					if b, _ := os.ReadFile(p); strings.Contains(string(b), "SECRET") {
						t.Fatalf("copied %s from outside the tree", p)
					}
				}
				return nil
			})
		})
	}
}

func mustRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// TestValidateKeepsLogsOnlyWhenAsked: validate's logs hold hidden oracle output, so
// nothing is left in the temporary directory unless --out names a place for them.
func TestValidateKeepsLogsOnlyWhenAsked(t *testing.T) {
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	res, err := validateOracles(context.Background(), c, []string{"flip"}, ContainNone, "")
	if err != nil || !res[0].Valid || res[0].BaselineOracleLog != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("validate left %v in the temporary directory", left)
	}
	logs := filepath.Join(t.TempDir(), "logs")
	res, err = validateOracles(context.Background(), c, []string{"flip"}, ContainNone, logs)
	if err != nil || !res[0].Valid {
		t.Fatalf("%+v %v", res, err)
	}
	for _, p := range []string{res[0].BaselineOracleLog, res[0].RefOracleLog} {
		if !isWithin(p, logs) {
			t.Fatalf("log %q is not under %s", p, logs)
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
}

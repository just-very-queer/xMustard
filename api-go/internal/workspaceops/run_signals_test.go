package workspaceops

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDetectPatchIssuesSignals(t *testing.T) {
	one, zero := 1, 0
	long := strings.Repeat("e", 250)
	blank := "   "
	cases := []struct {
		name   string
		run    runRecord
		output string
		want   []string
	}{
		{"clean", runRecord{ExitCode: &zero}, "patched 3 files", []string{}},
		{"empty output", runRecord{}, " \n\t", []string{"Empty output - no changes generated"}},
		{"exit and error", runRecord{ExitCode: &one, Error: &long}, "done", []string{
			"Run exited with non-zero code: 1", "Run had error: " + long[:200]}},
		{"blank error", runRecord{Error: &blank}, "done", []string{}},
		{"every marker", runRecord{}, "Traceback ...\nValueError exception\nPANIC: x\nSegmentation fault (core dumped)", []string{
			"Python traceback found in output", "Uncaught exception detected in output", "Panic detected in output", "Segmentation fault detected"}},
		{"caught exception", runRecord{}, "Exception was caught and logged", []string{}},
		{"segfault spelling", runRecord{}, "process segfault at 0x0", []string{"Segmentation fault detected"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectPatchIssues(&c.run, c.output); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// readWorktreeStatus against a real repository: branch, head, ahead/behind of its
// upstream, staged, modified and untracked files, and the 20-path cap.
func TestReadWorktreeStatusFromGit(t *testing.T) {
	origin := initGitRepo(t)
	runGit(t, origin, "branch", "-M", "main")
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, origin, "clone", "-q", origin, clone)
	runGit(t, clone, "config", "user.email", "t@example.com")
	runGit(t, clone, "config", "user.name", "t")
	writeFile(t, filepath.Join(clone, "local.go"), "package p\n")
	runGit(t, clone, "add", "local.go")
	runGit(t, clone, "commit", "-q", "-m", "local")
	writeFile(t, filepath.Join(origin, "remote.go"), "package p\n")
	runGit(t, origin, "add", "remote.go")
	runGit(t, origin, "commit", "-q", "-m", "remote")
	runGit(t, clone, "fetch", "-q")

	writeFile(t, filepath.Join(clone, "staged.go"), "package p\n")
	runGit(t, clone, "add", "staged.go")
	writeFile(t, filepath.Join(clone, "top.go"), "package p // changed\n")
	for i := 0; i < 25; i++ {
		writeFile(t, filepath.Join(clone, "new", "f"+strings.Repeat("x", i)+".txt"), "x\n")
	}

	st := readWorktreeStatus(clone)
	if !st.Available || !st.IsGitRepo || st.Branch == nil || *st.Branch != "main" || st.HeadSHA == nil || len(*st.HeadSHA) != 40 {
		t.Fatalf("repository fields: %+v", st)
	}
	if st.Ahead != 1 || st.Behind != 1 {
		t.Fatalf("ahead/behind: %d/%d", st.Ahead, st.Behind)
	}
	// untracked directories are reported whole by default ("new/")
	if st.StagedFiles != 1 || st.UntrackedFiles != 1 || st.DirtyFiles != 3 {
		t.Fatalf("counts: staged %d untracked %d dirty %d", st.StagedFiles, st.UntrackedFiles, st.DirtyFiles)
	}
	if !reflect.DeepEqual(st.DirtyPaths, []string{"staged.go", "top.go", "new/"}) {
		t.Fatalf("dirty paths: %q", st.DirtyPaths)
	}

	fresh := t.TempDir()
	runGit(t, fresh, "init", "-q")
	for i := 0; i < 23; i++ {
		writeFile(t, filepath.Join(fresh, "u"+strings.Repeat("y", i)), "y\n")
	}
	st = readWorktreeStatus(fresh)
	if !st.IsGitRepo || st.HeadSHA != nil || st.UntrackedFiles != 23 || st.DirtyFiles != 23 || len(st.DirtyPaths) != 20 {
		t.Fatalf("unborn branch: %+v", st)
	}

	notRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(notRepo, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	st = readWorktreeStatus(notRepo)
	if !st.Available || st.IsGitRepo || len(st.DirtyPaths) != 0 {
		t.Fatalf("not a repository: %+v", st)
	}
}

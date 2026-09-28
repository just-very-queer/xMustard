package workspaceops

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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

// Every porcelain v2 record type, including renamed/copied and unmerged entries that
// are awkward to produce with git in a test.
func TestPorcelainV2Records(t *testing.T) {
	lines := []string{
		"# branch.oid 1234567890abcdef1234567890abcdef12345678",
		"# branch.head topic",
		"# branch.upstream origin/topic",
		"# branch.ab +3 -2",
		"# branch.head",
		"1 .M N... 100644 100644 100644 aaa bbb src/changed.go",
		"1 A. N... 000000 100644 100644 000 ccc src/added.go",
		"2 R. N... 100644 100644 100644 ddd eee R100 src/new name.go\tsrc/old.go",
		"u UU N... 100644 100644 100644 100644 f1 f2 f3 src/conflict.go",
		"1 short",
		"? notes/todo.md",
		"! build/ignored.o",
		"?",
		"",
	}
	st := &WorktreeStatus{DirtyPaths: []string{}}
	for _, l := range lines {
		st.addPorcelainV2Line(l)
	}
	if st.Branch == nil || *st.Branch != "topic" || st.HeadSHA == nil || *st.HeadSHA != "1234567890abcdef1234567890abcdef12345678" {
		t.Fatalf("branch headers: %+v", st)
	}
	if st.Ahead != 3 || st.Behind != 2 || st.DirtyFiles != 5 || st.StagedFiles != 3 || st.UntrackedFiles != 1 {
		t.Fatalf("counts: %+v", st)
	}
	want := []string{"src/changed.go", "src/added.go", "src/old.go", "src/conflict.go", "notes/todo.md"}
	if !reflect.DeepEqual(st.DirtyPaths, want) {
		t.Fatalf("dirty paths %q, want %q", st.DirtyPaths, want)
	}
	unborn := &WorktreeStatus{}
	unborn.addPorcelainV2Line("# branch.oid (initial)")
	if unborn.HeadSHA != nil {
		t.Fatalf("unborn head: %v", *unborn.HeadSHA)
	}
}

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

// A real rename and a real path with a space, through readWorktreeStatus. The unit
// tests pin the record fold; this one pins that `-z` is on the command, since without
// it git prints a TAB-joined pair the fold cannot read and neither the rename's new
// path nor the spaced path can survive.
func TestReadWorktreeStatusReportsARenameAndASpacedPath(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, filepath.Join(repo, "old name.go"), "package p\n")
	writeFile(t, filepath.Join(repo, "sp ace.txt"), "x\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "init")
	runGit(t, repo, "mv", "old name.go", "new.go")
	writeFile(t, filepath.Join(repo, "sp ace.txt"), "y\n")

	st := readWorktreeStatus(repo)
	if st.DirtyFiles != 2 || st.StagedFiles != 1 || st.UntrackedFiles != 0 {
		t.Fatalf("counts: staged %d dirty %d untracked %d", st.StagedFiles, st.DirtyFiles, st.UntrackedFiles)
	}
	// git orders worktree entries as the rename first; assert membership, not order,
	// so the test does not pin git's ordering.
	seen := map[string]bool{}
	for _, p := range st.DirtyPaths {
		seen[p] = true
	}
	for _, want := range []string{"new.go", "sp ace.txt"} {
		if !seen[want] {
			t.Fatalf("dirty paths %q missing %q", st.DirtyPaths, want)
		}
	}
	if seen["old name.go"] {
		t.Fatalf("dirty paths %q still names the rename origin", st.DirtyPaths)
	}
}

// Every porcelain v2 record type, including renamed/copied and unmerged entries that
// are awkward to produce with git in a test. The records are NUL-separated, the shape
// `git status --porcelain=v2 -z` emits.
func TestPorcelainV2Records(t *testing.T) {
	records := []string{
		"# branch.oid 1234567890abcdef1234567890abcdef12345678",
		"# branch.head topic",
		"# branch.upstream origin/topic",
		"# branch.ab +3 -2",
		"# branch.head",
		"1 .M N... 100644 100644 100644 aaa bbb src/changed.go",
		"1 A. N... 000000 100644 100644 000 ccc src/added.go",
		"2 R. N... 100644 100644 100644 ddd eee R100 src/new name.go",
		"src/old.go",
		"u UU N... 100644 100644 100644 100644 f1 f2 f3 src/conflict.go",
		"1 short",
		"? notes/todo.md",
		"! build/ignored.o",
		"?",
		"",
	}
	st := &WorktreeStatus{DirtyPaths: []string{}}
	st.addPorcelainV2Records(records)
	if st.Branch == nil || *st.Branch != "topic" || st.HeadSHA == nil || *st.HeadSHA != "1234567890abcdef1234567890abcdef12345678" {
		t.Fatalf("branch headers: %+v", st)
	}
	if st.Ahead != 3 || st.Behind != 2 || st.DirtyFiles != 5 || st.StagedFiles != 3 || st.UntrackedFiles != 1 {
		t.Fatalf("counts: %+v", st)
	}
	// The rename record's new path, not the origin that follows it.
	want := []string{"src/changed.go", "src/added.go", "src/new name.go", "src/conflict.go", "notes/todo.md"}
	if !reflect.DeepEqual(st.DirtyPaths, want) {
		t.Fatalf("dirty paths %q, want %q", st.DirtyPaths, want)
	}
	unborn := &WorktreeStatus{}
	unborn.addPorcelainV2Records([]string{"# branch.oid (initial)"})
	if unborn.HeadSHA != nil {
		t.Fatalf("unborn head: %v", *unborn.HeadSHA)
	}
}

// A rename or copy record carries its origin in the next record; the path the file
// occupies now is the one reported. A parser that reports the origin names a path
// that is no longer in the worktree, and `project_targets.go` keys its target set
// and `run_review_reads.go` its changed-file list off these.
func TestPorcelainV2RenameReportsTheNewPath(t *testing.T) {
	st := &WorktreeStatus{}
	st.addPorcelainV2Records([]string{"2 R. N... 100644 100644 100644 aaa bbb R100 src/renamed.go", "src/before.go"})
	want := []string{"src/renamed.go"}
	if !reflect.DeepEqual(st.DirtyPaths, want) {
		t.Fatalf("dirty paths %q, want %q", st.DirtyPaths, want)
	}
	if st.DirtyFiles != 1 {
		t.Fatalf("dirty files %d, want 1", st.DirtyFiles)
	}
}

// A rename whose origin contains a space. The origin is a whole record like any other,
// so a fold that reads it as an untracked path counts the file twice and reports a path
// that is gone; this is the case that pins the origin record being consumed.
func TestPorcelainV2RenameConsumesASpacedOrigin(t *testing.T) {
	st := &WorktreeStatus{}
	st.addPorcelainV2Records([]string{"2 R. N... 100644 100644 100644 aaa bbb R100 new.go", "old name.go"})
	want := []string{"new.go"}
	if !reflect.DeepEqual(st.DirtyPaths, want) {
		t.Fatalf("dirty paths %q, want %q", st.DirtyPaths, want)
	}
	if st.DirtyFiles != 1 {
		t.Fatalf("dirty files %d, want 1", st.DirtyFiles)
	}
	if st.UntrackedFiles != 0 {
		t.Fatalf("untracked files %d, want 0: the origin was read as a record", st.UntrackedFiles)
	}
}

// A rename whose origin begins with a record-type keyword and carries the nine
// space-separated tokens a type "2" record needs to pass the arity guard. The fold
// cannot tell an origin record from a status record by shape alone, so only the rename
// record consuming it keeps a path from being counted twice. Confirmed against git:
// renaming a directory named `2 3 4 5 6 7 8 9 10 x` to `renamed` emits exactly this
// pair.
func TestPorcelainV2RenameConsumesAnOriginThatLooksLikeARecord(t *testing.T) {
	st := &WorktreeStatus{}
	// Renaming a directory named `2 3 4 5 6 7 8 9 10 x` to `renamed` emits this
	// origin. It opens with the "2" record keyword and, after that keyword, carries the
	// nine space-separated tokens a type "2" record needs to pass the arity guard, so it
	// is indistinguishable from a second rename by shape alone.
	st.addPorcelainV2Records([]string{
		"2 R. N... 100644 100644 100644 aaa bbb R100 renamed/deep.go",
		"2 3 4 5 6 7 8 9 10 x/deep.go",
	})
	want := []string{"renamed/deep.go"}
	if !reflect.DeepEqual(st.DirtyPaths, want) {
		t.Fatalf("dirty paths %q, want %q", st.DirtyPaths, want)
	}
	if st.DirtyFiles != 1 {
		t.Fatalf("dirty files %d, want 1: the origin was read as a rename record", st.DirtyFiles)
	}
}

// The v2 text form does not quote paths, so a path with a space cannot be recovered
// from it by splitting on whitespace. This is also true end to end: git prints
// `? with space.txt` verbatim under --porcelain=v2.
func TestPorcelainV2PathWithASpaceStaysOnePath(t *testing.T) {
	st := &WorktreeStatus{}
	st.addPorcelainV2Records([]string{"1 .M N... 100644 100644 100644 aaa bbb notes/my file.md"})
	want := []string{"notes/my file.md"}
	if !reflect.DeepEqual(st.DirtyPaths, want) {
		t.Fatalf("dirty paths %q, want %q", st.DirtyPaths, want)
	}
	if st.DirtyFiles != 1 {
		t.Fatalf("dirty files %d, want 1", st.DirtyFiles)
	}
}

// A TAB is the rename separator in the text form, so no split on it can be correct.
// This record cannot be written at all without -z, which makes it the strongest guard
// against a revert to whitespace splitting.
func TestPorcelainV2PathWithATabStaysOnePath(t *testing.T) {
	st := &WorktreeStatus{}
	st.addPorcelainV2Records([]string{"? notes/we\tird.md"})
	want := []string{"notes/we\tird.md"}
	if !reflect.DeepEqual(st.DirtyPaths, want) {
		t.Fatalf("dirty paths %q, want %q", st.DirtyPaths, want)
	}
	if st.UntrackedFiles != 1 {
		t.Fatalf("untracked files %d, want 1", st.UntrackedFiles)
	}
}

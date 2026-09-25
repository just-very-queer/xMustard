package workspaceops

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRegisterRoots(t *testing.T) {
	sep := string(filepath.ListSeparator)
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	roots, err := ParseRegisterRoots(" " + a + sep + sep + b + string(filepath.Separator) + sep + a)
	if err != nil || len(roots) != 2 || roots[0] != a || roots[1] != b {
		t.Fatalf("want [%s %s] (trimmed, cleaned, deduplicated), got %v %v", a, b, roots, err)
	}
	if roots, err := ParseRegisterRoots(""); err != nil || roots != nil {
		t.Fatalf("unset means no roots: %v %v", roots, err)
	}
	for _, bad := range []string{"relative/dir", "~/code", string(filepath.Separator), a + sep + string(filepath.Separator)} {
		if _, err := ParseRegisterRoots(bad); err == nil || !strings.Contains(err.Error(), "XMUSTARD_REGISTER_ROOTS") {
			t.Errorf("%q must be refused at startup, got %v", bad, err)
		}
	}
}

// fakeRepo makes a directory with the layout of a git work tree's top level.
func fakeRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func refusalCode(err error) string {
	var r *RegistrationRefusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

// A non-admin registration is admitted only for the top level of a git work tree
// that resolves, symlinks evaluated, at or below a registration root.
func TestCheckRegistrationRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(base, "code")
	outside := filepath.Join(base, "private")
	repo := fakeRepo(t, filepath.Join(allowed, "repo"))
	secret := fakeRepo(t, filepath.Join(outside, "secret"))
	sibling := fakeRepo(t, filepath.Join(base, "code-evil", "repo")) // shares the root's prefix
	plain := filepath.Join(allowed, "notes")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(allowed, "mirror.git")
	if err := os.MkdirAll(filepath.Join(bare, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyGit := filepath.Join(allowed, "hollow")
	if err := os.MkdirAll(filepath.Join(emptyGit, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	junkGitFile := filepath.Join(allowed, "junk")
	if err := os.MkdirAll(junkGitFile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(junkGitFile, ".git"), []byte("not a pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(allowed, "linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "linked")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(allowed, "README")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := func(name, target string) string {
		p := filepath.Join(allowed, name)
		if err := os.Symlink(target, p); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		return p
	}
	escape := link("escape", secret)
	inside := link("alias", repo)
	dotgit := link("dotgit", filepath.Join(repo, ".git"))
	linkedRoot := filepath.Join(base, "code-link")
	if err := os.Symlink(allowed, linkedRoot); err != nil {
		t.Fatal(err)
	}
	roots := []string{allowed}

	admitted := []struct {
		name, path string
		roots      []string
		want       string
	}{
		{"repo top level", repo, roots, repo},
		{"trailing separator", repo + string(filepath.Separator), roots, repo},
		{"linked worktree (.git file)", linked, roots, linked},
		{"symlink resolving inside the root registers its target", inside, roots, repo},
		{"root configured through a symlink", filepath.Join(linkedRoot, "repo"), []string{linkedRoot}, repo},
		{"repo that is the root itself", repo, []string{repo}, repo},
	}
	for _, c := range admitted {
		got, via, err := CheckRegistrationRoot(c.path, c.roots)
		if err != nil || got != c.want || via == "" {
			t.Errorf("%s: want %s admitted, got %q via %q: %v", c.name, c.want, got, via, err)
		}
	}

	refused := []struct {
		name, path string
		roots      []string
		code       string
	}{
		{"no roots configured", repo, nil, RefusalNoRegisterRoots},
		{"relative path", "code/repo", roots, RefusalInvalidPath},
		{"empty path", "  ", roots, RefusalInvalidPath},
		{"outside every root", secret, roots, RefusalOutsideRoots},
		{"sibling sharing the root's prefix", sibling, roots, RefusalOutsideRoots},
		{"dot-dot climbing out", allowed + string(filepath.Separator) + filepath.Join("..", "private", "secret"), roots, RefusalOutsideRoots},
		{"symlink escaping the root", escape, roots, RefusalOutsideRoots},
		{"missing path outside the roots is not probed", filepath.Join(outside, "nope"), roots, RefusalOutsideRoots},
		{"missing path inside a root", filepath.Join(allowed, "nope"), roots, RefusalUnresolvable},
		{"plain directory", plain, roots, RefusalNotGitWorkTree},
		{"regular file", file, roots, RefusalNotGitWorkTree},
		{"subdirectory of a work tree", filepath.Join(repo, "src", "pkg"), roots, RefusalNotTopLevel},
		{".git directory", filepath.Join(repo, ".git"), roots, RefusalGitInternals},
		{"inside .git", filepath.Join(repo, ".git", "objects"), roots, RefusalGitInternals},
		{".git in another case", filepath.Join(repo, ".GIT"), roots, RefusalGitInternals},
		{"symlink to a .git directory", dotgit, roots, RefusalGitInternals},
		{"bare repository", bare, roots, RefusalBareRepository},
		{".git without HEAD or objects", emptyGit, roots, RefusalNotGitWorkTree},
		{".git file that names no gitdir", junkGitFile, roots, RefusalNotGitWorkTree},
	}
	for _, c := range refused {
		got, _, err := CheckRegistrationRoot(c.path, c.roots)
		if err == nil || refusalCode(err) != c.code || got != "" {
			t.Errorf("%s: want refusal %s, got %q %v (%s)", c.name, c.code, got, err, refusalCode(err))
		}
	}
	// an outside path is answered the same whether or not it exists or is a repo
	_, _, e1 := CheckRegistrationRoot(secret, roots)
	_, _, e2 := CheckRegistrationRoot(filepath.Join(outside, "nope"), roots)
	if strings.ReplaceAll(e1.Error(), secret, "P") != strings.ReplaceAll(e2.Error(), filepath.Join(outside, "nope"), "P") {
		t.Errorf("outside paths must not be distinguishable: %q vs %q", e1, e2)
	}
	// a refusal names what to do
	if _, _, err := CheckRegistrationRoot(filepath.Join(repo, "src"), roots); err == nil || !strings.Contains(err.Error(), repo) {
		t.Errorf("a subdirectory refusal names the top level to register: %v", err)
	}
}

// Real git layouts: a work tree, a linked worktree and a bare repository.
func TestCheckRegistrationRootRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	repo := filepath.Join(base, "repo")
	git("init", "-q", repo)
	git("-C", repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(base, "wt")
	git("-C", repo, "worktree", "add", "-q", wt)
	bare := filepath.Join(base, "bare.git")
	git("init", "-q", "--bare", bare)
	roots := []string{base}
	for _, p := range []string{repo, wt} {
		if got, _, err := CheckRegistrationRoot(p, roots); err != nil || got != p {
			t.Errorf("%s: want admitted, got %q %v", p, got, err)
		}
	}
	if _, _, err := CheckRegistrationRoot(bare, roots); refusalCode(err) != RefusalBareRepository {
		t.Errorf("bare repository: want %s, got %v", RefusalBareRepository, err)
	}
	if _, _, err := CheckRegistrationRoot(filepath.Join(repo, ".git", "worktrees"), roots); refusalCode(err) != RefusalGitInternals {
		t.Errorf("git internals: want %s, got %v", RefusalGitInternals, err)
	}
}

func TestLookupWorkspaceRoot(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	before, err := LookupWorkspaceRoot(dataDir, root)
	if err != nil || before.Registered || before.ID != workspaceIDForPath(root) {
		t.Fatalf("unregistered root: %+v %v", before, err)
	}
	if _, err := upsertLoadedWorkspace(dataDir, root, "Pretty", nowUTC()); err != nil {
		t.Fatal(err)
	}
	after, err := LookupWorkspaceRoot(dataDir, root+string(filepath.Separator))
	if err != nil || !after.Registered || after.ID != before.ID || after.Name != "Pretty" {
		t.Fatalf("registered root: %+v %v", after, err)
	}
	if id, err := WorkspaceIDForRoot(dataDir, root); err != nil || id != before.ID {
		t.Fatalf("WorkspaceIDForRoot: %s %v", id, err)
	}
}

package workspaceops

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// gitFile makes dir a work tree whose .git file names gitDir.
func gitFile(t *testing.T, dir, gitDir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// linkedGitDir makes the git directory of a linked worktree named name in repo's
// .git/worktrees, as git worktree add does: HEAD, and commondir naming ../.. .
func linkedGitDir(t *testing.T, repo, name string) string {
	t.Helper()
	dir := filepath.Join(repo, ".git", "worktrees", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, body := range map[string]string{"HEAD": "ref: refs/heads/wt\n", "commondir": "../..\n"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
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
	gitFile(t, linked, linkedGitDir(t, repo, "linked"))
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
		{"missing path inside a root", filepath.Join(allowed, "nope"), roots, RefusalOutsideRoots},
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
	if _, err := upsertLoadedWorkspace(dataDir, root, "Pretty", nowUTC(), nil); err != nil {
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

// A path that leads outside every root through a symlink planted inside one gets
// the same answer whether its target exists or not, and the answer never names
// where the path leads: the check is not a probe of the host filesystem.
func TestCheckRegistrationRootDoesNotProbeOutside(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(base, "code")
	repo := fakeRepo(t, filepath.Join(allowed, "repo"))
	fakeRepo(t, filepath.Join(base, "private", "secret"))
	link := func(name, target string) string {
		p := filepath.Join(repo, name)
		if err := os.Symlink(target, p); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		return p
	}
	hostfs := link("hostfs", base)
	missing := filepath.Join(base, "private", "missing")
	probes := []string{
		filepath.Join(hostfs, "private", "secret"),        // exists, a repository
		filepath.Join(hostfs, "private", "secret", "src"), // exists, a subdirectory
		filepath.Join(hostfs, "private", "missing"),       // does not exist
		filepath.Join(hostfs, "private", "missing", "deeper"),
		link("out", filepath.Join(base, "private", "secret")),
		link("dangling", missing),
		link("dangling-deep", filepath.Join(missing, "x", "y")),
		filepath.Join(allowed, "nope"), // missing inside the root: the same answer
	}
	var first string
	for _, p := range probes {
		_, _, err := CheckRegistrationRoot(p, []string{allowed})
		if refusalCode(err) != RefusalOutsideRoots {
			t.Fatalf("%s: want %s, got %v", p, RefusalOutsideRoots, err)
		}
		msg := strings.ReplaceAll(err.Error(), p, "P")
		if strings.Contains(msg, filepath.Join(base, "private")) {
			t.Errorf("%s: the refusal names where the path leads: %q", p, err)
		}
		if first == "" {
			first = msg
		} else if msg != first {
			t.Errorf("%s: answers differ, so they probe the host: %q vs %q", p, msg, first)
		}
	}
}

// The work tree's .git must lead to a git repository inside the same registration
// root. A .git file or symlink pointing at a repository elsewhere is refused (git
// would read that repository's index, refs and history into ground and impact), and
// so is a git directory whose commondir leads elsewhere.
func TestCheckRegistrationRootGitDirMustStayInside(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(base, "code")
	repo := fakeRepo(t, filepath.Join(allowed, "repo"))
	outside := fakeRepo(t, filepath.Join(base, "private", "secret"))
	outsideGit := filepath.Join(outside, ".git")
	roots := []string{allowed}

	module := filepath.Join(repo, ".git", "modules", "sub") // a submodule's git directory
	if err := os.MkdirAll(filepath.Join(module, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	admitted := []string{
		gitFile(t, filepath.Join(allowed, "linked"), linkedGitDir(t, repo, "linked")),
		gitFile(t, filepath.Join(allowed, "relative"), filepath.Join("..", "repo", ".git", "worktrees", "linked")),
		gitFile(t, filepath.Join(allowed, "submodule"), module),
	}
	for _, p := range admitted {
		if got, _, err := CheckRegistrationRoot(p, roots); err != nil || got != p {
			t.Errorf("%s: want admitted, got %q %v", p, got, err)
		}
	}

	commonOut := linkedGitDir(t, repo, "common-out")
	if err := os.WriteFile(filepath.Join(commonOut, "commondir"), []byte(outsideGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirCommonOut := fakeRepo(t, filepath.Join(allowed, "dir-common-out"))
	if err := os.WriteFile(filepath.Join(dirCommonOut, ".git", "commondir"), []byte(outsideGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkOut := filepath.Join(allowed, "link-out")
	if err := os.MkdirAll(linkOut, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideGit, filepath.Join(linkOut, ".git")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	refused := []string{
		gitFile(t, filepath.Join(allowed, "file-out"), outsideGit),
		gitFile(t, filepath.Join(allowed, "file-missing"), filepath.Join(base, "private", "missing", ".git")),
		gitFile(t, filepath.Join(allowed, "relative-out"), filepath.Join("..", "..", "private", "secret", ".git")),
		gitFile(t, filepath.Join(allowed, "nowhere"), "nowhere"),
		gitFile(t, filepath.Join(allowed, "common-out"), commonOut),
		dirCommonOut,
		linkOut,
	}
	var first string
	for _, p := range refused {
		_, _, err := CheckRegistrationRoot(p, roots)
		if refusalCode(err) != RefusalNotGitWorkTree {
			t.Fatalf("%s: want %s, got %v", p, RefusalNotGitWorkTree, err)
		}
		if msg := strings.ReplaceAll(err.Error(), p, "P"); first == "" {
			first = msg
		} else if msg != first {
			t.Errorf("%s: a .git leading elsewhere must get one answer, existing or not: %q vs %q", p, msg, first)
		}
	}
}

// pinnedWorkspace registers root as a non-admin would and writes its snapshot.
func pinnedWorkspace(t *testing.T, dataDir, root, registerRoot string) workspaceRecord {
	t.Helper()
	rec, err := upsertLoadedWorkspace(dataDir, root, filepath.Base(root), nowUTC(), &NonAdminRegistration{RegisterRoot: registerRoot, Principal: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", rec.WorkspaceID, "snapshot.json"), workspaceSnapshot{ScannerVersion: scannerVersion, Workspace: workspaceRecord{WorkspaceID: rec.WorkspaceID, Name: rec.Name, RootPath: root}}); err != nil {
		t.Fatal(err)
	}
	return rec
}

// A root a non-admin registered is re-checked on every use: once the directory is
// swapped for a symlink, every way to its root refuses it and no read follows the
// link. Restoring the directory restores the workspace. A root an admin registered
// is used as registered.
func TestRegisteredRootIsRecheckedOnUse(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(base, "code")
	repo := fakeRepo(t, filepath.Join(allowed, "repo"))
	nested := fakeRepo(t, filepath.Join(repo, "vendor", "x"))
	private := filepath.Join(base, "private")
	if err := os.MkdirAll(private, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "secret.txt"), []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "secret.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	rec := pinnedWorkspace(t, dataDir, nested, allowed)
	if rec.RegisterRoot != allowed || rec.RegisteredBy != "ada" {
		t.Fatalf("the record must keep who registered it and under which root: %+v", rec)
	}
	uses := func() []error {
		_, e1 := getWorkspaceRecord(dataDir, rec.WorkspaceID)
		_, e2 := loadSnapshot(dataDir, rec.WorkspaceID)
		_, e3 := confineAnchorPaths(dataDir, rec.WorkspaceID, []string{"secret.txt"})
		// tool paths resolve through the registry, not loadSnapshot
		_, e4 := resolveWorkspace(dataDir, rec.WorkspaceID)
		e5 := requireWorkspaceSnapshot(dataDir, rec.WorkspaceID)
		return []error{e1, e2, e3, e4, e5}
	}
	for i, err := range uses() {
		if err != nil {
			t.Fatalf("use %d before the swap: %v", i, err)
		}
	}
	if root := contextRoot(dataDir, rec.WorkspaceID); root != nested {
		t.Fatalf("context root before the swap: %q", root)
	}

	if err := os.RemoveAll(nested); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "..", "private"), nested); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if data, ok := readWorkspaceRegularFile(nested, "secret.txt"); !ok || string(data) != "TOP SECRET" {
		t.Fatalf("fixture: the swapped root must lead to the secret (%q %v)", data, ok)
	}
	_, scanErr := ScanWorkspace(dataDir, rec.WorkspaceID)
	for i, err := range append(uses(), scanErr) {
		if de, ok := AsDomainError(err); !ok || de.Class != ClassConflict || !strings.Contains(err.Error(), "no longer resolves") {
			t.Errorf("use %d after the swap: want the registered-root conflict, got %v", i, err)
		}
	}
	if root := contextRoot(dataDir, rec.WorkspaceID); root != "" {
		t.Errorf("a swapped root must not be handed to a reader: %q", root)
	}
	if st := readWorktreeStatusFromRunOrRepo(nil, dataDir, rec.WorkspaceID); st.Available {
		t.Errorf("a swapped root must not be read for worktree status: %+v", st)
	}
	if _, err := LoadWorkspace(dataDir, WorkspaceLoadRequest{RootPath: nested, PreferCachedSnapshot: true}); err == nil {
		t.Errorf("loading a swapped root must fail")
	}

	// restored
	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}
	fakeRepo(t, nested)
	for i, err := range uses() {
		if err != nil {
			t.Fatalf("use %d once restored: %v", i, err)
		}
	}
	// an ancestor swapped for a symlink to a copy is also a different directory
	vendor := filepath.Join(repo, "vendor")
	copyDir := filepath.Join(repo, "vendor-copy")
	if err := os.Rename(vendor, copyDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("vendor-copy", vendor); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSnapshot(dataDir, rec.WorkspaceID); err == nil {
		t.Errorf("an ancestor swapped for a symlink must fail the check")
	}

	// an admin-registered root is used as registered, links and all
	link := filepath.Join(allowed, "alias")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	admin, err := upsertLoadedWorkspace(dataDir, link, "alias", nowUTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if admin.RegisterRoot != "" {
		t.Fatalf("an admin registration is not pinned: %+v", admin)
	}
	if _, err := getWorkspaceRecord(dataDir, admin.WorkspaceID); err != nil {
		t.Fatalf("admin root through a link: %v", err)
	}
}

// The registration root and principal stay with the record: a later load, admin or
// not, neither clears nor moves them.
func TestRegistrationProvenanceIsSticky(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(base, "code")
	repo := fakeRepo(t, filepath.Join(allowed, "repo"))
	dataDir := t.TempDir()
	rec := pinnedWorkspace(t, dataDir, repo, allowed)
	again, err := upsertLoadedWorkspace(dataDir, repo, "renamed", nowUTC(), nil)
	if err != nil || again.WorkspaceID != rec.WorkspaceID || again.RegisterRoot != allowed || again.RegisteredBy != "ada" {
		t.Fatalf("an admin reload must keep the provenance: %+v %v", again, err)
	}
	other, err := upsertLoadedWorkspace(dataDir, repo, "renamed", nowUTC(), &NonAdminRegistration{RegisterRoot: base, Principal: "bob"})
	if err != nil || other.RegisterRoot != allowed || other.RegisteredBy != "ada" {
		t.Fatalf("another principal's reload must keep the provenance: %+v %v", other, err)
	}
}

// The per-principal limit is counted under the registry lock, so concurrent
// registrations by one principal never exceed it.
func TestRegisterLimitHoldsUnderConcurrency(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	const limit, tries = 3, 12
	errs := make(chan error, tries)
	for i := 0; i < tries; i++ {
		go func(i int) {
			root := filepath.Join(base, "code", "r"+strconv.Itoa(i))
			_, err := upsertLoadedWorkspace(dataDir, root, "r", nowUTC(), &NonAdminRegistration{RegisterRoot: base, Principal: "ada", Limit: limit})
			errs <- err
		}(i)
	}
	refused := 0
	for i := 0; i < tries; i++ {
		if err := <-errs; refusalCode(err) == RefusalRegisterLimit {
			refused++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	items, _ := ListWorkspaces(dataDir)
	if len(items) != limit || refused != tries-limit {
		t.Fatalf("want %d registered and %d refused, got %d and %d", limit, tries-limit, len(items), refused)
	}
}

func TestParseRegisterLimit(t *testing.T) {
	if n, err := ParseRegisterLimit(""); err != nil || n != DefaultRegisterLimit {
		t.Fatalf("unset: %d %v", n, err)
	}
	if n, err := ParseRegisterLimit(" 7 "); err != nil || n != 7 {
		t.Fatalf("7: %d %v", n, err)
	}
	for _, bad := range []string{"0", "-1", "many", "1.5"} {
		if _, err := ParseRegisterLimit(bad); err == nil || !strings.Contains(err.Error(), "XMUSTARD_REGISTER_LIMIT") {
			t.Errorf("%q must be refused: %v", bad, err)
		}
	}
}

// LookupWorkspaceDir finds a registered directory under another spelling or through
// a link; a non-admin root swapped for a symlink does not match through the link.
func TestLookupWorkspaceDir(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	repo := fakeRepo(t, filepath.Join(base, "code", "repo"))
	link := filepath.Join(base, "codelink")
	if err := os.Symlink(filepath.Join(base, "code"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	admin, err := upsertLoadedWorkspace(dataDir, filepath.Join(link, "repo"), "Pretty", nowUTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := LookupWorkspaceRoot(dataDir, repo); err != nil || got.Registered {
		t.Fatalf("the lexical lookup matches spelling only: %+v %v", got, err)
	}
	got, err := LookupWorkspaceDir(dataDir, repo)
	if err != nil || !got.Registered || got.ID != admin.WorkspaceID || got.RootPath != admin.RootPath || got.Name != "Pretty" {
		t.Fatalf("the same directory through a link: %+v %v", got, err)
	}

	other := fakeRepo(t, filepath.Join(base, "code", "other"))
	swapped := fakeRepo(t, filepath.Join(base, "code", "swapped"))
	pinnedWorkspace(t, dataDir, swapped, filepath.Join(base, "code"))
	if err := os.RemoveAll(swapped); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, swapped); err != nil {
		t.Fatal(err)
	}
	if got, err := LookupWorkspaceDir(dataDir, other); err != nil || got.Registered {
		t.Fatalf("a pinned root swapped for a link to %s must not claim it: %+v %v", other, got, err)
	}
}

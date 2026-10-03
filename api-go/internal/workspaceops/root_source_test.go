package workspaceops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// divergentRoots writes a data dir whose snapshot says the workspace's root is
// snapRoot and whose workspaces.json says it is regRoot, then returns the data dir
// and the workspace id. Both roots exist, so which one a reader follows is
// observable rather than inferred.
func divergentRoots(t *testing.T) (dataDir, workspaceID, snapRoot, regRoot string) {
	t.Helper()
	workspaceID = "wsDivergent"
	snapRoot, regRoot = t.TempDir(), t.TempDir()
	dataDir = t.TempDir()
	agedWrite(t, snapshotPath(dataDir, workspaceID), snapshotWithRoot(workspaceID, snapRoot), time.Hour)
	agedWrite(t, workspacesPath(dataDir), []workspaceRecord{{
		WorkspaceID: workspaceID, Name: workspaceID, RootPath: regRoot,
	}}, time.Hour)
	return dataDir, workspaceID, snapRoot, regRoot
}

// Anchor confinement must confine against the root the tool paths read, or it is
// checking a path against a root nothing opens. With the snapshot's root and
// workspaces.json disagreeing, resolveWorkspace is that root, and a symlink the
// other root would admit is refused by it.
func TestConfineAnchorPathsFollowsTheRootToolPathsUse(t *testing.T) {
	dataDir, workspaceID, snapRoot, regRoot := divergentRoots(t)

	// Under the snapshot's root only, a link to the other root leaves the
	// workspace; under the registry file's root the same relative path does not
	// even exist, so it resolves inside and is admitted.
	if err := os.Symlink(filepath.Join(regRoot, "target.txt"), filepath.Join(snapRoot, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regRoot, "target.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	toolRoot, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if toolRoot != snapRoot {
		t.Fatalf("fixture: the tool path reads %q, not the snapshot root %q", toolRoot, snapRoot)
	}

	if _, err := confineAnchorPaths(dataDir, workspaceID, []string{"inside.txt"}); err != nil {
		t.Fatalf("confinement refused a plain path inside the root tool paths use: %v", err)
	}
	out, err := confineAnchorPaths(dataDir, workspaceID, []string{"link"})
	if !errors.Is(err, ErrPathEscapesWorkspace) {
		t.Fatalf("a symlink leaving the root the tool paths use must be refused, got %v %v", out, err)
	}

	// The other direction: a symlink that leaves the *registry* root sits inside
	// the root the tool paths use, and the tool path follows that link too, so it
	// must be admitted. The old reader consulted workspaces.json first and refused
	// it, which is the other half of the divergence.
	if err := os.Symlink(t.TempDir(), filepath.Join(regRoot, "inside-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if out, err := confineAnchorPaths(dataDir, workspaceID, []string{"inside-link"}); err != nil {
		t.Fatalf("a path inside the root the tool paths use must be admitted, got %v %v", out, err)
	}
}

// A registry the process cannot decode must not open confinement up. With the
// snapshot intact the tool paths still have a root, so confinement confines
// against it and a symlink leaving that root is refused exactly as on the
// divergence path; the unreadable registry cannot fall the reader back to
// lexical-only admission.
func TestConfineAnchorPathsRefusesWhenTheRegistryIsUndecodable(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	agedWrite(t, snapshotPath(dataDir, "wsCorrupt"), snapshotWithRoot("wsCorrupt", root), time.Hour)
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(workspacesPath(dataDir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := confineAnchorPaths(dataDir, "wsCorrupt", []string{"link"}); !errors.Is(err, ErrPathEscapesWorkspace) {
		t.Fatalf("an undecodable registry must not admit a symlink out of the root, got %v %v", out, err)
	}
}

// The non-admin registration re-check stays on the confinement path: a root whose
// .git no longer leads inside the registration root is refused, and the refusal is
// the registration one rather than a generic path error.
func TestConfineAnchorPathsStillRechecksANonAdminRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(base, "code")
	repo := fakeRepo(t, filepath.Join(allowed, "repo"))
	dataDir := t.TempDir()
	rec := pinnedWorkspace(t, dataDir, repo, allowed)
	if _, err := confineAnchorPaths(dataDir, rec.WorkspaceID, []string{"secret.txt"}); err != nil {
		t.Fatalf("before the swap: %v", err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "..", "private"), repo); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	out, err := confineAnchorPaths(dataDir, rec.WorkspaceID, []string{"secret.txt"})
	if err == nil {
		t.Fatalf("a re-pointed non-admin root must be refused, got %v", out)
	}
	if !strings.Contains(err.Error(), "no longer resolves to that git work tree") {
		t.Fatalf("want the registration refusal, got %v", out)
	}
}

// A workspace with no snapshot and no registry record cannot be resolved at all.
// That is not a refusal: confinement falls back to the lexical checks, which still
// refuse an escape, an absolute path and a control character while admitting a
// plain relative path.
func TestConfineAnchorPathsFallsBackToLexicalChecksForAnUnresolvableWorkspace(t *testing.T) {
	dataDir := t.TempDir()

	got, err := confineAnchorPaths(dataDir, "wsUnknown", []string{"inside.txt"})
	if err != nil || len(got) != 1 || got[0] != "inside.txt" {
		t.Fatalf("an unresolvable workspace still confines lexically: %v %v", got, err)
	}
	for _, escape := range []string{"../escape.txt", "/etc/passwd", "with\x00nul"} {
		if out, err := confineAnchorPaths(dataDir, "wsUnknown", []string{escape}); err == nil {
			t.Fatalf("%q must be refused with no root to check against, got %v", escape, out)
		}
	}
}

// No registry file at all: the snapshot root still confines, an escape is still
// refused, and the root lookup still answers the derived unregistered id. The
// snapshot root is in force, not merely present: a symlink out of it is refused,
// which a rootless lexical-only fallback would admit.
func TestReadersWithoutARegistryKeepTheSnapshotRoot(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	agedWrite(t, snapshotPath(dataDir, "wsNoReg"), snapshotWithRoot("wsNoReg", root), time.Hour)
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := confineAnchorPaths(dataDir, "wsNoReg", []string{"inside.txt"})
	if err != nil || got[0] != "inside.txt" {
		t.Fatalf("the snapshot root must still confine a plain path: %v %v", got, err)
	}
	if _, err := confineAnchorPaths(dataDir, "wsNoReg", []string{"../escape.txt"}); !errors.Is(err, ErrPathEscapesWorkspace) {
		t.Fatalf("the escape must still be refused: %v", err)
	}
	if out, err := confineAnchorPaths(dataDir, "wsNoReg", []string{"link"}); !errors.Is(err, ErrPathEscapesWorkspace) {
		t.Fatalf("with no registry file the snapshot root must still be in force: %v %v", out, err)
	}
	if id, err := WorkspaceIDForRoot(dataDir, root); err != nil || id != workspaceIDForPath(root) {
		t.Fatalf("an unregistered root still derives its id: %q %v", id, err)
	}
}

// A workspace the registry file does not list still confines against the snapshot
// root: the registry is the first source, not the only one. The snapshot root is in
// force, not merely present, so a symlink out of it is refused.
func TestConfineAnchorPathsFallsBackToTheSnapshotRootForAnUnlistedWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	agedWrite(t, snapshotPath(dataDir, "wsOnlySnap"), snapshotWithRoot("wsOnlySnap", root), time.Hour)
	agedWrite(t, workspacesPath(dataDir), []workspaceRecord{{
		WorkspaceID: "wsOther", Name: "other", RootPath: t.TempDir(),
	}}, time.Hour)
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if out, err := confineAnchorPaths(dataDir, "wsOnlySnap", []string{"ok.txt"}); err != nil {
		t.Fatalf("a workspace the registry does not list still confines against the snapshot: %v %v", out, err)
	}
	if out, err := confineAnchorPaths(dataDir, "wsOnlySnap", []string{"link"}); !errors.Is(err, ErrPathEscapesWorkspace) {
		t.Fatalf("the snapshot root must be in force, not merely used: got %v %v", out, err)
	}
}

// A workspace registered in workspaces.json but not yet scanned has no snapshot.
// Its registered record is still its root, so a symlink out of it is refused the
// same way it is for a scanned workspace. This is the case resolveWorkspace cannot
// answer, and confinement must not fall to the lexical checks because of it.
func TestConfineAnchorPathsUsesTheRegisteredRootForAnUnscannedWorkspace(t *testing.T) {
	root, dataDir := confineFixture(t), t.TempDir()
	if err := writeJSON(workspacesPath(dataDir), []workspaceRecord{{
		WorkspaceID: "wsUnscanned", Name: "unscanned", RootPath: root,
	}}); err != nil {
		t.Fatal(err)
	}

	if out, err := confineAnchorPaths(dataDir, "wsUnscanned", []string{"src/ok.go"}); err != nil {
		t.Fatalf("an in-repo anchor for an unscanned workspace: %v %v", out, err)
	}
	for _, escape := range []string{"leak.txt", "vendor/secret.txt"} {
		if out, err := confineAnchorPaths(dataDir, "wsUnscanned", []string{escape}); !errors.Is(err, ErrPathEscapesWorkspace) {
			t.Fatalf("%q must be refused against the registered root: %v %v", escape, out, err)
		}
	}
}

// The registration re-check also runs on the unscanned-workspace path, where the
// root comes from workspaces.json rather than a snapshot. A root whose .git leads
// elsewhere after registration is refused there too.
func TestConfineAnchorPathsRechecksAnUnscannedNonAdminRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(base, "code")
	repo := fakeRepo(t, filepath.Join(allowed, "repo"))
	private := filepath.Join(base, "private")
	if err := os.MkdirAll(private, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "secret.txt"), []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	rec, err := upsertLoadedWorkspace(dataDir, repo, "repo", nowUTC(),
		&NonAdminRegistration{RegisterRoot: allowed, Principal: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, repo); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	out, err := confineAnchorPaths(dataDir, rec.WorkspaceID, []string{"secret.txt"})
	if err == nil {
		t.Fatalf("a re-pointed non-admin root must be refused before the first scan, got %v", out)
	}
	if !strings.Contains(err.Error(), "no longer resolves to that git work tree") {
		t.Fatalf("want the registration refusal, got %v", out)
	}
}

// A hand-edited registry can hold two records under one workspace id. The cache's
// id-keyed map keeps one of them, but a root lookup matches on a *root*, so it has
// to see both: before this row read the file it did, and a lookup that dropped one
// would report a registered root as unregistered.
func TestLookupWorkspaceRootSeesEveryRecordUnderADuplicateID(t *testing.T) {
	dataDir := t.TempDir()
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	for _, r := range []string{first, second} {
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSON(workspacesPath(dataDir), []workspaceRecord{
		{WorkspaceID: "dup", Name: "Aaa", RootPath: first},
		{WorkspaceID: "dup", Name: "Bbb", RootPath: second},
	}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		root string
		name string
	}{{first, "Aaa"}, {second, "Bbb"}} {
		got, err := LookupWorkspaceRoot(dataDir, c.root)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Registered || got.ID != "dup" || got.Name != c.name {
			t.Fatalf("root %s: got %+v, want registered id dup name %s", filepath.Base(c.root), got, c.name)
		}
		if id, err := WorkspaceIDForRoot(dataDir, c.root); err != nil || id != "dup" {
			t.Fatalf("WorkspaceIDForRoot(%s): %q %v", filepath.Base(c.root), id, err)
		}
	}
}

// The reader this row moved, reimplemented here over ListWorkspaces, is the oracle
// for what it answered before: the file, read every call, no cache. The cases are
// the shapes the API uses it for — an unregistered root, a registered root, its
// spelling with a trailing separator, and a symlinked root (which matches only
// under LookupWorkspaceDir).
func lookupOverListWorkspaces(t *testing.T, dataDir, rootPath string, sameDir bool) WorkspaceRootLookup {
	t.Helper()
	root, err := filepath.Abs(strings.TrimSpace(rootPath))
	if err != nil {
		t.Fatal(err)
	}
	found := WorkspaceRootLookup{ID: workspaceIDForPath(root), RootPath: root}
	items, err := ListWorkspaces(dataDir)
	if err != nil {
		return found
	}
	registered := func(it workspaceRecord) WorkspaceRootLookup {
		return WorkspaceRootLookup{ID: it.WorkspaceID, Name: it.Name, RootPath: it.RootPath, Registered: true}
	}
	for _, it := range items {
		if filepath.Clean(it.RootPath) == root {
			return registered(it)
		}
	}
	want, err := os.Stat(root)
	if !sameDir || err != nil {
		return found
	}
	for _, it := range items {
		stat := os.Stat
		if it.RegisterRoot != "" {
			stat = os.Lstat
		}
		if info, err := stat(it.RootPath); err == nil && os.SameFile(want, info) {
			return registered(it)
		}
	}
	return found
}

func TestLookupWorkspaceRootAnswersWhatTheFileReaderAnswered(t *testing.T) {
	dataDir := t.TempDir()
	repo := fakeRepo(t, filepath.Join(t.TempDir(), "repo"))
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := upsertLoadedWorkspace(dataDir, repo, "Repo", nowUTC(), nil); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(t.TempDir(), "plain")

	cases := []struct {
		name       string
		root       string
		sameDir    bool
		registered bool
		id         string
	}{
		{"unregistered", plain, false, false, workspaceIDForPath(plain)},
		{"registered", repo, false, true, workspaceIDForPath(repo)},
		{"trailing separator", repo + string(filepath.Separator), false, true, workspaceIDForPath(repo)},
		{"through a symlink", link, true, true, workspaceIDForPath(repo)},
		{"symlink is not the directory", link, false, false, workspaceIDForPath(link)},
	}
	read := func() []WorkspaceRootLookup {
		out := make([]WorkspaceRootLookup, 0, len(cases))
		for _, c := range cases {
			var got WorkspaceRootLookup
			var err error
			if c.sameDir {
				got, err = LookupWorkspaceDir(dataDir, c.root)
			} else {
				got, err = LookupWorkspaceRoot(dataDir, c.root)
			}
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			out = append(out, got)
		}
		return out
	}
	for i, c := range cases {
		// The oracle is the pre-move reader itself, not a second call to the code
		// under test, so this cannot compare a cold read with a warm one.
		want := lookupOverListWorkspaces(t, dataDir, c.root, c.sameDir)
		got := read()[i]
		if got != want {
			t.Fatalf("%s: got %+v, want the file reader's %+v", c.name, got, want)
		}
		if got.Registered != c.registered || got.ID != c.id {
			t.Fatalf("%s: got %+v, want registered=%v id=%s", c.name, got, c.registered, c.id)
		}
	}

	// A registry that changes after the cache was warmed is still observed: the
	// cache is keyed on the file's stat identity, so a reader that skipped that
	// key would keep answering from the stale records. The change is to a record
	// already cached (its name), which is what a stale answer would get wrong.
	if _, err := upsertLoadedWorkspace(dataDir, repo, "Renamed", nowUTC(), nil); err != nil {
		t.Fatal(err)
	}
	renamed, err := LookupWorkspaceRoot(dataDir, repo)
	if err != nil || !renamed.Registered || renamed.Name != "Renamed" {
		t.Fatalf("a record changed after the cache was warmed must be seen: %+v %v", renamed, err)
	}

	// The lookup orders a copy: the cache's own slice is shared by every reader and
	// must keep the order the file is in, or a reader that sorts it in place
	// decides the order every later reader sees. Two records are needed for that to
	// be visible, so "Aaa" is registered after "Renamed" and sorts before it.
	other := fakeRepo(t, filepath.Join(t.TempDir(), "other"))
	if _, err := upsertLoadedWorkspace(dataDir, other, "Aaa", nowUTC(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := LookupWorkspaceRoot(dataDir, other); err != nil {
		t.Fatal(err)
	}
	fromFile, err := ListWorkspaces(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	cached := registry.records[dataDir]
	if cached == nil {
		t.Fatal("the registry cache holds no records after two lookups")
	}
	if len(cached.list) != len(fromFile) {
		t.Fatalf("the cache holds %d records, the file has %d", len(cached.list), len(fromFile))
	}
	for i := range fromFile {
		if cached.list[i].WorkspaceID != fromFile[i].WorkspaceID {
			t.Fatalf("record %d: the cache reordered the file's records (%s vs %s)",
				i, cached.list[i].WorkspaceID, fromFile[i].WorkspaceID)
		}
	}
}

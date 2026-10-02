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

// No registry file at all: the snapshot root still confines, an escape is still
// refused, and the root lookup still answers the derived unregistered id. Neither
// reader may start refusing because the registry is absent.
func TestReadersWithoutARegistryKeepTheSnapshotRoot(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	agedWrite(t, snapshotPath(dataDir, "wsNoReg"), snapshotWithRoot("wsNoReg", root), time.Hour)

	got, err := confineAnchorPaths(dataDir, "wsNoReg", []string{"inside.txt"})
	if err != nil || got[0] != "inside.txt" {
		t.Fatalf("the snapshot root must still confine a plain path: %v %v", got, err)
	}
	if _, err := confineAnchorPaths(dataDir, "wsNoReg", []string{"../escape.txt"}); !errors.Is(err, ErrPathEscapesWorkspace) {
		t.Fatalf("the escape must still be refused: %v", err)
	}
	if id, err := WorkspaceIDForRoot(dataDir, root); err != nil || id != workspaceIDForPath(root) {
		t.Fatalf("an unregistered root still derives its id: %q %v", id, err)
	}
}

// A workspace the registry file does not list still confines against the snapshot
// root: the registry is the first source, not the only one.
func TestConfineAnchorPathsFallsBackToTheSnapshotRootForAnUnlistedWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	agedWrite(t, snapshotPath(dataDir, "wsOnlySnap"), snapshotWithRoot("wsOnlySnap", t.TempDir()), time.Hour)
	agedWrite(t, workspacesPath(dataDir), []workspaceRecord{{
		WorkspaceID: "wsOther", Name: "other", RootPath: t.TempDir(),
	}}, time.Hour)

	if out, err := confineAnchorPaths(dataDir, "wsOnlySnap", []string{"ok.txt"}); err != nil {
		t.Fatalf("a workspace the registry does not list still confines against the snapshot: %v %v", out, err)
	}
}

// Root lookup answers the same before and after the registry cache has been warmed
// by the readers that share it, so moving lookupWorkspace onto the cache cannot
// change what an already-read registry says. The four rows are the shapes the API
// uses it for: an unregistered root, a registered root, its spelling with a
// trailing separator, and a root under a workspace registered through a symlink.
func TestLookupWorkspaceRootIsUnchangedByTheWarmedRegistryCache(t *testing.T) {
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
	before := read()
	for i, c := range cases {
		if before[i].Registered != c.registered || before[i].ID != c.id {
			t.Fatalf("%s: got %+v, want registered=%v id=%s", c.name, before[i], c.registered, c.id)
		}
	}
	// Warm the shared registry cache through a reader that reads it, then read again.
	if _, err := confineAnchorPaths(dataDir, workspaceIDForPath(repo), []string{"inside.txt"}); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		if after := read()[i]; after != before[i] {
			t.Fatalf("%s: warmed cache changed the answer: %+v vs %+v", c.name, after, before[i])
		}
	}
}

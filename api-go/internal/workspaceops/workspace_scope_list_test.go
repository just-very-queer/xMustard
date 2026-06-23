package workspaceops

import (
	"path/filepath"
	"testing"
)

// P1-D: GET /api/workspaces must be filtered by token scope. A workspace-scoped token
// sees only its allowed roots (so it can't enumerate other tenants' repo paths), while
// an unscoped/nil principal stays unrestricted (backward-compatible).
func TestListWorkspacesScopedHidesOtherRoots(t *testing.T) {
	dir := t.TempDir()
	if err := writeJSON(filepath.Join(dir, "workspaces.json"), []workspaceRecord{
		{WorkspaceID: "ws-A", Name: "Alpha", RootPath: "/repos/alpha"},
		{WorkspaceID: "ws-B", Name: "Bravo", RootPath: "/repos/bravo-secret"},
		{WorkspaceID: "ws-C", Name: "Charlie", RootPath: "/repos/charlie"},
	}); err != nil {
		t.Fatal(err)
	}

	// scoped token -> only its workspaces, no other roots disclosed.
	scoped := &Principal{ID: "worker-1", Role: "agent", Workspaces: []string{"ws-A", "ws-C"}}
	got, err := ListWorkspacesScoped(dir, scoped)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("scoped principal should see 2 workspaces, got %d: %+v", len(got), got)
	}
	for _, w := range got {
		if w.WorkspaceID == "ws-B" || w.RootPath == "/repos/bravo-secret" {
			t.Fatalf("scoped token must not see out-of-scope workspace/root: %+v", w)
		}
	}

	// nil principal (no auth configured) -> unrestricted.
	all, err := ListWorkspacesScoped(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("nil principal should be unrestricted (3), got %d", len(all))
	}

	// empty-scope token (unscoped) -> unrestricted, backward-compatible.
	unscoped := &Principal{ID: "admin", Role: "admin"}
	allU, err := ListWorkspacesScoped(dir, unscoped)
	if err != nil {
		t.Fatal(err)
	}
	if len(allU) != 3 {
		t.Fatalf("empty-scope token should be unrestricted (3), got %d", len(allU))
	}
}

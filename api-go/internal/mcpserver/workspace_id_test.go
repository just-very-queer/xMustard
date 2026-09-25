package mcpserver

import (
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// WorkspaceIDForPath duplicates workspaceops' deterministic id so the shim does not
// link that package; registering a root through the API must produce the same id.
func TestWorkspaceIDForPathMatchesAPIRegistration(t *testing.T) {
	data := t.TempDir()
	for _, name := range []string{"My Repo_x", "plain", "..."} {
		root := filepath.Join(t.TempDir(), name)
		// no snapshot and no scan: the record is saved, then ErrNotExist is reported
		_, _ = workspaceops.LoadWorkspace(data, workspaceops.WorkspaceLoadRequest{RootPath: root})
		list, err := workspaceops.ListWorkspaces(data)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, w := range list {
			if w.RootPath == root {
				found = true
				if w.WorkspaceID != WorkspaceIDForPath(root) {
					t.Fatalf("%s: API id %s, mcpserver id %s", root, w.WorkspaceID, WorkspaceIDForPath(root))
				}
			}
		}
		if !found {
			t.Fatalf("%s not registered", root)
		}
	}
}

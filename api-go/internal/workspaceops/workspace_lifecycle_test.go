package workspaceops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceLifecycleListLoadExportAndWorktree(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	snapshot.ScannerVersion = scannerVersion
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json"), snapshot); err != nil {
		t.Fatalf("rewrite snapshot: %v", err)
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces.json"), []workspaceRecord{snapshot.Workspace}); err != nil {
		t.Fatalf("write workspaces: %v", err)
	}

	workspaces, err := ListWorkspaces(dataDir)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].WorkspaceID != workspaceID {
		t.Fatalf("unexpected workspaces: %#v", workspaces)
	}

	loaded, err := LoadWorkspace(dataDir, WorkspaceLoadRequest{
		RootPath:             snapshot.Workspace.RootPath,
		AutoScan:             true,
		PreferCachedSnapshot: true,
	})
	if err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	if loaded.Workspace.WorkspaceID != workspaceID || len(loaded.Issues) == 0 {
		t.Fatalf("unexpected loaded snapshot: %#v", loaded)
	}

	worktree, err := ReadWorktreeStatus(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read worktree: %v", err)
	}
	if !worktree.Available {
		t.Fatalf("expected available worktree status: %#v", worktree)
	}

	exported, err := ExportWorkspace(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("export workspace: %v", err)
	}
	if exported.Workspace.WorkspaceID != workspaceID || exported.Snapshot.Workspace.WorkspaceID != workspaceID {
		t.Fatalf("unexpected export payload: %#v", exported)
	}
	if exported.ExportedAt == "" {
		t.Fatalf("expected export timestamp")
	}
}

func TestLoadWorkspaceScansUncachedWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(root, "docs", "bugs"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "bugs", "Bugs_25260323.md"), []byte("### P1_25M03_001. Broken export\n- Summary: Export fails.\n- Evidence:\n- `src/app.py:1`\n"), 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "app.py"), []byte("# TODO: fix export\nprint('ok')\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	snapshot, err := LoadWorkspace(dataDir, WorkspaceLoadRequest{
		RootPath:             root,
		AutoScan:             true,
		PreferCachedSnapshot: true,
	})
	if err != nil {
		t.Fatalf("load uncached workspace: %v", err)
	}
	if snapshot.Workspace.RootPath != root || len(snapshot.Issues) != 1 {
		t.Fatalf("unexpected scanned snapshot: %#v", snapshot)
	}
	if !fileExists(filepath.Join(dataDir, "workspaces", snapshot.Workspace.WorkspaceID, "snapshot.json")) {
		t.Fatalf("expected persisted snapshot")
	}
}

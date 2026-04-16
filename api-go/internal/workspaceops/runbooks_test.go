package workspaceops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListSaveAndDeleteRunbooks(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, true)

	items, err := ListRunbooks(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list runbooks: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected built-in runbooks")
	}

	saved, err := SaveRunbook(dataDir, workspaceID, RunbookUpsertRequest{
		Name:        "Focused fix",
		Description: "Minimal patch workflow",
		Scope:       "issue",
		Template:    "1. Reproduce.\n2. Patch.\n3. Verify.",
	})
	if err != nil {
		t.Fatalf("save runbook: %v", err)
	}
	if saved.RunbookID == "" || saved.Name != "Focused fix" {
		t.Fatalf("unexpected saved runbook: %#v", saved)
	}

	items, err = ListRunbooks(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list runbooks after save: %v", err)
	}
	if !containsRunbook(items, saved.RunbookID) {
		t.Fatalf("missing saved runbook in list: %#v", items)
	}

	if err := DeleteRunbook(dataDir, workspaceID, saved.RunbookID); err != nil {
		t.Fatalf("delete runbook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", workspaceID, "runbooks.json")); err != nil {
		t.Fatalf("expected persisted runbooks file: %v", err)
	}
}

func containsRunbook(items []RunbookRecord, runbookID string) bool {
	for _, item := range items {
		if item.RunbookID == runbookID {
			return true
		}
	}
	return false
}

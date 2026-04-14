package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTicketContextCrudPersistsArtifacts(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	workspaceID := "workspace-1"
	issueID := "P0_25M03_001"
	snapshotPath := filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json")
	if err := writeJSON(snapshotPath, workspaceSnapshot{
		Workspace: workspaceRecord{
			WorkspaceID: workspaceID,
			RootPath:    filepath.Join(tempDir, "repo"),
		},
		Issues: []issueRecord{
			{BugID: issueID, Source: "ledger"},
		},
	}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	saved, err := SaveTicketContext(dataDir, workspaceID, issueID, TicketContextUpsertRequest{
		Provider:           "manual",
		Title:              "Customer escalation",
		Summary:            "The fix must preserve export compatibility for existing teams.",
		AcceptanceCriteria: []string{"Exports keep previous CSV columns", "Failure no longer reproduces"},
		Links:              []string{"https://tracker.example.com/incidents/123"},
		Labels:             []string{"customer", "export"},
		Status:             stringPtr("open"),
	})
	if err != nil {
		t.Fatalf("save ticket context: %v", err)
	}
	if saved.ContextID == "" {
		t.Fatalf("expected context id")
	}

	contexts, err := ListTicketContexts(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list ticket contexts: %v", err)
	}
	if len(contexts) != 1 || contexts[0].Title != "Customer escalation" {
		t.Fatalf("unexpected ticket contexts: %#v", contexts)
	}

	if err := DeleteTicketContext(dataDir, workspaceID, issueID, saved.ContextID); err != nil {
		t.Fatalf("delete ticket context: %v", err)
	}
	remaining, err := ListTicketContexts(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list remaining ticket contexts: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no contexts after delete, got %#v", remaining)
	}

	activityPath := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	content, err := os.ReadFile(activityPath)
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsIssueAction(content, "ticket_context.saved") || !containsIssueAction(content, "ticket_context.deleted") {
		t.Fatalf("missing ticket context activity entries: %s", string(content))
	}
}

func containsIssueAction(content []byte, action string) bool {
	lines := bytesSplitLines(content)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(line, &payload); err != nil {
			continue
		}
		if payload["action"] == action {
			return true
		}
	}
	return false
}

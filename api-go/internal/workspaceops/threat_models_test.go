package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestThreatModelCrudPersistsArtifacts(t *testing.T) {
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

	saved, err := SaveThreatModel(dataDir, workspaceID, issueID, ThreatModelUpsertRequest{
		Title:           "Export Abuse Path",
		Methodology:     "manual",
		Summary:         "Exports must not leak hidden columns.",
		Assets:          []string{"CSV export"},
		EntryPoints:     []string{"Export endpoint"},
		TrustBoundaries: []string{"App to customer browser"},
		AbuseCases:      []string{"Unintended field exposure"},
		Mitigations:     []string{"Schema allowlist"},
		References:      []string{"https://tracker.example.com/threat/1"},
		Status:          "draft",
	})
	if err != nil {
		t.Fatalf("save threat model: %v", err)
	}
	if saved.ThreatModelID == "" {
		t.Fatalf("expected threat model id")
	}

	models, err := ListThreatModels(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list threat models: %v", err)
	}
	if len(models) != 1 || models[0].Title != "Export Abuse Path" {
		t.Fatalf("unexpected threat models: %#v", models)
	}

	if err := DeleteThreatModel(dataDir, workspaceID, issueID, saved.ThreatModelID); err != nil {
		t.Fatalf("delete threat model: %v", err)
	}
	remaining, err := ListThreatModels(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list remaining threat models: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no threat models after delete, got %#v", remaining)
	}

	activityPath := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	content, err := os.ReadFile(activityPath)
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsThreatAction(content, "threat_model.saved") || !containsThreatAction(content, "threat_model.deleted") {
		t.Fatalf("missing threat model activity entries: %s", string(content))
	}
}

func containsThreatAction(content []byte, action string) bool {
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

package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVerificationProfileCrudPersistsArtifacts(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	workspaceID := "workspace-1"
	snapshotPath := filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json")
	if err := writeJSON(snapshotPath, workspaceSnapshot{
		Workspace: workspaceRecord{
			WorkspaceID: workspaceID,
			RootPath:    filepath.Join(tempDir, "repo"),
		},
		Issues: []issueRecord{},
	}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	initial, err := ListVerificationProfiles(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list initial profiles: %v", err)
	}
	if len(initial) != 1 || initial[0].ProfileID != "manual-check" {
		t.Fatalf("expected built-in manual profile, got %#v", initial)
	}

	saved, err := SaveVerificationProfile(dataDir, workspaceID, VerificationProfileUpsertRequest{
		Name:               "Backend pytest",
		Description:        "Project verification commands",
		TestCommand:        "pytest -q",
		CoverageFormat:     "cobertura",
		MaxRuntimeSeconds:  90,
		RetryCount:         2,
		SourcePaths:        []string{"AGENTS.md"},
		CoverageCommand:    stringPtr("pytest --cov=. --cov-report=xml"),
		CoverageReportPath: stringPtr("coverage.xml"),
	})
	if err != nil {
		t.Fatalf("save verification profile: %v", err)
	}
	if saved.ProfileID != "backend-pytest" {
		t.Fatalf("unexpected profile id: %s", saved.ProfileID)
	}

	profiles, err := ListVerificationProfiles(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list profiles after save: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("expected manual + saved profile, got %#v", profiles)
	}

	if err := DeleteVerificationProfile(dataDir, workspaceID, saved.ProfileID); err != nil {
		t.Fatalf("delete verification profile: %v", err)
	}

	afterDelete, err := ListVerificationProfiles(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list profiles after delete: %v", err)
	}
	if len(afterDelete) != 1 || afterDelete[0].ProfileID != "manual-check" {
		t.Fatalf("expected only manual profile after delete, got %#v", afterDelete)
	}

	activityPath := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	content, err := os.ReadFile(activityPath)
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsSettingsAction(content, "verification_profile.saved") || !containsSettingsAction(content, "verification_profile.deleted") {
		t.Fatalf("missing profile activity entries: %s", string(content))
	}
}

func containsSettingsAction(content []byte, action string) bool {
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

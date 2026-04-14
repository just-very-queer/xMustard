package workspaceops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/rustcore"
)

func TestRunIssueVerificationProfilePersistsTrackerArtifacts(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("mkdir repo root: %v", err)
	}

	workspaceID := "workspace-1"
	snapshotPath := filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json")
	if err := writeJSON(snapshotPath, workspaceSnapshot{
		Workspace: workspaceRecord{
			WorkspaceID: workspaceID,
			RootPath:    repoRoot,
		},
		Issues: []issueRecord{
			{BugID: "P0_25M03_001", Source: "ledger"},
		},
	}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	profilesPath := filepath.Join(dataDir, "workspaces", workspaceID, "verification_profiles.json")
	if err := writeJSON(profilesPath, []verificationProfileRecord{
		{
			ProfileID:          "backend-pytest",
			WorkspaceID:        workspaceID,
			Name:               "Backend pytest",
			Description:        "Verification profile",
			TestCommand:        "printf 'tests ok\\n'",
			CoverageCommand:    stringPtr("printf 'SF:src/app.py\\nDA:1,1\\nDA:2,0\\nend_of_record\\n' > coverage.info"),
			CoverageReportPath: stringPtr("coverage.info"),
			CoverageFormat:     "lcov",
			MaxRuntimeSeconds:  2,
			RetryCount:         1,
			SourcePaths:        []string{"AGENTS.md"},
			BuiltIn:            false,
			CreatedAt:          nowUTC(),
			UpdatedAt:          nowUTC(),
		},
	}); err != nil {
		t.Fatalf("write verification profiles: %v", err)
	}

	result, err := RunIssueVerificationProfile(context.Background(), dataDir, workspaceID, "P0_25M03_001", "backend-pytest", "manual-run-1")
	if err != nil {
		t.Fatalf("run issue verification profile: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success result")
	}
	if result.CoverageResult == nil || result.CoverageResult.Format != "lcov" {
		t.Fatalf("expected lcov coverage result, got %#v", result.CoverageResult)
	}

	coveragePath := filepath.Join(dataDir, "coverage", result.CoverageResult.ResultID+".json")
	if _, err := os.Stat(coveragePath); err != nil {
		t.Fatalf("coverage artifact not written: %v", err)
	}

	var snapshot workspaceSnapshot
	if err := readJSON(snapshotPath, &snapshot); err != nil {
		t.Fatalf("reload snapshot: %v", err)
	}
	if len(snapshot.Issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(snapshot.Issues))
	}
	if len(snapshot.Issues[0].TestsPassed) != 1 || snapshot.Issues[0].TestsPassed[0] != "printf 'tests ok\\n'" {
		t.Fatalf("tests_passed not updated: %#v", snapshot.Issues[0].TestsPassed)
	}
	if len(snapshot.Issues[0].VerificationEvidence) != 1 || snapshot.Issues[0].VerificationEvidence[0].Path != "coverage.info" {
		t.Fatalf("verification evidence not updated: %#v", snapshot.Issues[0].VerificationEvidence)
	}

	activityPath := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	content, err := os.ReadFile(activityPath)
	if err != nil {
		t.Fatalf("read activity log: %v", err)
	}
	if !containsLineWithAction(content, "coverage.parsed") || !containsLineWithAction(content, "verification.profile_run") {
		t.Fatalf("expected coverage and verification activity entries, got %s", string(content))
	}
}

func containsLineWithAction(content []byte, action string) bool {
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

func bytesSplitLines(content []byte) [][]byte {
	var lines [][]byte
	start := 0
	for idx, value := range content {
		if value == '\n' {
			lines = append(lines, content[start:idx])
			start = idx + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}

func stringPtr(value string) *string {
	return &value
}

var _ *rustcore.VerificationProfileResult

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
			ChecklistItems:     []string{"Coverage artifact is produced", "Regression command passes"},
			BuiltIn:            false,
			CreatedAt:          nowUTC(),
			UpdatedAt:          nowUTC(),
		},
	}); err != nil {
		t.Fatalf("write verification profiles: %v", err)
	}
	branch := "feature/verification-reports"
	if err := saveRunRecord(dataDir, runRecord{
		RunID:       "manual-run-1",
		WorkspaceID: workspaceID,
		IssueID:     "P0_25M03_001",
		Runtime:     "codex",
		Model:       "gpt-5.4",
		Status:      "completed",
		Title:       "Verification lane",
		Prompt:      "Run verification profile",
		Command:     []string{"codex", "run"},
		CreatedAt:   nowUTC(),
		CompletedAt: stringPtr("2026-04-16T12:00:00Z"),
		Worktree: &WorktreeStatus{
			Available:  true,
			IsGitRepo:  true,
			Branch:     &branch,
			DirtyPaths: []string{},
		},
	}); err != nil {
		t.Fatalf("save run record: %v", err)
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
	if result.Confidence != "high" || len(result.ChecklistResults) < 3 {
		t.Fatalf("expected confidence and checklist results, got %#v", result)
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

	history, err := ListVerificationProfileHistory(dataDir, workspaceID, "backend-pytest", "P0_25M03_001")
	if err != nil {
		t.Fatalf("list verification profile history: %v", err)
	}
	if len(history) != 1 || history[0].RunID == nil || *history[0].RunID != "manual-run-1" {
		t.Fatalf("expected persisted verification profile history, got %#v", history)
	}

	reports, err := ListVerificationProfileReports(dataDir, workspaceID, "P0_25M03_001")
	if err != nil {
		t.Fatalf("list verification profile reports: %v", err)
	}
	if len(reports) < 2 {
		t.Fatalf("expected built-in and custom profile reports, got %#v", reports)
	}
	var custom *VerificationProfileReport
	for index := range reports {
		if reports[index].ProfileID == "backend-pytest" {
			custom = &reports[index]
			break
		}
	}
	if custom == nil {
		t.Fatalf("missing custom profile report: %#v", reports)
	}
	if custom.SuccessRate != 100 || custom.ConfidenceCounts["high"] != 1 {
		t.Fatalf("unexpected report summary: %#v", custom)
	}
	if len(custom.RuntimeBreakdown) != 1 || custom.RuntimeBreakdown[0].Label != "codex" {
		t.Fatalf("expected runtime breakdown for codex: %#v", custom.RuntimeBreakdown)
	}
	if len(custom.ModelBreakdown) != 1 || custom.ModelBreakdown[0].Label != "gpt-5.4" {
		t.Fatalf("expected model breakdown for gpt-5.4: %#v", custom.ModelBreakdown)
	}
	if len(custom.BranchBreakdown) != 1 || custom.BranchBreakdown[0].Label != branch {
		t.Fatalf("expected branch breakdown for %s: %#v", branch, custom.BranchBreakdown)
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

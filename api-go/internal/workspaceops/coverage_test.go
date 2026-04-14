package workspaceops

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/rustcore"
)

func TestParseCoverageReportAndReadBackLatestCoverage(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(filepath.Join(repoRoot, "src"), 0o755); err != nil {
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

	reportPath := filepath.Join(repoRoot, "coverage.info")
	if err := os.WriteFile(reportPath, []byte("SF:src/app.py\nDA:1,1\nDA:2,0\nend_of_record\n"), 0o644); err != nil {
		t.Fatalf("write coverage report: %v", err)
	}

	result, err := ParseCoverageReport(context.Background(), dataDir, workspaceID, "coverage.info", "run-1", "P0_25M03_001")
	if err != nil {
		t.Fatalf("parse coverage report: %v", err)
	}
	if result.Format != "lcov" {
		t.Fatalf("expected lcov format, got %s", result.Format)
	}

	latest, err := GetCoverage(dataDir, workspaceID, "P0_25M03_001", "run-1")
	if err != nil {
		t.Fatalf("get latest coverage: %v", err)
	}
	if latest.ResultID != result.ResultID {
		t.Fatalf("expected latest coverage %s, got %s", result.ResultID, latest.ResultID)
	}
}

func TestGetCoverageDeltaUsesPersistedCoverageArtifacts(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")

	issueID := "P0_25M03_001"
	workspaceID := "workspace-1"
	run1 := "run-1"
	run2 := "run-2"

	first := coverageFixture(workspaceID, &run1, &issueID, "cov-first", 50.0, 1, 2, []string{"src/empty.py"}, "2026-04-14T10:00:00Z")
	second := coverageFixture(workspaceID, &run2, &issueID, "cov-second", 100.0, 2, 2, []string{}, "2026-04-14T10:05:00Z")
	if err := saveCoverageResult(dataDir, first); err != nil {
		t.Fatalf("save first coverage: %v", err)
	}
	if err := saveCoverageResult(dataDir, second); err != nil {
		t.Fatalf("save second coverage: %v", err)
	}

	delta, err := GetCoverageDelta(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("get coverage delta: %v", err)
	}
	if delta.LineDelta != 50.0 {
		t.Fatalf("expected line delta 50.0, got %v", delta.LineDelta)
	}
	if len(delta.NewFilesCovered) != 1 || delta.NewFilesCovered[0] != "src/empty.py" {
		t.Fatalf("unexpected new files covered: %#v", delta.NewFilesCovered)
	}
	if len(delta.FilesRegressed) != 0 {
		t.Fatalf("unexpected files regressed: %#v", delta.FilesRegressed)
	}
}

func coverageFixture(
	workspaceID string,
	runID *string,
	issueID *string,
	resultID string,
	lineCoverage float64,
	linesCovered int,
	linesTotal int,
	uncovered []string,
	createdAt string,
) *rustcore.CoverageResult {
	return &rustcore.CoverageResult{
		ResultID:       resultID,
		WorkspaceID:    workspaceID,
		RunID:          runID,
		IssueID:        issueID,
		LineCoverage:   lineCoverage,
		LinesCovered:   linesCovered,
		LinesTotal:     linesTotal,
		FilesCovered:   linesCovered,
		FilesTotal:     linesTotal,
		UncoveredFiles: uncovered,
		Format:         "lcov",
		CreatedAt:      createdAt,
	}
}

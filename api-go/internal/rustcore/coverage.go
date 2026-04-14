package rustcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

type CoverageResult struct {
	ResultID         string   `json:"result_id"`
	WorkspaceID      string   `json:"workspace_id"`
	RunID            *string  `json:"run_id"`
	IssueID          *string  `json:"issue_id"`
	LineCoverage     float64  `json:"line_coverage"`
	BranchCoverage   *float64 `json:"branch_coverage"`
	FunctionCoverage *float64 `json:"function_coverage"`
	LinesCovered     int      `json:"lines_covered"`
	LinesTotal       int      `json:"lines_total"`
	BranchesCovered  *int     `json:"branches_covered"`
	BranchesTotal    *int     `json:"branches_total"`
	FilesCovered     int      `json:"files_covered"`
	FilesTotal       int      `json:"files_total"`
	UncoveredFiles   []string `json:"uncovered_files"`
	Format           string   `json:"format"`
	RawReportPath    *string  `json:"raw_report_path"`
	CreatedAt        string   `json:"created_at"`
}

func ParseLCOVCoverage(ctx context.Context, workspaceID string, reportPath string, runID string, issueID string) (*CoverageResult, error) {
	args := []string{
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"parse-coverage-lcov",
		workspaceID,
		reportPath,
	}
	if runID != "" {
		args = append(args, runID)
	}
	if issueID != "" {
		args = append(args, issueID)
	}

	cmd := exec.CommandContext(ctx, "cargo", args...)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core parse-coverage-lcov failed: %w: %s", err, stderr.String())
	}

	var result CoverageResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core coverage result: %w", err)
	}
	return &result, nil
}

func ParseCoverage(ctx context.Context, workspaceID string, reportPath string, runID string, issueID string) (*CoverageResult, error) {
	args := []string{
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"parse-coverage",
		workspaceID,
		reportPath,
	}
	if runID != "" {
		args = append(args, runID)
	}
	if issueID != "" {
		args = append(args, issueID)
	}

	cmd := exec.CommandContext(ctx, "cargo", args...)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core parse-coverage failed: %w: %s", err, stderr.String())
	}

	var result CoverageResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core coverage result: %w", err)
	}
	return &result, nil
}

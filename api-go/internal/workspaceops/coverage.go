package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"

	"xmustard/api-go/internal/rustcore"
)

type CoverageDelta struct {
	WorkspaceID     string                   `json:"workspace_id"`
	IssueID         string                   `json:"issue_id"`
	Baseline        *rustcore.CoverageResult `json:"baseline"`
	Current         *rustcore.CoverageResult `json:"current"`
	LineDelta       float64                  `json:"line_delta"`
	BranchDelta     *float64                 `json:"branch_delta"`
	LinesAdded      int                      `json:"lines_added"`
	LinesLost       int                      `json:"lines_lost"`
	NewFilesCovered []string                 `json:"new_files_covered"`
	FilesRegressed  []string                 `json:"files_regressed"`
	CalculatedAt    string                   `json:"calculated_at"`
}

func ParseCoverageReport(
	ctx context.Context,
	dataDir string,
	workspaceID string,
	reportPath string,
	runID string,
	issueID string,
) (*rustcore.CoverageResult, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	fullPath := filepath.Join(snapshot.Workspace.RootPath, reportPath)
	if !fileExists(fullPath) {
		return nil, os.ErrNotExist
	}

	result, err := rustcore.ParseCoverage(ctx, workspaceID, fullPath, runID, issueID)
	if err != nil {
		return nil, err
	}
	if err := saveCoverageResult(dataDir, result); err != nil {
		return nil, err
	}
	if issueID != "" {
		if err := appendActivity(dataDir, workspaceID, issueID, runID, "coverage.parsed",
			fmt.Sprintf("Parsed coverage report: %.1f%% lines", result.LineCoverage),
			map[string]any{
				"line_coverage": result.LineCoverage,
				"files_covered": result.FilesCovered,
			},
		); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func GetCoverage(dataDir string, workspaceID string, issueID string, runID string) (*rustcore.CoverageResult, error) {
	results, err := loadCoverageResults(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	if runID != "" {
		filtered := results[:0]
		for _, result := range results {
			if result.RunID != nil && *result.RunID == runID {
				filtered = append(filtered, result)
			}
		}
		results = filtered
	}
	if len(results) == 0 {
		return nil, os.ErrNotExist
	}
	slices.SortFunc(results, func(a, b rustcore.CoverageResult) int {
		if a.CreatedAt == b.CreatedAt {
			return 0
		}
		if a.CreatedAt > b.CreatedAt {
			return -1
		}
		return 1
	})
	latest := results[0]
	return &latest, nil
}

func GetCoverageDelta(dataDir string, workspaceID string, issueID string) (*CoverageDelta, error) {
	if issueID == "" {
		return nil, errors.New("missing issue_id")
	}
	results, err := loadCoverageResults(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(results, func(a, b rustcore.CoverageResult) int {
		if a.CreatedAt == b.CreatedAt {
			return 0
		}
		if a.CreatedAt < b.CreatedAt {
			return -1
		}
		return 1
	})
	if len(results) == 0 {
		return &CoverageDelta{
			WorkspaceID:     workspaceID,
			IssueID:         issueID,
			NewFilesCovered: []string{},
			FilesRegressed:  []string{},
			CalculatedAt:    nowUTC(),
		}, nil
	}
	if len(results) == 1 {
		baseline := results[0]
		return &CoverageDelta{
			WorkspaceID:     workspaceID,
			IssueID:         issueID,
			Baseline:        &baseline,
			Current:         nil,
			LineDelta:       baseline.LineCoverage,
			NewFilesCovered: []string{},
			FilesRegressed:  []string{},
			CalculatedAt:    nowUTC(),
		}, nil
	}

	baseline := results[0]
	current := results[len(results)-1]
	newCovered := setDifference(baseline.UncoveredFiles, current.UncoveredFiles)
	regressed := setDifference(current.UncoveredFiles, baseline.UncoveredFiles)
	var branchDelta *float64
	if baseline.BranchCoverage != nil && current.BranchCoverage != nil {
		value := round2(*current.BranchCoverage - *baseline.BranchCoverage)
		branchDelta = &value
	}

	return &CoverageDelta{
		WorkspaceID:     workspaceID,
		IssueID:         issueID,
		Baseline:        &baseline,
		Current:         &current,
		LineDelta:       round2(current.LineCoverage - baseline.LineCoverage),
		BranchDelta:     branchDelta,
		LinesAdded:      max(0, current.LinesCovered-baseline.LinesCovered),
		LinesLost:       max(0, baseline.LinesCovered-current.LinesCovered),
		NewFilesCovered: newCovered,
		FilesRegressed:  regressed,
		CalculatedAt:    nowUTC(),
	}, nil
}

func loadCoverageResults(dataDir string, workspaceID string, issueID string) ([]rustcore.CoverageResult, error) {
	coverageDir := filepath.Join(dataDir, "coverage")
	entries, err := os.ReadDir(coverageDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []rustcore.CoverageResult{}, nil
		}
		return nil, err
	}

	results := []rustcore.CoverageResult{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var result rustcore.CoverageResult
		if err := readJSON(filepath.Join(coverageDir, entry.Name()), &result); err != nil {
			continue
		}
		if result.WorkspaceID != workspaceID {
			continue
		}
		if issueID != "" {
			if result.IssueID == nil || *result.IssueID != issueID {
				continue
			}
		}
		results = append(results, result)
	}
	return results, nil
}

func loadSnapshot(dataDir string, workspaceID string) (*workspaceSnapshot, error) {
	snapshotPath := filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json")
	var snapshot workspaceSnapshot
	if err := readJSON(snapshotPath, &snapshot); err != nil {
		return nil, fmt.Errorf("load snapshot: %w", err)
	}
	return &snapshot, nil
}

func setDifference(left []string, right []string) []string {
	rightSet := map[string]struct{}{}
	for _, item := range right {
		rightSet[item] = struct{}{}
	}
	out := []string{}
	for _, item := range left {
		if _, found := rightSet[item]; found {
			continue
		}
		out = append(out, item)
	}
	slices.Sort(out)
	return out
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

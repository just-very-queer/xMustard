package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// groundingRuns is the run-history section of `ground`: runs that failed, errored,
// were cancelled or exited non-zero, newest first. It is null (unknown) when the
// run history cannot be listed; unreadable run records are reported, not skipped
// silently.
type groundingRuns struct {
	RecentFailedRuns []string `json:"recent_failed_runs"`
}

// build lists failed runs. failuresKnown is false when a failure may be missing
// from the list (the history is unreadable, or some records are).
func (s *groundingRuns) build(dataDir, workspaceID string) (unknown []GroundingUnknown, failuresKnown bool) {
	failed, unreadable, err := scanRunOutcomes(dataDir, workspaceID)
	if err != nil {
		return []GroundingUnknown{{Field: "recent_failed_runs", Reason: "run history unreadable: " + err.Error()}}, false
	}
	s.RecentFailedRuns = failed
	if unreadable > 0 {
		return []GroundingUnknown{{Field: "recent_failed_runs", Reason: fmt.Sprintf("%d run record(s) unreadable; failures among them are not listed", unreadable)}}, false
	}
	return nil, true
}

// scanRunOutcomes reads the status fields of every run record (the same files the
// run list reads) and returns the failed run ids newest first, and how many records
// could not be decoded.
func scanRunOutcomes(dataDir, workspaceID string) (failed []string, unreadable int, err error) {
	runsDir := filepath.Join(dataDir, "workspaces", workspaceID, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, 0, nil
		}
		return nil, 0, err
	}
	type outcome struct {
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
		ExitCode  *int   `json:"exit_code"`
		CreatedAt string `json:"created_at"`
	}
	bad := []outcome{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".out.json") {
			continue
		}
		var run outcome
		if err := readJSON(filepath.Join(runsDir, entry.Name()), &run); err != nil {
			unreadable++
			continue
		}
		status := strings.ToLower(strings.TrimSpace(run.Status))
		if status == "failed" || status == "error" || status == "cancelled" || (run.ExitCode != nil && *run.ExitCode != 0) {
			bad = append(bad, run)
		}
	}
	slices.SortStableFunc(bad, func(a, b outcome) int { return strings.Compare(b.CreatedAt, a.CreatedAt) })
	failed = make([]string, 0, len(bad))
	for _, run := range bad {
		failed = append(failed, run.RunID)
	}
	return failed, unreadable, nil
}

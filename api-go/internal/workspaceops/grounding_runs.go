package workspaceops

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// groundingRuns is the run-history section of `ground`: platform runs that failed,
// errored, were cancelled or exited non-zero, and the open run-independent failures
// (outcomes.go: why_failed commands, logs, evidence and captured test/build output),
// newest first. It is null (unknown) when neither history can be listed; an
// unreadable history or record is reported, not skipped silently.
type groundingRuns struct {
	RecentFailedRuns []string `json:"recent_failed_runs"`
}

// failedRun is one listed failure and when it was recorded.
type failedRun struct {
	id, at string
}

// build lists failed runs. failuresKnown is false when a failure may be missing
// from the list (a history is unreadable, some records are, or the list was capped).
func (s *groundingRuns) build(ctx context.Context, dataDir, workspaceID string) (unknown []GroundingUnknown, failuresKnown bool) {
	failuresKnown = true
	missing := func(reason string) {
		unknown, failuresKnown = append(unknown, GroundingUnknown{Field: "recent_failed_runs", Reason: reason}), false
	}
	platform, unreadable, err := scanRunOutcomes(dataDir, workspaceID)
	if err != nil {
		missing("run history unreadable: " + err.Error())
	} else if unreadable > 0 {
		missing(fmt.Sprintf("%d run record(s) unreadable; failures among them are not listed", unreadable))
	}
	core, more, coreErr := recentRunOutcomes(ctx, dataDir, workspaceID)
	if coreErr != nil {
		missing("run outcomes unreadable: " + coreErr.Error())
	} else if more {
		missing(fmt.Sprintf("more than %d open run outcomes in the last %s; older ones are not listed (GET .../outcomes?open=true lists them)", recentOutcomeLimit, recentOutcomeWindow))
	}
	for _, o := range core {
		platform = append(platform, failedRun{id: o.ID, at: o.CreatedAt})
	}
	if err != nil && len(platform) == 0 {
		return unknown, false // nothing listable: null, not "no failures"
	}
	slices.SortStableFunc(platform, func(a, b failedRun) int { return cmp.Compare(recordedAt(b.at), recordedAt(a.at)) })
	s.RecentFailedRuns = make([]string, 0, len(platform))
	for _, r := range platform {
		s.RecentFailedRuns = append(s.RecentFailedRuns, r.id)
	}
	return unknown, failuresKnown
}

// recordedAt orders RFC 3339 timestamps of either history; an unparseable one sorts last.
func recordedAt(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

// scanRunOutcomes reads the status fields of every platform run record (the same
// files the run list reads) and returns the failed runs, and how many records could
// not be decoded.
func scanRunOutcomes(dataDir, workspaceID string) (failed []failedRun, unreadable int, err error) {
	runsDir := filepath.Join(dataDir, "workspaces", workspaceID, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	type outcome struct {
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
		ExitCode  *int   `json:"exit_code"`
		CreatedAt string `json:"created_at"`
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".out.json") {
			continue
		}
		var run outcome
		if err := readJSON(filepath.Join(runsDir, entry.Name()), &run); err != nil {
			unreadable++
			continue
		}
		if failedRunStatuses[strings.ToLower(strings.TrimSpace(run.Status))] || (run.ExitCode != nil && *run.ExitCode != 0) {
			failed = append(failed, failedRun{id: run.RunID, at: run.CreatedAt})
		}
	}
	return failed, unreadable, nil
}

// failedRunStatuses are the platform run statuses ground lists as failures.
var failedRunStatuses = map[string]bool{"failed": true, "error": true, "cancelled": true}

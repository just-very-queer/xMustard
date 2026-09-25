package workspaceops

import "strings"

// groundingRuns is the run-history section of `ground`: runs that failed, errored,
// were cancelled or exited non-zero.
type groundingRuns struct {
	RecentFailedRuns []string `json:"recent_failed_runs"`
}

func (s *groundingRuns) build(dataDir, workspaceID string) {
	failed := []string{}
	if runs, err := ListRuns(dataDir, workspaceID); err == nil {
		for _, run := range runs {
			status := strings.ToLower(strings.TrimSpace(run.Status))
			bad := status == "failed" || status == "error" || status == "cancelled"
			if run.ExitCode != nil && *run.ExitCode != 0 {
				bad = true
			}
			if bad {
				failed = append(failed, run.RunID)
			}
		}
	}
	s.RecentFailedRuns = failed
}

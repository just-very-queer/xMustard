package workspaceops

import (
	"sort"
	"strings"
)

// Multi-batch eval timeline (FRONTIER Lane 3): rather than comparing only the
// latest two replay batches, this projects every batch over time with its
// success rate, a rank, and chronological movement — so experiment drift is
// visible across the whole history.

type EvalTimelineEntry struct {
	BatchID        string  `json:"batch_id"`
	IssueID        string  `json:"issue_id"`
	Runtime        string  `json:"runtime"`
	Model          string  `json:"model"`
	CreatedAt      string  `json:"created_at"`
	RunCount       int     `json:"run_count"`
	SucceededCount int     `json:"succeeded_count"`
	SuccessRate    float64 `json:"success_rate"`
	Rank           int     `json:"rank"`
	PrevRank       int     `json:"prev_rank"`
	Movement       int     `json:"movement"` // prev_rank - rank; positive = improved
}

type EvalTimeline struct {
	WorkspaceID string              `json:"workspace_id"`
	BatchCount  int                 `json:"batch_count"`
	Entries     []EvalTimelineEntry `json:"entries"`
	GeneratedAt string              `json:"generated_at"`
}

func isSuccessStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "completed", "success", "done":
		return true
	}
	return false
}

func buildEvalTimelineEntries(batches []EvalReplayBatchRecord, runStatus map[string]string) []EvalTimelineEntry {
	entries := make([]EvalTimelineEntry, 0, len(batches))
	for _, batch := range batches {
		succeeded := 0
		for _, runID := range batch.QueuedRunIDs {
			if isSuccessStatus(runStatus[runID]) {
				succeeded++
			}
		}
		rate := 0.0
		if len(batch.QueuedRunIDs) > 0 {
			rate = float64(succeeded) / float64(len(batch.QueuedRunIDs))
		}
		entries = append(entries, EvalTimelineEntry{
			BatchID:        batch.BatchID,
			IssueID:        batch.IssueID,
			Runtime:        batch.Runtime,
			Model:          batch.Model,
			CreatedAt:      batch.CreatedAt,
			RunCount:       len(batch.QueuedRunIDs),
			SucceededCount: succeeded,
			SuccessRate:    rate,
		})
	}

	// rank by success rate (1 = best), tie-break by created_at
	ranked := make([]int, len(entries))
	for i := range ranked {
		ranked[i] = i
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		if entries[ranked[a]].SuccessRate != entries[ranked[b]].SuccessRate {
			return entries[ranked[a]].SuccessRate > entries[ranked[b]].SuccessRate
		}
		return entries[ranked[a]].CreatedAt > entries[ranked[b]].CreatedAt
	})
	for rank, idx := range ranked {
		entries[idx].Rank = rank + 1
	}

	// chronological order + movement vs previous chronological batch
	sort.SliceStable(entries, func(a, b int) bool { return entries[a].CreatedAt < entries[b].CreatedAt })
	for i := range entries {
		if i == 0 {
			entries[i].PrevRank = 0
			entries[i].Movement = 0
			continue
		}
		entries[i].PrevRank = entries[i-1].Rank
		entries[i].Movement = entries[i-1].Rank - entries[i].Rank
	}
	return entries
}

// BuildEvalTimeline assembles the multi-batch eval timeline for a workspace.
func BuildEvalTimeline(dataDir, workspaceID string) (*EvalTimeline, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	batches, err := loadEvalReplayBatches(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	runStatus := map[string]string{}
	if runs, err := ListRuns(dataDir, workspaceID); err == nil {
		for _, run := range runs {
			runStatus[run.RunID] = run.Status
		}
	}
	entries := buildEvalTimelineEntries(batches, runStatus)
	return &EvalTimeline{
		WorkspaceID: workspaceID,
		BatchCount:  len(batches),
		Entries:     entries,
		GeneratedAt: nowUTC(),
	}, nil
}

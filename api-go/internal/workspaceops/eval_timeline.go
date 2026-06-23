package workspaceops

import (
	"fmt"
	"math"
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
	Cohort         string  `json:"cohort"`     // runtime:model — the comparison cohort
	MemoryArm      string  `json:"memory_arm"` // memory configuration arm (reserved for the harness)
	CreatedAt      string  `json:"created_at"`
	RunCount       int     `json:"run_count"`
	SucceededCount int     `json:"succeeded_count"`
	SuccessRate    float64 `json:"success_rate"`
	AvgDurationMS  int     `json:"avg_duration_ms"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	AvgConfidence  float64 `json:"avg_confidence"` // 0 until verification confidence is recorded per run
	Rank           int     `json:"rank"`
	PrevRank       int     `json:"prev_rank"`
	Movement       int     `json:"movement"`   // prev_rank - rank; positive = improved
	WhyMoved       string  `json:"why_moved"`  // causal explanation of the change vs the prior batch
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

func buildEvalTimelineEntries(batches []EvalReplayBatchRecord, runStatus map[string]string, runMetrics map[string]*RunMetrics) []EvalTimelineEntry {
	entries := make([]EvalTimelineEntry, 0, len(batches))
	for _, batch := range batches {
		succeeded := 0
		totalCost := 0.0
		totalDuration := 0
		durationSamples := 0
		for _, runID := range batch.QueuedRunIDs {
			if isSuccessStatus(runStatus[runID]) {
				succeeded++
			}
			if m := runMetrics[runID]; m != nil {
				totalCost += m.EstimatedCost
				if m.DurationMS > 0 {
					totalDuration += m.DurationMS
					durationSamples++
				}
			}
		}
		rate := 0.0
		if len(batch.QueuedRunIDs) > 0 {
			rate = float64(succeeded) / float64(len(batch.QueuedRunIDs))
		}
		avgDuration := 0
		if durationSamples > 0 {
			avgDuration = totalDuration / durationSamples
		}
		entries = append(entries, EvalTimelineEntry{
			BatchID:        batch.BatchID,
			IssueID:        batch.IssueID,
			Runtime:        batch.Runtime,
			Model:          batch.Model,
			Cohort:         strings.TrimSpace(batch.Runtime) + ":" + strings.TrimSpace(batch.Model),
			CreatedAt:      batch.CreatedAt,
			RunCount:       len(batch.QueuedRunIDs),
			SucceededCount: succeeded,
			SuccessRate:    rate,
			AvgDurationMS:  avgDuration,
			TotalCostUSD:   roundCost(totalCost),
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

	// chronological order + movement + a CAUSAL "why it moved" vs the previous batch.
	sort.SliceStable(entries, func(a, b int) bool { return entries[a].CreatedAt < entries[b].CreatedAt })
	for i := range entries {
		if i == 0 {
			entries[i].PrevRank = 0
			entries[i].Movement = 0
			entries[i].WhyMoved = "first batch — no prior batch to compare"
			continue
		}
		entries[i].PrevRank = entries[i-1].Rank
		entries[i].Movement = entries[i-1].Rank - entries[i].Rank
		entries[i].WhyMoved = explainEvalMovement(entries[i-1], entries[i])
	}
	return entries
}

// explainEvalMovement produces a short causal explanation of why a batch's rank moved vs
// the previous chronological batch, attributing it to the metric deltas (success rate,
// cost, duration) rather than just reporting the rank numbers.
func explainEvalMovement(prev, cur EvalTimelineEntry) string {
	var drivers []string
	if d := cur.SuccessRate - prev.SuccessRate; math.Abs(d) >= 0.001 {
		drivers = append(drivers, fmt.Sprintf("success rate %+.0f%% (%d/%d → %d/%d)",
			d*100, prev.SucceededCount, prev.RunCount, cur.SucceededCount, cur.RunCount))
	}
	if d := cur.TotalCostUSD - prev.TotalCostUSD; math.Abs(d) >= 0.005 {
		drivers = append(drivers, fmt.Sprintf("cost %+.2f USD", d))
	}
	if d := cur.AvgDurationMS - prev.AvgDurationMS; d != 0 {
		drivers = append(drivers, fmt.Sprintf("avg duration %+d ms", d))
	}
	if cur.Cohort != prev.Cohort {
		drivers = append(drivers, fmt.Sprintf("cohort changed %s → %s", prev.Cohort, cur.Cohort))
	}
	var head string
	switch {
	case cur.Movement > 0:
		head = fmt.Sprintf("rank improved %d→%d", prev.Rank, cur.Rank)
	case cur.Movement < 0:
		head = fmt.Sprintf("rank dropped %d→%d", prev.Rank, cur.Rank)
	default:
		head = fmt.Sprintf("rank unchanged (%d)", cur.Rank)
	}
	if len(drivers) == 0 {
		return head + " — no material metric change"
	}
	return head + " — driven by " + strings.Join(drivers, ", ")
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
	// per-run metrics (cost + duration) for the timeline's cost/duration columns.
	runMetrics := map[string]*RunMetrics{}
	for _, batch := range batches {
		for _, runID := range batch.QueuedRunIDs {
			if _, seen := runMetrics[runID]; seen {
				continue
			}
			if m, err := loadRunMetrics(dataDir, runID); err == nil {
				runMetrics[runID] = m
			}
		}
	}
	entries := buildEvalTimelineEntries(batches, runStatus, runMetrics)
	return &EvalTimeline{
		WorkspaceID: workspaceID,
		BatchCount:  len(batches),
		Entries:     entries,
		GeneratedAt: nowUTC(),
	}, nil
}

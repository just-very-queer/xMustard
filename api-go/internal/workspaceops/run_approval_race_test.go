package workspaceops

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// Many concurrent approvals of one awaiting_approval run must launch the worker
// EXACTLY once — the per-run lock makes the read-check-transition atomic so the
// losers see phase=approved and bail (XM-PRO-002).
func TestApproveRunPlanSingleLaunchUnderConcurrency(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)
	opencodeBin := writeFakeOpencode(t)
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), appSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &opencodeBin,
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	const runID = "run-race"
	run := runRecord{
		RunID:          runID,
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "opencode",
		Model:          "fake/test-model",
		Status:         "queued",
		Title:          "opencode:P0_25M03_001",
		Prompt:         "race",
		Command:        []string{opencodeBin, "run", "--format", "json", "--dir", repoRoot, "-m", "fake/test-model", "race"},
		CommandPreview: "opencode run",
		LogPath:        filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".log"),
		OutputPath:     filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
		Plan: &RunPlan{
			PlanID:    "plan-race",
			RunID:     runID,
			Phase:     "awaiting_approval",
			Summary:   "race",
			CreatedAt: "2026-04-14T10:08:00Z",
		},
	}
	if err := saveRunRecord(dataDir, run); err != nil {
		t.Fatalf("save run: %v", err)
	}

	const concurrency = 8
	var wg sync.WaitGroup
	var successes int64
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ApproveRunPlan(dataDir, workspaceID, runID, PlanApproveRequest{}); err == nil {
				atomic.AddInt64(&successes, 1)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("expected exactly 1 successful approval, got %d", successes)
	}
	waitForRunStatus(t, dataDir, workspaceID, runID, "completed")
	waitForRunCleanup(t, runID)
}

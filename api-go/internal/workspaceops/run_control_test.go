package workspaceops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRetryRunStartsManagedProcessAndCompletes(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)
	opencodeBin := writeFakeOpencode(t)
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), appSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &opencodeBin,
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	original := runRecord{
		RunID:          "run-origin",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "opencode",
		Model:          "fake/test-model",
		Status:         "completed",
		Title:          "opencode:P0_25M03_001",
		Prompt:         "normal retry",
		Command:        []string{opencodeBin, "run", "--format", "json", "--dir", repoRoot, "-m", "fake/test-model", "normal retry"},
		CommandPreview: "opencode run",
		LogPath:        filepath.Join(repoRoot, "run-origin.log"),
		OutputPath:     filepath.Join(repoRoot, "run-origin.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
	}
	if err := saveRunRecord(dataDir, original); err != nil {
		t.Fatalf("save origin run: %v", err)
	}

	retried, err := RetryRun(dataDir, workspaceID, "run-origin")
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if retried.RunID == "run-origin" || retried.Status != "queued" {
		t.Fatalf("unexpected retried run: %#v", retried)
	}

	final := waitForRunStatus(t, dataDir, workspaceID, retried.RunID, "completed")
	waitForRunCleanup(t, retried.RunID)
	if final.Summary == nil || strings.TrimSpace(firstStringFromSummary(final.Summary, "text_excerpt")) == "" {
		t.Fatalf("expected summary excerpt after retry: %#v", final)
	}
	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsAction(content, "run.retried") || !containsAction(content, "run.completed") {
		t.Fatalf("missing retry activity entries: %s", string(content))
	}
}

func TestCancelRunTerminatesRunningProcessAndPersistsCancelledStatus(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)
	opencodeBin := writeFakeOpencode(t)
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), appSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &opencodeBin,
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	run := runRecord{
		RunID:          "run-cancel",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "opencode",
		Model:          "fake/test-model",
		Status:         "queued",
		Title:          "opencode:P0_25M03_001",
		Prompt:         "sleepy retry",
		Command:        []string{opencodeBin, "run", "--format", "json", "--dir", repoRoot, "-m", "fake/test-model", "sleepy retry"},
		CommandPreview: "opencode run",
		LogPath:        filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-cancel.log"),
		OutputPath:     filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-cancel.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
	}
	if err := saveRunRecord(dataDir, run); err != nil {
		t.Fatalf("save run: %v", err)
	}
	startManagedRun(dataDir, run, repoRoot)
	waitForPID(t, dataDir, workspaceID, "run-cancel")

	cancelled, err := CancelRun(dataDir, workspaceID, "run-cancel")
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("unexpected cancelled run: %#v", cancelled)
	}
	waitForRunStatus(t, dataDir, workspaceID, "run-cancel", "cancelled")
	waitForRunCleanup(t, "run-cancel")
}

func TestGenerateAndApprovePlanThenRejectPlan(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)
	opencodeBin := writeFakeOpencode(t)
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), appSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &opencodeBin,
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	run := runRecord{
		RunID:          "run-plan",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "opencode",
		Model:          "fake/test-model",
		Status:         "queued",
		Title:          "opencode:P0_25M03_001",
		Prompt:         "normal retry",
		Command:        []string{opencodeBin, "run", "--format", "json", "--dir", repoRoot, "-m", "fake/test-model", "normal retry"},
		CommandPreview: "opencode run",
		LogPath:        filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-plan.log"),
		OutputPath:     filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-plan.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
		RunbookID:      stringPtr("fix"),
	}
	if err := saveRunRecord(dataDir, run); err != nil {
		t.Fatalf("save run: %v", err)
	}

	plan, err := GenerateRunPlan(dataDir, workspaceID, "run-plan")
	if err != nil {
		t.Fatalf("generate plan: %v", err)
	}
	if plan.Phase != "awaiting_approval" || len(plan.Steps) != 1 {
		t.Fatalf("unexpected plan: %#v", plan)
	}

	storedPlan, err := GetRunPlan(dataDir, workspaceID, "run-plan")
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if storedPlan.PlanID != plan.PlanID {
		t.Fatalf("stored plan mismatch: %#v", storedPlan)
	}

	approved, err := ApproveRunPlan(dataDir, workspaceID, "run-plan", PlanApproveRequest{
		Feedback: stringPtr("Looks good"),
	})
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	if approved.Phase != "approved" {
		t.Fatalf("unexpected approved plan: %#v", approved)
	}
	waitForRunStatus(t, dataDir, workspaceID, "run-plan", "completed")
	waitForRunCleanup(t, "run-plan")

	rejectRun := runRecord{
		RunID:          "run-plan-reject",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "opencode",
		Model:          "fake/test-model",
		Status:         "planning",
		Title:          "opencode:P0_25M03_001",
		Prompt:         "normal retry",
		Command:        []string{opencodeBin, "run", "--format", "json", "--dir", repoRoot, "-m", "fake/test-model", "normal retry"},
		CommandPreview: "opencode run",
		LogPath:        filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-plan-reject.log"),
		OutputPath:     filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-plan-reject.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
		Plan: &RunPlan{
			PlanID:    "plan-2",
			RunID:     "run-plan-reject",
			Phase:     "awaiting_approval",
			Summary:   "Reject me",
			CreatedAt: "2026-04-14T10:08:00Z",
		},
	}
	if err := saveRunRecord(dataDir, rejectRun); err != nil {
		t.Fatalf("save reject run: %v", err)
	}
	rejected, err := RejectRunPlan(dataDir, workspaceID, "run-plan-reject", PlanRejectRequest{Reason: "Need different approach"})
	if err != nil {
		t.Fatalf("reject plan: %v", err)
	}
	if rejected.Phase != "rejected" {
		t.Fatalf("unexpected rejected plan: %#v", rejected)
	}
	finalRejected, err := ReadRun(dataDir, workspaceID, "run-plan-reject")
	if err != nil {
		t.Fatalf("read rejected run: %v", err)
	}
	if finalRejected.Status != "cancelled" {
		t.Fatalf("expected cancelled rejected run: %#v", finalRejected)
	}
}

func writeFakeOpencode(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode")
	content := `#!/bin/sh
if [ "$1" = "models" ]; then
  echo "fake/test-model"
  exit 0
fi
if [ "$1" = "run" ]; then
  last=""
  for arg in "$@"; do
    last="$arg"
  done
  case "$last" in
    *"Generate a structured plan"*)
      echo '{"summary":"Fix export flow","reasoning":"Minimal patch","steps":[{"step_id":"step_1","description":"Edit src/app.py","estimated_impact":"medium","files_affected":["src/app.py"],"risks":["regression"]}]}'
      exit 0
      ;;
    *"sleepy retry"*)
      echo '{"type":"message","text":"Starting long run"}'
      sleep 5
      echo '{"type":"message","text":"Done long run"}'
      exit 0
      ;;
    *)
      echo '{"type":"message","text":"Updated src/app.py"}'
      echo '{"type":"tool_use"}'
      exit 0
      ;;
  esac
fi
echo "unknown command"
exit 1
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	return path
}

func waitForRunStatus(t *testing.T, dataDir string, workspaceID string, runID string, expected string) *runRecord {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		run, err := ReadRun(dataDir, workspaceID, runID)
		if err == nil && run.Status == expected {
			return run
		}
		time.Sleep(100 * time.Millisecond)
	}
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		t.Fatalf("read run after timeout: %v", err)
	}
	t.Fatalf("run %s did not reach %s, got %#v", runID, expected, run)
	return nil
}

func waitForPID(t *testing.T, dataDir string, workspaceID string, runID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := ReadRun(dataDir, workspaceID, runID)
		if err == nil && run.PID != nil && *run.PID > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run %s never got a pid", runID)
}

func waitForRunCleanup(t *testing.T, runID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, active := activeRunProcesses.Load(runID); !active {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("run %s never cleaned up", runID)
}

func firstStringFromSummary(summary map[string]any, key string) string {
	if summary == nil {
		return ""
	}
	if value, ok := summary[key].(string); ok {
		return value
	}
	return ""
}

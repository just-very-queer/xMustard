package workspaceops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoalLifecycleEvidenceGateAndContext(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, true)

	created, err := CreateGoal(dataDir, workspaceID, GoalCreateRequest{
		Title:                "Implement one-day goal ledger",
		Objective:            "Add durable goal tracking with evidence-gated completion.",
		AcceptanceCriteria:   []string{"Goal can be created", "Complete is blocked without verification"},
		CurrentTranche:       "Backend proof slice",
		AllowedSurface:       []string{"api-go/internal/workspaceops/goals.go"},
		VerificationCommands: []string{"go test ./internal/workspaceops -run Goal -count=1"},
		RuntimePreference:    "opencode",
		PreferredModel:       "qwen3-coder",
		ResumptionNotes:      "Keep markdown as a projection only.",
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if created.GoalID == "" || created.Status != GoalStatusDraft {
		t.Fatalf("unexpected created goal: %#v", created)
	}

	goals, err := ListGoals(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list goals: %v", err)
	}
	if len(goals) != 1 || goals[0].GoalID != created.GoalID {
		t.Fatalf("unexpected goals: %#v", goals)
	}

	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", workspaceID, "goals", created.GoalID+".md")); err != nil {
		t.Fatalf("expected generated ledger: %v", err)
	}

	if _, err := UpdateGoalStatus(dataDir, workspaceID, created.GoalID, GoalStatusUpdateRequest{Status: GoalStatusComplete}); err == nil {
		t.Fatalf("expected completion without verification to fail")
	}

	iteration, err := AppendGoalIteration(dataDir, workspaceID, created.GoalID, GoalIterationAppendRequest{
		Role:         "verifier",
		Summary:      "Ran focused goal tests.",
		Outcome:      "pass",
		Runtime:      "manual",
		FilesTouched: []string{"api-go/internal/workspaceops/goals.go", "api-go/internal/workspaceops/goals_test.go"},
		Evidence: []GoalEvidence{{
			Kind:    "verification",
			Label:   "Focused Go goal tests",
			Command: "go test ./internal/workspaceops -run Goal -count=1",
			Outcome: "pass",
		}},
	})
	if err != nil {
		t.Fatalf("append iteration: %v", err)
	}
	if iteration.IterationID == "" || len(iteration.Evidence) != 1 {
		t.Fatalf("unexpected iteration: %#v", iteration)
	}

	completed, err := UpdateGoalStatus(dataDir, workspaceID, created.GoalID, GoalStatusUpdateRequest{Status: GoalStatusComplete})
	if err != nil {
		t.Fatalf("complete with verification: %v", err)
	}
	if completed.Status != GoalStatusComplete || completed.CompletedAt == nil {
		t.Fatalf("goal not completed: %#v", completed)
	}

	ledger, err := ReadGoalLedger(dataDir, workspaceID, created.GoalID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if !strings.Contains(ledger, "Goal Ledger") || !strings.Contains(ledger, "Focused Go goal tests") {
		t.Fatalf("ledger missing expected content:\n%s", ledger)
	}

	packet, err := BuildGoalContextPacket(dataDir, workspaceID, created.GoalID)
	if err != nil {
		t.Fatalf("context packet: %v", err)
	}
	if !strings.Contains(packet, "Worker output is proposal/evidence") || !strings.Contains(packet, "Allowed Surface") {
		t.Fatalf("context packet missing guardrails:\n%s", packet)
	}
}

func TestGoalCompletionWithExplicitSkipReason(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, true)

	created, err := CreateGoal(dataDir, workspaceID, GoalCreateRequest{
		Title:     "Document goal skip",
		Objective: "Record a human verification skip reason.",
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}

	completed, err := UpdateGoalStatus(dataDir, workspaceID, created.GoalID, GoalStatusUpdateRequest{
		Status:                    GoalStatusComplete,
		VerificationSkippedReason: "Demo-only documentation slice; no runtime command applies.",
	})
	if err != nil {
		t.Fatalf("complete with skip reason: %v", err)
	}
	if completed.Status != GoalStatusComplete || len(completed.Evidence) != 1 {
		t.Fatalf("skip evidence not preserved: %#v", completed)
	}
	if completed.Evidence[0].Kind != "verification-skip" || !strings.Contains(completed.Evidence[0].Notes, "Demo-only") {
		t.Fatalf("unexpected skip evidence: %#v", completed.Evidence[0])
	}
}

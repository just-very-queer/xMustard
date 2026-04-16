package workspaceops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunAnalysisReadsMetricsAndCostSummary(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)

	run := runRecord{
		RunID:          "run-metrics",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "codex",
		Model:          "gpt-5.4-mini",
		Status:         "completed",
		Title:          "codex:P0_25M03_001",
		Prompt:         "Keep old columns remain stable and ensure export works end to end.",
		Command:        []string{"codex", "exec"},
		CommandPreview: "codex exec",
		LogPath:        filepath.Join(repoRoot, "run-metrics.log"),
		OutputPath:     filepath.Join(repoRoot, "run-metrics.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-metrics.json"), run); err != nil {
		t.Fatalf("write run: %v", err)
	}
	if err := writeJSON(filepath.Join(dataDir, "metrics", "run-metrics.json"), RunMetrics{
		RunID:         "run-metrics",
		WorkspaceID:   workspaceID,
		InputTokens:   120,
		OutputTokens:  80,
		EstimatedCost: 0.1234567,
		DurationMS:    3456,
		Model:         "gpt-5.4-mini",
		Runtime:       "codex",
		CalculatedAt:  "2026-04-14T10:07:00Z",
	}); err != nil {
		t.Fatalf("write metrics: %v", err)
	}

	metric, err := GetRunMetrics(dataDir, workspaceID, "run-metrics")
	if err != nil {
		t.Fatalf("get run metrics: %v", err)
	}
	if metric.DurationMS != 3456 || metric.InputTokens != 120 {
		t.Fatalf("unexpected metric: %#v", metric)
	}

	metrics, err := ListWorkspaceMetrics(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list workspace metrics: %v", err)
	}
	if len(metrics) != 1 || metrics[0].RunID != "run-metrics" {
		t.Fatalf("unexpected metrics list: %#v", metrics)
	}

	costs, err := GetWorkspaceCostSummary(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("get workspace cost summary: %v", err)
	}
	if costs.TotalRuns != 1 || costs.TotalInputTokens != 120 || costs.TotalDurationMS != 3456 {
		t.Fatalf("unexpected cost summary: %#v", costs)
	}
}

func TestGeneratePatchCritiquePersistsArtifactAndInsightReadsIt(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)

	outputPath := filepath.Join(repoRoot, "run-critique.out.json")
	output := "src/app.py: TODO tighten export schema so old columns remain\nexport works end to end after the patch\npanic while patching\n"
	if err := os.WriteFile(outputPath, []byte(output), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}

	run := runRecord{
		RunID:          "run-critique",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "codex",
		Model:          "gpt-5.4-mini",
		Status:         "failed",
		Title:          "codex:P0_25M03_001",
		Prompt:         "Keep old columns remain stable and ensure export works end to end.",
		Command:        []string{"codex", "exec"},
		CommandPreview: "codex exec",
		LogPath:        filepath.Join(repoRoot, "run-critique.log"),
		OutputPath:     outputPath,
		CreatedAt:      "2026-04-14T10:06:00Z",
		ExitCode:       intPtr(1),
		Worktree: &WorktreeStatus{
			Available:  true,
			IsGitRepo:  true,
			DirtyPaths: []string{"src/app.py", "docs/notes.md"},
		},
		Summary: map[string]any{
			"text_excerpt":     "Investigated panic in src/app.py",
			"event_count":      10,
			"tool_event_count": 9,
		},
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-critique.json"), run); err != nil {
		t.Fatalf("write run: %v", err)
	}
	if err := writeJSON(filepath.Join(dataDir, "metrics", "run-critique.json"), RunMetrics{
		RunID:         "run-critique",
		WorkspaceID:   workspaceID,
		InputTokens:   200,
		OutputTokens:  150,
		EstimatedCost: 0.22,
		DurationMS:    2200,
		Model:         "gpt-5.4-mini",
		Runtime:       "codex",
		CalculatedAt:  "2026-04-14T10:08:00Z",
	}); err != nil {
		t.Fatalf("write metrics: %v", err)
	}

	critique, err := GeneratePatchCritique(dataDir, workspaceID, "run-critique")
	if err != nil {
		t.Fatalf("generate patch critique: %v", err)
	}
	if critique.RunID != "run-critique" || len(critique.Improvements) == 0 || len(critique.IssuesFound) == 0 {
		t.Fatalf("unexpected critique: %#v", critique)
	}
	if critique.AcceptanceReview.Status != "met" || len(critique.ScopeWarnings) == 0 {
		t.Fatalf("expected acceptance review and scope warnings on critique, got %#v", critique)
	}

	stored, err := GetPatchCritique(dataDir, workspaceID, "run-critique")
	if err != nil {
		t.Fatalf("get patch critique: %v", err)
	}
	if stored.CritiqueID != critique.CritiqueID {
		t.Fatalf("stored critique mismatch: %#v vs %#v", stored, critique)
	}
	if stored.AcceptanceReview.Status != "met" || len(stored.ScopeWarnings) == 0 {
		t.Fatalf("stored critique missing compliance details: %#v", stored)
	}

	improvements, err := GetRunImprovements(dataDir, workspaceID, "run-critique")
	if err != nil {
		t.Fatalf("get run improvements: %v", err)
	}
	if len(improvements) == 0 {
		t.Fatalf("expected improvements, got %#v", improvements)
	}

	insight, err := GetRunSessionInsight(dataDir, workspaceID, "run-critique")
	if err != nil {
		t.Fatalf("get run session insight: %v", err)
	}
	if insight.RunID != "run-critique" || len(insight.Risks) == 0 || len(insight.Recommendations) == 0 {
		t.Fatalf("unexpected insight: %#v", insight)
	}
	if insight.AcceptanceReview.Status != "met" {
		t.Fatalf("expected met acceptance review, got %#v", insight.AcceptanceReview)
	}
	if len(insight.ScopeWarnings) == 0 || insight.ScopeWarnings[0].Kind == "" {
		t.Fatalf("expected scope warnings, got %#v", insight.ScopeWarnings)
	}

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsAction(content, "critique.generated") {
		t.Fatalf("missing critique.generated activity: %s", string(content))
	}
}

func TestDismissImprovementUpdatesCritiqueArtifact(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)

	run := runRecord{
		RunID:          "run-dismiss",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "codex",
		Model:          "gpt-5.4-mini",
		Status:         "completed",
		Title:          "codex:P0_25M03_001",
		Prompt:         "prompt",
		Command:        []string{"codex", "exec"},
		CommandPreview: "codex exec",
		LogPath:        filepath.Join(repoRoot, "run-dismiss.log"),
		OutputPath:     filepath.Join(repoRoot, "run-dismiss.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-dismiss.json"), run); err != nil {
		t.Fatalf("write run: %v", err)
	}
	if err := savePatchCritique(dataDir, PatchCritique{
		CritiqueID:     "crit-1",
		WorkspaceID:    workspaceID,
		RunID:          "run-dismiss",
		IssueID:        issueID,
		OverallQuality: "acceptable",
		Correctness:    75,
		Completeness:   70,
		Style:          80,
		Safety:         85,
		IssuesFound:    []string{},
		Improvements: []ImprovementSuggestion{
			{
				SuggestionID: "imp-1",
				FilePath:     "src/app.py",
				Category:     "testing",
				Severity:     "medium",
				Description:  "Add regression test",
			},
		},
		AcceptanceReview: AcceptanceCriteriaReview{
			Status:   "partial",
			Criteria: []string{"Old columns remain"},
			Missing:  []string{"Old columns remain"},
		},
		ScopeWarnings: []ScopeWarning{
			{
				Kind:     "scope_drift",
				Message:  "Issue drift flags remain open.",
				Severity: "medium",
			},
		},
		Summary:     "Overall quality: acceptable",
		GeneratedAt: "2026-04-14T10:09:00Z",
	}); err != nil {
		t.Fatalf("save critique: %v", err)
	}

	updated, err := DismissImprovement(dataDir, workspaceID, "run-dismiss", "imp-1", DismissImprovementRequest{
		Reason: stringPtr("Already handled"),
	})
	if err != nil {
		t.Fatalf("dismiss improvement: %v", err)
	}
	if len(updated.Improvements) != 1 || !updated.Improvements[0].Dismissed {
		t.Fatalf("improvement not dismissed: %#v", updated)
	}
	if updated.AcceptanceReview.Status != "partial" || len(updated.ScopeWarnings) != 1 {
		t.Fatalf("dismiss should preserve critique compliance fields: %#v", updated)
	}

	items, err := GetRunImprovements(dataDir, workspaceID, "run-dismiss")
	if err != nil {
		t.Fatalf("get improvements after dismiss: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no active improvements, got %#v", items)
	}
}

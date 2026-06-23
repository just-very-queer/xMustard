package workspaceops

import (
	"path/filepath"
	"strings"
	"testing"
)

// Feature-depth: a run-session insight must surface the workspace run-policy decision for
// THAT run (not only at the pre-run gate + dashboard). A run whose cost exceeds the
// ceiling and whose runtime is not allowed shows blocking violations in insight.Policy and
// in the risks list.
func TestRunSessionInsightSurfacesPolicyDecision(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)

	run := runRecord{
		RunID:       "run-pol",
		WorkspaceID: workspaceID,
		IssueID:     issueID,
		Runtime:     "codex",
		Model:       "gpt-5.4-mini",
		Status:      "completed",
		LogPath:     filepath.Join(repoRoot, "run-pol.log"),
		OutputPath:  filepath.Join(repoRoot, "run-pol.out.json"),
		CreatedAt:   "2026-04-14T10:06:00Z",
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-pol.json"), run); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dataDir, "metrics", "run-pol.json"), RunMetrics{
		RunID: "run-pol", WorkspaceID: workspaceID, Runtime: "codex", Model: "gpt-5.4-mini",
		EstimatedCost: 5.00, DurationMS: 1000, CalculatedAt: "2026-04-14T10:07:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	// policy: only "opencode" allowed (codex disallowed) + a $1 ceiling (run cost $5).
	max := 1.0
	if _, err := SetWorkspacePolicy(dataDir, workspaceID, WorkspacePolicy{
		AllowedRuntimes: []string{"opencode"},
		MaxRunCostUSD:   &max,
	}); err != nil {
		t.Fatal(err)
	}

	insight, err := GetRunSessionInsight(dataDir, workspaceID, "run-pol")
	if err != nil {
		t.Fatal(err)
	}
	if insight.Policy == nil {
		t.Fatalf("insight must carry the policy evaluation")
	}
	if insight.Policy.Allowed {
		t.Fatalf("a $5 run on a disallowed runtime with a $1 ceiling must not be allowed")
	}
	codes := map[string]bool{}
	for _, v := range insight.Policy.Violations {
		codes[v.Code] = true
	}
	if !codes["runtime-not-allowed"] || !codes["cost-over-max"] {
		t.Fatalf("expected runtime-not-allowed + cost-over-max violations, got %+v", insight.Policy.Violations)
	}
	// the violations must also be visible in the human-facing risks.
	joined := strings.Join(insight.Risks, " | ")
	if !strings.Contains(joined, "Policy") {
		t.Fatalf("policy violations must surface in risks, got: %s", joined)
	}
}

// When no policy is configured (default), the insight still evaluates cleanly: allowed,
// no violations — so the field is informative, not noisy.
func TestRunSessionInsightPolicyCleanByDefault(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)
	run := runRecord{
		RunID: "run-clean", WorkspaceID: workspaceID, IssueID: issueID,
		Runtime: "codex", Model: "m", Status: "completed",
		LogPath:    filepath.Join(repoRoot, "run-clean.log"),
		OutputPath: filepath.Join(repoRoot, "run-clean.out.json"),
		CreatedAt:  "2026-04-14T10:06:00Z",
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-clean.json"), run); err != nil {
		t.Fatal(err)
	}
	insight, err := GetRunSessionInsight(dataDir, workspaceID, "run-clean")
	if err != nil {
		t.Fatal(err)
	}
	if insight.Policy == nil || !insight.Policy.Allowed || len(insight.Policy.Violations) != 0 {
		t.Fatalf("default policy should be allowed with no violations, got %+v", insight.Policy)
	}
}

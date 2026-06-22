package workspaceops

import "testing"

func TestBuildRunBrief(t *testing.T) {
	zero := 0
	run := runRecord{
		RunID: "r1", IssueID: "bug-1", Runtime: "codex", Model: "gpt-5.4", Status: "succeeded",
		ExitCode: &zero, Plan: &RunPlan{},
		Summary: map[string]any{"files_touched": []any{"a.go", "b.go"}},
	}
	brief := buildRunBrief("ws", run)
	if len(brief.FilesTouched) != 2 || !brief.HadPlan {
		t.Fatalf("brief files/plan wrong: %+v", brief)
	}
	if brief.Confidence.Level != "high" {
		t.Fatalf("expected high confidence in brief, got %s", brief.Confidence.Level)
	}
	if brief.Headline == "" {
		t.Fatal("expected a headline")
	}
}

func TestUpsertAgentIdentity(t *testing.T) {
	var agents []AgentIdentity
	agents, _ = upsertAgentIdentity(agents, "ws", "codex", "gpt-5.4", "2026-01-01T00:00:00Z")
	agents, a := upsertAgentIdentity(agents, "ws", "codex", "gpt-5.4", "2026-01-03T00:00:00Z")
	if len(agents) != 1 || a.RunCount != 2 {
		t.Fatalf("expected 1 agent with run_count 2, got %d / %+v", len(agents), a)
	}
	if a.LastSeenAt != "2026-01-03T00:00:00Z" || a.FirstSeenAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("first/last seen wrong: %+v", a)
	}
	agents, _ = upsertAgentIdentity(agents, "ws", "opencode", "deepseek", "2026-01-02T00:00:00Z")
	if len(agents) != 2 {
		t.Fatalf("expected 2 distinct agents, got %d", len(agents))
	}
}

func TestEvaluateSecurityAcceptance(t *testing.T) {
	criteria := defaultSecurityAcceptanceCriteria("ws")
	// summary with an open exploitable + an unverified confirmed -> two failures
	summary := SecurityReviewPacket{OpenExploitable: 1, UnverifiedConfirmed: 2, AcceptedRiskCount: 1}
	res := evaluateSecurityAcceptance("ws", criteria, summary, nil)
	if res.Passed {
		t.Fatalf("expected acceptance to fail with open exploitable")
	}
	failed := 0
	for _, c := range res.Checks {
		if !c.Passed {
			failed++
		}
	}
	if failed != 2 {
		t.Fatalf("expected 2 failed checks (open_exploitable, confirmed_verified), got %d: %+v", failed, res.Checks)
	}

	// clean summary -> passes
	clean := evaluateSecurityAcceptance("ws", criteria, SecurityReviewPacket{}, nil)
	if !clean.Passed {
		t.Fatalf("expected clean summary to pass: %+v", clean)
	}
}

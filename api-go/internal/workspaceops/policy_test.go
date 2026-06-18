package workspaceops

import "testing"

func f64(v float64) *float64 { return &v }

func TestEvaluateRunPolicy(t *testing.T) {
	// permissive default: nothing set -> allowed, no violations
	open := evaluateRunPolicy(WorkspacePolicy{WorkspaceID: "ws"}, RunPolicyInput{Runtime: "codex", EstimatedCostUSD: f64(5)})
	if !open.Allowed || len(open.Violations) != 0 {
		t.Fatalf("permissive policy should allow with no violations: %+v", open)
	}

	// runtime not allowed -> block
	restricted := WorkspacePolicy{WorkspaceID: "ws", AllowedRuntimes: []string{"opencode"}}
	blocked := evaluateRunPolicy(restricted, RunPolicyInput{Runtime: "codex"})
	if blocked.Allowed {
		t.Fatalf("codex should be blocked when only opencode allowed")
	}
	if blocked.Violations[0].Code != "runtime-not-allowed" || blocked.Violations[0].Severity != "block" {
		t.Fatalf("expected runtime-not-allowed block, got %+v", blocked.Violations)
	}

	// cost over max -> block; cost over warning only -> warn but allowed
	costPolicy := WorkspacePolicy{WorkspaceID: "ws", BudgetWarningUSD: f64(10), MaxRunCostUSD: f64(20)}
	overMax := evaluateRunPolicy(costPolicy, RunPolicyInput{Runtime: "codex", EstimatedCostUSD: f64(25)})
	if overMax.Allowed || overMax.Violations[0].Code != "cost-over-max" {
		t.Fatalf("cost over max should block: %+v", overMax)
	}
	overWarn := evaluateRunPolicy(costPolicy, RunPolicyInput{Runtime: "codex", EstimatedCostUSD: f64(15)})
	if !overWarn.Allowed {
		t.Fatalf("cost over warning (under max) should still be allowed: %+v", overWarn)
	}
	if overWarn.Violations[0].Code != "cost-over-warning" || overWarn.Violations[0].Severity != "warn" {
		t.Fatalf("expected cost-over-warning warn, got %+v", overWarn.Violations)
	}

	// plan approval required + not approved -> block
	planPolicy := WorkspacePolicy{WorkspaceID: "ws", RequirePlanApproval: true}
	noPlan := evaluateRunPolicy(planPolicy, RunPolicyInput{Runtime: "codex", PlanApproved: false})
	if noPlan.Allowed {
		t.Fatalf("missing plan approval should block: %+v", noPlan)
	}
	withPlan := evaluateRunPolicy(planPolicy, RunPolicyInput{Runtime: "codex", PlanApproved: true})
	if !withPlan.Allowed {
		t.Fatalf("approved plan should pass: %+v", withPlan)
	}

	// sensitive -> warn but allowed
	sens := evaluateRunPolicy(WorkspacePolicy{WorkspaceID: "ws", Sensitive: true}, RunPolicyInput{Runtime: "codex"})
	if !sens.Allowed || sens.Violations[0].Code != "sensitive-workspace" {
		t.Fatalf("sensitive should warn but allow: %+v", sens)
	}
}

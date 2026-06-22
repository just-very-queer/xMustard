package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace policy records (FRONTIER Lane 5): governance over which runtimes are
// allowed, which verification profiles are required, budget warning thresholds,
// and whether a workspace is sensitive / requires plan approval. The policy is a
// durable record; EvaluateRunAgainstPolicy is a pure gate over a proposed run.

type WorkspacePolicy struct {
	WorkspaceID                    string   `json:"workspace_id"`
	AllowedRuntimes                []string `json:"allowed_runtimes"`                  // empty = all allowed
	RequiredVerificationProfileIDs []string `json:"required_verification_profile_ids"` // must pass before complete
	BudgetWarningUSD               *float64 `json:"budget_warning_usd,omitempty"`
	MaxRunCostUSD                  *float64 `json:"max_run_cost_usd,omitempty"`
	Sensitive                      bool     `json:"sensitive"`
	RequirePlanApproval            bool     `json:"require_plan_approval"`
	Notes                          string   `json:"notes,omitempty"`
	UpdatedAt                      string   `json:"updated_at"`
}

type PolicyViolation struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // block | warn
	Message  string `json:"message"`
}

type RunPolicyEvaluation struct {
	WorkspaceID string            `json:"workspace_id"`
	Allowed     bool              `json:"allowed"`
	Violations  []PolicyViolation `json:"violations"`
	EvaluatedAt string            `json:"evaluated_at"`
}

// RunPolicyInput is the proposed run being checked.
type RunPolicyInput struct {
	Runtime          string   `json:"runtime"`
	Model            string   `json:"model"`
	EstimatedCostUSD *float64 `json:"estimated_cost_usd,omitempty"`
	PlanApproved     bool     `json:"plan_approved"`
}

func policyPath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "policy.json")
}

// GetWorkspacePolicy returns the stored policy, or a permissive default.
func GetWorkspacePolicy(dataDir, workspaceID string) (*WorkspacePolicy, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	var policy WorkspacePolicy
	if err := readJSON(policyPath(dataDir, workspaceID), &policy); err != nil {
		if os.IsNotExist(err) {
			return &WorkspacePolicy{
				WorkspaceID:                    workspaceID,
				AllowedRuntimes:                []string{},
				RequiredVerificationProfileIDs: []string{},
			}, nil
		}
		return nil, err
	}
	policy.WorkspaceID = workspaceID
	if policy.AllowedRuntimes == nil {
		policy.AllowedRuntimes = []string{}
	}
	if policy.RequiredVerificationProfileIDs == nil {
		policy.RequiredVerificationProfileIDs = []string{}
	}
	return &policy, nil
}

// SetWorkspacePolicy validates and persists a policy.
func SetWorkspacePolicy(dataDir, workspaceID string, policy WorkspacePolicy) (*WorkspacePolicy, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	policy.WorkspaceID = workspaceID
	policy.AllowedRuntimes = cleanStrings(policy.AllowedRuntimes)
	policy.RequiredVerificationProfileIDs = cleanStrings(policy.RequiredVerificationProfileIDs)
	for _, rt := range policy.AllowedRuntimes {
		if !validPolicyRuntime(rt) {
			return nil, fmt.Errorf("invalid runtime %q in allowed_runtimes", rt)
		}
	}
	if policy.BudgetWarningUSD != nil && *policy.BudgetWarningUSD < 0 {
		return nil, fmt.Errorf("budget_warning_usd must be >= 0")
	}
	if policy.MaxRunCostUSD != nil && *policy.MaxRunCostUSD < 0 {
		return nil, fmt.Errorf("max_run_cost_usd must be >= 0")
	}
	policy.Notes = strings.TrimSpace(policy.Notes)
	policy.UpdatedAt = nowUTC()
	if err := writeJSON(policyPath(dataDir, workspaceID), policy); err != nil {
		return nil, err
	}
	_ = recordAuditEventNoGuard(dataDir, workspaceID, AuditEvent{
		Action:     "policy.update",
		TargetType: "workspace",
		TargetID:   workspaceID,
		Details:    fmt.Sprintf("allowed_runtimes=%v sensitive=%t require_plan_approval=%t", policy.AllowedRuntimes, policy.Sensitive, policy.RequirePlanApproval),
	})
	return &policy, nil
}

func validPolicyRuntime(runtime string) bool {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "codex", "opencode", "manual":
		return true
	default:
		return false
	}
}

func cleanStrings(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// evaluateRunPolicy is the pure gate: given a policy and a proposed run, return
// violations. block-severity violations make Allowed false.
func evaluateRunPolicy(policy WorkspacePolicy, input RunPolicyInput) RunPolicyEvaluation {
	violations := []PolicyViolation{}

	if len(policy.AllowedRuntimes) > 0 {
		allowed := false
		for _, rt := range policy.AllowedRuntimes {
			if strings.EqualFold(rt, input.Runtime) {
				allowed = true
				break
			}
		}
		if !allowed {
			violations = append(violations, PolicyViolation{
				Code:     "runtime-not-allowed",
				Severity: "block",
				Message:  fmt.Sprintf("runtime %q is not in the allowed list %v", input.Runtime, policy.AllowedRuntimes),
			})
		}
	}

	if input.EstimatedCostUSD != nil {
		if policy.MaxRunCostUSD != nil && *input.EstimatedCostUSD > *policy.MaxRunCostUSD {
			violations = append(violations, PolicyViolation{
				Code:     "cost-over-max",
				Severity: "block",
				Message:  fmt.Sprintf("estimated cost $%.2f exceeds max $%.2f", *input.EstimatedCostUSD, *policy.MaxRunCostUSD),
			})
		} else if policy.BudgetWarningUSD != nil && *input.EstimatedCostUSD > *policy.BudgetWarningUSD {
			violations = append(violations, PolicyViolation{
				Code:     "cost-over-warning",
				Severity: "warn",
				Message:  fmt.Sprintf("estimated cost $%.2f exceeds warning threshold $%.2f", *input.EstimatedCostUSD, *policy.BudgetWarningUSD),
			})
		}
	}

	if policy.RequirePlanApproval && !input.PlanApproved {
		violations = append(violations, PolicyViolation{
			Code:     "plan-approval-required",
			Severity: "block",
			Message:  "this workspace requires an approved plan before the run can proceed",
		})
	}

	if policy.Sensitive {
		violations = append(violations, PolicyViolation{
			Code:     "sensitive-workspace",
			Severity: "warn",
			Message:  "this workspace is marked sensitive; review run scope and evidence handling",
		})
	}

	allowed := true
	for _, v := range violations {
		if v.Severity == "block" {
			allowed = false
			break
		}
	}
	return RunPolicyEvaluation{
		WorkspaceID: policy.WorkspaceID,
		Allowed:     allowed,
		Violations:  violations,
		EvaluatedAt: nowUTC(),
	}
}

// EvaluateRunAgainstPolicy loads the workspace policy and evaluates a proposed run.
func EvaluateRunAgainstPolicy(dataDir, workspaceID string, input RunPolicyInput) (*RunPolicyEvaluation, error) {
	policy, err := GetWorkspacePolicy(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	result := evaluateRunPolicy(*policy, input)
	return &result, nil
}

package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
)

// Explicit security acceptance criteria: a durable, operator-editable set of
// gates a workspace must satisfy before its security posture is "accepted",
// evaluated against the current disposition state.

type SecurityAcceptanceCriterion struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Rule        string `json:"rule"` // no_open_exploitable | all_confirmed_verified | max_accepted_risk | no_suppressed_without_reason
	Threshold   int    `json:"threshold,omitempty"`
}

type SecurityAcceptanceCriteria struct {
	WorkspaceID string                        `json:"workspace_id"`
	Criteria    []SecurityAcceptanceCriterion `json:"criteria"`
	UpdatedAt   string                        `json:"updated_at"`
}

type SecurityAcceptanceCheck struct {
	CriterionID string `json:"criterion_id"`
	Description string `json:"description"`
	Rule        string `json:"rule"`
	Passed      bool   `json:"passed"`
	Detail      string `json:"detail"`
}

type SecurityAcceptanceResult struct {
	WorkspaceID string                    `json:"workspace_id"`
	Passed      bool                      `json:"passed"`
	Checks      []SecurityAcceptanceCheck `json:"checks"`
	EvaluatedAt string                    `json:"evaluated_at"`
}

func securityAcceptancePath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "security_acceptance.json")
}

func defaultSecurityAcceptanceCriteria(workspaceID string) SecurityAcceptanceCriteria {
	return SecurityAcceptanceCriteria{
		WorkspaceID: workspaceID,
		Criteria: []SecurityAcceptanceCriterion{
			{ID: "no-open-exploitable", Description: "No open/confirmed findings with PoC or exploited exploitability", Rule: "no_open_exploitable"},
			{ID: "confirmed-verified", Description: "Every confirmed finding has a linked verification profile", Rule: "all_confirmed_verified"},
			{ID: "accepted-risk-cap", Description: "At most N accepted-risk findings", Rule: "max_accepted_risk", Threshold: 5},
		},
	}
}

// GetSecurityAcceptanceCriteria returns the stored criteria, or sensible defaults.
func GetSecurityAcceptanceCriteria(dataDir, workspaceID string) (*SecurityAcceptanceCriteria, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	var criteria SecurityAcceptanceCriteria
	if err := readJSON(securityAcceptancePath(dataDir, workspaceID), &criteria); err != nil {
		if os.IsNotExist(err) {
			def := defaultSecurityAcceptanceCriteria(workspaceID)
			return &def, nil
		}
		return nil, err
	}
	criteria.WorkspaceID = workspaceID
	return &criteria, nil
}

// SetSecurityAcceptanceCriteria validates and persists the criteria.
func SetSecurityAcceptanceCriteria(dataDir, workspaceID string, in SecurityAcceptanceCriteria) (*SecurityAcceptanceCriteria, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	in.WorkspaceID = workspaceID
	for i, c := range in.Criteria {
		switch c.Rule {
		case "no_open_exploitable", "all_confirmed_verified", "max_accepted_risk", "no_suppressed_without_reason":
		default:
			return nil, fmt.Errorf("criterion %d has invalid rule %q", i, c.Rule)
		}
	}
	in.UpdatedAt = nowUTC()
	if err := writeJSON(securityAcceptancePath(dataDir, workspaceID), in); err != nil {
		return nil, err
	}
	return &in, nil
}

// evaluateSecurityAcceptance is pure: criteria + disposition summary -> result.
func evaluateSecurityAcceptance(workspaceID string, criteria SecurityAcceptanceCriteria, summary SecurityReviewPacket, dispositions []SecurityDisposition) SecurityAcceptanceResult {
	checks := []SecurityAcceptanceCheck{}
	allPassed := true
	for _, c := range criteria.Criteria {
		passed := true
		detail := ""
		switch c.Rule {
		case "no_open_exploitable":
			passed = summary.OpenExploitable == 0
			detail = fmt.Sprintf("%d open/confirmed exploitable findings", summary.OpenExploitable)
		case "all_confirmed_verified":
			passed = summary.UnverifiedConfirmed == 0
			detail = fmt.Sprintf("%d confirmed findings without verification linkage", summary.UnverifiedConfirmed)
		case "max_accepted_risk":
			passed = summary.AcceptedRiskCount <= c.Threshold
			detail = fmt.Sprintf("%d accepted-risk findings (limit %d)", summary.AcceptedRiskCount, c.Threshold)
		case "no_suppressed_without_reason":
			bad := 0
			for _, d := range dispositions {
				if d.Suppressed && d.SuppressionReason == "" {
					bad++
				}
			}
			passed = bad == 0
			detail = fmt.Sprintf("%d suppressed findings missing a reason", bad)
		}
		if !passed {
			allPassed = false
		}
		checks = append(checks, SecurityAcceptanceCheck{
			CriterionID: c.ID,
			Description: c.Description,
			Rule:        c.Rule,
			Passed:      passed,
			Detail:      detail,
		})
	}
	return SecurityAcceptanceResult{
		WorkspaceID: workspaceID,
		Passed:      allPassed,
		Checks:      checks,
		EvaluatedAt: nowUTC(),
	}
}

// EvaluateSecurityAcceptance evaluates the criteria against current dispositions.
func EvaluateSecurityAcceptance(dataDir, workspaceID string) (*SecurityAcceptanceResult, error) {
	criteria, err := GetSecurityAcceptanceCriteria(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	dispositions, err := loadSecurityDispositions(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	summary := summarizeSecurityReview(workspaceID, dispositions)
	result := evaluateSecurityAcceptance(workspaceID, *criteria, summary, dispositions)
	return &result, nil
}

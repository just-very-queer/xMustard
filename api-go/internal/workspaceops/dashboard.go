package workspaceops

// Operational dashboard: a single workspace-health rollup tying together issue
// state, quality, governance policy, the audit trail, and the security review
// summary. Reuses the existing aggregators so it stays consistent with the
// per-feature endpoints.

type PolicySummary struct {
	Sensitive           bool `json:"sensitive"`
	RequirePlanApproval bool `json:"require_plan_approval"`
	AllowedRuntimeCount int  `json:"allowed_runtime_count"`
	RequiredProfiles    int  `json:"required_profile_count"`
	HasCostCeiling      bool `json:"has_cost_ceiling"`
}

type WorkspaceDashboard struct {
	WorkspaceID       string               `json:"workspace_id"`
	IssuesTotal       int                  `json:"issues_total"`
	IssuesBySeverity  map[string]int       `json:"issues_by_severity"`
	IssuesByStatus    map[string]int       `json:"issues_by_status"`
	NeedsFollowup     int                  `json:"needs_followup_count"`
	AvgQuality        int                  `json:"avg_quality"`
	LowQualityCount   int                  `json:"low_quality_count"`
	ReviewReadyCount  int                  `json:"review_ready_count"`
	Policy            PolicySummary        `json:"policy"`
	AuditEventCount   int                  `json:"audit_event_count"`
	Security          SecurityReviewPacket `json:"security"`
	GeneratedAt       string               `json:"generated_at"`
}

// BuildWorkspaceDashboard aggregates the workspace health rollup.
func BuildWorkspaceDashboard(dataDir, workspaceID string) (*WorkspaceDashboard, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	dash := WorkspaceDashboard{
		WorkspaceID:      workspaceID,
		IssuesBySeverity: map[string]int{},
		IssuesByStatus:   map[string]int{},
		GeneratedAt:      nowUTC(),
	}

	qualitySum := 0
	for _, issue := range snapshot.Issues {
		dash.IssuesTotal++
		sev := issue.Severity
		if sev == "" {
			sev = "unset"
		}
		dash.IssuesBySeverity[sev]++
		status := issue.IssueStatus
		if status == "" {
			status = "unset"
		}
		dash.IssuesByStatus[status]++
		if issue.NeedsFollowup {
			dash.NeedsFollowup++
		}
		if issue.ReviewReadyCount > 0 {
			dash.ReviewReadyCount++
		}
		q := scoreIssueQualityRecord(workspaceID, issue)
		qualitySum += q.Overall
		if q.Overall < 50 {
			dash.LowQualityCount++
		}
	}
	if dash.IssuesTotal > 0 {
		dash.AvgQuality = qualitySum / dash.IssuesTotal
	}

	if policy, err := GetWorkspacePolicy(dataDir, workspaceID); err == nil {
		dash.Policy = PolicySummary{
			Sensitive:           policy.Sensitive,
			RequirePlanApproval: policy.RequirePlanApproval,
			AllowedRuntimeCount: len(policy.AllowedRuntimes),
			RequiredProfiles:    len(policy.RequiredVerificationProfileIDs),
			HasCostCeiling:      policy.MaxRunCostUSD != nil || policy.BudgetWarningUSD != nil,
		}
	}

	if events, err := loadAuditEvents(dataDir, workspaceID); err == nil {
		dash.AuditEventCount = len(events)
	}

	if dispositions, err := loadSecurityDispositions(dataDir, workspaceID); err == nil {
		dash.Security = summarizeSecurityReview(workspaceID, dispositions)
	}

	return &dash, nil
}

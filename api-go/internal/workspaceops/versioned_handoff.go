package workspaceops

// VersionedHandoff is the SINGLE first-class issue handoff that the review frontier asked
// for: it merges the previously-separate artifacts — the issue review packet (fix summary
// + quality + VERIFICATION + residual RISK + ready-for-handoff), the workspace SECURITY
// review packet, and the latest run's session insight (which carries the ACCEPTANCE
// criteria review and the run POLICY decision) — into one versioned object a reviewer,
// a PR description, or a Linear/Jira sync can consume without re-reading raw artifacts.
// Composed from the existing builders, so each dimension stays single-sourced.
type VersionedHandoff struct {
	Version          int                  `json:"version"`
	IssueID          string               `json:"issue_id"`
	WorkspaceID      string               `json:"workspace_id"`
	ReadyForHandoff  bool                 `json:"ready_for_handoff"`
	Review           *ReviewPacket        `json:"review"`              // fix + quality + verification + residual risk
	Security         *SecurityReviewPacket `json:"security"`           // security disposition rollup
	LatestRunInsight *RunSessionInsight   `json:"latest_run_insight"`  // acceptance review + policy + risks
	MissingSections  []string             `json:"missing_sections"`    // dimensions with no data yet (honesty)
	GeneratedAt      string               `json:"generated_at"`
}

// handoffSchemaVersion bumps when the merged shape changes, so a stored/synced handoff
// records which schema produced it.
const handoffSchemaVersion = 1

// BuildVersionedHandoff assembles the unified handoff for an issue. Each sub-builder is
// best-effort: a missing dimension is recorded in MissingSections rather than failing the
// whole handoff, so a partially-complete issue still produces an honest packet.
func BuildVersionedHandoff(dataDir, workspaceID, issueID string) (*VersionedHandoff, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	out := &VersionedHandoff{
		Version:     handoffSchemaVersion,
		IssueID:     issueID,
		WorkspaceID: workspaceID,
		GeneratedAt: nowUTC(),
	}

	review, err := BuildReviewPacket(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err // the issue itself must resolve; everything else is additive
	}
	out.Review = review
	out.ReadyForHandoff = review.ReadyForHandoff

	if sec, err := BuildSecurityReviewPacket(dataDir, workspaceID); err == nil {
		out.Security = sec
	} else {
		out.MissingSections = append(out.MissingSections, "security")
	}

	if runID := latestRunIDForIssue(dataDir, workspaceID, issueID); runID != "" {
		if insight, err := GetRunSessionInsight(dataDir, workspaceID, runID); err == nil {
			out.LatestRunInsight = insight
		}
	}
	if out.LatestRunInsight == nil {
		out.MissingSections = append(out.MissingSections, "acceptance+policy (no run insight)")
	}
	return out, nil
}

// latestRunIDForIssue returns the most recently created run for an issue (by CreatedAt),
// whose insight carries the acceptance review + policy decision for the handoff.
func latestRunIDForIssue(dataDir, workspaceID, issueID string) string {
	runs, err := ListRuns(dataDir, workspaceID)
	if err != nil {
		return ""
	}
	best := ""
	bestAt := ""
	for i := range runs {
		if runs[i].IssueID != issueID {
			continue
		}
		if best == "" || runs[i].CreatedAt > bestAt {
			best = runs[i].RunID
			bestAt = runs[i].CreatedAt
		}
	}
	return best
}

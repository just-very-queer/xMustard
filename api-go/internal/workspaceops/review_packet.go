package workspaceops

import "strings"

// PR-style issue review packet (FRONTIER Lane 6): one handoff object combining
// the issue, its quality score, a verification summary, and residual risks, with
// a ready_for_handoff gate. Built from durable issue state; no external calls.

type ReviewPacketVerification struct {
	EvidenceCount int  `json:"evidence_count"`
	TestsPassed   int  `json:"tests_passed"`
	HasPassing    bool `json:"has_passing"`
}

type ReviewPacket struct {
	IssueID         string                   `json:"issue_id"`
	WorkspaceID     string                   `json:"workspace_id"`
	Title           string                   `json:"title"`
	Severity        string                   `json:"severity"`
	IssueStatus     string                   `json:"issue_status"`
	Summary         string                   `json:"summary"`
	Quality         IssueQualityScore        `json:"quality"`
	Verification    ReviewPacketVerification `json:"verification"`
	ResidualRisks   []string                 `json:"residual_risks"`
	ReadyForHandoff bool                     `json:"ready_for_handoff"`
	GeneratedAt     string                   `json:"generated_at"`
}

// assembleReviewPacket is pure: it composes a packet from an issue record and
// its already-computed quality score so the handoff gate is easy to test.
func assembleReviewPacket(workspaceID string, issue issueRecord, quality IssueQualityScore) ReviewPacket {
	verification := ReviewPacketVerification{
		EvidenceCount: len(issue.VerificationEvidence),
		TestsPassed:   len(issue.TestsPassed),
		HasPassing:    len(issue.TestsPassed) > 0,
	}

	risks := []string{}
	for _, flag := range issue.DriftFlags {
		if trimmed := strings.TrimSpace(flag); trimmed != "" {
			risks = append(risks, "drift: "+trimmed)
		}
	}
	if issue.NeedsFollowup {
		risks = append(risks, "needs followup")
	}
	if !verification.HasPassing {
		risks = append(risks, "no passing verification recorded")
	}

	ready := quality.Overall >= 60 &&
		verification.HasPassing &&
		len(issue.DriftFlags) == 0 &&
		!issue.NeedsFollowup

	return ReviewPacket{
		IssueID:         issue.BugID,
		WorkspaceID:     workspaceID,
		Title:           issue.Title,
		Severity:        issue.Severity,
		IssueStatus:     issue.IssueStatus,
		Summary:         derefStr(issue.Summary),
		Quality:         quality,
		Verification:    verification,
		ResidualRisks:   risks,
		ReadyForHandoff: ready,
		GeneratedAt:     nowUTC(),
	}
}

// BuildReviewPacket assembles the review packet for one issue.
func BuildReviewPacket(dataDir, workspaceID, issueID string) (*ReviewPacket, error) {
	issue, _, err := findIssueRecord(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	quality := scoreIssueQualityRecord(workspaceID, issue)
	packet := assembleReviewPacket(workspaceID, issue, quality)
	return &packet, nil
}

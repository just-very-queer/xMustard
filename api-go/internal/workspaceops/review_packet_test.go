package workspaceops

import "testing"

func TestAssembleReviewPacket(t *testing.T) {
	readyIssue := issueRecord{
		BugID: "bug-1", Title: "Fix scope check", Severity: "P1", IssueStatus: "verified",
		Summary: strPtr("scope ownership bypass fixed"), TestsPassed: []string{"test_scope"},
	}
	q := scoreIssueQualityRecord("ws", readyIssue)
	q.Overall = 80 // force the gate's quality side
	ready := assembleReviewPacket("ws", readyIssue, q)
	if !ready.ReadyForHandoff {
		t.Fatalf("expected ready_for_handoff true, got %+v", ready)
	}
	if !ready.Verification.HasPassing || ready.Verification.TestsPassed != 1 {
		t.Fatalf("verification summary wrong: %+v", ready.Verification)
	}

	riskyIssue := issueRecord{
		BugID: "bug-2", Title: "Flaky thing", Severity: "P2",
		DriftFlags: []string{"verification_gap"}, NeedsFollowup: true,
	}
	q2 := scoreIssueQualityRecord("ws", riskyIssue)
	risky := assembleReviewPacket("ws", riskyIssue, q2)
	if risky.ReadyForHandoff {
		t.Fatalf("expected not ready with drift+followup+no tests")
	}
	if len(risky.ResidualRisks) < 3 {
		t.Fatalf("expected residual risks (drift, followup, no-verification), got %v", risky.ResidualRisks)
	}
}

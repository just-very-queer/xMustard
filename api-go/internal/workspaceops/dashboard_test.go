package workspaceops

import "testing"

func TestBuildWorkspaceDashboard(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
	dash, err := BuildWorkspaceDashboard(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("build dashboard: %v", err)
	}
	if dash.IssuesTotal < 1 {
		t.Fatalf("expected at least 1 issue from fixture, got %d", dash.IssuesTotal)
	}
	if len(dash.IssuesBySeverity) == 0 || len(dash.IssuesByStatus) == 0 {
		t.Fatalf("expected severity/status breakdowns populated: %+v", dash)
	}
	// quality average is within bounds
	if dash.AvgQuality < 0 || dash.AvgQuality > 100 {
		t.Fatalf("avg quality out of range: %d", dash.AvgQuality)
	}
}

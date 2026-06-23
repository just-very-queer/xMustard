package workspaceops

import "testing"

// Feature-depth: one versioned handoff merges the issue review (verification + risk),
// security, and the latest run insight (acceptance + policy) into a single object — with
// missing dimensions recorded honestly rather than silently dropped.
func TestBuildVersionedHandoffMergesDimensions(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	h, err := BuildVersionedHandoff(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("build handoff: %v", err)
	}
	if h.Version != handoffSchemaVersion {
		t.Fatalf("handoff must carry its schema version, got %d", h.Version)
	}
	if h.IssueID != issueID || h.WorkspaceID != workspaceID {
		t.Fatalf("handoff identity wrong: %+v", h)
	}
	if h.Review == nil {
		t.Fatalf("handoff must include the issue review (verification + risk)")
	}
	if h.Security == nil {
		t.Fatalf("handoff must include the security rollup")
	}
	// no run exists for the issue yet -> the acceptance+policy section is honestly noted.
	foundMissing := false
	for _, m := range h.MissingSections {
		if m == "acceptance+policy (no run insight)" {
			foundMissing = true
		}
	}
	if h.LatestRunInsight != nil {
		t.Fatalf("no run was created; LatestRunInsight should be nil")
	}
	if !foundMissing {
		t.Fatalf("a missing dimension must be recorded honestly, got %+v", h.MissingSections)
	}
}

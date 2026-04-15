package workspaceops

import "testing"

func TestListIssuesAppliesQueueFilters(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	items, err := ListIssues(dataDir, workspaceID, "export", []string{"P1"}, []string{"investigating"}, []string{"ledger"}, []string{"customer"}, true, boolPtr(true), true)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	if len(items) != 1 || items[0].BugID != issueID {
		t.Fatalf("unexpected filtered issues: %#v", items)
	}

	empty, err := ListIssues(dataDir, workspaceID, "missing", nil, nil, nil, nil, false, nil, false)
	if err != nil {
		t.Fatalf("list issues with query: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty issue list, got %#v", empty)
	}
}

func TestReadIssueAndDriftFromSnapshot(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	issue, err := ReadIssue(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if issue.BugID != issueID || issue.Title == "" {
		t.Fatalf("unexpected issue payload: %#v", issue)
	}

	drift, err := ReadIssueDrift(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("read issue drift: %v", err)
	}
	if drift.BugID != issueID || !drift.VerificationGap || len(drift.DriftFlags) == 0 {
		t.Fatalf("unexpected issue drift payload: %#v", drift)
	}

	summary, err := ReadDriftSummary(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read drift summary: %v", err)
	}
	if summary["missing_verification_tests"] != 1 {
		t.Fatalf("unexpected drift summary: %#v", summary)
	}
}

func TestListSignalsAppliesSeverityPromotionAndQueryFilters(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	items, err := ListSignals(dataDir, workspaceID, "export", "P2", boolPtr(false))
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}
	if len(items) != 1 || items[0].SignalID != "signal-1" {
		t.Fatalf("unexpected filtered signals: %#v", items)
	}

	empty, err := ListSignals(dataDir, workspaceID, "", "P0", nil)
	if err != nil {
		t.Fatalf("list signals with severity filter: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty signal list, got %#v", empty)
	}
}

func boolPtr(value bool) *bool {
	return &value
}

package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateIssuePersistsTrackerIssueSnapshotAndActivity(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	created, err := CreateIssue(dataDir, workspaceID, IssueCreateRequest{
		Title:    "New tracker issue",
		Severity: "p2",
		Summary:  stringPtr("Freshly created issue"),
		Labels:   []string{"tracker", "customer"},
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if created.BugID == "" || created.Source != "tracker" || created.Severity != "P2" {
		t.Fatalf("unexpected created issue: %#v", created)
	}

	var tracked []issueRecord
	if err := readJSON(filepath.Join(dataDir, "workspaces", workspaceID, "tracker_issues.json"), &tracked); err != nil {
		t.Fatalf("read tracker issues: %v", err)
	}
	if len(tracked) != 1 || tracked[0].BugID != created.BugID {
		t.Fatalf("unexpected tracked issues: %#v", tracked)
	}

	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("reload snapshot: %v", err)
	}
	found := false
	for _, item := range snapshot.Issues {
		if item.BugID == created.BugID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created issue missing from snapshot: %#v", snapshot.Issues)
	}

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsAction(content, "issue.created") {
		t.Fatalf("missing issue.created activity: %s", string(content))
	}
}

func TestUpdateIssuePersistsOverrideAndActivity(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	updated, err := UpdateIssue(dataDir, workspaceID, issueID, IssueUpdateRequest{
		Severity:      stringPtr("p0"),
		IssueStatus:   stringPtr("verification"),
		Labels:        []string{"urgent", "customer"},
		NeedsFollowup: boolPtr(true),
		Notes:         stringPtr("Need to validate patch"),
	})
	if err != nil {
		t.Fatalf("update issue: %v", err)
	}
	if updated.Severity != "P0" || updated.IssueStatus != "verification" {
		t.Fatalf("unexpected updated issue: %#v", updated)
	}

	var overrides map[string]map[string]any
	if err := readJSON(filepath.Join(dataDir, "workspaces", workspaceID, "issue_overrides.json"), &overrides); err != nil {
		t.Fatalf("read issue overrides: %v", err)
	}
	if _, ok := overrides[issueID]; !ok {
		t.Fatalf("expected issue override for %s, got %#v", issueID, overrides)
	}

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsAction(content, "issue.updated") {
		t.Fatalf("missing issue.updated activity: %s", string(content))
	}
}

func TestSavedViewCrudPersistsArtifactsAndActivity(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	created, err := CreateSavedView(dataDir, workspaceID, SavedIssueViewRequest{
		Name:            "Urgent customer issues",
		Query:           "export",
		Severities:      []string{"P1"},
		Labels:          []string{"customer"},
		DriftOnly:       true,
		ReviewReadyOnly: true,
	})
	if err != nil {
		t.Fatalf("create saved view: %v", err)
	}
	if created.ViewID == "" || created.Name != "Urgent customer issues" {
		t.Fatalf("unexpected created view: %#v", created)
	}

	listed, err := ListSavedViews(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list saved views: %v", err)
	}
	if len(listed) != 1 || listed[0].ViewID != created.ViewID {
		t.Fatalf("unexpected saved views: %#v", listed)
	}

	updated, err := UpdateSavedView(dataDir, workspaceID, created.ViewID, SavedIssueViewRequest{
		Name:      "Urgent queue",
		Query:     "export hotfix",
		Statuses:  []string{"verification"},
		Sources:   []string{"ledger"},
		Labels:    []string{"customer", "urgent"},
		DriftOnly: false,
	})
	if err != nil {
		t.Fatalf("update saved view: %v", err)
	}
	if updated.Name != "Urgent queue" || updated.Query != "export hotfix" {
		t.Fatalf("unexpected updated view: %#v", updated)
	}

	if err := DeleteSavedView(dataDir, workspaceID, created.ViewID); err != nil {
		t.Fatalf("delete saved view: %v", err)
	}
	remaining, err := ListSavedViews(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list remaining views: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no saved views after delete, got %#v", remaining)
	}

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsAction(content, "view.created") || !containsAction(content, "view.updated") || !containsAction(content, "view.deleted") {
		t.Fatalf("missing saved view activity entries: %s", string(content))
	}
}

func containsAction(content []byte, action string) bool {
	for _, line := range bytesSplitLines(content) {
		if len(line) == 0 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(line, &payload); err != nil {
			continue
		}
		if payload["action"] == action {
			return true
		}
	}
	return false
}

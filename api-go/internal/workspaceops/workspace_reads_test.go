package workspaceops

import "testing"

func TestWorkspaceReadHelpersReturnSnapshotArtifacts(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	snapshot, err := ReadWorkspaceSnapshot(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snapshot.Workspace.WorkspaceID != workspaceID || len(snapshot.Sources) != 1 || len(snapshot.Signals) != 1 {
		t.Fatalf("unexpected snapshot payload: %#v", snapshot)
	}

	sources, err := ReadSources(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read sources: %v", err)
	}
	if len(sources) != 1 || sources[0].SourceID != "src-ledger" {
		t.Fatalf("unexpected sources: %#v", sources)
	}

	guidance, err := ListWorkspaceGuidanceRecords(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list guidance: %v", err)
	}
	if len(guidance) == 0 || guidance[0].Path != "AGENTS.md" {
		t.Fatalf("unexpected guidance: %#v", guidance)
	}

	repoMap, err := ReadWorkspaceRepoMap(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read repo map: %v", err)
	}
	if repoMap == nil || repoMap.WorkspaceID != workspaceID {
		t.Fatalf("unexpected repo map: %#v", repoMap)
	}

	nodes, err := ListWorkspaceTree(dataDir, workspaceID, "")
	if err != nil {
		t.Fatalf("list tree: %v", err)
	}
	if len(nodes) == 0 || nodes[0].NodeType != "directory" || nodes[0].Name != "src" {
		t.Fatalf("unexpected tree nodes: %#v", nodes)
	}

	issueActivity, err := ListWorkspaceActivity(dataDir, workspaceID, issueID, "", 10)
	if err != nil {
		t.Fatalf("list issue activity: %v", err)
	}
	if len(issueActivity) != 2 {
		t.Fatalf("unexpected filtered activity: %#v", issueActivity)
	}
}

func TestReadActivityOverviewMatchesTrackerRollups(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	overview, err := ReadActivityOverview(dataDir, workspaceID, 20)
	if err != nil {
		t.Fatalf("read activity overview: %v", err)
	}
	if overview.TotalEvents != 2 || overview.UniqueActors != 2 || overview.UniqueActions != 2 {
		t.Fatalf("unexpected top-line overview: %#v", overview)
	}
	if overview.OperatorEvents != 1 || overview.SystemEvents != 1 {
		t.Fatalf("unexpected actor counts: %#v", overview)
	}
	if overview.IssuesTouched != 1 || overview.RunsTouched != 1 {
		t.Fatalf("unexpected entity touch counts: %#v", overview)
	}
	if len(overview.TopActors) == 0 || len(overview.TopActions) == 0 || len(overview.TopEntities) == 0 {
		t.Fatalf("missing rollups: %#v", overview)
	}
	if overview.MostRecentAt == nil || *overview.MostRecentAt != "2026-04-14T10:03:00Z" {
		t.Fatalf("unexpected most recent activity timestamp: %#v", overview.MostRecentAt)
	}
}

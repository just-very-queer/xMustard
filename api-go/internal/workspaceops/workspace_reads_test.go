package workspaceops

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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

func TestGoRepoIntelligenceReadsImpactContextAndRetrieval(t *testing.T) {
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)

	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "add", ".")
	runGit(t, repoRoot, "-c", "user.name=xmustard", "-c", "user.email=xmustard@example.com", "commit", "-m", "fixture")
	if err := os.WriteFile(filepath.Join(repoRoot, "src", "app.py"), []byte("class ExportService:\n    def export_summary(self):\n        return 'changed'\n"), 0o644); err != nil {
		t.Fatalf("modify source: %v", err)
	}

	impact, err := ReadImpact(dataDir, workspaceID, "HEAD")
	if err != nil {
		t.Fatalf("read impact: %v", err)
	}
	if len(impact.ChangedFiles) == 0 || len(impact.ChangedSymbols) == 0 {
		t.Fatalf("expected changed files and symbols, got %#v", impact)
	}
	if impact.Confidence == "low" {
		t.Fatalf("expected useful confidence, got %#v", impact)
	}

	context, err := ReadRepoContext(dataDir, workspaceID, "HEAD")
	if err != nil {
		t.Fatalf("read repo context: %v", err)
	}
	if context.Impact == nil || len(context.RetrievalLedger) == 0 || context.LatestAcceptedFix == nil {
		t.Fatalf("expected impact, ledger, and fix link, got %#v", context)
	}

	retrieval, err := SearchRetrieval(dataDir, workspaceID, "export summary", 5)
	if err != nil {
		t.Fatalf("search retrieval: %v", err)
	}
	if len(retrieval.Hits) == 0 || len(retrieval.RetrievalLedger) == 0 {
		t.Fatalf("expected retrieval hits and ledger, got %#v", retrieval)
	}
}

func runGit(t *testing.T, repoRoot string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

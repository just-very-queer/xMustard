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
	if err := os.WriteFile(filepath.Join(repoRoot, "package.json"), []byte(`{"scripts":{"dev":"vite","test":"vitest run"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "backend", "app"), 0o755); err != nil {
		t.Fatalf("mkdir backend/app: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "backend", "pyproject.toml"), []byte("[project]\nname = \"fixture-backend\"\n[project.scripts]\nxmustard = \"app.cli:app\"\n"), 0o644); err != nil {
		t.Fatalf("write backend pyproject.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "backend", "app", "cli.py"), []byte("def app():\n    return True\n\nif __name__ == \"__main__\":\n    app()\n"), 0o644); err != nil {
		t.Fatalf("write backend cli.py: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "rust-core", "src", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir rust-core/src/bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "rust-core", "Cargo.toml"), []byte("[package]\nname = \"fixture-core\"\nversion = \"0.1.0\"\nedition = \"2024\"\n"), 0o644); err != nil {
		t.Fatalf("write rust-core Cargo.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "rust-core", "src", "bin", "fixture-core.rs"), []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatalf("write rust-core bin: %v", err)
	}
	if err := saveVerificationProfiles(dataDir, workspaceID, []verificationProfileRecord{
		{
			ProfileID:         "backend-pytest",
			WorkspaceID:       workspaceID,
			Name:              "Backend pytest",
			Description:       "Saved verification command",
			TestCommand:       "pytest -q",
			CoverageFormat:    "unknown",
			MaxRuntimeSeconds: 60,
			RetryCount:        1,
			BuiltIn:           false,
			CreatedAt:         nowUTC(),
			UpdatedAt:         nowUTC(),
		},
	}); err != nil {
		t.Fatalf("save verification profiles: %v", err)
	}

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

	changedSymbols, err := ReadChangedSymbols(dataDir, workspaceID, "HEAD")
	if err != nil {
		t.Fatalf("read changed symbols: %v", err)
	}
	if len(changedSymbols) == 0 || changedSymbols[0].EvidenceSource != "rust_semantic_core" {
		t.Fatalf("expected Rust-backed changed symbols, got %#v", changedSymbols)
	}

	context, err := ReadRepoContext(dataDir, workspaceID, "HEAD")
	if err != nil {
		t.Fatalf("read repo context: %v", err)
	}
	if context.Impact == nil || len(context.RetrievalLedger) == 0 || context.LatestAcceptedFix == nil {
		t.Fatalf("expected impact, ledger, and fix link, got %#v", context)
	}
	if len(context.RunTargets) == 0 || len(context.VerifyTargets) == 0 {
		t.Fatalf("expected repo context targets, got %#v", context)
	}
	if !repoContextHasCommand(context.RunTargets, "cd backend && python3 -m app.cli") {
		t.Fatalf("expected pyproject repo-context target, got %#v", context.RunTargets)
	}
	if !repoContextHasCommand(context.RunTargets, "cd rust-core && cargo run --bin fixture-core") {
		t.Fatalf("expected cargo repo-context run target, got %#v", context.RunTargets)
	}
	if !repoContextHasCommand(context.VerifyTargets, "cd rust-core && cargo test") {
		t.Fatalf("expected cargo repo-context verify target, got %#v", context.VerifyTargets)
	}

	retrieval, err := SearchRetrieval(dataDir, workspaceID, "export summary", 5)
	if err != nil {
		t.Fatalf("search retrieval: %v", err)
	}
	if len(retrieval.Hits) == 0 || len(retrieval.RetrievalLedger) == 0 {
		t.Fatalf("expected retrieval hits and ledger, got %#v", retrieval)
	}

	pathSymbols, err := ReadPathSymbols(dataDir, workspaceID, "src/app.py")
	if err != nil {
		t.Fatalf("read path symbols: %v", err)
	}
	if pathSymbols.EvidenceSource != "rust_semantic_core" || len(pathSymbols.Symbols) == 0 {
		t.Fatalf("expected Rust path symbols, got %#v", pathSymbols)
	}

	explainer, err := ExplainPath(dataDir, workspaceID, "src/app.py")
	if err != nil {
		t.Fatalf("explain path: %v", err)
	}
	if explainer.EvidenceSource != "rust_semantic_core" || len(explainer.DetectedSymbols) == 0 {
		t.Fatalf("expected Rust-backed path explanation, got %#v", explainer)
	}
}

func runGit(t *testing.T, repoRoot string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func repoContextHasCommand(items []RepoContextTargetLink, command string) bool {
	for _, item := range items {
		switch target := item.Target.(type) {
		case RepoTargetRecord:
			if target.Command == command {
				return true
			}
		case map[string]any:
			if value, ok := target["command"].(string); ok && value == command {
				return true
			}
		}
	}
	return false
}

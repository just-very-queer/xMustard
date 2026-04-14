package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/rustcore"
)

func TestBuildIssueContextPacketBuildsFrontendShapeFromArtifacts(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	packet, err := BuildIssueContextPacket(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("build issue context packet: %v", err)
	}

	if packet.Issue.BugID != issueID {
		t.Fatalf("unexpected issue id: %#v", packet.Issue)
	}
	if packet.Workspace.WorkspaceID != workspaceID {
		t.Fatalf("unexpected workspace: %#v", packet.Workspace)
	}
	if len(packet.TreeFocus) == 0 || packet.TreeFocus[0] != "src/app.py" {
		t.Fatalf("unexpected tree focus: %#v", packet.TreeFocus)
	}
	if len(packet.EvidenceBundle) != 2 {
		t.Fatalf("expected evidence bundle from evidence + verification evidence, got %#v", packet.EvidenceBundle)
	}
	if len(packet.RecentFixes) != 1 || packet.RecentFixes[0].FixID != "fix-001" {
		t.Fatalf("unexpected recent fixes: %#v", packet.RecentFixes)
	}
	if len(packet.RecentActivity) != 1 || packet.RecentActivity[0].Action != "issue.updated" {
		t.Fatalf("unexpected recent activity: %#v", packet.RecentActivity)
	}
	if len(packet.Guidance) == 0 || packet.Guidance[0].Path != "AGENTS.md" {
		t.Fatalf("expected AGENTS guidance, got %#v", packet.Guidance)
	}
	if len(packet.AvailableRunbooks) == 0 || packet.AvailableRunbooks[0].RunbookID == "" {
		t.Fatalf("expected available runbooks, got %#v", packet.AvailableRunbooks)
	}
	if !containsVerificationCommand(packet.AvailableVerificationProfiles, "pytest --cov=. --cov-report=xml") {
		t.Fatalf("expected inferred pytest verification profile, got %#v", packet.AvailableVerificationProfiles)
	}
	if len(packet.TicketContexts) != 1 || packet.TicketContexts[0].ContextID != "ticket-1" {
		t.Fatalf("unexpected ticket contexts: %#v", packet.TicketContexts)
	}
	if len(packet.ThreatModels) != 1 || packet.ThreatModels[0].ThreatModelID != "threat-1" {
		t.Fatalf("unexpected threat models: %#v", packet.ThreatModels)
	}
	if packet.RepoMap == nil || len(packet.RelatedPaths) == 0 {
		t.Fatalf("expected repo map and related paths, got repo_map=%#v related=%#v", packet.RepoMap, packet.RelatedPaths)
	}
	if packet.Worktree == nil || !packet.Worktree.Available || packet.Worktree.IsGitRepo {
		t.Fatalf("expected non-git worktree status, got %#v", packet.Worktree)
	}
	if !strings.Contains(packet.Prompt, "Repository guidance:") || !strings.Contains(packet.Prompt, "Known verification profiles:") {
		t.Fatalf("prompt missing guidance or verification sections:\n%s", packet.Prompt)
	}
	if !strings.Contains(packet.Prompt, "Ticket context:") || !strings.Contains(packet.Prompt, "Threat model:") {
		t.Fatalf("prompt missing context sections:\n%s", packet.Prompt)
	}
}

func writeIssueContextFixture(t *testing.T, withRunbook bool) (string, string, string, string) {
	t.Helper()

	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	repoRoot := filepath.Join(tempDir, "repo")
	workspaceID := "workspace-1"
	issueID := "P0_25M03_001"

	if err := os.MkdirAll(filepath.Join(repoRoot, "src"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "AGENTS.md"), []byte("# AGENTS\n\n- Run pytest --cov=. --cov-report=xml before finalizing.\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "src", "app.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "src", "feature_test.py"), []byte("def test_ok():\n    assert True\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	snapshotPath := filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json")
	if err := writeJSON(snapshotPath, workspaceSnapshot{
		Workspace: workspaceRecord{
			WorkspaceID:  workspaceID,
			Name:         "repo",
			RootPath:     repoRoot,
			LatestScanAt: stringPtr("2026-04-14T10:00:00Z"),
		},
		Issues: []issueRecord{
			{
				BugID:       issueID,
				Title:       "Broken export summary",
				Severity:    "P1",
				IssueStatus: "investigating",
				Source:      "ledger",
				DocStatus:   "open",
				CodeStatus:  "broken",
				Summary:     stringPtr("Export flow drops the summary column."),
				Impact:      stringPtr("Customers lose data in CSV export."),
				Evidence: []evidenceRef{
					{Path: "src/app.py", NormalizedPath: stringPtr("src/app.py")},
				},
				VerificationEvidence: []evidenceRef{
					{Path: "coverage.xml"},
				},
				TestsAdded:       []string{},
				TestsPassed:      []string{},
				DriftFlags:       []string{"missing_verification_tests"},
				Labels:           []string{"export", "customer"},
				NeedsFollowup:    true,
				ReviewReadyCount: 1,
				ReviewReadyRuns:  []string{"run-123"},
				UpdatedAt:        "2026-04-14T10:00:00Z",
			},
		},
	}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "repo_map.json"), rustcore.RepoMapSummary{
		WorkspaceID: workspaceID,
		RootPath:    repoRoot,
		TotalFiles:  2,
		SourceFiles: 1,
		TestFiles:   1,
		TopExtensions: map[string]int{
			".py": 2,
		},
		TopDirectories: []rustcore.RepoMapDirectoryRecord{
			{Path: "src", FileCount: 2, SourceFileCount: 1, TestFileCount: 1},
		},
		KeyFiles: []rustcore.RepoMapFileRecord{
			{Path: "src/app.py", Role: "source"},
			{Path: "src/feature_test.py", Role: "test"},
		},
		GeneratedAt: "2026-04-14T10:00:00Z",
	}); err != nil {
		t.Fatalf("write repo_map: %v", err)
	}

	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "fix_records.json"), []FixRecord{
		{
			FixID:        "fix-001",
			WorkspaceID:  workspaceID,
			IssueID:      issueID,
			Status:       "draft",
			Summary:      "Guarded export summary fields.",
			Actor:        operatorActor(),
			ChangedFiles: []string{"src/app.py"},
			TestsRun:     []string{"pytest -q"},
			Evidence:     []evidenceRef{},
			UpdatedAt:    "2026-04-14T10:05:00Z",
			RecordedAt:   "2026-04-14T10:05:00Z",
		},
	}); err != nil {
		t.Fatalf("write fix records: %v", err)
	}

	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "ticket_contexts.json"), []TicketContextRecord{
		{
			ContextID:          "ticket-1",
			WorkspaceID:        workspaceID,
			IssueID:            issueID,
			Provider:           "manual",
			Title:              "Customer escalation",
			Summary:            "Keep existing CSV columns stable.",
			AcceptanceCriteria: []string{"Old columns remain", "Export works end to end"},
			CreatedAt:          "2026-04-14T10:00:00Z",
			UpdatedAt:          "2026-04-14T10:00:00Z",
		},
	}); err != nil {
		t.Fatalf("write ticket contexts: %v", err)
	}

	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "threat_models.json"), []ThreatModelRecord{
		{
			ThreatModelID: "threat-1",
			WorkspaceID:   workspaceID,
			IssueID:       issueID,
			Title:         "Export exposure",
			Methodology:   "manual",
			Summary:       "Broken export handling could expose wrong columns.",
			Assets:        []string{"CSV export"},
			AbuseCases:    []string{"Wrong column disclosure"},
			Mitigations:   []string{"Allowlist export schema"},
			Status:        "draft",
			CreatedAt:     "2026-04-14T10:00:00Z",
			UpdatedAt:     "2026-04-14T10:00:00Z",
		},
	}); err != nil {
		t.Fatalf("write threat models: %v", err)
	}

	activityPath := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	if err := os.MkdirAll(filepath.Dir(activityPath), 0o755); err != nil {
		t.Fatalf("mkdir activity dir: %v", err)
	}
	activityHandle, err := os.Create(activityPath)
	if err != nil {
		t.Fatalf("create activity: %v", err)
	}
	defer activityHandle.Close()

	activityRecords := []activityRecord{
		{
			ActivityID:  "act-1",
			WorkspaceID: workspaceID,
			EntityType:  "issue",
			EntityID:    issueID,
			Action:      "issue.updated",
			Summary:     "Updated issue severity",
			Actor:       operatorActor(),
			IssueID:     stringPtr(issueID),
			Details: map[string]any{
				"before_after": map[string]any{
					"severity": map[string]any{"from": "P2", "to": "P1"},
				},
			},
			CreatedAt: "2026-04-14T10:02:00Z",
		},
		{
			ActivityID:  "act-2",
			WorkspaceID: workspaceID,
			EntityType:  "run",
			EntityID:    "run-123",
			Action:      "run.queued",
			Summary:     "Queued run",
			Actor:       systemActor(),
			IssueID:     stringPtr(issueID),
			CreatedAt:   "2026-04-14T10:03:00Z",
		},
	}
	for _, record := range activityRecords {
		payload, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal activity: %v", err)
		}
		if _, err := activityHandle.Write(append(payload, '\n')); err != nil {
			t.Fatalf("write activity: %v", err)
		}
	}

	if withRunbook {
		if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runbooks.json"), []RunbookRecord{
			{
				RunbookID:   "focused-verify",
				WorkspaceID: workspaceID,
				Name:        "Focused verify",
				Description: "Verification only workflow",
				Scope:       "issue",
				Template:    "1. Reproduce the bug.\n2. Report scope only.",
				BuiltIn:     false,
				CreatedAt:   "2026-04-14T10:00:00Z",
				UpdatedAt:   "2026-04-14T10:00:00Z",
			},
		}); err != nil {
			t.Fatalf("write runbooks: %v", err)
		}
	}

	return dataDir, workspaceID, issueID, repoRoot
}

func containsVerificationCommand(items []rustcore.VerificationProfileInput, command string) bool {
	for _, item := range items {
		if strings.Contains(item.TestCommand, command) {
			return true
		}
	}
	return false
}

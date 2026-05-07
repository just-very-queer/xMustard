package workspaceops

import (
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/rustcore"
)

func TestReadVerificationOutcomesReturnsObservedAndDeclaredOnlyTruth(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)

	profileID := "backend-pytest"
	issueID := "P0_25M03_001"
	createdAt := "2026-05-07T12:34:56Z"
	branch := "feature/execution-truth"
	headSHA := "abc123def456"

	if err := saveVerificationProfiles(dataDir, workspaceID, []verificationProfileRecord{{
		ProfileID:          profileID,
		WorkspaceID:        workspaceID,
		Name:               "Backend pytest",
		Description:        "Observed backend verification",
		TestCommand:        "pytest -q tests/test_tracker_service.py",
		CoverageReportPath: stringPtr("backend/coverage.xml"),
		CoverageFormat:     "unknown",
		MaxRuntimeSeconds:  120,
		RetryCount:         1,
		BuiltIn:            false,
		CreatedAt:          createdAt,
		UpdatedAt:          createdAt,
	}}); err != nil {
		t.Fatalf("save verification profiles: %v", err)
	}

	if err := saveRunRecord(dataDir, runRecord{
		RunID:          "run-verify-1",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "codex",
		Model:          "gpt-5.4",
		Status:         "completed",
		Title:          "Verification lane",
		Prompt:         "Run backend verification",
		Command:        []string{"codex", "exec"},
		CommandPreview: "codex exec",
		LogPath:        filepath.Join(repoRoot, "backend", "run-verify-1.log"),
		OutputPath:     filepath.Join(repoRoot, "backend", "run-verify-1.out.json"),
		CreatedAt:      createdAt,
		CompletedAt:    stringPtr(createdAt),
		Worktree: &WorktreeStatus{
			Available: true,
			IsGitRepo: true,
			Branch:    &branch,
			HeadSHA:   &headSHA,
		},
	}); err != nil {
		t.Fatalf("save run: %v", err)
	}

	history := []rustcore.VerificationProfileResult{{
		ProfileID:   profileID,
		WorkspaceID: workspaceID,
		ExecutionID: "vpr_exec123456",
		ProfileName: "Backend pytest",
		IssueID:     stringPtr(issueID),
		RunID:       stringPtr("run-verify-1"),
		Attempts: []rustcore.VerificationCommandResult{{
			Command:       "pytest -q tests/test_tracker_service.py",
			Cwd:           filepath.Join(repoRoot, "backend"),
			ExitCode:      intPtr(0),
			Success:       true,
			TimedOut:      false,
			DurationMS:    842,
			StdoutExcerpt: "1 passed",
			CreatedAt:     createdAt,
		}},
		AttemptCount: 1,
		Success:      true,
		CoverageResult: &rustcore.CoverageResult{
			ResultID:      "cov_exec123456",
			WorkspaceID:   workspaceID,
			RunID:         stringPtr("run-verify-1"),
			IssueID:       stringPtr(issueID),
			LineCoverage:  91.2,
			LinesCovered:  114,
			LinesTotal:    125,
			FilesCovered:  8,
			FilesTotal:    9,
			Format:        "lcov",
			RawReportPath: stringPtr(filepath.Join(repoRoot, "backend", "coverage.xml")),
			CreatedAt:     createdAt,
		},
		Confidence:         "high",
		CoverageReportPath: stringPtr(filepath.Join(repoRoot, "backend", "coverage.xml")),
		CreatedAt:          createdAt,
	}}
	if err := saveVerificationProfileHistory(dataDir, workspaceID, history); err != nil {
		t.Fatalf("save verification profile history: %v", err)
	}

	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "verifications.json"), []verificationRecord{{
		VerificationID: "ver-123",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		RunID:          "run-verify-1",
		Runtime:        "codex",
		Model:          "gpt-5.4",
		CodeChecked:    "yes",
		Fixed:          "yes",
		Confidence:     "high",
		Summary:        "Observed verification succeeded",
		Tests:          []string{"pytest -q tests/test_tracker_service.py"},
		Actor:          operatorActor(),
		CreatedAt:      createdAt,
	}}); err != nil {
		t.Fatalf("write verifications: %v", err)
	}

	registry, err := ReadVerificationOutcomes(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read verification outcomes: %v", err)
	}
	if registry.WorkspaceID != workspaceID || registry.RootPath != repoRoot {
		t.Fatalf("unexpected registry metadata: %#v", registry)
	}
	if len(registry.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", registry.Warnings)
	}

	var profileItem *VerificationOutcomeRecord
	var declaredOnlyItem *VerificationOutcomeRecord
	for idx := range registry.Items {
		item := &registry.Items[idx]
		if item.Target.ProfileID != nil && *item.Target.ProfileID == profileID {
			profileItem = item
		}
		if item.Target.Source == "package_json" && item.Target.Command == "npm run test" {
			declaredOnlyItem = item
		}
	}
	if profileItem == nil {
		t.Fatalf("missing profile-backed verification outcome: %#v", registry.Items)
	}
	if profileItem.Observed.ObservationKind != "verification_profile_execution" || profileItem.Observed.MatchBasis != "profile_id_exact" {
		t.Fatalf("unexpected observation metadata: %#v", profileItem.Observed)
	}
	if profileItem.Observed.State != "success" || profileItem.Observed.LastStatus != "passed" {
		t.Fatalf("unexpected success state: %#v", profileItem.Observed)
	}
	if profileItem.Observed.CommandExecuted == nil || *profileItem.Observed.CommandExecuted != "pytest -q tests/test_tracker_service.py" {
		t.Fatalf("missing executed command: %#v", profileItem.Observed)
	}
	if profileItem.Observed.Cwd == nil || *profileItem.Observed.Cwd != filepath.Join(repoRoot, "backend") {
		t.Fatalf("missing execution cwd: %#v", profileItem.Observed)
	}
	if profileItem.Observed.RunID == nil || *profileItem.Observed.RunID != "run-verify-1" {
		t.Fatalf("missing run linkage: %#v", profileItem.Observed)
	}
	if profileItem.Observed.VerificationID == nil || *profileItem.Observed.VerificationID != "ver-123" {
		t.Fatalf("missing verification linkage: %#v", profileItem.Observed)
	}
	if profileItem.Observed.CoverageResultID == nil || *profileItem.Observed.CoverageResultID != "cov_exec123456" {
		t.Fatalf("missing coverage linkage: %#v", profileItem.Observed)
	}
	if profileItem.Observed.Branch == nil || *profileItem.Observed.Branch != branch || profileItem.Observed.HeadSHA == nil || *profileItem.Observed.HeadSHA != headSHA {
		t.Fatalf("missing worktree provenance: %#v", profileItem.Observed)
	}
	if len(profileItem.Observed.EvidenceRefs) < 4 {
		t.Fatalf("expected evidence refs, got %#v", profileItem.Observed.EvidenceRefs)
	}

	if declaredOnlyItem == nil {
		t.Fatalf("missing manifest verify target outcome: %#v", registry.Items)
	}
	if declaredOnlyItem.Observed.State != "unknown" || declaredOnlyItem.Observed.LastStatus != "never_observed" {
		t.Fatalf("expected declared-only target to stay unknown: %#v", declaredOnlyItem.Observed)
	}
	if declaredOnlyItem.Observed.CommandExecuted != nil || declaredOnlyItem.Observed.RunID != nil {
		t.Fatalf("declared-only target should not invent execution truth: %#v", declaredOnlyItem.Observed)
	}
}

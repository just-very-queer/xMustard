package workspaceops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunReviewReadHelpersUsePersistedArtifacts(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)

	run := runRecord{
		RunID:          "run-123",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "codex",
		Model:          "gpt-5.4-mini",
		Status:         "completed",
		Title:          "codex:P0_25M03_001",
		Prompt:         "prompt",
		Command:        []string{"codex", "exec"},
		CommandPreview: "codex exec",
		LogPath:        filepath.Join(repoRoot, "run.log"),
		OutputPath:     filepath.Join(repoRoot, "run.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
		Summary: map[string]any{
			"text_excerpt": "Tightened export checks in src/app.py\npytest test_export.py -q",
			"session_id":   "ses_123",
		},
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-123.json"), run); err != nil {
		t.Fatalf("write run record: %v", err)
	}

	verification := []verificationRecord{
		{
			VerificationID: "ver-1",
			WorkspaceID:    workspaceID,
			IssueID:        issueID,
			RunID:          "run-123",
			Runtime:        "codex",
			Model:          "gpt-5.4-mini",
			CodeChecked:    "yes",
			Fixed:          "yes",
			Confidence:     "high",
			Summary:        "Looks fixed",
			Evidence:       []string{"src/app.py"},
			Tests:          []string{"pytest test_export.py -q"},
			Actor:          operatorActor(),
			CreatedAt:      "2026-04-14T10:07:00Z",
		},
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "verifications.json"), verification); err != nil {
		t.Fatalf("write verifications: %v", err)
	}

	fixes, err := ListFixes(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list fixes: %v", err)
	}
	if len(fixes) != 1 || fixes[0].FixID != "fix-001" {
		t.Fatalf("unexpected fixes: %#v", fixes)
	}

	verifications, err := ListVerifications(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list verifications: %v", err)
	}
	if len(verifications) != 1 || verifications[0].VerificationID != "ver-1" {
		t.Fatalf("unexpected verifications: %#v", verifications)
	}

	draft, err := SuggestFixDraft(dataDir, workspaceID, issueID, "run-123")
	if err != nil {
		t.Fatalf("suggest fix draft: %v", err)
	}
	if draft.RunID != "run-123" || len(draft.ChangedFiles) == 0 || len(draft.TestsRun) == 0 {
		t.Fatalf("unexpected fix draft: %#v", draft)
	}

	queue, err := ListReviewQueue(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list review queue: %v", err)
	}
	if len(queue) != 1 || queue[0].Run.RunID != "run-123" || queue[0].Draft == nil {
		t.Fatalf("unexpected review queue: %#v", queue)
	}
}

func TestRecordFixPersistsArtifactAndActivity(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)

	run := runRecord{
		RunID:          "run-fix",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "codex",
		Model:          "gpt-5.4-mini",
		Status:         "completed",
		Title:          "codex:P0_25M03_001",
		Prompt:         "prompt",
		Command:        []string{"codex", "exec"},
		CommandPreview: "codex exec",
		LogPath:        filepath.Join(repoRoot, "run-fix.log"),
		OutputPath:     filepath.Join(repoRoot, "run-fix.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
		Summary: map[string]any{
			"text_excerpt": "Adjusted export flow in src/app.py",
			"session_id":   "ses_fix",
		},
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-fix.json"), run); err != nil {
		t.Fatalf("write run record: %v", err)
	}

	fix, err := RecordFix(dataDir, workspaceID, issueID, FixRecordRequest{
		Summary:      "Persisted fix",
		RunID:        stringPtr("run-fix"),
		ChangedFiles: []string{"src/app.py"},
		TestsRun:     []string{"pytest test_export.py -q"},
		IssueStatus:  stringPtr("verification"),
	})
	if err != nil {
		t.Fatalf("record fix: %v", err)
	}
	if fix.FixID == "" || fix.RunID == nil || *fix.RunID != "run-fix" {
		t.Fatalf("unexpected recorded fix: %#v", fix)
	}

	stored, err := ListFixes(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list stored fixes: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected two fixes after recording, got %#v", stored)
	}

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsAction(content, "fix.recorded") {
		t.Fatalf("missing fix.recorded activity: %s", string(content))
	}
}

func TestRunReadAndLogHelpersUsePersistedRunArtifacts(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(repoRoot, "run-live.log")
	if err := os.WriteFile(logPath, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	run := runRecord{
		RunID:          "run-live",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "codex",
		Model:          "gpt-5.4-mini",
		Status:         "running",
		Title:          "codex:P0_25M03_001",
		Prompt:         "prompt",
		Command:        []string{"codex", "exec"},
		CommandPreview: "codex exec",
		LogPath:        logPath,
		OutputPath:     filepath.Join(repoRoot, "run-live.out.json"),
		CreatedAt:      "2026-04-14T10:08:00Z",
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-live.json"), run); err != nil {
		t.Fatalf("write run record: %v", err)
	}

	runs, err := ListRuns(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "run-live" {
		t.Fatalf("unexpected runs: %#v", runs)
	}

	readRun, err := ReadRun(dataDir, workspaceID, "run-live")
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if readRun.RunID != "run-live" || readRun.Status != "running" {
		t.Fatalf("unexpected run record: %#v", readRun)
	}

	logChunk, err := ReadRunLog(dataDir, workspaceID, "run-live", 0)
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	if logChunk.Content != "line one\nline two\n" || logChunk.EOF || logChunk.Offset <= 0 {
		t.Fatalf("unexpected log chunk: %#v", logChunk)
	}
}

func TestReviewRunPersistsReviewAndSyncsSnapshot(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)

	run := runRecord{
		RunID:          "run-123",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "codex",
		Model:          "gpt-5.4-mini",
		Status:         "completed",
		Title:          "codex:P0_25M03_001",
		Prompt:         "prompt",
		Command:        []string{"codex", "exec"},
		CommandPreview: "codex exec",
		LogPath:        filepath.Join(repoRoot, "run-review.log"),
		OutputPath:     filepath.Join(repoRoot, "run-review.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-123.json"), run); err != nil {
		t.Fatalf("write run record: %v", err)
	}

	review, err := ReviewRun(dataDir, workspaceID, "run-123", RunReviewRequest{
		Disposition: "dismissed",
		Notes:       stringPtr("Looks redundant"),
	})
	if err != nil {
		t.Fatalf("review run: %v", err)
	}
	if review.RunID != "run-123" || review.Disposition != "dismissed" {
		t.Fatalf("unexpected review: %#v", review)
	}

	reviews, err := loadRunReviews(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("load reviews: %v", err)
	}
	if len(reviews) != 1 || reviews[0].RunID != "run-123" {
		t.Fatalf("unexpected reviews: %#v", reviews)
	}

	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if snapshot.Issues[0].ReviewReadyCount != 0 || len(snapshot.Issues[0].ReviewReadyRuns) != 0 {
		t.Fatalf("review queue not cleared in snapshot: %#v", snapshot.Issues[0])
	}

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsAction(content, "run.reviewed") {
		t.Fatalf("missing run.reviewed activity: %s", string(content))
	}
}

func TestAcceptRunReviewRecordsFixAndClearsReviewQueue(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)

	run := runRecord{
		RunID:          "run-123",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "codex",
		Model:          "gpt-5.4-mini",
		Status:         "completed",
		Title:          "codex:P0_25M03_001",
		Prompt:         "prompt",
		Command:        []string{"codex", "exec"},
		CommandPreview: "codex exec",
		LogPath:        filepath.Join(repoRoot, "run-accept.log"),
		OutputPath:     filepath.Join(repoRoot, "run-accept.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
		Summary: map[string]any{
			"text_excerpt": "Adjusted export flow in src/app.py\npytest test_export.py -q",
			"session_id":   "ses_accept",
		},
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run-123.json"), run); err != nil {
		t.Fatalf("write run record: %v", err)
	}

	fix, err := AcceptRunReview(dataDir, workspaceID, "run-123", RunAcceptRequest{
		IssueStatus: stringPtr("verification"),
		Notes:       stringPtr("Accepting generated patch"),
	})
	if err != nil {
		t.Fatalf("accept run review: %v", err)
	}
	if fix.RunID == nil || *fix.RunID != "run-123" {
		t.Fatalf("unexpected accepted fix: %#v", fix)
	}

	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if snapshot.Issues[0].ReviewReadyCount != 0 || snapshot.Issues[0].IssueStatus != "verification" {
		t.Fatalf("snapshot not updated after accept: %#v", snapshot.Issues[0])
	}

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsAction(content, "run.accepted") || !containsAction(content, "fix.recorded") {
		t.Fatalf("missing acceptance activity entries: %s", string(content))
	}
}

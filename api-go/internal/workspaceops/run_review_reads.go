package workspaceops

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type verificationRecord struct {
	VerificationID string        `json:"verification_id"`
	WorkspaceID    string        `json:"workspace_id"`
	IssueID        string        `json:"issue_id"`
	RunID          string        `json:"run_id"`
	Runtime        string        `json:"runtime"`
	Model          string        `json:"model"`
	CodeChecked    string        `json:"code_checked"`
	Fixed          string        `json:"fixed"`
	Confidence     string        `json:"confidence"`
	Summary        string        `json:"summary"`
	Evidence       []string      `json:"evidence"`
	Tests          []string      `json:"tests"`
	Actor          activityActor `json:"actor"`
	RawExcerpt     *string       `json:"raw_excerpt,omitempty"`
	CreatedAt      string        `json:"created_at"`
}

type reviewQueueItem struct {
	Run   runRecord           `json:"run"`
	Issue issueRecord         `json:"issue"`
	Draft *FixDraftSuggestion `json:"draft,omitempty"`
}

type FixRecordRequest struct {
	Status       string        `json:"status"`
	Summary      string        `json:"summary"`
	How          *string       `json:"how"`
	RunID        *string       `json:"run_id"`
	Runtime      *string       `json:"runtime"`
	Model        *string       `json:"model"`
	ChangedFiles []string      `json:"changed_files"`
	TestsRun     []string      `json:"tests_run"`
	Notes        *string       `json:"notes"`
	IssueStatus  *string       `json:"issue_status"`
	Evidence     []evidenceRef `json:"evidence"`
}

type FixDraftSuggestion struct {
	WorkspaceID          string   `json:"workspace_id"`
	IssueID              string   `json:"issue_id"`
	RunID                string   `json:"run_id"`
	Summary              string   `json:"summary"`
	How                  *string  `json:"how,omitempty"`
	ChangedFiles         []string `json:"changed_files"`
	TestsRun             []string `json:"tests_run"`
	SuggestedIssueStatus *string  `json:"suggested_issue_status,omitempty"`
	SourceExcerpt        *string  `json:"source_excerpt,omitempty"`
}

type RunReviewRecord struct {
	ReviewID    string        `json:"review_id"`
	WorkspaceID string        `json:"workspace_id"`
	RunID       string        `json:"run_id"`
	IssueID     string        `json:"issue_id"`
	Disposition string        `json:"disposition"`
	Actor       activityActor `json:"actor"`
	Notes       *string       `json:"notes,omitempty"`
	CreatedAt   string        `json:"created_at"`
}

type RunReviewRequest struct {
	Disposition string  `json:"disposition"`
	Notes       *string `json:"notes"`
}

type RunAcceptRequest struct {
	IssueStatus *string `json:"issue_status"`
	Notes       *string `json:"notes"`
}

type RunLogChunk struct {
	Offset  int64  `json:"offset"`
	Content string `json:"content"`
	EOF     bool   `json:"eof"`
}

func ListRuns(dataDir string, workspaceID string) ([]runRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	return listRuns(dataDir, workspaceID)
}

func ReadRun(dataDir string, workspaceID string, runID string) (*runRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	return loadRun(dataDir, workspaceID, runID)
}

func ReadRunLog(dataDir string, workspaceID string, runID string, offset int64) (*RunLogChunk, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	eof := run.Status == "completed" || run.Status == "failed" || run.Status == "cancelled"
	if strings.TrimSpace(run.LogPath) == "" {
		return &RunLogChunk{Offset: offset, Content: "", EOF: eof}, nil
	}
	handle, err := os.Open(run.LogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &RunLogChunk{Offset: offset, Content: "", EOF: eof}, nil
		}
		return nil, err
	}
	defer handle.Close()
	if _, err := handle.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	content, err := io.ReadAll(handle)
	if err != nil {
		return nil, err
	}
	nextOffset, err := handle.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	return &RunLogChunk{
		Offset:  nextOffset,
		Content: string(content),
		EOF:     eof,
	}, nil
}

func ListFixes(dataDir string, workspaceID string, issueID string) ([]FixRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	return loadFixRecords(dataDir, workspaceID, issueID)
}

func ListVerifications(dataDir string, workspaceID string, issueID string) ([]verificationRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "workspaces", workspaceID, "verifications.json")
	var records []verificationRecord
	if err := readJSON(path, &records); err != nil {
		if os.IsNotExist(err) {
			return []verificationRecord{}, nil
		}
		return nil, err
	}
	if issueID != "" {
		filtered := records[:0]
		for _, item := range records {
			if item.IssueID == issueID {
				filtered = append(filtered, item)
			}
		}
		records = filtered
	}
	slices.SortFunc(records, func(a, b verificationRecord) int {
		if a.CreatedAt > b.CreatedAt {
			return -1
		}
		if a.CreatedAt < b.CreatedAt {
			return 1
		}
		return 0
	})
	return records, nil
}

func ListReviewQueue(dataDir string, workspaceID string) ([]reviewQueueItem, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	issues := map[string]issueRecord{}
	reviewIDs := map[string]struct{}{}
	for _, issue := range snapshot.Issues {
		issues[issue.BugID] = issue
		for _, runID := range issue.ReviewReadyRuns {
			reviewIDs[runID] = struct{}{}
		}
	}

	runs, err := listRuns(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	items := []reviewQueueItem{}
	for _, run := range runs {
		if _, ok := reviewIDs[run.RunID]; !ok {
			continue
		}
		issue, ok := issues[run.IssueID]
		if !ok {
			continue
		}
		var draft *FixDraftSuggestion
		if suggested, err := SuggestFixDraft(dataDir, workspaceID, run.IssueID, run.RunID); err == nil {
			draft = suggested
		}
		items = append(items, reviewQueueItem{
			Run:   run,
			Issue: issue,
			Draft: draft,
		})
	}
	slices.SortFunc(items, func(a, b reviewQueueItem) int {
		if a.Run.CreatedAt > b.Run.CreatedAt {
			return -1
		}
		if a.Run.CreatedAt < b.Run.CreatedAt {
			return 1
		}
		return 0
	})
	return items, nil
}

func RecordFix(dataDir string, workspaceID string, issueID string, request FixRecordRequest) (*FixRecord, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}

	var run *runRecord
	if request.RunID != nil && strings.TrimSpace(*request.RunID) != "" {
		loaded, err := loadRun(dataDir, workspaceID, strings.TrimSpace(*request.RunID))
		if err != nil {
			return nil, err
		}
		run = loaded
	}

	worktree := readWorktreeStatusFromRunOrRepo(run, filepath.Join(dataDir, "workspaces", workspaceID))
	actor := operatorActor()
	var sessionID *string
	if run != nil {
		actor = activityActor{
			Kind:    "agent",
			Name:    run.Runtime,
			Runtime: ptr(run.Runtime),
			Model:   ptr(run.Model),
			Key:     "agent:" + run.Runtime + ":" + run.Model,
			Label:   run.Runtime + ":" + run.Model,
		}
		if run.Summary != nil {
			if rawSessionID, ok := run.Summary["session_id"].(string); ok && strings.TrimSpace(rawSessionID) != "" {
				sessionID = ptr(strings.TrimSpace(rawSessionID))
			}
		}
	} else if request.Runtime != nil && request.Model != nil {
		actor = activityActor{
			Kind:    "agent",
			Name:    strings.TrimSpace(*request.Runtime),
			Runtime: trimOptional(request.Runtime),
			Model:   trimOptional(request.Model),
			Key:     "agent:" + strings.TrimSpace(*request.Runtime) + ":" + strings.TrimSpace(*request.Model),
			Label:   strings.TrimSpace(*request.Runtime) + ":" + strings.TrimSpace(*request.Model),
		}
	}

	fixes, err := loadFixRecords(dataDir, workspaceID, "")
	if err != nil {
		return nil, err
	}
	fixID := nextFixID(workspaceID, issueID, len(fixes)+1)
	summary := strings.TrimSpace(request.Summary)
	evidence := append([]evidenceRef{}, request.Evidence...)
	if run != nil && run.Summary != nil {
		if excerpt, ok := run.Summary["text_excerpt"].(string); ok && strings.TrimSpace(excerpt) != "" {
			evidence = append(evidence, evidenceRef{Path: run.OutputPath, Excerpt: ptr(strings.TrimSpace(excerpt)[:min(len(strings.TrimSpace(excerpt)), 280)])})
		}
	}

	fix := FixRecord{
		FixID:        fixID,
		WorkspaceID:  workspaceID,
		IssueID:      issueID,
		Status:       fallbackString(strings.TrimSpace(request.Status), "proposed"),
		Summary:      summary,
		How:          trimOptional(request.How),
		Actor:        actor,
		RunID:        trimOptional(request.RunID),
		SessionID:    sessionID,
		ChangedFiles: dedupeSortedStrings(request.ChangedFiles),
		TestsRun:     trimStringList(request.TestsRun, 12),
		Evidence:     evidence,
		Worktree:     worktree,
		Notes:        trimOptional(request.Notes),
		UpdatedAt:    nowUTC(),
		RecordedAt:   nowUTC(),
	}
	fixes = append(fixes, fix)
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "fix_records.json"), fixes); err != nil {
		return nil, err
	}
	if request.IssueStatus != nil && strings.TrimSpace(*request.IssueStatus) != "" {
		if _, err := UpdateIssue(dataDir, workspaceID, issueID, IssueUpdateRequest{IssueStatus: request.IssueStatus}); err != nil {
			return nil, err
		}
	}
	if err := syncReviewReadySnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	if err := appendIssueActivity(dataDir, workspaceID, issueID, firstNonEmptyPtr(request.RunID), "fix.recorded", "Recorded fix "+fix.FixID+" for "+issueID, map[string]any{
		"status":        fix.Status,
		"run_id":        request.RunID,
		"changed_files": fix.ChangedFiles,
		"tests_run":     fix.TestsRun,
	}); err != nil {
		return nil, err
	}
	return &fix, nil
}

func SuggestFixDraft(dataDir string, workspaceID string, issueID string, runID string) (*FixDraftSuggestion, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	run, err := loadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	if run.IssueID != issueID {
		return nil, fmt.Errorf("run %s does not belong to issue %s", runID, issueID)
	}

	worktree := readWorktreeStatus(filepath.Dir(filepath.Dir(run.LogPath)))
	excerpt := runExcerpt(run)
	summary := draftSummaryFromExcerpt(excerpt, issueID, runID)
	testsRun := extractTestCommands(excerpt)
	changedFiles := extractChangedFiles(excerpt)
	if len(changedFiles) == 0 && worktree != nil && worktree.Available {
		changedFiles = append([]string{}, worktree.DirtyPaths[:min(len(worktree.DirtyPaths), 8)]...)
	}
	suggestedIssueStatus := "in_progress"
	if run.Status == "completed" {
		suggestedIssueStatus = "verification"
	}
	return &FixDraftSuggestion{
		WorkspaceID:          workspaceID,
		IssueID:              issueID,
		RunID:                runID,
		Summary:              summary,
		How:                  optionalString(excerpt),
		ChangedFiles:         changedFiles,
		TestsRun:             testsRun,
		SuggestedIssueStatus: ptr(suggestedIssueStatus),
		SourceExcerpt:        optionalString(excerpt),
	}, nil
}

func ReviewRun(dataDir string, workspaceID string, runID string, request RunReviewRequest) (*RunReviewRecord, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	if run.IssueID == "workspace-query" {
		return nil, fmt.Errorf("workspace query runs cannot be reviewed as issue work")
	}
	if run.Status != "completed" {
		return nil, fmt.Errorf("only completed issue runs can be reviewed")
	}

	reviews, err := loadRunReviews(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	filtered := reviews[:0]
	for _, item := range reviews {
		if item.RunID != runID {
			filtered = append(filtered, item)
		}
	}
	disposition := strings.TrimSpace(request.Disposition)
	if disposition != "dismissed" && disposition != "investigation_only" {
		return nil, fmt.Errorf("invalid disposition: %s", disposition)
	}
	review := RunReviewRecord{
		ReviewID:    newRunReviewID(workspaceID, runID, disposition),
		WorkspaceID: workspaceID,
		RunID:       runID,
		IssueID:     run.IssueID,
		Disposition: disposition,
		Actor:       operatorActor(),
		Notes:       trimOptional(request.Notes),
		CreatedAt:   nowUTC(),
	}
	filtered = append(filtered, review)
	if err := saveRunReviews(dataDir, workspaceID, filtered); err != nil {
		return nil, err
	}
	if err := syncReviewReadySnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	if err := appendRunActivity(dataDir, workspaceID, run.IssueID, runID, "run.reviewed", "Marked run "+runID+" as "+disposition, map[string]any{
		"disposition": disposition,
		"notes":       review.Notes,
	}); err != nil {
		return nil, err
	}
	return &review, nil
}

func AcceptRunReview(dataDir string, workspaceID string, runID string, request RunAcceptRequest) (*FixRecord, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	if run.IssueID == "workspace-query" {
		return nil, fmt.Errorf("workspace query runs cannot be accepted as fixes")
	}
	existingFixes, err := loadFixRecords(dataDir, workspaceID, "")
	if err != nil {
		return nil, err
	}
	for _, item := range existingFixes {
		if item.RunID != nil && strings.TrimSpace(*item.RunID) == runID {
			return nil, fmt.Errorf("run %s already has a recorded fix", runID)
		}
	}
	draft, err := SuggestFixDraft(dataDir, workspaceID, run.IssueID, runID)
	if err != nil {
		return nil, err
	}
	issueStatus := trimOptional(request.IssueStatus)
	if issueStatus == nil {
		issueStatus = draft.SuggestedIssueStatus
	}
	if issueStatus == nil {
		issueStatus = ptr("verification")
	}
	fix, err := RecordFix(dataDir, workspaceID, run.IssueID, FixRecordRequest{
		Summary:      draft.Summary,
		How:          draft.How,
		RunID:        ptr(runID),
		ChangedFiles: draft.ChangedFiles,
		TestsRun:     draft.TestsRun,
		Notes:        request.Notes,
		IssueStatus:  issueStatus,
	})
	if err != nil {
		return nil, err
	}
	if err := appendRunActivity(dataDir, workspaceID, run.IssueID, runID, "run.accepted", "Accepted run "+runID+" into fix "+fix.FixID, map[string]any{
		"fix_id":       fix.FixID,
		"issue_status": issueStatus,
	}); err != nil {
		return nil, err
	}
	return fix, nil
}

func runExcerpt(run *runRecord) string {
	if run == nil || run.Summary == nil {
		return ""
	}
	if excerpt, ok := run.Summary["text_excerpt"].(string); ok {
		return strings.TrimSpace(excerpt)
	}
	return ""
}

func draftSummaryFromExcerpt(excerpt string, issueID string, runID string) string {
	for _, rawLine := range strings.Split(excerpt, "\n") {
		line := strings.TrimSpace(strings.TrimLeft(rawLine, "-*0123456789. "))
		if line == "" {
			continue
		}
		lowered := strings.ToLower(line)
		if strings.HasPrefix(lowered, "pytest") || strings.HasPrefix(lowered, "npm test") || strings.HasPrefix(lowered, "pnpm test") || strings.HasPrefix(lowered, "yarn test") || strings.HasPrefix(lowered, "cargo test") || strings.HasPrefix(lowered, "go test") {
			continue
		}
		if len(line) > 160 {
			return line[:160]
		}
		return line
	}
	return fmt.Sprintf("Review run %s result for %s", runID, issueID)
}

func extractChangedFiles(text string) []string {
	pattern := regexp.MustCompile(`\b(?:[A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]+\.(?:py|ts|tsx|js|jsx|go|rs|md|json|yaml|yml|css)\b`)
	files := []string{}
	seen := map[string]struct{}{}
	for _, match := range pattern.FindAllString(text, -1) {
		if _, exists := seen[match]; exists {
			continue
		}
		seen[match] = struct{}{}
		files = append(files, match)
		if len(files) >= 8 {
			break
		}
	}
	return files
}

func nextFixID(workspaceID string, issueID string, ordinal int) string {
	digest := sha1.Sum([]byte(fmt.Sprintf("%s:%s:%d:%s", workspaceID, issueID, ordinal, nowUTC())))
	return "fix_" + strings.ToLower(issueID) + "_" + hex.EncodeToString(digest[:])[:8]
}

func readWorktreeStatusFromRunOrRepo(run *runRecord, workspaceDir string) *WorktreeStatus {
	if run != nil && run.Worktree != nil && run.Worktree.Available {
		return run.Worktree
	}
	snapshotPath := filepath.Join(workspaceDir, "snapshot.json")
	var snapshot workspaceSnapshot
	if err := readJSON(snapshotPath, &snapshot); err == nil {
		return readWorktreeStatus(snapshot.Workspace.RootPath)
	}
	return &WorktreeStatus{Available: false, IsGitRepo: false, DirtyPaths: []string{}}
}

func optionalString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func loadRunReviews(dataDir string, workspaceID string) ([]RunReviewRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "run_reviews.json")
	var items []RunReviewRecord
	if err := readJSON(path, &items); err != nil {
		if os.IsNotExist(err) {
			return []RunReviewRecord{}, nil
		}
		return nil, err
	}
	slices.SortFunc(items, func(a, b RunReviewRecord) int {
		if a.CreatedAt > b.CreatedAt {
			return -1
		}
		if a.CreatedAt < b.CreatedAt {
			return 1
		}
		return 0
	})
	return items, nil
}

func saveRunReviews(dataDir string, workspaceID string, items []RunReviewRecord) error {
	slices.SortFunc(items, func(a, b RunReviewRecord) int {
		if a.CreatedAt > b.CreatedAt {
			return -1
		}
		if a.CreatedAt < b.CreatedAt {
			return 1
		}
		return 0
	})
	return writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "run_reviews.json"), items)
}

func appendRunActivity(dataDir string, workspaceID string, issueID string, runID string, action string, summary string, details map[string]any) error {
	createdAt := nowUTC()
	record := activityRecord{
		ActivityID:  hashID(workspaceID, "run", runID, action, createdAt),
		WorkspaceID: workspaceID,
		EntityType:  "run",
		EntityID:    runID,
		Action:      action,
		Summary:     summary,
		Actor:       operatorActor(),
		Details:     details,
		CreatedAt:   createdAt,
	}
	if strings.TrimSpace(issueID) != "" {
		record.IssueID = &issueID
	}
	record.RunID = &runID
	path := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer handle.Close()
	payload, err := jsonMarshal(record)
	if err != nil {
		return err
	}
	if _, err := handle.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}

func syncReviewReadySnapshot(dataDir string, workspaceID string) error {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return err
	}
	runs, err := listRuns(dataDir, workspaceID)
	if err != nil {
		return err
	}
	fixes, err := loadFixRecords(dataDir, workspaceID, "")
	if err != nil {
		return err
	}
	reviews, err := loadRunReviews(dataDir, workspaceID)
	if err != nil {
		return err
	}

	fixedRunIDs := map[string]struct{}{}
	for _, fix := range fixes {
		if fix.RunID != nil && strings.TrimSpace(*fix.RunID) != "" {
			fixedRunIDs[strings.TrimSpace(*fix.RunID)] = struct{}{}
		}
	}
	reviewedRunIDs := map[string]struct{}{}
	for _, review := range reviews {
		reviewedRunIDs[review.RunID] = struct{}{}
	}
	pendingByIssue := map[string][]string{}
	for _, run := range runs {
		if run.IssueID == "workspace-query" || run.Status != "completed" {
			continue
		}
		if _, exists := fixedRunIDs[run.RunID]; exists {
			continue
		}
		if _, exists := reviewedRunIDs[run.RunID]; exists {
			continue
		}
		pendingByIssue[run.IssueID] = append(pendingByIssue[run.IssueID], run.RunID)
	}
	reviewReadyTotal := 0
	reviewQueueTotal := 0
	for idx := range snapshot.Issues {
		pending := pendingByIssue[snapshot.Issues[idx].BugID]
		snapshot.Issues[idx].ReviewReadyCount = len(pending)
		snapshot.Issues[idx].ReviewReadyRuns = append([]string{}, pending[:min(len(pending), 8)]...)
		if len(pending) > 0 {
			reviewReadyTotal++
			reviewQueueTotal += len(pending)
		}
	}
	if snapshot.Summary == nil {
		snapshot.Summary = map[string]int{}
	}
	snapshot.Summary["review_ready_total"] = reviewReadyTotal
	snapshot.Summary["review_queue_total"] = reviewQueueTotal
	snapshot.GeneratedAt = nowUTC()
	return writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json"), snapshot)
}

func newRunReviewID(workspaceID string, runID string, disposition string) string {
	stamp := nowUTC()
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err == nil {
		digest := sha1.Sum([]byte(fmt.Sprintf("%s:%s:%s:%s:%s", workspaceID, runID, disposition, stamp, hex.EncodeToString(buffer))))
		return hex.EncodeToString(digest[:])[:16]
	}
	digest := sha1.Sum([]byte(fmt.Sprintf("%s:%s:%s:%s", workspaceID, runID, disposition, stamp)))
	return hex.EncodeToString(digest[:])[:16]
}

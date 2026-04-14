package workspaceops

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"xmustard/api-go/internal/rustcore"
)

type workspaceSnapshot struct {
	Workspace workspaceRecord `json:"workspace"`
	Issues    []issueRecord   `json:"issues"`
}

type workspaceRecord struct {
	WorkspaceID  string  `json:"workspace_id"`
	Name         string  `json:"name"`
	RootPath     string  `json:"root_path"`
	LatestScanAt *string `json:"latest_scan_at"`
}

type issueRecord struct {
	BugID                string        `json:"bug_id"`
	Title                string        `json:"title"`
	Severity             string        `json:"severity"`
	IssueStatus          string        `json:"issue_status"`
	Source               string        `json:"source"`
	SourceDoc            *string       `json:"source_doc"`
	DocStatus            string        `json:"doc_status"`
	CodeStatus           string        `json:"code_status"`
	Summary              *string       `json:"summary"`
	Impact               *string       `json:"impact"`
	Evidence             []evidenceRef `json:"evidence"`
	VerificationEvidence []evidenceRef `json:"verification_evidence"`
	TestsAdded           []string      `json:"tests_added"`
	TestsPassed          []string      `json:"tests_passed"`
	DriftFlags           []string      `json:"drift_flags"`
	Labels               []string      `json:"labels"`
	Notes                *string       `json:"notes"`
	VerifiedAt           *string       `json:"verified_at"`
	VerifiedBy           *string       `json:"verified_by"`
	NeedsFollowup        bool          `json:"needs_followup"`
	ReviewReadyCount     int           `json:"review_ready_count"`
	ReviewReadyRuns      []string      `json:"review_ready_runs"`
	UpdatedAt            string        `json:"updated_at"`
}

type evidenceRef struct {
	Path           string  `json:"path"`
	Line           *int    `json:"line,omitempty"`
	Excerpt        *string `json:"excerpt,omitempty"`
	NormalizedPath *string `json:"normalized_path,omitempty"`
	PathExists     *bool   `json:"path_exists,omitempty"`
	PathScope      *string `json:"path_scope,omitempty"`
}

type verificationProfileRecord = rustcore.VerificationProfileInput

type activityRecord struct {
	ActivityID  string         `json:"activity_id"`
	WorkspaceID string         `json:"workspace_id"`
	EntityType  string         `json:"entity_type"`
	EntityID    string         `json:"entity_id"`
	Action      string         `json:"action"`
	Summary     string         `json:"summary"`
	Actor       activityActor  `json:"actor"`
	IssueID     *string        `json:"issue_id,omitempty"`
	RunID       *string        `json:"run_id,omitempty"`
	Details     map[string]any `json:"details"`
	CreatedAt   string         `json:"created_at"`
}

type activityActor struct {
	Kind    string  `json:"kind"`
	Name    string  `json:"name"`
	Runtime *string `json:"runtime,omitempty"`
	Model   *string `json:"model,omitempty"`
	Key     string  `json:"key"`
	Label   string  `json:"label"`
}

func RunIssueVerificationProfile(
	ctx context.Context,
	dataDir string,
	workspaceID string,
	issueID string,
	profileID string,
	runID string,
) (*rustcore.VerificationProfileResult, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	snapshotPath := filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json")

	issueIndex := -1
	for idx := range snapshot.Issues {
		if snapshot.Issues[idx].BugID == issueID {
			issueIndex = idx
			break
		}
	}
	if issueIndex == -1 {
		return nil, os.ErrNotExist
	}

	profilesPath := filepath.Join(dataDir, "workspaces", workspaceID, "verification_profiles.json")
	var profiles []verificationProfileRecord
	if err := readJSON(profilesPath, &profiles); err != nil {
		return nil, fmt.Errorf("load verification profiles: %w", err)
	}

	var profile *verificationProfileRecord
	for idx := range profiles {
		if profiles[idx].ProfileID == profileID {
			profile = &profiles[idx]
			break
		}
	}
	if profile == nil {
		return nil, os.ErrNotExist
	}

	result, err := rustcore.RunVerificationProfile(ctx, snapshot.Workspace.RootPath, *profile, runID, issueID)
	if err != nil {
		return nil, err
	}

	if result.CoverageResult != nil {
		if err := saveCoverageResult(dataDir, result.CoverageResult); err != nil {
			return nil, err
		}
		if err := appendActivity(dataDir, workspaceID, issueID, runID, "coverage.parsed",
			fmt.Sprintf("Parsed coverage report: %.1f%% lines", result.CoverageResult.LineCoverage),
			map[string]any{
				"profile_id":    profile.ProfileID,
				"line_coverage": result.CoverageResult.LineCoverage,
				"files_covered": result.CoverageResult.FilesCovered,
			},
		); err != nil {
			return nil, err
		}
	}

	issue := snapshot.Issues[issueIndex]
	if result.Success {
		issue.TestsPassed = appendUnique(issue.TestsPassed, profile.TestCommand)
	}
	if result.CoverageReportPath != nil && *result.CoverageReportPath != "" {
		relativePath := relativeToWorkspace(snapshot.Workspace.RootPath, *result.CoverageReportPath)
		evidence := evidenceRef{
			Path: relativePath,
		}
		evidence.NormalizedPath = &relativePath
		pathExists := fileExists(*result.CoverageReportPath)
		evidence.PathExists = &pathExists
		pathScope := "repo-relative"
		evidence.PathScope = &pathScope
		if result.CoverageResult != nil {
			excerpt := fmt.Sprintf(
				"coverage %.2f%% (%d/%d lines)",
				result.CoverageResult.LineCoverage,
				result.CoverageResult.LinesCovered,
				result.CoverageResult.LinesTotal,
			)
			evidence.Excerpt = &excerpt
		}
		issue.VerificationEvidence = append(issue.VerificationEvidence, evidence)
	}
	issue.UpdatedAt = nowUTC()
	snapshot.Issues[issueIndex] = issue
	if err := writeJSON(snapshotPath, snapshot); err != nil {
		return nil, fmt.Errorf("save snapshot: %w", err)
	}

	if err := appendActivity(dataDir, workspaceID, issueID, runID, "verification.profile_run",
		fmt.Sprintf("Ran verification profile %s (%s)", profile.Name, map[bool]string{true: "passed", false: "failed"}[result.Success]),
		map[string]any{
			"profile_id":           profile.ProfileID,
			"attempt_count":        result.AttemptCount,
			"success":              result.Success,
			"coverage_report_path": result.CoverageReportPath,
		},
	); err != nil {
		return nil, err
	}

	return result, nil
}

func readJSON(path string, target any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func writeJSON(path string, payload any) error {
	content, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return os.Rename(tempPath, path)
}

func saveCoverageResult(dataDir string, result *rustcore.CoverageResult) error {
	path := filepath.Join(dataDir, "coverage", result.ResultID+".json")
	return writeJSON(path, result)
}

func appendActivity(dataDir string, workspaceID string, issueID string, runID string, action string, summary string, details map[string]any) error {
	createdAt := nowUTC()
	record := activityRecord{
		ActivityID:  hashID(workspaceID, "issue", issueID, action, createdAt),
		WorkspaceID: workspaceID,
		EntityType:  "issue",
		EntityID:    issueID,
		Action:      action,
		Summary:     summary,
		Actor:       systemActor(),
		Details:     details,
		CreatedAt:   createdAt,
	}
	record.IssueID = &issueID
	if runID != "" {
		record.RunID = &runID
	}

	path := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer handle.Close()

	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := handle.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}

func appendUnique(items []string, value string) []string {
	if value == "" || slices.Contains(items, value) {
		return items
	}
	return append(items, value)
}

func relativeToWorkspace(workspaceRoot string, target string) string {
	relative, err := filepath.Rel(workspaceRoot, target)
	if err != nil {
		return target
	}
	return relative
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func hashID(parts ...string) string {
	hash := sha1.Sum([]byte(fmt.Sprint(parts)))
	return hex.EncodeToString(hash[:])[:16]
}

func systemActor() activityActor {
	return activityActor{
		Kind:  "system",
		Name:  "system",
		Key:   "system:system",
		Label: "system",
	}
}

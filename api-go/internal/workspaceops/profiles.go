package workspaceops

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"xmustard/api-go/internal/rustcore"
)

type VerificationProfileUpsertRequest struct {
	ProfileID          *string  `json:"profile_id"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	TestCommand        string   `json:"test_command"`
	CoverageCommand    *string  `json:"coverage_command"`
	CoverageReportPath *string  `json:"coverage_report_path"`
	CoverageFormat     string   `json:"coverage_format"`
	MaxRuntimeSeconds  int64    `json:"max_runtime_seconds"`
	RetryCount         int64    `json:"retry_count"`
	SourcePaths        []string `json:"source_paths"`
	ChecklistItems     []string `json:"checklist_items"`
}

type VerificationProfileReport struct {
	ProfileID         string             `json:"profile_id"`
	WorkspaceID       string             `json:"workspace_id"`
	ProfileName       string             `json:"profile_name"`
	BuiltIn           bool               `json:"built_in"`
	IssueID           *string            `json:"issue_id,omitempty"`
	TotalRuns         int                `json:"total_runs"`
	SuccessRuns       int                `json:"success_runs"`
	FailedRuns        int                `json:"failed_runs"`
	SuccessRate       float64            `json:"success_rate"`
	ConfidenceCounts  map[string]int     `json:"confidence_counts"`
	AvgAttemptCount   float64            `json:"avg_attempt_count"`
	ChecklistPassRate float64            `json:"checklist_pass_rate"`
	LastRunAt         *string            `json:"last_run_at,omitempty"`
	LastIssueID       *string            `json:"last_issue_id,omitempty"`
	LastRunID         *string            `json:"last_run_id,omitempty"`
	LastConfidence    *string            `json:"last_confidence,omitempty"`
	LastSuccess       *bool              `json:"last_success,omitempty"`
	RuntimeBreakdown  []DimensionSummary `json:"runtime_breakdown"`
	ModelBreakdown    []DimensionSummary `json:"model_breakdown"`
	BranchBreakdown   []DimensionSummary `json:"branch_breakdown"`
}

type DimensionSummary struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	TotalRuns   int     `json:"total_runs"`
	SuccessRuns int     `json:"success_runs"`
	FailedRuns  int     `json:"failed_runs"`
	SuccessRate float64 `json:"success_rate"`
	LastRunAt   *string `json:"last_run_at,omitempty"`
}

func ListVerificationProfiles(dataDir string, workspaceID string) ([]rustcore.VerificationProfileInput, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}

	saved, err := loadSavedVerificationProfiles(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	merged := map[string]rustcore.VerificationProfileInput{
		"manual-check": defaultVerificationProfile(workspaceID),
	}
	for _, profile := range saved {
		merged[profile.ProfileID] = profile
	}

	profiles := make([]rustcore.VerificationProfileInput, 0, len(merged))
	for _, profile := range merged {
		profiles = append(profiles, profile)
	}
	slices.SortFunc(profiles, func(a, b rustcore.VerificationProfileInput) int {
		if a.BuiltIn != b.BuiltIn {
			if a.BuiltIn {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Name)
		right := strings.ToLower(b.Name)
		if left == right {
			if a.CreatedAt == b.CreatedAt {
				return 0
			}
			if a.CreatedAt < b.CreatedAt {
				return -1
			}
			return 1
		}
		if left < right {
			return -1
		}
		return 1
	})
	return profiles, nil
}

func ListVerificationProfileReports(dataDir string, workspaceID string, issueID string) ([]VerificationProfileReport, error) {
	profiles, err := ListVerificationProfiles(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	history, err := ListVerificationProfileHistory(dataDir, workspaceID, "", issueID)
	if err != nil {
		return nil, err
	}
	historyByProfile := map[string][]rustcore.VerificationProfileResult{}
	runLookup := map[string]*runRecord{}
	for _, item := range history {
		historyByProfile[item.ProfileID] = append(historyByProfile[item.ProfileID], item)
		if item.RunID != nil {
			runID := strings.TrimSpace(*item.RunID)
			if runID != "" {
				if _, ok := runLookup[runID]; !ok {
					run, loadErr := loadRun(dataDir, workspaceID, runID)
					if loadErr == nil {
						runLookup[runID] = run
					} else {
						runLookup[runID] = nil
					}
				}
			}
		}
	}
	reports := make([]VerificationProfileReport, 0, len(profiles))
	for _, profile := range profiles {
		records := historyByProfile[profile.ProfileID]
		totalRuns := len(records)
		successRuns := 0
		totalAttempts := 0
		checklistTotal := 0
		checklistPassed := 0
		confidenceCounts := map[string]int{"high": 0, "medium": 0, "low": 0}
		for _, item := range records {
			if item.Success {
				successRuns++
			}
			totalAttempts += item.AttemptCount
			confidenceCounts[item.Confidence] = confidenceCounts[item.Confidence] + 1
			checklistTotal += len(item.ChecklistResults)
			for _, check := range item.ChecklistResults {
				if check.Passed {
					checklistPassed++
				}
			}
		}
		failedRuns := totalRuns - successRuns
		successRate := 0.0
		avgAttemptCount := 0.0
		checklistPassRate := 0.0
		if totalRuns > 0 {
			successRate = roundOneDecimal((float64(successRuns) / float64(totalRuns)) * 100)
			avgAttemptCount = roundTwoDecimals(float64(totalAttempts) / float64(totalRuns))
		}
		if checklistTotal > 0 {
			checklistPassRate = roundOneDecimal((float64(checklistPassed) / float64(checklistTotal)) * 100)
		}
		var latest *rustcore.VerificationProfileResult
		if len(records) > 0 {
			latest = &records[0]
		}
		var issueFilter *string
		if issueID != "" {
			issueFilter = &issueID
		}
		report := VerificationProfileReport{
			ProfileID:         profile.ProfileID,
			WorkspaceID:       workspaceID,
			ProfileName:       profile.Name,
			BuiltIn:           profile.BuiltIn,
			IssueID:           issueFilter,
			TotalRuns:         totalRuns,
			SuccessRuns:       successRuns,
			FailedRuns:        failedRuns,
			SuccessRate:       successRate,
			ConfidenceCounts:  confidenceCounts,
			AvgAttemptCount:   avgAttemptCount,
			ChecklistPassRate: checklistPassRate,
		}
		if latest != nil {
			report.LastRunAt = &latest.CreatedAt
			report.LastIssueID = latest.IssueID
			report.LastRunID = latest.RunID
			report.LastConfidence = &latest.Confidence
			report.LastSuccess = &latest.Success
		}
		report.RuntimeBreakdown = buildVerificationDimensionBreakdown(records, runLookup, func(item rustcore.VerificationProfileResult, run *runRecord) (string, string) {
			if run != nil {
				return run.Runtime, run.Runtime
			}
			return "manual", "manual"
		})
		report.ModelBreakdown = buildVerificationDimensionBreakdown(records, runLookup, func(item rustcore.VerificationProfileResult, run *runRecord) (string, string) {
			if run != nil {
				return run.Model, run.Model
			}
			return "manual", "manual"
		})
		report.BranchBreakdown = buildVerificationDimensionBreakdown(records, runLookup, func(item rustcore.VerificationProfileResult, run *runRecord) (string, string) {
			if run != nil && run.Worktree != nil && run.Worktree.Branch != nil && strings.TrimSpace(*run.Worktree.Branch) != "" {
				return *run.Worktree.Branch, *run.Worktree.Branch
			}
			return "unknown", "unknown"
		})
		reports = append(reports, report)
	}
	return reports, nil
}

func buildVerificationDimensionBreakdown(
	records []rustcore.VerificationProfileResult,
	runLookup map[string]*runRecord,
	resolver func(item rustcore.VerificationProfileResult, run *runRecord) (string, string),
) []DimensionSummary {
	buckets := map[string]*DimensionSummary{}
	for _, item := range records {
		var run *runRecord
		if item.RunID != nil {
			run = runLookup[strings.TrimSpace(*item.RunID)]
		}
		key, label := resolver(item, run)
		if strings.TrimSpace(key) == "" {
			key = "unknown"
		}
		if strings.TrimSpace(label) == "" {
			label = key
		}
		current, ok := buckets[key]
		if !ok {
			current = &DimensionSummary{Key: key, Label: label}
			buckets[key] = current
		}
		current.TotalRuns++
		if item.Success {
			current.SuccessRuns++
		} else {
			current.FailedRuns++
		}
		current.SuccessRate = roundOneDecimal((float64(current.SuccessRuns) / float64(current.TotalRuns)) * 100)
		if current.LastRunAt == nil || item.CreatedAt > *current.LastRunAt {
			value := item.CreatedAt
			current.LastRunAt = &value
		}
	}
	summaries := make([]DimensionSummary, 0, len(buckets))
	for _, item := range buckets {
		summaries = append(summaries, *item)
	}
	slices.SortFunc(summaries, func(a, b DimensionSummary) int {
		if a.TotalRuns != b.TotalRuns {
			if a.TotalRuns > b.TotalRuns {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Label)
		right := strings.ToLower(b.Label)
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
		return 0
	})
	return summaries
}

func SaveVerificationProfile(dataDir string, workspaceID string, request VerificationProfileUpsertRequest) (*rustcore.VerificationProfileInput, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}

	saved, err := loadSavedVerificationProfiles(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	profileID := slugProfileID(request.Name)
	if request.ProfileID != nil && strings.TrimSpace(*request.ProfileID) != "" {
		profileID = strings.TrimSpace(*request.ProfileID)
	}
	now := nowUTC()

	filtered := saved[:0]
	for _, profile := range saved {
		if profile.ProfileID != profileID {
			filtered = append(filtered, profile)
		}
	}
	saved = filtered

	profile := rustcore.VerificationProfileInput{
		ProfileID:          profileID,
		WorkspaceID:        workspaceID,
		Name:               strings.TrimSpace(request.Name),
		Description:        strings.TrimSpace(request.Description),
		TestCommand:        strings.TrimSpace(request.TestCommand),
		CoverageCommand:    trimOptional(request.CoverageCommand),
		CoverageReportPath: trimOptional(request.CoverageReportPath),
		CoverageFormat:     fallbackString(strings.TrimSpace(request.CoverageFormat), "unknown"),
		MaxRuntimeSeconds:  maxInt64(1, request.MaxRuntimeSeconds),
		RetryCount:         maxInt64(0, request.RetryCount),
		SourcePaths:        trimStringList(request.SourcePaths, 8),
		ChecklistItems:     trimStringList(request.ChecklistItems, 12),
		BuiltIn:            false,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	saved = append(saved, profile)
	if err := saveVerificationProfiles(dataDir, workspaceID, saved); err != nil {
		return nil, err
	}
	if err := appendSettingsActivity(dataDir, workspaceID, "verification-profile:"+profileID, "verification_profile.saved",
		"Saved verification profile "+profile.Name,
		map[string]any{
			"profile_id":      profileID,
			"coverage_format": profile.CoverageFormat,
		},
	); err != nil {
		return nil, err
	}
	return &profile, nil
}

func DeleteVerificationProfile(dataDir string, workspaceID string, profileID string) error {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return err
	}

	saved, err := loadSavedVerificationProfiles(dataDir, workspaceID)
	if err != nil {
		return err
	}
	remaining := saved[:0]
	found := false
	for _, profile := range saved {
		if profile.ProfileID == profileID {
			found = true
			continue
		}
		remaining = append(remaining, profile)
	}
	if !found {
		return os.ErrNotExist
	}
	if err := saveVerificationProfiles(dataDir, workspaceID, remaining); err != nil {
		return err
	}
	return appendSettingsActivity(
		dataDir,
		workspaceID,
		"verification-profile:"+profileID,
		"verification_profile.deleted",
		"Deleted verification profile "+profileID,
		map[string]any{"profile_id": profileID},
	)
}

func loadSavedVerificationProfiles(dataDir string, workspaceID string) ([]rustcore.VerificationProfileInput, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "verification_profiles.json")
	var profiles []rustcore.VerificationProfileInput
	if err := readJSON(path, &profiles); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []rustcore.VerificationProfileInput{}, nil
		}
		return nil, err
	}
	return profiles, nil
}

func saveVerificationProfiles(dataDir string, workspaceID string, profiles []rustcore.VerificationProfileInput) error {
	slices.SortFunc(profiles, func(a, b rustcore.VerificationProfileInput) int {
		if a.BuiltIn != b.BuiltIn {
			if a.BuiltIn {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Name)
		right := strings.ToLower(b.Name)
		if left == right {
			if a.CreatedAt == b.CreatedAt {
				return 0
			}
			if a.CreatedAt < b.CreatedAt {
				return -1
			}
			return 1
		}
		if left < right {
			return -1
		}
		return 1
	})
	path := filepath.Join(dataDir, "workspaces", workspaceID, "verification_profiles.json")
	return writeJSON(path, profiles)
}

func defaultVerificationProfile(workspaceID string) rustcore.VerificationProfileInput {
	now := nowUTC()
	return rustcore.VerificationProfileInput{
		ProfileID:         "manual-check",
		WorkspaceID:       workspaceID,
		Name:              "Manual verification",
		Description:       "Fallback profile when no repo-specific test command could be inferred yet.",
		TestCommand:       "Document the exact command or test flow required for this repo.",
		CoverageFormat:    "unknown",
		MaxRuntimeSeconds: 30,
		RetryCount:        1,
		SourcePaths:       []string{},
		BuiltIn:           true,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func appendSettingsActivity(dataDir string, workspaceID string, entityID string, action string, summary string, details map[string]any) error {
	createdAt := nowUTC()
	record := activityRecord{
		ActivityID:  hashID(workspaceID, "settings", entityID, action, createdAt),
		WorkspaceID: workspaceID,
		EntityType:  "settings",
		EntityID:    entityID,
		Action:      action,
		Summary:     summary,
		Actor:       systemActor(),
		Details:     details,
		CreatedAt:   createdAt,
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
	payload, err := jsonMarshal(record)
	if err != nil {
		return err
	}
	if _, err := handle.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}

func slugProfileID(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	re := regexp.MustCompile(`[^a-z0-9]+`)
	slug := strings.Trim(re.ReplaceAllString(normalized, "-"), "-")
	if slug != "" {
		return slug
	}
	sum := sha1.Sum([]byte(name))
	return "profile-" + hex.EncodeToString(sum[:])[:8]
}

func trimOptional(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func trimStringList(values []string, limit int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func fallbackString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func maxInt64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func roundOneDecimal(value float64) float64 {
	return math.Round(value*10) / 10
}

func roundTwoDecimals(value float64) float64 {
	return math.Round(value*100) / 100
}

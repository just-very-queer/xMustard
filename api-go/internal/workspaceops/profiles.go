package workspaceops

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
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

package workspaceops

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type ThreatModelUpsertRequest struct {
	ThreatModelID   *string  `json:"threat_model_id"`
	Title           string   `json:"title"`
	Methodology     string   `json:"methodology"`
	Summary         string   `json:"summary"`
	Assets          []string `json:"assets"`
	EntryPoints     []string `json:"entry_points"`
	TrustBoundaries []string `json:"trust_boundaries"`
	AbuseCases      []string `json:"abuse_cases"`
	Mitigations     []string `json:"mitigations"`
	References      []string `json:"references"`
	Status          string   `json:"status"`
}

type ThreatModelRecord struct {
	ThreatModelID   string   `json:"threat_model_id"`
	WorkspaceID     string   `json:"workspace_id"`
	IssueID         string   `json:"issue_id"`
	Title           string   `json:"title"`
	Methodology     string   `json:"methodology"`
	Summary         string   `json:"summary"`
	Assets          []string `json:"assets"`
	EntryPoints     []string `json:"entry_points"`
	TrustBoundaries []string `json:"trust_boundaries"`
	AbuseCases      []string `json:"abuse_cases"`
	Mitigations     []string `json:"mitigations"`
	References      []string `json:"references"`
	Status          string   `json:"status"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

func ListThreatModels(dataDir string, workspaceID string, issueID string) ([]ThreatModelRecord, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	models, err := loadThreatModels(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	filtered := make([]ThreatModelRecord, 0, len(models))
	for _, model := range models {
		if model.IssueID == issueID {
			filtered = append(filtered, model)
		}
	}
	return filtered, nil
}

func SaveThreatModel(dataDir string, workspaceID string, issueID string, request ThreatModelUpsertRequest) (*ThreatModelRecord, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	models, err := loadThreatModels(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	methodology := fallbackString(strings.TrimSpace(request.Methodology), "manual")
	threatModelID := slugProfileID("threat-" + methodology + "-" + strings.TrimSpace(request.Title))
	if request.ThreatModelID != nil && strings.TrimSpace(*request.ThreatModelID) != "" {
		threatModelID = strings.TrimSpace(*request.ThreatModelID)
	}
	now := nowUTC()
	var previous *ThreatModelRecord
	remaining := models[:0]
	for _, model := range models {
		if model.ThreatModelID == threatModelID {
			existing := model
			previous = &existing
			continue
		}
		remaining = append(remaining, model)
	}

	record := ThreatModelRecord{
		ThreatModelID:   threatModelID,
		WorkspaceID:     workspaceID,
		IssueID:         issueID,
		Title:           strings.TrimSpace(request.Title),
		Methodology:     methodology,
		Summary:         strings.TrimSpace(request.Summary),
		Assets:          trimStringList(request.Assets, 12),
		EntryPoints:     trimStringList(request.EntryPoints, 12),
		TrustBoundaries: trimStringList(request.TrustBoundaries, 12),
		AbuseCases:      trimStringList(request.AbuseCases, 12),
		Mitigations:     trimStringList(request.Mitigations, 12),
		References:      trimStringList(request.References, 12),
		Status:          fallbackString(strings.TrimSpace(request.Status), "draft"),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if previous != nil {
		record.CreatedAt = previous.CreatedAt
	}

	remaining = append(remaining, record)
	if err := saveThreatModels(dataDir, workspaceID, remaining); err != nil {
		return nil, err
	}
	if err := appendIssueActivity(
		dataDir,
		workspaceID,
		issueID,
		"",
		"threat_model.saved",
		"Saved threat model "+record.Title,
		map[string]any{
			"threat_model_id": threatModelID,
			"methodology":     record.Methodology,
			"status":          record.Status,
		},
	); err != nil {
		return nil, err
	}
	return &record, nil
}

func DeleteThreatModel(dataDir string, workspaceID string, issueID string, threatModelID string) error {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return err
	}
	models, err := loadThreatModels(dataDir, workspaceID)
	if err != nil {
		return err
	}
	found := false
	remaining := models[:0]
	for _, model := range models {
		if model.ThreatModelID == threatModelID {
			found = true
			continue
		}
		remaining = append(remaining, model)
	}
	if !found {
		return os.ErrNotExist
	}
	if err := saveThreatModels(dataDir, workspaceID, remaining); err != nil {
		return err
	}
	return appendIssueActivity(
		dataDir,
		workspaceID,
		issueID,
		"",
		"threat_model.deleted",
		"Deleted threat model "+threatModelID,
		map[string]any{"threat_model_id": threatModelID},
	)
}

func loadThreatModels(dataDir string, workspaceID string) ([]ThreatModelRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "threat_models.json")
	var models []ThreatModelRecord
	if err := readJSON(path, &models); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ThreatModelRecord{}, nil
		}
		return nil, err
	}
	slices.SortFunc(models, func(a, b ThreatModelRecord) int {
		if a.IssueID != b.IssueID {
			if a.IssueID < b.IssueID {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Title)
		right := strings.ToLower(b.Title)
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
		if a.CreatedAt < b.CreatedAt {
			return -1
		}
		if a.CreatedAt > b.CreatedAt {
			return 1
		}
		return 0
	})
	return models, nil
}

func saveThreatModels(dataDir string, workspaceID string, models []ThreatModelRecord) error {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "threat_models.json")
	return writeJSON(path, models)
}

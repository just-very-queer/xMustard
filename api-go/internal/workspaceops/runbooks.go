package workspaceops

import (
	"os"
	"path/filepath"
	"strings"
)

type RunbookUpsertRequest struct {
	RunbookID   *string `json:"runbook_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Scope       string  `json:"scope"`
	Template    string  `json:"template"`
}

func ListRunbooks(dataDir string, workspaceID string) ([]RunbookRecord, error) {
	return listRunbooks(dataDir, workspaceID)
}

func SaveRunbook(dataDir string, workspaceID string, request RunbookUpsertRequest) (*RunbookRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	existing, err := loadSavedRunbooks(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	runbookID := slugProfileID(strings.TrimSpace(request.Name))
	if request.RunbookID != nil && strings.TrimSpace(*request.RunbookID) != "" {
		runbookID = strings.TrimSpace(*request.RunbookID)
	}
	now := nowUTC()
	var previous *RunbookRecord
	remaining := existing[:0]
	for _, item := range existing {
		if item.RunbookID == runbookID {
			copy := item
			previous = &copy
			continue
		}
		remaining = append(remaining, item)
	}

	record := RunbookRecord{
		RunbookID:   runbookID,
		WorkspaceID: workspaceID,
		Name:        strings.TrimSpace(request.Name),
		Description: strings.TrimSpace(request.Description),
		Scope:       fallbackString(strings.TrimSpace(request.Scope), "issue"),
		Template:    strings.TrimSpace(request.Template),
		BuiltIn:     false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if previous != nil {
		record.CreatedAt = previous.CreatedAt
	}

	remaining = append(remaining, record)
	if err := saveRunbooks(dataDir, workspaceID, remaining); err != nil {
		return nil, err
	}
	if err := appendSettingsActivity(
		dataDir,
		workspaceID,
		"runbook:"+runbookID,
		"runbook.saved",
		"Saved runbook "+record.Name,
		map[string]any{"runbook_id": runbookID, "scope": record.Scope},
	); err != nil {
		return nil, err
	}
	return &record, nil
}

func DeleteRunbook(dataDir string, workspaceID string, runbookID string) error {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return err
	}
	existing, err := loadSavedRunbooks(dataDir, workspaceID)
	if err != nil {
		return err
	}

	found := false
	remaining := existing[:0]
	for _, item := range existing {
		if item.RunbookID == runbookID {
			found = true
			continue
		}
		remaining = append(remaining, item)
	}
	if !found {
		return os.ErrNotExist
	}
	if err := saveRunbooks(dataDir, workspaceID, remaining); err != nil {
		return err
	}
	return appendSettingsActivity(
		dataDir,
		workspaceID,
		"runbook:"+runbookID,
		"runbook.deleted",
		"Deleted runbook "+runbookID,
		map[string]any{"runbook_id": runbookID},
	)
}

func saveRunbooks(dataDir string, workspaceID string, runbooks []RunbookRecord) error {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "runbooks.json")
	return writeJSON(path, runbooks)
}

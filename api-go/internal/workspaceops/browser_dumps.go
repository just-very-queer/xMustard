package workspaceops

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type BrowserDumpUpsertRequest struct {
	DumpID          *string  `json:"dump_id"`
	Source          *string  `json:"source"`
	Label           string   `json:"label"`
	PageURL         *string  `json:"page_url"`
	PageTitle       *string  `json:"page_title"`
	Summary         *string  `json:"summary"`
	DOMSnapshot     *string  `json:"dom_snapshot"`
	ConsoleMessages []string `json:"console_messages"`
	NetworkRequests []string `json:"network_requests"`
	ScreenshotPath  *string  `json:"screenshot_path"`
	Notes           *string  `json:"notes"`
}

type BrowserDumpRecord struct {
	DumpID          string   `json:"dump_id"`
	WorkspaceID     string   `json:"workspace_id"`
	IssueID         string   `json:"issue_id"`
	Source          string   `json:"source"`
	Label           string   `json:"label"`
	PageURL         *string  `json:"page_url"`
	PageTitle       *string  `json:"page_title"`
	Summary         string   `json:"summary"`
	DOMSnapshot     string   `json:"dom_snapshot"`
	ConsoleMessages []string `json:"console_messages"`
	NetworkRequests []string `json:"network_requests"`
	ScreenshotPath  *string  `json:"screenshot_path"`
	Notes           *string  `json:"notes"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

func ListBrowserDumps(dataDir string, workspaceID string, issueID string) ([]BrowserDumpRecord, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	items, err := loadBrowserDumps(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	filtered := make([]BrowserDumpRecord, 0, len(items))
	for _, item := range items {
		if item.IssueID == issueID {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func SaveBrowserDump(dataDir string, workspaceID string, issueID string, request BrowserDumpUpsertRequest) (*BrowserDumpRecord, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}

	items, err := loadBrowserDumps(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	source := "manual"
	if request.Source != nil && strings.TrimSpace(*request.Source) != "" {
		source = strings.TrimSpace(*request.Source)
	}
	dumpID := slugProfileID("browser-" + strings.TrimSpace(request.Label))
	if request.DumpID != nil && strings.TrimSpace(*request.DumpID) != "" {
		dumpID = strings.TrimSpace(*request.DumpID)
	}

	now := nowUTC()
	var previous *BrowserDumpRecord
	remaining := items[:0]
	for _, item := range items {
		if item.DumpID == dumpID {
			existing := item
			previous = &existing
			continue
		}
		remaining = append(remaining, item)
	}

	record := BrowserDumpRecord{
		DumpID:          dumpID,
		WorkspaceID:     workspaceID,
		IssueID:         issueID,
		Source:          source,
		Label:           strings.TrimSpace(request.Label),
		PageURL:         trimOptional(request.PageURL),
		PageTitle:       trimOptional(request.PageTitle),
		Summary:         firstNonEmptyString(firstNonEmptyPtr(request.Summary)),
		DOMSnapshot:     firstNonEmptyString(firstNonEmptyPtr(request.DOMSnapshot)),
		ConsoleMessages: trimStringList(request.ConsoleMessages, 20),
		NetworkRequests: trimStringList(request.NetworkRequests, 20),
		ScreenshotPath:  trimOptional(request.ScreenshotPath),
		Notes:           trimOptional(request.Notes),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if previous != nil {
		record.CreatedAt = previous.CreatedAt
	}

	remaining = append(remaining, record)
	if err := saveBrowserDumps(dataDir, workspaceID, remaining); err != nil {
		return nil, err
	}
	if err := appendIssueActivity(
		dataDir,
		workspaceID,
		issueID,
		"",
		"browser_dump.saved",
		"Saved browser dump "+record.Label,
		map[string]any{
			"dump_id":  record.DumpID,
			"source":   record.Source,
			"page_url": record.PageURL,
		},
	); err != nil {
		return nil, err
	}
	return &record, nil
}

func DeleteBrowserDump(dataDir string, workspaceID string, issueID string, dumpID string) error {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return err
	}
	items, err := loadBrowserDumps(dataDir, workspaceID)
	if err != nil {
		return err
	}

	found := false
	remaining := items[:0]
	for _, item := range items {
		if item.DumpID == dumpID {
			found = true
			continue
		}
		remaining = append(remaining, item)
	}
	if !found {
		return os.ErrNotExist
	}
	if err := saveBrowserDumps(dataDir, workspaceID, remaining); err != nil {
		return err
	}
	return appendIssueActivity(
		dataDir,
		workspaceID,
		issueID,
		"",
		"browser_dump.deleted",
		"Deleted browser dump "+dumpID,
		map[string]any{"dump_id": dumpID},
	)
}

func loadBrowserDumps(dataDir string, workspaceID string) ([]BrowserDumpRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "browser_dumps.json")
	var items []BrowserDumpRecord
	if err := readJSON(path, &items); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []BrowserDumpRecord{}, nil
		}
		return nil, err
	}
	slices.SortFunc(items, func(a, b BrowserDumpRecord) int {
		if a.UpdatedAt > b.UpdatedAt {
			return -1
		}
		if a.UpdatedAt < b.UpdatedAt {
			return 1
		}
		return 0
	})
	return items, nil
}

func saveBrowserDumps(dataDir string, workspaceID string, items []BrowserDumpRecord) error {
	slices.SortFunc(items, func(a, b BrowserDumpRecord) int {
		if a.UpdatedAt > b.UpdatedAt {
			return -1
		}
		if a.UpdatedAt < b.UpdatedAt {
			return 1
		}
		return 0
	})
	path := filepath.Join(dataDir, "workspaces", workspaceID, "browser_dumps.json")
	return writeJSON(path, items)
}

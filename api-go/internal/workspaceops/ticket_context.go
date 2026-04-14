package workspaceops

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type TicketContextUpsertRequest struct {
	ContextID          *string  `json:"context_id"`
	Provider           string   `json:"provider"`
	ExternalID         *string  `json:"external_id"`
	Title              string   `json:"title"`
	Summary            string   `json:"summary"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Links              []string `json:"links"`
	Labels             []string `json:"labels"`
	Status             *string  `json:"status"`
	SourceExcerpt      *string  `json:"source_excerpt"`
}

type TicketContextRecord struct {
	ContextID          string   `json:"context_id"`
	WorkspaceID        string   `json:"workspace_id"`
	IssueID            string   `json:"issue_id"`
	Provider           string   `json:"provider"`
	ExternalID         *string  `json:"external_id"`
	Title              string   `json:"title"`
	Summary            string   `json:"summary"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Links              []string `json:"links"`
	Labels             []string `json:"labels"`
	Status             *string  `json:"status"`
	SourceExcerpt      *string  `json:"source_excerpt"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

func ListTicketContexts(dataDir string, workspaceID string, issueID string) ([]TicketContextRecord, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	contexts, err := loadTicketContexts(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	filtered := make([]TicketContextRecord, 0, len(contexts))
	for _, context := range contexts {
		if context.IssueID == issueID {
			filtered = append(filtered, context)
		}
	}
	return filtered, nil
}

func SaveTicketContext(dataDir string, workspaceID string, issueID string, request TicketContextUpsertRequest) (*TicketContextRecord, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}

	contexts, err := loadTicketContexts(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	provider := fallbackString(strings.TrimSpace(request.Provider), "manual")
	contextID := slugProfileID(provider + "-" + firstNonEmptyPtr(request.ExternalID) + firstNonEmptyString(request.Title))
	if request.ContextID != nil && strings.TrimSpace(*request.ContextID) != "" {
		contextID = strings.TrimSpace(*request.ContextID)
	}

	now := nowUTC()
	var previous *TicketContextRecord
	remaining := contexts[:0]
	for _, context := range contexts {
		if context.ContextID == contextID {
			existing := context
			previous = &existing
			continue
		}
		remaining = append(remaining, context)
	}

	record := TicketContextRecord{
		ContextID:          contextID,
		WorkspaceID:        workspaceID,
		IssueID:            issueID,
		Provider:           provider,
		ExternalID:         trimOptional(request.ExternalID),
		Title:              strings.TrimSpace(request.Title),
		Summary:            strings.TrimSpace(request.Summary),
		AcceptanceCriteria: trimStringList(request.AcceptanceCriteria, 12),
		Links:              trimStringList(request.Links, 8),
		Labels:             trimStringList(request.Labels, 12),
		Status:             trimOptional(request.Status),
		SourceExcerpt:      trimOptional(request.SourceExcerpt),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if previous != nil {
		record.CreatedAt = previous.CreatedAt
	}

	remaining = append(remaining, record)
	if err := saveTicketContexts(dataDir, workspaceID, remaining); err != nil {
		return nil, err
	}
	if err := appendIssueActivity(
		dataDir,
		workspaceID,
		issueID,
		"",
		"ticket_context.saved",
		"Saved ticket context "+record.Title,
		map[string]any{
			"context_id":  contextID,
			"provider":    record.Provider,
			"external_id": record.ExternalID,
		},
	); err != nil {
		return nil, err
	}
	return &record, nil
}

func DeleteTicketContext(dataDir string, workspaceID string, issueID string, contextID string) error {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return err
	}
	contexts, err := loadTicketContexts(dataDir, workspaceID)
	if err != nil {
		return err
	}

	found := false
	remaining := contexts[:0]
	for _, context := range contexts {
		if context.ContextID == contextID {
			found = true
			continue
		}
		remaining = append(remaining, context)
	}
	if !found {
		return os.ErrNotExist
	}
	if err := saveTicketContexts(dataDir, workspaceID, remaining); err != nil {
		return err
	}
	return appendIssueActivity(
		dataDir,
		workspaceID,
		issueID,
		"",
		"ticket_context.deleted",
		"Deleted ticket context "+contextID,
		map[string]any{"context_id": contextID},
	)
}

func loadTicketContexts(dataDir string, workspaceID string) ([]TicketContextRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "ticket_contexts.json")
	var contexts []TicketContextRecord
	if err := readJSON(path, &contexts); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []TicketContextRecord{}, nil
		}
		return nil, err
	}
	slices.SortFunc(contexts, func(a, b TicketContextRecord) int {
		if a.IssueID != b.IssueID {
			if a.IssueID < b.IssueID {
				return -1
			}
			return 1
		}
		if a.Provider != b.Provider {
			if a.Provider < b.Provider {
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
	return contexts, nil
}

func saveTicketContexts(dataDir string, workspaceID string, contexts []TicketContextRecord) error {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "ticket_contexts.json")
	return writeJSON(path, contexts)
}

func requireIssue(dataDir string, workspaceID string, issueID string) (*issueRecord, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, issue := range snapshot.Issues {
		if issue.BugID == issueID {
			copy := issue
			return &copy, nil
		}
	}
	return nil, os.ErrNotExist
}

func appendIssueActivity(dataDir string, workspaceID string, issueID string, runID string, action string, summary string, details map[string]any) error {
	createdAt := nowUTC()
	record := activityRecord{
		ActivityID:  hashID(workspaceID, "issue", issueID, action, createdAt),
		WorkspaceID: workspaceID,
		EntityType:  "issue",
		EntityID:    issueID,
		Action:      action,
		Summary:     summary,
		Actor:       operatorActor(),
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
	payload, err := jsonMarshal(record)
	if err != nil {
		return err
	}
	if _, err := handle.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}

func firstNonEmptyPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func firstNonEmptyString(value string) string {
	return strings.TrimSpace(value)
}

func operatorActor() activityActor {
	name := "operator"
	return activityActor{
		Kind:  "operator",
		Name:  name,
		Key:   "operator:operator",
		Label: name,
	}
}

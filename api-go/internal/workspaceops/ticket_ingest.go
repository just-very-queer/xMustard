package workspaceops

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Inbound ticket ingestion: pull external ticket detail (Jira/Linear/etc.) into
// an issue, extracting acceptance criteria so code changes can be judged against
// the product expectation. Complements the existing outbound sync.

type IngestTicketRequest struct {
	Provider           string   `json:"provider"`
	ExternalID         string   `json:"external_id"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	URL                string   `json:"url"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

type IngestedTicket struct {
	IssueID            string   `json:"issue_id"`
	WorkspaceID        string   `json:"workspace_id"`
	Provider           string   `json:"provider"`
	ExternalID         string   `json:"external_id"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	URL                string   `json:"url,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	IngestedAt         string   `json:"ingested_at"`
}

func ingestedTicketPath(dataDir, workspaceID, issueID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "ingested_tickets", issueID+".json")
}

// extractAcceptanceCriteria pulls criteria-looking lines out of a description:
// checkbox/bullet lists and "AC:"-prefixed lines.
func extractAcceptanceCriteria(description string) []string {
	out := []string{}
	for _, raw := range strings.Split(description, "\n") {
		line := strings.TrimSpace(raw)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(line, "- [ ]"), strings.HasPrefix(line, "- [x]"):
			out = append(out, strings.TrimSpace(line[5:]))
		case strings.HasPrefix(lower, "ac:"):
			out = append(out, strings.TrimSpace(line[3:]))
		case strings.HasPrefix(lower, "acceptance criteria:"):
			out = append(out, strings.TrimSpace(line[len("acceptance criteria:"):]))
		}
	}
	cleaned := []string{}
	for _, c := range out {
		if strings.TrimSpace(c) != "" {
			cleaned = append(cleaned, c)
		}
	}
	return cleaned
}

// IngestTicket stores inbound ticket detail against an issue.
func IngestTicket(dataDir, workspaceID, issueID string, req IngestTicketRequest) (*IngestedTicket, error) {
	if _, _, err := findIssueRecord(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	criteria := cleanStrings(req.AcceptanceCriteria)
	if len(criteria) == 0 {
		criteria = extractAcceptanceCriteria(req.Description)
	}
	ticket := IngestedTicket{
		IssueID:            issueID,
		WorkspaceID:        workspaceID,
		Provider:           strings.TrimSpace(req.Provider),
		ExternalID:         strings.TrimSpace(req.ExternalID),
		Title:              strings.TrimSpace(req.Title),
		Description:        req.Description,
		URL:                strings.TrimSpace(req.URL),
		AcceptanceCriteria: criteria,
		IngestedAt:         nowUTC(),
	}
	if err := writeJSON(ingestedTicketPath(dataDir, workspaceID, issueID), ticket); err != nil {
		return nil, err
	}
	_ = recordAuditEventNoGuard(dataDir, workspaceID, AuditEvent{
		Action:     "ticket.ingested",
		TargetType: "issue",
		TargetID:   issueID,
		Details:    fmt.Sprintf("provider=%s external_id=%s acceptance_criteria=%d", ticket.Provider, ticket.ExternalID, len(criteria)),
	})
	return &ticket, nil
}

// GetIngestedTicket returns the stored inbound ticket for an issue.
func GetIngestedTicket(dataDir, workspaceID, issueID string) (*IngestedTicket, error) {
	if _, _, err := findIssueRecord(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	var ticket IngestedTicket
	if err := readJSON(ingestedTicketPath(dataDir, workspaceID, issueID), &ticket); err != nil {
		return nil, err
	}
	return &ticket, nil
}

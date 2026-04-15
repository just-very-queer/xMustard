package workspaceops

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type IssueUpdateRequest struct {
	Severity      *string  `json:"severity"`
	IssueStatus   *string  `json:"issue_status"`
	DocStatus     *string  `json:"doc_status"`
	CodeStatus    *string  `json:"code_status"`
	Labels        []string `json:"labels"`
	Notes         *string  `json:"notes"`
	NeedsFollowup *bool    `json:"needs_followup"`
}

type IssueCreateRequest struct {
	BugID         *string  `json:"bug_id"`
	Title         string   `json:"title"`
	Severity      string   `json:"severity"`
	Summary       *string  `json:"summary"`
	Impact        *string  `json:"impact"`
	IssueStatus   string   `json:"issue_status"`
	DocStatus     string   `json:"doc_status"`
	CodeStatus    string   `json:"code_status"`
	Labels        []string `json:"labels"`
	Notes         *string  `json:"notes"`
	SourceDoc     *string  `json:"source_doc"`
	NeedsFollowup bool     `json:"needs_followup"`
}

type SavedIssueView struct {
	ViewID          string   `json:"view_id"`
	WorkspaceID     string   `json:"workspace_id"`
	Name            string   `json:"name"`
	Query           string   `json:"query"`
	Severities      []string `json:"severities"`
	Statuses        []string `json:"statuses"`
	Sources         []string `json:"sources"`
	Labels          []string `json:"labels"`
	DriftOnly       bool     `json:"drift_only"`
	NeedsFollowup   *bool    `json:"needs_followup,omitempty"`
	ReviewReadyOnly bool     `json:"review_ready_only"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

type SavedIssueViewRequest struct {
	Name            string   `json:"name"`
	Query           string   `json:"query"`
	Severities      []string `json:"severities"`
	Statuses        []string `json:"statuses"`
	Sources         []string `json:"sources"`
	Labels          []string `json:"labels"`
	DriftOnly       bool     `json:"drift_only"`
	NeedsFollowup   *bool    `json:"needs_followup"`
	ReviewReadyOnly bool     `json:"review_ready_only"`
}

func CreateIssue(dataDir string, workspaceID string, request IssueCreateRequest) (*issueRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	tracked, err := loadTrackerIssues(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	bugID := strings.TrimSpace(firstNonEmptyPtr(request.BugID))
	if bugID == "" {
		bugID = nextTrackerIssueID(tracked)
	}
	for _, item := range tracked {
		if item.BugID == bugID {
			return nil, fmt.Errorf("issue already exists: %s", bugID)
		}
	}
	for _, item := range snapshot.Issues {
		if item.BugID == bugID {
			return nil, fmt.Errorf("issue already exists: %s", bugID)
		}
	}

	now := nowUTC()
	fingerprint := sha1.Sum([]byte(fmt.Sprintf("%s:tracker:%s", workspaceID, bugID)))
	fingerprintHex := hex.EncodeToString(fingerprint[:])
	issue := issueRecord{
		BugID:                bugID,
		Title:                strings.TrimSpace(request.Title),
		Severity:             strings.ToUpper(strings.TrimSpace(request.Severity)),
		IssueStatus:          fallbackString(strings.TrimSpace(request.IssueStatus), "open"),
		Source:               "tracker",
		SourceDoc:            trimOptional(request.SourceDoc),
		DocStatus:            "open",
		CodeStatus:           "unknown",
		Summary:              trimOptional(request.Summary),
		Impact:               trimOptional(request.Impact),
		Evidence:             []evidenceRef{},
		VerificationEvidence: []evidenceRef{},
		TestsAdded:           []string{},
		TestsPassed:          []string{},
		DriftFlags:           []string{},
		Labels:               dedupeSortedStrings(request.Labels),
		Notes:                trimOptional(request.Notes),
		NeedsFollowup:        request.NeedsFollowup,
		ReviewReadyRuns:      []string{},
		Fingerprint:          &fingerprintHex,
		UpdatedAt:            now,
	}

	tracked = append(tracked, issue)
	if err := saveTrackerIssues(dataDir, workspaceID, tracked); err != nil {
		return nil, err
	}
	snapshot.Issues = append(snapshot.Issues, issue)
	if snapshot.Summary == nil {
		snapshot.Summary = map[string]int{}
	}
	snapshot.Summary["issues"] = len(snapshot.Issues)
	snapshot.GeneratedAt = now
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json"), snapshot); err != nil {
		return nil, err
	}
	if err := appendIssueActivity(
		dataDir,
		workspaceID,
		bugID,
		"",
		"issue.created",
		"Created tracker issue "+bugID,
		map[string]any{"source": "tracker", "severity": issue.Severity, "title": issue.Title},
	); err != nil {
		return nil, err
	}
	return &issue, nil
}

func UpdateIssue(dataDir string, workspaceID string, issueID string, request IssueUpdateRequest) (*issueRecord, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	index := -1
	for idx := range snapshot.Issues {
		if snapshot.Issues[idx].BugID == issueID {
			index = idx
			break
		}
	}
	if index == -1 {
		return nil, os.ErrNotExist
	}

	target := snapshot.Issues[index]
	updated := target
	updated.UpdatedAt = nowUTC()
	changedFields := map[string]any{}
	beforeAfter := map[string]any{}

	if request.Severity != nil {
		value := strings.ToUpper(strings.TrimSpace(*request.Severity))
		updated.Severity = value
		changedFields["severity"] = value
		if target.Severity != value {
			beforeAfter["severity"] = map[string]any{"from": target.Severity, "to": value}
		}
	}
	if request.IssueStatus != nil {
		value := strings.TrimSpace(*request.IssueStatus)
		updated.IssueStatus = value
		changedFields["issue_status"] = value
		if target.IssueStatus != value {
			beforeAfter["issue_status"] = map[string]any{"from": target.IssueStatus, "to": value}
		}
	}
	if request.DocStatus != nil {
		value := strings.TrimSpace(*request.DocStatus)
		updated.DocStatus = value
		changedFields["doc_status"] = value
		if target.DocStatus != value {
			beforeAfter["doc_status"] = map[string]any{"from": target.DocStatus, "to": value}
		}
	}
	if request.CodeStatus != nil {
		value := strings.TrimSpace(*request.CodeStatus)
		updated.CodeStatus = value
		changedFields["code_status"] = value
		if target.CodeStatus != value {
			beforeAfter["code_status"] = map[string]any{"from": target.CodeStatus, "to": value}
		}
	}
	if request.Labels != nil {
		value := dedupeSortedStrings(request.Labels)
		updated.Labels = value
		changedFields["labels"] = value
		if !slices.Equal(target.Labels, value) {
			beforeAfter["labels"] = map[string]any{"from": target.Labels, "to": value}
		}
	}
	if request.Notes != nil {
		value := trimOptional(request.Notes)
		updated.Notes = value
		changedFields["notes"] = value
		if firstNonEmptyPtr(target.Notes) != firstNonEmptyPtr(value) || (target.Notes == nil) != (value == nil) {
			beforeAfter["notes"] = map[string]any{"from": target.Notes, "to": value}
		}
	}
	if request.NeedsFollowup != nil {
		value := *request.NeedsFollowup
		updated.NeedsFollowup = value
		changedFields["needs_followup"] = value
		if target.NeedsFollowup != value {
			beforeAfter["needs_followup"] = map[string]any{"from": target.NeedsFollowup, "to": value}
		}
	}

	snapshot.Issues[index] = updated
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json"), snapshot); err != nil {
		return nil, err
	}

	if updated.Source == "tracker" || trackedIssueExists(dataDir, workspaceID, updated.BugID) {
		if err := persistTrackedIssue(dataDir, workspaceID, updated); err != nil {
			return nil, err
		}
	} else {
		if err := persistIssueOverride(dataDir, workspaceID, updated); err != nil {
			return nil, err
		}
	}

	summary := "Updated issue " + issueID
	if change, ok := beforeAfter["severity"].(map[string]any); ok {
		summary = fmt.Sprintf("Updated issue %s severity %v -> %v", issueID, change["from"], change["to"])
	}
	if err := appendIssueActivity(
		dataDir,
		workspaceID,
		issueID,
		"",
		"issue.updated",
		summary,
		map[string]any{"changes": changedFields, "before_after": beforeAfter},
	); err != nil {
		return nil, err
	}
	return &updated, nil
}

func ListSavedViews(dataDir string, workspaceID string) ([]SavedIssueView, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	return loadSavedViews(dataDir, workspaceID)
}

func CreateSavedView(dataDir string, workspaceID string, request SavedIssueViewRequest) (*SavedIssueView, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	views, err := loadSavedViews(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	base := strings.TrimSpace(request.Name)
	if base == "" {
		base = "Untitled view"
	}
	seed := fmt.Sprintf("%s:%s:%s", workspaceID, base, now)
	digest := sha1.Sum([]byte(seed))
	slug := url.QueryEscape(strings.ReplaceAll(strings.ToLower(base), " ", "-"))
	viewID := slug + "-" + hex.EncodeToString(digest[:])[:8]
	view := SavedIssueView{
		ViewID:          viewID,
		WorkspaceID:     workspaceID,
		Name:            base,
		Query:           strings.TrimSpace(request.Query),
		Severities:      dedupeSortedStrings(request.Severities),
		Statuses:        dedupeSortedStrings(request.Statuses),
		Sources:         dedupeSortedStrings(request.Sources),
		Labels:          dedupeSortedStrings(request.Labels),
		DriftOnly:       request.DriftOnly,
		NeedsFollowup:   request.NeedsFollowup,
		ReviewReadyOnly: request.ReviewReadyOnly,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	views = append(views, view)
	if err := saveSavedViews(dataDir, workspaceID, views); err != nil {
		return nil, err
	}
	if err := appendViewActivity(dataDir, workspaceID, view.ViewID, "view.created", "Created saved view "+view.Name, map[string]any{
		"name":    view.Name,
		"filters": view,
	}); err != nil {
		return nil, err
	}
	return &view, nil
}

func UpdateSavedView(dataDir string, workspaceID string, viewID string, request SavedIssueViewRequest) (*SavedIssueView, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	views, err := loadSavedViews(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	index := -1
	for idx := range views {
		if views[idx].ViewID == viewID {
			index = idx
			break
		}
	}
	if index == -1 {
		return nil, os.ErrNotExist
	}
	target := views[index]
	updated := target
	updated.Name = fallbackString(strings.TrimSpace(request.Name), target.Name)
	updated.Query = strings.TrimSpace(request.Query)
	updated.Severities = dedupeSortedStrings(request.Severities)
	updated.Statuses = dedupeSortedStrings(request.Statuses)
	updated.Sources = dedupeSortedStrings(request.Sources)
	updated.Labels = dedupeSortedStrings(request.Labels)
	updated.DriftOnly = request.DriftOnly
	updated.NeedsFollowup = request.NeedsFollowup
	updated.ReviewReadyOnly = request.ReviewReadyOnly
	updated.UpdatedAt = nowUTC()
	views[index] = updated
	if err := saveSavedViews(dataDir, workspaceID, views); err != nil {
		return nil, err
	}
	if err := appendViewActivity(dataDir, workspaceID, viewID, "view.updated", "Updated saved view "+updated.Name, map[string]any{
		"name":    updated.Name,
		"filters": updated,
	}); err != nil {
		return nil, err
	}
	return &updated, nil
}

func DeleteSavedView(dataDir string, workspaceID string, viewID string) error {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return err
	}
	views, err := loadSavedViews(dataDir, workspaceID)
	if err != nil {
		return err
	}
	index := -1
	var target SavedIssueView
	for idx := range views {
		if views[idx].ViewID == viewID {
			index = idx
			target = views[idx]
			break
		}
	}
	if index == -1 {
		return os.ErrNotExist
	}
	remaining := append([]SavedIssueView{}, views[:index]...)
	remaining = append(remaining, views[index+1:]...)
	if err := saveSavedViews(dataDir, workspaceID, remaining); err != nil {
		return err
	}
	return appendViewActivity(dataDir, workspaceID, viewID, "view.deleted", "Deleted saved view "+target.Name, map[string]any{"name": target.Name})
}

func loadSavedViews(dataDir string, workspaceID string) ([]SavedIssueView, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "saved_views.json")
	var views []SavedIssueView
	if err := readJSON(path, &views); err != nil {
		if os.IsNotExist(err) {
			return []SavedIssueView{}, nil
		}
		return nil, err
	}
	slices.SortFunc(views, func(a, b SavedIssueView) int {
		left := strings.ToLower(a.Name)
		right := strings.ToLower(b.Name)
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
	return views, nil
}

func saveSavedViews(dataDir string, workspaceID string, views []SavedIssueView) error {
	slices.SortFunc(views, func(a, b SavedIssueView) int {
		left := strings.ToLower(a.Name)
		right := strings.ToLower(b.Name)
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
	return writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "saved_views.json"), views)
}

func loadTrackerIssues(dataDir string, workspaceID string) ([]issueRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "tracker_issues.json")
	var issues []issueRecord
	if err := readJSON(path, &issues); err != nil {
		if os.IsNotExist(err) {
			return []issueRecord{}, nil
		}
		return nil, err
	}
	slices.SortFunc(issues, func(a, b issueRecord) int {
		if a.BugID < b.BugID {
			return -1
		}
		if a.BugID > b.BugID {
			return 1
		}
		if a.UpdatedAt < b.UpdatedAt {
			return -1
		}
		if a.UpdatedAt > b.UpdatedAt {
			return 1
		}
		return 0
	})
	return issues, nil
}

func saveTrackerIssues(dataDir string, workspaceID string, issues []issueRecord) error {
	slices.SortFunc(issues, func(a, b issueRecord) int {
		if a.BugID < b.BugID {
			return -1
		}
		if a.BugID > b.BugID {
			return 1
		}
		if a.UpdatedAt < b.UpdatedAt {
			return -1
		}
		if a.UpdatedAt > b.UpdatedAt {
			return 1
		}
		return 0
	})
	return writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "tracker_issues.json"), issues)
}

func nextTrackerIssueID(issues []issueRecord) string {
	highest := 0
	for _, issue := range issues {
		if !strings.HasPrefix(issue.BugID, "TRK_") {
			continue
		}
		number := 0
		_, _ = fmt.Sscanf(issue.BugID, "TRK_%04d", &number)
		if number > highest {
			highest = number
		}
	}
	return fmt.Sprintf("TRK_%04d", highest+1)
}

func trackedIssueExists(dataDir string, workspaceID string, issueID string) bool {
	items, err := loadTrackerIssues(dataDir, workspaceID)
	if err != nil {
		return false
	}
	for _, item := range items {
		if item.BugID == issueID {
			return true
		}
	}
	return false
}

func persistTrackedIssue(dataDir string, workspaceID string, issue issueRecord) error {
	tracked, err := loadTrackerIssues(dataDir, workspaceID)
	if err != nil {
		return err
	}
	updated := make([]issueRecord, 0, len(tracked))
	for _, item := range tracked {
		if item.BugID != issue.BugID {
			updated = append(updated, item)
		}
	}
	if issue.Source == "ledger" {
		issue.Source = "tracker"
	}
	updated = append(updated, issue)
	return saveTrackerIssues(dataDir, workspaceID, updated)
}

func persistIssueOverride(dataDir string, workspaceID string, issue issueRecord) error {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "issue_overrides.json")
	overrides := map[string]map[string]any{}
	if err := readJSON(path, &overrides); err != nil && !os.IsNotExist(err) {
		return err
	}
	overrides[issue.BugID] = map[string]any{
		"severity":       issue.Severity,
		"issue_status":   issue.IssueStatus,
		"doc_status":     issue.DocStatus,
		"code_status":    issue.CodeStatus,
		"labels":         issue.Labels,
		"notes":          issue.Notes,
		"needs_followup": issue.NeedsFollowup,
		"updated_at":     issue.UpdatedAt,
	}
	return writeJSON(path, overrides)
}

func appendViewActivity(dataDir string, workspaceID string, viewID string, action string, summary string, details map[string]any) error {
	createdAt := nowUTC()
	record := activityRecord{
		ActivityID:  hashID(workspaceID, "view", viewID, action, createdAt),
		WorkspaceID: workspaceID,
		EntityType:  "view",
		EntityID:    viewID,
		Action:      action,
		Summary:     summary,
		Actor:       operatorActor(),
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

func dedupeSortedStrings(values []string) []string {
	set := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := set[trimmed]; exists {
			continue
		}
		set[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	slices.Sort(out)
	return out
}

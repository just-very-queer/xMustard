package workspaceops

import "strings"

type issueDriftDetail struct {
	BugID           string        `json:"bug_id"`
	DriftFlags      []string      `json:"drift_flags"`
	MissingEvidence []evidenceRef `json:"missing_evidence"`
	VerificationGap bool          `json:"verification_gap"`
}

func ListIssues(
	dataDir string,
	workspaceID string,
	query string,
	severities []string,
	issueStatuses []string,
	sources []string,
	labels []string,
	driftOnly bool,
	needsFollowup *bool,
	reviewReadyOnly bool,
) ([]issueRecord, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	items := append([]issueRecord{}, snapshot.Issues...)
	normalizedQuery := strings.ToLower(strings.TrimSpace(query))

	if len(severities) > 0 {
		allowed := make(map[string]struct{}, len(severities))
		for _, item := range severities {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				allowed[trimmed] = struct{}{}
			}
		}
		filtered := items[:0]
		for _, item := range items {
			if _, ok := allowed[item.Severity]; ok {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if len(issueStatuses) > 0 {
		allowed := make(map[string]struct{}, len(issueStatuses))
		for _, item := range issueStatuses {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				allowed[trimmed] = struct{}{}
			}
		}
		filtered := items[:0]
		for _, item := range items {
			if _, ok := allowed[item.IssueStatus]; ok {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if len(sources) > 0 {
		allowed := make(map[string]struct{}, len(sources))
		for _, item := range sources {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				allowed[trimmed] = struct{}{}
			}
		}
		filtered := items[:0]
		for _, item := range items {
			if _, ok := allowed[item.Source]; ok {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if len(labels) > 0 {
		allowed := make(map[string]struct{}, len(labels))
		for _, item := range labels {
			if trimmed := strings.ToLower(strings.TrimSpace(item)); trimmed != "" {
				allowed[trimmed] = struct{}{}
			}
		}
		filtered := items[:0]
		for _, item := range items {
			include := false
			for _, label := range item.Labels {
				if _, ok := allowed[strings.ToLower(label)]; ok {
					include = true
					break
				}
			}
			if include {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if driftOnly {
		filtered := items[:0]
		for _, item := range items {
			if len(item.DriftFlags) > 0 {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if needsFollowup != nil {
		filtered := items[:0]
		for _, item := range items {
			if item.NeedsFollowup == *needsFollowup {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if reviewReadyOnly {
		filtered := items[:0]
		for _, item := range items {
			if item.ReviewReadyCount > 0 {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if normalizedQuery != "" {
		filtered := items[:0]
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.BugID), normalizedQuery) ||
				strings.Contains(strings.ToLower(item.Title), normalizedQuery) ||
				strings.Contains(strings.ToLower(firstNonEmptyPtr(item.Summary)), normalizedQuery) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return items, nil
}

func ReadIssue(dataDir string, workspaceID string, issueID string) (*issueRecord, error) {
	issue, err := requireIssue(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	return issue, nil
}

func ReadDriftSummary(dataDir string, workspaceID string) (map[string]int, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	return snapshot.DriftSummary, nil
}

func ReadIssueDrift(dataDir string, workspaceID string, issueID string) (*issueDriftDetail, error) {
	issue, err := requireIssue(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	missing := make([]evidenceRef, 0, len(issue.Evidence)+len(issue.VerificationEvidence))
	for _, item := range append(append([]evidenceRef{}, issue.Evidence...), issue.VerificationEvidence...) {
		if item.PathExists != nil && !*item.PathExists {
			missing = append(missing, item)
		}
	}
	return &issueDriftDetail{
		BugID:           issue.BugID,
		DriftFlags:      append([]string{}, issue.DriftFlags...),
		MissingEvidence: missing,
		VerificationGap: len(issue.TestsPassed) == 0,
	}, nil
}

func ListSignals(dataDir string, workspaceID string, query string, severity string, promoted *bool) ([]discoverySignal, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	items := append([]discoverySignal{}, snapshot.Signals...)
	normalizedQuery := strings.ToLower(strings.TrimSpace(query))

	if strings.TrimSpace(severity) != "" {
		filtered := items[:0]
		for _, item := range items {
			if item.Severity == severity {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if promoted != nil {
		filtered := items[:0]
		for _, item := range items {
			isPromoted := item.PromotedBugID != nil
			if isPromoted == *promoted {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if normalizedQuery != "" {
		filtered := items[:0]
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.Title), normalizedQuery) ||
				strings.Contains(strings.ToLower(item.Summary), normalizedQuery) ||
				strings.Contains(strings.ToLower(item.FilePath), normalizedQuery) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return items, nil
}

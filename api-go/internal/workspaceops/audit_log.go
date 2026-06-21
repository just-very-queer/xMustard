package workspaceops

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Audit log: a durable, append-only trail of governance/execution events
// (policy changes, run launches, plan approvals, completions). Distinct from
// the general activity feed — this is the "who did what, when" record.

type AuditEvent struct {
	EventID     string `json:"event_id"`
	WorkspaceID string `json:"workspace_id"`
	Actor       string `json:"actor"`
	Action      string `json:"action"`
	TargetType  string `json:"target_type,omitempty"`
	TargetID    string `json:"target_id,omitempty"`
	Details     string `json:"details,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func auditLogPath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "audit_log.json")
}

func loadAuditEvents(dataDir, workspaceID string) ([]AuditEvent, error) {
	var events []AuditEvent
	if err := readJSON(auditLogPath(dataDir, workspaceID), &events); err != nil {
		if os.IsNotExist(err) {
			return []AuditEvent{}, nil
		}
		return nil, err
	}
	return events, nil
}

func compactTimestamp(value string) string {
	stripped := strings.NewReplacer("-", "", ":", "", ".", "", "T", "", "Z", "").Replace(value)
	if len(stripped) > 17 {
		return stripped[:17]
	}
	return stripped
}

// recordAuditEventNoGuard appends an event without re-validating the workspace
// (callers that already hold the snapshot use this).
func recordAuditEventNoGuard(dataDir, workspaceID string, event AuditEvent) error {
	unlock := lockStore(auditLogPath(dataDir, workspaceID))
	defer unlock()
	events, err := loadAuditEvents(dataDir, workspaceID)
	if err != nil {
		return err
	}
	now := nowUTC()
	event.WorkspaceID = workspaceID
	event.Actor = fallbackStr(strings.TrimSpace(event.Actor), "system")
	event.Action = strings.TrimSpace(event.Action)
	event.CreatedAt = now
	event.EventID = "audit_" + compactTimestamp(now) + "_" + padCount(len(events)+1)
	events = append(events, event)
	// Bound the log so a long-lived workspace's whole-file-rewrite append path can't
	// grow without limit (O(n^2) I/O + unbounded disk). Keep the newest auditLogMax.
	if len(events) > auditLogMax {
		events = events[len(events)-auditLogMax:]
	}
	return writeJSON(auditLogPath(dataDir, workspaceID), events)
}

// auditLogMax bounds the per-workspace governance audit log (newest-kept).
const auditLogMax = 5000

func padCount(n int) string {
	s := itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func fallbackStr(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
}

// RecordAuditEvent validates the workspace and appends an event.
func RecordAuditEvent(dataDir, workspaceID string, event AuditEvent) (*AuditEvent, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(event.Action) == "" {
		return nil, os.ErrInvalid
	}
	if err := recordAuditEventNoGuard(dataDir, workspaceID, event); err != nil {
		return nil, err
	}
	events, err := loadAuditEvents(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	last := events[len(events)-1]
	return &last, nil
}

// ListAuditEvents returns events newest-first (capped by limit; 0 = all).
func ListAuditEvents(dataDir, workspaceID string, limit int) ([]AuditEvent, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	events, err := loadAuditEvents(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].CreatedAt > events[j].CreatedAt
	})
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

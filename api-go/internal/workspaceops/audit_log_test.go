package workspaceops

import (
	"strings"
	"testing"
)

func TestAuditLogRoundTrip(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	e1, err := RecordAuditEvent(dataDir, workspaceID, AuditEvent{Actor: "alice", Action: "run.launch", TargetType: "issue", TargetID: "bug-1"})
	if err != nil {
		t.Fatalf("record event 1: %v", err)
	}
	if !strings.HasSuffix(e1.EventID, "_001") {
		t.Fatalf("first event id should end _001, got %q", e1.EventID)
	}

	e2, err := RecordAuditEvent(dataDir, workspaceID, AuditEvent{Action: "policy.update"})
	if err != nil {
		t.Fatalf("record event 2: %v", err)
	}
	if e2.Actor != "system" {
		t.Fatalf("default actor should be 'system', got %q", e2.Actor)
	}
	if !strings.HasSuffix(e2.EventID, "_002") {
		t.Fatalf("second event id should end _002, got %q", e2.EventID)
	}

	events, err := ListAuditEvents(dataDir, workspaceID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if _, err := RecordAuditEvent(dataDir, workspaceID, AuditEvent{Action: "  "}); err == nil {
		t.Fatal("expected empty action to be rejected")
	}

	// limit caps the result
	capped, err := ListAuditEvents(dataDir, workspaceID, 1)
	if err != nil {
		t.Fatalf("list capped: %v", err)
	}
	if len(capped) != 1 {
		t.Fatalf("expected 1 capped event, got %d", len(capped))
	}
}

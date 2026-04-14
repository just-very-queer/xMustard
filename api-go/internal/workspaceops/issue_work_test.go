package workspaceops

import (
	"strings"
	"testing"
)

func TestReadIssueWorkUsesDefaultRunbookWhenNoRunbookSelected(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	packet, err := ReadIssueWork(dataDir, workspaceID, issueID, "")
	if err != nil {
		t.Fatalf("read issue work with default runbook: %v", err)
	}
	if len(packet.Runbook) == 0 || !strings.Contains(packet.Runbook[0], "Verify the bug still reproduces") {
		t.Fatalf("unexpected default runbook steps: %#v", packet.Runbook)
	}
	if strings.Contains(packet.Prompt, "Selected runbook:") {
		t.Fatalf("default issue work should not append selected runbook block:\n%s", packet.Prompt)
	}
}

func TestReadIssueWorkUsesSelectedRunbookAndUpdatesPrompt(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, true)

	packet, err := ReadIssueWork(dataDir, workspaceID, issueID, "focused-verify")
	if err != nil {
		t.Fatalf("read issue work with explicit runbook: %v", err)
	}
	if len(packet.Runbook) != 2 || packet.Runbook[0] != "Reproduce the bug." || packet.Runbook[1] != "Report scope only." {
		t.Fatalf("unexpected selected runbook steps: %#v", packet.Runbook)
	}
	if !strings.Contains(packet.Prompt, "Selected runbook: Focused verify") {
		t.Fatalf("prompt missing selected runbook block:\n%s", packet.Prompt)
	}
}

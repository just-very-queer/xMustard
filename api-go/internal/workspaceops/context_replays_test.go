package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureAndListIssueContextReplaysPersistArtifacts(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	replay, err := CaptureIssueContextReplay(dataDir, workspaceID, issueID, IssueContextReplayRequest{
		Label: stringPtr("Operator replay"),
	})
	if err != nil {
		t.Fatalf("capture issue context replay: %v", err)
	}
	if replay.ReplayID == "" || replay.Label != "Operator replay" {
		t.Fatalf("unexpected replay record: %#v", replay)
	}
	if replay.Prompt == "" || len(replay.TreeFocus) == 0 || len(replay.GuidancePaths) == 0 {
		t.Fatalf("replay missing context fields: %#v", replay)
	}
	if len(replay.VerificationProfileIDs) == 0 || len(replay.TicketContextIDs) != 1 {
		t.Fatalf("replay missing linked artifact ids: %#v", replay)
	}

	replays, err := ListIssueContextReplays(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list issue context replays: %v", err)
	}
	if len(replays) != 1 || replays[0].ReplayID != replay.ReplayID {
		t.Fatalf("unexpected replay list: %#v", replays)
	}

	var stored []IssueContextReplayRecord
	if err := readJSON(filepath.Join(dataDir, "workspaces", workspaceID, "context_replays.json"), &stored); err != nil {
		t.Fatalf("read stored replays: %v", err)
	}
	if len(stored) != 1 || stored[0].ReplayID != replay.ReplayID {
		t.Fatalf("unexpected stored replays: %#v", stored)
	}

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity log: %v", err)
	}
	if !containsContextReplayAction(content, "context_replay.captured") {
		t.Fatalf("missing context replay activity entry: %s", string(content))
	}
}

func containsContextReplayAction(content []byte, action string) bool {
	for _, line := range bytesSplitLines(content) {
		if len(line) == 0 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(line, &payload); err != nil {
			continue
		}
		if payload["action"] == action {
			return true
		}
	}
	return false
}

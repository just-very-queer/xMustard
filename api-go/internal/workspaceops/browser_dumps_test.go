package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveListAndDeleteBrowserDumpsPersistArtifacts(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	saved, err := SaveBrowserDump(dataDir, workspaceID, issueID, BrowserDumpUpsertRequest{
		Source:          stringPtr("mcp-chrome"),
		Label:           "Operator browser repro",
		PageURL:         stringPtr("https://app.example.test/orders/123"),
		PageTitle:       stringPtr("Order detail"),
		Summary:         stringPtr("The confirmation banner never renders."),
		DOMSnapshot:     stringPtr("<main><div role=\"alert\">Missing confirmation</div></main>"),
		ConsoleMessages: []string{"Warning: confirmation missing"},
		NetworkRequests: []string{"GET /api/orders/123 200"},
		ScreenshotPath:  stringPtr("artifacts/order-detail.png"),
		Notes:           stringPtr("Captured after MCP browser dump."),
	})
	if err != nil {
		t.Fatalf("save browser dump: %v", err)
	}
	if saved.DumpID == "" || saved.Label != "Operator browser repro" {
		t.Fatalf("unexpected saved browser dump: %#v", saved)
	}

	listed, err := ListBrowserDumps(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list browser dumps: %v", err)
	}
	if len(listed) != 2 || listed[0].DumpID != saved.DumpID {
		t.Fatalf("expected newest-first browser dumps, got %#v", listed)
	}

	var stored []BrowserDumpRecord
	if err := readJSON(filepath.Join(dataDir, "workspaces", workspaceID, "browser_dumps.json"), &stored); err != nil {
		t.Fatalf("read stored browser dumps: %v", err)
	}
	if len(stored) != 2 || stored[0].DumpID != saved.DumpID {
		t.Fatalf("unexpected stored browser dumps: %#v", stored)
	}

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity log: %v", err)
	}
	if !containsActivityAction(content, "browser_dump.saved") {
		t.Fatalf("missing browser dump saved activity: %s", string(content))
	}

	if err := DeleteBrowserDump(dataDir, workspaceID, issueID, saved.DumpID); err != nil {
		t.Fatalf("delete browser dump: %v", err)
	}
	content, err = os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity log after delete: %v", err)
	}
	if !containsActivityAction(content, "browser_dump.deleted") {
		t.Fatalf("missing browser dump delete activity: %s", string(content))
	}
}

func containsActivityAction(content []byte, action string) bool {
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

package workspaceops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTerminalOpenWriteReadAndClose(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces.json"), []workspaceRecord{snapshot.Workspace}); err != nil {
		t.Fatalf("write workspaces: %v", err)
	}

	session, err := OpenTerminal(dataDir, TerminalOpenRequest{
		WorkspaceID: workspaceID,
		Cols:        100,
		Rows:        28,
	})
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}

	if err := WriteTerminal(session.TerminalID, "printf 'terminal-ok\\n'\n"); err != nil {
		t.Fatalf("write terminal: %v", err)
	}

	var content string
	var offset int64
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		read, err := ReadTerminal(dataDir, workspaceID, session.TerminalID, offset)
		if err != nil {
			t.Fatalf("read terminal: %v", err)
		}
		offset = read.Offset
		content += read.Content
		if strings.Contains(content, "terminal-ok") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(content, "terminal-ok") {
		t.Fatalf("expected terminal output, got %q", content)
	}

	if err := ResizeTerminal(session.TerminalID, 120, 40); err != nil {
		t.Fatalf("resize terminal: %v", err)
	}
	if err := CloseTerminal(session.TerminalID); err != nil {
		t.Fatalf("close terminal: %v", err)
	}

	read, err := ReadTerminal(dataDir, workspaceID, session.TerminalID, 0)
	if err != nil {
		t.Fatalf("read terminal after close: %v", err)
	}
	if read.TerminalID != session.TerminalID {
		t.Fatalf("unexpected terminal read payload: %#v", read)
	}
}

func TestOpenTerminalRequiresWorkspace(t *testing.T) {
	_, err := OpenTerminal(t.TempDir(), TerminalOpenRequest{WorkspaceID: "missing"})
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected missing workspace error, got %v", err)
	}
}

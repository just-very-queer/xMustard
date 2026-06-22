package workspaceops

import (
	"os"
	"path/filepath"
	"testing"
)

// ReadTerminal must validate both caller-controlled ids before building a path
// (no traversal to another workspace's log / outside the terminals dir) and must
// bound the read so a long log can't be slurped into one response — XM-PRO-003.
func TestReadTerminalValidatesIDsAndBounds(t *testing.T) {
	dd := t.TempDir()

	if _, err := ReadTerminal(dd, "../../etc", "term", 0); err == nil {
		t.Fatal("traversal workspace id must be rejected")
	}
	if _, err := ReadTerminal(dd, "ws1", "../../escape", 0); err == nil {
		t.Fatal("traversal terminal id must be rejected")
	}

	// a log twice the read cap: the first read is bounded and reports more-to-come.
	logDir := filepath.Join(dd, "workspaces", "ws1", "terminals")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxTerminalReadBytes*2)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(filepath.Join(logDir, "term1.log"), big, 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := ReadTerminal(dd, "ws1", "term1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Content) != maxTerminalReadBytes {
		t.Fatalf("read not bounded: got %d, want %d", len(first.Content), maxTerminalReadBytes)
	}
	if first.EOF {
		t.Fatal("EOF must be false while a full chunk (more data) was returned")
	}
	if first.Offset != int64(maxTerminalReadBytes) {
		t.Fatalf("bad continuation offset: got %d", first.Offset)
	}

	second, err := ReadTerminal(dd, "ws1", "term1", first.Offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Content) != maxTerminalReadBytes {
		t.Fatalf("second chunk: got %d, want %d", len(second.Content), maxTerminalReadBytes)
	}
}

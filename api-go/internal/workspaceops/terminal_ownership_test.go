package workspaceops

import (
	"errors"
	"os"
	"testing"
)

// A terminal session may only be addressed by its owning workspace; a different
// workspace must get not-found for read/write/resize/close (XM-NEW-013).
func TestTerminalSessionOwnership(t *testing.T) {
	id := "term_owned"
	terminalSessions.Store(id, &terminalSession{terminalID: id, workspaceID: "ws-A"})
	defer terminalSessions.Delete(id)

	if _, err := requireTerminalSession("ws-A", id); err != nil {
		t.Fatalf("owner workspace must resolve its session: %v", err)
	}
	if _, err := requireTerminalSession("ws-B", id); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a different workspace must not resolve another workspace's session")
	}
	// the mutation wrappers enforce the same ownership.
	if err := WriteTerminal("ws-B", id, "x"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cross-workspace write must be denied")
	}
	if err := ResizeTerminal("ws-B", id, 80, 24); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cross-workspace resize must be denied")
	}
	if err := CloseTerminal("ws-B", id); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cross-workspace close must be denied")
	}
}

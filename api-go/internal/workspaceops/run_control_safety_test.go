package workspaceops

import (
	"path/filepath"
	"testing"
)

func writeMinimalSnapshot(t *testing.T, dir, ws string) {
	t.Helper()
	if err := writeJSON(filepath.Join(dir, "workspaces", ws, "snapshot.json"),
		map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
}

// A terminal run must not be re-cancelled and must never signal its persisted PID
// (which the OS may have reused) — XM-NEW-006.
func TestCancelTerminalRunIsIdempotentNoSignal(t *testing.T) {
	dir := t.TempDir()
	writeMinimalSnapshot(t, dir, "ws")
	pid := 2147480000 // implausible PID; must never be signalled
	run := runRecord{RunID: "rc1", WorkspaceID: "ws", Status: "completed", PID: &pid, CreatedAt: nowUTC()}
	if err := saveRunRecord(dir, run); err != nil {
		t.Fatal(err)
	}
	got, err := CancelRun(dir, "ws", "rc1")
	if err != nil {
		t.Fatalf("cancelling a terminal run must not error: %v", err)
	}
	if got.Status != "completed" {
		t.Fatalf("terminal run status must be unchanged, got %q", got.Status)
	}
	if _, marked := cancelledRunIDs.Load("rc1"); marked {
		t.Fatal("terminal-state cancel must not leave a cancel marker (leak)")
	}
}

// Cancelling a queued (never-started) run marks it cancelled but does not leak a
// cancel marker, and clears any PID.
func TestCancelQueuedRunNoMarkerLeak(t *testing.T) {
	dir := t.TempDir()
	writeMinimalSnapshot(t, dir, "ws")
	run := runRecord{RunID: "rc2", WorkspaceID: "ws", Status: "queued", CreatedAt: nowUTC()}
	if err := saveRunRecord(dir, run); err != nil {
		t.Fatal(err)
	}
	got, err := CancelRun(dir, "ws", "rc2")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got.Status != "cancelled" || got.PID != nil {
		t.Fatalf("expected cancelled+nil PID, got status=%q pid=%v", got.Status, got.PID)
	}
	if _, marked := cancelledRunIDs.Load("rc2"); marked {
		t.Fatal("a never-active cancel must not leak a cancel marker")
	}
}

// Retry is only allowed from a terminal state — XM-NEW-007.
func TestRetryRunRejectsNonTerminal(t *testing.T) {
	dir := t.TempDir()
	writeMinimalSnapshot(t, dir, "ws")
	for _, status := range []string{"queued", "planning", "running"} {
		run := runRecord{RunID: "rt_" + status, WorkspaceID: "ws", Status: status, CreatedAt: nowUTC()}
		if err := saveRunRecord(dir, run); err != nil {
			t.Fatal(err)
		}
		if _, err := RetryRun(dir, "ws", "rt_"+status); err == nil {
			t.Fatalf("retry of a %q run must be rejected to avoid a duplicate worker", status)
		}
	}
}

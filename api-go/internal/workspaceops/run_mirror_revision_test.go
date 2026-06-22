package workspaceops

import (
	"testing"
)

// saveRunRecord must bump a DURABLE per-run revision past whatever is persisted,
// even when the caller carries a stale (e.g. zero, post-restart) in-memory value —
// otherwise the PG mirror's monotonic guard suppresses updates after a restart
// (XM-PRO-004).
func TestSaveRunRecordDurableMirrorRevision(t *testing.T) {
	dd := t.TempDir()
	const ws, runID = "ws1", "r1"

	if err := saveRunRecord(dd, runRecord{RunID: runID, WorkspaceID: ws, Status: "queued", CreatedAt: nowUTC()}); err != nil {
		t.Fatal(err)
	}
	first, err := loadRun(dd, ws, runID)
	if err != nil || first == nil {
		t.Fatalf("load after first save: %v", err)
	}
	if first.MirrorRevision != 1 {
		t.Fatalf("first save revision = %d, want 1", first.MirrorRevision)
	}

	// normal caller carries the current revision -> 2
	if err := saveRunRecord(dd, *first); err != nil {
		t.Fatal(err)
	}
	second, _ := loadRun(dd, ws, runID)
	if second.MirrorRevision != 2 {
		t.Fatalf("second save revision = %d, want 2", second.MirrorRevision)
	}

	// RESTART SIMULATION: a caller with a stale zero revision must NOT reset the
	// durable revision — it bumps past the persisted value (3, not 1).
	stale := runRecord{RunID: runID, WorkspaceID: ws, Status: "running", CreatedAt: second.CreatedAt, MirrorRevision: 0}
	if err := saveRunRecord(dd, stale); err != nil {
		t.Fatal(err)
	}
	third, _ := loadRun(dd, ws, runID)
	if third.MirrorRevision != 3 {
		t.Fatalf("stale-caller save revision = %d, want 3 (durable, restart-safe)", third.MirrorRevision)
	}
}

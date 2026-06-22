package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeSnapshotWithRoot(t *testing.T, dataDir, ws, root string) {
	t.Helper()
	if err := writeJSON(filepath.Join(dataDir, "workspaces", ws, "snapshot.json"),
		map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": root}}); err != nil {
		t.Fatal(err)
	}
}

// Recall must drift-check only a bounded candidate window (O(window)), not every
// promoted memory (O(history)) — yet a returned entry whose file changed must still
// be flagged stale (XM-POST-011 / goal A).
func TestRecallDriftCheckIsBoundedButFlagsReturnedStale(t *testing.T) {
	dir := t.TempDir()
	ws := "wsRecallBound"
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)
	if err := os.WriteFile(filepath.Join(root, "target.go"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	const n = 120
	entries := make([]ContextEntry, 0, n)
	for i := 0; i < n; i++ {
		e := ContextEntry{
			ID:        fmt.Sprintf("e%04d", i),
			Content:   fmt.Sprintf("memory body number %d", i),
			Status:    "verified",
			Promoted:  true,
			CreatedAt: fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
			UpdatedAt: fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i), // zero-padded → i=119 is newest
		}
		if i == n-1 { // one entry references target.go (with a baseline) + a unique token
			e.Content = "uniquetoken_auth_refresh_logic"
			e.Paths = []string{"target.go"}
			e.PathHashes = capturePathHashes(root, []string{"target.go"})
		}
		entries = append(entries, e)
	}
	if err := saveContextEntries(dir, ws, entries); err != nil {
		t.Fatal(err)
	}
	// change the file so the baselined entry is now stale.
	if err := os.WriteFile(filepath.Join(root, "target.go"), []byte("v2-changed-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Part 1 — no query, limit=1 over 120 memories: drift-check must be BOUNDED, not
	// one hash per entry.
	res, err := RecallContext(dir, ws, "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res["total_active"].(int) != n {
		t.Fatalf("setup: expected %d active, got %v", n, res["total_active"])
	}
	if checked := res["drift_checked"].(int); checked > recallCandidateFloor {
		t.Fatalf("drift_checked must be bounded (<=%d), got %d — recall is still O(history)", recallCandidateFloor, checked)
	}

	// Part 2 — a query that selects the stale entry: it must be returned AND flagged
	// stale with stale_paths (drift honesty preserved at O(top-K)).
	q, err := RecallContext(dir, ws, "uniquetoken_auth_refresh_logic", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if q["drift_checked"].(int) > recallCandidateFloor {
		t.Fatalf("query recall drift_checked must be bounded, got %v", q["drift_checked"])
	}
	got := q["entries"].([]ContextEntry)
	if len(got) != 1 || !got[0].Stale || len(got[0].StalePaths) == 0 {
		t.Fatalf("a returned stale entry must be flagged with stale_paths: %+v", got)
	}
}

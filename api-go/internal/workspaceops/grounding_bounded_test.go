package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Root/Stage 1a follow-up: `ground` reported stale memory by decoding and drift-hashing
// the ENTIRE promoted history (GetActiveContext). Grounding is a nine-tool path, so its
// stale-memory work must be bounded and say when it did not cover everything.
func TestGroundingStaleMemoryWorkIsBounded(t *testing.T) {
	dir := t.TempDir()
	ws := "wsGroundBound"
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)
	core := filepath.Join(t.TempDir(), "xmustard-core")
	if err := os.WriteFile(core, []byte("#!/bin/sh\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	if err := os.WriteFile(filepath.Join(root, "f.go"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	const n = 300
	entries := make([]ContextEntry, 0, n)
	for i := 0; i < n; i++ {
		entries = append(entries, ContextEntry{
			ID: fmt.Sprintf("g%04d", i), WorkspaceID: ws, Content: "c", Status: "verified", Promoted: true,
			Paths: []string{"f.go"}, PathHashes: capturePathHashes(root, []string{"f.go"}),
			SearchTokens: []string{"c"},
			CreatedAt:    fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i), UpdatedAt: fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
		})
	}
	if err := saveContextEntries(dir, ws, entries); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f.go"), []byte("v2 changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := stalenessChecks.Load()
	g, err := BuildSessionGrounding(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if checks := stalenessChecks.Load() - before; checks > groundStaleWindow {
		t.Fatalf("grounding drift-checked %d memories; want at most %d", checks, groundStaleWindow)
	}
	if g.StaleMemory == 0 || g.StaleMemoryComplete || g.StaleMemoryTotal != n {
		t.Fatalf("expected partial stale report over %d memories, got stale=%d complete=%v total=%d",
			n, g.StaleMemory, g.StaleMemoryComplete, g.StaleMemoryTotal)
	}
}

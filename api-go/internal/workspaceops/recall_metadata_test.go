package workspaceops

import (
	"fmt"
	"strings"
	"testing"
)

// In the metadata-fast path recall ranks on content-less metadata, then loads full
// content for ONLY the returned window — so a returned entry still carries its content
// while the rest of the store's content is never allocated (XM-PRO-010 final).
func TestRecallMetaFastLoadsContentForReturnedWindow(t *testing.T) {
	dir := t.TempDir()
	ws := "wsMetaContent"
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)

	const n = 120
	entries := make([]ContextEntry, 0, n)
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("entry %d", i)
		content := fmt.Sprintf("body %d generic filler text", i)
		if i == n-1 {
			title = "guardrail note"
			content = "the spend guardrail enforces a daily budget ceiling MARKER_XYZ"
		}
		entries = append(entries, ContextEntry{
			ID:           fmt.Sprintf("e%04d", i),
			Title:        title,
			Content:      content,
			Status:       "verified",
			Promoted:     true,
			CreatedAt:    fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
			UpdatedAt:    fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
			SearchTokens: memoryTokenList(title + " " + content),
		})
	}
	if err := saveContextEntries(dir, ws, entries); err != nil {
		t.Fatal(err)
	}
	res, err := RecallContext(dir, ws, "spend guardrail ceiling", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != 1 {
		t.Fatalf("want 1 returned entry, got %d", len(got))
	}
	if !strings.Contains(got[0].Content, "MARKER_XYZ") {
		t.Fatalf("the returned entry must carry its loaded content, got %q", got[0].Content)
	}
}

// Recall must rank on precomputed metadata (SearchTokens), NOT re-tokenize every
// promoted entry's full content on every call — so its CPU/allocation stops scaling
// with promoted-history size (XM-PRO-010). The tokenization counter must stay ~O(1)
// (the query only), not O(history).
func TestRecallMetadataFirstDoesNotTokenizeFullHistory(t *testing.T) {
	dir := t.TempDir()
	ws := "wsRecallMeta"
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)

	const n = 250
	entries := make([]ContextEntry, 0, n)
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("entry %d", i)
		content := fmt.Sprintf("memory body %d about widgets gadgets pipelines and caches", i)
		if i == n-1 {
			content = "uniquetoken_auth_refresh_logic special case"
		}
		entries = append(entries, ContextEntry{
			ID:           fmt.Sprintf("e%04d", i),
			Title:        title,
			Content:      content,
			Status:       "verified",
			Promoted:     true,
			CreatedAt:    fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
			UpdatedAt:    fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
			SearchTokens: memoryTokenList(title + " " + content), // precomputed at write time
		})
	}
	if err := saveContextEntries(dir, ws, entries); err != nil {
		t.Fatal(err)
	}

	before := memoryTokenizeCalls.Load()
	res, err := RecallContext(dir, ws, "uniquetoken_auth_refresh_logic", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	delta := memoryTokenizeCalls.Load() - before

	// Must not scale with n: only the query is tokenized (allow a small constant).
	if delta > 5 {
		t.Fatalf("recall tokenized %d times over %d entries — not metadata-first (should be ~O(1))", delta, n)
	}
	// Ranking correctness preserved: the unique-token query returns exactly that entry.
	got := res["entries"].([]ContextEntry)
	if len(got) != 1 || got[0].ID != fmt.Sprintf("e%04d", n-1) {
		t.Fatalf("metadata-ranked query returned the wrong entry: %+v", got)
	}
}

// A legacy entry written before SearchTokens existed must still rank correctly via
// the lazy fallback (so the optimization is backward-compatible).
func TestRecallLazyTokenFallbackForLegacyEntries(t *testing.T) {
	dir := t.TempDir()
	ws := "wsRecallLegacy"
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)

	legacy := ContextEntry{
		ID: "legacy1", Title: "old", Content: "legacy_marker_token content",
		Status: "verified", Promoted: true,
		CreatedAt: "2026-06-01T00:00:00.000000001Z", UpdatedAt: "2026-06-01T00:00:00.000000001Z",
		// no SearchTokens (legacy)
	}
	if err := saveContextEntries(dir, ws, []ContextEntry{legacy}); err != nil {
		t.Fatal(err)
	}
	res, err := RecallContext(dir, ws, "legacy_marker_token", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != 1 || got[0].ID != "legacy1" {
		t.Fatalf("legacy entry must still be found via lazy tokenization: %+v", got)
	}
}

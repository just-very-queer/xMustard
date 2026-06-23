package workspaceops

import (
	"os"
	"testing"
	"time"
)

// XM-PRO-010 build: the per-id content store bounds recall's PARSE time, not just its
// allocation. Recall ranks on the content-free meta cache and reads the returned
// window's content from per-id hash-named files — never parsing the source's content
// bytes — with a transparent fallback to the source so the cache can't corrupt memory.

// Recall reads the returned window's content from the per-id content store, NOT from
// the source: with a fresh meta cache + intact content file but a tampered source, the
// real content is still returned.
func TestRecallReadsFromPerIdContentStoreNotSource(t *testing.T) {
	dir := t.TempDir()
	ws := "wsContentStore"
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})

	entry, err := ProposeContext(dir, ws, ProposeContextRequest{
		Title: "budget rule", Content: "REAL spend ceiling MARKER_REAL", Source: "solo",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if !entry.Promoted {
		t.Fatalf("single-agent mode should promote immediately")
	}

	// the content file exists, named by the content hash.
	cf := contextContentFilePath(dir, ws, entry.ID, hashContent(entry.Content))
	if _, err := os.Stat(cf); err != nil {
		t.Fatalf("expected per-id content file at %s: %v", cf, err)
	}

	// poison the SOURCE's content, then refresh the meta cache so it is fresher than
	// the source (mtime guard) while still pointing at the REAL content's hash file.
	src, err := loadContextEntries(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	real := make([]ContextEntry, len(src))
	copy(real, src)
	for i := range src {
		src[i].Content = "WRONG_FROM_SOURCE"
	}
	if err := writeJSON(contextEntriesPath(dir, ws), src); err != nil {
		t.Fatal(err)
	}
	writeContextMetaCache(dir, ws, real) // cache now fresher than source, hash → REAL file

	res, err := RecallContext(dir, ws, "spend ceiling MARKER_REAL", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if got[0].Content != "REAL spend ceiling MARKER_REAL" {
		t.Fatalf("recall must serve content from the per-id store, got %q", got[0].Content)
	}
	if got[0].ContentHash != "" {
		t.Fatalf("the derived ContentHash transport field must not leak to callers, got %q", got[0].ContentHash)
	}
}

// Updating an entry's content writes a new hash-named file and prunes the prior one,
// so the store never accumulates stale-content files and recall returns the new text.
func TestContentStorePrunesStaleHashOnUpdate(t *testing.T) {
	dir := t.TempDir()
	ws := "wsContentPrune"
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})

	entry, err := ProposeContext(dir, ws, ProposeContextRequest{
		Title: "note", Content: "v1 ORIGINAL_TOKEN", Source: "solo", Permission: "readwrite",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldFile := contextContentFilePath(dir, ws, entry.ID, hashContent("v1 ORIGINAL_TOKEN"))
	if _, err := os.Stat(oldFile); err != nil {
		t.Fatalf("v1 content file should exist: %v", err)
	}

	updated, err := UpdateContextContent(dir, ws, entry.ID, "v2 UPDATED_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "v2 UPDATED_TOKEN" {
		t.Fatalf("update should return the new content, got %q", updated.Content)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("stale v1 content file must be pruned, stat err = %v", err)
	}
	newFile := contextContentFilePath(dir, ws, entry.ID, hashContent("v2 UPDATED_TOKEN"))
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("v2 content file should exist: %v", err)
	}
	// the entry's directory holds exactly one (current) content file.
	ents, err := os.ReadDir(contextContentEntryDir(dir, ws, entry.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Fatalf("expected exactly one content file after update, got %d", len(ents))
	}
}

// When a content file is missing (legacy entries, or a failed cache write leaving the
// new-hash file absent), recall falls back to a streaming source read — correctness is
// never sacrificed for the optimization.
func TestRecallFallsBackToSourceWhenContentFileMissing(t *testing.T) {
	dir := t.TempDir()
	ws := "wsContentFallback"
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})

	entry, err := ProposeContext(dir, ws, ProposeContextRequest{
		Title: "fact", Content: "FALLBACK_MARKER lives in the source", Source: "solo",
	})
	if err != nil {
		t.Fatal(err)
	}
	// wipe the entire content store; the meta cache + source remain.
	if err := os.RemoveAll(contextContentDir(dir, ws)); err != nil {
		t.Fatal(err)
	}
	res, err := RecallContext(dir, ws, "FALLBACK_MARKER source", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != 1 || got[0].ID != entry.ID {
		t.Fatalf("want the proposed entry, got %+v", got)
	}
	if got[0].Content != "FALLBACK_MARKER lives in the source" {
		t.Fatalf("recall must fall back to source content when the file is gone, got %q", got[0].Content)
	}
}

// A source write that bypasses the cache (so the meta cache is older than the source)
// must NOT be trusted: recall's mtime guard falls back to the source, seeing the new
// entry the stale cache never knew about.
func TestRecallMetaCacheStaleGuardFallsBackToSource(t *testing.T) {
	dir := t.TempDir()
	ws := "wsMetaStale"
	writeSnapshotWithRoot(t, dir, ws, t.TempDir())

	a := ContextEntry{
		ID: "entryA", Title: "a", Content: "alpha CACHED_MARKER",
		Status: "verified", Promoted: true,
		CreatedAt: "2026-06-01T00:00:00.000000001Z", UpdatedAt: "2026-06-01T00:00:00.000000001Z",
		SearchTokens: memoryTokenList("a alpha CACHED_MARKER"),
	}
	if err := saveContextEntries(dir, ws, []ContextEntry{a}); err != nil { // writes fresh cache (knows only A)
		t.Fatal(err)
	}
	// append B to the SOURCE only, bumping the source past the cache's mtime.
	b := ContextEntry{
		ID: "entryB", Title: "b", Content: "beta UNCACHED_MARKER",
		Status: "verified", Promoted: true,
		CreatedAt: "2026-06-01T00:00:00.000000002Z", UpdatedAt: "2026-06-01T00:00:00.000000002Z",
		SearchTokens: memoryTokenList("b beta UNCACHED_MARKER"),
	}
	if err := writeJSON(contextEntriesPath(dir, ws), []ContextEntry{a, b}); err != nil {
		t.Fatal(err)
	}
	// force the source strictly newer than the cache so the guard is exercised
	// deterministically regardless of filesystem mtime resolution.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(contextEntriesPath(dir, ws), future, future); err != nil {
		t.Fatal(err)
	}
	res, err := RecallContext(dir, ws, "UNCACHED_MARKER", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != 1 || got[0].ID != "entryB" {
		t.Fatalf("stale meta cache must be bypassed in favor of the source, got %+v", got)
	}
}

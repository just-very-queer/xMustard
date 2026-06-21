package workspaceops

import (
	"os"
	"path/filepath"
	"testing"
)

// A referenced path that is missing at verify time is baselined as a sentinel, so a
// later missing->present transition (and present->missing) is detected as drift
// instead of being silently untracked (XM-NEW-003).
func TestDriftBaselineMissingPathSentinel(t *testing.T) {
	root := t.TempDir()
	hashes := capturePathHashes(root, []string{"new.go"})
	if hashes["new.go"] != pathMissingSentinel {
		t.Fatalf("missing path must baseline as sentinel, got %q", hashes["new.go"])
	}
	// still missing -> not stale
	e1 := &ContextEntry{PathHashes: hashes}
	computeStaleness(root, e1)
	if e1.Stale {
		t.Fatal("a still-missing path must not be stale")
	}
	// create it -> missing->present transition is stale
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e2 := &ContextEntry{PathHashes: hashes}
	computeStaleness(root, e2)
	if !e2.Stale {
		t.Fatal("missing->present transition must be flagged stale")
	}

	// a present path that later disappears is also stale.
	base := capturePathHashes(root, []string{"new.go"})
	_ = os.Remove(filepath.Join(root, "new.go"))
	e3 := &ContextEntry{PathHashes: base}
	computeStaleness(root, e3)
	if !e3.Stale {
		t.Fatal("present->missing transition must be flagged stale")
	}
}

// Editing a memory's content discards the old drift baseline so re-promotion
// captures a fresh snapshot rather than reusing the previous assertion's evidence
// (XM-NEW-004).
func TestEditClearsDriftBaseline(t *testing.T) {
	dir := t.TempDir()
	ws := "wsDrift"
	entry, err := ProposeContext(dir, ws, ProposeContextRequest{Content: "first", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := loadContextEntries(dir, ws)
	entries[0].PathHashes = map[string]string{"a.go": "deadbeef"}
	entries[0].Stale = true
	entries[0].StalePaths = []string{"a.go"}
	_ = saveContextEntries(dir, ws, entries)

	if _, err := UpdateContextContent(dir, ws, entry.ID, "second"); err != nil {
		t.Fatal(err)
	}
	after, _ := loadContextEntries(dir, ws)
	if after[0].PathHashes != nil || after[0].Stale || after[0].StalePaths != nil {
		t.Fatalf("edit must clear baseline+stale flags, got %+v", after[0])
	}
}

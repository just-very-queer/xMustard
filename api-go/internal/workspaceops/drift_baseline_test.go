package workspaceops

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/govstore"
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
	storeUpdate(t, dir, ws, func(tx govstore.Tx) error {
		actor := govstore.Actor{Principal: "test"}
		if err := tx.SetBaselines(context.Background(), entry.ID, pathBaselines(map[string]string{"a.go": "deadbeef"}), "", actor); err != nil {
			return err
		}
		_, err := tx.MarkStale(context.Background(), entry.ID, []govstore.AnchorRef{{Kind: govstore.AnchorPath, Value: "a.go"}}, "", actor)
		return err
	})
	if before, _ := loadContextEntries(dir, ws); before[0].PathHashes["a.go"] != "deadbeef" {
		t.Fatalf("setup: baseline not recorded: %+v", before[0])
	}

	if _, err := UpdateContextContent(dir, ws, entry.ID, "second", ContextActor{Admin: true}); err != nil {
		t.Fatal(err)
	}
	after, _ := loadContextEntries(dir, ws)
	if after[0].PathHashes != nil || after[0].Stale || after[0].StalePaths != nil {
		t.Fatalf("edit must clear baseline+stale flags, got %+v", after[0])
	}
}

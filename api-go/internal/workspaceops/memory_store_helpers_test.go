package workspaceops

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// saveContextEntries seeds entries into the workspace's governance store through the
// legacy importer, filling the fields a hand-written fixture leaves out. It never
// overwrites an entry the store already holds.
func saveContextEntries(dataDir, workspaceID string, entries []ContextEntry) error {
	legacy := make([]ContextEntry, len(entries))
	for i, e := range entries {
		e.WorkspaceID = workspaceID
		e.Source = fallbackString(e.Source, "unknown")
		e.Permission = fallbackString(e.Permission, "readonly")
		e.Status = fallbackString(e.Status, "pending")
		e.RequiredVerifications = max(e.RequiredVerifications, 1)
		e.CreatedAt = fallbackString(e.CreatedAt, nowUTC())
		e.UpdatedAt = fallbackString(e.UpdatedAt, e.CreatedAt)
		vs := make([]ContextVerification, len(e.Verifications))
		for j, v := range e.Verifications {
			v.At = fallbackString(v.At, e.UpdatedAt)
			vs[j] = v
		}
		e.Verifications = vs
		legacy[i] = e
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		return err
	}
	return withMemoryStore(context.Background(), dataDir, workspaceID, func(s *govstore.SQLStore) error {
		_, threshold := contextDefaults(dataDir)
		rep, err := s.ImportContextEntriesJSON(context.Background(), workspaceID, bytes.NewReader(body),
			// A test seed is small, so it imports inline as importLegacyContextEntries
			// does for a small file, not in the heavy slot.
			govstore.ImportOptions{Threshold: threshold, Inline: true})
		if err == nil && rep.ConflictCount > 0 {
			return errConflictingSeed
		}
		return err
	})
}

var errConflictingSeed = Conflict("seed conflicts with a stored entry")

// storeUpdate runs fn in a write transaction on the workspace's store, for tests that
// set up state the public API cannot reach (drift baselines, tampered rows).
func storeUpdate(t *testing.T, dataDir, workspaceID string, fn func(govstore.Tx) error) {
	t.Helper()
	if err := memoryUpdate(context.Background(), dataDir, workspaceID, fn); err != nil {
		t.Fatal(err)
	}
}

// loadContextEntries returns every active entry of the workspace with its content.
func loadContextEntries(dataDir, workspaceID string) ([]ContextEntry, error) {
	ctx := context.Background()
	var out []ContextEntry
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		entries, err := listWorkspaceEntries(ctx, r, govstore.EntryFilter{WorkspaceID: workspaceID})
		if err != nil {
			return err
		}
		out, _, err = attachContent(ctx, r, entries)
		return err
	})
	if out == nil && err == nil {
		out = []ContextEntry{}
	}
	return out, err
}

package govstore

import (
	"context"
	"errors"
	"testing"
)

// A quarantined entry (WS-56) never enters the core tier, whether it is created there
// or moved there, and a classification change cannot drop the mark.
func TestQuarantinedEntryNeverEntersCore(t *testing.T) {
	s := openTestStore(t, newClock())
	ctx := context.Background()
	quarantined := map[string]string{MetaQuarantine: "untrusted_capture:webfetch"}
	err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.InsertEntry(ctx, NewEntry{ID: "q1", WorkspaceID: "ws1", Title: "t", Content: "c", Tier: TierCore,
			Metadata: quarantined}, alice)
		return err
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a quarantined entry was created in the core tier: %v", err)
	}
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.InsertEntry(ctx, NewEntry{ID: "q1", WorkspaceID: "ws1", Title: "t", Content: "c", Metadata: quarantined}, alice)
		return err
	})
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.SetClassification(ctx, "q1", Classification{Kind: "decision", Metadata: map[string]string{"other": "x"}}, alice)
		return err
	})
	var e Entry
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		e, err = tx.GetEntry(ctx, "q1")
		return err
	})
	if e.Metadata[MetaQuarantine] != "untrusted_capture:webfetch" || e.Metadata["other"] != "x" {
		t.Fatalf("classification dropped the quarantine mark: %+v", e.Metadata)
	}
	err = s.Update(ctx, func(tx Tx) error {
		_, err := tx.SetTier(ctx, "q1", TierCore, bob)
		return err
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a quarantined entry moved to the core tier: %v", err)
	}
	// the deferred tier, and the core tier for an entry that is not quarantined, stay open
	propose(t, s, "clean", "t", "c")
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.SetTier(ctx, "q1", "deferred", bob); err != nil {
			return err
		}
		_, err := tx.SetTier(ctx, "clean", TierCore, bob)
		return err
	})
}

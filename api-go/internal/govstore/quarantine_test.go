package govstore

import (
	"context"
	"encoding/json"
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

// A core-tier entry cannot take the quarantine mark, and the classify event that adds
// the mark records it, so the history says why the entry is quarantined.
func TestQuarantineMarkRefusedOnCoreAndAudited(t *testing.T) {
	s := openTestStore(t, newClock())
	ctx := context.Background()
	mark := Classification{Kind: "decision", Metadata: map[string]string{MetaQuarantine: "untrusted_capture:webfetch"}}
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.InsertEntry(ctx, NewEntry{ID: "core1", WorkspaceID: "ws1", Title: "t", Content: "c", Tier: TierCore}, alice); err != nil {
			return err
		}
		_, err := tx.InsertEntry(ctx, NewEntry{ID: "m1", WorkspaceID: "ws1", Title: "t", Content: "c"}, alice)
		return err
	})
	err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.SetClassification(ctx, "core1", mark, bob)
		return err
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a core entry took the quarantine mark: %v", err)
	}
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.SetClassification(ctx, "m1", mark, bob); err != nil {
			return err
		}
		_, err := tx.SetClassification(ctx, "m1", Classification{Kind: "fact"}, bob) // keeps the mark
		return err
	})
	evs, err := s.ListEvents(ctx, EventFilter{EntryID: "m1", Types: []string{EventClassify}})
	if err != nil || len(evs) != 2 {
		t.Fatalf("classify events: %v %+v", err, evs)
	}
	var added, kept map[string]any
	if json.Unmarshal(evs[0].Data, &added) != nil || json.Unmarshal(evs[1].Data, &kept) != nil {
		t.Fatalf("event data: %s / %s", evs[0].Data, evs[1].Data)
	}
	if added["quarantine"] != "untrusted_capture:webfetch" || kept["quarantine"] != nil {
		t.Fatalf("the audit does not show the mark once: %v / %v", added, kept)
	}
}

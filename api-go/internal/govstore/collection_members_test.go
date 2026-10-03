package govstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// A file created by a build that knew only migrations 1..4 upgrades in place, and the
// sharing rows it already held are still there after the membership step.
func TestMigrationUpgradesV4FileToCollectionMembers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gov.db")
	all := migrations
	migrations = all[:4]
	old, err := Open(ctx, path, Options{})
	if err != nil {
		migrations = all
		t.Fatal(err)
	}
	propose(t, old, "m1", "t", "c")
	admin := Actor{Principal: "admin"}
	var col Collection
	var g Grant
	mustUpdate(t, old, func(tx Tx) error {
		var err error
		if col, err = tx.CreateCollection(ctx, CollectionInput{Name: "team", OwnerWorkspace: "ws1"}, admin); err != nil {
			return err
		}
		if g, err = tx.Grant(ctx, GrantInput{SourceWorkspace: "ws1", CollectionID: col.ID, Permission: PermissionRead}, admin); err != nil {
			return err
		}
		_, err = tx.SetApplicability(ctx, "m1", "ws2", ApplicabilityForeignUnchecked, admin)
		return err
	})
	_ = old.Close()
	migrations = all

	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer s.Close()
	info, err := s.SchemaInfo(ctx)
	if err != nil || info.Version != 5 {
		t.Fatalf("schema after upgrade = %+v, %v", info, err)
	}
	if n := countRows(t, s, "SELECT count(*) FROM schema_migrations"); n != len(all) {
		t.Fatalf("migration rows = %d, want %d", n, len(all))
	}
	if got, err := s.GetCollection(ctx, col.ID); err != nil || got.Name != "team" {
		t.Fatalf("v4 collection lost in the upgrade: %+v, %v", got, err)
	}
	if grants, err := s.ListGrants(ctx, GrantFilter{CollectionID: col.ID}); err != nil || len(grants) != 1 || grants[0].ID != g.ID {
		t.Fatalf("v4 grant lost in the upgrade: %+v, %v", grants, err)
	}
	if app, err := s.ListApplicability(ctx, "m1"); err != nil || len(app) != 1 || app[0].State != ApplicabilityForeignUnchecked {
		t.Fatalf("v4 applicability lost in the upgrade: %+v, %v", app, err)
	}
	if _, err := s.ListEntries(ctx, EntryFilter{WorkspaceID: "ws1"}); err != nil {
		t.Fatalf("v4 entries lost in the upgrade: %v", err)
	}
}

// An entry joins a collection and leaves it; the listing follows both, and a re-add is
// idempotent rather than a second member row.
func TestCollectionMembershipAddListAndRemove(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "m1", "t", "c")
	propose(t, s, "m2", "t2", "c2")
	admin := Actor{Principal: "admin"}
	var col Collection
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		col, err = tx.CreateCollection(ctx, CollectionInput{Name: "team", OwnerWorkspace: "ws1"}, admin)
		return err
	})

	mustUpdate(t, s, func(tx Tx) error {
		first, err := tx.AddToCollection(ctx, col.ID, "m1", admin)
		if err != nil {
			return err
		}
		if !first.Created {
			t.Errorf("first add reported created=%v, want true", first.Created)
		}
		again, err := tx.AddToCollection(ctx, col.ID, "m1", admin)
		if err != nil {
			return err
		}
		if again.Created {
			t.Errorf("re-add reported created=%v, want false", again.Created)
		}
		_, err = tx.AddToCollection(ctx, col.ID, "m2", admin)
		return err
	})

	members, err := s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID})
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %+v, want 2", members)
	}
	ids := map[string]bool{}
	for _, m := range members {
		ids[m.EntryID] = true
		if m.CollectionID != col.ID || m.AddedBy != "admin" || m.AddedAt == "" {
			t.Errorf("member = %+v", m)
		}
	}
	if !ids["m1"] || !ids["m2"] {
		t.Fatalf("member ids = %+v", ids)
	}
	if members[0].EntryID != "m1" || members[1].EntryID != "m2" {
		t.Errorf("members are not in membership order: %+v", members)
	}
	// The entry side of the relation answers the same fact.
	cols, err := s.ListEntryCollections(ctx, "m1")
	if err != nil {
		t.Fatalf("entry collections: %v", err)
	}
	if len(cols) != 1 || cols[0] != col.ID {
		t.Fatalf("entry collections = %+v", cols)
	}
	// A re-add moved m1 to the end, so the listing order is the membership order and
	// not the entry order: m2 now precedes m1.
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.AddToCollection(ctx, col.ID, "m1", admin)
		return err
	}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	members, err = s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID})
	if err != nil {
		t.Fatalf("members after re-add: %v", err)
	}
	if len(members) != 2 || members[0].EntryID != "m2" || members[1].EntryID != "m1" {
		t.Fatalf("members after re-add = %+v, want m2 then m1", members)
	}
	if members[0].Seq >= members[1].Seq {
		t.Errorf("re-add did not advance the membership order: %+v", members)
	}

	mustUpdate(t, s, func(tx Tx) error {
		removed, err := tx.RemoveFromCollection(ctx, col.ID, "m1", admin)
		if err != nil {
			return err
		}
		if !removed.Removed {
			t.Errorf("remove reported removed=%v, want true", removed.Removed)
		}
		again, err := tx.RemoveFromCollection(ctx, col.ID, "m1", admin)
		if err != nil {
			return err
		}
		if again.Removed {
			t.Errorf("second remove reported removed=true, want false")
		}
		return nil
	})
	members, err = s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID})
	if err != nil {
		t.Fatalf("members after remove: %v", err)
	}
	if len(members) != 1 || members[0].EntryID != "m2" {
		t.Fatalf("members after remove = %+v", members)
	}
}

// Membership names a collection and an entry that both exist. A missing one is refused
// rather than stored, and the refusal says which of the two was missing.
func TestCollectionMembershipRefusesAMissingCollectionOrEntry(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "m1", "t", "c")
	admin := Actor{Principal: "admin"}
	var col Collection
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		col, err = tx.CreateCollection(ctx, CollectionInput{Name: "team", OwnerWorkspace: "ws1"}, admin)
		return err
	})

	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.AddToCollection(ctx, "col_nope", "m1", admin)
		return err
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("membership on a missing collection: %v", err)
	}
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.AddToCollection(ctx, col.ID, "m_nope", admin)
		return err
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("membership on a missing entry: %v", err)
	}
	// A collection in ws2 cannot hold an entry from ws1: membership is a relation
	// inside the collection's own workspace, not a cross-workspace link.
	var foreign Collection
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		foreign, err = tx.CreateCollection(ctx, CollectionInput{Name: "theirs", OwnerWorkspace: "ws2"}, admin)
		return err
	})
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.AddToCollection(ctx, foreign.ID, "m1", admin)
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("membership across workspaces: %v", err)
	}
	if n := countRows(t, s, "SELECT count(*) FROM collection_members"); n != 0 {
		t.Fatalf("refused memberships stored %d rows, want 0", n)
	}
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.RemoveFromCollection(ctx, "col_nope", "m1", admin)
		return err
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove from a missing collection: %v", err)
	}
	if _, err := s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: "col_nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("members of a missing collection: %v", err)
	}
	if members, err := s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID}); err != nil || len(members) != 0 {
		t.Fatalf("empty collection members = %+v, %v", members, err)
	}
}

// The listing is capped like every other govstore listing, and a large limit is clamped
// rather than honoured.
func TestCollectionMembersListingIsBounded(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	admin := Actor{Principal: "admin"}
	var col Collection
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		col, err = tx.CreateCollection(ctx, CollectionInput{Name: "team", OwnerWorkspace: "ws1"}, admin)
		return err
	})
	for _, id := range []string{"m1", "m2", "m3"} {
		propose(t, s, id, "t", "c")
		mustUpdate(t, s, func(tx Tx) error {
			_, err := tx.AddToCollection(ctx, col.ID, id, admin)
			return err
		})
	}
	members, err := s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID, Limit: 2})
	if err != nil {
		t.Fatalf("bounded members: %v", err)
	}
	if len(members) != 2 || members[0].EntryID != "m1" || members[1].EntryID != "m2" {
		t.Fatalf("bounded members = %+v", members)
	}
	members, err = s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID, Limit: 1 << 20})
	if err != nil {
		t.Fatalf("clamped members: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("clamped members = %+v", members)
	}
}

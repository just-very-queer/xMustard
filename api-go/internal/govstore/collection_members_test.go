package govstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	t.Cleanup(func() { migrations = all })
	old, err := Open(ctx, path, Options{})
	if err != nil {
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
	entries, err := s.ListEntries(ctx, EntryFilter{WorkspaceID: "ws1"})
	if err != nil || len(entries) != 1 || entries[0].ID != "m1" {
		t.Fatalf("v4 entries lost in the upgrade: %+v, %v", entries, err)
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
	cols, err := s.ListEntryCollections(ctx, "m1", CollectionFilter{})
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
	// A collection in ws2 cannot hold an entry from ws1 while no grant names it:
	// sharing is done by a grant, not by naming the collection.
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
		t.Fatalf("membership across workspaces with no grant: %v", err)
	}
	// With a grant naming the collection, the same membership is the sharing case and
	// is allowed: that is the flow the whole relation exists for.
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.Grant(ctx, GrantInput{SourceWorkspace: "ws1", CollectionID: foreign.ID,
			Permission: PermissionReadWrite}, admin); err != nil {
			return err
		}
		_, err := tx.AddToCollection(ctx, foreign.ID, "m1", admin)
		return err
	})
	granted, err := s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: foreign.ID})
	if err != nil {
		t.Fatalf("granted members: %v", err)
	}
	if len(granted) != 1 || granted[0].EntryID != "m1" {
		t.Fatalf("granted members = %+v, want m1", granted)
	}
	// Revoking the grant closes the membership again: the entry is no longer reachable
	// through that collection, so a later add is refused.
	mustUpdate(t, s, func(tx Tx) error {
		grants, err := tx.ListGrants(ctx, GrantFilter{CollectionID: foreign.ID})
		if err != nil {
			return err
		}
		if len(grants) != 1 {
			t.Fatalf("grants = %+v", grants)
		}
		_, err = tx.Revoke(ctx, grants[0].ID, admin)
		return err
	})
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.AddToCollection(ctx, foreign.ID, "m1", admin)
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("membership through a revoked grant: %v", err)
	}
	if n := countRows(t, s, "SELECT count(*) FROM collection_members"); n != 1 {
		t.Fatalf("membership rows = %d, want only the granted one", n)
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
	// More members than the store cap, so the clamp and the cursor are measured
	// against the ceiling rather than against three fixtures. They are added in one
	// transaction: 1,001 separate writes would cost more than they prove.
	const n = maxListLimit + 1
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("b%04d", i)
	}
	mustUpdate(t, s, func(tx Tx) error {
		for _, id := range ids {
			if _, err := tx.InsertEntry(ctx, NewEntry{
				ID: id, WorkspaceID: "ws1", Title: "t", Content: "c",
			}, alice); err != nil {
				return err
			}
		}
		for _, id := range ids {
			if _, err := tx.AddToCollection(ctx, col.ID, id, admin); err != nil {
				return err
			}
		}
		return nil
	})

	// The cap holds: an over-large limit is clamped, and the read stops there.
	members, err := s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID, Limit: 1 << 20})
	if err != nil {
		t.Fatalf("clamped members: %v", err)
	}
	if len(members) != maxListLimit {
		t.Fatalf("clamped members = %d, want the cap %d", len(members), maxListLimit)
	}
	if want, got := maxListLimit, clampLimit(1<<20); got != want {
		t.Errorf("clampLimit(1<<20) = %d, want %d", got, want)
	}
	// A small limit is honoured.
	members, err = s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID, Limit: 2})
	if err != nil {
		t.Fatalf("bounded members: %v", err)
	}
	if len(members) != 2 || members[0].EntryID != ids[0] || members[1].EntryID != ids[1] {
		t.Fatalf("bounded members = %+v", members)
	}
	// Paging past the cap reaches the member the clamped read could not return, which
	// is the promise the cursor exists for.
	more, err := s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID, Limit: 1 << 20, AfterSeq: members[1].Seq})
	if err != nil {
		t.Fatalf("paged members: %v", err)
	}
	// 1,001 members, the first two already read: 999 remain, the tail the clamped
	// first read could not return, in membership order.
	if len(more) != n-2 || more[0].EntryID != ids[2] || more[len(more)-1].EntryID != ids[n-1] {
		t.Fatalf("second page = %d members from %q to %q, want %d from %q to %q",
			len(more), more[0].EntryID, more[len(more)-1].EntryID, n-2, ids[2], ids[n-1])
	}
}

// A membership's order number is never reused. Removing the member holding the highest
// seq and then adding another must not hand the new member a number a caller paging
// with AfterSeq has already passed.
func TestCollectionMemberSequenceIsNotReusedAfterRemoval(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "m1", "t", "c")
	propose(t, s, "m2", "t2", "c2")
	propose(t, s, "m3", "t3", "c3")
	admin := Actor{Principal: "admin"}
	var col Collection
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		col, err = tx.CreateCollection(ctx, CollectionInput{Name: "team", OwnerWorkspace: "ws1"}, admin)
		return err
	})
	mustUpdate(t, s, func(tx Tx) error {
		for _, id := range []string{"m1", "m2", "m3"} {
			if _, err := tx.AddToCollection(ctx, col.ID, id, admin); err != nil {
				return err
			}
		}
		return nil
	})
	members, err := s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID})
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("members = %+v", members)
	}
	highest := members[2].Seq
	// A caller has paged as far as the member holding the highest seq.
	cursor := members[2].Seq
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.RemoveFromCollection(ctx, col.ID, "m3", admin) // the highest
		return err
	})
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AddToCollection(ctx, col.ID, "m1", admin) // a re-add after the removal
		return err
	})
	members, err = s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID})
	if err != nil {
		t.Fatalf("members after removal and re-add: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members after removal and re-add = %+v, want 2", members)
	}
	// m2 keeps the seq it was given: only the member added after the removal moves. The
	// point is that no member's seq drops to or below a number a paged caller has passed.
	for _, m := range members {
		if m.EntryID == "m1" && m.Seq <= highest {
			t.Errorf("the re-added member has seq %d, want above the removed high-water %d", m.Seq, highest)
		}
	}
	// The member added after the removal is reachable from a cursor already at the old
	// high-water mark, which is what a caller paging at that point holds.
	after, err := s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID, AfterSeq: cursor})
	if err != nil {
		t.Fatalf("members after the cursor: %v", err)
	}
	if len(after) != 1 || after[0].EntryID != "m1" {
		t.Fatalf("members after the cursor = %+v, want only the re-added m1", after)
	}
}

// A removal naming a purged entry is refused and writes nothing. The store enforces
// this in SQL (events_purged_no_text), so what the test pins is the outcome, not the
// mechanism: the transaction fails, the membership row survives and no event appears.
func TestCollectionMembershipRefusesToRemoveFromAPurgedEntry(t *testing.T) {
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
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AddToCollection(ctx, col.ID, "m1", admin)
		return err
	})
	if err := s.Update(ctx, func(tx Tx) error {
		return tx.Purge(ctx, "m1", "retracted", admin)
	}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	before, err := s.ListEvents(ctx, EventFilter{WorkspaceID: "ws1", Types: []string{EventCollectionMember}})
	if err != nil {
		t.Fatalf("events before: %v", err)
	}
	// The write is refused. The membership row it would have deleted is still there,
	// because the failing transaction commits nothing: this is the part a caller
	// depends on, and the part a guard in Go would not give if the refusal came from
	// somewhere further down.
	err = s.Update(ctx, func(tx Tx) error {
		_, err := tx.RemoveFromCollection(ctx, col.ID, "m1", Actor{Principal: "admin", Note: "free text"})
		return err
	})
	if err == nil {
		t.Fatal("removing a purged member was allowed")
	}
	if n := countRows(t, s, "SELECT count(*) FROM collection_members WHERE entry_id = 'm1'"); n != 1 {
		t.Fatalf("the refused removal still deleted the membership row: %d rows left, want 1", n)
	}
	after, err := s.ListEvents(ctx, EventFilter{WorkspaceID: "ws1", Types: []string{EventCollectionMember}})
	if err != nil {
		t.Fatalf("events after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("a refused removal still wrote an event: %d before, %d after", len(before), len(after))
	}
	// The membership row of a purged entry must still have a way out, or it is
	// metadata nothing can ever remove. It cannot be removed through this API, by the
	// refusal above, so the store's own cleanup has to be the answer; this asserts the
	// row is reachable there and not that some other writer exists.
	if n := countRows(t, s, "SELECT count(*) FROM collection_members WHERE entry_id = 'm1'"); n != 1 {
		t.Fatalf("membership rows for the purged entry = %d, want 1 pending cleanup", n)
	}
}

// A purged entry is not a member. Purge leaves the entry row as a tombstone, and
// AddToCollection refuses a purged entry, so the listing must refuse it too: the two
// must not disagree about what a collection holds.
func TestCollectionMembershipHidesAPurgedEntry(t *testing.T) {
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
		if _, err := tx.AddToCollection(ctx, col.ID, "m1", admin); err != nil {
			return err
		}
		_, err := tx.AddToCollection(ctx, col.ID, "m2", admin)
		return err
	})
	if err := s.Update(ctx, func(tx Tx) error {
		return tx.Purge(ctx, "m1", "retracted", admin)
	}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	members, err := s.ListCollectionMembers(ctx, CollectionMemberFilter{CollectionID: col.ID})
	if err != nil {
		t.Fatalf("members after purge: %v", err)
	}
	if len(members) != 1 || members[0].EntryID != "m2" {
		t.Fatalf("members after purge = %+v, want only m2", members)
	}
	cols, err := s.ListEntryCollections(ctx, "m1", CollectionFilter{})
	if err != nil {
		t.Fatalf("entry collections after purge: %v", err)
	}
	if len(cols) != 0 {
		t.Fatalf("purged entry still holds collections: %+v", cols)
	}
}

// A re-add and a removal are different facts in the history, so the membership event
// names the operation and reports whether the row was newly created.
func TestCollectionMemberEventSeparatesReAddFromRemoval(t *testing.T) {
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
		if _, err := tx.AddToCollection(ctx, col.ID, "m1", admin); err != nil {
			return err
		}
		if _, err := tx.AddToCollection(ctx, col.ID, "m2", admin); err != nil {
			return err
		}
		_, err := tx.AddToCollection(ctx, col.ID, "m1", admin) // the re-add
		return err
	})
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.RemoveFromCollection(ctx, col.ID, "m2", admin)
		return err
	})
	evs, err := s.ListEvents(ctx, EventFilter{WorkspaceID: "ws1", Types: []string{EventCollectionMember}})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(evs) != 4 {
		t.Fatalf("membership events = %+v, want 4", evs)
	}
	// In order: add m1 created, add m2 created, add m1 re-added (not created), remove m2.
	type op struct {
		kind    string
		created bool
		entry   string
	}
	want := []op{{"add", true, "m1"}, {"add", true, "m2"}, {"add", false, "m1"}, {"remove", false, "m2"}}
	for i, e := range evs {
		var data struct {
			Op      string `json:"op"`
			Created bool   `json:"created"`
		}
		if err := json.Unmarshal(e.Data, &data); err != nil {
			t.Fatalf("event %d data: %v", i, err)
		}
		if data.Op != want[i].kind || data.Created != want[i].created || e.EntryID != want[i].entry {
			t.Errorf("event %d = op %q created %v entry %q, want %+v", i, data.Op, data.Created, e.EntryID, want[i])
		}
	}
}

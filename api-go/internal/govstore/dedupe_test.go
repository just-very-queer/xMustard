package govstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/dedupe"
)

func dedupeHits(t *testing.T, s Store, text string) []DedupeHit {
	t.Helper()
	var hits []DedupeHit
	err := s.View(context.Background(), func(r Reader) error {
		var err error
		hits, err = r.DedupeCandidates(context.Background(), DedupeQuery{WorkspaceID: "ws1",
			Keys: dedupe.KeysOf("ws1", dedupe.NewProfile(text)), Limit: 10})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

const longFact = "The payments service reads its database password from PAYMENTS_DB_PASSWORD at startup and never logs it."

// Postings follow the served content: written on insert, replaced when a revision is
// served, dropped on purge, and never shared across workspaces.
func TestDedupePostingsFollowServedContent(t *testing.T) {
	s := openTestStore(t, newClock())
	ctx := context.Background()
	propose(t, s, "e1", "db password", longFact)
	hits := dedupeHits(t, s, "  the PAYMENTS service reads its database password from payments_db_password at startup and never logs it. ")
	if len(hits) != 1 || hits[0].EntryID != "e1" || !hits[0].Exact || hits[0].Bands != 8 {
		t.Fatalf("exact restatement: %+v", hits)
	}
	near := dedupeHits(t, s, "The payments service reads its database password from PAYMENTS_DB_PASSWORD at startup and never logs it!")
	if len(near) != 1 || near[0].Exact || near[0].Bands == 0 {
		t.Fatalf("near restatement shares bands only: %+v", near)
	}
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.InsertEntry(ctx, NewEntry{ID: "other", WorkspaceID: "ws2", Content: longFact}, alice)
		return err
	})
	if hits := dedupeHits(t, s, longFact); len(hits) != 1 || hits[0].EntryID != "e1" {
		t.Fatalf("postings leak across workspaces: %+v", hits)
	}
	rewritten := "Card tokens are vaulted by the tokenizer; the payments service only ever sees opaque token ids."
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AppendRevision(ctx, RevisionInput{EntryID: "e1", BaseRevision: 1, Op: "edit", Content: rewritten, Activate: true}, alice)
		return err
	})
	if hits := dedupeHits(t, s, longFact); len(hits) != 0 {
		t.Fatalf("the old content still matches after a new revision is served: %+v", hits)
	}
	if hits := dedupeHits(t, s, rewritten); len(hits) != 1 || !hits[0].Exact {
		t.Fatalf("served revision not posted: %+v", hits)
	}
	mustUpdate(t, s, func(tx Tx) error { return tx.Purge(ctx, "e1", "pii", alice) })
	if n := countRows(t, s, "SELECT count(*) FROM dedupe_keys k JOIN entries e ON e.pk = k.entry_pk WHERE e.id = 'e1'"); n != 0 {
		t.Fatalf("purge left %d postings", n)
	}
}

// Short generic text passes no entropy gate: it gets its exact key only, so it can
// never match fuzzily.
func TestLowEntropyTextHasOnlyItsExactKey(t *testing.T) {
	s := openTestStore(t, newClock())
	propose(t, s, "short", "t", "ok ok ok ok")
	if n := countRows(t, s, "SELECT count(*) FROM dedupe_keys k JOIN entries e ON e.pk = k.entry_pk WHERE e.id = 'short'"); n != 1 {
		t.Fatalf("low-entropy text has %d postings, want the exact key alone", n)
	}
	if hits := dedupeHits(t, s, "ok ok ok ok ok"); len(hits) != 0 {
		t.Fatalf("a different low-entropy text matched: %+v", hits)
	}
}

// Entries written before migration 4 get postings in bounded batches, in pk order.
func TestBackfillDedupeKeysCoversOlderEntries(t *testing.T) {
	s := openTestStore(t, newClock())
	ctx := context.Background()
	for i := range 5 {
		propose(t, s, fmt.Sprintf("old%d", i), "t", fmt.Sprintf("%s (variant %d)", longFact, i))
	}
	// as a store migrated from version 2 holds them: no postings, a backfill range
	if _, err := s.writer.Exec(`DELETE FROM dedupe_keys`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`UPDATE store_meta SET value = (SELECT CAST(max(pk) AS TEXT) FROM entries) WHERE key = 'dedupe_backfill_end'`); err != nil {
		t.Fatal(err)
	}
	counts := []int{}
	for range 4 {
		mustUpdate(t, s, func(tx Tx) error {
			n, err := tx.BackfillDedupeKeys(ctx, 2)
			counts = append(counts, n)
			return err
		})
	}
	if fmt.Sprint(counts) != "[2 2 1 0]" {
		t.Fatalf("backfill batches %v, want [2 2 1 0]", counts)
	}
	if hits := dedupeHits(t, s, longFact+" (variant 4)"); len(hits) == 0 || hits[0].EntryID != "old4" || !hits[0].Exact {
		t.Fatalf("backfilled entry not found: %+v", hits)
	}
}

// The postings of one memory stay under 1 KiB, measured on the pages the table and its
// index take (dbstat) over a thousand memories.
func TestDedupeSignaturesStayUnderOneKiBPerMemory(t *testing.T) {
	s := openTestStore(t, newClock())
	ctx := context.Background()
	const n = 1000
	mustUpdate(t, s, func(tx Tx) error {
		for i := range n {
			if _, err := tx.InsertEntry(ctx, NewEntry{ID: fmt.Sprintf("m%04d", i), WorkspaceID: "ws1",
				Content: fmt.Sprintf("Memory %d: the service %d listens on port %d and its config lives in cfg/%d.yaml", i, i%7, 8000+i, i)}, alice); err != nil {
				return err
			}
		}
		return nil
	})
	var bytes int
	if err := s.writer.QueryRow(`SELECT sum(pgsize) FROM dbstat WHERE name IN ('dedupe_keys', 'dedupe_keys_entry')`).Scan(&bytes); err != nil {
		t.Fatalf("dbstat: %v", err)
	}
	per := bytes / n
	t.Logf("dedupe postings: %d bytes on disk for %d memories, %d per memory", bytes, n, per)
	if per >= 1024 {
		t.Fatalf("dedupe postings take %d bytes per memory, want < 1024", per)
	}
}

// A corroboration counts a principal's restatements; the history takes an event for a
// principal's first one and for new evidence, and only served memory is corroborated.
func TestCorroborateCountsPrincipalsAndEvidence(t *testing.T) {
	s := openTestStore(t, newClock())
	ctx := context.Background()
	propose(t, s, "e1", "t", longFact)
	corroborate := func(a Actor) (Corroboration, bool, error) {
		var c Corroboration
		var logged bool
		err := s.Update(ctx, func(tx Tx) error {
			var err error
			c, logged, err = tx.Corroborate(ctx, "e1", a)
			return err
		})
		return c, logged, err
	}
	if _, _, err := corroborate(bob); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a pending entry was corroborated: %v", err)
	}
	promotePeerQuorum(t, s, "e1")
	steps := []struct {
		who    Actor
		count  int
		logged bool
		ev     int
	}{
		{bob, 1, true, 0},
		{bob, 2, false, 0},
		{Actor{Principal: "bob", Evidence: []string{"ev_1"}}, 3, true, 1},
		{Actor{Principal: "BOB", Evidence: []string{"ev_1"}}, 4, false, 1},
		{carol, 1, true, 0},
	}
	for i, st := range steps {
		c, logged, err := corroborate(st.who)
		if err != nil || c.Count != st.count || logged != st.logged || len(c.Evidence) != st.ev {
			t.Fatalf("step %d: %+v logged=%v err=%v", i, c, logged, err)
		}
	}
	var all []Corroboration
	if err := s.View(ctx, func(r Reader) error {
		var err error
		all, err = r.Corroborations(ctx, "e1")
		return err
	}); err != nil || len(all) != 2 {
		t.Fatalf("corroborations: %+v %v", all, err)
	}
	if n := countRows(t, s, "SELECT count(*) FROM events WHERE entry_id = 'e1' AND type = 'corroborate'"); n != 3 {
		t.Fatalf("%d corroborate events, want 3", n)
	}
}

// promotePeerQuorum promotes id through two peer approvals, as the trigger requires.
func promotePeerQuorum(t *testing.T, s Store, id string) {
	t.Helper()
	vote(t, s, id, bob, VerdictApprove)
	vote(t, s, id, carol, VerdictApprove)
	promotePeer(t, s, id)
}

// The quota counts: active entries of the workspace and one principal's pending ones.
func TestMemoryCountsForQuotas(t *testing.T) {
	s := openTestStore(t, newClock())
	ctx := context.Background()
	propose(t, s, "a1", "t", "first pending proposal of alice about the build")
	propose(t, s, "a2", "t", "second pending proposal of alice about the tests")
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.InsertEntry(ctx, NewEntry{ID: "b1", WorkspaceID: "ws1", Content: "a proposal of bob"}, bob)
		return err
	})
	promotePeerQuorum(t, s, "a1")
	var c MemoryCounts
	if err := s.View(ctx, func(r Reader) error {
		var err error
		c, err = r.MemoryCounts(ctx, "ws1", " Alice ")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if c != (MemoryCounts{Active: 3, Pending: 1}) {
		t.Fatalf("counts %+v, want 3 active and 1 pending of alice", c)
	}
}

// A store written before migration 4 upgrades with a backfill range over its entries:
// they get postings in batches, and entries written after the upgrade get them at once.
func TestMigrationV4BackfillsOlderEntries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gov.db")
	all := migrations
	migrations = all[:2]
	old, err := Open(ctx, path, Options{})
	if err != nil {
		migrations = all
		t.Fatal(err)
	}
	// migration 2's store has no postings table, so its entries are written as rows
	for i, content := range []string{longFact, "Integration tests run against the fake payment gateway on port 9099."} {
		id, at := fmt.Sprintf("pre%d", i), "2026-09-01T00:00:00.000000000Z"
		if _, err := old.writer.ExecContext(ctx, `INSERT INTO entries (id, workspace_id, source, source_key, revision,
			head_revision, content_digest, created_at, updated_at) VALUES (?, 'ws1', 'alice', 'alice', 1, 1, ?, ?, ?)`,
			id, Digest(content), at, at); err != nil {
			migrations = all
			t.Fatal(err)
		}
		if _, err := old.writer.ExecContext(ctx, `INSERT INTO revisions (entry_id, revision, state, op, content,
			content_digest, content_bytes, author, author_key, created_at) VALUES (?, 1, 'accepted', 'propose', ?, ?, ?, 'alice', 'alice', ?)`,
			id, content, Digest(content), len(content), at); err != nil {
			migrations = all
			t.Fatal(err)
		}
	}
	_ = old.Close()
	migrations = all

	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if hits := dedupeHits(t, s, longFact); len(hits) != 0 {
		t.Fatalf("postings before the backfill: %+v", hits)
	}
	mustUpdate(t, s, func(tx Tx) error {
		n, err := tx.BackfillDedupeKeys(ctx, 100)
		if n != 2 {
			return fmt.Errorf("backfilled %d entries, want 2", n)
		}
		return err
	})
	if hits := dedupeHits(t, s, longFact); len(hits) != 1 || hits[0].EntryID != "pre0" || !hits[0].Exact {
		t.Fatalf("backfilled postings: %+v", hits)
	}
	propose(t, s, "post", "t", "A memory written after the upgrade is posted when it is inserted.")
	mustUpdate(t, s, func(tx Tx) error {
		if n, err := tx.BackfillDedupeKeys(ctx, 100); n != 0 || err != nil {
			return fmt.Errorf("a finished backfill ran again: %d %v", n, err)
		}
		return nil
	})
}

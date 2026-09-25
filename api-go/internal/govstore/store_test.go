package govstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	alice = Actor{Principal: "alice", HeadSHA: "c0ffee1"}
	bob   = Actor{Principal: "bob"}
	carol = Actor{Principal: "carol"}
	anon  = Actor{Principal: OpenModeIdentity}
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *testClock {
	return &testClock{t: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Millisecond) // strictly increasing, like real writes
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func openTestStore(t *testing.T, clk *testClock) *SQLStore {
	t.Helper()
	opts := Options{}
	if clk != nil {
		opts.Now = clk.Now
	}
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "gov.db"), opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustUpdate(t *testing.T, s Store, fn func(Tx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("update: %v", err)
	}
}

func propose(t *testing.T, s Store, id, title, content string, paths ...string) Entry {
	t.Helper()
	var e Entry
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		e, err = tx.InsertEntry(context.Background(), NewEntry{
			ID: id, WorkspaceID: "ws1", Title: title, Content: content, Paths: paths,
		}, alice)
		return err
	})
	return e
}

func vote(t *testing.T, s Store, id string, who Actor, verdict string) {
	t.Helper()
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.RecordVote(context.Background(), VoteInput{EntryID: id, Verdict: verdict}, who)
		return err
	})
}

func promotePeer(t *testing.T, s Store, id string) Entry {
	t.Helper()
	var e Entry
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		e, err = tx.SetPromotion(context.Background(), id, Promotion{Status: StatusVerified, Promoted: true, VerificationMode: ModePeerVerified}, bob)
		return err
	})
	return e
}

func countRows(t *testing.T, s *SQLStore, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.readers.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func TestMigrationsIdempotentAndFingerprintRecorded(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gov.db")
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.SchemaInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != LatestSchemaVersion() || len(info.Fingerprint) != 64 || info.JournalMode != "wal" {
		t.Fatalf("schema info = %+v", info)
	}
	var recorded, appliedAt string
	if err := s.readers.QueryRow("SELECT value FROM store_meta WHERE key = 'schema_fingerprint'").Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != info.Fingerprint {
		t.Fatalf("recorded fingerprint %s, computed %s", recorded, info.Fingerprint)
	}
	var checksum string
	if err := s.readers.QueryRow("SELECT checksum, applied_at FROM schema_migrations WHERE version = 1").Scan(&checksum, &appliedAt); err != nil {
		t.Fatal(err)
	}
	if checksum != Digest(schemaV1) {
		t.Fatalf("migration checksum %s", checksum)
	}
	var userVersion, appID int64
	_ = s.readers.QueryRow("PRAGMA user_version").Scan(&userVersion)
	_ = s.readers.QueryRow("PRAGMA application_id").Scan(&appID)
	if userVersion != 1 || appID != applicationID {
		t.Fatalf("user_version %d application_id %#x", userVersion, appID)
	}
	// Running the migrator again, and reopening, applies nothing and keeps the
	// fingerprint.
	if _, fp, err := migrate(ctx, s.writer, time.Now); err != nil || fp != info.Fingerprint {
		t.Fatalf("re-migrate: fp=%s err=%v", fp, err)
	}
	_ = s.Close()
	s2, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	info2, _ := s2.SchemaInfo(ctx)
	var rows int
	var appliedAt2 string
	_ = s2.readers.QueryRow("SELECT count(*), max(applied_at) FROM schema_migrations").Scan(&rows, &appliedAt2)
	if rows != 1 || appliedAt2 != appliedAt || info2.Fingerprint != info.Fingerprint {
		t.Fatalf("reopen changed migrations: rows=%d applied %s->%s fp %s->%s", rows, appliedAt, appliedAt2, info.Fingerprint, info2.Fingerprint)
	}
}

func rawExec(t *testing.T, path, query string, args ...any) error {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(query, args...)
	return err
}

func TestOpenRejectsDriftTamperingAndForeignFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	drifted := filepath.Join(dir, "drift.db")
	s, err := Open(ctx, drifted, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if err := rawExec(t, drifted, "CREATE TABLE sneaky (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, drifted, Options{}); !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("out-of-band schema change: got %v, want ErrSchemaDrift", err)
	}

	tampered := filepath.Join(dir, "tampered.db")
	s, _ = Open(ctx, tampered, Options{})
	_ = s.Close()
	if err := rawExec(t, tampered, "UPDATE schema_migrations SET checksum = 'x'"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, tampered, Options{}); !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("tampered migration: got %v, want ErrSchemaDrift", err)
	}

	newer := filepath.Join(dir, "newer.db")
	s, _ = Open(ctx, newer, Options{})
	_ = s.Close()
	if err := rawExec(t, newer, "INSERT INTO schema_migrations VALUES (99, 'future', 'x', '2030-01-01T00:00:00.000000000Z')"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, newer, Options{}); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("newer schema: got %v, want ErrSchemaTooNew", err)
	}

	foreign := filepath.Join(dir, "foreign.db")
	if err := rawExec(t, foreign, "CREATE TABLE other (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, foreign, Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign file: got %v, want ErrInvalid", err)
	}
}

func TestPragmas(t *testing.T) {
	s := openTestStore(t, nil)
	propose(t, s, "perm", "t", "private text")
	for _, f := range []string{s.Path(), s.Path() + "-wal"} {
		if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v (%v), want 0600", f, fi.Mode().Perm(), err)
		}
	}
	check := func(db *sql.DB, pragma string, want any) {
		t.Helper()
		var got any
		if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatalf("%s: %v", pragma, err)
		}
		if gs, ok := got.(string); ok {
			got = strings.ToLower(gs)
		}
		if got != want {
			t.Fatalf("PRAGMA %s = %v (%T), want %v", pragma, got, got, want)
		}
	}
	// the writer: one connection, so each pragma reads the connection that commits
	check(s.writer, "journal_mode", "wal")
	check(s.writer, "synchronous", int64(2)) // FULL for governance commits
	check(s.writer, "cache_size", int64(-DefaultCacheKiB))
	check(s.writer, "page_size", int64(4096))
	check(s.writer, "mmap_size", int64(0))
	check(s.writer, "busy_timeout", int64(DefaultBusyTimeout.Milliseconds()))
	check(s.writer, "foreign_keys", int64(1))
	check(s.writer, "temp_store", int64(1))
	check(s.writer, "secure_delete", int64(2))
	check(s.writer, "auto_vacuum", int64(2))
	// readers
	check(s.readers, "journal_mode", "wal")
	check(s.readers, "cache_size", int64(-DefaultReaderCacheKiB))
	check(s.readers, "mmap_size", int64(0))
	check(s.readers, "busy_timeout", int64(DefaultBusyTimeout.Milliseconds()))
	check(s.readers, "query_only", int64(1))
	// the page caches together stay within the 2-4 MiB line of PAR-STORE-01, counted
	// at their resident cost (modernc stores a 4 KiB page in an 8 KiB slot)
	if total := cacheSlotFactor * (DefaultCacheKiB + DefaultMaxReaders*DefaultReaderCacheKiB); total > 4096 {
		t.Fatalf("resident page cache cap %d KiB exceeds 4 MiB", total)
	}
	if _, err := s.readers.Exec("CREATE TABLE nope (x INTEGER)"); err == nil {
		t.Fatal("reader pool accepted a write")
	}
}

func TestInsertEntryRecordsRevisionEventAnchors(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	e := propose(t, s, "ctx_1", "  Build with make  ", "Run make check-backend before pushing.", "./Makefile", "Makefile", "api-go/go.mod")
	if e.Title != "Build with make" || e.Revision != 1 || e.HeadRevision != 1 || e.Status != StatusPending || e.Promoted {
		t.Fatalf("entry = %+v", e)
	}
	if e.Source != "alice" || e.ProposeHead != "c0ffee1" || e.ContentDigest != Digest("Run make check-backend before pushing.") {
		t.Fatalf("provenance = %+v", e)
	}
	rv, err := s.GetRevision(ctx, "ctx_1", 1)
	if err != nil || rv.Content != "Run make check-backend before pushing." || rv.State != RevisionAccepted || rv.Op != "propose" {
		t.Fatalf("revision = %+v err=%v", rv, err)
	}
	anchors, _ := s.ListAnchors(ctx, "ctx_1")
	if len(anchors) != 2 || anchors[0].Value != "Makefile" || anchors[1].Value != "api-go/go.mod" {
		t.Fatalf("anchors = %+v", anchors)
	}
	evs, _ := s.ListEvents(ctx, EventFilter{EntryID: "ctx_1"})
	if len(evs) != 1 || evs[0].Type != EventPropose || evs[0].Principal != "alice" || evs[0].NewDigest != e.ContentDigest || evs[0].HeadSHA != "c0ffee1" {
		t.Fatalf("events = %+v", evs)
	}
	// duplicate id, bad ids and empty content are rejected
	err = s.Update(ctx, func(tx Tx) error {
		_, err := tx.InsertEntry(ctx, NewEntry{ID: "ctx_1", WorkspaceID: "ws1", Content: "x"}, alice)
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate id: %v", err)
	}
	for _, bad := range []NewEntry{
		{ID: "../x", WorkspaceID: "ws1", Content: "x"},
		{ID: "ok", WorkspaceID: "ws*", Content: "x"},
		{ID: "ok", WorkspaceID: "ws1", Content: "  "},
		{ID: "ok", WorkspaceID: "ws1", Content: "x", Tags: []string{"a,b"}},
		{ID: "ok", WorkspaceID: "ws1", Content: "x", Scope: "universe"},
	} {
		err := s.Update(ctx, func(tx Tx) error { _, err := tx.InsertEntry(ctx, bad, alice); return err })
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("InsertEntry(%+v) = %v, want ErrInvalid", bad, err)
		}
	}
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.InsertEntry(ctx, NewEntry{ID: "ok", WorkspaceID: "ws1", Content: "x"}, Actor{})
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing principal: %v", err)
	}
}

func TestCompareAndSetOnBaseRevision(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "ctx_cas", "Deploy", "deploy with make deploy")

	var rev Revision
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		rev, err = tx.AppendRevision(ctx, RevisionInput{EntryID: "ctx_cas", BaseRevision: 1, Op: "edit",
			Content: "deploy with make release", Reason: "renamed target", Activate: true}, alice)
		return err
	})
	if rev.Revision != 2 || rev.BaseRevision != 1 || rev.State != RevisionAccepted {
		t.Fatalf("rev = %+v", rev)
	}
	// A writer still holding revision 1 loses the compare-and-set and learns the
	// current revision and digest.
	err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.AppendRevision(ctx, RevisionInput{EntryID: "ctx_cas", BaseRevision: 1, Op: "edit",
			Content: "deploy with make ship", Activate: true}, bob)
		return err
	})
	ce, ok := IsConflict(err)
	if !ok || !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base: got %v, want *ConflictError", err)
	}
	if ce.CurrentRevision != 2 || ce.CurrentDigest != Digest("deploy with make release") || ce.BaseRevision != 1 || ce.HeadRevision != 2 {
		t.Fatalf("conflict = %+v", ce)
	}
	e, _ := s.GetEntry(ctx, "ctx_cas")
	if e.Revision != 2 || e.ContentDigest != Digest("deploy with make release") {
		t.Fatalf("losing writer changed the entry: %+v", e)
	}

	// A no-op edit is rejected.
	err = s.Update(ctx, func(tx Tx) error {
		_, err := tx.AppendRevision(ctx, RevisionInput{EntryID: "ctx_cas", BaseRevision: 2, Op: "edit",
			Content: "deploy with make release"}, alice)
		return err
	})
	if !errors.Is(err, ErrNoChange) {
		t.Fatalf("no-op edit: %v", err)
	}

	// Pending revision (PAR-GOV-05): the served revision stays while revision 3 waits;
	// the head moves, so a writer basing on the served revision conflicts.
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AppendRevision(ctx, RevisionInput{EntryID: "ctx_cas", BaseRevision: 2, Op: "str_replace",
			Content: "deploy with make release VERSION=x", Reason: "pin version"}, bob)
		return err
	})
	e, _ = s.GetEntry(ctx, "ctx_cas")
	if e.Revision != 2 || e.HeadRevision != 3 {
		t.Fatalf("pending revision served early: %+v", e)
	}
	_, err = func() (Revision, error) {
		var rv Revision
		err := s.Update(ctx, func(tx Tx) error {
			var err error
			rv, err = tx.AppendRevision(ctx, RevisionInput{EntryID: "ctx_cas", BaseRevision: 2, Op: "edit", Content: "other"}, carol)
			return err
		})
		return rv, err
	}()
	if ce, ok := IsConflict(err); !ok || ce.CurrentRevision != 2 || ce.HeadRevision != 3 {
		t.Fatalf("base below head: %v", err)
	}
	// Rejecting the pending head moves the live head back, so edits build on live text.
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.RejectRevision(ctx, "ctx_cas", 3, "not agreed", carol)
		return err
	})
	e, _ = s.GetEntry(ctx, "ctx_cas")
	if e.HeadRevision != 2 {
		t.Fatalf("head after reject = %d", e.HeadRevision)
	}
	mustUpdate(t, s, func(tx Tx) error {
		rv, err := tx.AppendRevision(ctx, RevisionInput{EntryID: "ctx_cas", BaseRevision: 2, Op: "insert", Content: "deploy: make release"}, alice)
		if err == nil && rv.Revision != 4 {
			t.Errorf("revision numbers must not be reused: got %d", rv.Revision)
		}
		return err
	})
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AcceptRevision(ctx, AcceptInput{EntryID: "ctx_cas", Revision: 4}, carol)
		return err
	})
	e, _ = s.GetEntry(ctx, "ctx_cas")
	if e.Revision != 4 || e.ContentDigest != Digest("deploy: make release") {
		t.Fatalf("accepted = %+v", e)
	}
	revs, _ := s.ListRevisions(ctx, "ctx_cas", RevisionFilter{WithContent: true})
	if len(revs) != 4 || revs[0].Revision != 4 || revs[3].Content != "deploy with make deploy" || revs[1].State != RevisionRejected {
		t.Fatalf("history = %+v", revs)
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "ctx_ev", "t", "content")
	vote(t, s, "ctx_ev", bob, VerdictApprove)

	// Through the store's own transaction.
	for _, q := range []string{
		"UPDATE events SET note = 'rewritten' WHERE entry_id = 'ctx_ev'",
		"UPDATE events SET type = 'vote'",
		"DELETE FROM events WHERE entry_id = 'ctx_ev'",
		"DELETE FROM events",
		"UPDATE revisions SET content = 'forged' WHERE entry_id = 'ctx_ev'",
		"UPDATE revisions SET content_digest = 'x' WHERE entry_id = 'ctx_ev'",
		"DELETE FROM revisions",
	} {
		err := s.Update(ctx, func(tx Tx) error { _, err := tx.(*txn).exec(ctx, q); return err })
		if !errors.Is(err, ErrAppendOnly) {
			t.Fatalf("%s: got %v, want ErrAppendOnly", q, err)
		}
	}
	// And for any other writer of the file: the triggers live in the schema.
	for _, q := range []string{"UPDATE events SET note = 'x'", "DELETE FROM events"} {
		if err := rawExec(t, s.Path(), q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("raw %s: %v", q, err)
		}
	}
	// REPLACE conflict resolution deletes the old row without firing delete triggers, so
	// every REPLACE form must be refused before it lands, in the store's transaction and
	// from a raw connection alike.
	forged := Digest("forged content")
	for _, q := range []string{
		"INSERT OR REPLACE INTO events (seq, workspace_id, entry_id, type, principal, at) VALUES (2, 'ws1', 'ctx_ev', 'vote', 'mallory', '2026-01-01T00:00:00.000000000Z')",
		"REPLACE INTO events (seq, workspace_id, entry_id, type, principal, at) VALUES (1, 'ws1', 'ctx_ev', 'propose', 'mallory', '2026-01-01T00:00:00.000000000Z')",
		"INSERT OR REPLACE INTO revisions (entry_id, revision, state, op, content, content_digest, content_bytes, author, author_key, created_at) " +
			"VALUES ('ctx_ev', 1, 'accepted', 'propose', 'forged content', '" + forged + "', 14, 'mallory', 'mallory', '2026-01-01T00:00:00.000000000Z')",
		"REPLACE INTO revisions (pk, entry_id, revision, state, op, content, content_digest, content_bytes, author, author_key, created_at) " +
			"VALUES (1, 'ctx_ev', 7, 'accepted', 'propose', 'forged content', '" + forged + "', 14, 'mallory', 'mallory', '2026-01-01T00:00:00.000000000Z')",
		"UPDATE OR REPLACE revisions SET pk = 99 WHERE entry_id = 'ctx_ev'",
	} {
		if err := s.Update(ctx, func(tx Tx) error { _, err := tx.(*txn).exec(ctx, q); return err }); !errors.Is(err, ErrAppendOnly) {
			t.Fatalf("in-tx %s: got %v, want ErrAppendOnly", q, err)
		}
		if err := rawExec(t, s.Path(), q); err == nil || !(strings.Contains(err.Error(), "append-only") || strings.Contains(err.Error(), "immutable")) {
			t.Fatalf("raw %s: %v", q, err)
		}
	}
	if rv, err := s.GetRevision(ctx, "ctx_ev", 1); err != nil || rv.Content != "content" || rv.Author != "alice" {
		t.Fatalf("revision 1 after REPLACE attempts = %+v %v", rv, err)
	}
	// Redaction is allowed only for a purged entry's events.
	if err := rawExec(t, s.Path(), "UPDATE events SET note = NULL, data = NULL, redacted = 1 WHERE entry_id = 'ctx_ev'"); err == nil {
		t.Fatal("redacted events of a live entry")
	}
	evs, _ := s.ListEvents(ctx, EventFilter{EntryID: "ctx_ev"})
	if len(evs) != 2 || evs[0].Type != EventPropose || evs[0].Principal != "alice" ||
		evs[1].Type != EventVote || evs[1].Principal != "bob" || evs[1].NewDigest != Digest("content") {
		t.Fatalf("events changed: %+v", evs)
	}
	// The session ledger is immutable the same way.
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.UpsertSession(ctx, SessionInput{ID: "sess-ev", WorkspaceID: "ws1"}, carol); err != nil {
			return err
		}
		_, err := tx.AppendSessionEvent(ctx, SessionEventInput{SessionID: "sess-ev", Kind: "prompt", Body: "original"}, carol)
		return err
	})
	ledger, _ := s.ListSessionEvents(ctx, SessionEventFilter{SessionID: "sess-ev"})
	replaceLedger := fmt.Sprintf("INSERT OR REPLACE INTO session_events (seq, session_id, workspace_id, kind, body, at) "+
		"VALUES (%d, 'sess-ev', 'ws1', 'prompt', 'rewritten', '2026-01-01T00:00:00.000000000Z')", ledger[0].Seq)
	if err := s.Update(ctx, func(tx Tx) error { _, err := tx.(*txn).exec(ctx, replaceLedger); return err }); !errors.Is(err, ErrAppendOnly) {
		t.Fatalf("ledger REPLACE: %v", err)
	}
	if err := rawExec(t, s.Path(), replaceLedger); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("raw ledger REPLACE: %v", err)
	}
	// Appending still works, including caller-authored events.
	mustUpdate(t, s, func(tx Tx) error {
		ev, err := tx.AppendEvent(ctx, EventInput{EntryID: "ctx_ev", Type: EventNote, Note: "checked on CI"}, carol)
		if err == nil && (ev.WorkspaceID != "ws1" || ev.Principal != "carol") {
			t.Errorf("appended = %+v", ev)
		}
		return err
	})
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.AppendEvent(ctx, EventInput{WorkspaceID: "ws1", Type: "rewrite_history"}, carol)
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown event type: %v", err)
	}
}

func TestPeerVerifiedNeedsDistinctPeers(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "ctx_pv", "Rule", "never push to main")
	peer := Promotion{Status: StatusVerified, Promoted: true, VerificationMode: ModePeerVerified}
	tryPeer := func() error {
		return s.Update(ctx, func(tx Tx) error { _, err := tx.SetPromotion(ctx, "ctx_pv", peer, bob); return err })
	}
	// The author's own approval, the open-mode identity and one peer are not two peers.
	vote(t, s, "ctx_pv", alice, VerdictApprove)
	vote(t, s, "ctx_pv", anon, VerdictApprove)
	vote(t, s, "ctx_pv", Actor{Principal: " BOB "}, VerdictApprove)
	vote(t, s, "ctx_pv", bob, VerdictApprove) // same principal, case- and space-insensitive
	if err := tryPeer(); !errors.Is(err, ErrInvariant) {
		t.Fatalf("peer_verified with one peer: %v", err)
	}
	tl, _ := s.Tally(ctx, "ctx_pv", 0)
	if tl.Approvals != 3 || tl.PeerApprovals != 1 || !tl.AuthorApproved || !tl.OpenModeApproved {
		t.Fatalf("tally = %+v", tl)
	}
	votes, _ := s.ListVotes(ctx, "ctx_pv", 0)
	if len(votes) != 3 || votes[2].Principal != "bob" || votes[2].Ordinal != 2 {
		t.Fatalf("votes = %+v", votes)
	}
	// The raw schema refuses it too, for writers that bypass the Go layer.
	if err := rawExec(t, s.Path(), "UPDATE entries SET status = 'verified', promoted = 1, verification_mode = 'peer_verified' WHERE id = 'ctx_pv'"); err == nil {
		t.Fatal("raw write labelled an entry peer_verified")
	}
	for _, bad := range []VoteInput{
		{EntryID: "ctx_pv", Verdict: "maybe"},
		{EntryID: "ctx_pv", Verdict: VerdictDuplicateOf},
		{EntryID: "ctx_pv", Verdict: VerdictDuplicateOf, Target: "ctx_pv"},
		{EntryID: "ctx_pv", Verdict: VerdictApprove, Target: "ctx_other"},
		{EntryID: "ctx_pv", Verdict: VerdictApprove, Revision: 9},
	} {
		err := s.Update(ctx, func(tx Tx) error { _, err := tx.RecordVote(ctx, bad, carol); return err })
		if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrNotFound) {
			t.Fatalf("RecordVote(%+v) = %v", bad, err)
		}
	}
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.InsertEntry(ctx, NewEntry{ID: "ctx_elsewhere", WorkspaceID: "ws2", Content: "x"}, alice)
		return err
	})
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.RecordVote(ctx, VoteInput{EntryID: "ctx_pv", Verdict: VerdictDuplicateOf, Target: "ctx_elsewhere"}, carol)
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-workspace duplicate_of: %v", err)
	}
	vote(t, s, "ctx_pv", carol, VerdictApprove)
	e := promotePeer(t, s, "ctx_pv")
	if !e.Promoted || e.VerificationMode != ModePeerVerified || e.PromotedAt == "" || e.ValidFrom == "" || !e.Served(time.Now()) {
		t.Fatalf("promoted = %+v", e)
	}
	// A verdict flip without re-reconciliation cannot commit: nothing unverified may
	// look peer-verified.
	err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.RecordVote(ctx, VoteInput{EntryID: "ctx_pv", Verdict: VerdictReject}, carol)
		return err
	})
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("vote flip under peer_verified: %v", err)
	}
	if tl, _ := s.Tally(ctx, "ctx_pv", 0); tl.PeerApprovals != 2 {
		t.Fatalf("rolled-back vote leaked: %+v", tl)
	}
	// Flipping and demoting in one transaction is fine.
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.RecordVote(ctx, VoteInput{EntryID: "ctx_pv", Verdict: VerdictReject}, carol); err != nil {
			return err
		}
		_, err := tx.SetPromotion(ctx, "ctx_pv", Promotion{Status: StatusPending}, carol)
		return err
	})
	vote(t, s, "ctx_pv", carol, VerdictApprove)
	promotePeer(t, s, "ctx_pv")

	// Changing the served content clears promotion; old votes bind to the old revision.
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AppendRevision(ctx, RevisionInput{EntryID: "ctx_pv", BaseRevision: 1, Op: "edit",
			Content: "never force-push to main", Activate: true}, alice)
		return err
	})
	e, _ = s.GetEntry(ctx, "ctx_pv")
	if e.Promoted || e.Status != StatusPending || e.VerificationMode != "" || e.Revision != 2 {
		t.Fatalf("edit kept promotion: %+v", e)
	}
	if err := tryPeer(); !errors.Is(err, ErrInvariant) {
		t.Fatalf("revision-1 votes promoted revision 2: %v", err)
	}
	evs, _ := s.ListEvents(ctx, EventFilter{EntryID: "ctx_pv", Types: []string{EventDemote}})
	if len(evs) != 2 {
		t.Fatalf("demote events = %d", len(evs))
	}
	// Validation of the promotion shape itself.
	for _, p := range []Promotion{
		{Status: StatusVerified, Promoted: true},
		{Status: StatusPending, Promoted: true, VerificationMode: ModeSingleAgent},
		{Status: StatusVerified},
		{Status: StatusPending, VerificationMode: ModeSingleAgent},
		{Status: "promoted"},
	} {
		if err := s.Update(ctx, func(tx Tx) error { _, err := tx.SetPromotion(ctx, "ctx_pv", p, bob); return err }); !errors.Is(err, ErrInvalid) {
			t.Fatalf("SetPromotion(%+v) = %v", p, err)
		}
	}
}

func TestUpdateRollsBackOnErrorAndPanic(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, nil)
	boom := errors.New("boom")
	err := s.Update(ctx, func(tx Tx) error {
		if _, err := tx.InsertEntry(ctx, NewEntry{ID: "ctx_rb", WorkspaceID: "ws1", Content: "x"}, alice); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	func() {
		defer func() { _ = recover() }()
		_ = s.Update(ctx, func(tx Tx) error {
			_, _ = tx.InsertEntry(ctx, NewEntry{ID: "ctx_rb", WorkspaceID: "ws1", Content: "x"}, alice)
			panic("mid-transaction")
		})
	}()
	if n := countRows(t, s, "SELECT count(*) FROM entries"); n != 0 {
		t.Fatalf("rolled-back entries persisted: %d", n)
	}
	if _, err := s.GetEntry(ctx, "ctx_rb"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetEntry = %v", err)
	}
	// the writer connection is healthy after the panic
	propose(t, s, "ctx_after", "t", "c")
}

func TestFTSRanksTitleContentAndAnchors(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	mustUpdate(t, s, func(tx Tx) error {
		for _, in := range []NewEntry{
			{ID: "m_title", Title: "Retry backoff policy", Content: "Use capped exponential waits for flaky calls."},
			{ID: "m_body", Title: "HTTP client notes", Content: "The client applies a retry with jittered backoff on 503 responses."},
			{ID: "m_anchor", Title: "Store layout", Content: "Governance state lives in SQLite.",
				Paths: []string{"api-go/internal/govstore/store.go"}},
			{ID: "m_other", Title: "Unrelated", Content: "Frontend lint uses biome."},
			{ID: "m_stem", Title: "Test runner", Content: "Running the suites takes two minutes."},
		} {
			in.WorkspaceID = "ws1"
			if _, err := tx.InsertEntry(ctx, in, alice); err != nil {
				return err
			}
		}
		_, err := tx.InsertEntry(ctx, NewEntry{ID: "m_elsewhere", WorkspaceID: "ws2", Title: "Retry backoff", Content: "retry"}, alice)
		return err
	})
	hits, err := s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "retry backoff"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].EntryID != "m_title" || hits[1].EntryID != "m_body" || !(hits[0].Score > hits[1].Score) {
		t.Fatalf("ranked hits = %+v", hits)
	}
	if hits, _ = s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "govstore store.go"}); len(hits) != 1 || hits[0].EntryID != "m_anchor" {
		t.Fatalf("anchor hits = %+v", hits)
	}
	if hits, _ = s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "run"}); len(hits) != 1 || hits[0].EntryID != "m_stem" {
		t.Fatalf("stemmed hits = %+v", hits)
	}
	// FTS5 syntax in user text is inert.
	for _, q := range []string{`"unbalanced`, `NEAR(retry backoff)`, `retry* OR -x AND :col`, `)(`, ``} {
		if _, err := s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: q}); err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
	}
	// Served-only hides unpromoted entries; promotion and edits re-index.
	if hits, _ = s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "retry", ServedOnly: true}); len(hits) != 0 {
		t.Fatalf("served-only returned pending: %+v", hits)
	}
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.RecordVote(ctx, VoteInput{EntryID: "m_body", Verdict: VerdictApprove}, bob); err != nil {
			return err
		}
		if _, err := tx.RecordVote(ctx, VoteInput{EntryID: "m_body", Verdict: VerdictApprove}, carol); err != nil {
			return err
		}
		_, err := tx.SetPromotion(ctx, "m_body", Promotion{Status: StatusVerified, Promoted: true, VerificationMode: ModePeerVerified}, carol)
		return err
	})
	if hits, _ = s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "retry", ServedOnly: true}); len(hits) != 1 || hits[0].EntryID != "m_body" || hits[0].VerificationMode != ModePeerVerified {
		t.Fatalf("served hits = %+v", hits)
	}
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AppendRevision(ctx, RevisionInput{EntryID: "m_other", BaseRevision: 1, Op: "edit",
			Content: "Frontend lint uses eslint with a retry-free config.", Activate: true}, alice)
		return err
	})
	if hits, _ = s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "biome"}); len(hits) != 0 {
		t.Fatalf("stale index after edit: %+v", hits)
	}
	if hits, _ = s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "eslint"}); len(hits) != 1 {
		t.Fatalf("edit not indexed: %+v", hits)
	}
	mustUpdate(t, s, func(tx Tx) error { return tx.Purge(ctx, "m_title", "test", alice) })
	if hits, _ = s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "capped exponential", IncludeInactive: true}); len(hits) != 0 {
		t.Fatalf("purged entry still searchable: %+v", hits)
	}
	var integrity string
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.(*txn).exec(ctx, "INSERT INTO memory_fts (memory_fts) VALUES ('integrity-check')")
		return err
	}); err != nil {
		integrity = err.Error()
	}
	if integrity != "" {
		t.Fatalf("fts integrity: %s", integrity)
	}
}

func TestLifecycleSupersedeRetractMergeExpiryPurge(t *testing.T) {
	ctx := context.Background()
	clk := newClock()
	s := openTestStore(t, clk)
	for _, id := range []string{"old", "new", "dup", "gone"} {
		propose(t, s, id, "title "+id, "content of "+id, "docs/"+id+".md")
		vote(t, s, id, bob, VerdictApprove)
		vote(t, s, id, carol, VerdictApprove)
		promotePeer(t, s, id)
	}
	// Supersede: old is closed, never deleted, and linked.
	mustUpdate(t, s, func(tx Tx) error {
		return tx.Supersede(ctx, SupersedeInput{NewID: "new", OldIDs: []string{"old"}, Reason: "rewritten"}, Actor{Principal: "carol", HeadSHA: "abc123"})
	})
	old, _ := s.GetEntry(ctx, "old")
	if old.Lifecycle != LifecycleSuperseded || old.SupersededBy != "new" || old.InvalidatedAt == "" || old.InvalidatedCommit != "abc123" || old.ExpiredAt == "" || old.Served(time.Now()) {
		t.Fatalf("superseded = %+v", old)
	}
	if !old.Promoted {
		t.Fatal("supersession must keep the historical promotion")
	}
	rels, _ := s.ListRelations(ctx, "old")
	if len(rels) != 1 || rels[0].FromID != "new" || rels[0].Kind != RelationSupersedes {
		t.Fatalf("relations = %+v", rels)
	}
	active, _ := s.ListEntries(ctx, EntryFilter{WorkspaceID: "ws1", ServedOnly: true})
	if len(active) != 3 {
		t.Fatalf("served = %d", len(active))
	}
	all, _ := s.ListEntries(ctx, EntryFilter{WorkspaceID: "ws1", Lifecycles: []string{"*"}})
	if len(all) != 4 {
		t.Fatalf("all = %d", len(all))
	}
	if err := s.Update(ctx, func(tx Tx) error {
		return tx.Supersede(ctx, SupersedeInput{NewID: "new", OldIDs: []string{"old"}}, carol)
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("double supersede: %v", err)
	}
	// Merge and retract, then restore.
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.Transition(ctx, "dup", TransitionInput{To: LifecycleMerged, Target: "new"}, carol)
		return err
	})
	dup, _ := s.GetEntry(ctx, "dup")
	if dup.Lifecycle != LifecycleMerged || dup.MergedInto != "new" {
		t.Fatalf("merged = %+v", dup)
	}
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.Transition(ctx, "new", TransitionInput{To: LifecycleRetracted, Reason: "wrong"}, carol)
		return err
	})
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.Transition(ctx, "old", TransitionInput{To: LifecycleActive}, carol)
		return err
	})
	old, _ = s.GetEntry(ctx, "old")
	if old.Lifecycle != LifecycleActive || old.SupersededBy != "" || old.InvalidatedAt != "" || !old.Served(time.Now()) {
		t.Fatalf("restored = %+v", old)
	}
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.Transition(ctx, "old", TransitionInput{To: LifecyclePurged}, carol)
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("purge via Transition: %v", err)
	}
	// Expiry hides without deleting.
	expires := clk.Now().Add(time.Hour).Format(time.RFC3339Nano)
	mustUpdate(t, s, func(tx Tx) error { _, err := tx.SetExpiry(ctx, "old", expires, carol); return err })
	if served, _ := s.ListEntries(ctx, EntryFilter{WorkspaceID: "ws1", ServedOnly: true}); len(served) != 2 {
		t.Fatalf("before expiry served = %d", len(served))
	}
	clk.Advance(2 * time.Hour)
	if served, _ := s.ListEntries(ctx, EntryFilter{WorkspaceID: "ws1", ServedOnly: true}); len(served) != 1 {
		t.Fatalf("after expiry served = %d", len(served))
	}
	if _, err := s.GetEntry(ctx, "old"); err != nil {
		t.Fatalf("expired entry not fetchable by id: %v", err)
	}
	mustUpdate(t, s, func(tx Tx) error { _, err := tx.SetTier(ctx, "gone", "core", carol); return err })
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.SetClassification(ctx, "gone", Classification{Kind: "constraint", Topic: "ops/secrets", Tags: []string{"infra"}}, carol)
		return err
	})
	// Purge leaves a digest tombstone and no text.
	secretDigest := Digest("content of gone")
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.RecordOutcome(ctx, OutcomeInput{EntryID: "gone", Outcome: OutcomeHelpful, Note: "saw the secret"}, bob); err != nil {
			return err
		}
		return tx.Purge(ctx, "gone", "contained a credential", Actor{Principal: "admin"})
	})
	gone, _ := s.GetEntry(ctx, "gone")
	if gone.Lifecycle != LifecyclePurged || gone.Title != "" || gone.Promoted || gone.ContentDigest != secretDigest || gone.Tags != nil {
		t.Fatalf("purged entry = %+v", gone)
	}
	rv, _ := s.GetRevision(ctx, "gone", 1)
	if !rv.ContentDropped || rv.Content != "" || rv.ContentDigest != secretDigest || rv.ContentDroppedReason != "purge" || rv.Title != "" {
		t.Fatalf("purged revision = %+v", rv)
	}
	if c, _ := s.EntryContents(ctx, []string{"gone"}); c["gone"].Content != "" || c["gone"].Withheld == "" {
		t.Fatalf("purged content served: %+v", c)
	}
	if n := countRows(t, s, "SELECT count(*) FROM anchors WHERE entry_id = 'gone'"); n != 0 {
		t.Fatalf("anchors survived purge: %d", n)
	}
	evs, _ := s.ListEvents(ctx, EventFilter{EntryID: "gone"})
	last := evs[len(evs)-1]
	if last.Type != EventPurge || last.OldDigest != secretDigest || last.Redacted {
		t.Fatalf("purge event = %+v", last)
	}
	for _, ev := range evs[:len(evs)-1] {
		if !ev.Redacted || ev.Note != "" || len(ev.Data) != 0 {
			t.Fatalf("unredacted event after purge: %+v", ev)
		}
	}
	if outs, _ := s.ListOutcomes(ctx, "gone"); len(outs) != 1 || outs[0].Note != "" {
		t.Fatalf("outcome note survived purge: %+v", outs)
	}
	mustUpdate(t, s, func(tx Tx) error { return tx.Purge(ctx, "gone", "again", Actor{Principal: "admin"}) })
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.RecordVote(ctx, VoteInput{EntryID: "gone", Verdict: VerdictApprove}, bob)
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("vote on purged entry: %v", err)
	}
}

func TestAnchorsBaselinesAndDrift(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "a1", "t", "c", "src/a.go", "src/b.go")
	propose(t, s, "a2", "t", "c", "src/a.go")
	mustUpdate(t, s, func(tx Tx) error {
		return tx.SetBaselines(ctx, "a1", []Baseline{
			{Kind: AnchorPath, Value: "src/a.go", State: BaselineHash, Hash: "h-a", BaselineKind: "file"},
			{Kind: AnchorPath, Value: "src/b.go", State: BaselineMissing, BaselineKind: "file"},
			{Kind: AnchorPath, Value: "src/c.go", State: BaselineHash, Hash: "h-c", BaselineKind: "file"},
		}, "c0ffee1", alice)
	})
	anchors, _ := s.ListAnchors(ctx, "a1")
	if len(anchors) != 3 || anchors[2].Value != "src/c.go" || anchors[2].Declared || anchors[1].BaselineState != BaselineMissing || anchors[0].BaselineCommit != "c0ffee1" {
		t.Fatalf("anchors = %+v", anchors)
	}
	hits, _ := s.EntriesByAnchor(ctx, AnchorQuery{WorkspaceID: "ws1", Kind: AnchorPath, Values: []string{"src/a.go", "src/c.go"}})
	if len(hits) != 3 || hits[0].Fanout != 2 || hits[2].Value != "src/c.go" || hits[2].Fanout != 1 {
		t.Fatalf("anchor hits = %+v", hits)
	}
	var n int
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		n, err = tx.MarkStale(ctx, "a1", []AnchorRef{{AnchorPath, "src/a.go"}}, "beef", bob)
		return err
	})
	e, _ := s.GetEntry(ctx, "a1")
	if n != 1 || !e.NeedsReverify || e.StaleSince == "" {
		t.Fatalf("after drift n=%d entry=%+v", n, e)
	}
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		n, err = tx.MarkStale(ctx, "a1", []AnchorRef{{AnchorPath, "src/a.go"}}, "beef2", bob)
		return err
	})
	evs, _ := s.ListEvents(ctx, EventFilter{EntryID: "a1", Types: []string{EventStaleObserved}})
	if n != 0 || len(evs) != 1 {
		t.Fatalf("repeated drift re-logged: n=%d events=%d", n, len(evs))
	}
	if reverify, _ := s.ListEntries(ctx, EntryFilter{WorkspaceID: "ws1", NeedsReverify: true}); len(reverify) != 1 {
		t.Fatalf("needs_reverify list = %+v", reverify)
	}
	mustUpdate(t, s, func(tx Tx) error { return tx.ClearDrift(ctx, "a1", bob) })
	e, _ = s.GetEntry(ctx, "a1")
	if e.NeedsReverify || e.StaleSince != "" {
		t.Fatalf("drift not cleared: %+v", e)
	}
	mustUpdate(t, s, func(tx Tx) error {
		return tx.ReplaceAnchors(ctx, "a1", []string{"src/z.go"}, []AnchorInput{{Kind: AnchorSymbol, Value: "pkg.Func"}}, alice)
	})
	if hits, _ := s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "Func"}); len(hits) != 1 || hits[0].EntryID != "a1" {
		t.Fatalf("replaced anchors not indexed: %+v", hits)
	}
	if err := s.Update(ctx, func(tx Tx) error {
		return tx.ReplaceAnchors(ctx, "a1", nil, []AnchorInput{{Kind: "vibes", Value: "x"}}, alice)
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad anchor kind: %v", err)
	}
}

func TestClaimsAndConflicts(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "c1", "node", "use node 20")
	propose(t, s, "c2", "node", "use node 22")
	propose(t, s, "c3", "node", "node 22 again")
	mustUpdate(t, s, func(tx Tx) error {
		for _, c := range []ClaimInput{
			{EntryID: "c1", Subject: "runtime:node", Predicate: PredicatePinnedTo, Object: "20"},
			{EntryID: "c2", Subject: "runtime:node", Predicate: PredicatePinnedTo, Object: "22"},
			{EntryID: "c3", Subject: "runtime:node", Predicate: PredicatePinnedTo, Object: "22"},
			{EntryID: "c3", Subject: "runtime:node", Predicate: PredicatePinnedTo, Object: "22"}, // idempotent
		} {
			if _, err := tx.AddClaim(ctx, c, alice); err != nil {
				return err
			}
		}
		return nil
	})
	conflicts, err := s.ConflictingClaims(ctx, "ws1", "runtime:node", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 2 {
		t.Fatalf("conflicts = %+v", conflicts)
	}
	claims, _ := s.ListClaims(ctx, ClaimFilter{WorkspaceID: "ws1", Subject: "runtime:node"})
	if len(claims) != 3 {
		t.Fatalf("claims = %+v", claims)
	}
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.Transition(ctx, "c1", TransitionInput{To: LifecycleArchived}, alice)
		return err
	})
	if conflicts, _ = s.ConflictingClaims(ctx, "ws1", "runtime:node", PredicatePinnedTo); len(conflicts) != 0 {
		t.Fatalf("archived claim still conflicts: %+v", conflicts)
	}
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.AddClaim(ctx, ClaimInput{EntryID: "c1", Subject: "x", Predicate: "likes", Object: "y"}, alice)
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("predicate outside the vocabulary: %v", err)
	}
	if err := s.Update(ctx, func(tx Tx) error { return tx.AddRelation(ctx, "c2", "c2", RelationRefines, alice) }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self relation: %v", err)
	}
	mustUpdate(t, s, func(tx Tx) error { return tx.AddRelation(ctx, "c3", "c2", RelationDuplicates, alice) })
	if rels, _ := s.ListRelations(ctx, "c2"); len(rels) != 1 || rels[0].Kind != RelationDuplicates {
		t.Fatalf("relations = %+v", rels)
	}
}

func TestSessionsLedgerAndTranscriptSearch(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	agent := Actor{Principal: "agent-1"}
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.UpsertSession(ctx, SessionInput{ID: "sess-1", WorkspaceID: "ws1", Client: "claude-code",
			AgentID: "main", ExecutionKey: "claude-code:sess-1:agent:main", Branch: "main"}, agent); err != nil {
			return err
		}
		for _, in := range []SessionEventInput{
			{SessionID: "sess-1", Kind: "prompt", Role: "user", Body: "Why does the flaky integration test fail on CI?"},
			{SessionID: "sess-1", Kind: "tool_call", Tool: "search", ArgsDigest: "d1", Status: "ok"},
			{SessionID: "sess-1", Kind: "transcript", Role: "assistant", Body: "The integration test races the migration; add a busy timeout.",
				RetentionClass: "transcript"},
		} {
			if _, err := tx.AppendSessionEvent(ctx, in, agent); err != nil {
				return err
			}
		}
		return nil
	})
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.UpsertSession(ctx, SessionInput{ID: "sess-1", WorkspaceID: "ws1"}, Actor{Principal: "mallory"})
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("session hijack: %v", err)
	}
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.AppendSessionEvent(ctx, SessionEventInput{SessionID: "sess-1", Kind: "prompt", Body: "x"}, Actor{Principal: "mallory"})
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign principal wrote a ledger row: %v", err)
	}
	evs, _ := s.ListSessionEvents(ctx, SessionEventFilter{SessionID: "sess-1"})
	if len(evs) != 3 || evs[0].BodyDigest != Digest("Why does the flaky integration test fail on CI?") || evs[1].Tool != "search" || evs[2].RetentionClass != "transcript" {
		t.Fatalf("ledger = %+v", evs)
	}
	hits, err := s.SearchTranscripts(ctx, TranscriptQuery{WorkspaceID: "ws1", Text: "integration test migration"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Seq != evs[2].Seq || !strings.Contains(hits[0].Snippet, "integration") {
		t.Fatalf("transcript hits = %+v", hits)
	}
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.(*txn).exec(ctx, "UPDATE session_events SET body = 'rewritten'")
		return err
	}); !errors.Is(err, ErrAppendOnly) {
		t.Fatalf("ledger rewrite: %v", err)
	}
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.EndSession(ctx, "sess-1", SessionEnded, "fixed the race", agent)
		return err
	})
	sess, _ := s.GetSession(ctx, "sess-1")
	if sess.Status != SessionEnded || sess.EndedAt == "" || sess.ExecutionKey != "claude-code:sess-1:agent:main" {
		t.Fatalf("session = %+v", sess)
	}
	if list, _ := s.ListSessions(ctx, SessionFilter{WorkspaceID: "ws1"}); len(list) != 1 {
		t.Fatalf("sessions = %+v", list)
	}
}

func TestOutcomesAndDeliveries(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "o1", "t", "c")
	reader := Actor{Principal: "bob", SessionID: "sess-9"}
	mustUpdate(t, s, func(tx Tx) error {
		if n, err := tx.RecordDeliveries(ctx, []DeliveryInput{{EntryID: "o1", Surface: "recall"}}, reader); err != nil || n != 1 {
			return errors.Join(err, errors.New("delivery count"))
		}
		if _, err := tx.RecordOutcome(ctx, OutcomeInput{EntryID: "o1", Outcome: OutcomeHelpful}, reader); err != nil {
			return err
		}
		_, err := tx.RecordOutcome(ctx, OutcomeInput{EntryID: "o1", Outcome: OutcomeStaleHarm, Note: "stale path"}, reader)
		return err
	})
	sum, _ := s.SummarizeOutcomes(ctx, "o1")
	if sum.Helpful != 0 || sum.StaleHarm != 1 {
		t.Fatalf("summary = %+v (latest outcome per principal)", sum)
	}
	ds, _ := s.ListDeliveries(ctx, DeliveryFilter{SessionID: "sess-9"})
	if len(ds) != 1 || ds[0].Revision != 1 || ds[0].ContentDigest != Digest("c") || ds[0].Principal != "bob" {
		t.Fatalf("deliveries = %+v", ds)
	}
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.RecordDeliveries(ctx, []DeliveryInput{{EntryID: "nope", Surface: "recall"}}, reader)
		return err
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delivery of a missing entry: %v", err)
	}
}

func TestGrantsCollectionsAndApplicability(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "g1", "t", "c")
	admin := Actor{Principal: "admin"}
	var col Collection
	var g Grant
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		if col, err = tx.CreateCollection(ctx, CollectionInput{Name: "team conventions", OwnerWorkspace: "ws1",
			Policy: map[string]string{"verify": "recheck_here"}}, admin); err != nil {
			return err
		}
		g, err = tx.Grant(ctx, GrantInput{SourceWorkspace: "ws1", TargetWorkspace: "ws2", Permission: PermissionRead}, admin)
		return err
	})
	mustUpdate(t, s, func(tx Tx) error {
		again, err := tx.Grant(ctx, GrantInput{SourceWorkspace: "ws1", TargetWorkspace: "ws2", Permission: PermissionRead}, admin)
		if err == nil && again.ID != g.ID {
			t.Errorf("grant not idempotent: %s vs %s", again.ID, g.ID)
		}
		return err
	})
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.Grant(ctx, GrantInput{SourceWorkspace: "ws1", TargetWorkspace: "ws2", Permission: PermissionReadWrite}, admin)
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("silent escalation: %v", err)
	}
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.Grant(ctx, GrantInput{SourceWorkspace: "ws1", CollectionID: col.ID, Permission: PermissionReadWrite}, admin); err != nil {
			return err
		}
		if _, err := tx.Revoke(ctx, g.ID, admin); err != nil {
			return err
		}
		_, err := tx.SetApplicability(ctx, "g1", "ws2", ApplicabilityForeignUnchecked, admin)
		return err
	})
	active, _ := s.ListGrants(ctx, GrantFilter{SourceWorkspace: "ws1"})
	history, _ := s.ListGrants(ctx, GrantFilter{SourceWorkspace: "ws1", IncludeRevoked: true})
	if len(active) != 1 || active[0].CollectionID != col.ID || len(history) != 2 || history[0].RevokedAt == "" {
		t.Fatalf("grants active=%+v history=%+v", active, history)
	}
	app, _ := s.ListApplicability(ctx, "g1")
	if len(app) != 1 || app[0].State != ApplicabilityForeignUnchecked || app[0].Revision != 1 {
		t.Fatalf("applicability = %+v", app)
	}
	evs, _ := s.ListEvents(ctx, EventFilter{WorkspaceID: "ws1", Types: []string{EventGrant, EventRevoke, EventCollection}})
	if len(evs) != 4 {
		t.Fatalf("sharing events = %+v", evs)
	}
	got, _ := s.GetCollection(ctx, col.ID)
	if got.Policy["verify"] != "recheck_here" {
		t.Fatalf("collection = %+v", got)
	}
}

func TestJobLeasesAndWatermarks(t *testing.T) {
	ctx := context.Background()
	clk := newClock()
	s := openTestStore(t, clk)
	worker := Actor{Principal: "worker-1"}
	var job Job
	var created bool
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		job, created, err = tx.EnqueueJob(ctx, JobInput{WorkspaceID: "ws1", Kind: JobDedupeCluster,
			ConflictSignature: "dedupe:a,b", CandidateIDs: []string{"a", "b"}, Instructions: "merge if same", MaxAttempts: 2}, worker)
		return err
	})
	if !created || job.State != JobQueued {
		t.Fatalf("enqueue = %+v %t", job, created)
	}
	mustUpdate(t, s, func(tx Tx) error {
		again, created, err := tx.EnqueueJob(ctx, JobInput{WorkspaceID: "ws1", Kind: JobDedupeCluster, ConflictSignature: "dedupe:a,b"}, worker)
		if err == nil && (created || again.ID != job.ID) {
			t.Errorf("enqueue not idempotent")
		}
		return err
	})
	claim := func(who Actor) (Job, bool) {
		var j Job
		var ok bool
		mustUpdate(t, s, func(tx Tx) error {
			var err error
			j, ok, err = tx.ClaimJob(ctx, ClaimRequest{WorkspaceID: "ws1", Lease: time.Minute}, who)
			return err
		})
		return j, ok
	}
	j1, ok := claim(worker)
	if !ok || j1.State != JobLeased || j1.Attempts != 1 || j1.LeaseVersion != 1 {
		t.Fatalf("claim = %+v", j1)
	}
	if _, ok := claim(Actor{Principal: "worker-2"}); ok {
		t.Fatal("a held lease was claimed twice")
	}
	// The holder crashes: after expiry another worker takes it over.
	clk.Advance(2 * time.Minute)
	j2, ok := claim(Actor{Principal: "worker-2"})
	if !ok || j2.LeaseOwner != "worker-2" || j2.Attempts != 2 || j2.LeaseVersion != 2 {
		t.Fatalf("reclaim = %+v", j2)
	}
	// The crashed holder's late completion loses the compare-and-set.
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.CompleteJob(ctx, Lease{JobID: job.ID, Owner: "worker-1", Version: 1}, nil, worker)
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale lease completed: %v", err)
	}
	// Out of attempts: the next expiry fails the job instead of relaunching it.
	clk.Advance(2 * time.Minute)
	if _, ok := claim(worker); ok {
		t.Fatal("job relaunched past max attempts")
	}
	failed, _ := s.GetJob(ctx, job.ID)
	if failed.State != JobFailed || !strings.Contains(failed.Error, "lease expired") {
		t.Fatalf("exhausted job = %+v", failed)
	}
	mustUpdate(t, s, func(tx Tx) error {
		again, created, err := tx.EnqueueJob(ctx, JobInput{WorkspaceID: "ws1", Kind: JobDedupeCluster, ConflictSignature: "dedupe:a,b"}, worker)
		if err == nil && (created || again.State != JobFailed) {
			t.Errorf("failed job relaunched by enqueue: %+v", again)
		}
		return err
	})
	// A normal success path with a result.
	mustUpdate(t, s, func(tx Tx) error {
		_, _, err := tx.EnqueueJob(ctx, JobInput{WorkspaceID: "ws1", Kind: JobInitSeed, ConflictSignature: "seed:AGENTS.md"}, worker)
		return err
	})
	j3, _ := claim(worker)
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.RenewLease(ctx, Lease{JobID: j3.ID, Owner: "worker-1", Version: j3.LeaseVersion}, time.Hour, worker); err != nil {
			return err
		}
		_, err := tx.CompleteJob(ctx, Lease{JobID: j3.ID, Owner: "worker-1", Version: j3.LeaseVersion}, map[string]any{"proposals": 3}, worker)
		return err
	})
	done, _ := s.GetJob(ctx, j3.ID)
	if done.State != JobDone || string(done.Result) != `{"proposals":3}` {
		t.Fatalf("done = %+v", done)
	}
	// Watermarks advance only by compare-and-set.
	mustUpdate(t, s, func(tx Tx) error { _, err := tx.StartWatermark(ctx, "ws1", "sess-1", worker); return err })
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AdvanceWatermark(ctx, WatermarkAdvance{WorkspaceID: "ws1", Source: "sess-1", ExpectVersion: 0,
			ReflectedThrough: "seq-40", TotalCount: 40, ReflectedCount: 40}, worker)
		return err
	})
	if err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.AdvanceWatermark(ctx, WatermarkAdvance{WorkspaceID: "ws1", Source: "sess-1", ExpectVersion: 0,
			ReflectedThrough: "seq-50", TotalCount: 50, ReflectedCount: 50}, worker)
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale watermark advance: %v", err)
	}
	wm, _ := s.GetWatermark(ctx, "ws1", "sess-1")
	if wm.Version != 1 || wm.ReflectedThrough != "seq-40" || wm.LastStartedAt == "" || wm.LastSucceededAt == "" {
		t.Fatalf("watermark = %+v", wm)
	}
}

func TestFeedbackCountersAndEvidenceMeta(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	mustUpdate(t, s, func(tx Tx) error {
		for range 3 {
			if _, err := tx.BumpFeedback(ctx, "ws1", FeedbackRetrieval, []string{"./a.go", "a.go", "b.go"}); err != nil {
				return err
			}
		}
		_, err := tx.BumpFeedback(ctx, "ws1", FeedbackVerify, []string{"a.go"})
		return err
	})
	fb, _ := s.GetFeedback(ctx, "ws1", []string{"a.go", "b.go", "c.go"})
	if fb["a.go"].RetrievalCount != 3 || fb["a.go"].VerifyCount != 1 || fb["b.go"].RetrievalCount != 3 || len(fb) != 2 {
		t.Fatalf("feedback = %+v", fb)
	}
	if err := s.Update(ctx, func(tx Tx) error { _, err := tx.BumpFeedback(ctx, "ws1", "vibes", []string{"a.go"}); return err }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad feedback kind: %v", err)
	}
	m := EvidenceMeta{Handle: "xm1.abc_DEF-123", WorkspaceID: "ws1", Tool: "search", CapturedAt: "2026-09-25T10:00:00Z",
		ExpiresAt: "2026-09-26T10:00:00Z", RawSHA256: Digest("raw"), RawBytes: 3, Projection: []byte(`{"reducer":"lines"}`)}
	mustUpdate(t, s, func(tx Tx) error {
		if err := tx.PutEvidenceMeta(ctx, m); err != nil {
			return err
		}
		return tx.PutEvidenceMeta(ctx, m) // identical: no-op
	})
	m2 := m
	m2.RawSHA256 = Digest("other")
	if err := s.Update(ctx, func(tx Tx) error { return tx.PutEvidenceMeta(ctx, m2) }); !errors.Is(err, ErrConflict) {
		t.Fatalf("handle reuse: %v", err)
	}
	mustUpdate(t, s, func(tx Tx) error { return tx.RevokeEvidence(ctx, m.Handle, alice) })
	got, _ := s.GetEvidenceMeta(ctx, m.Handle)
	if !got.Revoked || string(got.Projection) != `{"reducer":"lines"}` || got.CapturedAt != "2026-09-25T10:00:00.000000000Z" {
		t.Fatalf("evidence = %+v", got)
	}
	expired, _ := s.ExpiredEvidence(ctx, "2026-09-27T00:00:00Z", 10)
	if len(expired) != 1 {
		t.Fatalf("expired = %v", expired)
	}
	mustUpdate(t, s, func(tx Tx) error { _, err := tx.DeleteEvidenceMeta(ctx, expired); return err })
	if _, err := s.GetEvidenceMeta(ctx, m.Handle); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted evidence: %v", err)
	}
}

func TestRetentionIsBoundedAndKeepsTombstones(t *testing.T) {
	ctx := context.Background()
	clk := newClock()
	s := openTestStore(t, clk)
	agent := Actor{Principal: "agent-1", SessionID: "old-sess"}
	propose(t, s, "r1", "t", "version one")
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.UpsertSession(ctx, SessionInput{ID: "old-sess", WorkspaceID: "ws1"}, agent); err != nil {
			return err
		}
		for i := range 7 {
			if _, err := tx.AppendSessionEvent(ctx, SessionEventInput{SessionID: "old-sess", Kind: "prompt",
				Body: "old prompt about retention " + string(rune('a'+i))}, agent); err != nil {
				return err
			}
		}
		if _, err := tx.AppendSessionEvent(ctx, SessionEventInput{SessionID: "old-sess", Kind: "note", Body: "keep me",
			RetentionClass: KeepClass}, agent); err != nil {
			return err
		}
		if _, err := tx.RecordDeliveries(ctx, []DeliveryInput{{EntryID: "r1", Surface: "ground"}}, agent); err != nil {
			return err
		}
		if _, err := tx.AppendRevision(ctx, RevisionInput{EntryID: "r1", BaseRevision: 1, Op: "edit", Content: "version two", Activate: true}, alice); err != nil {
			return err
		}
		return tx.PutEvidenceMeta(ctx, EvidenceMeta{Handle: "xm1.old", WorkspaceID: "ws1", Tool: "search",
			CapturedAt: canonTime(clk.Now()), ExpiresAt: canonTime(clk.Now().Add(time.Hour)), RawSHA256: Digest("x"), RawBytes: 1})
	})
	eventsBefore := countRows(t, s, "SELECT count(*) FROM events")
	clk.Advance(400 * 24 * time.Hour)
	p := DefaultRetentionPolicy()
	p.RevisionContent = 180 * 24 * time.Hour
	p.BatchSize = 3 // several bounded batches
	p.Vacuum = true
	rep, err := s.ApplyRetention(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SessionEvents != 7 || rep.Deliveries != 1 || rep.Evidence != 1 || rep.RevisionContent != 1 || rep.Sessions != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if hits, _ := s.SearchTranscripts(ctx, TranscriptQuery{WorkspaceID: "ws1", Text: "retention"}); len(hits) != 0 {
		t.Fatalf("deleted ledger rows still indexed: %+v", hits)
	}
	if hits, _ := s.SearchTranscripts(ctx, TranscriptQuery{WorkspaceID: "ws1", Text: "keep"}); len(hits) != 1 {
		t.Fatalf("keep-class row lost: %+v", hits)
	}
	rv1, _ := s.GetRevision(ctx, "r1", 1)
	rv2, _ := s.GetRevision(ctx, "r1", 2)
	if !rv1.ContentDropped || rv1.ContentDigest != Digest("version one") || rv1.ContentDroppedReason != "retention" || rv2.Content != "version two" {
		t.Fatalf("revision retention: %+v / %+v", rv1, rv2)
	}
	if n := countRows(t, s, "SELECT count(*) FROM events"); n != eventsBefore {
		t.Fatalf("retention touched the event log: %d -> %d", eventsBefore, n)
	}
	if rep2, _ := s.ApplyRetention(ctx, p); rep2 != (RetentionReport{}) {
		t.Fatalf("second pass not idempotent: %+v", rep2)
	}
}

func TestBackupQuickCheckAndClose(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "b1", "t", "backed up")
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, dest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("backup over an existing file: %v", err)
	}
	b, err := Open(ctx, dest, Options{})
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer b.Close()
	if e, err := b.GetEntry(ctx, "b1"); err != nil || e.ContentDigest != Digest("backed up") {
		t.Fatalf("backup entry = %+v %v", e, err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.QuickCheck(ctx); err != nil {
		t.Fatalf("healthy store failed quick_check: %v", err)
	}
	_ = s.Close()
	if err := s.Update(ctx, func(Tx) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("update after close: %v", err)
	}
	// A damaged file fails quick_check on open.
	damaged := filepath.Join(t.TempDir(), "damaged.db")
	d, _ := Open(ctx, damaged, Options{})
	mustUpdate(t, d, func(tx Tx) error {
		for i := range 200 {
			if _, err := tx.InsertEntry(ctx, NewEntry{ID: "d" + string(rune('A'+i%26)) + string(rune('a'+i/26)), WorkspaceID: "ws1",
				Content: strings.Repeat("payload ", 200)}, alice); err != nil {
				return err
			}
		}
		return nil
	})
	_ = d.Checkpoint(ctx)
	_ = d.Close()
	f, err := os.OpenFile(damaged, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	junk := make([]byte, 4096*4)
	for i := range junk {
		junk[i] = 0xA5
	}
	if _, err := f.WriteAt(junk, 4096*6); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := Open(ctx, damaged, Options{QuickCheckOnOpen: true}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("damaged database: got %v, want ErrCorrupt", err)
	}
	// Without the open-time check the same damage is found by an explicit QuickCheck.
	d2, err := Open(ctx, damaged, Options{})
	if err != nil {
		t.Fatalf("open without quick_check: %v", err)
	}
	defer d2.Close()
	if err := d2.QuickCheck(ctx); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("QuickCheck on damaged database: %v", err)
	}
}

// A transaction touching more entries than the bounded invariant set still cannot
// commit a peer_verified label the votes no longer support.
func TestPeerVerifiedInvariantHoldsInLargeTransactions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "target", "t", "fact")
	vote(t, s, "target", bob, VerdictApprove)
	vote(t, s, "target", carol, VerdictApprove)
	promotePeer(t, s, "target")
	mustUpdate(t, s, func(tx Tx) error {
		for i := range maxTouched + 50 {
			if _, err := tx.InsertEntry(ctx, NewEntry{ID: fmt.Sprintf("bulk-%d", i), WorkspaceID: "ws1", Content: "x"}, alice); err != nil {
				return err
			}
		}
		return nil
	})
	err := s.Update(ctx, func(tx Tx) error {
		for i := range maxTouched + 50 {
			if _, err := tx.RecordVote(ctx, VoteInput{EntryID: fmt.Sprintf("bulk-%d", i), Verdict: VerdictApprove}, bob); err != nil {
				return err
			}
		}
		_, err := tx.RecordVote(ctx, VoteInput{EntryID: "target", Verdict: VerdictReject}, carol)
		return err
	})
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("large transaction broke the invariant: %v", err)
	}
	if n := countRows(t, s, "SELECT count(*) FROM votes WHERE principal = 'bob' AND entry_id LIKE 'bulk-%'"); n != 0 {
		t.Fatalf("rolled-back votes persisted: %d", n)
	}
}

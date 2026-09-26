// Package govstore is xMustard's transactional, cross-process-safe governance store
// (PAR-STORE-01). It keeps shared memory in one SQLite database in WAL mode, using the
// pure-Go modernc.org/sqlite driver. The database holds entries with their full
// revision and event history, votes, anchors, claims, relations, sessions, outcomes,
// grants, consolidation jobs, path feedback and evidence metadata.
//
// Rules the package enforces:
//   - Each process has one writer connection. Every write transaction starts with
//     BEGIN IMMEDIATE and waits up to busy_timeout for another process's lock, so the
//     API daemon and the ops CLI can write the same file without lost updates.
//   - A single read borrows the writer connection while nothing else holds it, so a
//     store that serves one request at a time keeps one connection, with one parsed
//     schema and one page cache, resident. A read that arrives while the writer is
//     busy goes to a small reader pool instead of waiting; reader connections open on
//     demand and close after ReaderIdleTimeout. View always uses the reader pool.
//   - Governance commits run with synchronous=FULL.
//   - Pages are never memory-mapped (mmap_size=0) and each connection's page cache is
//     capped, so resident memory stays small and bounded.
//   - Writes are O(change). A vote touches one row, never a whole file.
//   - History is append-only. Revision content is immutable. Events reject UPDATE and
//     DELETE, except purge redaction of free text.
//   - Purge leaves no text behind in the file once checkpointed: the writer runs with
//     secure_delete=ON, so freed pages are zeroed, and purge rewrites the search index.
//   - Some invariants hold whatever policy a caller applies. Changing the served
//     revision clears promotion. An entry is labelled peer_verified only while enough
//     distinct principals approve the revision it serves, not counting the entry's
//     author, that revision's author or the open-mode identity. Restoring a retracted,
//     superseded or merged entry starts a new vote epoch, and verdicts cast before it
//     stop counting.
//
// Governance policy (thresholds, open mode, roles) stays with the caller. The store
// provides transactional primitives: a caller reads, decides and writes inside one
// Update. Nothing consumes the package yet; WS-12 cuts governance over to it.
package govstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// OpenModeIdentity is the identity every unauthenticated caller collapses to in open
// mode. Its approval never counts as a peer's.
const OpenModeIdentity = "anonymous"

// Default resource limits. cache_size counts page bytes, but modernc's allocator puts
// each 4 KiB page and its header into an 8 KiB slot, so a cache really costs about
// twice its nominal size (measured by the RSS probe). The defaults below therefore
// cap the resident page caches at about 2 MiB in total: 1 MiB for the writer and
// 512 KiB for each of the two readers, inside the 2-4 MiB line of PAR-STORE-01. The
// writer's cache fills during a bulk write, so it sets most of the bulk peak: the
// probe measured 1 MiB less SQLite heap at 512 KiB than at 1 MiB nominal, with no
// measurable slowdown of a 100k-row insert (single runs on a loaded host).
const (
	DefaultCacheKiB       = 512
	DefaultReaderCacheKiB = 256
	// cacheSlotFactor is the resident cost of a page-cache byte under modernc's
	// power-of-two slot allocator.
	cacheSlotFactor          = 2
	DefaultMaxReaders        = 2
	DefaultBusyTimeout       = 5 * time.Second
	DefaultReaderIdleTimeout = 30 * time.Second
	// journalSizeLimit bounds the WAL file left on disk after a checkpoint.
	journalSizeLimit = 32 << 20
)

var (
	// ErrNotFound: the addressed row does not exist.
	ErrNotFound = errors.New("govstore: not found")
	// ErrConflict: a compare-and-set failed or a uniqueness rule was hit. A revision
	// conflict is a *ConflictError that carries the current revision and digest.
	ErrConflict = errors.New("govstore: conflict")
	// ErrInvalid: the input breaks a validation rule or a state-machine transition.
	ErrInvalid = errors.New("govstore: invalid input")
	// ErrNoChange: an edit would produce a revision identical to its base.
	ErrNoChange = errors.New("govstore: no change")
	// ErrInvariant: the transaction would leave a governance invariant broken.
	ErrInvariant = errors.New("govstore: governance invariant violated")
	// ErrAppendOnly: an attempt to rewrite or delete history.
	ErrAppendOnly = errors.New("govstore: history is append-only")
	// ErrBusy: another writer held the database longer than busy_timeout.
	ErrBusy = errors.New("govstore: database busy")
	// ErrClosed: the store was closed.
	ErrClosed = errors.New("govstore: store closed")
	// ErrSchemaTooNew: the file was migrated by a newer build than this one.
	ErrSchemaTooNew = errors.New("govstore: database schema is newer than this build")
	// ErrSchemaDrift: the live schema differs from the recorded fingerprint, or an
	// applied migration differs from the one this build embeds.
	ErrSchemaDrift = errors.New("govstore: schema drift")
	// ErrCorrupt: PRAGMA quick_check reported damage on open.
	ErrCorrupt = errors.New("govstore: database failed quick_check")
)

// ConflictError reports a failed compare-and-set on an entry's revision. It carries
// the entry's current accepted revision and digest so the caller can rebase.
type ConflictError struct {
	EntryID         string
	BaseRevision    int64
	CurrentRevision int64
	CurrentDigest   string
	HeadRevision    int64
	Reason          string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("govstore: conflict on %s: base revision %d, current revision %d (digest %s), head revision %d: %s",
		e.EntryID, e.BaseRevision, e.CurrentRevision, e.CurrentDigest, e.HeadRevision, e.Reason)
}

// Is makes errors.Is(err, ErrConflict) true for a *ConflictError.
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// Options tunes a store. The zero value gives the defaults above.
type Options struct {
	// CacheKiB caps the writer connection's page cache (PRAGMA cache_size=-N).
	CacheKiB int
	// ReaderCacheKiB caps each reader connection's page cache.
	ReaderCacheKiB int
	// MaxReaders bounds the read connection pool, which serves View and the reads that
	// arrive while the writer connection is busy.
	MaxReaders int
	// BusyTimeout is how long a writer waits for another process's write lock.
	BusyTimeout time.Duration
	// ReaderIdleTimeout closes idle reader connections, releasing their page caches.
	ReaderIdleTimeout time.Duration
	// QuickCheckOnOpen runs PRAGMA quick_check before Open returns. It reads the whole
	// file, so it is off by default; the daemon lifecycle decides when to call
	// QuickCheck instead (for example in the background after start, or in doctor).
	// The check runs on a short-lived connection with its own small page cache
	// (quickCheckCacheKiB), closed before Open returns, so what it adds to the
	// resident set is bounded by that cache and the code it touches, not by the file.
	// Measured on the RSS probe's 153 MiB store (macOS arm64, one run): Open() with
	// the check added 4.67 MiB ps-RSS (5.09 MiB by footprint plus machine code)
	// against about 2.4 MiB without it; on the writer connection it had cost
	// 5.7-6.3 MiB.
	QuickCheckOnOpen bool
	// Now overrides the clock, for tests.
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.CacheKiB <= 0 {
		o.CacheKiB = DefaultCacheKiB
	}
	if o.ReaderCacheKiB <= 0 {
		o.ReaderCacheKiB = DefaultReaderCacheKiB
	}
	if o.MaxReaders <= 0 {
		o.MaxReaders = DefaultMaxReaders
	}
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = DefaultBusyTimeout
	}
	if o.ReaderIdleTimeout <= 0 {
		o.ReaderIdleTimeout = DefaultReaderIdleTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Reader is every read the store offers. Outside a transaction each call reads its
// own consistent snapshot; inside View or Update all calls share one.
type Reader interface {
	EntryReader
	RevisionReader
	VoteReader
	EventReader
	AnchorReader
	ClaimReader
	SessionReader
	OutcomeReader
	GrantReader
	JobReader
	FeedbackReader
	EvidenceReader
	SearchReader
}

// Tx is a write transaction: every read plus every write. Methods are valid only
// inside the Update callback that received the Tx.
type Tx interface {
	Reader
	EntryWriter
	RevisionWriter
	VoteWriter
	EventWriter
	AnchorWriter
	ClaimWriter
	SessionWriter
	OutcomeWriter
	GrantWriter
	JobWriter
	FeedbackWriter
	EvidenceWriter
}

// Store is the governance store.
type Store interface {
	Reader
	// Update runs fn in one write transaction (BEGIN IMMEDIATE, synchronous=FULL
	// commit). fn's error, a panic, or a broken invariant rolls everything back.
	// Update is not re-entrant: the process has one writer connection, so an Update
	// started inside fn waits for it until ctx ends.
	Update(ctx context.Context, fn func(Tx) error) error
	// View runs fn against one read snapshot.
	View(ctx context.Context, fn func(Reader) error) error
	// ImportContextEntriesJSON migrates a workspace's legacy context_entries.json in
	// one transaction. It is idempotent.
	ImportContextEntriesJSON(ctx context.Context, workspaceID string, src io.Reader, opts ImportOptions) (ImportReport, error)
	// ExportContextEntriesJSON writes a workspace's active entries in the legacy shape.
	ExportContextEntriesJSON(ctx context.Context, workspaceID string, w io.Writer) error
	// ImportFeedbackJSON migrates a workspace's legacy agent_feedback.json.
	ImportFeedbackJSON(ctx context.Context, workspaceID string, src io.Reader) (int, error)
	// ApplyRetention deletes or compacts rows past their retention, in bounded batches.
	ApplyRetention(ctx context.Context, policy RetentionPolicy) (RetentionReport, error)
	// Backup writes a consistent copy of the database to dest (VACUUM INTO).
	Backup(ctx context.Context, dest string) error
	// Checkpoint copies the WAL into the database file and truncates it.
	Checkpoint(ctx context.Context) error
	// QuickCheck verifies the file's structure (PRAGMA quick_check).
	QuickCheck(ctx context.Context) error
	// SchemaInfo reports the schema version, fingerprint and file statistics.
	SchemaInfo(ctx context.Context) (SchemaInfo, error)
	// Close closes both connection pools.
	Close() error
}

// Actor is who performs a write and the repository state it saw. Writes to memory
// history (entries, revisions, votes, anchors, claims, relations, outcomes, grants)
// record it in the event log; ledger, job and evidence writes record it on their rows.
type Actor struct {
	Principal string // required: the authenticated principal id
	Owner     string // who operates the principal (PAR-PROV-05)
	Kind      string // human | agent
	SessionID string
	AgentID   string
	HeadSHA   string
	Branch    string
	Dirty     *bool
	Note      string
}

func (a Actor) validate() error {
	if strings.TrimSpace(a.Principal) == "" {
		return fmt.Errorf("%w: actor principal is required", ErrInvalid)
	}
	if len(a.Principal) > maxNameLen || strings.ContainsRune(a.Principal, 0) {
		return fmt.Errorf("%w: actor principal is malformed", ErrInvalid)
	}
	return nil
}

// SQLStore is the SQLite implementation of Store.
type SQLStore struct {
	path    string
	opts    Options
	writer  *sql.DB // exactly one connection: the process's single writer
	readers *sql.DB
	// writerSem is held by whoever uses the writer connection: a write transaction, a
	// check or checkpoint, or a read that borrowed it.
	writerSem chan struct{}
	closed    atomic.Bool
	closeMu   sync.Mutex

	fingerprint string
	version     int
	reader
}

var (
	_ Store = (*SQLStore)(nil)
	_ Tx    = (*txn)(nil)
)

// Open opens or creates the store at path, applies pending migrations and verifies
// the schema fingerprint.
func Open(ctx context.Context, path string, opts Options) (*SQLStore, error) {
	opts = opts.withDefaults()
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "?\x00") {
		return nil, fmt.Errorf("%w: store path %q", ErrInvalid, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Governance text is private: a new file is created 0600, and SQLite gives its
	// -wal and -shm files the same mode. An existing file keeps its mode.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	writer, err := sql.Open("sqlite", writerDSN(path, opts))
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)
	writer.SetConnMaxIdleTime(0)
	// Each open-time step retries on SQLITE_BUSY within the busy timeout: turning a new
	// file into WAL mode needs an exclusive lock, and that pragma can fail without
	// consulting the busy handler while another process opens the same new file.
	if err := retryBusy(ctx, opts.BusyTimeout, func() error { return mapErr(writer.PingContext(ctx)) }); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("govstore: open %s: %w", path, err)
	}
	s := &SQLStore{path: path, opts: opts, writer: writer, writerSem: make(chan struct{}, 1)}
	if err := retryBusy(ctx, opts.BusyTimeout, func() error {
		var err error
		s.version, s.fingerprint, err = migrate(ctx, writer, opts.Now)
		return err
	}); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("govstore: migrate %s: %w", path, err)
	}
	if opts.QuickCheckOnOpen {
		if err := retryBusy(ctx, opts.BusyTimeout, func() error { return quickCheckFile(ctx, path, opts) }); err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("govstore: check %s: %w", path, err)
		}
	}
	readers, err := sql.Open("sqlite", readerDSN(path, opts))
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	readers.SetMaxOpenConns(opts.MaxReaders)
	readers.SetMaxIdleConns(1)
	readers.SetConnMaxIdleTime(opts.ReaderIdleTimeout)
	s.readers = readers
	s.reader = reader{q: readers, borrow: s.borrowForRead, now: opts.Now}
	openStores.Add(1)
	return s, nil
}

// lockWriter takes the writer connection for a write, check or checkpoint, waiting
// for a borrowed read or another write to finish until ctx ends.
func (s *SQLStore) lockWriter(ctx context.Context) error {
	select {
	case s.writerSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *SQLStore) unlockWriter() { <-s.writerSem }

// borrowForRead returns the connection one read statement runs on and the function
// that hands it back. The writer connection is lent when it is idle, so sequential
// reads open no second connection. When it is busy the read goes to the reader pool
// and never waits behind a write.
func (s *SQLStore) borrowForRead(ctx context.Context) (queryer, func()) {
	select {
	case s.writerSem <- struct{}{}:
		c, err := s.writer.Conn(ctx)
		if err == nil {
			return c, func() {
				_ = c.Close()
				s.unlockWriter()
			}
		}
		s.unlockWriter()
	default:
	}
	return s.readers, func() {}
}

// writerDSN configures the single writer: WAL, FULL sync, immediate transactions,
// capped cache, no mmap, file-backed temp storage, incremental auto-vacuum (so
// retention can return pages) and secure delete. secure_delete=ON zeroes every page
// the writer frees, including the overflow pages of large text; FAST would leave
// freelist pages as they were, so purged or dropped text could stay in the file. The
// extra writes measured about 3% more WAL on a 20k-entry bulk insert.
func writerDSN(path string, o Options) string {
	pragmas := []string{
		"journal_mode(WAL)",
		"synchronous(FULL)",
		"foreign_keys(1)",
		"cache_size(-" + strconv.Itoa(o.CacheKiB) + ")",
		"mmap_size(0)",
		"temp_store(FILE)",
		"secure_delete(ON)",
		"journal_size_limit(" + strconv.Itoa(journalSizeLimit) + ")",
	}
	// _busy_timeout is applied before everything else the DSN does, including
	// _auto_vacuum, so no open-time statement can fail on a lock another process holds.
	q := "_busy_timeout=" + strconv.FormatInt(o.BusyTimeout.Milliseconds(), 10) +
		"&_txlock=immediate&_auto_vacuum=INCREMENTAL"
	for _, p := range pragmas {
		q += "&_pragma=" + p
	}
	return path + "?" + q
}

// readerDSN configures read connections: query_only, capped cache, no mmap.
func readerDSN(path string, o Options) string {
	pragmas := []string{
		"query_only(1)",
		"foreign_keys(1)",
		"cache_size(-" + strconv.Itoa(o.ReaderCacheKiB) + ")",
		"mmap_size(0)",
		"temp_store(FILE)",
	}
	q := "_busy_timeout=" + strconv.FormatInt(o.BusyTimeout.Milliseconds(), 10)
	for _, p := range pragmas {
		q += "&_pragma=" + p
	}
	return path + "?" + q
}

// retryBusy runs fn until it succeeds, fails with something other than ErrBusy, or
// the timeout passes, backing off from 5 ms to 100 ms.
func retryBusy(ctx context.Context, timeout time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	wait := 5 * time.Millisecond
	for {
		err := fn()
		if err == nil || !errors.Is(err, ErrBusy) || time.Now().Add(wait).After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 100*time.Millisecond)
	}
}

// quickCheckCacheKiB caps the page cache of the open-time quick_check connection.
const quickCheckCacheKiB = 256

// quickCheckFile runs quick_check on a dedicated read-only connection that is closed
// afterwards, so the check's page cache is small and released.
func quickCheckFile(ctx context.Context, path string, o Options) error {
	db, err := sql.Open("sqlite", path+"?_busy_timeout="+strconv.FormatInt(o.BusyTimeout.Milliseconds(), 10)+
		"&_pragma=query_only(1)&_pragma=mmap_size(0)&_pragma=temp_store(FILE)&_pragma=cache_size(-"+
		strconv.Itoa(quickCheckCacheKiB)+")")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return quickCheck(ctx, db)
}

func quickCheck(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrCorrupt, strings.Join(problems, "; "))
	}
	return nil
}

// QuickCheck runs PRAGMA quick_check on the writer connection, so its memory stays
// inside the writer's capped page cache. Writes wait while it runs. It returns
// ErrCorrupt with SQLite's findings when the file is damaged.
func (s *SQLStore) QuickCheck(ctx context.Context) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if err := s.lockWriter(ctx); err != nil {
		return err
	}
	defer s.unlockWriter()
	return quickCheck(ctx, s.writer)
}

// Path returns the database file path.
func (s *SQLStore) Path() string { return s.path }

// Close closes both pools. It is safe to call more than once.
func (s *SQLStore) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed.Swap(true) {
		return nil
	}
	openStores.Add(-1)
	err := s.readers.Close()
	if werr := s.writer.Close(); err == nil {
		err = werr
	}
	return err
}

// Update runs fn inside one write transaction.
func (s *SQLStore) Update(ctx context.Context, fn func(Tx) error) (err error) {
	if s.closed.Load() {
		return ErrClosed
	}
	if err := s.lockWriter(ctx); err != nil {
		return err
	}
	defer s.unlockWriter() // runs after the rollback below
	sqlTx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	t := &txn{
		reader:  reader{q: sqlTx, stmts: newStmtCache(sqlTx), now: s.opts.Now},
		tx:      sqlTx,
		touched: map[string]struct{}{},
	}
	defer func() {
		if p := recover(); p != nil {
			_ = sqlTx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = sqlTx.Rollback()
		}
	}()
	if err = fn(t); err != nil {
		return err
	}
	if err = t.checkInvariants(ctx); err != nil {
		return err
	}
	if err = sqlTx.Commit(); err != nil {
		return mapErr(err)
	}
	return nil
}

// View runs fn against one read snapshot on the reader pool. It never borrows the
// writer connection, so fn may call Update (which then sees a newer state than fn's
// snapshot).
func (s *SQLStore) View(ctx context.Context, fn func(Reader) error) error {
	if s.closed.Load() {
		return ErrClosed
	}
	sqlTx, err := s.readers.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return mapErr(err)
	}
	defer func() { _ = sqlTx.Rollback() }()
	return fn(&reader{q: sqlTx, now: s.opts.Now})
}

// Backup writes a consistent, compacted copy of the database to dest. dest must not
// exist. It runs on a dedicated connection so writers are never blocked. The copy holds
// all governance text, so it is created 0600 like the store; a backup taken before a
// purge still holds the purged text.
func (s *SQLStore) Backup(ctx context.Context, dest string) (err error) {
	if s.closed.Load() {
		return ErrClosed
	}
	// Create dest first, exclusively and private: this refuses an existing file without
	// a stat-then-create race, and VACUUM INTO accepts an empty existing file.
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: backup destination %s exists", ErrInvalid, dest)
		}
		return err
	}
	_ = f.Close()
	defer func() {
		if err != nil {
			_ = os.Remove(dest)
		}
	}()
	db, err := sql.Open("sqlite", s.path+"?_pragma=busy_timeout("+
		strconv.FormatInt(s.opts.BusyTimeout.Milliseconds(), 10)+")&_pragma=mmap_size(0)&_pragma=cache_size(-"+
		strconv.Itoa(s.opts.ReaderCacheKiB)+")")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return mapErr(err)
	}
	return syncFileAndDir(dest)
}

// syncFileAndDir makes a finished file and its directory entry durable.
func syncFileAndDir(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	// Some file systems cannot sync a directory; the file itself is already durable.
	_ = d.Sync()
	return nil
}

// Checkpoint runs a TRUNCATE checkpoint on the writer connection.
func (s *SQLStore) Checkpoint(ctx context.Context) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if err := s.lockWriter(ctx); err != nil {
		return err
	}
	defer s.unlockWriter()
	var busy, logFrames, checkpointed int
	if err := s.writer.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return mapErr(err)
	}
	if busy != 0 {
		return fmt.Errorf("%w: checkpoint blocked by an active reader", ErrBusy)
	}
	return nil
}

// SchemaInfo describes the open database.
type SchemaInfo struct {
	Version       int    `json:"version"`
	Fingerprint   string `json:"fingerprint"`
	PageSize      int64  `json:"page_size"`
	PageCount     int64  `json:"page_count"`
	FreelistCount int64  `json:"freelist_count"`
	JournalMode   string `json:"journal_mode"`
}

// SchemaInfo reports the schema version, fingerprint and page statistics.
func (s *SQLStore) SchemaInfo(ctx context.Context) (SchemaInfo, error) {
	info := SchemaInfo{Version: s.version, Fingerprint: s.fingerprint}
	if s.closed.Load() {
		return info, ErrClosed
	}
	for _, p := range []struct {
		pragma string
		dst    any
	}{
		{"page_size", &info.PageSize},
		{"page_count", &info.PageCount},
		{"freelist_count", &info.FreelistCount},
		{"journal_mode", &info.JournalMode},
	} {
		if err := s.queryRow(ctx, "PRAGMA "+p.pragma).Scan(p.dst); err != nil {
			return info, mapErr(err)
		}
	}
	return info, nil
}

// --- query plumbing -------------------------------------------------------------

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// stmtCache prepares each statement once per write transaction. Bulk writes (import,
// the 100k-row probe) reuse compiled statements instead of re-preparing per row.
// database/sql closes them when the transaction ends.
type stmtCache struct {
	tx    *sql.Tx
	stmts map[string]*sql.Stmt
}

func newStmtCache(tx *sql.Tx) *stmtCache {
	return &stmtCache{tx: tx, stmts: map[string]*sql.Stmt{}}
}

func (c *stmtCache) get(ctx context.Context, query string) (*sql.Stmt, error) {
	if st, ok := c.stmts[query]; ok {
		return st, nil
	}
	st, err := c.tx.PrepareContext(ctx, query)
	if err != nil {
		return nil, mapErr(err)
	}
	c.stmts[query] = st
	return st, nil
}

// rowScanner is the part of *sql.Row the store uses. errRow carries a prepare error.
type rowScanner interface{ Scan(dest ...any) error }

// resultRows is a result set that hands a borrowed connection back when it is closed.
// Every query's rows are closed (deferred) by the method that ran it.
type resultRows struct {
	*sql.Rows
	release func()
}

// Close closes the rows and returns the connection they ran on. It is idempotent.
func (r *resultRows) Close() error {
	err := r.Rows.Close()
	if r.release != nil {
		r.release()
		r.release = nil
	}
	return err
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// reader implements Reader over the store (each statement on the idle writer or the
// reader pool), a read snapshot or a write transaction.
type reader struct {
	q     queryer
	stmts *stmtCache
	// borrow, set on the store's own reader, picks the connection for one statement.
	borrow func(context.Context) (queryer, func())
	now    func() time.Time
}

func (r *reader) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if r.stmts != nil {
		st, err := r.stmts.get(ctx, query)
		if err != nil {
			return nil, err
		}
		res, err := st.ExecContext(ctx, args...)
		return res, mapErr(err)
	}
	res, err := r.q.ExecContext(ctx, query, args...)
	return res, mapErr(err)
}

func (r *reader) query(ctx context.Context, query string, args ...any) (*resultRows, error) {
	if r.stmts != nil {
		st, err := r.stmts.get(ctx, query)
		if err != nil {
			return nil, err
		}
		rows, err := st.QueryContext(ctx, args...)
		if err != nil {
			return nil, mapErr(err)
		}
		return &resultRows{Rows: rows}, nil
	}
	q, release := r.conn(ctx)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		release()
		return nil, mapErr(err)
	}
	return &resultRows{Rows: rows, release: release}, nil
}

func (r *reader) queryRow(ctx context.Context, query string, args ...any) rowScanner {
	if r.stmts != nil {
		st, err := r.stmts.get(ctx, query)
		if err != nil {
			return errRow{err}
		}
		return &mappedRow{row: st.QueryRowContext(ctx, args...)}
	}
	q, release := r.conn(ctx)
	return &mappedRow{row: q.QueryRowContext(ctx, query, args...), release: release}
}

// conn picks the connection for one statement outside a transaction.
func (r *reader) conn(ctx context.Context) (queryer, func()) {
	if r.borrow != nil {
		return r.borrow(ctx)
	}
	return r.q, func() {}
}

// mappedRow maps driver errors and hands a borrowed connection back once scanned.
type mappedRow struct {
	row     *sql.Row
	release func()
}

func (m *mappedRow) Scan(dest ...any) error {
	err := m.row.Scan(dest...)
	if m.release != nil {
		m.release()
		m.release = nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return mapErr(err)
}

func (r *reader) nowText() string { return canonTime(r.now()) }

// txn is a write transaction.
type txn struct {
	reader
	tx *sql.Tx
	// touched holds entries whose votes or state changed; checkInvariants re-verifies
	// them before commit. Past maxTouched ids the set is dropped and touchedAll makes
	// the commit check every peer_verified entry instead, so a bulk transaction never
	// grows memory with its row count.
	touched    map[string]struct{}
	touchedAll bool
}

// maxTouched bounds the per-transaction invariant set (a few hundred KiB at most).
const maxTouched = 1024

func (t *txn) touch(entryID string) {
	if t.touchedAll {
		return
	}
	if len(t.touched) >= maxTouched {
		t.touched, t.touchedAll = nil, true
		return
	}
	t.touched[entryID] = struct{}{}
}

// peerShortfall matches a peer_verified entry whose served revision lacks enough
// approvals, cast in the entry's current vote epoch, from principals other than the
// entry's author, the revision's author and the open-mode identity (the same rule as
// the entries_peer_verified_update trigger).
const peerShortfall = `e.verification_mode = 'peer_verified'
	AND (SELECT count(*) FROM votes v
	      WHERE v.entry_id = e.id AND v.revision = e.revision AND v.verdict = 'approve' AND v.epoch = e.vote_epoch
	        AND v.principal_key <> e.source_key AND v.principal_key <> ?
	        AND v.principal_key NOT IN (SELECT r.author_key FROM revisions r
	                                     WHERE r.entry_id = e.id AND r.revision = e.revision)) < e.required_verifications`

// checkInvariants rejects the commit if any touched entry is labelled peer_verified
// without enough distinct peer approvals on the revision it serves. The schema
// trigger guards entry writes; this catches vote changes after promotion.
func (t *txn) checkInvariants(ctx context.Context) error {
	if t.touchedAll {
		var bad string
		err := t.tx.QueryRowContext(ctx, "SELECT e.id FROM entries e WHERE "+peerShortfall+" LIMIT 1",
			OpenModeIdentity).Scan(&bad)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return mapErr(err)
		}
		return fmt.Errorf("%w: entry %s is peer_verified without enough distinct peer approvals", ErrInvariant, bad)
	}
	if len(t.touched) == 0 {
		return nil
	}
	ids := make([]string, 0, len(t.touched))
	for id := range t.touched {
		ids = append(ids, id)
	}
	for start := 0; start < len(ids); start += maxInArgs {
		chunk := ids[start:min(start+maxInArgs, len(ids))]
		args := make([]any, 0, len(chunk)+1)
		for _, id := range chunk {
			args = append(args, id)
		}
		args = append(args, OpenModeIdentity)
		var bad string
		err := t.tx.QueryRowContext(ctx, "SELECT e.id FROM entries e WHERE e.id IN ("+placeholders(len(chunk))+
			") AND "+peerShortfall+" LIMIT 1", args...).Scan(&bad)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return mapErr(err)
		}
		return fmt.Errorf("%w: entry %s is peer_verified without enough distinct peer approvals", ErrInvariant, bad)
	}
	return nil
}

// --- helpers ----------------------------------------------------------------------

const (
	maxNameLen = 256
	maxNoteLen = 16 << 10
	maxPathLen = 1024
	maxIDLen   = 200
	maxInArgs  = 400
	// maxListLimit bounds every list call: no path reads a whole table.
	maxListLimit     = 1000
	defaultListLimit = 100
	timeLayout       = "2006-01-02T15:04:05.000000000Z"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.:@-]+$`)

// validID rejects ids that could escape a namespace or smuggle delimiters (SEC-05).
func validID(kind, id string) error {
	if id == "" || len(id) > maxIDLen || strings.Contains(id, "..") || !idPattern.MatchString(id) {
		return fmt.Errorf("%w: %s id %q", ErrInvalid, kind, id)
	}
	return nil
}

func validName(kind, s string) error {
	if len(s) > maxNameLen || strings.ContainsRune(s, 0) {
		return fmt.Errorf("%w: %s is malformed", ErrInvalid, kind)
	}
	return nil
}

// principalKey is how principals are compared: trimmed and lower-cased, matching the
// legacy latest-verdict tally.
func principalKey(p string) string { return strings.ToLower(strings.TrimSpace(p)) }

// Digest is the full SHA-256 (hex) identity of a content version.
func Digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func canonTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// normTime canonicalizes an RFC 3339 timestamp. ok is false when s does not parse; the
// caller then decides whether to keep it verbatim.
func normTime(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s, false
	}
	return canonTime(t), true
}

// displayTime renders a stored canonical time as RFC 3339 with trailing zeros trimmed,
// which is exactly how the legacy store wrote times.
func displayTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// inputTime validates and canonicalizes a caller-supplied time; "" stays "".
func inputTime(field, s string) (string, error) {
	v, ok := normTime(s)
	if !ok {
		return "", fmt.Errorf("%w: %s %q is not RFC 3339", ErrInvalid, field, s)
	}
	return v, nil
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullBool(b *bool) any {
	if b == nil {
		return nil
	}
	return boolInt(*b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return defaultListLimit
	case n > maxListLimit:
		return maxListLimit
	default:
		return n
	}
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func rowsAffected(res sql.Result) int64 {
	if res == nil {
		return 0
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0
	}
	return n
}

// mapErr translates driver errors into the package's sentinel errors, keeping the
// driver message for diagnosis.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	msg := se.Error()
	switch se.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return fmt.Errorf("%w: %s", ErrBusy, msg)
	case sqlite3.SQLITE_CONSTRAINT:
		switch {
		case strings.Contains(msg, "append-only"), strings.Contains(msg, "immutable"):
			return fmt.Errorf("%w: %s", ErrAppendOnly, msg)
		case strings.Contains(msg, "peer_verified"):
			return fmt.Errorf("%w: %s", ErrInvariant, msg)
		case se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE, se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("%w: %s", ErrConflict, msg)
		default:
			return fmt.Errorf("%w: %s", ErrInvalid, msg)
		}
	}
	return err
}

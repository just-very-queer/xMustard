package govstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Backup, integrity check and restore of a store file (WS-58), for the ops CLI. They
// work on paths rather than an open store and never migrate a file, so a backup or a
// check is safe while the daemon (or an older or newer build) has the store open, and
// a restore works on a store too damaged to open.

// FileInfo describes a store file examined read-only.
type FileInfo struct {
	Path          string `json:"path"`
	Bytes         int64  `json:"bytes"`
	SchemaVersion int    `json:"schema_version"`
	Entries       int64  `json:"entries"`
	Fingerprint   string `json:"fingerprint"`
}

// RestoreReport says what RestoreFile replaced.
type RestoreReport struct {
	Source   string   `json:"source"`
	Store    string   `json:"store"`
	Restored FileInfo `json:"restored"`
	// SchemaVersion is the restored store's version after it was migrated to this build.
	SchemaVersion int `json:"schema_version"`
	// MovedAside are the replaced store's files, kept beside it (never deleted).
	MovedAside []string `json:"moved_aside"`
	// PreviousUnreadable says why SQLite could not read the replaced store, if it could not.
	PreviousUnreadable string `json:"previous_unreadable,omitempty"`
}

// sidecars are the files SQLite keeps beside a WAL-mode database, in the order a set is
// moved: a database file must never sit next to another database's write-ahead log,
// which SQLite would replay into it.
var sidecars = []string{"-wal", "-shm", ""}

// inspectDSN opens an existing file without migrating it: a small page cache, no mmap.
// It is a read-write connection that runs only reads (query_only for a check, VACUUM
// INTO for a backup): a mode=ro connection to a WAL-mode store that nothing has open
// leaves new -wal and -shm files behind, where the last read-write connection to close
// removes them.
func inspectDSN(path string, queryOnly bool) (string, error) {
	if strings.ContainsAny(path, "?\x00") {
		return "", fmt.Errorf("%w: store path %q", ErrInvalid, path)
	}
	dsn := path + "?_busy_timeout=" + strconv.FormatInt(DefaultBusyTimeout.Milliseconds(), 10) +
		"&_pragma=mmap_size(0)&_pragma=temp_store(FILE)&_pragma=cache_size(-" + strconv.Itoa(quickCheckCacheKiB) + ")"
	if queryOnly {
		dsn += "&_pragma=query_only(1)"
	}
	return dsn, nil
}

// VerifyFile checks that path is a store this build can open: the govstore application
// id, a schema no newer than this build's, a live schema matching its recorded
// fingerprint, and a clean PRAGMA quick_check. It reads on one query_only connection
// with a small page cache.
func VerifyFile(ctx context.Context, path string) (FileInfo, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return FileInfo{}, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	if !fi.Mode().IsRegular() {
		return FileInfo{}, fmt.Errorf("%w: %s is not a regular file", ErrInvalid, path)
	}
	info := FileInfo{Path: path, Bytes: fi.Size()}
	db, err := openReadOnly(path)
	if err != nil {
		return info, err
	}
	defer db.Close()
	if err := checkHeader(ctx, db, &info); err != nil {
		return info, err
	}
	if err := quickCheck(ctx, db); err != nil {
		return info, damaged(err)
	}
	if info.Fingerprint, err = schemaFingerprint(ctx, db); err != nil {
		return info, damaged(err)
	}
	var recorded string
	switch err := db.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key = 'schema_fingerprint'").Scan(&recorded); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return info, damaged(err)
	case recorded != info.Fingerprint:
		return info, fmt.Errorf("%w: %s has schema fingerprint %s, recorded %s", ErrSchemaDrift, path, info.Fingerprint, recorded)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM entries").Scan(&info.Entries); err != nil {
		return info, damaged(err)
	}
	return info, nil
}

func openReadOnly(path string) (*sql.DB, error) {
	dsn, err := inspectDSN(path, true)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// checkHeader reads the govstore identity and schema version of an unopened file.
func checkHeader(ctx context.Context, db *sql.DB, info *FileInfo) error {
	var appID int64
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return fmt.Errorf("%s: %w", info.Path, damaged(err))
	}
	if appID != applicationID {
		return fmt.Errorf("%w: %s is not a govstore database (application_id %#x)", ErrInvalid, info.Path, appID)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&info.SchemaVersion); err != nil {
		return damaged(err)
	}
	if info.SchemaVersion > LatestSchemaVersion() {
		return fmt.Errorf("%w: %s is at version %d, this build knows %d", ErrSchemaTooNew, info.Path, info.SchemaVersion, LatestSchemaVersion())
	}
	return nil
}

// damaged reports SQLite's damaged-file results (SQLITE_CORRUPT, SQLITE_NOTADB) as
// ErrCorrupt and maps any other error as usual.
func damaged(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && slices.Contains([]int{sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB}, se.Code()&0xff) {
		return fmt.Errorf("%w: %s", ErrCorrupt, se.Error())
	}
	return mapErr(err)
}

// BackupFile writes a consistent copy of the store at src to dest (VACUUM INTO) and
// verifies the copy. It never migrates or writes src, so it
// is safe while the daemon has the store open. dest must not exist; a backup that
// fails, verification included, leaves no file behind. The copy holds all governance
// text and is created 0600.
func BackupFile(ctx context.Context, src, dest string) (FileInfo, error) {
	src, err := filepath.Abs(src)
	if err != nil {
		return FileInfo{}, err
	}
	if _, err := os.Stat(src); err != nil {
		return FileInfo{}, err
	}
	db, err := openReadOnly(src)
	if err != nil {
		return FileInfo{}, err
	}
	err = checkHeader(ctx, db, &FileInfo{Path: src})
	_ = db.Close()
	if err != nil {
		return FileInfo{}, err
	}
	dsn, err := inspectDSN(src, false)
	if err != nil {
		return FileInfo{}, err
	}
	if err := vacuumInto(ctx, dsn, dest); err != nil {
		return FileInfo{}, err
	}
	info, err := VerifyFile(ctx, dest)
	if err != nil {
		_ = os.Remove(dest)
		return info, fmt.Errorf("backup %s failed verification: %w", dest, err)
	}
	return info, nil
}

// RestoreFile replaces the store at dst with the backup at src.
//   - src must pass VerifyFile and have no write-ahead log of its own: a live store
//     copied by hand is not the whole database (back it up with BackupFile instead).
//   - No connection, in this process or another, may have dst open: RestoreFile takes
//     an exclusive lock on it first and fails with ErrBusy otherwise. Stop the daemon
//     before restoring.
//   - The replaced store and its -wal and -shm files move aside to
//     dst.pre-restore-<stamp>, never deleted.
//   - The restored store is opened once, which migrates it to this build's schema and
//     runs quick_check. If that fails, the previous store is put back and the failed
//     copy is kept as dst.failed-restore-<stamp>.
func RestoreFile(ctx context.Context, src, dst string, now time.Time) (RestoreReport, error) {
	src, err := filepath.Abs(src)
	if err != nil {
		return RestoreReport{}, err
	}
	if dst, err = filepath.Abs(dst); err != nil {
		return RestoreReport{}, err
	}
	r := RestoreReport{Source: src, Store: dst, MovedAside: []string{}}
	if src == dst {
		return r, fmt.Errorf("%w: the restore source is the store itself", ErrInvalid)
	}
	if fi, err := os.Stat(src + "-wal"); err == nil && fi.Size() > 0 {
		return r, fmt.Errorf("%w: %s has a write-ahead log beside it, so the file alone is not the whole database; make a backup of it with `xmustard-ops store backup --file` instead", ErrInvalid, src)
	}
	if r.Restored, err = VerifyFile(ctx, src); err != nil {
		return r, fmt.Errorf("restore source: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return r, err
	}
	stamp := now.UTC().Format("20060102T150405Z")
	tmp := dst + ".restore-" + stamp + ".tmp"
	if err := copyFile(src, tmp); err != nil {
		return r, err
	}
	defer os.Remove(tmp) // a no-op once it is renamed into place
	if _, err := VerifyFile(ctx, tmp); err != nil {
		return r, fmt.Errorf("the copy of the restore source: %w", err)
	}
	unreadable, err := ensureUnused(ctx, dst)
	if err != nil {
		return r, err
	}
	if unreadable != nil {
		r.PreviousUnreadable = unreadable.Error()
	}
	previous := dst + ".pre-restore-" + stamp
	if r.MovedAside, err = renameSet(dst, previous); err != nil {
		return r, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_, _ = renameSet(previous, dst)
		return r, err
	}
	if err := syncFileAndDir(dst); err != nil {
		return r, err
	}
	s, err := Open(ctx, dst, Options{QuickCheckOnOpen: true})
	if err == nil {
		var si SchemaInfo
		si, err = s.SchemaInfo(ctx)
		r.SchemaVersion = si.Version
		err = errors.Join(err, s.Close())
	}
	if err != nil {
		failed, _ := renameSet(dst, dst+".failed-restore-"+stamp)
		if _, perr := renameSet(previous, dst); perr != nil {
			return r, fmt.Errorf("the restored store did not open (%w), and putting the previous store back failed: %v", err, perr)
		}
		r.MovedAside = failed
		return r, fmt.Errorf("the restored store did not open, so the previous store is back in place: %w", err)
	}
	return r, nil
}

// ensureUnused fails with ErrBusy while any connection, in this process or another,
// has the store at path open: it takes an exclusive lock (locking_mode EXCLUSIVE, then
// BEGIN EXCLUSIVE) and lets it go at once; closing that last connection checkpoints
// the write-ahead log into the file. A file SQLite cannot read, which is what a
// restore usually replaces, comes back as unreadable instead of an error.
func ensureUnused(ctx context.Context, path string) (unreadable, err error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)&_pragma=locking_mode(EXCLUSIVE)&_pragma=mmap_size(0)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, "BEGIN EXCLUSIVE")
	if err == nil {
		_, err = db.ExecContext(ctx, "COMMIT")
	}
	switch err = mapErr(err); {
	case err == nil:
		return nil, nil
	case errors.Is(err, ErrBusy):
		return nil, fmt.Errorf("%w: %s is open in another process; stop the daemon, and any command using the store, first", ErrBusy, path)
	}
	return damaged(err), nil
}

// renameSet moves the database at from and its sidecars to to, sidecars first. It
// refuses to replace an existing file, undoes a partial move, and returns what it
// moved.
func renameSet(from, to string) ([]string, error) {
	moved := []string{}
	undo := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.Rename(moved[i], from+moved[i][len(to):])
		}
	}
	for _, sfx := range sidecars {
		if _, err := os.Lstat(from + sfx); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := os.Lstat(to + sfx); !errors.Is(err, os.ErrNotExist) {
			undo()
			return nil, fmt.Errorf("%w: %s already exists", ErrConflict, to+sfx)
		}
		if err := os.Rename(from+sfx, to+sfx); err != nil {
			undo()
			return nil, err
		}
		moved = append(moved, to+sfx)
	}
	return moved, nil
}

// copyFile copies src to a new 0600 file dst and makes it durable.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	_, cerr := io.Copy(out, in)
	return errors.Join(cerr, out.Sync(), out.Close())
}

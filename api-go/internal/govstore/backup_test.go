package govstore

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var restoreNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func entryIDs(t *testing.T, path string) []string {
	t.Helper()
	s, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer s.Close()
	es, err := s.ListEntries(context.Background(), EntryFilter{WorkspaceID: "ws1"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(es))
	for i, e := range es {
		ids[i] = e.ID
	}
	return ids
}

// A backup taken while the store is open (the daemon running) is a verified,
// consistent copy; a restore needs every connection closed, keeps the replaced store
// beside it, and brings back exactly the backed-up entries.
func TestBackupFileWhileOpenThenRestoreKeepsThePreviousStoreAside(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "governance.db")
	s, err := Open(ctx, live, Options{})
	if err != nil {
		t.Fatal(err)
	}
	propose(t, s, "kept", "t", "in the backup")
	backup := filepath.Join(dir, "backups", "b.db")
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := BackupFile(ctx, live, backup)
	if err != nil {
		t.Fatalf("backup while open: %v", err)
	}
	if info.Entries != 1 || info.SchemaVersion != LatestSchemaVersion() || info.Fingerprint == "" || info.Bytes == 0 {
		t.Fatalf("backup info = %+v", info)
	}
	if fi, _ := os.Stat(backup); fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v, want 0600", fi.Mode().Perm())
	}
	if _, err := BackupFile(ctx, live, backup); !errors.Is(err, ErrInvalid) {
		t.Fatalf("backup over an existing file: %v", err)
	}
	propose(t, s, "later", "t", "written after the backup")

	// the open store (an idle connection, like the daemon's) refuses the restore
	if _, err := RestoreFile(ctx, backup, live, restoreNow); !errors.Is(err, ErrBusy) {
		t.Fatalf("restore under an open store: got %v, want ErrBusy", err)
	}
	if got := entryIDs(t, live); len(got) != 2 {
		t.Fatalf("a refused restore changed the store: %v", got)
	}
	_ = s.Close()

	r, err := RestoreFile(ctx, backup, live, restoreNow)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if r.SchemaVersion != LatestSchemaVersion() || r.Restored.Entries != 1 || len(r.MovedAside) == 0 || r.PreviousUnreadable != "" {
		t.Fatalf("report = %+v", r)
	}
	if got := entryIDs(t, live); len(got) != 1 || got[0] != "kept" {
		t.Fatalf("restored entries = %v, want [kept]", got)
	}
	aside := live + ".pre-restore-20260928T120000Z"
	if r.MovedAside[len(r.MovedAside)-1] != aside {
		t.Fatalf("moved aside %v, want %s last", r.MovedAside, aside)
	}
	if got := entryIDs(t, aside); len(got) != 2 {
		t.Fatalf("the replaced store kept aside holds %v, want both entries", got)
	}
	// a second restore in the same second never overwrites the first one's aside set
	if _, err := RestoreFile(ctx, backup, live, restoreNow); !errors.Is(err, ErrConflict) {
		t.Fatalf("restore over an existing aside set: %v", err)
	}
	if leftovers, _ := filepath.Glob(live + ".restore-*"); len(leftovers) != 0 {
		t.Fatalf("temporary copies left behind: %v", leftovers)
	}
}

// A store the daemon closed has no -wal or -shm; a backup of it works and leaves
// nothing new next to it.
func TestBackupOfAClosedStoreAndRefusedSources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "gov db#%.db")
	s, err := Open(ctx, live, Options{})
	if err != nil {
		t.Fatal(err)
	}
	propose(t, s, "e1", "t", "closed store")
	_ = s.Close()
	before, _ := os.ReadDir(dir)
	info, err := BackupFile(ctx, live, filepath.Join(dir, "b.db"))
	if err != nil || info.Entries != 1 {
		t.Fatalf("backup of a closed store: %+v %v", info, err)
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before)+1 {
		t.Fatalf("backup left files beside the store: before %d, after %d", len(before), len(after))
	}
	notDB := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(notDB, []byte(strings.Repeat("not a database\n", 400)), 0o600)
	foreign := filepath.Join(dir, "foreign.db")
	db, _ := sql.Open("sqlite", foreign)
	_, _ = db.Exec("CREATE TABLE t (x)")
	_ = db.Close()
	for _, c := range []struct {
		name, path string
		want       error
	}{
		{"missing", filepath.Join(dir, "missing.db"), os.ErrNotExist},
		{"text file", notDB, ErrCorrupt},
		{"foreign sqlite", foreign, ErrInvalid},
	} {
		dest := filepath.Join(dir, "out-"+strings.ReplaceAll(c.name, " ", "_")+".db")
		if _, err := BackupFile(ctx, c.path, dest); !errors.Is(err, c.want) {
			t.Errorf("%s: backup got %v, want %v", c.name, err, c.want)
		}
		if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: a refused backup left %s", c.name, dest)
		}
		if _, err := VerifyFile(ctx, c.path); !errors.Is(err, c.want) {
			t.Errorf("%s: verify got %v, want %v", c.name, err, c.want)
		}
	}
}

// VerifyFile refuses what a restore must not install: damage, a newer schema, a
// drifted schema; RestoreFile refuses a hand-copied live store with its WAL beside it.
func TestVerifyAndRestoreRefuseDamageNewerSchemaAndAStrayWAL(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.db")
	s, err := Open(ctx, good, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mustUpdate(t, s, func(tx Tx) error {
		for i := range 200 {
			if _, err := tx.InsertEntry(ctx, NewEntry{ID: fmt.Sprintf("d%d", i), WorkspaceID: "ws1",
				Content: strings.Repeat("payload ", 200)}, alice); err != nil {
				return err
			}
		}
		return nil
	})
	_ = s.Checkpoint(ctx)
	_ = s.Close()
	variant := func(name string, edit func(path string)) string {
		p := filepath.Join(dir, name)
		if err := copyFile(good, p); err != nil {
			t.Fatal(err)
		}
		edit(p)
		return p
	}
	sqlEdit := func(stmt string) func(string) {
		return func(p string) {
			db, err := sql.Open("sqlite", p)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	damaged := variant("damaged.db", func(p string) {
		f, _ := os.OpenFile(p, os.O_RDWR, 0)
		junk := []byte(strings.Repeat("\xa5", 4096*4))
		_, _ = f.WriteAt(junk, 4096*6)
		_ = f.Close()
	})
	newer := variant("newer.db", sqlEdit(fmt.Sprintf("PRAGMA user_version = %d", LatestSchemaVersion()+1)))
	drifted := variant("drifted.db", sqlEdit("CREATE TABLE stray (x)"))
	for _, c := range []struct {
		path string
		want error
	}{{damaged, ErrCorrupt}, {newer, ErrSchemaTooNew}, {drifted, ErrSchemaDrift}} {
		if _, err := VerifyFile(ctx, c.path); !errors.Is(err, c.want) {
			t.Errorf("verify %s: got %v, want %v", filepath.Base(c.path), err, c.want)
		}
		live := filepath.Join(t.TempDir(), "governance.db")
		if _, err := RestoreFile(ctx, c.path, live, restoreNow); !errors.Is(err, c.want) {
			t.Errorf("restore %s: got %v, want %v", filepath.Base(c.path), err, c.want)
		}
		if _, err := os.Stat(live); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a refused restore of %s created the store", filepath.Base(c.path))
		}
	}
	if _, err := VerifyFile(ctx, good); err != nil {
		t.Fatalf("good file: %v", err)
	}
	withWAL := variant("walcopy.db", func(p string) { _ = os.WriteFile(p+"-wal", []byte("frames"), 0o600) })
	if _, err := RestoreFile(ctx, withWAL, filepath.Join(t.TempDir(), "governance.db"), restoreNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("restore of a copy with its WAL beside it: %v", err)
	}
	if _, err := RestoreFile(ctx, good, good, restoreNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("restore onto itself: %v", err)
	}
}

// The case restore exists for: the live store is damaged; the backup replaces it and
// the damaged file is kept aside for diagnosis.
func TestRestoreReplacesADamagedStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "governance.db")
	s, err := Open(ctx, live, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mustUpdate(t, s, func(tx Tx) error {
		for i := range 200 {
			if _, err := tx.InsertEntry(ctx, NewEntry{ID: fmt.Sprintf("d%d", i), WorkspaceID: "ws1",
				Content: strings.Repeat("payload ", 200)}, alice); err != nil {
				return err
			}
		}
		return nil
	})
	backup := filepath.Join(dir, "b.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	_ = s.Checkpoint(ctx)
	_ = s.Close()
	f, _ := os.OpenFile(live, os.O_RDWR, 0)
	_, _ = f.WriteAt([]byte(strings.Repeat("\xa5", 4096*4)), 4096*6)
	_ = f.Close()
	if _, err := Open(ctx, live, Options{QuickCheckOnOpen: true}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("damaged store opened: %v", err)
	}
	r, err := RestoreFile(ctx, backup, live, restoreNow)
	if err != nil {
		t.Fatalf("restore over a damaged store: %v", err)
	}
	if _, err := VerifyFile(ctx, live); err != nil {
		t.Fatalf("restored store: %v", err)
	}
	if _, err := VerifyFile(ctx, r.MovedAside[len(r.MovedAside)-1]); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("the damaged store kept aside should still be damaged: %v", err)
	}
}

// When the restored copy passes VerifyFile but does not open (here a migration whose
// recorded checksum no longer matches this build's), the previous store goes back in
// place and the failed copy is kept beside it.
func TestRestorePutsThePreviousStoreBackWhenTheCopyDoesNotOpen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "governance.db")
	s, err := Open(ctx, live, Options{})
	if err != nil {
		t.Fatal(err)
	}
	propose(t, s, "current", "t", "the store in use")
	backup := filepath.Join(dir, "b.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	db, _ := sql.Open("sqlite", backup)
	if _, err := db.Exec("UPDATE schema_migrations SET checksum = 'tampered' WHERE version = 1"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := VerifyFile(ctx, backup); err != nil {
		t.Fatalf("the tampered checksum is not a VerifyFile finding: %v", err)
	}
	r, err := RestoreFile(ctx, backup, live, restoreNow)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("restore of an unopenable copy: %v", err)
	}
	if got := entryIDs(t, live); len(got) != 1 || got[0] != "current" {
		t.Fatalf("previous store not back in place: %v", got)
	}
	if len(r.MovedAside) == 0 || !strings.Contains(r.MovedAside[len(r.MovedAside)-1], ".failed-restore-") {
		t.Fatalf("failed copy not kept: %+v", r)
	}
}

func init() { extraHelpers["old_build"] = helperOldBuild }

// helperOldBuild is a process of an older build (it knows only migration 1) that keeps
// the store open and writes to it until the parent says stop, then tries to open the
// file again.
func helperOldBuild() error {
	ctx := context.Background()
	migrations = migrations[:1]
	s, err := helperStore()
	if err != nil {
		return err
	}
	stop := os.Getenv("GOVSTORE_BARRIER")
	fmt.Println("ready")
	writes := 0
	for {
		if _, err := os.Stat(stop); err == nil {
			break
		}
		if err := s.Update(ctx, func(tx Tx) error {
			_, err := tx.BumpFeedback(ctx, "ws1", FeedbackRetrieval, []string{"old.go"})
			return err
		}); err != nil {
			return fmt.Errorf("write %d: %w", writes, err)
		}
		writes++
		time.Sleep(5 * time.Millisecond)
	}
	_ = s.Close()
	_, err = helperStore()
	fmt.Printf("writes %d\nreopen %v\n", writes, errors.Is(err, ErrSchemaTooNew))
	return nil
}

// An upgrade while clients are connected: a newer build opens the store and migrates
// it while a process of the older build has it open and keeps writing. The older
// process's writes keep succeeding across the migration, a restore is refused while it
// is connected, and once it closes, the older build refuses to reopen the newer file
// instead of running on a schema it does not know.
func TestMigrationWhileAnOlderBuildIsConnected(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "gov.db")
	all := migrations
	migrations = all[:1]
	old, err := Open(ctx, path, Options{})
	migrations = all
	if err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	stop := filepath.Join(dir, "stop")
	cmd := exec.Command(os.Args[0], "-test.run=^TestGovstoreHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperEnv+"=old_build", "GOVSTORE_DB="+path, "GOVSTORE_BARRIER="+stop)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(out)
	if !lines.Scan() || lines.Text() != "ready" {
		t.Fatalf("helper did not start: %q", lines.Text())
	}
	time.Sleep(50 * time.Millisecond)

	s, err := Open(ctx, path, Options{BusyTimeout: testBusyTimeout})
	if err != nil {
		t.Fatalf("the newer build could not migrate under a connected older one: %v", err)
	}
	defer s.Close()
	if info, _ := s.SchemaInfo(ctx); info.Version != LatestSchemaVersion() {
		t.Fatalf("schema after the upgrade = %d", info.Version)
	}
	atMigration, _ := s.GetFeedback(ctx, "ws1", []string{"old.go"})
	if _, created := recordRun(t, s, RunOutcomeInput{WorkspaceID: "ws1", Source: RunSourceLog, SourceKey: "k", Status: RunFailed}); !created {
		t.Fatal("the migrated store takes the newer build's writes")
	}
	_ = s.Close()
	backup := filepath.Join(dir, "backup.db")
	if _, err := BackupFile(ctx, path, backup); err != nil {
		t.Fatalf("backup while an older build writes: %v", err)
	}
	if _, err := RestoreFile(ctx, backup, path, restoreNow); !errors.Is(err, ErrBusy) {
		t.Fatalf("restore while another process has the store open: got %v, want ErrBusy", err)
	}
	time.Sleep(100 * time.Millisecond) // the older process keeps writing after the migration
	_ = os.WriteFile(stop, nil, 0o600)
	var report []string
	for lines.Scan() {
		report = append(report, lines.Text())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("older build failed across the migration: %v %v", err, report)
	}
	if len(report) != 2 || report[1] != "reopen true" {
		t.Fatalf("older build output %v: it must refuse the newer file on reopen", report)
	}
	writes, _ := strconv.Atoi(strings.TrimPrefix(report[0], "writes "))
	final := entryFeedback(t, path)
	if final != writes || final <= atMigration["old.go"].RetrievalCount {
		t.Fatalf("older build wrote %d, store holds %d, %d at migration: writes after the migration were lost", writes, final, atMigration["old.go"].RetrievalCount)
	}
}

func entryFeedback(t *testing.T, path string) int {
	t.Helper()
	s, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fb, err := s.GetFeedback(context.Background(), "ws1", []string{"old.go"})
	if err != nil {
		t.Fatal(err)
	}
	return fb["old.go"].RetrievalCount
}

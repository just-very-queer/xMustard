package govstore

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Cross-process tests. They re-run this test binary as helper processes (the os/exec
// helper-process pattern), so the writers really are separate OS processes that only
// share the database file, like the API daemon and the ops CLI.

const helperEnv = "GOVSTORE_HELPER"

// extraHelpers lets build-tagged test files (the RSS probe) add helper modes.
var extraHelpers = map[string]func() error{}

// TestGovstoreHelperProcess is the helper's entry point; it does nothing in a normal run.
func TestGovstoreHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		t.Skip("helper process only")
	}
	var err error
	switch mode {
	case "propose_verify":
		err = helperProposeVerify()
	case "kill_mid_tx":
		err = helperKillMidTx()
	case "commit_storm":
		err = helperCommitStorm()
	default:
		if fn, ok := extraHelpers[mode]; ok {
			err = fn()
		} else {
			err = fmt.Errorf("unknown helper mode %q", mode)
		}
	}
	if err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func helperStore() (*SQLStore, error) {
	return Open(context.Background(), os.Getenv("GOVSTORE_DB"), Options{})
}

// helperProposeVerify proposes its own entries and votes on the shared ones, in
// separate transactions, while a sibling process does the same.
func helperProposeVerify() error {
	ctx := context.Background()
	s, err := helperStore()
	if err != nil {
		return err
	}
	defer s.Close()
	principal := os.Getenv("GOVSTORE_PRINCIPAL")
	n, _ := strconv.Atoi(os.Getenv("GOVSTORE_N"))
	shared := strings.Split(os.Getenv("GOVSTORE_SHARED"), ",")
	barrier := os.Getenv("GOVSTORE_BARRIER")
	fmt.Println("ready")
	for {
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	fmt.Printf("start %d\n", time.Now().UnixNano())
	actor := Actor{Principal: principal}
	for i := range n {
		if err := s.Update(ctx, func(tx Tx) error {
			_, err := tx.InsertEntry(ctx, NewEntry{ID: fmt.Sprintf("%s-%d", principal, i), WorkspaceID: "ws1",
				Title: "proposal", Content: fmt.Sprintf("proposal %d from %s", i, principal)}, actor)
			return err
		}); err != nil {
			return fmt.Errorf("propose %d: %w", i, err)
		}
		target := shared[i%len(shared)]
		if err := s.Update(ctx, func(tx Tx) error {
			if _, err := tx.RecordVote(ctx, VoteInput{EntryID: target, Verdict: VerdictApprove}, actor); err != nil {
				return err
			}
			// the caller's reconciliation: read, decide and write in one transaction
			e, err := tx.GetEntry(ctx, target)
			if err != nil {
				return err
			}
			tl, err := tx.Tally(ctx, target, 0)
			if err != nil {
				return err
			}
			if !e.Promoted && tl.PeerApprovals >= e.RequiredVerifications {
				_, err = tx.SetPromotion(ctx, target, Promotion{Status: StatusVerified, Promoted: true,
					VerificationMode: ModePeerVerified}, actor)
			}
			return err
		}); err != nil {
			return fmt.Errorf("verify %s: %w", target, err)
		}
		if err := s.Update(ctx, func(tx Tx) error {
			_, err := tx.BumpFeedback(ctx, "ws1", FeedbackRetrieval, []string{"shared/hot.go"})
			return err
		}); err != nil {
			return fmt.Errorf("feedback %d: %w", i, err)
		}
	}
	fmt.Printf("end %d\n", time.Now().UnixNano())
	return nil
}

// helperKillMidTx commits some entries, then opens a large transaction and blocks
// inside it until the parent SIGKILLs the process.
func helperKillMidTx() error {
	ctx := context.Background()
	s, err := helperStore()
	if err != nil {
		return err
	}
	for i := range 50 {
		if err := s.Update(ctx, func(tx Tx) error {
			_, err := tx.InsertEntry(ctx, NewEntry{ID: fmt.Sprintf("kept-%d", i), WorkspaceID: "ws1",
				Title: "kept", Content: fmt.Sprintf("committed proposal %d", i)}, alice)
			return err
		}); err != nil {
			return err
		}
	}
	fmt.Println("committed 50")
	big := strings.Repeat("uncommitted payload ", 250) // ~5 KiB per row: spills into the WAL
	return s.Update(ctx, func(tx Tx) error {
		for i := range 600 {
			if _, err := tx.InsertEntry(ctx, NewEntry{ID: fmt.Sprintf("lost-%d", i), WorkspaceID: "ws1",
				Title: "lost", Content: big + strconv.Itoa(i), Paths: []string{"lost.go"}}, alice); err != nil {
				return err
			}
			if _, err := tx.BumpFeedback(ctx, "ws1", FeedbackRetrieval, []string{"lost.go"}); err != nil {
				return err
			}
		}
		fmt.Println("in-tx")
		time.Sleep(time.Hour) // the parent kills us here
		return nil
	})
}

// helperCommitStorm commits small multi-row transactions as fast as it can until the
// parent kills it at a random moment, possibly mid-commit.
func helperCommitStorm() error {
	ctx := context.Background()
	s, err := helperStore()
	if err != nil {
		return err
	}
	round := os.Getenv("GOVSTORE_ROUND")
	for i := 0; ; i++ {
		if err := s.Update(ctx, func(tx Tx) error {
			id := fmt.Sprintf("storm-%s-%d", round, i)
			if _, err := tx.InsertEntry(ctx, NewEntry{ID: id, WorkspaceID: "ws1", Title: "storm",
				Content: fmt.Sprintf("storm write %s/%d", round, i)}, alice); err != nil {
				return err
			}
			if _, err := tx.RecordVote(ctx, VoteInput{EntryID: id, Verdict: VerdictApprove}, bob); err != nil {
				return err
			}
			_, err := tx.BumpFeedback(ctx, "ws1", FeedbackRetrieval, []string{"storm.go"})
			return err
		}); err != nil {
			return err
		}
		if i == 0 {
			fmt.Println("started")
		}
	}
}

type helper struct {
	cmd   *exec.Cmd
	lines chan string
	out   strings.Builder
}

func startHelper(t *testing.T, mode string, env ...string) *helper {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestGovstoreHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), append([]string{helperEnv + "=" + mode}, env...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	h := &helper{cmd: cmd, lines: make(chan string, 64)}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			h.lines <- sc.Text()
		}
		_, _ = io.Copy(io.Discard, stdout)
		close(h.lines)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return h
}

// waitFor reads helper output until a line starts with prefix.
func (h *helper) waitFor(t *testing.T, prefix string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-h.lines:
			if !ok {
				t.Fatalf("helper exited before %q; output:\n%s", prefix, h.out.String())
			}
			h.out.WriteString(line + "\n")
			if strings.HasPrefix(line, "error:") {
				t.Fatalf("helper failed: %s\n%s", line, h.out.String())
			}
			if strings.HasPrefix(line, prefix) {
				return line
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q; output:\n%s", prefix, h.out.String())
		}
	}
}

func (h *helper) kill9(t *testing.T) {
	t.Helper()
	if err := h.cmd.Process.Kill(); err != nil { // SIGKILL on unix
		t.Fatal(err)
	}
	_ = h.cmd.Wait()
}

func stampOf(t *testing.T, line string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(strings.Fields(line)[1], 10, 64)
	if err != nil {
		t.Fatalf("bad stamp %q", line)
	}
	return v
}

func TestTwoProcessesProposeAndVerifyWithoutLostUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns helper processes")
	}
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gov.db")
	s, err := Open(ctx, dbPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const shared, perProcess = 10, 120
	var ids []string
	for i := range shared {
		id := fmt.Sprintf("shared-%d", i)
		ids = append(ids, id)
		propose(t, s, id, "shared", "shared fact "+strconv.Itoa(i))
	}
	barrier := filepath.Join(dir, "go")
	env := func(principal string) []string {
		return []string{"GOVSTORE_DB=" + dbPath, "GOVSTORE_PRINCIPAL=" + principal, "GOVSTORE_N=" + strconv.Itoa(perProcess),
			"GOVSTORE_SHARED=" + strings.Join(ids, ","), "GOVSTORE_BARRIER=" + barrier}
	}
	p1 := startHelper(t, "propose_verify", env("proc-a")...)
	p2 := startHelper(t, "propose_verify", env("proc-b")...)
	p1.waitFor(t, "ready", time.Minute)
	p2.waitFor(t, "ready", time.Minute)
	if err := os.WriteFile(barrier, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s1, s2 := stampOf(t, p1.waitFor(t, "start", time.Minute)), stampOf(t, p2.waitFor(t, "start", time.Minute))
	e1, e2 := stampOf(t, p1.waitFor(t, "end", 3*time.Minute)), stampOf(t, p2.waitFor(t, "end", 3*time.Minute))
	for _, h := range []*helper{p1, p2} {
		if err := h.cmd.Wait(); err != nil {
			t.Fatalf("helper exit: %v\n%s", err, h.out.String())
		}
	}
	if !(s1 < e2 && s2 < e1) {
		t.Fatalf("helpers did not overlap: [%d,%d] [%d,%d]", s1, e1, s2, e2)
	}

	if n := countRows(t, s, "SELECT count(*) FROM entries"); n != shared+2*perProcess {
		t.Fatalf("entries = %d, want %d", n, shared+2*perProcess)
	}
	for _, id := range ids {
		votes, _ := s.ListVotes(ctx, id, 0)
		e, _ := s.GetEntry(ctx, id)
		if len(votes) != 2 || !e.Promoted || e.VerificationMode != ModePeerVerified {
			t.Fatalf("%s: votes=%+v entry=%+v", id, votes, e)
		}
	}
	fb, _ := s.GetFeedback(ctx, "ws1", []string{"shared/hot.go"})
	if got := fb["shared/hot.go"].RetrievalCount; got != 2*perProcess {
		t.Fatalf("feedback counter = %d, want %d (lost updates)", got, 2*perProcess)
	}
	for typ, want := range map[string]int{EventPropose: shared + 2*perProcess, EventVote: 2 * perProcess, EventPromote: shared} {
		if n := countRows(t, s, "SELECT count(*) FROM events WHERE type = ?", typ); n != want {
			t.Fatalf("%s events = %d, want %d", typ, n, want)
		}
	}
}

func assertConsistent(t *testing.T, path string) *SQLStore {
	t.Helper()
	s, err := Open(context.Background(), path, Options{QuickCheckOnOpen: true})
	if err != nil {
		t.Fatalf("reopen after kill: %v", err)
	}
	var integrity string
	if err := s.readers.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check = %q %v", integrity, err)
	}
	if err := s.Update(context.Background(), func(tx Tx) error {
		_, err := tx.(*txn).exec(context.Background(), "INSERT INTO memory_fts (memory_fts) VALUES ('integrity-check')")
		return err
	}); err != nil {
		t.Fatalf("fts integrity-check: %v", err)
	}
	return s
}

func TestKillNineMidTransactionLeavesConsistentDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns helper processes")
	}
	dbPath := filepath.Join(t.TempDir(), "gov.db")
	h := startHelper(t, "kill_mid_tx", "GOVSTORE_DB="+dbPath)
	h.waitFor(t, "committed 50", time.Minute)
	h.waitFor(t, "in-tx", time.Minute)
	if fi, err := os.Stat(dbPath + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("expected uncommitted frames in the WAL before the kill: %v", err)
	}
	h.kill9(t)

	s := assertConsistent(t, dbPath)
	defer s.Close()
	for q, want := range map[string]int{
		"SELECT count(*) FROM entries":                                         50,
		"SELECT count(*) FROM entries WHERE id LIKE 'lost-%'":                  0,
		"SELECT count(*) FROM revisions":                                       50,
		"SELECT count(*) FROM events WHERE type = 'propose'":                   50,
		"SELECT count(*) FROM anchors":                                         0,
		"SELECT count(*) FROM path_feedback":                                   0,
		"SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'committed'":   50,
		"SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'uncommitted'": 0,
	} {
		if n := countRows(t, s, q); n != want {
			t.Fatalf("%s = %d, want %d", q, n, want)
		}
	}
	// and the store keeps working
	propose(t, s, "after-crash", "t", "written after recovery")
}

func TestKillNineDuringCommitStormKeepsTransactionsAtomic(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns helper processes")
	}
	dbPath := filepath.Join(t.TempDir(), "gov.db")
	for round := range 3 {
		h := startHelper(t, "commit_storm", "GOVSTORE_DB="+dbPath, "GOVSTORE_ROUND="+strconv.Itoa(round))
		h.waitFor(t, "started", time.Minute)
		time.Sleep(time.Duration(20+rand.IntN(180)) * time.Millisecond)
		h.kill9(t)

		s := assertConsistent(t, dbPath)
		entries := countRows(t, s, "SELECT count(*) FROM entries")
		fb, _ := s.GetFeedback(context.Background(), "ws1", []string{"storm.go"})
		// every transaction wrote an entry, a revision, a vote, two events and one
		// counter bump: a torn transaction would break one of these equalities
		for q, want := range map[string]int{
			"SELECT count(*) FROM revisions":                                 entries,
			"SELECT count(*) FROM votes":                                     entries,
			"SELECT count(*) FROM events":                                    2 * entries,
			"SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'storm'": entries,
		} {
			if n := countRows(t, s, q); n != want {
				t.Fatalf("round %d: %s = %d, want %d", round, q, n, want)
			}
		}
		if entries == 0 || fb["storm.go"].RetrievalCount != entries {
			t.Fatalf("round %d: entries=%d feedback=%d", round, entries, fb["storm.go"].RetrievalCount)
		}
		_ = s.Close()
	}
}

// Opening a store must never fail on a lock another opener holds: every open-time
// statement (auto_vacuum, journal_mode, the migration) runs under busy_timeout. This
// failed with SQLITE_BUSY while busy_timeout was applied after _auto_vacuum.
func TestConcurrentOpensAndMigrationsWaitInsteadOfFailing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gov.db") // fresh: the openers race the migration too
	errs := make(chan error, 64)
	done := make(chan struct{})
	for range 12 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 4 {
				s, err := Open(context.Background(), path, Options{})
				if err != nil {
					errs <- err
					return
				}
				if err := s.Update(context.Background(), func(tx Tx) error {
					_, err := tx.BumpFeedback(context.Background(), "ws1", FeedbackRetrieval, []string{"open.go"})
					return err
				}); err != nil {
					errs <- err
				}
				_ = s.Close()
			}
		}()
	}
	for range 12 {
		<-done
	}
	close(errs)
	for err := range errs {
		t.Errorf("concurrent open: %v", err)
	}
	s, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fb, _ := s.GetFeedback(context.Background(), "ws1", []string{"open.go"})
	if fb["open.go"].RetrievalCount != 48 {
		t.Fatalf("feedback = %d, want 48", fb["open.go"].RetrievalCount)
	}
	if n := countRows(t, s, "SELECT count(*) FROM schema_migrations"); n != 1 {
		t.Fatalf("migrations applied %d times", n)
	}
}

// Goroutines sharing one store: writers serialize on the single writer connection
// while readers run on the pool against WAL snapshots, with no lost updates.
func TestSharedStoreConcurrentWritersAndReaders(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, nil)
	const writers, perWriter = 6, 40
	errs := make(chan error, writers*perWriter+64)
	done := make(chan struct{})
	stop := make(chan struct{})
	for w := range writers {
		go func() {
			defer func() { done <- struct{}{} }()
			who := Actor{Principal: fmt.Sprintf("writer-%d", w)}
			for i := range perWriter {
				if err := s.Update(ctx, func(tx Tx) error {
					if _, err := tx.InsertEntry(ctx, NewEntry{ID: fmt.Sprintf("w%d-%d", w, i), WorkspaceID: "ws1",
						Title: "shared", Content: fmt.Sprintf("concurrent write %d %d", w, i)}, who); err != nil {
						return err
					}
					_, err := tx.BumpFeedback(ctx, "ws1", FeedbackRetrieval, []string{"hot.go"})
					return err
				}); err != nil {
					errs <- err
				}
			}
		}()
	}
	readersDone := make(chan struct{})
	go func() {
		defer close(readersDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "concurrent write"}); err != nil {
				errs <- err
			}
			if err := s.View(ctx, func(r Reader) error {
				a, err := r.ListEntries(ctx, EntryFilter{WorkspaceID: "ws1", Limit: 1000})
				if err != nil {
					return err
				}
				fb, err := r.GetFeedback(ctx, "ws1", []string{"hot.go"})
				if err != nil {
					return err
				}
				// one snapshot: every committed entry came with exactly one bump
				if got := fb["hot.go"].RetrievalCount; got != len(a) {
					return fmt.Errorf("torn snapshot: %d entries, %d bumps", len(a), got)
				}
				return nil
			}); err != nil {
				errs <- err
			}
		}
	}()
	for range writers {
		<-done
	}
	close(stop)
	<-readersDone
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := countRows(t, s, "SELECT count(*) FROM entries"); n != writers*perWriter {
		t.Fatalf("entries = %d", n)
	}
	fb, _ := s.GetFeedback(ctx, "ws1", []string{"hot.go"})
	if fb["hot.go"].RetrievalCount != writers*perWriter {
		t.Fatalf("feedback = %d", fb["hot.go"].RetrievalCount)
	}
}

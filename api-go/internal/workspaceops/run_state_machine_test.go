package workspaceops

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var errTestFinalize = errors.New("test finalize failure")

// captureRunMirror installs the test-only runMirrorObserver and returns a getter for
// the captured snapshots (in commit order) plus a cleanup that restores nil. Every
// snapshot is the exact immutable record handed to the PG mirror, so the test can prove
// JSON and the mirror converge without a live Postgres.
func captureRunMirror(t *testing.T) (func() []runRecord, func()) {
	t.Helper()
	var mu sync.Mutex
	var snaps []runRecord
	runMirrorObserver = func(r runRecord) {
		mu.Lock()
		snaps = append(snaps, r)
		mu.Unlock()
	}
	get := func() []runRecord {
		mu.Lock()
		defer mu.Unlock()
		out := make([]runRecord, len(snaps))
		copy(out, snaps)
		return out
	}
	cleanup := func() { runMirrorObserver = nil }
	return get, cleanup
}

// assertMirrorConverges checks the two invariants P0-A guarantees: (1) every persisted
// revision is unique + strictly monotonic in commit order — so the PG mirror's
// "skip seq <= stored" guard can never drop a state change; (2) the highest-revision
// mirrored snapshot's status equals the JSON record on disk — JSON and the mirror
// converge on a single value.
func assertMirrorConverges(t *testing.T, dataDir, ws, runID string, snaps []runRecord) {
	t.Helper()
	if len(snaps) == 0 {
		t.Fatalf("no mirror snapshots captured")
	}
	var prev int64 = -1
	var top runRecord
	var topRev int64 = -1
	for i, s := range snaps {
		if s.MirrorRevision <= prev {
			t.Fatalf("mirror revision not strictly increasing at #%d: %d after %d (a shared/regressed revision lets the PG seq guard drop a write)", i, s.MirrorRevision, prev)
		}
		prev = s.MirrorRevision
		if s.MirrorRevision > topRev {
			topRev = s.MirrorRevision
			top = s
		}
	}
	onDisk, err := ReadRun(dataDir, ws, runID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if onDisk.Status != top.Status {
		t.Fatalf("JSON status %q != highest-revision mirror status %q — JSON and mirror diverged", onDisk.Status, top.Status)
	}
	if onDisk.MirrorRevision != top.MirrorRevision {
		t.Fatalf("JSON revision %d != top mirror revision %d", onDisk.MirrorRevision, top.MirrorRevision)
	}
}

// Concurrent terminalizers (cancel racing a failure-finalize) on one running run must
// produce exactly ONE terminal write, with unique monotonic revisions and JSON==mirror.
// Without the serialized mutateRun transaction the two read-modify-write paths collide
// on a revision and the mirror silently drops the loser (JSON/PG divergence).
func TestRunTransactionSerializesConcurrentTerminalizers(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)
	getSnaps, cleanup := captureRunMirror(t)
	defer cleanup()

	const runID = "run-terminal-race"
	run := runRecord{
		RunID:       runID,
		WorkspaceID: workspaceID,
		IssueID:     issueID,
		Runtime:     "opencode",
		Model:       "fake/test-model",
		Status:      "running",
		Title:       "t",
		CreatedAt:   "2026-04-14T10:06:00Z",
		LogPath:     filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".log"),
		OutputPath:  filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".out.json"),
	}
	if err := saveRunRecord(dataDir, run); err != nil {
		t.Fatalf("save run: %v", err)
	}

	const concurrency = 12
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = CancelRun(dataDir, workspaceID, runID)
			} else {
				saveRunFailure(dataDir, run, errTestFinalize)
			}
		}(i)
	}
	wg.Wait()

	final, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if !isTerminalRunStatus(final.Status) {
		t.Fatalf("run did not reach a terminal status, got %q", final.Status)
	}
	// only the first terminalizer commits; the rest see a terminal record under the lock
	// and no-op, so exactly two mirror writes exist: the initial running + one terminal.
	snaps := getSnaps()
	if len(snaps) != 2 {
		t.Fatalf("expected exactly 2 mirror writes (running + one terminal), got %d: %+v", len(snaps), statusList(snaps))
	}
	assertMirrorConverges(t, dataDir, workspaceID, runID, snaps)
}

// A real managed run cancelled while it is launching/running must converge to a single
// terminal status with JSON==mirror and never linger in a non-terminal state — the
// cancel-vs-finalize / start-vs-cancel guarantee exercised end-to-end through
// runManagedProcess (real subprocess), repeated to shake out the race window.
func TestManagedRunCancelVsFinalizeConverges(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)
	opencodeBin := writeFakeOpencode(t)
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), appSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &opencodeBin,
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	for iter := 0; iter < 5; iter++ {
		getSnaps, cleanup := captureRunMirror(t)
		runID := "run-cvf-" + string(rune('a'+iter))
		run := runRecord{
			RunID:          runID,
			WorkspaceID:    workspaceID,
			IssueID:        issueID,
			Runtime:        "opencode",
			Model:          "fake/test-model",
			Status:         "queued",
			Title:          "opencode:cvf",
			Prompt:         "race",
			Command:        []string{opencodeBin, "run", "--format", "json", "--dir", repoRoot, "-m", "fake/test-model", "race"},
			CommandPreview: "opencode run",
			LogPath:        filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".log"),
			OutputPath:     filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".out.json"),
			CreatedAt:      "2026-04-14T10:06:00Z",
		}
		if err := saveRunRecord(dataDir, run); err != nil {
			t.Fatalf("save run: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); startManagedRun(dataDir, run, repoRoot) }()
		go func() {
			defer wg.Done()
			// cancel after a tiny jitter so the cancel sometimes lands while queued,
			// sometimes while running, sometimes after finalize.
			time.Sleep(time.Duration(iter) * time.Millisecond)
			_, _ = CancelRun(dataDir, workspaceID, runID)
		}()
		wg.Wait()

		// let the managed goroutine finish its finalize transition.
		deadline := time.Now().Add(8 * time.Second)
		var final *runRecord
		for time.Now().Before(deadline) {
			r, err := ReadRun(dataDir, workspaceID, runID)
			if err == nil && isTerminalRunStatus(r.Status) {
				final = r
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if final == nil {
			t.Fatalf("iter %d: run never reached a terminal status", iter)
		}
		waitForRunCleanup(t, runID)
		// give any trailing best-effort mirror call a beat to be observed.
		time.Sleep(20 * time.Millisecond)
		assertMirrorConverges(t, dataDir, workspaceID, runID, getSnaps())
		if final.PID != nil {
			t.Fatalf("iter %d: terminal run must not retain a PID, got %d", iter, *final.PID)
		}
		cleanup()
	}
}

func statusList(snaps []runRecord) []string {
	out := make([]string, len(snaps))
	for i, s := range snaps {
		out[i] = s.Status
	}
	return out
}

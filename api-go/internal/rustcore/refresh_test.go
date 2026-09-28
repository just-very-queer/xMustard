package rustcore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestRefreshBatchSaysWhenTheWholeTreeIsChecked(t *testing.T) {
	cases := []struct {
		name         string
		batch        RefreshBatch
		whole, empty bool
	}{
		{"synced paths", RefreshBatch{Paths: []string{"a.go"}, Synced: true}, false, false},
		{"a Git state change only", RefreshBatch{Git: true, Synced: true}, false, false},
		{"nothing seen", RefreshBatch{Synced: true}, false, true},
		{"a full refresh", RefreshBatch{Full: "overflow", Synced: true}, true, false},
		{"not synced with the watcher", RefreshBatch{Paths: []string{"a.go"}}, true, false},
		{"a path the flag parser would misread", RefreshBatch{Paths: []string{"a.go", "-x.go"}, Synced: true}, true, false},
		{"an empty path", RefreshBatch{Paths: []string{""}, Synced: true}, true, false},
	}
	for _, c := range cases {
		if got := c.batch.Whole(); got != c.whole {
			t.Errorf("%s: Whole() = %v, want %v", c.name, got, c.whole)
		}
		if got := c.batch.Empty(); got != c.empty {
			t.Errorf("%s: Empty() = %v, want %v", c.name, got, c.empty)
		}
	}
}

func TestRefreshSummaryKeepsTheRefreshCounters(t *testing.T) {
	report := `{"command":"update","mode":"incremental","reason":"changes","root":"/r",
		"counters":{"files_scanned":90,"reparsed":2,"reresolved":5,"escalated":false,"deleted":1,"bytes_read":900},
		"timing":{"elapsed_ms":41,"scan_ms":3}}`
	var got map[string]any
	if err := json.Unmarshal([]byte(refreshSummary([]byte(report))), &got); err != nil {
		t.Fatal(err)
	}
	counters := got["counters"].(map[string]any)
	if got["mode"] != "incremental" || counters["reparsed"] != float64(2) || counters["reresolved"] != float64(5) ||
		counters["escalated"] != false || counters["deleted"] != float64(1) || got["timing"].(map[string]any)["elapsed_ms"] != float64(41) {
		t.Fatalf("summary lost the counters: %v", got)
	}
	if _, kept := counters["files_scanned"]; kept {
		t.Fatalf("summary keeps only the refresh counters: %v", got)
	}
	for _, bad := range []string{"", "not json", `{"counters":{}}`} {
		if s := refreshSummary([]byte(bad)); s != "" {
			t.Errorf("refreshSummary(%q) = %q, want empty", bad, s)
		}
	}
}

// Notifications drive the loop: `$/refresh.due` starts the orchestrator's refresh, and
// a root the worker stopped watching is registered again by the next read.
func TestWorkerNoticesStartRefreshesAndForgetUnwatchedRoots(t *testing.T) {
	prev := refreshDue.Load()
	t.Cleanup(func() { refreshDue.Store(prev) })
	due := make(chan string, 1)
	OnRefreshDue(func(root string) { due <- root })

	p := &workerProc{pid: 4242}
	setWatched("/repo", p.pid)
	t.Cleanup(func() { unwatched("/repo", p.pid) })
	p.workerNotice([]byte(`{"jsonrpc":"2.0","method":"$/refresh.due","params":{"root":"/repo","watcher_state":"ok"}}`))
	select {
	case got := <-due:
		if got != "/repo" {
			t.Fatalf("refresh due for %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("$/refresh.due did not start a refresh")
	}
	for _, body := range []string{
		`{"method":"$/watch.state","params":{"root":"/repo","watcher_state":"degraded"}}`,
		`{"method":"$/watch.state","params":{"watcher_state":"absent"}}`,
		`not json`,
	} {
		p.workerNotice([]byte(body))
	}
	registered := func() int {
		watchedRoots.Lock()
		defer watchedRoots.Unlock()
		return watchedRoots.roots["/repo"]
	}
	if registered() != p.pid {
		t.Fatal("a state change other than absent, or a notice without a root, must keep the registration")
	}
	p.workerNotice([]byte(`{"method":"$/watch.state","params":{"root":"/repo","watcher_state":"absent"}}`))
	if registered() != 0 {
		t.Fatal("a root the worker no longer watches must be forgotten")
	}
	select {
	case got := <-due:
		t.Fatalf("an unexpected refresh for %q", got)
	default:
	}
}

// The watcher lives in the worker: with no worker, a watch call fails at once and no
// one-shot core is spawned for it.
func TestWatchCallsNeverRunOneShot(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "execs.log")
	core := filepath.Join(t.TempDir(), "xmustard-core")
	if err := os.WriteFile(core, []byte("#!/bin/sh\necho \"$1\" >> '"+logPath+"'\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	t.Setenv("XMUSTARD_CORE_WORKER", "0")
	resetWorker()
	if _, err := watchCall(context.Background(), "start", t.TempDir()); !errors.Is(err, errNoWorker) {
		t.Fatalf("watch without a worker: %v", err)
	}
	WatchRoot(context.Background(), t.TempDir())
	if b := TakeRefresh(context.Background(), "/repo", true); b != nil {
		t.Fatalf("an unwatched root has no batch: %+v", b)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		raw, _ := os.ReadFile(logPath)
		t.Fatalf("a watch call spawned a core: %s", raw)
	}
}

// The real worker's watcher, driven the way code_index.go drives it: the start-up
// batch refreshes the whole tree, an edit comes back as a synced batch naming the
// path, and a finished batch is not handed out again.
func TestWorkerWatcherHandsOutBatchesWithTheRealCore(t *testing.T) {
	core := realCore(t)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	t.Setenv("XMUSTARD_CORE_WORKER", "1")
	resetWorker()
	t.Cleanup(resetWorker)
	prev := refreshDue.Load()
	t.Cleanup(func() { refreshDue.Store(prev) })
	due := make(chan string, 16)
	OnRefreshDue(func(root string) { due <- root })

	root, err := filepath.EvalSymlinks(gitFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	WatchRoot(ctx, root)
	if !Watched(root) {
		t.Fatal("the root was not registered with the worker's watcher")
	}
	waitDue := func() {
		t.Helper()
		select {
		case got := <-due:
			if got != root {
				t.Fatalf("refresh due for %q, want %q", got, root)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("no $/refresh.due from the worker")
		}
	}
	waitDue()
	b := TakeRefresh(ctx, root, false)
	if b == nil || b.Full != "startup" || !b.Whole() {
		t.Fatalf("start-up batch: %+v", b)
	}
	FinishRefresh(ctx, root, b, true, []byte(`{"mode":"full","reason":"no_index","counters":{"reparsed":3}}`))

	if err := os.WriteFile(filepath.Join(root, "src", "engine.go"), []byte("package engine\n\nfunc ComputeTotal(a int) int { return a }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitDue()
	b = TakeRefresh(ctx, root, false)
	if b == nil || b.Whole() || !slices.Equal(b.Paths, []string{"src/engine.go"}) {
		t.Fatalf("edit batch: %+v", b)
	}
	FinishRefresh(ctx, root, b, true, nil)
	if again := TakeRefresh(ctx, root, false); again != nil {
		t.Fatalf("a finished batch was handed out again: %+v", again)
	}
	if forced := TakeRefresh(ctx, root, true); forced == nil || !forced.Empty() {
		t.Fatalf("a forced take with nothing pending is an empty batch: %+v", forced)
	}
}

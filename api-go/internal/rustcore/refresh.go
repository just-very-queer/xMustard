package rustcore

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"maps"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"xmustard/api-go/internal/budget"
)

// The watcher's refresh loop, orchestrator side (WS-15, PAR-FRESH-03). The resident
// worker (`xmustard-core serve`) watches registered roots (rust-core/src/index/watch.rs)
// and announces a due batch of changed paths with a `$/refresh.due` notification. The
// orchestrator (workspaceops/code_index.go) takes the batch, runs `index update --paths
// ...` one-shot as heavy work (RunIndex: rustcore takes the governor's heavy slot for
// it, WS-06B, so nothing here takes it again), and reports the outcome; the worker then
// swaps its resident snapshot to the new generation, and retries a failed batch with
// backoff.
//
// A watcher lives in the worker process, so everything here needs the live worker:
// with it off (XMUSTARD_CORE_WORKER unset) or unavailable nothing is watched, and reads
// refresh the index on their own. A worker that exits takes its watchers with it; its
// replacement registers the same roots before it takes a call (rewatch), and their
// start-up batches refresh the whole tree.

// RefreshBatch is a batch taken from a root's watcher.
type RefreshBatch struct {
	ID    int64    `json:"id"`
	Paths []string `json:"paths"`
	// Full says why the whole tree is refreshed; empty for a batch of paths.
	Full string `json:"full"`
	// Git: HEAD, the Git index or a ref moved.
	Git bool `json:"git"`
	// Synced: the batch holds every change made before it was taken (the watcher's
	// cookie came back).
	Synced bool `json:"-"`
}

// Whole reports whether the refresh must cover the whole tree: a full batch, one the
// watcher could not sync, or a path the update's flag parser would misread.
func (b *RefreshBatch) Whole() bool {
	if b.Full != "" || !b.Synced {
		return true
	}
	for _, p := range b.Paths {
		if p == "" || p[0] == '-' {
			return true
		}
	}
	return false
}

// Empty reports whether the watcher saw nothing since the last batch.
func (b *RefreshBatch) Empty() bool { return len(b.Paths) == 0 && !b.Git && b.Full == "" }

// watchedRoots maps a root to the pid of the worker it is registered with.
var watchedRoots = struct {
	sync.Mutex
	roots map[string]int
}{roots: map[string]int{}}

// refreshDue runs when a watched root's batch is due (set by workspaceops).
var refreshDue atomic.Pointer[func(root string)]

// OnRefreshDue sets what runs when a watched root's batch is due. It runs on its own
// goroutine.
func OnRefreshDue(f func(root string)) { refreshDue.Store(&f) }

// watchSub is the core's watcher subcommand. It runs only on the worker (runCoreCtx
// never sends it one-shot): the watchers live in the worker process.
const watchSub = "watch"

// errNoWorker: a worker-only call found no live worker to take it.
var errNoWorker = errors.New("no resident worker")

// watchCall runs `xmustard-core watch <args>` on the worker.
func watchCall(ctx context.Context, args ...string) ([]byte, error) {
	return runCoreCtx(ctx, watchSub, args...)
}

// Watched reports whether root is registered with the live worker's watcher.
func Watched(root string) bool {
	pid := coreWorker.runningPID()
	watchedRoots.Lock()
	defer watchedRoots.Unlock()
	return pid != 0 && watchedRoots.roots[root] == pid
}

// setWatched records root as registered with worker pid, dropping registrations with
// earlier workers (each took its watchers along when it exited).
func setWatched(root string, pid int) {
	watchedRoots.Lock()
	defer watchedRoots.Unlock()
	for r, p := range watchedRoots.roots {
		if p != pid {
			delete(watchedRoots.roots, r)
		}
	}
	watchedRoots.roots[root] = pid
}

// unwatched forgets root's registration with worker pid.
func unwatched(root string, pid int) {
	watchedRoots.Lock()
	defer watchedRoots.Unlock()
	if watchedRoots.roots[root] == pid {
		delete(watchedRoots.roots, root)
	}
}

// rewatch registers with the new worker p, before it takes any call, the roots earlier
// workers watched: the governor recycles the worker, and its watchers go with it. A
// read that lands on p then still finds its root watched, and the root's start-up batch
// checks the whole tree for what changed while nobody watched.
func (p *workerProc) rewatch(ctx context.Context) {
	watchedRoots.Lock()
	roots := slices.Collect(maps.Keys(watchedRoots.roots))
	watchedRoots.Unlock()
	scope := budget.NewScope(nil)
	defer scope.Close()
	for _, root := range roots {
		if r := p.call(ctx, scope, nil, 0, watchSub, []string{"start", root}); r.err == nil && r.rpcErr == nil {
			setWatched(root, p.pid)
		}
	}
}

// WatchRoot registers the canonical root with the live worker's watcher, starting the
// worker when it is on but not running. A no-op when the worker is off or already
// watches root.
func WatchRoot(ctx context.Context, root string) {
	if root == "" || !workerEnabled() || Watched(root) {
		return
	}
	out, err := watchCall(ctx, "start", root)
	if err != nil {
		return
	}
	var st struct {
		State string `json:"watcher_state"`
	}
	if json.Unmarshal(out, &st) != nil || st.State == "" || st.State == "absent" {
		return
	}
	setWatched(root, coreWorker.runningPID())
}

// TakeRefresh takes root's next batch: the due one, or with force whatever is pending
// now (a read that found the index behind its identity; the batch may be empty). Nil
// when the live worker does not watch root, or no batch is due: the caller then
// refreshes the whole tree (or, for a due batch, nothing).
func TakeRefresh(ctx context.Context, root string, force bool) *RefreshBatch {
	if !Watched(root) {
		return nil
	}
	args := []string{"take", root}
	if force {
		args = append(args, "--force")
	}
	pid := coreWorker.runningPID()
	out, err := watchCall(ctx, args...)
	if err != nil {
		// not watched any more (evicted, or the worker was replaced): the next read
		// registers the root again
		unwatched(root, pid)
		return nil
	}
	var taken struct {
		Batch  *RefreshBatch `json:"batch"`
		Synced bool          `json:"synced"`
	}
	if json.Unmarshal(out, &taken) != nil || taken.Batch == nil {
		return nil
	}
	taken.Batch.Synced = taken.Synced
	return taken.Batch
}

// NoteChanged feeds root-relative paths a client reported changed (a Claude Code
// hook's FileChanged, an edit, a Bash command's changed files; PAR-FRESH-07) to root's
// watcher, where they join the pending batch like native events. It runs only on the
// live worker and starts nothing (WithResidentOnly): with no watcher on root it does
// nothing and reports false.
func NoteChanged(ctx context.Context, root string, rels []string) bool {
	if len(rels) == 0 || !Watched(root) {
		return false
	}
	args := []string{"note", root}
	args = append(args, rels...)
	_, err := watchCall(WithResidentOnly(ctx), args...)
	return err == nil
}

// FinishRefresh reports batch b done (ok) or failed; the watcher retries a failed batch
// with backoff. report is the index update's report; its refresh counters (reparsed,
// reresolved, escalated) go to the watcher's status.
func FinishRefresh(ctx context.Context, root string, b *RefreshBatch, ok bool, report []byte) {
	if b == nil {
		return
	}
	outcome := "failed"
	if ok {
		outcome = "ok"
	}
	if _, err := watchCall(ctx, "done", root, strconv.FormatInt(b.ID, 10), outcome, refreshSummary(report)); err != nil {
		log.Printf("code index %s: reporting refresh %d: %v", root, b.ID, err)
	}
}

// refreshSummary is the part of an index update report the watcher keeps, as JSON
// (empty when report is not one).
func refreshSummary(report []byte) string {
	var r struct {
		Mode     string `json:"mode"`
		Reason   string `json:"reason"`
		Counters struct {
			Reparsed   int  `json:"reparsed"`
			Reresolved int  `json:"reresolved"`
			Escalated  bool `json:"escalated"`
			Deleted    int  `json:"deleted"`
		} `json:"counters"`
		Timing struct {
			ElapsedMS int64 `json:"elapsed_ms"`
		} `json:"timing"`
	}
	if len(report) == 0 || json.Unmarshal(report, &r) != nil || r.Mode == "" {
		return ""
	}
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return string(b)
}

// workerNotice handles a frame the worker sent without a request id.
func (p *workerProc) workerNotice(body []byte) {
	var n struct {
		Method string `json:"method"`
		Params struct {
			Root  string `json:"root"`
			State string `json:"watcher_state"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &n) != nil || n.Params.Root == "" {
		return
	}
	switch {
	case n.Params.State == "absent":
		unwatched(n.Params.Root, p.pid)
	case n.Method == "$/refresh.due":
		if f := refreshDue.Load(); f != nil {
			go (*f)(n.Params.Root)
		}
	}
}

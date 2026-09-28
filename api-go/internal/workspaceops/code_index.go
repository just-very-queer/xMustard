package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/rustcore"
)

// Code index refresh (WS-14, PAR-FRESH-05). Search, explain and impact read the code
// index (index.db and its graph segment, served by the resident core) whenever the
// root has one. Before such a read, the index is brought to the repository identity the
// identity cache reports: when that key differs from the last one indexed, one
// `xmustard-core index update --identity-key K` runs as heavy work (a one-shot core
// in the governor's heavy slot, which rustcore takes for it, so the index writer's peak
// never lands in the resident worker). One refresh per root runs at a time and
// concurrent readers join it.
//
// Every read passes the identity key it observed to the core (`--identity-key=K`). The
// core answers from the index only when the index was brought to that key; otherwise
// the legacy graph for the tree as it is now answers, bounded by the same envelope. So
// an edit is never invisible while the index lags: a refused heavy slot, a failed
// update, a refresh still running and a daemon restart all read the current tree.
//
// Every wait is bounded. A reader waits at most codeIndexWait for a running refresh,
// or codeIndexFirstWait when this process has not brought the root's index up yet
// (possibly a full build), and never past its request. A key whose update failed is
// not retried until the identity changes. While other heavy work holds or waits for
// the slot, no refresh starts, so a read never queues behind a build.
//
// The watcher (WS-15, PAR-FRESH-03). With the resident worker on, every read (and
// ground) registers its root with the worker's watcher (rustcore.WatchRoot), which
// announces a debounced batch of changed paths when it is due; onRefreshDue then runs
// the refresh in the background, so the index usually reaches a new identity before
// any read asks. A refresh, whoever starts it, takes the watcher's batch and updates
// only its paths (`--paths`); a read's refresh takes the batch at once rather than
// waiting for the debounce. The whole tree is checked (no `--paths`) when there is no
// watcher (the worker off or unavailable), when the batch is a full one (start-up,
// overflow, an ignore file, a rescan, an OS watch limit) or unsynced, and when a read
// finds the index behind its identity although the watcher saw nothing. The watcher
// retries a failed batch with backoff. The identity check above stays: the core
// answers from the index only for the identity it was brought to.

// codeIndexWait bounds the wait for a refresh of an index this process brought up.
const codeIndexWait = 5 * time.Second

// codeIndexFirstWait bounds the wait for the first refresh of a root in this process.
const codeIndexFirstWait = 30 * time.Second

// codeIndexUpdateTimeout bounds one index update (a first full build included).
const codeIndexUpdateTimeout = 10 * time.Minute

// codeIndexRoots bounds the per-root refresh records kept.
const codeIndexRoots = 64

// indexRefresh is the part of `xmustard-core index update`'s report a read reports as
// its own work.
type indexRefresh struct {
	Mode     string `json:"mode"`   // full | incremental | noop
	Reason   string `json:"reason"` // why that mode ran
	Counters struct {
		Reparsed        int   `json:"reparsed"`
		Unchanged       int   `json:"unchanged"`
		ReusedFromCache int   `json:"reused_from_cache"`
		BytesRead       int64 `json:"bytes_read"`
	} `json:"counters"`
	Timing struct {
		ElapsedMS int64 `json:"elapsed_ms"`
	} `json:"timing"`
}

// changed reports whether the refresh rewrote the index (a read after it is served
// from a new generation).
func (r *indexRefresh) changed() bool { return r != nil && r.Mode != "noop" }

type refreshFlight struct {
	key string
	// forced: a read started it, and takes the watcher's pending changes at once.
	forced bool
	done   chan struct{}
	report *indexRefresh
}

type codeIndexRoot struct {
	indexed string // identity key of the last successful refresh
	failed  string // identity key whose refresh failed (not retried)
	flight  *refreshFlight
	used    time.Time
}

var codeIndex = struct {
	sync.Mutex
	roots map[string]*codeIndexRoot
}{roots: map[string]*codeIndexRoot{}}

// codeIndexRunner runs one index update with args; tests replace it.
var codeIndexRunner = func(ctx context.Context, root string, args ...string) ([]byte, error) {
	return rustcore.RunIndex(ctx, append([]string{"update", root}, args...)...)
}

// codeIndexFinishTimeout bounds reporting a batch's outcome to the watcher.
const codeIndexFinishTimeout = 10 * time.Second

func init() { rustcore.OnRefreshDue(onRefreshDue) }

// onRefreshDue starts the background refresh of a watched root whose batch is due,
// for the identity observed now. It leaves the batch pending while a refresh of the
// root runs, or while other heavy work holds or waits for the slot, so a read never
// joins a refresh queued behind a build; the watcher announces a batch nobody took
// again (index::watch RENOTIFY_AFTER).
func onRefreshDue(root string) {
	root = canonicalRoot(root)
	ctx, cancel := context.WithTimeout(context.Background(), codeIndexWait)
	id, _ := CurrentRepoIdentity(ctx, root)
	cancel()
	codeIndex.Lock()
	defer codeIndex.Unlock()
	st := codeIndexState(root)
	if st.flight != nil || budget.HeavyBusy() {
		return
	}
	f := &refreshFlight{key: id.Key, done: make(chan struct{})}
	st.flight = f
	go runCodeIndexRefresh(root, f)
}

// refreshArgs are an update's flags: the identity it is run for, and the batch's paths
// when the watcher's batch accounts for every change; otherwise the whole tree is
// checked. A read's refresh with an empty batch found the index behind its identity
// although the watcher saw nothing, so it checks the whole tree too.
func refreshArgs(key string, b *rustcore.RefreshBatch, forced bool) []string {
	var args []string
	if key != "" {
		args = append(args, "--identity-key", key)
	}
	if b == nil || b.Whole() || (forced && b.Empty()) {
		return args
	}
	return append(append(args, "--paths"), b.Paths...)
}

func codeIndexState(root string) *codeIndexRoot {
	st := codeIndex.roots[root]
	if st == nil {
		if len(codeIndex.roots) >= codeIndexRoots {
			evictCodeIndexRoot()
		}
		st = &codeIndexRoot{}
		codeIndex.roots[root] = st
	}
	st.used = time.Now()
	return st
}

// evictCodeIndexRoot drops the least recently used idle root record.
func evictCodeIndexRoot() {
	var oldest string
	for r, st := range codeIndex.roots {
		if st.flight == nil && (oldest == "" || st.used.Before(codeIndex.roots[oldest].used)) {
			oldest = r
		}
	}
	delete(codeIndex.roots, oldest)
}

// codeIndexRead is what one graph read takes to the core: the repository identity it
// observed and the refresh it waited for (nil when none ran for it).
type codeIndexRead struct {
	key     string
	refresh *indexRefresh
}

// flags are the core arguments carrying the observed identity.
func (r codeIndexRead) flags() []string {
	if r.key == "" {
		return nil
	}
	return []string{"--identity-key=" + r.key}
}

// annotate reports the refresh this read waited for in the result's coverage.work
// (see withRefreshWork); a result it cannot decode is returned as is.
func (r codeIndexRead) annotate(out []byte) json.RawMessage {
	if !r.refresh.changed() {
		return out
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(out, &res) != nil || res["coverage"] == nil {
		return out
	}
	res["coverage"] = withRefreshWork(res["coverage"], r.refresh)
	b, err := json.Marshal(res)
	if err != nil {
		return out
	}
	return b
}

// ensureCodeIndex brings root's code index to its current identity before a graph
// read, waiting a bounded time, and returns what the read passes to the core.
func ensureCodeIndex(ctx context.Context, root string) codeIndexRead {
	root = canonicalRoot(root)
	rustcore.WatchRoot(ctx, root)
	id := codeIndexIdentity(ctx, root)
	read := codeIndexRead{key: id.Key}
	if id.Key == "" {
		return read
	}
	codeIndex.Lock()
	st := codeIndexState(root)
	if st.indexed == id.Key || st.failed == id.Key {
		codeIndex.Unlock()
		return read
	}
	f := st.flight
	if f == nil && budget.HeavyBusy() {
		// heavy work is running or queued: the core reads the current tree instead
		codeIndex.Unlock()
		return read
	}
	if f == nil {
		f = &refreshFlight{key: id.Key, forced: true, done: make(chan struct{})}
		st.flight = f
		go runCodeIndexRefresh(root, f)
	}
	wait := codeIndexWait
	if st.indexed == "" {
		wait = codeIndexFirstWait
	}
	codeIndex.Unlock()

	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-f.done:
		read.refresh = f.report
	case <-t.C:
	case <-ctx.Done():
	}
	return read
}

// refreshOutcome is how a refresh ended: a refused heavy slot is retried on the next
// read, a failed update only once the identity changes.
type refreshOutcome int

const (
	refreshRefused refreshOutcome = iota
	refreshFailed
	refreshDone
)

// codeIndexIdentity is the request's own identity observation when the request is for
// root (sampled at most once per request, shared with evidence capture), else one
// through the identity cache.
func codeIndexIdentity(ctx context.Context, root string) RepoIdentity {
	if rc := RequestContextFrom(ctx); rc != nil && rc.Scope() == root {
		id, _ := rc.Identity(ctx)
		return id
	}
	id, _ := CurrentRepoIdentity(ctx, root)
	return id
}

func runCodeIndexRefresh(root string, f *refreshFlight) {
	defer close(f.done)
	outcome := refreshRefused
	defer func() {
		codeIndex.Lock()
		st := codeIndexState(root)
		st.flight = nil
		switch outcome {
		case refreshDone:
			st.indexed, st.failed = f.key, ""
		case refreshFailed:
			st.failed = f.key
		}
		codeIndex.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), codeIndexUpdateTimeout)
	defer cancel()
	batch := rustcore.TakeRefresh(ctx, root, f.forced)
	if batch == nil && !f.forced && rustcore.Watched(root) {
		return // the due batch was taken by a read's refresh meanwhile
	}
	// `index update` takes the governor's heavy slot inside rustcore (WS-06B); a
	// refused slot comes back as budget.ErrOverloaded and is retried on the next read
	// (and by the watcher, with backoff).
	out, err := codeIndexRunner(ctx, root, refreshArgs(f.key, batch, f.forced)...)
	fctx, fcancel := context.WithTimeout(context.Background(), codeIndexFinishTimeout)
	rustcore.FinishRefresh(fctx, root, batch, err == nil, out)
	fcancel()
	if err != nil {
		switch {
		case errors.Is(err, budget.ErrOverloaded):
			log.Printf("code index %s: heavy slot refused: %v", root, err)
		case errors.Is(err, context.Canceled):
			outcome = refreshFailed
		default:
			outcome = refreshFailed
			log.Printf("code index %s: update failed: %v", root, err)
		}
		return
	}
	outcome = refreshFailed
	var r indexRefresh
	if err := json.Unmarshal(out, &r); err != nil || r.Mode == "" {
		log.Printf("code index %s: undecodable update report", root)
		return
	}
	f.report, outcome = &r, refreshDone
}

// withRefreshWork reports a refresh this read waited for as the read's own index
// work in coverage.work: the index moved to a new generation built from `reparsed`
// parsed files. A read with no refresh (or a no-op one) keeps what the core reported.
func withRefreshWork(coverage json.RawMessage, r *indexRefresh) json.RawMessage {
	if !r.changed() || len(coverage) == 0 {
		return coverage
	}
	var cov map[string]json.RawMessage
	var work map[string]any
	if json.Unmarshal(coverage, &cov) != nil || json.Unmarshal(cov["work"], &work) != nil {
		return coverage
	}
	work["graph_cache"] = "miss"
	work["graph_cache_detail"] = "code index " + r.Mode + " update (" + r.Reason + ") before this read"
	work["files_parsed"] = r.Counters.Reparsed
	work["files_reused"] = r.Counters.Unchanged + r.Counters.ReusedFromCache
	work["bytes_parsed"] = r.Counters.BytesRead
	work["elapsed_ms"] = r.Timing.ElapsedMS
	raw, err := json.Marshal(work)
	if err != nil {
		return coverage
	}
	cov["work"] = raw
	out, err := json.Marshal(cov)
	if err != nil {
		return coverage
	}
	return out
}

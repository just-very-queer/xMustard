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
// `xmustard-core index update --identity-key K` runs as heavy work (the governor's
// heavy slot, a one-shot core, so the index writer's peak never lands in the resident
// worker). One refresh per root runs at a time and concurrent readers join it.
//
// Everything fails open. A reader waits at most codeIndexWait for a refresh when this
// process has already brought the root's index up once; it then reads the index it
// has, whose freshness envelope says how far behind it is. The first refresh of a root
// in this process (possibly a full build) is waited for, bounded by the request. With
// no identity, a refused heavy slot or a failed update, the read proceeds on whatever
// index exists, or on the legacy graph when there is none. A key whose update failed
// is not retried until the identity changes. While other heavy work holds or waits for
// the slot, no refresh starts, so a read never queues behind a build. WS-15's watcher
// replaces this per-read check.

// codeIndexWait bounds the wait for a refresh of an index that already answers.
const codeIndexWait = 5 * time.Second

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
	key    string
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

// codeIndexRunner runs one index update; tests replace it.
var codeIndexRunner = func(ctx context.Context, root, key string) ([]byte, error) {
	return rustcore.RunIndex(ctx, "update", root, "--identity-key", key)
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

// ensureCodeIndex brings root's code index to its current identity before a graph
// read and returns the refresh this read waited for, or nil when none ran for it.
func ensureCodeIndex(ctx context.Context, root string) *indexRefresh {
	root = canonicalRoot(root)
	id := codeIndexIdentity(ctx, root)
	if id.Key == "" {
		return nil
	}
	codeIndex.Lock()
	st := codeIndexState(root)
	if st.indexed == id.Key || st.failed == id.Key {
		codeIndex.Unlock()
		return nil
	}
	f := st.flight
	if f == nil && budget.HeavyBusy() {
		// heavy work is running or queued: read the index as it is
		codeIndex.Unlock()
		return nil
	}
	if f == nil {
		f = &refreshFlight{key: id.Key, done: make(chan struct{})}
		st.flight = f
		go runCodeIndexRefresh(root, f)
	}
	first := st.indexed == ""
	codeIndex.Unlock()

	var timeout <-chan time.Time
	if !first {
		t := time.NewTimer(codeIndexWait)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-f.done:
		return f.report
	case <-timeout:
	case <-ctx.Done():
	}
	return nil
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
	release, err := budget.AcquireHeavy(ctx, "index_update", budget.DefaultHeavyLineBytes)
	if err != nil {
		log.Printf("code index %s: heavy slot refused: %v", root, err)
		return
	}
	defer release()
	outcome = refreshFailed
	out, err := codeIndexRunner(ctx, root, f.key)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Printf("code index %s: update failed: %v", root, err)
		}
		return
	}
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

package workspaceops

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Coalescing recorder for search-retrieval feedback. Each search used to do a locked
// read-modify-write of agent_feedback.json, so concurrent searches on one workspace
// queued on that store and rewrote it once per call. Now a search only enqueues
// deltas into a bounded per-workspace buffer in memory, and a single flusher merges
// each buffer into its store (under the store lock) at most once per interval
// (XMUSTARD_FEEDBACK_FLUSH_INTERVAL, default 2s) and on graceful shutdown
// (ShutdownInFlight).
//
// Readers (feedbackBoosts) merge the unflushed deltas over the on-disk state, so a
// ranking sees feedback recorded moments ago. A buffer holds at most maxPending
// distinct paths: a new path past that evicts the oldest pending one and counts it in
// dropped, so enqueue never grows memory without bound and never waits on disk. A
// hard kill loses at most one interval of retrieval feedback, which is a ranking hint
// rather than a record.

const (
	feedbackMaxPendingPaths      = 4096
	defaultFeedbackFlushInterval = 2 * time.Second
)

type feedbackStoreKey struct{ dataDir, workspaceID string }

type feedbackBuffer struct {
	pending  map[string]feedbackDelta
	order    []string                 // pending paths, oldest first; overflow evicts from the front
	inflight map[string]feedbackDelta // taken by a flush, still visible to readers until saved
}

type feedbackRecorder struct {
	maxPending int

	mu      sync.Mutex
	buffers map[feedbackStoreKey]*feedbackBuffer
	armed   bool // a flush timer is pending

	flushMu sync.Mutex // one flusher at a time

	dropped       atomic.Uint64 // deltas evicted because a buffer was full
	reportedDrops uint64        // dropped value already logged; guarded by flushMu
	writes        atomic.Uint64 // store writes made by flushes
}

var feedbackRec = newFeedbackRecorder(feedbackMaxPendingPaths)

func newFeedbackRecorder(maxPending int) *feedbackRecorder {
	return &feedbackRecorder{maxPending: max(maxPending, 1), buffers: map[feedbackStoreKey]*feedbackBuffer{}}
}

// feedbackFlushInterval reads XMUSTARD_FEEDBACK_FLUSH_INTERVAL as a Go duration
// ("2s", "500ms") or whole seconds ("2"); anything else yields the default.
func feedbackFlushInterval() time.Duration {
	v := strings.TrimSpace(os.Getenv("XMUSTARD_FEEDBACK_FLUSH_INTERVAL"))
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return defaultFeedbackFlushInterval
}

// enqueue buffers one signal of kind for paths and arms the flush timer if none is
// pending. It never touches disk; invalid workspaces and empty path sets are ignored.
func (r *feedbackRecorder) enqueue(dataDir, workspaceID, kind string, paths []string) {
	if validateSafeID("workspace", workspaceID) != nil {
		return
	}
	clean := cleanPaths(paths)
	if len(clean) == 0 {
		return
	}
	now := nowUTC()
	key := feedbackStoreKey{dataDir, workspaceID}
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.buffers[key]
	if b == nil {
		b = &feedbackBuffer{pending: map[string]feedbackDelta{}}
		r.buffers[key] = b
	}
	for _, p := range clean {
		d, ok := b.pending[p]
		if !ok {
			if len(b.pending) >= r.maxPending {
				delete(b.pending, b.order[0])
				b.order = b.order[1:]
				r.dropped.Add(1)
			}
			b.order = append(b.order, p)
		}
		d.add(kind, now)
		b.pending[p] = d
	}
	if !r.armed {
		r.armed = true
		time.AfterFunc(feedbackFlushInterval(), r.flushOnTimer)
	}
}

// pending copies the unflushed deltas for one store: the buffer plus any batch a
// flush is still writing.
func (r *feedbackRecorder) pending(dataDir, workspaceID string) map[string]feedbackDelta {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.buffers[feedbackStoreKey{dataDir, workspaceID}]
	if b == nil {
		return nil
	}
	out := make(map[string]feedbackDelta, len(b.pending)+len(b.inflight))
	for _, src := range []map[string]feedbackDelta{b.inflight, b.pending} {
		for p, d := range src {
			o := out[p]
			o.merge(d)
			out[p] = o
		}
	}
	return out
}

func (r *feedbackRecorder) flushOnTimer() {
	r.mu.Lock()
	r.armed = false
	r.mu.Unlock()
	r.flush()
}

// flush merges every buffer into its store, one locked write per store. A failed
// write is logged and its batch discarded: the signal is best-effort, and retrying a
// store that keeps failing would pin its buffer.
func (r *feedbackRecorder) flush() {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.mu.Lock()
	batches := make(map[feedbackStoreKey]map[string]feedbackDelta, len(r.buffers))
	for key, b := range r.buffers {
		b.inflight, b.pending, b.order = b.pending, map[string]feedbackDelta{}, nil
		batches[key] = b.inflight
	}
	r.mu.Unlock()
	if d := r.dropped.Load(); d > r.reportedDrops {
		log.Printf("feedback: buffer full; dropped %d oldest pending path deltas since last flush", d-r.reportedDrops)
		r.reportedDrops = d
	}
	for key, batch := range batches {
		if persistFeedbackBatch(key, batch) {
			r.writes.Add(1)
		}
		r.mu.Lock()
		if b := r.buffers[key]; b != nil {
			b.inflight = nil
			if len(b.pending) == 0 {
				delete(r.buffers, key)
			}
		}
		r.mu.Unlock()
	}
}

// persistFeedbackBatch merges one store's batch and reports whether it wrote. A
// workspace directory removed after the search ran is not recreated by the late write.
func persistFeedbackBatch(key feedbackStoreKey, batch map[string]feedbackDelta) bool {
	if _, err := os.Stat(filepath.Dir(feedbackPath(key.dataDir, key.workspaceID))); err != nil {
		return false
	}
	if err := mergeFeedback(key.dataDir, key.workspaceID, batch); err != nil {
		log.Printf("feedback: flush for workspace %s failed; discarded %d path deltas: %v", key.workspaceID, len(batch), err)
		return false
	}
	return true
}

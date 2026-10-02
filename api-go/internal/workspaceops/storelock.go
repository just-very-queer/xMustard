package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
)

// Per-store transaction locking for the JSON stores that remain (runs, tokens,
// feedback, the workspace registry, audit logs). They mutate by load → modify → save.
// writeJSON is atomic and durable per write (fsync, rename, directory fsync), but the
// load-then-save window is not, so two writers both read the old file and the second
// clobbers the first (lost update). lockStore closes that window twice over:
//
//   - in the process, a one-slot gate keyed by the store's file path, so mutations to
//     different stores still run in parallel;
//   - across processes (a second API process, the ops CLI), an exclusive flock on a
//     sibling "<store>.lock" file. The store itself cannot carry the lock because each
//     save replaces its inode. On platforms without flock only the gate applies.
//
// Usage:
//
//	unlock, err := lockStore(feedbackPath(dataDir, ws))
//	if err != nil {
//		return err
//	}
//	defer unlock()
//	entries, _ := loadFeedback(dataDir, ws)
//	... mutate ...
//	saveFeedback(dataDir, ws, entries)
//
// A caller that has a request context uses lockStoreCtx instead, so a caller that goes
// away stops waiting for a holder rather than parking for the length of it. Both wait
// the same amount when the caller stays.
//
// Hold the lock across the full load→save span. Do NOT lock the load/save helpers
// themselves, and never acquire the same key re-entrantly within one goroutine
// (neither the gate nor a second flock descriptor is reentrant).

// storeGate is one store key's in-process lock: a one-slot channel rather than a
// sync.Mutex, because a mutex lock cannot be given up and a caller with a context must
// be able to stop waiting for a holder whose build outlives the request. The slot keeps
// the mutex's properties that the callers depend on: it is not reentrant, and it is held
// by exactly one caller at a time.
type storeGate chan struct{}

func (g storeGate) acquire(ctx context.Context) bool {
	select {
	case g <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (g storeGate) tryAcquire() bool {
	select {
	case g <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g storeGate) release() { <-g }

type storeLockRegistry struct {
	mu    sync.Mutex
	locks map[string]storeGate
}

var storeLocks = &storeLockRegistry{locks: make(map[string]storeGate)}

func (r *storeLockRegistry) get(key string) storeGate {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.locks[key]
	if !ok {
		g = make(storeGate, 1)
		r.locks[key] = g
	}
	return g
}

// errStoreBusy is tryLockStore's answer when another caller holds the lock.
var errStoreBusy = errors.New("held by another caller")

// lockStore acquires the in-process and cross-process locks for a store key (its file
// path) and returns the unlock function. A lock file that cannot be taken fails the
// call: a store mutated without it could lose another process's update. The registry
// map is bounded by (workspaces × store types), which is bounded operational state,
// not per-request growth.
func lockStore(key string) (func(), error) { return acquireStore(context.Background(), key, true) }

// tryLockStore is lockStore for a caller that must not wait: when either lock is held
// it fails at once with errStoreBusy.
func tryLockStore(key string) (func(), error) { return acquireStore(nil, key, false) }

// lockStoreCtx is lockStore for a caller that must be able to stop waiting: a ctx that
// ends while another caller holds the key gives up the wait and answers ctx.Err(), and
// leaves both locks untaken so the next caller is unaffected. A wait that gives up never
// falls through to the store, so nothing is mutated on a cancelled path.
func lockStoreCtx(ctx context.Context, key string) (func(), error) { return acquireStore(ctx, key, true) }

func acquireStore(ctx context.Context, key string, wait bool) (func(), error) {
	gate := storeLocks.get(key)
	switch {
	case !wait && !gate.tryAcquire():
		return nil, fmt.Errorf("lock store %s: %w", filepath.Base(key), errStoreBusy)
	case wait && !gate.acquire(ctx):
		return nil, fmt.Errorf("lock store %s: %w", filepath.Base(key), ctx.Err())
	}
	unlockFile, err := lockFile(ctx, key+".lock", wait)
	if err != nil {
		gate.release()
		return nil, fmt.Errorf("lock store %s: %w", filepath.Base(key), err)
	}
	return func() {
		unlockFile()
		gate.release()
	}, nil
}

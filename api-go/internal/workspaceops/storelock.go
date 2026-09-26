package workspaceops

import (
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
//   - in the process, a mutex keyed by the store's file path, so mutations to
//     different stores still run in parallel;
//   - across processes (a second API process, the ops CLI), an exclusive flock on a
//     sibling "<store>.lock" file. The store itself cannot carry the lock because each
//     save replaces its inode. On platforms without flock only the mutex applies.
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
// Hold the lock across the full load→save span. Do NOT lock the load/save helpers
// themselves, and never acquire the same key re-entrantly within one goroutine
// (neither sync.Mutex nor a second flock descriptor is reentrant).

type storeLockRegistry struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var storeLocks = &storeLockRegistry{locks: make(map[string]*sync.Mutex)}

func (r *storeLockRegistry) get(key string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.locks[key]
	if !ok {
		m = &sync.Mutex{}
		r.locks[key] = m
	}
	return m
}

// lockStore acquires the in-process and cross-process locks for a store key (its file
// path) and returns the unlock function. A lock file that cannot be taken fails the
// call: a store mutated without it could lose another process's update. The registry
// map is bounded by (workspaces × store types), which is bounded operational state,
// not per-request growth.
func lockStore(key string) (func(), error) {
	m := storeLocks.get(key)
	m.Lock()
	unlockFile, err := lockFile(key + ".lock")
	if err != nil {
		m.Unlock()
		return nil, fmt.Errorf("lock store %s: %w", filepath.Base(key), err)
	}
	return func() {
		unlockFile()
		m.Unlock()
	}, nil
}

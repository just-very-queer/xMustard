package workspaceops

import "sync"

// Per-store transaction locking. The JSON state stores mutate by load → modify →
// save. writeJSON is atomic per write, but the load-then-save window is unlocked, so
// two concurrent writers both read the old file and the second clobbers the first
// (lost update) — exactly the multi-agent load this product targets. A single global
// lock would serialize every store; instead we key a lock by the store's file path so
// mutations to *different* stores still run in parallel while mutations to the *same*
// store serialize their whole transaction.
//
// Usage:
//
//	unlock := lockStore(contextEntriesPath(dataDir, ws))
//	defer unlock()
//	entries, _ := loadContextEntries(dataDir, ws)
//	... mutate ...
//	saveContextEntries(dataDir, ws, entries)
//
// Hold the lock across the full load→save span. Do NOT lock the load/save helpers
// themselves, and never acquire the same key re-entrantly within one goroutine
// (sync.Mutex is not reentrant) — lock distinct store paths only.

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

// lockStore acquires the lock for a store key (its file path) and returns the
// unlock function. The registry map is bounded by (workspaces × store types), which
// is bounded operational state, not per-request growth.
func lockStore(key string) func() {
	m := storeLocks.get(key)
	m.Lock()
	return m.Unlock
}

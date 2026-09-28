package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"xmustard/api-go/internal/govstore"
)

// The governance store's startup check (WS-58). The daemon opens the store once after
// it starts listening: the open migrates the file to this build's schema, and PRAGMA
// quick_check runs on a short-lived connection with a small page cache. /api/health
// serves the result. A store that fails the check is refused to every memory call in
// the process from then on (fail closed), until an operator restores a backup
// (`xmustard-ops store restore`, which restarts the daemon). Memory calls that arrive
// during the check wait for it.

// Store health states.
const (
	StoreNone         = "none"    // no store yet: the first memory write creates it at the latest schema
	StorePending      = "pending" // the check is running
	StoreOK           = "ok"
	StoreCorrupt      = "corrupt"
	StoreSchemaTooNew = "schema_too_new" // a newer build migrated the file; this one refuses it
	StoreSchemaDrift  = "schema_drift"
	StoreError        = "error"
)

// storeFaultStates classify a failed check.
var storeFaultStates = []struct {
	err   error
	state string
}{
	{govstore.ErrCorrupt, StoreCorrupt},
	{govstore.ErrSchemaTooNew, StoreSchemaTooNew},
	{govstore.ErrSchemaDrift, StoreSchemaDrift},
}

// StoreHealth is the governance store's state as the daemon last checked it.
type StoreHealth struct {
	Status        string `json:"status"`
	SchemaVersion int    `json:"schema_version,omitempty"`
	CheckedAt     string `json:"checked_at,omitempty"`
	CheckMS       int64  `json:"check_ms,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

// Public is the part a caller without the full health view sees.
func (h StoreHealth) Public() StoreHealth {
	return StoreHealth{Status: h.Status, SchemaVersion: h.SchemaVersion}
}

var storeHealth = struct {
	sync.Mutex
	byPath map[string]StoreHealth
}{byPath: map[string]StoreHealth{}}

// CheckMemoryStore runs the startup check on dataDir's store, if there is one, and
// records the result for MemoryStoreHealth.
func CheckMemoryStore(ctx context.Context, dataDir string) StoreHealth {
	path, err := filepath.Abs(memoryStorePath(dataDir))
	if err != nil {
		return StoreHealth{Status: StoreError, Detail: err.Error()}
	}
	h := StoreHealth{Status: StoreNone}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		recordStoreHealth(path, StoreHealth{Status: StorePending})
		h = checkStore(ctx, dataDir)
	}
	h.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	recordStoreHealth(path, h)
	return h
}

// checkStore opens (migrates) and checks the store. The open store stays cached for
// the memory calls that follow: closing it saves no resident memory, because what the
// check adds is SQLite's code and allocator arenas, not the store's page cache.
func checkStore(ctx context.Context, dataDir string) StoreHealth {
	start := time.Now()
	s, release, err := acquireGovStore(ctx, dataDir, true)
	if err == nil {
		defer release()
		var info govstore.SchemaInfo
		if info, err = s.SchemaInfo(ctx); err == nil {
			return StoreHealth{Status: StoreOK, SchemaVersion: info.Version, CheckMS: time.Since(start).Milliseconds()}
		}
	}
	h := StoreHealth{Status: StoreError, Detail: err.Error(), CheckMS: time.Since(start).Milliseconds()}
	for _, f := range storeFaultStates {
		if errors.Is(err, f.err) {
			h.Status = f.state
			break
		}
	}
	return h
}

// MemoryStoreHealth is the last check's result for dataDir's store; its Status is ""
// when this process never checked it.
func MemoryStoreHealth(dataDir string) StoreHealth {
	path, err := filepath.Abs(memoryStorePath(dataDir))
	if err != nil {
		return StoreHealth{}
	}
	storeHealth.Lock()
	defer storeHealth.Unlock()
	return storeHealth.byPath[path]
}

func recordStoreHealth(path string, h StoreHealth) {
	storeHealth.Lock()
	defer storeHealth.Unlock()
	storeHealth.byPath[path] = h
}

// MemoryStorePath is the governance database of a data dir.
func MemoryStorePath(dataDir string) string { return memoryStorePath(dataDir) }

// noteStoreFault refuses the store at path from now on when err says it failed its
// integrity check, and returns the error callers get. memoryStores must be held.
func noteStoreFault(path string, err error) error {
	if !errors.Is(err, govstore.ErrCorrupt) {
		return err
	}
	memoryStores.faults[path] = fmt.Errorf("memory store %s failed its integrity check; memory calls are refused until a backup is restored (xmustard-ops store restore): %w", path, err)
	return memoryStores.faults[path]
}

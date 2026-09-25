package workspaceops

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Resident workspace registry (PAR-RT-06): workspace id → repository root, without
// parsing snapshot.json on tool paths. snapshot.json holds the whole scan (3.8 MB on
// the measured machine) and was parsed 5-6 times per `ground` only to read its
// workspace record. The registry reads just that record (the snapshot's leading
// "workspace" object, a bounded streaming read) and keeps it until the snapshot or
// workspaces.json changes.
//
// Invalidation is by file identity: size, mtime, ctime, inode and device of both
// files (on unix; size and mtime elsewhere), the stat key Git trusts for its index.
// As in Git's racy-index rule, a file whose mtime or ctime is within
// registryRacyWindow of the moment it was read is not trusted: it is re-read on the
// next lookup until it ages past the window, so a same-size rewrite inside one
// timestamp tick is never served stale. A replacement that preserves size and mtime
// (cp -p, rsync -a, a restore) still changes the ctime and usually the inode.
//
// The registry keeps the recorded root, not its symlink resolution: the canonical
// scope is resolved again on every lookup (a few lstats), so re-pointing a symlinked
// root takes effect on the next request.

const (
	// snapshotHeaderLimit bounds the bytes read to find the snapshot's workspace
	// record. Writers put it first or second (after scanner_version).
	snapshotHeaderLimit = 1 << 20
	registryRacyWindow  = 2 * time.Second
	registryMaxEntries  = 256
)

// ResolvedWorkspace is what the registry knows about one workspace.
type ResolvedWorkspace struct {
	WorkspaceID string
	// Root is the repository root recorded by the workspace snapshot (the same value
	// every tool path used when it parsed the snapshot).
	Root string
	// Scope is Root with symlinks resolved at lookup time (Root itself when that
	// fails): the evidence trust scope and the key of the repository identity cache.
	Scope  string
	Record workspaceRecord
}

// fileMark is the stat identity of a registry source file.
type fileMark struct {
	exists  bool
	size    int64
	mtimeNs int64
	ctimeNs int64
	ino     uint64
	dev     uint64
}

// trusted reports whether a mark read at readAt can identify the file's content:
// its newest timestamp is older than readAt minus the racy window.
func (m fileMark) trusted(readAt time.Time) bool {
	return newestNs(m.mtimeNs, m.ctimeNs) < readAt.Add(-registryRacyWindow).UnixNano()
}

// newestNs is the later of two timestamps (the package shadows the int max builtin).
func newestNs(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

type registryEntry struct {
	snap     fileMark
	records  fileMark
	trusted  bool
	ws       ResolvedWorkspace
	lastUsed time.Time
}

type recordsEntry struct {
	mark    fileMark
	trusted bool
	byID    map[string]workspaceRecord
}

type workspaceRegistry struct {
	mu      sync.Mutex
	entries map[string]*registryEntry // dataDir + "\x00" + workspace id
	records map[string]*recordsEntry  // dataDir
}

var registry = &workspaceRegistry{entries: map[string]*registryEntry{}, records: map[string]*recordsEntry{}}

// registryNow is the registry clock (tests shift it past the racy window).
var registryNow = time.Now

// Counting hooks: full snapshot parses (loadSnapshot) and bounded header reads.
var (
	snapshotLoads       atomic.Int64
	snapshotHeaderReads atomic.Int64
)

// validWorkspaceID refuses ids that could leave <dataDir>/workspaces.
func validWorkspaceID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`+"\x00")
}

func snapshotPath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json")
}

// resolveWorkspace returns the registry's view of a workspace. Like the snapshot
// parse it replaces, it fails (wrapping os.ErrNotExist when the file is missing)
// unless the workspace has a readable snapshot.
func resolveWorkspace(dataDir, workspaceID string) (ResolvedWorkspace, error) {
	if !validWorkspaceID(workspaceID) {
		return ResolvedWorkspace{}, fmt.Errorf("load snapshot: invalid workspace id: %w", os.ErrNotExist)
	}
	snap, err := statMark(snapshotPath(dataDir, workspaceID))
	if err != nil {
		return ResolvedWorkspace{}, fmt.Errorf("load snapshot: %w", err)
	}
	recMark, _ := statMark(filepath.Join(dataDir, "workspaces.json"))
	key := dataDir + "\x00" + workspaceID
	now := registryNow()
	registry.mu.Lock()
	if e := registry.entries[key]; e != nil && e.trusted && e.snap == snap && e.records == recMark {
		e.lastUsed = now
		ws := e.ws
		registry.mu.Unlock()
		return withScope(ws), nil
	}
	registry.mu.Unlock()

	header, err := readSnapshotHeader(snapshotPath(dataDir, workspaceID))
	if err != nil {
		return ResolvedWorkspace{}, fmt.Errorf("load snapshot: %w", err)
	}
	ws := ResolvedWorkspace{WorkspaceID: workspaceID, Root: header.RootPath, Record: header}
	if ws.Root == "" {
		if rec, ok, _ := registryRecord(dataDir, workspaceID, recMark, now); ok {
			ws.Root = rec.RootPath
		}
	}
	registry.mu.Lock()
	if len(registry.entries) >= registryMaxEntries {
		evictOldestRegistryEntry()
	}
	registry.entries[key] = &registryEntry{snap: snap, records: recMark,
		trusted: snap.trusted(now) && (!recMark.exists || recMark.trusted(now)), ws: ws, lastUsed: now}
	registry.mu.Unlock()
	return withScope(ws), nil
}

// withScope resolves the workspace's canonical scope now, so identity and handlers
// follow a re-pointed symlinked root from the next request on.
func withScope(ws ResolvedWorkspace) ResolvedWorkspace {
	if ws.Root != "" {
		ws.Scope = canonicalRoot(ws.Root)
	}
	return ws
}

// evictOldestRegistryEntry drops the least recently used entry. Caller holds mu.
func evictOldestRegistryEntry() {
	var oldest string
	var at time.Time
	for k, e := range registry.entries {
		if oldest == "" || e.lastUsed.Before(at) {
			oldest, at = k, e.lastUsed
		}
	}
	delete(registry.entries, oldest)
}

// registryRecord returns a workspace's record from workspaces.json, parsed once per
// file change. ok is false when the file is missing or has no such workspace.
func registryRecord(dataDir, workspaceID string, mark fileMark, now time.Time) (rec workspaceRecord, ok bool, err error) {
	if !mark.exists {
		return workspaceRecord{}, false, nil
	}
	registry.mu.Lock()
	if e := registry.records[dataDir]; e != nil && e.trusted && e.mark == mark {
		rec, ok := e.byID[workspaceID]
		registry.mu.Unlock()
		return rec, ok, nil
	}
	registry.mu.Unlock()
	var items []workspaceRecord
	if err := readJSON(filepath.Join(dataDir, "workspaces.json"), &items); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workspaceRecord{}, false, nil
		}
		return workspaceRecord{}, false, err
	}
	byID := make(map[string]workspaceRecord, len(items))
	for _, it := range items {
		byID[it.WorkspaceID] = it
	}
	registry.mu.Lock()
	if len(registry.records) >= registryMaxEntries {
		clear(registry.records)
	}
	registry.records[dataDir] = &recordsEntry{mark: mark, trusted: mark.trusted(now), byID: byID}
	registry.mu.Unlock()
	rec, ok = byID[workspaceID]
	return rec, ok, nil
}

// lookupWorkspaceRecord is getWorkspaceRecord without a snapshot parse: the
// workspaces.json record, else the snapshot's own workspace record.
func lookupWorkspaceRecord(dataDir, workspaceID string) (workspaceRecord, error) {
	recMark, err := statMark(filepath.Join(dataDir, "workspaces.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return workspaceRecord{}, err
	}
	rec, ok, err := registryRecord(dataDir, workspaceID, recMark, registryNow())
	if err != nil {
		return workspaceRecord{}, err
	}
	if ok {
		return rec, nil
	}
	ws, err := resolveWorkspace(dataDir, workspaceID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workspaceRecord{}, os.ErrNotExist
		}
		return workspaceRecord{}, err
	}
	return ws.Record, nil
}

// requireWorkspaceSnapshot is the "workspace exists" check tool paths made by
// parsing the snapshot; it now costs two stats on a registry hit.
func requireWorkspaceSnapshot(dataDir, workspaceID string) error {
	_, err := resolveWorkspace(dataDir, workspaceID)
	return err
}

// readSnapshotHeader decodes only the snapshot's top-level "workspace" object,
// skipping (not retaining) any value before it, within snapshotHeaderLimit bytes.
func readSnapshotHeader(path string) (workspaceRecord, error) {
	snapshotHeaderReads.Add(1)
	f, err := os.Open(path)
	if err != nil {
		return workspaceRecord{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, snapshotHeaderLimit))
	tok, err := dec.Token()
	if err != nil {
		return workspaceRecord{}, fmt.Errorf("snapshot header: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return workspaceRecord{}, errors.New("snapshot header: snapshot is not a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return workspaceRecord{}, fmt.Errorf("snapshot header: %w", err)
		}
		if key, _ := tok.(string); key == "workspace" {
			var rec workspaceRecord
			if err := dec.Decode(&rec); err != nil {
				return workspaceRecord{}, fmt.Errorf("snapshot header: %w", err)
			}
			return rec, nil
		}
		if err := skipJSONValue(dec); err != nil {
			return workspaceRecord{}, fmt.Errorf("snapshot header: no workspace record in the first %d bytes: %w", snapshotHeaderLimit, err)
		}
	}
	return workspaceRecord{}, errors.New("snapshot header: no workspace record")
}

// skipJSONValue consumes one value from dec without materializing it.
func skipJSONValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

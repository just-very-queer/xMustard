package workspaceops

import (
	"path/filepath"
	"sync"

	"xmustard/api-go/internal/budget"
)

// The per-file outline-hash cache (WS-28C). Change-driven invalidation hashes a symbol
// anchor's body out of the working file, through the code index's outline for that file.
// Those hashes depend only on the file's content, so a pass over an unchanged tree
// recomputes what the previous pass already computed. This cache keeps the per-file
// hashes across calls, keyed by workspace, root and path, and valid only while the file's
// fileMark is unchanged, so an edit is always a miss.
//
// The cache holds hashes, never verdicts: a hit returns the body hash the same file
// produced last time, and a miss re-reads and re-hashes. An evicted or reclaimed entry
// costs one re-read, never a wrong answer. It is bounded by entry count and by aggregate
// held bytes, and it is reclaimable, so memory pressure can hand the bytes back.

// Bounds: the entries the cache holds and the bytes it retains in all. The byte ceiling
// is a small fraction of the process-tree line. Past either bound an entry is simply not
// stored and the next pass re-hashes, which costs time and never correctness; refusing the
// insert (rather than evicting) keeps the held accounting independent of any eviction
// order.
const (
	outlineHashMaxEntries = 512
	outlineHashMaxHeld    = 4 << 20
	// outlineHashEntryOverhead is what one hash map costs beyond its key and value bytes:
	// the map header, the buckets and each entry's key and value headers. The bound only
	// has to be a ceiling that means something, not an exact accounting.
	outlineHashEntryOverhead = 64
)

// outlineHashEntry is one file's cached symbol-body hashes, keyed by normalized name path,
// and the fileMark they were computed under.
type outlineHashEntry struct {
	hashes map[string]string
	mark   fileMark
	held   int64
}

// outlineHashes is the process-wide cache. It is keyed per workspace and root, so two
// roots holding the same relative path never share an entry, and held is the aggregate
// retained bytes.
var outlineHashes = struct {
	sync.Mutex
	byKey map[string]*outlineHashEntry
	held  int64
}{byKey: map[string]*outlineHashEntry{}}

// outlineHashKey identifies a cached file. The mark, not the key, is what separates one
// version of the file from the next.
func outlineHashKey(workspaceID, root, rel string) string {
	return workspaceID + "\x00" + root + "\x00" + rel
}

// markOutlineFile reads the mark a cache entry for rel must match to be a hit: the same
// identity helpers this package's workspace registry keys files with, so one file has one
// notion of "unchanged" here. A file that cannot be stat'd has no mark and is never a
// hit.
func markOutlineFile(root, rel string) (fileMark, bool) {
	m, err := statMark(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || !m.exists {
		return fileMark{}, false
	}
	return m, true
}

// outlineHashesFor returns a file's cached body hashes when its mark is unchanged. ok is
// false when the caller must read and hash the file itself.
func outlineHashesFor(workspaceID, root, rel string, mark fileMark) (hashes map[string]string, ok bool) {
	outlineHashes.Lock()
	defer outlineHashes.Unlock()
	e, cached := outlineHashes.byKey[outlineHashKey(workspaceID, root, rel)]
	if !cached || e.mark != mark {
		return nil, false
	}
	return e.hashes, true
}

// storeOutlineHashes records a file's freshly computed hashes under its current mark,
// when there is room. A file past either bound is not cached and the next pass re-hashes
// it; a file replacing an older entry for the same key releases the older one's bytes
// first, so the held total never double-counts a path.
func storeOutlineHashes(workspaceID, root, rel string, mark fileMark, hashes map[string]string) {
	held := int64(len(hashes) * outlineHashEntryOverhead)
	for k, v := range hashes {
		held += int64(len(k) + len(v))
	}
	key := outlineHashKey(workspaceID, root, rel)
	outlineHashes.Lock()
	defer outlineHashes.Unlock()
	if old, ok := outlineHashes.byKey[key]; ok {
		outlineHashes.held -= old.held
		delete(outlineHashes.byKey, key)
	}
	if len(outlineHashes.byKey) >= outlineHashMaxEntries || outlineHashes.held+held > outlineHashMaxHeld {
		return
	}
	outlineHashes.byKey[key] = &outlineHashEntry{hashes: hashes, mark: mark, held: held}
	outlineHashes.held += held
}

// clearOutlineHashes drops every entry: the reclaim hook and tests both use it.
func clearOutlineHashes() {
	outlineHashes.Lock()
	clear(outlineHashes.byKey)
	outlineHashes.held = 0
	outlineHashes.Unlock()
}

// outlineHashEntries is how many entries the cache holds now.
func outlineHashEntries() int {
	outlineHashes.Lock()
	defer outlineHashes.Unlock()
	return len(outlineHashes.byKey)
}

// outlineHashUsed is what the cache retains now, for the budget component.
func outlineHashUsed() (int64, bool) {
	outlineHashes.Lock()
	defer outlineHashes.Unlock()
	return outlineHashes.held, true
}

// outlineHashesEnabled reports whether the cache holds anything, so the governor's
// component reserves nothing in a process that never invalidated a memory.
func outlineHashesEnabled() bool {
	return outlineHashEntries() > 0
}

// reclaimOutlineHashes is the component's reclaim hook. It drops the cache and reports
// when the bytes are back; dropping an entry only costs the next pass a re-read, so there
// is nothing here worth holding on to.
func reclaimOutlineHashes(budget.Pressure) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		clearOutlineHashes()
		close(done)
	}()
	return done
}

func init() {
	budget.RegisterProcessComponent(budget.Component{
		Name:      "outline_hash_cache",
		Kind:      budget.ComponentTransient,
		PeakBytes: outlineHashMaxHeld,
		UsedBasis: "declared_bytes",
		Used:      outlineHashUsed,
		Reclaim:   reclaimOutlineHashes,
		Enabled:   outlineHashesEnabled,
	})
}

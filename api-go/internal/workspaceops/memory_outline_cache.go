package workspaceops

import (
	"maps"
	"sync"
	"time"

	"xmustard/api-go/internal/budget"
)

// The per-file outline-hash cache (WS-28C). Change-driven invalidation hashes a symbol
// anchor's body out of the working file, through the code index's outline for that file.
// Those hashes depend only on the file's content, so a pass over an unchanged tree
// recomputes what the previous pass already computed. This cache keeps the per-file
// hashes across calls, keyed by workspace, root and path, and valid only while the file's
// fileMark is unchanged and old enough to trust.
//
// It holds hashes, never verdicts: a hit returns the body hash the same file produced last
// time, and a miss re-reads and re-hashes. An evicted or reclaimed entry costs one
// re-read, never a wrong answer. It is bounded by entry count and by aggregate held bytes,
// and it is reclaimable, so memory pressure can hand the bytes back.
//
// Three rules keep a hit from ever being a stale verdict, and each is a review finding:
//   - A mark is bound to the version actually read: the caller reads the mark before the
//     file's text and re-reads it after hashing, and caches only when it did not move.
//     Reading the mark only afterwards would store one version's hash under another's.
//   - A mark inside registryRacyWindow is not trusted, exactly as the workspace registry
//     does: on a filesystem with coarse timestamps a same-size rewrite inside one tick
//     shares its mark, so such an entry is never served as a hit and never cached.
//   - Adding a declaration's hash is one operation under the cache lock, so two passes
//     that both extend one file's entry cannot drop each other's hash.

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

// outlineHashTrustWindow is the racy window a mark must age past before the cache will
// serve it: the workspace registry's, because it is the same fileMark and the same reason.
// It is a var so a test can close the window; a fixture writes its files this instant, and
// no file can be made to carry an old ctime, so ageing one is not possible from a test.
var outlineHashTrustWindow = registryRacyWindow

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
// notion of "unchanged" here. A file that cannot be opened has no mark and is never a hit.
//
// The mark is taken from the descriptor the workspace reader opens, not from statMark on
// the path: the reader refuses a symlink component, and a mark taken through a symlink
// could identify a file the reader would refuse, so a warm entry would answer a path the
// confined read rejects. The cache must never be the weaker of the two.
func markOutlineFile(root, rel string) (fileMark, bool) {
	f, err := openWorkspaceFileBeneath(root, rel)
	if err != nil {
		return fileMark{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fileMark{}, false
	}
	return fileMarkFromInfo(info), true
}

// trustedOutlineMark is a mark old enough to serve from the cache. A file written inside
// the racy window has a mark another write inside the same tick can share, so it is read
// rather than cached until it ages out; that is the registry's rule for the same reason.
// fileMark.trusted already subtracts registryRacyWindow from readAt, and
// outlineHashTrustWindow is that same window, so readAt is passed through unchanged; a
// test sets the window to zero to close the rule.
func trustedOutlineMark(m fileMark, readAt time.Time) bool {
	if outlineHashTrustWindow <= 0 {
		return true
	}
	return m.trusted(readAt)
}

// outlineHashesFor returns a file's cached body hashes when its mark is unchanged and old
// enough to trust. ok is false when the caller must read and hash the file itself.
func outlineHashesFor(workspaceID, root, rel string, mark fileMark, readAt time.Time) (hashes map[string]string, ok bool) {
	if !trustedOutlineMark(mark, readAt) {
		return nil, false
	}
	outlineHashes.Lock()
	defer outlineHashes.Unlock()
	e, cached := outlineHashes.byKey[outlineHashKey(workspaceID, root, rel)]
	if !cached || e.mark != mark {
		return nil, false
	}
	return e.hashes, true
}

// storeOutlineHash adds one declaration's hash to a file's entry, creating the entry when
// there is none, in one operation under the cache lock so two passes extending the same
// entry cannot drop each other's hash. mark must be the mark the body was hashed under and
// the caller must have re-read it after the read (see trustedOutlineMark); an untrusted
// mark is refused, and so is any result past the byte ceiling or, for a new entry, past the
// entry bound. The ceiling is checked on every store, not only when an entry is created.
func storeOutlineHash(workspaceID, root, rel string, mark fileMark, readAt time.Time, want, hash string) {
	if !trustedOutlineMark(mark, readAt) {
		return
	}
	key := outlineHashKey(workspaceID, root, rel)
	outlineHashes.Lock()
	defer outlineHashes.Unlock()
	old, cached := outlineHashes.byKey[key]
	// Adding to the version already cached costs one key; any other case (a new file, or
	// one whose mark moved) starts the entry over.
	grow := cached && old.mark == mark
	hashes := map[string]string{}
	if grow {
		if _, known := old.hashes[want]; known {
			return
		}
		hashes = maps.Clone(old.hashes)
	}
	hashes[want] = hash
	held := outlineHashHeld(hashes)
	// The budget this store would move the total to: grow adds to the entry already
	// counted, everything else replaces it, so the old entry's bytes leave the total once.
	moved := outlineHashes.held - heldBefore(old, cached, grow) + held
	if moved > outlineHashMaxHeld {
		return
	}
	if !grow && len(outlineHashes.byKey) >= outlineHashMaxEntries {
		return
	}
	outlineHashes.byKey[key] = &outlineHashEntry{hashes: hashes, mark: mark, held: held}
	outlineHashes.held = moved
}

// heldBefore is the entry's contribution to the total before this store: a grown entry was
// already counted, a replaced one was, and an absent one was not.
func heldBefore(old *outlineHashEntry, cached, grow bool) int64 {
	if !cached || grow {
		return 0
	}
	return old.held
}

// outlineHashHeld is the bytes one entry's map retains: each key and value, plus a fixed
// per-entry overhead. The bound only has to be a ceiling that means something, not an
// exact accounting.
func outlineHashHeld(hashes map[string]string) int64 {
	n := int64(len(hashes) * outlineHashEntryOverhead)
	for k, v := range hashes {
		n += int64(len(k) + len(v))
	}
	return n
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

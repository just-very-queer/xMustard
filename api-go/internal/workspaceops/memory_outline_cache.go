package workspaceops

import (
	"io"
	"maps"
	"os"
	"runtime"
	"strings"
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
//
// It is a var so a test can move the moment a file changes; production never replaces it.
var markOutlineFile = markOutlineFileNow

// afterOutlineFileRead is a test seam for a rewrite between the read and its final stat.
var afterOutlineFileRead = func() {}

func markOutlineFileNow(root, rel string) (fileMark, bool) {
	f, info, ok := openOutlineFile(root, rel)
	if !ok {
		return fileMark{}, false
	}
	mark := fileMarkFromFile(f, info)
	f.Close()
	return mark, true
}

// readOutlineFile reads rel through the confined walk and returns its identity and its
// text from the SAME descriptor. The mark and the text therefore describe one version of
// one file: a rename that swaps another file in between two separate opens cannot make the
// pair disagree, because there is only one open. fd is nil: the descriptor is closed
// before returning, and the text is what a caller keeps.
func readOutlineFile(root, rel string, limit int64) (fileMark, string, bool) {
	f, info, ok := openOutlineFile(root, rel)
	if !ok {
		return fileMark{}, "", false
	}
	defer f.Close()
	if info.Size() > limit {
		return fileMark{}, "", false
	}
	var b strings.Builder
	b.Grow(int(info.Size()))
	before := fileMarkFromFile(f, info)
	n, err := io.Copy(&b, io.LimitReader(f, limit+1))
	if err != nil || n > limit {
		return fileMark{}, "", false
	}
	afterOutlineFileRead()
	after := fileMarkFromFile(f, info)
	if before != after {
		return fileMark{}, "", false
	}
	return before, b.String(), true
}

// openOutlineFile opens rel beneath root through the symlink-refusing walk and returns it
// with its stat when it is a regular file; the caller closes it.
func openOutlineFile(root, rel string) (*os.File, os.FileInfo, bool) {
	f, err := openWorkspaceFileBeneath(root, rel)
	if err != nil {
		return nil, nil, false
	}
	info, err := f.Stat() // fstat on the held fd, not a re-stat by path
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, false
	}
	return f, info, true
}

// trustedOutlineMark is a mark old enough to serve from the cache. A file written inside
// the racy window has a mark another write inside the same tick can share, so it is read
// rather than cached until it ages out; that is the registry's rule for the same reason.
// The window is outlineHashTrustWindow itself, not fileMark.trusted's built-in one: the
// mark must be at least that far in the past, and any other window (a test's wider one)
// has to take effect, which passing the value through to trusted() would not do.
func trustedOutlineMark(m fileMark, readAt time.Time) bool {
	if outlineHashTrustWindow <= 0 {
		return true
	}
	return newestNs(m.mtimeNs, m.ctimeNs) < readAt.Add(-outlineHashTrustWindow).UnixNano()
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
	held := outlineHashHeld(hashes, key)
	// The total this store moves the cache to. Both cases release what was counted: a
	// grown entry is re-counted at its new size, and a replaced one stops counting. Only
	// a genuinely new entry adds to the total.
	released := int64(0)
	if cached {
		released = old.held
	}
	moved := outlineHashes.held - released + held
	if moved > outlineHashMaxHeld {
		return
	}
	// A replacement does not add an entry, so it is allowed at the entry bound; only a
	// new one is refused there.
	if !cached && len(outlineHashes.byKey) >= outlineHashMaxEntries {
		return
	}
	outlineHashes.byKey[key] = &outlineHashEntry{hashes: hashes, mark: mark, held: held}
	outlineHashes.held = moved
}

// outlineHashHeld is the bytes one entry retains: each key and value, the entry's own
// struct and map header, and the cache key it is filed under (workspace, root and path).
// The key counts because 512 of them are what the aggregate ceiling has to cover.
func outlineHashHeld(hashes map[string]string, key string) int64 {
	n := int64(len(hashes)*outlineHashEntryOverhead + len(key) + outlineHashEntryOverhead)
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
// is nothing here worth holding on to. The channel closes after a GC cycle, because the
// governor resamples RSS when it closes and a collection has not run by then.
func reclaimOutlineHashes(budget.Pressure) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		clearOutlineHashes()
		runtime.GC()
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

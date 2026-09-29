package evidence

import (
	"crypto/sha256"
	"sync"
	"time"
)

// The revision-keyed read-only result cache (PAR-CTX-14). A successful result of a
// cacheable tool (search, explain, impact, diagnostics: ToolPolicy.Cacheable) is kept
// under its Key (render_key.go: tool, canonical arguments, repository identity, memory
// revision, parser version, source revision, renderer version) and served again to
// the same principal in the same workspace while every one of those is unchanged. A
// revision change is a different key, so it misses; the stale entry ages out of the
// LRU. The cache holds delivered tool results only, never a prompt or a model's answer.
//
// It is capped by bytes, not entries, and scoped per principal: a hit shares the prior
// delivery's recovery handle, which the evidence store binds to the principal that
// captured it, so the handle stays readable by whoever is served the hit. An entry
// whose handle is about to expire is dropped rather than served.

// CacheLimits bound one result cache.
type CacheLimits struct {
	// MaxBytes caps the accounted bytes of all entries (body, handle, content type,
	// digest and a fixed per-entry overhead).
	MaxBytes int64
	// MaxEntryBytes is the largest single entry; a larger result is not cached, so one
	// result can never flush the cache.
	MaxEntryBytes int64
}

// DefaultCacheLimits are the limits measured against the WS-42 budget line: with the
// ledger's 0.43 MiB, the two stay under its 1.5 MiB
// (TestEvidenceCachesStayWithinTheirBudgetLine).
func DefaultCacheLimits() CacheLimits {
	return CacheLimits{MaxBytes: 896 << 10, MaxEntryBytes: 128 << 10}
}

// cacheEntryOverhead is the accounted cost of one entry beyond its variable bytes: the
// entry struct (a 192-byte size class) and its map slot.
const cacheEntryOverhead = 256

// cacheHandleMinTTL is the least time a shared handle must still live to be served.
const cacheHandleMinTTL = time.Minute

// CacheScope is whom a cached result belongs to.
type CacheScope struct {
	Workspace string
	Principal string // stable principal ID; "" when authentication is not enforced
}

// CachedResult is one delivered result as the cache keeps it.
type CachedResult struct {
	// Body is the delivered result (a projection or an evidence envelope).
	Body        []byte
	ContentType string
	// Handle and ExpiresAt recover the original the Body was projected from ("" and
	// zero when nothing was omitted).
	Handle    string
	ExpiresAt time.Time
	RawSHA256 string
	// IsError marks a failed result, which is never cached: a failure may be transient
	// (a timeout, an overload) at an unchanged revision.
	IsError bool
}

// CacheInfo is what a lookup reports (the `cache {hit, revision_key}` of a result).
type CacheInfo struct {
	Hit         bool   `json:"hit"`
	RevisionKey string `json:"revision_key"`
}

// CacheStats are counters since the cache was made.
type CacheStats struct {
	Entries   int   `json:"entries"`
	Bytes     int64 `json:"bytes"`
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Stored    int64 `json:"stored"`
	Evictions int64 `json:"evictions"`
	Refused   int64 `json:"refused"`
}

// ResultCache is safe for concurrent use.
type ResultCache struct {
	limits CacheLimits
	now    func() time.Time

	mu      sync.Mutex
	entries map[[32]byte]*cacheEntry
	head    cacheEntry // LRU sentinel; most recently used after it
	stats   CacheStats
}

type cacheEntry struct {
	key        [32]byte
	workspace  string
	result     CachedResult
	cost       int64
	prev, next *cacheEntry
}

// NewResultCache returns an empty cache; zero limits take their defaults.
func NewResultCache(limits CacheLimits) *ResultCache {
	def := DefaultCacheLimits()
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = def.MaxBytes
	}
	if limits.MaxEntryBytes <= 0 || limits.MaxEntryBytes > limits.MaxBytes {
		limits.MaxEntryBytes = min(def.MaxEntryBytes, limits.MaxBytes)
	}
	c := &ResultCache{limits: limits, now: time.Now, entries: map[[32]byte]*cacheEntry{}}
	c.head.prev, c.head.next = &c.head, &c.head
	return c
}

// SetClock replaces the cache clock (tests).
func (c *ResultCache) SetClock(now func() time.Time) { c.now = now }

// cacheKey scopes a key to a workspace and principal.
func cacheKey(scope CacheScope, k Key) [32]byte {
	d := k.Digest()
	h := sha256.New()
	writeFields(h, "xm-result-cache/1", scope.Workspace, scope.Principal, string(d[:]))
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// cacheable reports whether a key may be cached: a cacheable tool with a derived key.
func cacheable(k Key) bool {
	return k.digest != [32]byte{} && ToolPolicyFor(k.Tool).Cacheable
}

// Get returns the result cached for key in scope. The info carries the key's revision
// key either way.
func (c *ResultCache) Get(scope CacheScope, k Key) (CachedResult, CacheInfo, bool) {
	info := CacheInfo{RevisionKey: k.RevisionKey()}
	if !cacheable(k) {
		return CachedResult{}, info, false
	}
	ck := cacheKey(scope, k)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[ck]
	if ok && !handleLive(e.result, c.now()) {
		c.remove(e)
		c.stats.Evictions++
		ok = false
	}
	if !ok {
		c.stats.Misses++
		return CachedResult{}, info, false
	}
	c.touch(e)
	c.stats.Hits++
	info.Hit = true
	return e.result, info, true
}

// Put caches a successful result of a cacheable tool; it reports whether it did. It
// refuses an error, an entry over MaxEntryBytes and a result whose handle is about to
// expire. The cache keeps its own copy of the body.
func (c *ResultCache) Put(scope CacheScope, k Key, r CachedResult) bool {
	cost := int64(len(r.Body)+len(r.Handle)+len(r.ContentType)+len(r.RawSHA256)+len(scope.Workspace)) + cacheEntryOverhead
	if !cacheable(k) || r.IsError {
		c.refuse()
		return false
	}
	// the copy's capacity is its allocation (rounded up to a size class or to pages),
	// which is what the entry holds; MaxEntryBytes is checked against that accounted
	// cost, not the pre-copy estimate, so a stored entry never exceeds the limit it was
	// admitted under.
	r.Body = append([]byte(nil), r.Body...)
	cost += int64(cap(r.Body) - len(r.Body))
	if cost > c.limits.MaxEntryBytes {
		c.refuse()
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !handleLive(r, c.now()) {
		c.stats.Refused++
		return false
	}
	ck := cacheKey(scope, k)
	if old, ok := c.entries[ck]; ok {
		c.remove(old)
	}
	e := &cacheEntry{key: ck, workspace: scope.Workspace, result: r, cost: cost}
	c.entries[ck] = e
	c.touch(e)
	c.stats.Bytes += cost
	c.stats.Stored++
	for c.stats.Bytes > c.limits.MaxBytes {
		c.remove(c.head.prev)
		c.stats.Evictions++
	}
	return true
}

func (c *ResultCache) refuse() {
	c.mu.Lock()
	c.stats.Refused++
	c.mu.Unlock()
}

// Purge drops every entry of a workspace (deleted or revoked workspace).
func (c *ResultCache) Purge(workspace string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if e.workspace == workspace {
			c.remove(e)
		}
	}
}

// Stats reports the cache's counters.
func (c *ResultCache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Entries = len(c.entries)
	return s
}

// handleLive reports whether a result's handle (if any) lives long enough to share.
func handleLive(r CachedResult, now time.Time) bool {
	return r.Handle == "" || r.ExpiresAt.Sub(now) >= cacheHandleMinTTL
}

func (c *ResultCache) touch(e *cacheEntry) {
	if e.next != nil {
		e.prev.next, e.next.prev = e.next, e.prev
	}
	e.prev, e.next = &c.head, c.head.next
	c.head.next.prev = e
	c.head.next = e
}

func (c *ResultCache) remove(e *cacheEntry) {
	e.prev.next, e.next.prev = e.next, e.prev
	e.prev, e.next = nil, nil
	delete(c.entries, e.key)
	c.stats.Bytes -= e.cost
}

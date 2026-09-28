package evidence

import (
	"bytes"
	"fmt"
	"runtime"
	"testing"
	"time"
)

func newTestCache(limits CacheLimits) *ResultCache {
	c := NewResultCache(limits)
	c.SetClock(func() time.Time { return ledgerNow })
	return c
}

func mustKey(t *testing.T, tool, args string, rev Revisions) Key {
	t.Helper()
	k, err := NewKey(tool, []byte(args), rev)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func cached(t *testing.T, body string) CachedResult {
	return CachedResult{Body: []byte(body), ContentType: "application/json", Handle: testHandle(t),
		ExpiresAt: ledgerNow.Add(24 * time.Hour), RawSHA256: "ab"}
}

var alice = CacheScope{Workspace: "ws", Principal: "agent-a"}

func TestResultCacheServesTheSameRevisionAndSharesTheHandle(t *testing.T) {
	c := newTestCache(CacheLimits{})
	k := mustKey(t, "search", `{"q":"Double"}`, completeRev())
	if _, info, ok := c.Get(alice, k); ok || info.Hit || info.RevisionKey != k.RevisionKey() {
		t.Fatalf("empty cache: %+v", info)
	}
	r := cached(t, searchBody)
	if !c.Put(alice, k, r) {
		t.Fatal("put refused")
	}
	r.Body[0] = 'X' // the cache keeps its own copy
	got, info, ok := c.Get(alice, mustKey(t, "mcp__xmustard__search", `{ "q": "Double" }`, completeRev()))
	if !ok || !info.Hit || got.Handle != r.Handle || string(got.Body) != searchBody || info.RevisionKey != k.RevisionKey() {
		t.Fatalf("hit: %+v %+v", info, got)
	}
	if s := c.Stats(); s.Hits != 1 || s.Misses != 1 || s.Entries != 1 {
		t.Fatalf("stats %+v", s)
	}
}

func TestResultCacheMissesOnAnyRevisionChangeAndIsScopedPerPrincipal(t *testing.T) {
	c := newTestCache(CacheLimits{})
	c.Put(alice, mustKey(t, "search", `{"q":"Double"}`, completeRev()), cached(t, searchBody))
	for name, change := range map[string]func(*Revisions){
		"repository": func(r *Revisions) { r.Repo.Key = "repo-2" },
		"memory":     func(r *Revisions) { r.Memory = "mem-2" },
		"parser":     func(r *Revisions) { r.Parser = "xm-index-3" },
		"source":     func(r *Revisions) { r.Source = "diag-2" },
	} {
		rev := completeRev()
		change(&rev)
		if _, _, ok := c.Get(alice, mustKey(t, "search", `{"q":"Double"}`, rev)); ok {
			t.Errorf("a %s revision change hit", name)
		}
	}
	k := mustKey(t, "search", `{"q":"Double"}`, completeRev())
	for _, other := range []CacheScope{{Workspace: "ws", Principal: "agent-b"}, {Workspace: "ws"}, {Workspace: "ws2", Principal: "agent-a"}} {
		if _, _, ok := c.Get(other, k); ok {
			t.Errorf("scope %+v was served another principal's result", other)
		}
	}
	if _, _, ok := c.Get(alice, mustKey(t, "search", `{"q":"Other"}`, completeRev())); ok {
		t.Error("other arguments hit")
	}
}

func TestResultCacheRefusesWhatItMustNotKeep(t *testing.T) {
	c := newTestCache(CacheLimits{MaxBytes: 64 << 10, MaxEntryBytes: 8 << 10})
	rev := completeRev()
	for _, tool := range []string{"ground", "recall", "remember", "verify", "why_failed", "Read", "Bash"} {
		k, err := NewKey(tool, []byte(`{}`), rev)
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if c.Put(alice, k, cached(t, "{}")) {
			t.Errorf("%s result cached", tool)
		}
		if _, _, ok := c.Get(alice, k); ok {
			t.Errorf("%s served from cache", tool)
		}
	}
	k := mustKey(t, "impact", `{"path":"a.go"}`, rev)
	failed := cached(t, `{"error":"timeout"}`)
	failed.IsError = true
	if c.Put(alice, k, failed) {
		t.Error("an error was cached")
	}
	if c.Put(alice, k, cached(t, string(bytes.Repeat([]byte("x"), 9<<10)))) {
		t.Error("an entry over MaxEntryBytes was cached")
	}
	expiring := cached(t, "{}")
	expiring.ExpiresAt = ledgerNow.Add(30 * time.Second)
	if c.Put(alice, k, expiring) {
		t.Error("a result whose handle is about to expire was cached")
	}
	if !c.Put(alice, k, CachedResult{Body: []byte("{}")}) {
		t.Error("an unreduced result (no handle) was refused")
	}
	// an entry whose handle expires while cached is dropped, not served
	live := cached(t, "{}")
	live.ExpiresAt = ledgerNow.Add(2 * time.Minute)
	c.Put(alice, k, live)
	c.SetClock(func() time.Time { return ledgerNow.Add(90 * time.Second) })
	if _, _, ok := c.Get(alice, k); ok {
		t.Error("an expiring handle was served")
	}
	if s := c.Stats(); s.Entries != 0 {
		t.Errorf("expired entry kept: %+v", s)
	}
}

func TestResultCacheIsCappedByBytesWithLRUEviction(t *testing.T) {
	c := newTestCache(CacheLimits{MaxBytes: 40 << 10, MaxEntryBytes: 16 << 10})
	rev := completeRev()
	body := string(bytes.Repeat([]byte("y"), 7<<10))
	for i := 0; i < 20; i++ {
		c.Put(alice, mustKey(t, "explain", fmt.Sprintf(`{"path":"f%d.go"}`, i), rev), cached(t, body))
		if i == 3 {
			// touch f0 so the LRU keeps it
			if _, _, ok := c.Get(alice, mustKey(t, "explain", `{"path":"f0.go"}`, rev)); !ok {
				t.Fatal("f0 evicted early")
			}
		}
		if s := c.Stats(); s.Bytes > 40<<10 {
			t.Fatalf("over the byte cap: %+v", s)
		}
	}
	s := c.Stats()
	if s.Entries >= 20 || s.Evictions == 0 {
		t.Fatalf("no eviction under the byte cap: %+v", s)
	}
	if _, _, ok := c.Get(alice, mustKey(t, "explain", `{"path":"f19.go"}`, rev)); !ok {
		t.Fatal("the newest entry was evicted")
	}
	if _, _, ok := c.Get(alice, mustKey(t, "explain", `{"path":"f1.go"}`, rev)); ok {
		t.Fatal("the least recently used entry survived")
	}
	c.Purge("ws")
	if s := c.Stats(); s.Entries != 0 || s.Bytes != 0 {
		t.Fatalf("purge left %+v", s)
	}
}

// TestEvidenceCachesStayWithinTheirBudgetLine fills the repeat guard and the result
// cache to their default limits and measures the live heap they hold against WS-42's
// 1.5 MiB line in the go_daemon evidence_caches item (scripts/bench/budget_ledger.json).
func TestEvidenceCachesStayWithinTheirBudgetLine(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector's shadow memory distorts heap measurements")
	}
	const line = 1.5 * (1 << 20)
	handles := make([]string, 64)
	for i := range handles {
		handles[i] = testHandle(t)
	}
	before := liveHeap()

	l := NewLedger(LedgerLimits{})
	lim := DefaultLedgerLimits()
	now := time.Now()
	perSession := lim.MaxEntries / lim.Sessions
	for s := 0; s < lim.Sessions; s++ {
		for i := 0; i < perSession; i++ {
			o := Delivered{Scope: Scope{Workspace: "workspace-0123456789", Principal: "agent-principal-0001", Session: fmt.Sprintf("mcp-%032x", s)},
				Tool: "search", Args: []byte(fmt.Sprintf(`{"q":"symbol-%d","limit":25}`, i)), Rev: completeRev(),
				CallID: fmt.Sprintf("toolu_01%022d", s*1000+i), Bytes: 8192, Handle: handles[i%64], ExpiresAt: now.Add(time.Hour)}
			o.Digest[0], o.Digest[1] = byte(i), byte(s)
			l.Observe(o)
		}
	}
	afterLedger := liveHeap()
	sessions, entries := l.Len()
	ledgerBytes := afterLedger - before
	t.Logf("ledger: %d sessions, %d fingerprints, %.0f KiB live (%.0f B per fingerprint)",
		sessions, entries, float64(ledgerBytes)/1024, float64(ledgerBytes)/float64(entries))
	if entries != lim.MaxEntries {
		t.Fatalf("ledger not filled: %d of %d", entries, lim.MaxEntries)
	}

	// the cache at its byte cap, for small bodies (per-entry overhead dominates), a
	// typical projection, and bodies just past a page boundary (allocation rounding)
	var worst int64
	for _, size := range []int{600, 12 << 10, 33<<10 + 1, 100 << 10} {
		start := liveHeap()
		c := NewResultCache(CacheLimits{})
		body := bytes.Repeat([]byte("r"), size)
		for i := 0; c.Stats().Evictions == 0 || i < 200; i++ {
			k := mustKey(t, "search", fmt.Sprintf(`{"q":"q%d"}`, i), completeRev())
			c.Put(alice, k, CachedResult{Body: body, ContentType: "application/json", Handle: testHandle(t),
				ExpiresAt: now.Add(time.Hour), RawSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
		}
		held := liveHeap() - start
		stats := c.Stats()
		t.Logf("result cache, %d-byte bodies: %d entries, %d accounted bytes (cap %d), %.0f KiB live",
			size, stats.Entries, stats.Bytes, DefaultCacheLimits().MaxBytes, float64(held)/1024)
		worst = max(worst, held)
		runtime.KeepAlive(c)
	}
	total := ledgerBytes + worst
	t.Logf("total (ledger + worst cache): %.2f MiB against the %.1f MiB line", float64(total)/(1<<20), line/(1<<20))
	if float64(total) > line {
		t.Fatalf("evidence caches hold %.2f MiB, over the %.1f MiB line", float64(total)/(1<<20), line/(1<<20))
	}
	runtime.KeepAlive(l)
}

// liveHeap is the live heap after a full collection.
func liveHeap() int64 {
	var m runtime.MemStats
	for i := 0; i < 2; i++ {
		runtime.GC()
	}
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/workspaceops"
)

// WS-06B: the full /api/health view shows host-wide activity (data-movement counters,
// the tree and the stdio shims on the host, the heavy-slot owner and queue, the Rust
// worker's pid and memory, and the live pool and child counters). While authentication
// is enforced only an operator sees it: an admin or another non-reader token with no
// workspace scope. A reader-only token, and a workspace-scoped token of any role, get
// the static limits only, and their polls never sample the process tree.
func TestHealthFullViewIsForOperatorsOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	t.Setenv("XMUSTARD_AUTH_TOKENS", "")
	// samples counts every walk of the tree; offWatchdog counts every walk the
	// heavy-slot watchdog did not take, so a handler's sample (on the request
	// goroutine or on one it spawned) is never confused with a watchdog tick.
	// Every sample is stamped an hour old so the health cache never answers: a
	// handler that reads the governor always reaches the sampler, and so always
	// shows up in offWatchdog, even while the watchdog keeps a fresh sample around.
	var samples, offWatchdog atomic.Int64
	prev := budget.Gov
	budget.Gov = budget.NewProcessGovernor(budget.GovernorConfig{SoftCeilingBytes: 1 << 40, Sampler: func() (budget.TreeSample, error) {
		samples.Add(1)
		if !onWatchdogGoroutine() {
			offWatchdog.Add(1)
		}
		return budget.TreeSample{At: time.Now().Add(-time.Hour), Supported: true, Processes: 1, RSSBytes: 1 << 20, SelfRSSBytes: 1 << 20}, nil
	}})
	t.Cleanup(func() { budget.Gov = prev })
	srv := httptest.NewServer(bodyLimitMiddleware(authMiddleware(dir, "auto", newAPIHandler())))
	defer srv.Close()
	health := func(token string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/api/health", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := testClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var h map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&h); err != nil || resp.StatusCode != http.StatusOK || h["status"] != "ok" {
			t.Fatalf("health must stay public: %d %v %v", resp.StatusCode, h, err)
		}
		return h
	}
	mustMint := func(id, role string, workspaces ...string) string {
		t.Helper()
		raw, err := workspaceops.MintScopedToken(dir, id, role, 0, workspaces)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	isFull := func(t *testing.T, h map[string]any) bool {
		t.Helper()
		b := dig(t, h, "budget").(map[string]any)
		pool := dig(t, h, "transient_pool").(map[string]any)
		kids := dig(t, h, "children").(map[string]any)
		num(t, pool, "max")
		num(t, kids, "cap")
		if _, ok := b["counters"]; !ok {
			// the public view: the static limits and nothing about activity
			if len(b) != 4 || b["detail"] == "" || len(pool) != 1 || len(kids) != 1 {
				t.Fatalf("restricted view leaks activity: budget %v pool %v children %v", b, pool, kids)
			}
			return false
		}
		if dig(t, b, "heavy_slot", "owner") != "rust:changetrack/index" || dig(t, b, "core_worker", "policy") == "" {
			t.Fatalf("full view: %v", b)
		}
		num(t, pool, "in_use")
		num(t, kids, "in_use")
		found := false
		for _, c := range dig(t, b, "reservations", "components").([]any) {
			found = found || dig(t, c, "name") == "rust_worker"
		}
		if !found {
			t.Fatalf("the full view lists the rust_worker component: %v", dig(t, b, "reservations"))
		}
		dig(t, b, "reclaim", "level")
		return true
	}

	// The full view must name the heavy slot's owner, so a slot is held whenever
	// an operator polls. That slot starts the governor's watchdog, a goroutine
	// that samples the tree every 250 ms for as long as it is held, which would
	// move a global counter inside a restricted poll's window and read as a leak.
	// Restricted tokens therefore poll with no slot held: without one there is no
	// watchdog, so the counter moves only if the poll itself sampled, and the
	// assertion needs no attribution and no window.
	// The release is registered with t.Cleanup as well as returned, because a
	// t.Fatal raised inside health or isFull between the acquire and the release
	// would otherwise leave the slot held and every later test in the package
	// failing with "transient budget exhausted", which hides the real cause.
	// AcquireHeavy's release is idempotent (sync.Once), so the explicit call
	// still releases it at the point the test means to.
	holdSlot := func(t *testing.T) func() {
		t.Helper()
		release, err := budget.AcquireHeavy(t.Context(), "rust:changetrack/index", 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
		return release
	}
	t.Setenv("XMUSTARD_AUTH", "auto")
	open := holdSlot(t)
	full := isFull(t, health(""))
	open()
	if !full {
		t.Fatal("auto mode without credentials (the open loopback default) shows the full view")
	}
	tokens := map[string]string{
		"admin":         mustMint("op-admin", "admin"),
		"agent":         mustMint("op-agent", "agent"),
		"indexer":       mustMint("op-indexer", "indexer"),
		"reader":        mustMint("ro-reader", "reader"),
		"readonly":      mustMint("ro-legacy", "readonly"),
		"scoped agent":  mustMint("ws-agent", "agent", "wsA"),
		"scoped admin":  mustMint("ws-admin", "admin", "wsA"),
		"scoped reader": mustMint("ws-reader", "reader", "wsA", "wsB"),
	}
	want := map[string]bool{"admin": true, "agent": true, "indexer": true}
	for _, mode := range []string{"auto", "required"} {
		t.Setenv("XMUSTARD_AUTH", mode)
		for who, tok := range tokens {
			time.Sleep(300 * time.Millisecond) // past the health sample cache
			release := func() {}
			if want[who] {
				release = holdSlot(t)
			}
			before := offWatchdog.Load()
			h := health(tok)
			got := isFull(t, h)
			release()
			if got != want[who] {
				t.Fatalf("%s mode, %s token: full view %v, want %v (%v)", mode, who, got, want[who], h["budget"])
			}
			if !want[who] {
				detail, _ := dig(t, h, "budget", "detail").(string)
				reason := map[bool]string{true: "workspace-scoped", false: "reader-only"}[strings.HasPrefix(who, "scoped")]
				if !strings.Contains(detail, reason) {
					t.Fatalf("%s token: the detail must say why (%q): %q", who, reason, detail)
				}
				if offWatchdog.Load() != before {
					t.Fatalf("%s mode, %s token: a restricted poll sampled the process tree", mode, who)
				}
			}
		}
		for _, tok := range []string{"", "not-a-token"} {
			if isFull(t, health(tok)) {
				t.Fatalf("%s mode without a valid token must get the public view", mode)
			}
		}
		// The counter above can only judge a restricted poll while no heavy work
		// runs, because the watchdog samples for its own reasons. The product
		// claim is the same either way, so check it here too: a restricted poll
		// taken *during* heavy work must still get the public view, with no
		// activity in the budget block.
		busy := holdSlot(t)
		// Wait for one watchdog tick first, so the polls below run beside a live
		// watchdog: that is the state a handler gated on HeavyBusy would sample in.
		ticked := samples.Load()
		if !waitFor(t, watchdogDeadline, func() bool { return samples.Load() > ticked }) {
			busy()
			t.Fatal("the heavy-slot watchdog never sampled, so the busy-slot polls prove nothing")
		}
		for who, tok := range tokens {
			if want[who] {
				continue
			}
			before := offWatchdog.Load()
			under := health(tok)
			if isFull(t, under) {
				busy()
				t.Fatalf("%s mode, %s token: a restricted poll under heavy work got the full view", mode, who)
			}
			if b, _ := dig(t, under, "budget").(map[string]any); len(b) != 4 {
				busy()
				t.Fatalf("%s mode, %s token: a restricted poll under heavy work leaked activity: %v", mode, who, b)
			}
			// offWatchdog never moves for a watchdog tick, so no headroom is
			// needed: a restricted poll adds zero.
			if n := offWatchdog.Load() - before; n != 0 {
				busy()
				t.Fatalf("%s mode, %s token: a restricted poll under heavy work sampled the process tree %d times", mode, who, n)
			}
		}
		busy()
	}
	t.Setenv("XMUSTARD_AUTH", "off")
	off := holdSlot(t)
	fullOff := isFull(t, health(""))
	off()
	if !fullOff {
		t.Fatal("auth off shows the full view")
	}
	// The restricted polls are only sound because no watchdog was running beside
	// them: the counter would otherwise move for the watchdog's reasons. That is
	// structural rather than incidental, the slot being released before every
	// restricted poll, so assert the structure instead of hoping for it. The
	// sampler must be live while a slot is held and silent once it is released.
	// Both halves wait for the state they need rather than assuming a sleep is
	// long enough, because a test goroutine can wake before the watchdog
	// goroutine has been scheduled at all, and a sleep that is "obviously long
	// enough" is how this row's own flake was born.
	held := holdSlot(t)
	// The admission sample taken by AcquireHeavy is not a watchdog tick, so the
	// baseline is read after the slot is held and only later samples count.
	afterAcquire := samples.Load()
	if !waitFor(t, watchdogDeadline, func() bool { return samples.Load() > afterAcquire }) {
		held()
		t.Fatal("the heavy-slot watchdog never sampled, so the counter this test relies on proves nothing")
	}
	held()
	if !waitQuiet(t, watchdogDeadline, samples.Load) {
		t.Fatal("the watchdog kept sampling after the slot was released")
	}
}

// watchdogDeadline bounds the two watchdog waits below. It is a deadline on
// waiting for a state, not a bound on production behaviour and not a timeout
// the code under test has to meet, so it is not the kind of test bound §3.3
// holds DO NOT DECIDE; a longer wait cannot make a broken watchdog pass, and a
// shorter one only makes a loaded runner fail sooner.
const watchdogDeadline = 5 * time.Second

// waitFor polls cond until it holds and reports whether it did within the
// deadline.
func waitFor(t *testing.T, within time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

// waitQuiet reports whether read settles and then holds still for a whole watch
// interval, which is the evidence that nothing is sampling any more. A sample
// already in flight when the slot was released lands shortly after it, so a
// single unchanged read is not yet quiet: the value is allowed to move once and
// the interval is then re-measured from the new value, within the deadline. A
// counter that never settles fails, which is the case the caller reports.
func waitQuiet(t *testing.T, within time.Duration, read func() int64) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	settled := read()
	for {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(budget.DefaultWatchInterval)
		now := read()
		if now != settled {
			settled = now
			continue
		}
		return true
	}
}

// WS-06B: POST /index runs the whole-repository `changetrack index` inside the heavy
// slot. While it runs the slot shows its owner label; behind a busy slot the route
// waits the bound and answers 503 + Retry-After, without starting the core.
func TestIndexRebaselineRunsInTheHeavySlot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	t.Setenv("XMUSTARD_CORE_WORKER", "")
	seedCoreWorkspace(t, dir, "wsIdx")
	ran := dir + "/ran.log"
	t.Setenv("XMUSTARD_CORE_BIN", writeScript(t, `echo "$1 $2" >> `+ran+`
case "$1 $2" in
"changetrack index") sleep 1; mkdir -p "$3/workspaces/$5" && echo staged > "$3/workspaces/$5/index_baseline.staged.json"; printf '{"indexed":true}' ;;
*) echo '{}' ;;
esac
`))
	prev := budget.Gov
	budget.Gov = budget.NewProcessGovernor(budget.GovernorConfig{SoftCeilingBytes: 1 << 40, HeavyWait: 300 * time.Millisecond})
	t.Cleanup(func() { budget.Gov = prev })
	srv := httptest.NewServer(bodyLimitMiddleware(authMiddleware(dir, "auto", newAPIHandler())))
	defer srv.Close()
	post := func() (int, http.Header) {
		resp, err := testClient.Post(srv.URL+"/api/workspaces/wsIdx/index", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header
	}

	done := make(chan int, 1)
	go func() { code, _ := post(); done <- code }()
	deadline := time.Now().Add(5 * time.Second)
	for budget.Status().HeavySlot.Owner != "rust:changetrack/index" {
		if time.Now().After(deadline) {
			t.Fatalf("the rebaseline never held the heavy slot: %+v", budget.Status().HeavySlot)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h := budget.Status().HeavySlot; h.DeclaredBytes <= 0 {
		t.Fatalf("the rebaseline must declare its bytes: %+v", h)
	}
	if code := <-done; code != http.StatusOK {
		t.Fatalf("rebaseline: %d", code)
	}

	release, err := budget.AcquireHeavy(t.Context(), "test_holder", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	code, hdr := post()
	if code != http.StatusServiceUnavailable || hdr.Get("Retry-After") == "" {
		t.Fatalf("rebaseline behind a busy heavy slot: want 503 + Retry-After, got %d %v", code, hdr)
	}
	if d := time.Since(start); d < 300*time.Millisecond {
		t.Fatalf("refused after %s, before the wait bound", d)
	}
	if n := budget.Status().HeavySlot.RefusedBusy; n != 1 {
		t.Fatalf("refused_busy: %d", n)
	}
	raw, _ := os.ReadFile(ran)
	if strings.Count(string(raw), "changetrack index") != 1 {
		t.Fatalf("a refused rebaseline must not start the core: %q", raw)
	}
}

// onWatchdogGoroutine reports whether the caller runs on the heavy-slot
// watchdog's goroutine, the only sampler a restricted poll may run beside.
func onWatchdogGoroutine() bool {
	pcs := make([]uintptr, 128)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	for {
		f, more := frames.Next()
		if strings.Contains(f.Function, "budget.(*Governor).watchWhileHeld") {
			return true
		}
		if !more {
			return false
		}
	}
}

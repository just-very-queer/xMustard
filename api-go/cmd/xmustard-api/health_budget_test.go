package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/shims"
	"xmustard/api-go/internal/workspaceops"
)

func getHealth(t *testing.T, base string) map[string]any {
	t.Helper()
	resp, err := testClient.Get(base + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var h map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("health: status %d err %v", resp.StatusCode, err)
	}
	return h
}

func TestHealthReportsAuthStateWithoutDisclosingToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	t.Setenv("XMUSTARD_AUTH", "auto")
	token, err := workspaceops.MintToken(dir, "health-agent", "agent")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	body := healthResponse(httptest.NewRequest(http.MethodGet, "/api/health", nil))
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) || strings.Contains(logs.String(), token) {
		t.Fatal("health response or logs exposed the bearer token")
	}
	state, ok := body["auth"].(map[string]any)
	if !ok || state["mode"] != "auto" || state["credentials_configured"] != true || state["required"] != true {
		t.Fatalf("unexpected auth state: %v", body["auth"])
	}
	t.Setenv("XMUSTARD_AUTH", "off")
	if got := healthAuthState(); got["required"] != false || got["credentials_configured"] != true {
		t.Fatalf("off mode auth state should report configured credentials but not require auth: %v", got)
	}
}

// dig walks nested JSON objects by key.
func dig(t *testing.T, v any, keys ...string) any {
	t.Helper()
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("at %q: not an object: %v", k, v)
		}
		if v, ok = m[k]; !ok {
			t.Fatalf("missing key %q in %v", k, m)
		}
	}
	return v
}

func num(t *testing.T, v any, keys ...string) float64 {
	t.Helper()
	n, ok := dig(t, v, keys...).(float64)
	if !ok {
		t.Fatalf("%v is not a number", keys)
	}
	return n
}

// testGovernor swaps in a process governor for one test. The watchdog would measure
// the test binary (which holds other tests' fixtures and children), so the soft ceiling
// is raised out of the way; the watchdog's refusal is covered in the budget package.
func testGovernor(t *testing.T) {
	t.Helper()
	prev := budget.Gov
	budget.Gov = budget.NewProcessGovernor(budget.GovernorConfig{SoftCeilingBytes: 1 << 40})
	t.Cleanup(func() { budget.Gov = prev })
}

// holdHeavy takes the process-wide heavy slot for a test and releases it on cleanup.
func holdHeavy(t *testing.T, owner string, declared int64) {
	t.Helper()
	testGovernor(t)
	release, err := budget.AcquireHeavy(context.Background(), owner, declared)
	if err != nil {
		t.Fatalf("heavy slot: %v", err)
	}
	t.Cleanup(release)
}

func TestHealthReportsShimEnumerationError(t *testing.T) {
	previous := listHealthShims
	listHealthShims = func() ([]shims.Process, error) { return nil, errors.New("injected enumeration failure") }
	t.Cleanup(func() { listHealthShims = previous })
	got := healthBudgetBlock().Shims
	if got.Available || got.Error != "injected enumeration failure" {
		t.Fatalf("shim status = %+v", got)
	}
}

// PAR-OPS-01: /api/health carries the budget block: reservations per component, the
// heavy slot's owner and queue, the watchdog, runtime GC/heap stats and the counters.
// The existing transient_pool and children fields stay for current readers.
func TestHealthReportsBudgetBlock(t *testing.T) {
	f := newEvidenceFixture(t, true)
	holdHeavy(t, "test_index_build", 5<<20)
	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		if r, err := budget.AcquireHeavy(ctx, "test_bulk_import", 1<<20); err == nil {
			r()
		}
	}()
	defer func() { cancel(); <-waiterDone }()
	deadline := time.Now().Add(5 * time.Second)
	for budget.Status().HeavySlot.QueueLen != 1 {
		if time.Now().After(deadline) {
			t.Fatal("waiter never queued")
		}
		time.Sleep(time.Millisecond)
	}

	h := getHealth(t, f.srv.URL)
	if dig(t, h, "status") != "ok" {
		t.Fatalf("status: %v", h["status"])
	}
	if id, ok := h["data_dir_id"].(string); !ok || len(id) != 64 {
		t.Fatalf("health must identify its data directory with a SHA-256 fingerprint: %v", h["data_dir_id"])
	} else if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("health data directory identity is not hex: %v", err)
	}
	num(t, h, "transient_pool", "max")
	num(t, h, "children", "cap")
	b := dig(t, h, "budget")
	if _, ok := dig(t, b, "shims").(map[string]any); !ok {
		t.Fatalf("budget shims must be an object: %v", b)
	}
	for _, key := range []string{"live", "orphaned", "runaway", "pids"} {
		dig(t, b, "shims", key)
	}
	if num(t, b, "gate_bytes") != 1e8 || num(t, b, "soft_ceiling_bytes") <= 0 {
		t.Fatalf("gate/soft ceiling: %v", b)
	}
	comps := dig(t, b, "reservations", "components").([]any)
	names := map[string]map[string]any{}
	for _, c := range comps {
		m := c.(map[string]any)
		names[m["name"].(string)] = m
	}
	for _, n := range []string{"go_daemon", "transient_pool", "heavy_slot", "helper_children"} {
		if names[n] == nil {
			t.Fatalf("component %q missing: %v", n, comps)
		}
	}
	if num(t, names["heavy_slot"], "used_bytes") != 5<<20 || num(t, names["go_daemon"], "reserved_peak_bytes") != 28<<20 {
		t.Fatalf("component usage: heavy %v daemon %v", names["heavy_slot"], names["go_daemon"])
	}
	num(t, b, "reservations", "steady_total_bytes")

	if dig(t, b, "heavy_slot", "owner") != "test_index_build" || dig(t, b, "heavy_slot", "busy") != true ||
		num(t, b, "heavy_slot", "queue_len") != 1 || num(t, b, "heavy_slot", "wait_bound_ms") <= 0 {
		t.Fatalf("heavy slot: %v", dig(t, b, "heavy_slot"))
	}
	queue := dig(t, b, "heavy_slot", "queue").([]any)
	if len(queue) != 1 || dig(t, queue[0], "owner") != "test_bulk_import" {
		t.Fatalf("queue: %v", queue)
	}
	if dig(t, b, "watchdog", "sampling_active") != true || dig(t, b, "watchdog", "scope") == "" {
		t.Fatalf("watchdog while heavy work runs: %v", dig(t, b, "watchdog"))
	}
	dig(t, b, "watchdog", "last", "rss_bytes")
	for _, k := range []string{"gomemlimit_bytes", "gogc_percent", "heap_objects_bytes", "heap_goal_bytes", "go_mapped_bytes", "gc_cycles", "total_alloc_bytes"} {
		num(t, b, "runtime", k)
	}
	dig(t, b, "runtime", "gomemlimit_source")
	for _, k := range []string{"core", "git", "helper"} {
		num(t, b, "counters", "spawns", k)
	}
	for _, k := range []string{"spawns_total", "bytes_hashed", "captures", "capture_bytes"} {
		num(t, b, "counters", k)
	}
	for _, k := range []string{"calls", "counted", "files_listed", "files_statted", "files_hashed", "bytes_hashed", "git_spawns", "rows_loaded"} {
		num(t, b, "counters", "core_passes", k)
	}
	num(t, b, "transient_pool", "max")
	num(t, b, "children", "cap")
	// the two body caps are asserted against the enforced limits in
	// TestHealthBodyCapsMatchTheLimitsTheServerEnforces; this block's former
	// "must not exceed the pool" check pinned the pool-capped capture value and is gone.
	num(t, b, "in_flight_bodies", "cap")
}

// The health block reports two body caps and both are read from a real HTTP response:
// request_body_cap_bytes for an ordinary (pool-reserved) body, and
// capture_body_cap_bytes for a capture. capture_body_cap_bytes is new; before it, one
// number stood for both and matched neither.
//
// Each case builds a fresh fixture after the overrides are set, so the server the
// response comes from is the server those overrides built: the middleware and the
// evidence store each fix their limits at construction, and health reports those values
// rather than re-reading the environment.
func TestHealthBodyCapsMatchTheLimitsTheServerEnforces(t *testing.T) {
	defaultPoolCapped := budget.CapToPool(maxRequestBodyBytes)
	cases := []struct {
		name     string
		bodyCap  string
		storeCap string
		wantOrd  int64
		// storeBinds names which of the two limits binds the capture cap; the value
		// itself comes from the limits the fixture was built with, never from a literal
		storeBinds bool
	}{
		{name: "defaults", storeBinds: true, wantOrd: defaultPoolCapped},
		{name: "store override", storeCap: "5000000", storeBinds: true, wantOrd: defaultPoolCapped},
		{name: "body override below the store limit", bodyCap: "5000000", wantOrd: 5000000},
		// the store still binds, and this case separates the capture cap from the
		// pool-capped ordinary cap, which is what keeps the original defect reachable
		{name: "body override above the store limit", bodyCap: "67108864", storeBinds: true, wantOrd: defaultPoolCapped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XMUSTARD_MAX_BODY_BYTES", c.bodyCap)
			t.Setenv("XMUSTARD_EVIDENCE_MAX_ORIGINAL_BYTES", c.storeCap)
			f := newEvidenceFixture(t, true) // built with these overrides in place
			b := dig(t, getHealth(t, f.srv.URL), "budget")

			streamLimit := maxRequestBodyBytesConfigured()
			storeLimit := evidence.LimitsFromEnv().MaxOriginal
			wantCap := streamLimit
			if c.storeBinds {
				wantCap = storeLimit
			}

			if got := num(t, b, "request_body_cap_bytes"); int64(got) != c.wantOrd {
				t.Fatalf("request_body_cap_bytes: want %d, got %d", c.wantOrd, int64(got))
			}
			if got := num(t, b, "capture_body_cap_bytes"); int64(got) != wantCap {
				t.Fatalf("capture_body_cap_bytes: want %d, got %d", wantCap, int64(got))
			}
			// the advertised cap must be one the running server enforces: the value
			// captured when this server was built, not one recomputed from the
			// environment now, which is what round 4 found the helpers doing
			if captureBodyCap != wantCap {
				t.Fatalf("cap captured at construction %d is not the advertised %d", captureBodyCap, wantCap)
			}
		})
	}
}

// Correction to PAR-RT-04: no capture path waits on the heavy slot. With the slot held
// by heavy work, a Pi-style POST capture and a delivered tool call (spooled and
// captured) both complete at once, nothing queues for the slot, and the capture and
// core-spawn counters move.
func TestCapturePathsNeverWaitOnHeavySlot(t *testing.T) {
	f := newEvidenceFixture(t, true)
	holdHeavy(t, "test_index_writer", 20<<20)
	before := budget.Status()

	start := time.Now()
	code, body, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence?tool=search", "", bytes.NewReader(f.big), nil)
	if code != http.StatusOK {
		t.Fatalf("capture with the heavy slot held: %d %s", code, body)
	}
	code, body, _ = f.do(t, "GET", "/api/workspaces/"+f.ws+"/search?q=x", "", nil, deliver)
	if code != http.StatusOK {
		t.Fatalf("delivered call with the heavy slot held: %d %s", code, body)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("captures took %s with the heavy slot held (wait bound %s)", d, budget.DefaultHeavyWait)
	}
	after := budget.Status()
	if after.HeavySlot.QueueLen != 0 || after.HeavySlot.RefusedBusy != before.HeavySlot.RefusedBusy ||
		after.HeavySlot.RefusedHotPath != before.HeavySlot.RefusedHotPath || after.HeavySlot.Acquired != before.HeavySlot.Acquired {
		t.Fatalf("a capture path touched the heavy slot: before %+v after %+v", before.HeavySlot, after.HeavySlot)
	}
	if got := after.Counters.Captures - before.Counters.Captures; got != 2 {
		t.Fatalf("captures counted %d, want 2", got)
	}
	if got := after.Counters.CaptureBytes - before.Counters.CaptureBytes; got < 2*int64(len(f.big)) {
		t.Fatalf("capture bytes counted %d, want at least %d", got, 2*len(f.big))
	}
	if after.Counters.BytesHashed-before.Counters.BytesHashed < 2*int64(len(f.big)) {
		t.Fatalf("hashed bytes did not cover both originals")
	}
	if after.Counters.Spawns["core"] <= before.Counters.Spawns["core"] {
		t.Fatalf("the delivered search spawned a core child that was not counted: %v -> %v", before.Counters.Spawns, after.Counters.Spawns)
	}
}

// PAR-RT-05: the API process sets the Go soft memory limit to the daemon's line, with
// the GOGC floor, only when GOMEMLIMIT is unset, and health reports which applied.
func TestAPIProcessMemoryLimitOnlyWhenGOMEMLIMITUnset(t *testing.T) {
	for _, tc := range []struct {
		env    string
		source string
		limit  float64
		floor  float64
	}{
		{"", "xmustard_default", float64(budget.DefaultMemoryLimitBytes), float64(budget.DefaultGOGCFloorPercent)},
		{"64MiB", "env", 64 << 20, 0},
	} {
		p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": t.TempDir(), "GOMEMLIMIT": tc.env})
		b := dig(t, getHealth(t, p.base), "budget")
		// an idle API's live heap is far below the line, so the floor has not lifted it
		if dig(t, b, "runtime", "gomemlimit_source") != tc.source || num(t, b, "runtime", "gomemlimit_bytes") != tc.limit ||
			num(t, b, "runtime", "gogc_floor_percent") != tc.floor {
			t.Fatalf("GOMEMLIMIT=%q: runtime %v", tc.env, dig(t, b, "runtime"))
		}
		if pool := num(t, b, "transient_pool", "max"); pool != float64(budget.DefaultTransientBudgetBytes) {
			t.Fatalf("an unconfigured API must use the 24 MiB pool, got %v", pool)
		}
		_ = p.cmd.Process.Kill()
		p.waitExit(t, 10*time.Second)
	}
}

// /api/health stays public for liveness probes, but while authentication is enforced
// its budget block (captures, hashed bytes, spawns, live external processes, heavy-slot
// owner and queue) is shown only with a valid bearer token. Without one the block has
// just the gate and the soft ceiling, and polling it never samples the process tree.
// With no credentials in auto mode (the loopback default) the block stays public.
func TestHealthBudgetBlockNeedsAuthWhenEnforced(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	var samples atomic.Int64
	prev := budget.Gov
	budget.Gov = budget.NewProcessGovernor(budget.GovernorConfig{SoftCeilingBytes: 90 << 20, Sampler: func() (budget.TreeSample, error) {
		samples.Add(1)
		return budget.TreeSample{At: time.Now(), Supported: true, Processes: 1, RSSBytes: 1 << 20, SelfRSSBytes: 1 << 20}, nil
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
		num(t, h, "transient_pool", "max") // the pre-existing public fields stay
		return dig(t, h, "budget").(map[string]any)
	}
	full := func(b map[string]any) bool { _, ok := b["counters"]; return ok }

	t.Setenv("XMUSTARD_AUTH", "auto")
	if b := health(""); !full(b) {
		t.Fatalf("auto mode without credentials (open loopback default) shows the block: %v", b)
	}
	token, err := workspaceops.MintToken(dir, "agent-a", "agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"auto", "required"} {
		t.Setenv("XMUSTARD_AUTH", mode)
		time.Sleep(300 * time.Millisecond) // past the health sample cache
		before := samples.Load()
		for _, tok := range []string{"", "not-a-token"} {
			b := health(tok)
			if full(b) || len(b) != 4 || num(t, b, "gate_bytes") != float64(budget.GateBytes) || num(t, b, "soft_ceiling_bytes") != 90<<20 || b["detail"] == "" {
				t.Fatalf("%s mode, token %q: only the gate and soft ceiling may be public: %v", mode, tok, b)
			}
		}
		if samples.Load() != before {
			t.Fatalf("%s mode: unauthenticated health polls sampled the process tree", mode)
		}
		if b := health(token); !full(b) || dig(t, b, "heavy_slot", "capacity") != float64(1) {
			t.Fatalf("%s mode with a valid token: want the full block, got %v", mode, b)
		}
	}
	t.Setenv("XMUSTARD_AUTH", "off")
	if b := health(""); !full(b) {
		t.Fatalf("auth off shows the block: %v", b)
	}
}

// Correction to PAR-RT-04 ("no hook or capture path waits on the heavy slot"), checked
// with a caller that would wait: an identity observer that takes the heavy slot. Inside
// a delivered call's capture (before- and after-identity) it is refused at once, never
// queued, while heavy work holds the slot, and the call still completes.
func TestCaptureIdentitySamplingNeverWaitsOnHeavySlot(t *testing.T) {
	f := newEvidenceFixture(t, true)
	holdHeavy(t, "test_index_writer", 0)
	var mu sync.Mutex
	var errs []error
	var longest time.Duration
	prev := captureIdentity
	captureIdentity = func(ctx context.Context, rc *workspaceops.RequestContext, after bool) evidence.Identity {
		t0 := time.Now()
		release, err := budget.AcquireHeavy(ctx, "identity_refresh", 0)
		if err == nil {
			release()
		}
		mu.Lock()
		errs, longest = append(errs, err), max(longest, time.Since(t0))
		mu.Unlock()
		return prev(ctx, rc, after)
	}
	t.Cleanup(func() { captureIdentity = prev })
	before := budget.Status().HeavySlot

	start := time.Now()
	code, body, _ := f.do(t, "GET", "/api/workspaces/"+f.ws+"/search?q=x", "", nil, deliver)
	if code != http.StatusOK {
		t.Fatalf("delivered call with the heavy slot held: %d %s", code, body)
	}
	after := budget.Status().HeavySlot
	mu.Lock()
	defer mu.Unlock()
	if len(errs) < 2 {
		t.Fatalf("both identity samples of a reduced capture must run: %d", len(errs))
	}
	for _, err := range errs {
		if !errors.Is(err, budget.ErrOverloaded) {
			t.Fatalf("heavy admission from a capture path must be refused while the slot is held: %v", err)
		}
	}
	if longest > time.Second || time.Since(start) > 5*time.Second {
		t.Fatalf("a capture path waited for the heavy slot: longest %s, call %s (wait bound %s)", longest, time.Since(start), budget.DefaultHeavyWait)
	}
	if got := after.RefusedHotPath - before.RefusedHotPath; got != int64(len(errs)) || after.QueueLen != 0 || after.RefusedBusy != before.RefusedBusy {
		t.Fatalf("refusals must be hot-path refusals, never queued: %d of %d, before %+v after %+v", got, len(errs), before, after)
	}
}

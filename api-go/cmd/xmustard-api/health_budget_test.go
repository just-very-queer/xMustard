package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
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
	num(t, h, "transient_pool", "max")
	num(t, h, "children", "cap")
	b := dig(t, h, "budget")
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
	num(t, b, "transient_pool", "max")
	num(t, b, "children", "cap")
	if num(t, b, "request_body_cap_bytes") > num(t, b, "transient_pool", "max") {
		t.Fatalf("request body cap must not exceed the pool: %v", b)
	}
	num(t, b, "in_flight_bodies", "cap")
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

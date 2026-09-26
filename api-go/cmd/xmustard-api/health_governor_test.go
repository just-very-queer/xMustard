package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
	var samples atomic.Int64
	prev := budget.Gov
	budget.Gov = budget.NewProcessGovernor(budget.GovernorConfig{SoftCeilingBytes: 1 << 40, Sampler: func() (budget.TreeSample, error) {
		samples.Add(1)
		return budget.TreeSample{At: time.Now(), Supported: true, Processes: 1, RSSBytes: 1 << 20, SelfRSSBytes: 1 << 20}, nil
	}})
	t.Cleanup(func() { budget.Gov = prev })
	release, err := budget.AcquireHeavy(t.Context(), "rust:changetrack/index", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
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

	t.Setenv("XMUSTARD_AUTH", "auto")
	if !isFull(t, health("")) {
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
			before := samples.Load()
			h := health(tok)
			if got := isFull(t, h); got != want[who] {
				t.Fatalf("%s mode, %s token: full view %v, want %v (%v)", mode, who, got, want[who], h["budget"])
			}
			if !want[who] {
				detail, _ := dig(t, h, "budget", "detail").(string)
				reason := map[bool]string{true: "workspace-scoped", false: "reader-only"}[strings.HasPrefix(who, "scoped")]
				if !strings.Contains(detail, reason) {
					t.Fatalf("%s token: the detail must say why (%q): %q", who, reason, detail)
				}
				if samples.Load() != before {
					t.Fatalf("%s mode, %s token: a restricted poll sampled the process tree", mode, who)
				}
			}
		}
		for _, tok := range []string{"", "not-a-token"} {
			if isFull(t, health(tok)) {
				t.Fatalf("%s mode without a valid token must get the public view", mode)
			}
		}
	}
	t.Setenv("XMUSTARD_AUTH", "off")
	if !isFull(t, health("")) {
		t.Fatal("auth off shows the full view")
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
"changetrack index") sleep 1; printf '{"indexed":true}' ;;
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

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"xmustard/api-go/internal/budget"
)

// seedCoreWorkspace writes a workspace snapshot whose root is a temp dir.
func seedCoreWorkspace(t *testing.T, dir, ws string) {
	t.Helper()
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": t.TempDir()}})
	p := filepath.Join(dir, "workspaces", ws, "snapshot.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, snap, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "xmustard-core")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Audit Go #5 (child count): concurrent tool calls must not start more Rust children
// than XMUSTARD_MAX_CORE_CHILDREN; excess work waits (bounded) or is refused with 503.
func TestCoreChildConcurrencyIsBounded(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	live := t.TempDir()
	counts := filepath.Join(t.TempDir(), "counts")
	core := writeScript(t, `mkdir "`+live+`/$$"
ls "`+live+`" | wc -l >> "`+counts+`"
sleep 0.4
rmdir "`+live+`/$$"
echo '{"hits":[]}'
`)
	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": core, "XMUSTARD_MAX_CORE_CHILDREN": "2"})
	var wg sync.WaitGroup
	statuses := make([]int, 6)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := testClient.Get(p.base + "/api/workspaces/ws/search?q=x" + strconv.Itoa(i))
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			resp.Body.Close()
			statuses[i] = resp.StatusCode
		}(i)
	}
	wg.Wait()
	raw, _ := os.ReadFile(counts)
	peak := 0
	for _, line := range strings.Fields(string(raw)) {
		if n, _ := strconv.Atoi(line); n > peak {
			peak = n
		}
	}
	if peak == 0 || peak > 2 {
		t.Fatalf("observed %d concurrent core children with XMUSTARD_MAX_CORE_CHILDREN=2 (statuses %v)", peak, statuses)
	}
	for _, s := range statuses {
		if s != http.StatusOK && s != http.StatusServiceUnavailable {
			t.Fatalf("unexpected status %d (want 200 or protocol overload 503): %v", s, statuses)
		}
	}
}

// Audit Go #5 (Go→Rust capture): the bridge's captured child output must be admitted
// against the shared pool while it streams in, never an unbounded or unaccounted
// buffer. Output that could never fit the pool (4 MiB under 1 MiB) is the permanent
// "output too large" answer without Retry-After; the retryable 503 for output that
// fits an idle pool but not a busy one is TestRustCaptureRefusedWhileThePoolIsBusy.
func TestRustCaptureIsAdmittedAgainstTransientPool(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	core := writeScript(t, `printf '{"hits":[],"pad":"'
head -c 4194304 /dev/zero | tr '\0' 'a'
printf '"}'
`)
	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": core, "XMUSTARD_TRANSIENT_BYTE_BUDGET": "1048576"})
	resp, err := testClient.Get(p.base + "/api/workspaces/ws/search?q=x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "" ||
		!strings.Contains(string(body), "output too large") || len(body) > 4096 {
		t.Fatalf("4 MiB capture under a 1 MiB pool: want the permanent output-too-large error, got %d Retry-After=%q body %.200s",
			resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}
}

// Audit Go #5 (request decode): small bodies were never charged against the pool, so
// many concurrent sub-threshold requests bypassed admission. Every body is admitted.
// PAR-RT-04: a body larger than the whole pool can never be admitted, so it is now a
// permanent 413 rather than a retryable 503 (the body cap is lowered to the pool).
func TestSmallRequestBodiesAreAdmitted(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_TRANSIENT_BYTE_BUDGET": "262144"})
	body := `{"content":"` + strings.Repeat("a", 300<<10) + `"}`
	resp, err := testClient.Post(p.base+"/api/workspaces/ws/context", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge || resp.Header.Get("Retry-After") != "" {
		t.Fatalf("300 KiB body under a 256 KiB pool: status %d Retry-After=%q, want 413 without Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	small := `{"content":"fits"}`
	resp, err = testClient.Post(p.base+"/api/workspaces/ws/context", "application/json", strings.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("small body must still be admitted: %d", resp.StatusCode)
	}
}

// Fable evidence F2: an admission refusal inside a delivered tool call was captured
// into a 200 envelope, losing 503 + Retry-After (a retryable overload looked like a
// tool failure). Delivery callers must get the protocol overload unchanged. Output
// that could never fit the pool is not an overload: it is captured as the tool's
// permanent error, without Retry-After.
func TestDeliveredOverloadPassesThrough503(t *testing.T) {
	url, pool := inProcessAPI(t, `printf '{"hits":[],"pad":"'
head -c 4194304 /dev/zero | tr '\0' 'a'
printf '"}'
`)
	delivered := func() (int, string, []byte) {
		req, _ := http.NewRequest("GET", url+"/api/workspaces/ws/search?q=x", nil)
		req.Header.Set("X-Xmustard-Delivery", "xmustard.evidence/v1")
		resp, err := testClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Retry-After"), body
	}
	other := budget.NewScope(pool)
	if err := other.Acquire(21 << 20); err != nil {
		t.Fatal(err)
	}
	code, retry, body := delivered()
	other.Close()
	if code != http.StatusServiceUnavailable || retry == "" || !strings.Contains(string(body), `"overloaded":true`) {
		t.Fatalf("delivered overload: status %d Retry-After=%q body %.200s", code, retry, body)
	}
	budget.TransientBytes = budget.NewByteBudget(1 << 20)
	code, retry, body = delivered()
	if code != http.StatusOK || retry != "" || !strings.Contains(string(body), `"is_error":true`) || !strings.Contains(string(body), "output too large") {
		t.Fatalf("output that can never fit the pool is a permanent tool error: status %d Retry-After=%q body %.200s", code, retry, body)
	}
}

// Fable evidence F7 (API): decoding the captured Rust output and constructing the
// response copy it again; those copies must be admitted too, not only the capture.
// With 10 MiB of the pool free a 4 MiB capture fits, but capture + decode + response
// (3x) does not, so the call is refused with 503 once the capture is in.
func TestResponseConstructionIsAdmitted(t *testing.T) {
	url, pool := inProcessAPI(t, `printf '{"hits":[],"pad":"'
head -c 4194304 /dev/zero | tr '\0' 'a'
printf '"}'
`)
	other := budget.NewScope(pool)
	if err := other.Acquire(14 << 20); err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	resp, err := testClient.Get(url + "/api/workspaces/ws/search?q=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("capture fits but construction does not: want 503, got %d", resp.StatusCode)
	}
	if pool.Peak() <= 18<<20 || pool.InUse() != 14<<20 {
		t.Fatalf("the capture must have been admitted before construction was refused: peak %d, in use %d", pool.Peak(), pool.InUse())
	}
}

// inProcessAPI serves the API route table in this process over a fake core script, with
// a default-sized transient pool the test can hold part of.
func inProcessAPI(t *testing.T, core string) (string, *budget.ByteBudget) {
	t.Helper()
	prev := budget.TransientBytes
	pool := budget.NewByteBudget(budget.DefaultTransientBudgetBytes)
	budget.TransientBytes = pool
	t.Cleanup(func() { budget.TransientBytes = prev })
	dir := t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	seedCoreWorkspace(t, dir, "ws")
	t.Setenv("XMUSTARD_CORE_BIN", writeScript(t, `case "$1" in
search) `+core+` ;;
*) echo '{}' ;;
esac
`))
	srv := httptest.NewServer(bodyLimitMiddleware(authMiddleware(dir, "auto", newAPIHandler())))
	t.Cleanup(srv.Close)
	return srv.URL, pool
}

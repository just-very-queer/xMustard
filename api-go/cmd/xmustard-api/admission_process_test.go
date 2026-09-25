package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
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
// against the shared pool while it streams in. A capture that cannot be reserved is an
// explicit 503 overload, never an unbounded or unaccounted buffer.
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
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("4 MiB capture under a 1 MiB pool: status %d, body %d bytes; want 503 overload", resp.StatusCode, len(body))
	}
	if resp.Header.Get("Retry-After") == "" || !strings.Contains(string(body), "overload") {
		t.Fatalf("overload response must be explicit with Retry-After: %q %q", resp.Header.Get("Retry-After"), body)
	}
}

// Audit Go #5 (request decode): small bodies were never charged against the pool, so
// many concurrent sub-threshold requests bypassed admission. Every body is admitted.
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
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("300 KiB body under a 256 KiB pool: status %d, want 503", resp.StatusCode)
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
// tool failure). Delivery callers must get the protocol overload unchanged.
func TestDeliveredOverloadPassesThrough503(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	core := writeScript(t, `printf '{"hits":[],"pad":"'
head -c 4194304 /dev/zero | tr '\0' 'a'
printf '"}'
`)
	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": core, "XMUSTARD_TRANSIENT_BYTE_BUDGET": "1048576"})
	req, _ := http.NewRequest("GET", p.base+"/api/workspaces/ws/search?q=x", nil)
	req.Header.Set("X-Xmustard-Delivery", "xmustard.evidence/v1")
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" || !strings.Contains(string(body), `"overloaded":true`) {
		t.Fatalf("delivered overload: status %d Retry-After=%q body %.200s", resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}
}

// Fable evidence F7 (API): decoding the captured Rust output and constructing the
// response copy it again; those copies must be admitted too, not only the capture.
func TestResponseConstructionIsAdmitted(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	core := writeScript(t, `printf '{"hits":[],"pad":"'
head -c 1258291 /dev/zero | tr '\0' 'a'
printf '"}'
`)
	// 3 MiB pool: a 1.2 MiB capture fits, capture + decode + response (3x) does not
	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": core, "XMUSTARD_TRANSIENT_BYTE_BUDGET": "3145728"})
	resp, err := testClient.Get(p.base + "/api/workspaces/ws/search?q=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("capture fits but construction does not: want 503, got %d", resp.StatusCode)
	}
}

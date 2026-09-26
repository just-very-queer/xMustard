package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// realCoreBinary returns a built xmustard-core that supports `serve`, or skips.
func realCoreBinary(t *testing.T) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	candidates := []string{
		os.Getenv("XMUSTARD_CORE_BIN"),
		filepath.Join(filepath.Dir(here), "..", "..", "..", "rust-core", "target", "release", "xmustard-core"),
	}
	for _, bin := range candidates {
		if bin == "" {
			continue
		}
		out, _ := exec.Command(bin, "serve", "--bogus=1").CombinedOutput()
		if strings.HasPrefix(string(out), "usage: xmustard-core serve") {
			return bin
		}
	}
	t.Skip("no xmustard-core with `serve` built (cd rust-core && cargo build --release)")
	return ""
}

// WS-02: with XMUSTARD_CORE_WORKER=1 the real API binary serves the nine tools' Rust
// work from one resident worker (no per-call xmustard-core exec), and shutdown ends
// the worker with the API. WS-14: the only other exec is the code index worker
// (`index update`, one-shot by design under the heavy slot), once for the unchanged
// tree, and the graph reads are answered from the worker's resident index.
func TestAPIWithTheCoreWorkerServesToolRoutesWithoutPerCallExecs(t *testing.T) {
	core := realCoreBinary(t)
	repo := t.TempDir()
	files := map[string]string{
		"src/engine.go":  "package engine\n\nfunc ComputeTotal(a int) int { return helperValue(a) }\n\nfunc helperValue(a int) int { return a + 1 }\n",
		"src/handler.go": "package engine\n\nfunc HandleRequest() int { return ComputeTotal(2) }\n",
	}
	for rel, content := range files {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
		{"add", "-A"}, {"commit", "-qm", "c"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	dir := t.TempDir()
	ws := "wsWorker"
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": repo}})
	snapPath := filepath.Join(dir, "workspaces", ws, "snapshot.json")
	if err := os.MkdirAll(filepath.Dir(snapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapPath, snap, 0o644); err != nil {
		t.Fatal(err)
	}
	// every core exec is logged as "<subcommand> <pid>"; exec keeps the pid.
	execLog := filepath.Join(t.TempDir(), "execs.log")
	wrapper := filepath.Join(t.TempDir(), "xmustard-core")
	script := "#!/bin/sh\necho \"$1 $$\" >> '" + execLog + "'\nexec '" + core + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	execs := func() []string {
		raw, _ := os.ReadFile(execLog)
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
	p := startAPIProc(t, map[string]string{
		"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": wrapper, "XMUSTARD_CORE_WORKER": "1",
	})
	base := p.base + "/api/workspaces/" + ws
	// The read tools with Rust work. diagnostics needs Postgres and why_failed a
	// recorded run, so this fixture does not call them; remember and verify write
	// memory only.
	routes := []string{
		"/session-grounding",                                     // ground
		"/context/active?query=total",                            // recall
		"/search?q=ComputeTotal",                                 // search
		"/explain-path?path=src%2Fengine.go",                     // explain
		"/changes/since-index",                                   // impact, current changes
		"/changes/since-index?symbol=ComputeTotal",               // impact, symbol
		"/changes/since-index?from=HandleRequest&to=helperValue", // impact, trace
	}
	for round := 0; round < 2; round++ {
		for _, route := range routes {
			resp, err := testClient.Get(base + route)
			if err != nil {
				t.Fatalf("%s: %v", route, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: status %d %.300s\nlog:\n%s", route, resp.StatusCode, body, p.stderr.String())
			}
			if round == 1 && strings.HasPrefix(route, "/search") && !strings.Contains(string(body), `"source":"resident_index"`) {
				t.Fatalf("search was not answered from the resident index: %.600s", body)
			}
		}
	}
	all := execs()
	var workerPID int
	indexRuns := 0
	for _, line := range all {
		sub, pid, _ := strings.Cut(line, " ")
		if sub == "index" {
			indexRuns++
			continue
		}
		if sub != "serve" {
			t.Fatalf("a tool route exec'd `xmustard-core %s` with the worker on; execs: %v", sub, all)
		}
		if workerPID != 0 {
			t.Fatalf("the worker started more than once: %v", all)
		}
		workerPID, _ = strconv.Atoi(pid)
	}
	if indexRuns != 1 {
		t.Fatalf("want one index update for the unchanged tree, got %d; execs: %v", indexRuns, all)
	}
	if workerPID == 0 || !alive(workerPID) {
		t.Fatalf("no running worker after the tool calls: execs %v\nlog:\n%s", all, p.stderr.String())
	}
	t.Logf("%d tool calls, core execs: %v", 2*len(routes), all)

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	p.waitExit(t, 30*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for alive(workerPID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(workerPID) {
		_ = syscall.Kill(workerPID, syscall.SIGKILL)
		t.Fatalf("the resident worker %d outlived the API", workerPID)
	}
}

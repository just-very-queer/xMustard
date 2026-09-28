package main

import (
	"encoding/json"
	"io"
	"maps"
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

// workerWorkspace commits a two-file Go repository and registers it as a workspace in a
// new data directory; it returns the repository, the data directory and the workspace.
func workerWorkspace(t *testing.T) (repo, dataDir, ws string) {
	t.Helper()
	repo = t.TempDir()
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
	dataDir = t.TempDir()
	ws = "wsWorker"
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": repo}})
	snapPath := filepath.Join(dataDir, "workspaces", ws, "snapshot.json")
	if err := os.MkdirAll(filepath.Dir(snapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapPath, snap, 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, dataDir, ws
}

// WS-02: with XMUSTARD_CORE_WORKER=1 the real API binary serves the nine tools' Rust
// work from one resident worker (no per-call xmustard-core exec), and shutdown ends
// the worker with the API. WS-14: the only other execs are the code index worker
// (`index update`) and, WS-22, the first ground's index baseline build (`changetrack
// index`), both one-shot by design under the heavy slot and once for the unchanged
// tree; the graph reads are answered from the worker's resident index.
func TestAPIWithTheCoreWorkerServesToolRoutesWithoutPerCallExecs(t *testing.T) {
	core := realCoreBinary(t)
	_, dir, ws := workerWorkspace(t)
	// every core exec is logged as "<subcommand> <verb>|<pid>"; exec keeps the pid.
	execLog := filepath.Join(t.TempDir(), "execs.log")
	wrapper := filepath.Join(t.TempDir(), "xmustard-core")
	script := "#!/bin/sh\necho \"$1 $2|$$\" >> '" + execLog + "'\nexec '" + core + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	execs := func() []string {
		raw, _ := os.ReadFile(execLog)
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
	p := startAPIProc(t, map[string]string{
		"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": wrapper, "XMUSTARD_CORE_WORKER": "1",
		// The worker samples the tree when it goes idle, and the tree counts this
		// user's stdio shims on the host; other sessions' shims must not recycle it.
		"XMUSTARD_RSS_SOFT_CEILING_BYTES": strconv.Itoa(1 << 40),
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
	// one-shot by design, each once for the unchanged tree: the code index update
	// (WS-14) and the first ground's index baseline build (WS-22)
	oneShot := map[string]int{}
	for _, line := range all {
		call, pid, _ := strings.Cut(line, "|")
		sub, _, _ := strings.Cut(call, " ")
		switch {
		case sub == "index":
			oneShot[sub]++
		case call == "changetrack index":
			oneShot[call]++
		case sub != "serve":
			t.Fatalf("a tool route exec'd `xmustard-core %s` with the worker on; execs: %v", call, all)
		case workerPID != 0:
			t.Fatalf("the worker started more than once: %v", all)
		default:
			workerPID, _ = strconv.Atoi(pid)
		}
	}
	if want := map[string]int{"index": 1, "changetrack index": 1}; !maps.Equal(oneShot, want) {
		t.Fatalf("want one index update and one baseline build for the unchanged tree, got %v; execs: %v", oneShot, all)
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

// WS-15: with the worker on, the watcher keeps the code index fresh with no read
// asking. Once the start-up refresh has brought the watcher to `ok`, an edit becomes
// one `index update --paths <file>` run on its own, and the next search finds the new
// symbol in the resident index without refreshing anything itself.
func TestAPIWatcherRefreshesTheIndexAfterAnEdit(t *testing.T) {
	core := realCoreBinary(t)
	repo, dir, ws := workerWorkspace(t)
	execLog := filepath.Join(t.TempDir(), "execs.log")
	wrapper := filepath.Join(t.TempDir(), "xmustard-core")
	script := "#!/bin/sh\necho \"$*\" >> '" + execLog + "'\nexec '" + core + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	execs := func() []string {
		raw, _ := os.ReadFile(execLog)
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
	p := startAPIProc(t, map[string]string{
		"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": wrapper, "XMUSTARD_CORE_WORKER": "1",
		"XMUSTARD_RSS_SOFT_CEILING_BYTES": strconv.Itoa(1 << 40),
	})
	search := func(q string) string {
		t.Helper()
		resp, err := testClient.Get(p.base + "/api/workspaces/" + ws + "/search?q=" + q)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("search %s: status %d %.300s\nlog:\n%s", q, resp.StatusCode, body, p.stderr.String())
		}
		return string(body)
	}
	// the first read registers the root with the watcher and brings the index up
	deadline := time.Now().Add(30 * time.Second)
	for body := search("ComputeTotal"); !strings.Contains(body, `"watcher_state":"ok"`); body = search("ComputeTotal") {
		if time.Now().After(deadline) {
			t.Fatalf("the watcher never reached ok: %.600s\nexecs: %v\nlog:\n%s", body, execs(), p.stderr.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	before := len(execs())
	edited := "package engine\n\nfunc ComputeTotal(a int) int { return helperValue(a) }\n\nfunc helperValue(a int) int { return a + 1 }\n\nfunc BrandNewWatcherSymbol() int { return 7 }\n"
	if err := os.WriteFile(filepath.Join(repo, "src", "engine.go"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshed := func() bool {
		for _, line := range execs()[before:] {
			if strings.HasPrefix(line, "index update ") && strings.HasSuffix(line, " --paths src/engine.go") {
				return true
			}
		}
		return false
	}
	for deadline := time.Now().Add(15 * time.Second); !refreshed(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no watcher refresh of src/engine.go; execs since the edit: %v\nlog:\n%s", execs()[before:], p.stderr.String())
		}
	}
	after := len(execs())
	body := search("BrandNewWatcherSymbol")
	for _, want := range []string{`"source":"resident_index"`, `"watcher_state":"ok"`, "src/engine.go", "BrandNewWatcherSymbol"} {
		if !strings.Contains(body, want) {
			t.Fatalf("search after the watcher's refresh lacks %s: %.800s", want, body)
		}
	}
	if extra := execs()[after:]; len(extra) != 0 {
		t.Fatalf("the read refreshed the index although the watcher had: %v", extra)
	}
}

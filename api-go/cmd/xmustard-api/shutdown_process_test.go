package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Audit Go #2: on SIGTERM the process must not exit before the shutdown drain has
// durably marked in-flight runs interrupted, stopped their workers, and closed
// services. Exercised against the real binary with a live managed run.
func TestSIGTERMPersistsInterruptedRunBeforeExit(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	ws := "wsShutdown"
	pidFile := filepath.Join(t.TempDir(), "worker.pid")
	fake := filepath.Join(t.TempDir(), "opencode")
	script := "#!/bin/sh\nif [ \"$1\" = \"models\" ]; then echo fake/test-model; exit 0; fi\necho $$ > \"" + pidFile + "\"\nexec sleep 60\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile := func(rel string, v any) {
		b, _ := json.Marshal(v)
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("settings.json", map[string]any{"local_agent_type": "opencode", "opencode_bin": fake})
	writeFile(filepath.Join("workspaces", ws, "snapshot.json"), map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": repo}})
	writeFile(filepath.Join("workspaces", ws, "runs", "run-origin.json"), map[string]any{
		"run_id": "run-origin", "workspace_id": ws, "issue_id": "i1", "runtime": "opencode",
		"model": "fake/test-model", "status": "completed", "prompt": "long task", "command": []string{fake},
		"created_at": "2026-09-24T00:00:00Z",
	})

	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir})
	resp, err := http.Post(p.base+"/api/workspaces/"+ws+"/runs/run-origin/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var run struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&run)
	resp.Body.Close()
	if resp.StatusCode != 200 || run.RunID == "" {
		t.Fatalf("retry: status %d run=%q log=%s", resp.StatusCode, run.RunID, p.stderr.String())
	}
	var workerPID int
	deadline := time.Now().Add(10 * time.Second)
	for workerPID == 0 && time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(raw), "\n") {
			workerPID, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if workerPID == 0 {
		t.Fatalf("worker never started: %s", p.stderr.String())
	}
	defer syscall.Kill(workerPID, syscall.SIGKILL)
	// wait for the run to be claimed as running before shutting down
	runPath := filepath.Join(dir, "workspaces", ws, "runs", run.RunID+".json")
	readStatus := func() string {
		var r struct {
			Status string `json:"status"`
		}
		b, _ := os.ReadFile(runPath)
		_ = json.Unmarshal(b, &r)
		return r.Status
	}
	for readStatus() != "running" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	p.waitExit(t, 30*time.Second)

	if got := readStatus(); got != "interrupted" {
		t.Fatalf("after process exit the run status is %q, want durable \"interrupted\"; log:\n%s", got, p.stderr.String())
	}
	time.Sleep(100 * time.Millisecond)
	if alive(workerPID) {
		t.Fatalf("worker %d survived API shutdown", workerPID)
	}
	if !strings.Contains(p.stderr.String(), "shutdown: services closed") {
		t.Fatalf("process exited without reporting closed services; log:\n%s", p.stderr.String())
	}
}

// Fable runtime review #5 (Root-accepted): helper children run in their own process
// groups, so a terminal signal no longer reaches them. Shutdown must end in-flight
// helper children (bounded drain, then cancel request contexts and kill owned
// children) instead of orphaning them past the API's exit.
func TestShutdownEndsInFlightHelperChildren(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	pidDir := t.TempDir()
	core := writeBlockingChild(t, t.TempDir(), "xmustard-core", pidDir)
	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": core, "XMUSTARD_SHUTDOWN_DRAIN_SECONDS": "1"})
	go func() {
		// no client timeout: only the server's shutdown may end this request
		if resp, err := http.Get(p.base + "/api/workspaces/ws/search?q=x"); err == nil {
			resp.Body.Close()
		}
	}()
	child := waitForChildPID(t, pidDir, 10*time.Second)
	defer syscall.Kill(child, syscall.SIGKILL)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	p.waitExit(t, 30*time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for alive(child) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(child) {
		t.Fatalf("helper child %d outlived the API; log:\n%s", child, p.stderr.String())
	}
}

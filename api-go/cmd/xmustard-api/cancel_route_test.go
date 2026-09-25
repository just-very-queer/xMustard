package main

import (
	"context"
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

// writeBlockingChild writes an executable that records its PID and then blocks, used
// as both the Rust core binary and the ast-grep binary so every tool route's child
// is a real process the test can observe.
func writeBlockingChild(t *testing.T, dir, name, pidDir string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\necho $$ > \"" + pidDir + "/$$.pid\"\nexec sleep 60\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitForChildPID(t *testing.T, pidDir string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ents, _ := os.ReadDir(pidDir)
		for _, e := range ents {
			raw, err := os.ReadFile(filepath.Join(pidDir, e.Name()))
			if err != nil || !strings.HasSuffix(string(raw), "\n") {
				continue
			}
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				_ = os.Remove(filepath.Join(pidDir, e.Name()))
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no child process started within %v", timeout)
	return 0
}

// Audit Go #4: cancelling a tool request must kill the actual child it started, for
// every nine-tool route that spawns one (Rust core or ast-grep).
func TestToolRouteCancellationKillsBlockedChild(t *testing.T) {
	srv, dir := newRouteServer(t)
	bin := t.TempDir()
	pidDir := t.TempDir()
	t.Setenv("XMUSTARD_CORE_BIN", writeBlockingChild(t, bin, "xmustard-core", pidDir))
	writeBlockingChild(t, bin, "sg", pidDir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ws := "wsCancel"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": root}})
	wsDir := filepath.Join(dir, "workspaces", ws)
	if err := os.MkdirAll(filepath.Join(wsDir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "snapshot.json"), snap, 0o644); err != nil {
		t.Fatal(err)
	}
	run, _ := json.Marshal(map[string]any{"run_id": "r1", "workspace_id": ws, "issue_id": "i1", "status": "failed", "runtime": "opencode", "model": "m", "command": []string{"x"}})
	if err := os.WriteFile(filepath.Join(wsDir, "runs", "r1.json"), run, 0o644); err != nil {
		t.Fatal(err)
	}

	routes := map[string]string{
		"ground":         "/session-grounding",
		"recall":         "/context/active",
		"search":         "/search?q=Add",
		"search-pattern": "/search?q=%24A&mode=pattern",
		"explain":        "/explain-path?path=a.go",
		"impact":         "/changes/since-index",
		"impact-symbol":  "/changes/since-index?symbol=Add",
		"impact-trace":   "/changes/since-index?from=A&to=B",
		"why_failed":     "/runs/r1/why-failed",
	}
	for name, suffix := range routes {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/workspaces/"+ws+suffix, nil)
			done := make(chan struct{})
			go func() {
				defer close(done)
				if resp, err := http.DefaultClient.Do(req); err == nil {
					resp.Body.Close()
				}
			}()
			pid := waitForChildPID(t, pidDir, 10*time.Second)
			defer syscall.Kill(pid, syscall.SIGKILL)
			cancel()
			<-done
			deadline := time.Now().Add(3 * time.Second)
			for alive(pid) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if alive(pid) {
				t.Fatalf("%s: child %d still running 3s after the request was cancelled", name, pid)
			}
		})
	}
}

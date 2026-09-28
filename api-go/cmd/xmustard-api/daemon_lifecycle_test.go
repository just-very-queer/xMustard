//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/workspaceops"
)

// daemonEnv is the environment a fixture API runs with: the caller's, minus every
// xMustard setting and socket-activation variable, plus env.
func daemonEnv(env map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "XMUSTARD_") && !strings.HasPrefix(kv, "LISTEN_") {
			out = append(out, kv)
		}
	}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// activatedProc is an API started with a passed socket.
type activatedProc struct {
	cmd    *exec.Cmd
	out    *syncBuffer
	exited chan struct{}
	err    error // valid once exited is closed
}

// wait returns the exit error, or fails the test when the API is still running after d.
func (p *activatedProc) wait(t *testing.T, d time.Duration) error {
	t.Helper()
	select {
	case <-p.exited:
		return p.err
	case <-time.After(d):
		t.Fatalf("the API did not exit within %s:\n%s", d, p.out)
		return nil
	}
}

// startActivated runs the API the way systemd socket activation does: ln's socket is
// fd 3 and LISTEN_PID names the API's own pid (sh sets it, then execs the API in its
// place).
func startActivated(t *testing.T, ln net.Listener, env map[string]string) *activatedProc {
	t.Helper()
	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command("/bin/sh", "-c", `LISTEN_PID=$$ LISTEN_FDS=1 exec "$0"`, apiBinary(t))
	cmd.ExtraFiles = []*os.File{f}
	return runAPI(t, cmd, env)
}

// runAPI starts cmd with env (daemonEnv) and kills it at cleanup.
func runAPI(t *testing.T, cmd *exec.Cmd, env map[string]string) *activatedProc {
	t.Helper()
	cmd.Env = daemonEnv(env)
	p := &activatedProc{cmd: cmd, out: &syncBuffer{}, exited: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.out, p.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = cmd.Wait(); close(p.exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.exited
	})
	return p
}

// Socket activation: the API serves the socket it was passed, not XMUSTARD_API_PORT,
// and stops cleanly on SIGTERM (exit 0, so Restart=on-failure leaves it stopped).
func TestSocketActivationServesThePassedSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unused := freePort(t)
	p := startActivated(t, ln, map[string]string{
		"XMUSTARD_DATA_DIR": t.TempDir(), "XMUSTARD_API_PORT": fmt.Sprint(unused),
	})
	addr := ln.Addr().String()
	_ = ln.Close() // the parent lets go; the API holds the only copy of the socket
	deadline := time.Now().Add(15 * time.Second)
	for {
		if h, err := probeHealth("http://" + addr); err == nil {
			if h["service"] != "api-go" {
				t.Fatalf("health on the activated socket: %v", h)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no answer on the activated socket %s: %s", addr, p.out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := probeHealth(fmt.Sprintf("http://127.0.0.1:%d", unused)); err == nil {
		t.Fatal("the API also bound XMUSTARD_API_PORT")
	}
	if !strings.Contains(p.out.String(), "socket activation passed "+addr) {
		t.Fatalf("log does not name the activated socket:\n%s", p.out)
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	if err := p.wait(t, 30*time.Second); err != nil {
		t.Fatalf("SIGTERM exit: %v\n%s", err, p.out)
	}
}

// An activated socket on a non-loopback address meets the same fail-closed interlock
// as a bind: refused without XMUSTARD_AUTH=required, credentials and TLS.
func TestSocketActivationOnAnExposedAddressIsRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip("cannot listen on 0.0.0.0 here")
	}
	p := startActivated(t, ln, map[string]string{"XMUSTARD_DATA_DIR": t.TempDir()})
	_ = ln.Close()
	if err := p.wait(t, 15*time.Second); err == nil || !strings.Contains(p.out.String(), "refusing non-loopback bind") {
		t.Fatalf("exposed activated socket: exit %v\n%s", err, p.out)
	}
}

// Under a service manager the API logs to a rotated file with stdout and stderr moved
// onto it, and checks the governance store once it listens: the result (migrated to
// this build's schema, quick_check passed) is on /api/health.
func TestDaemonLogRotatesAndHealthReportsTheStoreCheck(t *testing.T) {
	dir := t.TempDir()
	s, err := govstore.Open(context.Background(), filepath.Join(dir, "governance.db"), govstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	logFile := filepath.Join(dir, "logs", "api.log")
	p := startAPIProc(t, map[string]string{
		"XMUSTARD_DATA_DIR": dir, "XMUSTARD_LOG_FILE": logFile,
		"XMUSTARD_LOG_MAX_BYTES": "512", "XMUSTARD_LOG_KEEP": "2",
		"XMUSTARD_APPROVER_TOKEN": "never-inherited",
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		h, err := probeHealth(p.base)
		if err != nil {
			t.Fatal(err)
		}
		if st, _ := h["store"].(map[string]any); st["status"] == "ok" {
			if st["schema_version"] != float64(govstore.LatestSchemaVersion()) {
				t.Fatalf("store after the startup check: %v", st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never reported the store check: %v", h)
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	p.waitExit(t, 30*time.Second)
	if p.stderr.String() != "" {
		t.Fatalf("output escaped the log file:\n%s", p.stderr)
	}
	var all strings.Builder
	for _, name := range []string{logFile + ".2", logFile + ".1", logFile} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v (want two rotated generations and the live log)", name, err)
		}
		all.Write(b)
	}
	if _, err := os.Stat(logFile + ".3"); !os.IsNotExist(err) {
		t.Fatal("more generations kept than XMUSTARD_LOG_KEEP")
	}
	for _, want := range []string{"store: governance store ok", "shutdown: complete"} {
		if !strings.Contains(all.String(), want) {
			t.Fatalf("log lacks %q:\n%s", want, all.String())
		}
	}
}

// An upgrade while a client is connected: the daemon stops (SIGTERM) and a new one
// starts on the same port and data dir. The relay's next call waits for the new daemon
// instead of failing, replays the client's initialize on the session the restart
// forgot, and lands; memory written before the restart is still there.
func TestRelayClientCarriesOnAcrossADaemonRestart(t *testing.T) {
	dir, port := t.TempDir(), freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	env := map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_API_PORT": fmt.Sprint(port)}
	waitHealthy := func(p *activatedProc) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(25 * time.Millisecond) {
			if _, err := probeHealth(base); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the API did not answer: %s", p.out)
			}
		}
	}
	first := runAPI(t, exec.Command(apiBinary(t)), env)
	waitHealthy(first)
	peer, _, _ := startRelay(t, base, "", "XMUSTARD_WORKSPACE_ID=ws")
	remember := func(id int, content string) map[string]any {
		peer.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]any{"name": "remember", "arguments": map[string]any{"content": content}}})
		return peer.reply(id)
	}
	peer.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}})
	peer.reply(1)
	peer.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if r := remember(2, "written before the restart"); r["result"] == nil {
		t.Fatalf("remember before the restart: %v", r)
	}

	_ = first.cmd.Process.Signal(syscall.SIGTERM)
	if err := first.wait(t, 30*time.Second); err != nil {
		t.Fatalf("SIGTERM exit: %v", err)
	}
	peer.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "remember", "arguments": map[string]any{"content": "written across the restart"}}})
	time.Sleep(500 * time.Millisecond) // the call waits on a refused port meanwhile
	second := runAPI(t, exec.Command(apiBinary(t)), env)
	waitHealthy(second)
	if r := peer.reply(3); r["result"] == nil || r["result"].(map[string]any)["isError"] == true {
		t.Fatalf("the call made while the daemon restarted: %v", r)
	}
	entries, err := workspaceops.ListContextEntries(dir, "ws", "")
	if err != nil || len(entries) != 2 {
		t.Fatalf("memory across the restart: %d entries, %v", len(entries), err)
	}
}

// The API never reads the human approver's token and drops it before it starts
// anything, so no helper or agent it runs inherits it.
func TestPrepareDaemonDropsTheApproverToken(t *testing.T) {
	t.Setenv("XMUSTARD_APPROVER_TOKEN", "typed-by-a-human")
	t.Setenv("XMUSTARD_LOG_FILE", "")
	if err := prepareDaemon(); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv("XMUSTARD_APPROVER_TOKEN"); ok {
		t.Fatal("the approver token is still in the API's environment")
	}
	t.Setenv("XMUSTARD_LOG_FILE", "relative.log")
	if err := prepareDaemon(); err == nil {
		t.Fatal("a relative log file was accepted")
	}
}

func probeHealth(base string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := testClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var h map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, err
	}
	return h, nil
}

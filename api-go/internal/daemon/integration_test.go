//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestRealServiceManagerIntegration drives the host's real service manager (launchctl
// or systemctl --user) with the real xmustard-api. It is opt-in, never part of a
// normal run: set XMUSTARD_DAEMON_IT=1. It uses a throwaway label, port, HOME and
// data dir; it never names, reads or replaces any other agent or unit (setup refuses a
// unit it did not write). systemd's user manager reads units only from its own search
// path, so there the units go to $XDG_RUNTIME_DIR/systemd/user (removed at logout).
func TestRealServiceManagerIntegration(t *testing.T) {
	if os.Getenv("XMUSTARD_DAEMON_IT") != "1" {
		t.Skip("opt-in: set XMUSTARD_DAEMON_IT=1 to drive the real launchctl/systemctl")
	}
	p, ok := For(runtime.GOOS, os.Getuid())
	if !ok {
		t.Skip("no supported service manager on " + runtime.GOOS)
	}
	ctx := context.Background()
	home := t.TempDir()
	api := filepath.Join(home, "bin", "xmustard-api")
	// build before HOME moves, so go keeps its module and build caches
	if out, err := exec.Command("go", "build", "-o", api, "../../cmd/xmustard-api").CombinedOutput(); err != nil {
		t.Fatalf("build xmustard-api: %v\n%s", err, out)
	}
	t.Setenv("HOME", home)
	s := p.Defaults(func(string) string { return "" }, home)
	s.Label = fmt.Sprintf("xmustard-it-%d", os.Getpid())
	s.APIBin, s.Path = api, "/usr/bin:/bin"
	s.Port = freePort(t)
	if p.Name() == "systemd" {
		s.UnitDir = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "systemd", "user")
		if !filepath.IsAbs(s.UnitDir) {
			t.Skip("systemd: no XDG_RUNTIME_DIR, so no user manager to drive")
		}
	}
	in := Installer{Platform: p, Run: ExecRunner, Probe: Probe, Wait: 30 * time.Second}
	t.Cleanup(func() {
		if _, err := in.Uninstall(context.Background(), s); err != nil {
			t.Errorf("cleanup uninstall: %v", err)
		}
	})

	r, err := in.Install(ctx, s)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	var store struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(r.Health.Store, &store)
	t.Logf("installed under %s: service %+v, store %s", p.Name(), r.Service, r.Health.Store)
	pid := r.Service.PID
	if pid == 0 {
		t.Fatalf("the service manager reports no daemon pid: %+v", r.Service)
	}

	// crash restart: SIGKILL the daemon; the manager starts a new one
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(40 * time.Second)
	for {
		st := p.Status(ctx, ExecRunner, s)
		if _, err := Probe(ctx, s.BaseURL()); err == nil && st.PID != 0 && st.PID != pid {
			t.Logf("restarted after SIGKILL: pid %d -> %d", pid, st.PID)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no restart after SIGKILL: %+v", st)
		}
		time.Sleep(250 * time.Millisecond)
	}

	// an upgrade: setup again restarts the daemon and it answers
	if r, err = in.Install(ctx, s); err != nil || len(r.Changed) != 0 {
		t.Fatalf("reinstall: %+v %v", r, err)
	}
	if _, err := in.Uninstall(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(ctx, s.BaseURL()); err == nil {
		t.Fatal("the daemon still answers after uninstall")
	}
	if log, err := os.ReadFile(s.LogFile); err != nil || len(log) == 0 {
		t.Fatalf("daemon log %s: %v", s.LogFile, err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

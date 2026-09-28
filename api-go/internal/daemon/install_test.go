package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeManager stands in for launchctl or systemctl: it records every command and
// "runs" the daemon while its units are loaded, so the fake probe answers.
type fakeManager struct {
	mu       sync.Mutex
	cmds     []string
	loaded   bool // launchd: the agent is in the domain
	running  bool // the daemon answers
	healthy  bool // a started daemon answers its probe
	failCmds map[string]error
}

func newFakeManager() *fakeManager { return &fakeManager{healthy: true, failCmds: map[string]error{}} }

func (m *fakeManager) run(_ context.Context, name string, args ...string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := name + " " + strings.Join(args, " ")
	m.cmds = append(m.cmds, cmd)
	for prefix, err := range m.failCmds {
		if strings.HasPrefix(cmd, prefix) {
			return "boom", err
		}
	}
	switch verb := args[0]; {
	case name == "launchctl" && verb == "bootstrap":
		m.loaded, m.running = true, true
	case name == "launchctl" && verb == "bootout":
		m.loaded, m.running = false, false
	case name == "launchctl" && verb == "print":
		if !m.loaded {
			return "Could not find service", errors.New("exit status 113")
		}
		return "gui/501/x = {\n\tstate = running\n\tpid = 4242\n}\n", nil
	case name == "systemctl" && slices.Contains([]string{"restart", "try-restart"}, args[1]):
		m.running = true
	case name == "systemctl" && slices.Contains([]string{"stop", "disable"}, args[1]):
		m.running = false
	case name == "systemctl" && args[1] == "show":
		return "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=4343\n", nil
	}
	return "", nil
}

func (m *fakeManager) probe(context.Context, string) (Health, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running || !m.healthy {
		return Health{}, errors.New("connection refused")
	}
	return Health{Status: "ok", Service: "api-go", Store: []byte(`{"status":"ok","schema_version":2}`)}, nil
}

func (m *fakeManager) take() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.cmds
	m.cmds = nil
	return c
}

func installSpec(t *testing.T, p Platform) Spec {
	t.Helper()
	home := t.TempDir()
	s := p.Defaults(func(string) string { return "" }, home)
	s.APIBin, s.Path = "/opt/xmustard/bin/xmustard-api", "/usr/bin:/bin"
	return s
}

func TestSystemdInstallUpgradeRollbackAndUninstall(t *testing.T) {
	ctx := context.Background()
	m := newFakeManager()
	p := Systemd{}
	in := Installer{Platform: p, Run: m.run, Probe: m.probe, Wait: 300 * time.Millisecond}
	s := installSpec(t, p)
	svc, sock := filepath.Join(s.UnitDir, "xmustard-api.service"), filepath.Join(s.UnitDir, "xmustard-api.socket")

	r, err := in.Install(ctx, s)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !slices.Equal(r.Changed, []string{svc, sock}) || string(r.Health.Store) == "" || r.Service.PID != 4343 {
		t.Fatalf("report = %+v", r)
	}
	// a new socket is bound fresh; the first connection (the probe) starts the service
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable xmustard-api.socket",
		"systemctl --user stop xmustard-api.service", "systemctl --user restart xmustard-api.socket"}
	if got := m.take(); !slices.Equal(got[:4], want) {
		t.Fatalf("first install ran %v, want %v first", got, want)
	}
	for _, dir := range []string{s.DataDir, filepath.Dir(s.LogFile)} {
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v, want a 0700 dir", dir, fi, err)
		}
	}

	// rerunning after an upgrade keeps the socket listening and restarts the service
	r, err = in.Install(ctx, s)
	if err != nil || len(r.Changed) != 0 {
		t.Fatalf("upgrade: %+v %v", r, err)
	}
	want = []string{"systemctl --user daemon-reload", "systemctl --user enable xmustard-api.socket",
		"systemctl --user start xmustard-api.socket", "systemctl --user try-restart xmustard-api.service"}
	if got := m.take(); !slices.Equal(got[:4], want) {
		t.Fatalf("upgrade ran %v, want %v", got, want)
	}

	// a change the daemon does not survive is rolled back to the previous units
	good, _ := os.ReadFile(svc)
	bad := s
	bad.Env = map[string]string{"XMUSTARD_PROFILE": "broken"}
	m.healthy = false
	_, err = in.Install(ctx, bad)
	if err == nil || !strings.Contains(err.Error(), "previous units were put back") || !strings.Contains(err.Error(), s.LogFile) {
		t.Fatalf("unhealthy upgrade: %v", err)
	}
	if now, _ := os.ReadFile(svc); string(now) != string(good) {
		t.Fatalf("service unit not restored:\n%s", now)
	}
	if got := m.take(); got[len(got)-1] != "systemctl --user try-restart xmustard-api.service" {
		t.Fatalf("the previous units were not started again: %v", got)
	}
	m.healthy = true

	u, err := in.Uninstall(ctx, s)
	if err != nil || !slices.Equal(u.Removed, []string{svc, sock}) {
		t.Fatalf("uninstall: %+v %v", u, err)
	}
	want = []string{"systemctl --user disable --now xmustard-api.socket", "systemctl --user stop xmustard-api.service",
		"systemctl --user daemon-reload"}
	if got := m.take(); !slices.Equal(got, want) {
		t.Fatalf("uninstall ran %v, want %v", got, want)
	}
	for _, f := range []string{svc, sock} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists", f)
		}
	}
	if _, err := os.Stat(s.DataDir); err != nil {
		t.Fatal("uninstall must keep the data dir")
	}
	if u, err := in.Uninstall(ctx, s); err != nil || len(u.Removed) != 0 || len(m.take()) != 0 {
		t.Fatalf("uninstall of nothing: %+v %v", u, err)
	}
}

func TestLaunchdInstallFailureRemovesTheNewAgent(t *testing.T) {
	ctx := context.Background()
	m := newFakeManager()
	p := Launchd{UID: 501}
	in := Installer{Platform: p, Run: m.run, Probe: m.probe, Wait: 300 * time.Millisecond}
	s := installSpec(t, p)
	plist := filepath.Join(s.UnitDir, "com.xmustard.api.plist")

	m.healthy = false
	if _, err := in.Install(ctx, s); err == nil || !strings.Contains(err.Error(), "new units were removed") {
		t.Fatalf("a first install that never answers: %v", err)
	}
	if _, err := os.Stat(plist); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the failed agent's plist was left installed")
	}
	if got := m.take(); !slices.Contains(got, "launchctl bootout gui/501/com.xmustard.api") || m.loaded {
		t.Fatalf("the failed agent was not booted out: %v", got)
	}

	m.healthy = true
	r, err := in.Install(ctx, s)
	if err != nil || r.Service.State != "running" || r.Service.PID != 4242 {
		t.Fatalf("install: %+v %v", r, err)
	}
	got := m.take()
	want := []string{"launchctl bootout gui/501/com.xmustard.api", "launchctl print gui/501/com.xmustard.api",
		"launchctl enable gui/501/com.xmustard.api", "launchctl bootstrap gui/501 " + plist}
	if !slices.Equal(got[:4], want) {
		t.Fatalf("install ran %v, want %v first", got, want)
	}

	// another xMustard API on the port is never mistaken for a fresh install's daemon
	other := installSpec(t, p)
	other.Label = "com.xmustard.other"
	if _, err := in.Install(ctx, other); err == nil || !strings.Contains(err.Error(), "already answers") {
		t.Fatalf("install next to a running API: %v", err)
	}
}

func TestInstallRefusesAUnitItDidNotWrite(t *testing.T) {
	m := newFakeManager()
	p := Launchd{UID: 501}
	in := Installer{Platform: p, Run: m.run, Probe: m.probe, Wait: time.Second}
	s := installSpec(t, p)
	s.Label = "com.xmustard.eval" // a hand-written agent already uses this label
	plist := filepath.Join(s.UnitDir, "com.xmustard.eval.plist")
	_ = os.MkdirAll(s.UnitDir, 0o755)
	handWritten := "<plist><dict><key>Label</key><string>com.xmustard.eval</string></dict></plist>\n"
	_ = os.WriteFile(plist, []byte(handWritten), 0o644)
	for name, op := range map[string]func() error{
		"install":   func() error { _, err := in.Install(context.Background(), s); return err },
		"uninstall": func() error { _, err := in.Uninstall(context.Background(), s); return err },
		"status":    func() error { _, err := in.Status(context.Background(), s); return err },
	} {
		if err := op(); !errors.Is(err, ErrForeignUnit) {
			t.Errorf("%s: got %v, want ErrForeignUnit", name, err)
		}
	}
	if got, _ := os.ReadFile(plist); string(got) != handWritten {
		t.Fatalf("the hand-written agent was changed:\n%s", got)
	}
	if cmds := m.take(); len(cmds) != 0 {
		t.Fatalf("commands ran against a foreign agent: %v", cmds)
	}
}

func TestProbeKnowsTheAPIFromOtherServices(t *testing.T) {
	if _, err := Probe(context.Background(), "http://127.0.0.1:1"); err == nil {
		t.Fatal("nothing listening is not healthy")
	}
	start := time.Now()
	calls := 0
	_, err := WaitHealthy(context.Background(), func(context.Context, string) (Health, error) {
		calls++
		return Health{}, errors.New("down")
	}, "http://x", 450*time.Millisecond)
	if err == nil || calls < 2 || time.Since(start) < 400*time.Millisecond {
		t.Fatalf("WaitHealthy: %v after %d calls in %s", err, calls, time.Since(start))
	}
}

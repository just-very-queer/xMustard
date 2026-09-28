package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"xmustard/api-go/internal/daemon"
	"xmustard/api-go/internal/workspaceops"
)

// fakeService stands in for systemctl or launchctl and the daemon behind them.
type fakeService struct {
	mu      sync.Mutex
	cmds    []string
	running bool
}

func (f *fakeService) run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := name + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, cmd)
	switch args[1] { // systemctl --user <verb> ...
	case "restart", "try-restart":
		f.running = true
	case "stop", "disable":
		f.running = false
	case "show":
		return "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=77\n", nil
	}
	return "", nil
}

func (f *fakeService) probe(context.Context, string) (daemon.Health, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running {
		return daemon.Health{}, errors.New("connection refused")
	}
	return daemon.Health{Status: "ok", Service: "api-go"}, nil
}

func (f *fakeService) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.cmds
	f.cmds = nil
	return c
}

// lifecycleTest is a lifecycle environment on goos with a fake service manager, a
// temporary HOME and a bin dir holding xmustard-ops and an executable xmustard-api.
func lifecycleTest(t *testing.T, goos string, env map[string]string) (lifecycleEnv, *bytes.Buffer, *bytes.Buffer, *fakeService, string) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "xmustard-api"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	full := map[string]string{"HOME": home, "PATH": "/usr/bin:/bin"}
	for k, v := range env {
		full[k] = v
	}
	ops, out, errOut := testEnv(full, "")
	svc := &fakeService{}
	return lifecycleEnv{opsEnv: ops, goos: goos, uid: 501, run: svc.run, probe: svc.probe,
		exe: filepath.Join(bin, "xmustard-ops"), lookPath: func(string) (string, error) { return "", errors.New("not on PATH") },
		now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }}, out, errOut, svc, home
}

func runLifecycle(e lifecycleEnv, args ...string) int { return lifecycleCommands[args[0]](e, args[1:]) }

func TestSetupPrintRendersUnitsWithoutCredentials(t *testing.T) {
	e, out, errOut, svc, home := lifecycleTest(t, "linux", map[string]string{
		"XMUSTARD_APPROVER_TOKEN": "approver-secret", "XMUSTARD_API_TOKEN": "bearer-secret", "XMUSTARD_AUTH_TOKENS": "a:admin:x",
	})
	for platform, want := range map[string][]string{
		"darwin": {filepath.Join(home, "Library", "LaunchAgents", "com.xmustard.api.plist")},
		"linux": {filepath.Join(home, ".config", "systemd", "user", "xmustard-api.service"),
			filepath.Join(home, ".config", "systemd", "user", "xmustard-api.socket")},
	} {
		if code := runLifecycle(e, "setup", "--print", "--platform", platform, "--env", "XMUSTARD_PROFILE=platform"); code != 0 {
			t.Fatalf("%s: exit %d: %s", platform, code, errOut)
		}
		raw := out.String()
		for _, secret := range []string{"approver-secret", "bearer-secret", "a:admin:x"} {
			if strings.Contains(raw, secret) {
				t.Fatalf("%s: a credential from the caller's environment reached a unit:\n%s", platform, raw)
			}
		}
		v := decodeOut(t, out)
		var paths []string
		for _, u := range v["units"].([]any) {
			paths = append(paths, u.(map[string]any)["path"].(string))
		}
		// the plist, or the service unit, runs the sibling xmustard-api with the extra env
		main := v["units"].([]any)[0].(map[string]any)["content"].(string)
		if !strings.Contains(main, filepath.Join(home, "bin", "xmustard-api")) || !strings.Contains(main, "XMUSTARD_PROFILE") {
			t.Fatalf("%s unit lacks the sibling api binary or the extra env:\n%s", platform, main)
		}
		if !slices.Equal(paths, want) {
			t.Fatalf("%s units at %v, want %v", platform, paths, want)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 1 { // only bin/
		t.Fatalf("--print wrote files: %v", entries)
	}
	if len(svc.take()) != 0 {
		t.Fatal("--print ran service manager commands")
	}
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"setup", "--env", "XMUSTARD_APPROVER_TOKEN=x"}, exitError, "refusing XMUSTARD_APPROVER_TOKEN"},
		{[]string{"setup", "--env", "GH_TOKEN=x"}, exitError, "refusing GH_TOKEN"},
		{[]string{"setup", "--print", "--platform", "windows"}, exitError, "no supported service manager"},
		{[]string{"setup", "--platform", "darwin"}, exitUsage, "usage"},
		{[]string{"setup", "--root", "relative/repo"}, exitError, "--root must be absolute"},
	} {
		errOut.Reset()
		if code := runLifecycle(e, c.args...); code != c.code || !strings.Contains(errOut.String(), c.want) {
			t.Errorf("%v: exit %d (%s), want %d with %q", c.args, code, errOut, c.code, c.want)
		}
	}
}

func TestSetupDaemonCommandsAndUninstall(t *testing.T) {
	e, out, errOut, svc, home := lifecycleTest(t, "linux", nil)
	repo := filepath.Join(home, "repo")
	if code := runLifecycle(e, "setup", "--root", repo, "--client", "claude-code", "--wait", "2s"); code != 0 {
		t.Fatalf("setup: exit %d: %s", code, errOut)
	}
	r := decodeOut(t, out)
	if r["platform"] != "systemd" || len(r["changed"].([]any)) != 2 || r["api"] != "http://127.0.0.1:8042" {
		t.Fatalf("setup report: %v", r)
	}
	entry := r["mcp"].(map[string]any)["mcpServers"].(map[string]any)["xmustard"].(map[string]any)
	if u := entry["url"].(string); !strings.HasPrefix(u, "http://127.0.0.1:8042/mcp?") || !strings.Contains(u, "workspace=") || !strings.Contains(u, "client=claude-code") {
		t.Fatalf("client binding url = %s", u)
	}
	if h := entry["headers"].(map[string]any); h["Authorization"] != "Bearer ${XMUSTARD_API_TOKEN}" {
		t.Fatalf("the binding must name the token variable, never a token: %v", h)
	}
	svc.take()

	if code := runLifecycle(e, "daemon", "status"); code != 0 {
		t.Fatalf("status: exit %d: %s", code, errOut)
	}
	if st := decodeOut(t, out); st["service"].(map[string]any)["pid"] != float64(77) || st["health"] == nil || len(st["units"].([]any)) != 2 {
		t.Fatalf("status: %v", st)
	}
	for _, act := range []string{"stop", "restart"} {
		if code := runLifecycle(e, "daemon", act, "--wait", "2s"); code != 0 {
			t.Fatalf("daemon %s: exit %d: %s", act, code, errOut)
		}
		out.Reset()
	}
	if got := svc.take(); !slices.Contains(got, "systemctl --user stop xmustard-api.socket xmustard-api.service") ||
		!slices.Contains(got, "systemctl --user try-restart xmustard-api.service") {
		t.Fatalf("stop and restart ran %v", got)
	}
	if code := runLifecycle(e, "uninstall"); code != 0 {
		t.Fatalf("uninstall: exit %d: %s", code, errOut)
	}
	if u := decodeOut(t, out); len(u["removed"].([]any)) != 2 {
		t.Fatalf("uninstall: %v", u)
	}
	errOut.Reset()
	if code := runLifecycle(e, "daemon", "restart"); code != exitError || !strings.Contains(errOut.String(), "no daemon is installed") {
		t.Fatalf("restart without a unit: exit %d: %s", code, errOut)
	}
}

// store backup runs beside a live daemon; store restore stops the installed daemon,
// needs every connection to the store closed, keeps the replaced store beside it and
// starts the daemon again.
func TestStoreBackupCheckAndRestoreCommands(t *testing.T) {
	e, out, errOut, svc, home := lifecycleTest(t, "linux", nil)
	data := filepath.Join(home, "data")
	ws := "ws"
	remember := func(content string) {
		t.Helper()
		if _, err := workspaceops.Remember(data, ws, workspaceops.RememberRequest{ProposeContextRequest: workspaceops.ProposeContextRequest{
			Title: "t", Content: content}}, workspaceops.ContextActor{ID: "author"}); err != nil {
			t.Fatal(err)
		}
	}
	remember("the build uses make")
	if code := runLifecycle(e, "setup", "--data-dir", data, "--wait", "2s"); code != 0 {
		t.Fatalf("setup: exit %d: %s", code, errOut)
	}
	out.Reset()

	if code := runLifecycle(e, "store", "backup", "--data-dir", data); code != 0 {
		t.Fatalf("backup: exit %d: %s", code, errOut)
	}
	b := decodeOut(t, out)["backup"].(map[string]any)
	backup := b["path"].(string)
	if backup != filepath.Join(data, "backups", "governance-20260928T120000Z.db") || b["entries"] != float64(1) {
		t.Fatalf("backup: %v", b)
	}
	if code := runLifecycle(e, "store", "check", "--file", backup); code != 0 || decodeOut(t, out)["ok"] != true {
		t.Fatalf("check: exit %d: %s", code, errOut)
	}
	remember("written after the backup")
	svc.take()

	// this process still holds the store open, like a second API or an ops command
	if code := runLifecycle(e, "store", "restore", backup, "--data-dir", data); code != exitError || !strings.Contains(errOut.String(), "open in another process") {
		t.Fatalf("restore under an open store: exit %d: %s", code, errOut)
	}
	if got := svc.take(); !slices.Contains(got, "systemctl --user stop xmustard-api.socket xmustard-api.service") ||
		got[len(got)-1] != "systemctl --user try-restart xmustard-api.service" {
		t.Fatalf("a refused restore must stop the daemon and start it again: %v", got)
	}
	if entries, _ := workspaceops.ListContextEntries(data, ws, ""); len(entries) != 2 {
		t.Fatalf("a refused restore changed the store: %d entries", len(entries))
	}
	out.Reset()
	errOut.Reset()

	workspaceops.CloseMemoryStores()
	if code := runLifecycle(e, "store", "restore", backup, "--data-dir", data); code != 0 {
		t.Fatalf("restore: exit %d: %s", code, errOut)
	}
	r := decodeOut(t, out)
	if rep := r["restore"].(map[string]any); len(rep["moved_aside"].([]any)) == 0 || rep["schema_version"] == float64(0) {
		t.Fatalf("restore report: %v", r)
	}
	if r["health"].(map[string]any)["status"] != "ok" {
		t.Fatalf("the daemon did not come back: %v", r)
	}
	if entries, _ := workspaceops.ListContextEntries(data, ws, ""); len(entries) != 1 {
		t.Fatalf("restored store holds %d entries, want the backed-up 1", len(entries))
	}
	if matches, _ := filepath.Glob(filepath.Join(data, "governance.db.pre-restore-*")); len(matches) == 0 {
		t.Fatal("the replaced store was not kept")
	}
}

// A package manager links its versioned binaries into one bin dir. The unit names the
// link, which survives an upgrade that removes the old version, when PATH reaches the
// sibling through it; otherwise the sibling of xmustard-ops, else PATH.
func TestSetupBinaryPrefersAStableLinkToTheSibling(t *testing.T) {
	root := t.TempDir()
	cellar, linked, other := filepath.Join(root, "Cellar", "1.0", "bin"), filepath.Join(root, "bin"), filepath.Join(root, "other")
	for _, d := range []string{cellar, linked, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	api := filepath.Join(cellar, "xmustard-api")
	if err := os.WriteFile(api, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(api, filepath.Join(linked, "xmustard-api")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "xmustard-api"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	lookIn := func(dir string) func(string) (string, error) {
		return func(name string) (string, error) { return filepath.Join(dir, name), nil }
	}
	for _, c := range []struct {
		name, exe string
		look      func(string) (string, error)
		want      string
	}{
		{"link to the sibling", filepath.Join(cellar, "xmustard-ops"), lookIn(linked), filepath.Join(linked, "xmustard-api")},
		{"another install on PATH", filepath.Join(cellar, "xmustard-ops"), lookIn(other), api},
		{"no sibling", filepath.Join(root, "xmustard-ops"), lookIn(other), filepath.Join(other, "xmustard-api")},
		{"neither", filepath.Join(root, "xmustard-ops"), func(string) (string, error) { return "", errors.New("not found") }, ""},
	} {
		e := lifecycleEnv{exe: c.exe, lookPath: c.look}
		if got := e.binary("xmustard-api"); got != c.want {
			t.Errorf("%s: binary = %q, want %q", c.name, got, c.want)
		}
	}
}

// setup --root prints the binding exactly as mcp-config does: the mcpServers JSON, or,
// for a client with its own config syntax (Codex), that text in mcp_config.
func TestSetupClientBindingFollowsMcpConfig(t *testing.T) {
	root, api := t.TempDir(), "http://127.0.0.1:8042"
	for _, c := range []struct {
		client string
		field  func(setupReport) string
		prefix string
	}{
		{"claude-code", func(r setupReport) string { return string(r.MCP) }, `{`},
		{"", func(r setupReport) string { return string(r.MCP) }, `{`},
		{"codex", func(r setupReport) string { return r.MCPConfig }, "[mcp_servers.xmustard]\n"},
	} {
		r, err := clientBinding(root, c.client, api)
		if err != nil {
			t.Fatalf("%q: %v", c.client, err)
		}
		var want bytes.Buffer
		if err := writeMCPConfig(&want, []string{"--root", root, "--api", api, "--client", c.client}); err != nil {
			t.Fatal(err)
		}
		if got := c.field(r); got != want.String() || !strings.HasPrefix(got, c.prefix) || (len(r.MCP) > 0) == (r.MCPConfig != "") {
			t.Errorf("%q: binding %+v, want mcp-config's output:\n%s", c.client, r, want.String())
		}
	}
	if r, err := clientBinding("", "codex", api); err != nil || r.MCP != nil || r.MCPConfig != "" {
		t.Fatalf("no --root: %+v %v", r, err)
	}
}

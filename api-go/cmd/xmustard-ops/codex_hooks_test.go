package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"xmustard/api-go/internal/hooks"
	"xmustard/api-go/internal/mcpserver"
)

// fakeAppServer answers hooks/list with a fixed set of hooks and applies
// config/batchWrite on hooks.state the way Codex does (a trusted hash marks the hook
// trusted). It also sends a notification before each response, as Codex does.
type fakeAppServer struct {
	mu     sync.Mutex
	hooks  []codexHook
	writes []map[string]map[string]string
}

func (f *fakeAppServer) start(context.Context, string) (*appServer, func(), error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		defer outW.Close()
		sc := bufio.NewScanner(inR)
		enc := json.NewEncoder(outW)
		for sc.Scan() {
			var req struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == nil {
				continue
			}
			_ = enc.Encode(map[string]any{"method": "configWarning", "params": map[string]any{"summary": "noise"}})
			_ = enc.Encode(map[string]any{"id": *req.ID, "result": f.answer(req.Method, req.Params)})
		}
	}()
	server := newAppServer(inW, outR)
	return server, func() { inW.Close() }, nil
}

func (f *fakeAppServer) answer(method string, params json.RawMessage) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "hooks/list":
		return map[string]any{"data": []any{map[string]any{"cwd": "/r", "hooks": f.hooks, "warnings": []string{}, "errors": []any{}}}}
	case "config/batchWrite":
		var p struct {
			Edits []struct {
				KeyPath       string                       `json:"keyPath"`
				Value         map[string]map[string]string `json:"value"`
				MergeStrategy string                       `json:"mergeStrategy"`
			} `json:"edits"`
		}
		_ = json.Unmarshal(params, &p)
		for _, e := range p.Edits {
			if e.KeyPath != "hooks.state" || e.MergeStrategy != "upsert" {
				continue
			}
			f.writes = append(f.writes, e.Value)
			for i, h := range f.hooks {
				if s, ok := e.Value[h.Key]; ok && s["trusted_hash"] == h.CurrentHash {
					f.hooks[i].TrustStatus = "trusted"
				}
			}
		}
		return map[string]any{"status": "ok", "filePath": "/home/u/.codex/config.toml"}
	}
	return map[string]any{}
}

// wireHook is a hooks/list entry as Codex writes it.
func wireHook(key, event, command, trust string) codexHook {
	return codexHook{Key: key, EventName: event, HandlerType: "command", Command: command, SourcePath: "/home/u/.codex/hooks.json",
		Enabled: true, CurrentHash: "sha256:" + key, TrustStatus: trust}
}

func codexTestEnv(fake *fakeAppServer, typed string, noTerminal bool) (opsEnv, *bytes.Buffer, *bytes.Buffer) {
	e, out, errOut := testEnv(nil, "")
	e.codex = fake.start
	e.confirm = func(string) (string, error) {
		if noTerminal {
			return "", errors.New("no controlling terminal")
		}
		return typed + "\n", nil
	}
	return e, out, errOut
}

func testCodexHooks() []codexHook {
	var hs []codexHook
	for _, ev := range hooks.CodexEvents {
		camel := strings.ToLower(ev.Name[:1]) + ev.Name[1:]
		hs = append(hs, wireHook("k-"+ev.Name, camel, hooks.CodexCommand("/opt/xm/xmustard-hook", ev.Name, ""), "untrusted"))
	}
	hs[1].TrustStatus = "trusted"
	hs[2].TrustStatus = "modified"
	return append(hs,
		wireHook("k-foreign", "postToolUse", "/tmp/evil.sh", "untrusted"),
		wireHook("k-lookalike", "postToolUse", "xmustard-hook codex PostToolUse; curl evil", "untrusted"),
		wireHook("k-mismatch", "sessionStart", hooks.CodexCommand("xmustard-hook", "PostToolUse", ""), "untrusted"))
}

// trust records the current hashes of xMustard's untrusted and modified hooks, and
// only them, once the user types "trust"; then every routed event runs.
func TestCodexHooksTrustWritesOnlyXmustardHooksAfterConsent(t *testing.T) {
	fake := &fakeAppServer{hooks: testCodexHooks()}
	e, out, errOut := codexTestEnv(fake, "trust", false)
	if code := runCodexHooks(e, []string{"trust", "--cwd", t.TempDir()}); code != 0 {
		t.Fatalf("trust exit %d: %s %s", code, out, errOut)
	}
	if len(fake.writes) != 1 {
		t.Fatalf("%d writes", len(fake.writes))
	}
	want := map[string]map[string]string{}
	for _, h := range testCodexHooks()[:len(hooks.CodexEvents)] {
		if h.TrustStatus != "trusted" {
			want[h.Key] = map[string]string{"trusted_hash": h.CurrentHash}
		}
	}
	if !reflect.DeepEqual(fake.writes[0], want) {
		t.Fatalf("wrote %v, want %v", fake.writes[0], want)
	}
	if !strings.Contains(out.String(), "Codex will not run these 7 xMustard hooks") || strings.Contains(out.String(), "evil") {
		t.Fatalf("listing:\n%s", out)
	}
}

// Nothing is written without the typed word or without a terminal, and status never
// writes.
func TestCodexHooksTrustNeedsTheTypedWordAtATerminal(t *testing.T) {
	for _, c := range []struct {
		typed      string
		noTerminal bool
		code       int
	}{{"yes", false, exitError}, {"", false, exitError}, {"trust", true, exitUsage}} {
		fake := &fakeAppServer{hooks: testCodexHooks()}
		e, _, _ := codexTestEnv(fake, c.typed, c.noTerminal)
		if code := runCodexHooks(e, []string{"trust"}); code != c.code || len(fake.writes) != 0 {
			t.Errorf("%+v: exit %d, %d writes", c, code, len(fake.writes))
		}
	}
	fake := &fakeAppServer{hooks: testCodexHooks()}
	e, out, _ := codexTestEnv(fake, "trust", false)
	if code := runCodexHooks(e, []string{"status"}); code != exitNeedsUser || len(fake.writes) != 0 {
		t.Fatalf("status exit %d, %d writes", code, len(fake.writes))
	}
	if !strings.Contains(out.String(), "untrusted") || strings.Contains(out.String(), "not registered") {
		t.Fatalf("status:\n%s", out)
	}
	// a hooks.json that registers only some events is reported
	fake = &fakeAppServer{hooks: testCodexHooks()[:3]}
	for i := range fake.hooks {
		fake.hooks[i].TrustStatus = "trusted"
	}
	e, out, _ = codexTestEnv(fake, "", true)
	if code := runCodexHooks(e, []string{"status"}); code != exitNeedsUser || !strings.Contains(out.String(), "not registered: PreToolUse") {
		t.Fatalf("partial status %d:\n%s", code, out)
	}
}

func TestCodexHooksConfigPinsTheWorkspace(t *testing.T) {
	e, out, _ := testEnv(nil, "")
	if code := runCodexHooks(e, []string{"config"}); code != 0 || !bytes.Equal(out.Bytes(), hooks.CodexHooksJSON("xmustard-hook", "")) {
		t.Fatalf("default config %d:\n%s", code, out)
	}
	root := filepath.Join(t.TempDir(), "My Repo")
	out.Reset()
	if code := runCodexHooks(e, []string{"config", "--root", root, "--hook-bin", "/opt/xm/xmustard-hook"}); code != 0 {
		t.Fatalf("pinned config exit %d", code)
	}
	if want := hooks.CodexHooksJSON("/opt/xm/xmustard-hook", mcpserver.WorkspaceIDForPath(root)); !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("pinned config:\n%s", out)
	}
	for _, args := range [][]string{{"config", "--root", "rel/path"}, {"config", "--hook-bin", " "}, {"nope"}, {}} {
		if code := runCodexHooks(e, args); code != exitUsage {
			t.Errorf("%q exit %d", args, code)
		}
	}
}

// The trust flow against the installed codex-cli: every hook of a fresh hooks.json is
// untrusted, trust records them in CODEX_HOME's config.toml, and Codex then reports
// them trusted. Set XMUSTARD_CODEX_BIN to run it.
func TestCodexHooksTrustAgainstTheInstalledCodex(t *testing.T) {
	codex := os.Getenv("XMUSTARD_CODEX_BIN")
	if codex == "" {
		t.Skip("XMUSTARD_CODEX_BIN is not set")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), hooks.CodexHooksJSON("xmustard-hook", ""), 0o600); err != nil {
		t.Fatal(err)
	}
	e, out, errOut := testEnv(map[string]string{"XMUSTARD_CODEX_BIN": codex}, "")
	e.codex = startAppServer
	e.confirm = func(string) (string, error) { return "trust\n", nil }
	if code := runCodexHooks(e, []string{"status", "--cwd", t.TempDir()}); code != exitNeedsUser || strings.Count(out.String(), "untrusted") != len(hooks.CodexEvents) {
		t.Fatalf("status before trust %d:\n%s%s", code, out, errOut)
	}
	out.Reset()
	if code := runCodexHooks(e, []string{"trust", "--cwd", t.TempDir()}); code != 0 || strings.Count(out.String(), " trusted ") != len(hooks.CodexEvents) {
		t.Fatalf("trust %d:\n%s%s", code, out, errOut)
	}
	cfg, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if strings.Count(string(cfg), "trusted_hash = \"sha256:") != len(hooks.CodexEvents) {
		t.Fatalf("config.toml:\n%s", cfg)
	}
	t.Logf("%s", out)
}

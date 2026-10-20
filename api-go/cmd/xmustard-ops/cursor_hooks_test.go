package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/hooks"
)

// cursorHooksFile is the shape config prints, read back so the test asserts the
// file's own keys rather than a string.
type cursorHooksFile struct {
	Description string `json:"description"`
	Hooks       map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type       string `json:"type"`
			Command    string `json:"command"`
			FailClosed bool   `json:"failClosed"`
			LoopLimit  int    `json:"loop_limit"`
			Timeout    int    `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// readCursorHooks runs cursor-hooks config and parses what it printed.
func readCursorHooks(t *testing.T, args ...string) cursorHooksFile {
	t.Helper()
	var out, errOut bytes.Buffer
	e := opsEnv{stdout: &out, stderr: &errOut, getenv: func(string) string { return "" }}
	if code := runCursorHooks(e, append([]string{"config"}, args...)); code != 0 {
		t.Fatalf("cursor-hooks config %v: exit %d\n%s", args, code, errOut.String())
	}
	var file cursorHooksFile
	if err := json.Unmarshal(out.Bytes(), &file); err != nil {
		t.Fatalf("cursor-hooks config printed %q, which is not a hooks.json: %v", out.String(), err)
	}
	return file
}

// The file registers every Cursor event xMustard routes, with the matchers the
// row's own fixtures carry and the failure policy the owner decided.
func TestCursorHooksConfigRegistersTheFourEvents(t *testing.T) {
	file := readCursorHooks(t)
	for _, ev := range hooks.CursorEvents {
		groups, ok := file.Hooks[ev.Name]
		if !ok || len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s has no single hook: %+v", ev.Name, file.Hooks[ev.Name])
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" {
			t.Errorf("%s runs a %q hook, not a command", ev.Name, h.Type)
		}
		if h.FailClosed {
			t.Errorf("%s fails closed; the owner's answer is fail open", ev.Name)
		}
		if h.Command != hooks.CursorClient+" "+ev.Name {
			t.Errorf("%s runs %q, want %q", ev.Name, h.Command, hooks.CursorClient+" "+ev.Name)
		}
	}
	if got := file.Hooks["preToolUse"][0].Matcher; got != "Shell" {
		t.Errorf("preToolUse matches %q, want Shell", got)
	}
	if got := file.Hooks["postToolUse"][0].Matcher; got != "MCP:.+" {
		t.Errorf("postToolUse matches %q, want MCP:.+", got)
	}
	if got := file.Hooks["sessionStart"][0].Matcher; got != "" {
		t.Errorf("sessionStart carries the matcher %q; it matches every call", got)
	}
}

// A pin is a workspace id: the daemon's header is a workspace-id channel, and the
// client refuses an id it cannot validate (WS-40c-b r1-2).
func TestCursorHooksConfigPinsWithAWorkspaceID(t *testing.T) {
	file := readCursorHooks(t, "--workspace-id", "ws-cursor")
	for _, ev := range hooks.CursorEvents {
		want := hooks.CursorClient + " " + ev.Name + " --workspace ws-cursor"
		if got := file.Hooks[ev.Name][0].Hooks[0].Command; got != want {
			t.Errorf("%s runs %q, want %q", ev.Name, got, want)
		}
	}
	// A relative root is refused: the pin must name one repository.
	var out, errOut bytes.Buffer
	e := opsEnv{stdout: &out, stderr: &errOut, getenv: func(string) string { return "" }}
	if code := runCursorHooks(e, []string{"config", "--root", "relative/repo"}); code == 0 {
		t.Fatal("a relative root was accepted")
	}
	// An id the daemon would refuse is refused here, before it is written.
	if code := runCursorHooks(e, []string{"config", "--workspace-id", "../escape"}); code == 0 {
		t.Fatal("an unsafe workspace id was accepted")
	}
}

// The client a file runs must be the one xMustard ships, or an absolute path to it:
// a relative path or a NAME=value word would run what the repository holds.
func TestCursorHooksConfigRefusesAClientItCouldNotRun(t *testing.T) {
	var out, errOut bytes.Buffer
	e := opsEnv{stdout: &out, stderr: &errOut, getenv: func(string) string { return "" }}
	for _, bin := range []string{"bin/xmustard-cursor", "xmustard-cursor --evil", "/x/xmustard-cursor\nrm -rf /"} {
		if code := runCursorHooks(e, []string{"config", "--client-bin", bin}); code == 0 {
			t.Errorf("%q was accepted as a client", bin)
		}
	}
	// An absolute path to the client is fine.
	if code := runCursorHooks(e, []string{"config", "--client-bin", "/opt/xmustard/bin/xmustard-cursor"}); code != 0 {
		t.Errorf("an absolute path to the client was refused: %s", errOut.String())
	}
}

// The shipped file is the generated one, so the package and the install agree.
func TestTheShippedCursorHooksFileIsTheGeneratedOne(t *testing.T) {
	path := filepath.Join("..", "..", "..", "integrations", "cursor", "hooks.json")
	shipped, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := hooks.CursorHooksJSON(hooks.CursorClient, "")
	if !bytes.Equal(shipped, want) {
		t.Fatalf("integrations/cursor/hooks.json differs from the generated one:\n--- shipped\n%s\n--- generated\n%s", shipped, want)
	}
	// The shipped file names the four routed events and fails open.
	if !bytes.Contains(shipped, []byte(`"failClosed": false`)) {
		t.Error("the shipped file does not fail open")
	}
	if strings.Count(string(shipped), `"failClosed"`) != len(hooks.CursorEvents) {
		t.Errorf("the shipped file states the failure policy %d times, want %d",
			strings.Count(string(shipped), `"failClosed"`), len(hooks.CursorEvents))
	}
}

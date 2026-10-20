package main

import (
	"context"
	"encoding/json"

	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// cursorClient builds the Cursor hook client from this checkout's rust-core, the
// same way the core bridge test builds the core: the client is the other half of
// this row's wire contract, and a daemon answer the client cannot read is the
// failure WS-40c-b's round 1 found (r1-6). It never skips and never trusts a
// binary that has not just been built from the current sources.
func cursorClient(t *testing.T) string {
	t.Helper()
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Fatalf("cargo is not on PATH: build rust-core or set XMUSTARD_CURSOR_BIN (%v)", err)
	}
	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-30*time.Second))
		defer cancel()
	}
	build := exec.CommandContext(ctx, cargo, "build", "--quiet", "--bin", "xmustard-cursor")
	build.Dir = filepath.Join("..", "..", "..", "rust-core")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cargo build --bin xmustard-cursor: %v\n%s", err, out)
	}
	bin := filepath.Join("..", "..", "..", "rust-core", "target", "debug", "xmustard-cursor")
	if dir := os.Getenv("CARGO_TARGET_DIR"); dir != "" {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join("..", "..", "..", "rust-core", dir)
		}
		bin = filepath.Join(dir, "debug", "xmustard-cursor")
	}
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return bin
}

// The daemon's answer and the client's output are one contract, and only running
// both proves it: the daemon answers in Cursor's schema (WS-40c-c) and the client
// reads the member the event's schema names (WS-40c-b r1-6).
func TestTheCursorClientAndTheDaemonAgreeOnTheWire(t *testing.T) {
	f := newHookFixture(t)
	bin := cursorClient(t)

	event, _ := json.Marshal(map[string]any{
		"hook_event_name": "preToolUse",
		"conversation_id": "cursor-e2e",
		"tool_name":       "Read",
		"tool_input":      map[string]any{"file_path": f.root + "/src/billing.go"},
		"workspace_roots": []string{f.root},
	})
	// The client binds by the roots and the project dir, and pins the workspace by
	// its id: the header is a workspace-id channel (WS-40c-b r1-2).
	run := exec.Command(bin, "preToolUse", "--api", f.srv.URL, "--workspace", f.ws)
	run.Stdin = strings.NewReader(string(event))
	run.Env = append(os.Environ(),
		"XMUSTARD_API_TOKEN="+f.agent,
		"CURSOR_PROJECT_DIR="+f.root,
		"XMUSTARD_WORKSPACE_ID="+f.ws,
	)
	out, err := run.Output()
	if err != nil {
		t.Fatalf("xmustard-cursor preToolUse: %v\n%s", err, out)
	}
	if len(out) == 0 {
		t.Fatal("the client printed nothing: the daemon's answer did not reach it")
	}
	var answer map[string]any
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("the client printed %q, which is not a JSON object: %v", out, err)
	}
	for k := range answer {
		if k != "additional_context" && k != "updated_mcp_tool_output" && k != "updated_input" {
			t.Errorf("the client printed %q, which Cursor does not read", k)
		}
	}
	ctx, ok := answer["additional_context"].(string)
	if !ok || !strings.Contains(ctx, "billing rounding") {
		t.Fatalf("the client's answer carries no memory: %s", out)
	}
}

// An MCP tool's result is replaced, and the replacement is the shaped result's own
// JSON text: the client relays it and does not re-shape it.
func TestTheCursorClientRelaysTheDaemonReplacement(t *testing.T) {
	f := newHookFixture(t)
	bin := cursorClient(t)

	big := strings.Repeat("x", 40000)
	event, _ := json.Marshal(map[string]any{
		"hook_event_name": "postToolUse",
		"conversation_id": "cursor-e2e-2",
		"tool_name":       "MCP:recall",
		"tool_output":     `{"content":"` + big + `"}`,
		"workspace_roots": []string{f.root},
	})
	run := exec.Command(bin, "postToolUse", "--api", f.srv.URL, "--workspace", f.ws)
	run.Stdin = strings.NewReader(string(event))
	run.Env = append(os.Environ(),
		"XMUSTARD_API_TOKEN="+f.agent,
		"CURSOR_PROJECT_DIR="+f.root,
		"XMUSTARD_WORKSPACE_ID="+f.ws,
	)
	out, err := run.Output()
	if err != nil {
		t.Fatalf("xmustard-cursor postToolUse: %v\n%s", err, out)
	}
	var answer map[string]any
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("the client printed %q, which is not a JSON object: %v", out, err)
	}
	replaced, ok := answer["updated_mcp_tool_output"].(string)
	if !ok {
		t.Fatalf("the client printed no updated_mcp_tool_output: %s", out)
	}
	if len(replaced) >= len(big) {
		t.Errorf("the replacement carries %d characters, the un-reduced output %d", len(replaced), len(big))
	}
}

// A pin the daemon would refuse never reaches it: the client refuses it first, and
// the refusal is the fail-open policy's own (a non-zero exit that is not Cursor's
// blocking code 2), so the user's tool call still proceeds.
func TestTheCursorClientRefusesAnUnvalidatedPin(t *testing.T) {
	f := newHookFixture(t)
	bin := cursorClient(t)

	event, _ := json.Marshal(map[string]any{
		"hook_event_name": "sessionStart",
		"conversation_id": "cursor-e2e-3",
		"workspace_roots": []string{f.root},
	})
	run := exec.Command(bin, "sessionStart", "--api", f.srv.URL, "--workspace", "../escape")
	run.Stdin = strings.NewReader(string(event))
	run.Env = append(os.Environ(),
		"XMUSTARD_API_TOKEN="+f.agent,
		"CURSOR_PROJECT_DIR="+f.root,
		"XMUSTARD_WORKSPACE_ID="+f.ws,
	)
	out, err := run.Output()
	if err == nil {
		t.Fatalf("the client accepted a pin the daemon would refuse: %s", out)
	}
	if code := err.(*exec.ExitError).ExitCode(); code == 2 {
		t.Fatalf("the client exited 2, which is Cursor's blocking code: %d", code)
	}
	if len(out) != 0 {
		t.Fatalf("the client printed %q for a refused pin", out)
	}
}

// The roots and the project dir must agree, whatever id is configured: a
// disagreement sends nothing at all, and the user keeps their own output.
func TestTheCursorClientSendsNothingWhenTheRootsDisagree(t *testing.T) {
	f := newHookFixture(t)
	bin := cursorClient(t)

	event, _ := json.Marshal(map[string]any{
		"hook_event_name": "sessionStart",
		"conversation_id": "cursor-e2e-4",
		"workspace_roots": []string{f.root},
	})
	run := exec.Command(bin, "sessionStart", "--api", f.srv.URL, "--workspace", f.ws)
	run.Stdin = strings.NewReader(string(event))
	run.Env = append(os.Environ(),
		"XMUSTARD_API_TOKEN="+f.agent,
		"CURSOR_PROJECT_DIR=/somewhere/else",
		"XMUSTARD_WORKSPACE_ID="+f.ws,
	)
	out, err := run.Output()
	if err != nil {
		t.Fatalf("the client failed on a disagreement: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Fatalf("the client printed %q for a disagreement", out)
	}
}

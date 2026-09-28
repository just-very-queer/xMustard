package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/workspaceops"
)

// relayBinary finds the native stdio relay: XMUSTARD_RELAY_BIN, else the rust-core
// build output. Tests that need it skip when it has not been built.
func relayBinary(t *testing.T) string {
	t.Helper()
	candidates := []string{os.Getenv("XMUSTARD_RELAY_BIN")}
	for _, profile := range []string{"release", "debug"} {
		candidates = append(candidates, filepath.Join("..", "..", "..", "rust-core", "target", profile, "xmustard-relay"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	t.Skip("xmustard-relay is not built (cargo build --release --bin xmustard-relay)")
	return ""
}

// relayPeer drives a relay process over its stdin/stdout.
type relayPeer struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
}

func (p *relayPeer) send(msg map[string]any) {
	p.t.Helper()
	b, _ := json.Marshal(msg)
	if _, err := p.in.Write(append(b, '\n')); err != nil {
		p.t.Fatal(err)
	}
}

// reply reads stdout until the response to id (skipping notifications); a nil id
// matches an error answered with a null id.
func (p *relayPeer) reply(id any) map[string]any {
	p.t.Helper()
	for p.out.Scan() {
		var m map[string]any
		if err := json.Unmarshal(p.out.Bytes(), &m); err != nil {
			p.t.Fatalf("relay wrote a non-JSON line %q", p.out.Text())
		}
		if fmt.Sprint(m["id"]) == fmt.Sprint(id) {
			return m
		}
	}
	p.t.Fatalf("relay closed stdout before answering %d: %v", id, p.out.Err())
	return nil
}

// The native relay carries a stdio client through the API's /mcp endpoint: the same
// tools, the caller's principal on writes, and the workspace binding from
// XMUSTARD_WORKSPACE_ID. Its RSS is logged (the ≤3 MiB target is recorded in
// docs/benchmarks).
func TestNativeRelayAgainstAPI(t *testing.T) {
	srv, dir, tokens := mcpServer(t, exposurePosture{}, map[string]string{"alice": "agent"})
	p, cmd, in := startRelay(t, srv.URL, tokens["alice"], "XMUSTARD_WORKSPACE_ID=ws")
	p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}})
	if r := p.reply(1); r["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %v", r)
	}
	p.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	p.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if tools := p.reply(2)["result"].(map[string]any)["tools"].([]any); len(tools) != 9 {
		t.Fatalf("tools/list: %d tools", len(tools))
	}
	p.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "remember", "arguments": map[string]any{"content": "relayed fact"}}})
	res := p.reply(3)["result"].(map[string]any)
	ws := res["_meta"].(map[string]any)["xmustard/workspace"].(map[string]any)
	if res["isError"] == true || ws["workspace_id"] != "ws" || ws["source"] != "binding" {
		t.Fatalf("remember via relay: %v", res)
	}
	entries, _ := workspaceops.ListContextEntries(dir, "ws", "")
	if len(entries) != 1 || entries[0].Source != "alice" {
		t.Fatalf("memory not attributed to the relay's principal: %+v", entries)
	}
	if rss := processRSSKiB(cmd.Process.Pid); rss > 0 {
		t.Logf("xmustard-relay RSS after initialize, tools/list and a tool call: %d KiB", rss)
	}
	_ = in.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("relay did not exit at end of input")
	}
}

func startRelay(t *testing.T, base, token string, env ...string) (*relayPeer, *exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(relayBinary(t), "--client", "claude-code")
	cmd.Env = append(append(os.Environ(), "XMUSTARD_MCP_URL="+base+"/mcp", "XMUSTARD_API_TOKEN="+token, "XMUSTARD_WORKSPACE_ID="), env...)
	in, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	return &relayPeer{t: t, in: in, out: sc}, cmd, in
}

// roots/list conformance through the relay: with no binding, the API asks the client
// for its roots on the tool call's event stream, the relay passes the request out and
// the client's answer back, and the call resolves the workspace from the root.
func TestNativeRelayRootsList(t *testing.T) {
	srv, dir, tokens := mcpServer(t, exposurePosture{}, map[string]string{"alice": "agent"})
	var snap struct {
		Workspace struct {
			Root string `json:"root_path"`
		} `json:"workspace"`
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "workspaces", "ws", "snapshot.json"))
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	list, _ := json.Marshal([]map[string]any{{"workspace_id": "ws", "name": "ws", "root_path": snap.Workspace.Root}})
	if err := os.WriteFile(filepath.Join(dir, "workspaces.json"), list, 0o644); err != nil {
		t.Fatal(err)
	}
	p, _, _ := startRelay(t, srv.URL, tokens["alice"])
	p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{"roots": map[string]any{"listChanged": true}}, "clientInfo": map[string]any{"name": "t", "version": "1"}}})
	p.reply(1)
	p.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	p.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "recall", "arguments": map[string]any{}}})
	asked := false
	for p.out.Scan() {
		var m map[string]any
		if err := json.Unmarshal(p.out.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		if m["method"] == "roots/list" {
			asked = true
			p.send(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{
				"roots": []map[string]any{{"uri": "file://" + snap.Workspace.Root, "name": "ws"}}}})
			continue
		}
		if m["id"] != float64(2) {
			continue
		}
		res := m["result"].(map[string]any)
		meta, _ := res["_meta"].(map[string]any)
		ws, _ := meta["xmustard/workspace"].(map[string]any)
		if !asked || res["isError"] == true || ws["workspace_id"] != "ws" || ws["source"] != "roots" {
			t.Fatalf("asked=%v %v", asked, res)
		}
		return
	}
	t.Fatal("no answer to the tool call")
}

// The stdio shim's protocol cases (cmd/xmustard-mcp main_test.go and framing), run
// end to end through the relay and the API's /mcp endpoint: a relayed client sees the
// same answers as a shim client.
func TestNativeRelayShimProtocolCases(t *testing.T) {
	srv, _, tokens := mcpServer(t, exposurePosture{}, map[string]string{"alice": "agent"})
	p, _, _ := startRelay(t, srv.URL, tokens["alice"], "XMUSTARD_WORKSPACE_ID=ws")
	p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}})
	if r := p.reply(1)["result"].(map[string]any); r["protocolVersion"] != "2025-06-18" || r["instructions"] == nil {
		t.Fatalf("initialize: %v", r)
	}
	p.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	errCode := func(code float64) func(map[string]any) bool {
		return func(m map[string]any) bool { e, _ := m["error"].(map[string]any); return e != nil && e["code"] == code }
	}
	isError := func(want bool) func(map[string]any) bool {
		return func(m map[string]any) bool {
			r, _ := m["result"].(map[string]any)
			return r != nil && (r["isError"] == true) == want
		}
	}
	cases := []struct {
		name string
		id   any
		line string
		ok   func(map[string]any) bool
	}{
		{"unknown method", 2, `{"jsonrpc":"2.0","id":2,"method":"bogus/method"}`, errCode(-32601)},
		{"stray params field", 3, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall","arguments":{},"extra":1}}`, errCode(-32602)},
		{"_meta accepted", 4, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"recall","arguments":{},"_meta":{"progressToken":"p"}}}`, isError(false)},
		{"malformed params", 5, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":"x"}`, errCode(-32602)},
		{"unknown tool is a protocol error", 6, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"nope","arguments":{}}}`, errCode(-32602)},
		{"unknown argument is a result", 8, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"recall","arguments":{"bogus":1}}}`, isError(true)},
		{"oversized frame", nil, strings.Repeat("a", 8<<20+1024), errCode(-32600)},
		{"ping after an oversized frame", 7, `{"jsonrpc":"2.0","id":7,"method":"ping"}`, func(m map[string]any) bool { return m["result"] != nil }},
	}
	for _, c := range cases {
		if _, err := p.in.Write([]byte(c.line + "\n")); err != nil {
			t.Fatal(err)
		}
		if m := p.reply(c.id); !c.ok(m) {
			t.Errorf("%s: %v", c.name, m)
		}
	}
}

// processRSSKiB reads a process's resident set size with ps (0 when unavailable).
func processRSSKiB(pid int) int {
	out, err := exec.Command("ps", "-o", "rss=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

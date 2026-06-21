package main

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
)

// The MCP surface is deliberately small (see docs/RETHINK.md): governed memory +
// grounding + narrow retrieval, not a sprawling platform.
func TestToolsListIsSharpSurface(t *testing.T) {
	res := toolsListResult()
	list, _ := res["tools"].([]map[string]any)
	if len(list) != 9 {
		t.Fatalf("expected a sharp 9-tool surface, got %d", len(list))
	}
	want := map[string]bool{
		"ground": true, "recall": true, "remember": true, "verify": true,
		"search": true, "explain": true, "impact": true, "diagnostics": true, "why_failed": true,
	}
	for _, tl := range list {
		delete(want, tl["name"].(string))
		if tl["inputSchema"] == nil {
			t.Fatalf("tool %v missing inputSchema", tl["name"])
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

func TestDispatchInitialize(t *testing.T) {
	res, err := dispatch("initialize", nil)
	if err != nil {
		t.Fatalf("initialize error: %v", err)
	}
	m := res.(map[string]any)
	if m["protocolVersion"] != protocolVersion {
		t.Fatalf("bad protocol version: %v", m["protocolVersion"])
	}
}

func TestCallToolMissingArg(t *testing.T) {
	r := callTool("explain", map[string]string{"workspace_id": "ws"}) // missing path
	if r["isError"] != true {
		t.Fatalf("expected isError for missing arg, got %v", r)
	}
}

func TestUnknownMethod(t *testing.T) {
	_, err := dispatch("bogus/method", nil)
	if err == nil || err.Code != -32601 {
		t.Fatalf("expected method-not-found, got %v", err)
	}
}

func TestToolURLBuilding(t *testing.T) {
	tl, ok := toolByName("explain")
	if !ok {
		t.Fatal("explain missing")
	}
	method, path, body := tl.Build(map[string]string{"workspace_id": "ws1", "path": "src/a.go"})
	if method != "GET" || path != "/api/workspaces/ws1/explain-path?path=src%2Fa.go" || body != "" {
		t.Fatalf("bad url: %s %s %q", method, path, body)
	}
}

// remember must ship the memory content in a JSON BODY, never the URL query, so
// durable text isn't leaked into access logs / error strings (XM-NEW-018).
func TestRememberSendsContentInBody(t *testing.T) {
	tl, ok := toolByName("remember")
	if !ok {
		t.Fatal("remember missing")
	}
	method, path, body := tl.Build(map[string]string{
		"workspace_id": "ws1",
		"content":      "watch out: line one\nline\ttwo with \"quotes\" & <html>",
		"title":        "gotcha",
		"paths":        "a.go, b.go ,, c.go",
	})
	if method != "POST" {
		t.Fatalf("expected POST, got %s", method)
	}
	if path != "/api/workspaces/ws1/context" {
		t.Fatalf("content must not be in the URL; got path %q", path)
	}
	if strings.Contains(path, "content") || strings.Contains(path, "watch") {
		t.Fatalf("memory content leaked into the URL: %q", path)
	}
	var got struct {
		Content string   `json:"content"`
		Title   string   `json:"title"`
		Paths   []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body is not valid JSON (%v): %q", err, body)
	}
	if got.Content != "watch out: line one\nline\ttwo with \"quotes\" & <html>" {
		t.Fatalf("special-character content round-trip failed: %q", got.Content)
	}
	if got.Title != "gotcha" {
		t.Fatalf("title not in body: %q", got.Title)
	}
	if len(got.Paths) != 3 || got.Paths[0] != "a.go" || got.Paths[2] != "c.go" {
		t.Fatalf("paths not split/trimmed into body: %#v", got.Paths)
	}
}

func TestBuildArgsRejectsUnknownArgument(t *testing.T) {
	tl, _ := toolByName("recall")
	_, rerr := buildArgs(tl, map[string]any{"workspace_id": "ws", "bogus": "x"})
	if rerr == nil || rerr.Code != -32602 {
		t.Fatalf("expected -32602 for unknown argument, got %v", rerr)
	}
}

func TestBuildArgsRejectsNonScalar(t *testing.T) {
	tl, _ := toolByName("recall")
	_, rerr := buildArgs(tl, map[string]any{"workspace_id": map[string]any{"nested": 1}})
	if rerr == nil || rerr.Code != -32602 {
		t.Fatalf("expected -32602 for object-valued arg, got %v", rerr)
	}
	_, rerr = buildArgs(tl, map[string]any{"workspace_id": "ws", "query": []any{"a", "b"}})
	if rerr == nil || rerr.Code != -32602 {
		t.Fatalf("expected -32602 for array-valued arg, got %v", rerr)
	}
}

func TestBuildArgsBooleanCoercion(t *testing.T) {
	tl, _ := toolByName("verify")
	args, rerr := buildArgs(tl, map[string]any{"workspace_id": "ws", "entry_id": "ctx_1", "approve": false})
	if rerr != nil {
		t.Fatalf("unexpected error: %v", rerr)
	}
	if args["approve"] != "false" {
		t.Fatalf("boolean false should coerce to \"false\", got %q", args["approve"])
	}
	// a boolean passed as a string is a type error, not silently accepted
	_, rerr = buildArgs(tl, map[string]any{"workspace_id": "ws", "entry_id": "ctx_1", "approve": "false"})
	if rerr == nil || rerr.Code != -32602 {
		t.Fatalf("expected -32602 for string-typed boolean, got %v", rerr)
	}
}

func TestBuildArgsEnforcesEnum(t *testing.T) {
	tl, _ := toolByName("search")
	if _, rerr := buildArgs(tl, map[string]any{"workspace_id": "ws", "query": "x", "mode": "pattern"}); rerr != nil {
		t.Fatalf("valid enum value rejected: %v", rerr)
	}
	_, rerr := buildArgs(tl, map[string]any{"workspace_id": "ws", "query": "x", "mode": "bogus"})
	if rerr == nil || rerr.Code != -32602 {
		t.Fatalf("expected -32602 for out-of-enum mode, got %v", rerr)
	}
}

func TestDispatchToolsCallRejectsStrayField(t *testing.T) {
	// DisallowUnknownFields: a stray top-level field is malformed params, not ignored.
	_, rerr := dispatch("tools/call", json.RawMessage(`{"name":"recall","arguments":{"workspace_id":"ws"},"extra":1}`))
	if rerr == nil || rerr.Code != -32602 {
		t.Fatalf("expected -32602 for stray top-level field, got %v", rerr)
	}
}

func TestDispatchToolsCallMalformedParams(t *testing.T) {
	_, rerr := dispatch("tools/call", json.RawMessage(`{"name":`))
	if rerr == nil || rerr.Code != -32602 {
		t.Fatalf("expected -32602 for malformed params, got %v", rerr)
	}
}

func TestDispatchToolsCallUnknownToolIsResult(t *testing.T) {
	// Unknown tool is a recoverable tool result (isError), not a protocol error.
	res, rerr := dispatch("tools/call", json.RawMessage(`{"name":"nope","arguments":{}}`))
	if rerr != nil {
		t.Fatalf("unknown tool should not be a JSON-RPC error, got %v", rerr)
	}
	if m, _ := res.(map[string]any); m["isError"] != true {
		t.Fatalf("expected isError tool result for unknown tool, got %v", res)
	}
}

// A frame larger than maxMessageBytes is drained to the newline and reported
// truncated, never accumulated unboundedly (XM-NEW-020).
func TestReadBoundedLineTruncatesOversized(t *testing.T) {
	huge := strings.Repeat("a", maxMessageBytes+1024) + "\n" + "{\"ok\":1}\n"
	r := bufio.NewReaderSize(strings.NewReader(huge), 64<<10)
	line, truncated, err := readBoundedLine(r)
	if err != nil {
		t.Fatalf("unexpected read error: %v", err)
	}
	if !truncated {
		t.Fatal("expected oversized line to be flagged truncated")
	}
	if len(line) > maxMessageBytes {
		t.Fatalf("retained line %d exceeds cap %d", len(line), maxMessageBytes)
	}
	// the next (small) frame still parses — the stream stayed in sync
	next, truncated2, _ := readBoundedLine(r)
	if truncated2 || strings.TrimSpace(string(next)) != `{"ok":1}` {
		t.Fatalf("follow-up frame corrupted: %q truncated=%v", next, truncated2)
	}
}

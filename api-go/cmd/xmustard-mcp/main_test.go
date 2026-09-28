package main

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"xmustard/api-go/internal/mcpserver"
)

// The shim answers initialize through the shared session: a client naming no version
// keeps the legacy answer, a current client gets its version and the instructions.
func TestDispatchInitialize(t *testing.T) {
	res, err := dispatch("initialize", nil)
	if err != nil {
		t.Fatalf("initialize error: %v", err)
	}
	if m := res.(map[string]any); m["protocolVersion"] != mcpserver.LegacyProtocolVersion {
		t.Fatalf("bad protocol version: %v", m["protocolVersion"])
	}
	res, err = dispatch("initialize", json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`))
	if err != nil {
		t.Fatalf("initialize error: %v", err)
	}
	m := res.(map[string]any)
	if m["protocolVersion"] != "2025-06-18" || m["instructions"] != mcpserver.Instructions {
		t.Fatalf("negotiation/instructions: %v", m)
	}
}

func TestUnknownMethod(t *testing.T) {
	_, err := dispatch("bogus/method", nil)
	if err == nil || err.Code != -32601 {
		t.Fatalf("expected method-not-found, got %v", err)
	}
}

func TestDispatchToolsCallRejectsStrayField(t *testing.T) {
	// DisallowUnknownFields: a stray top-level field is malformed params, not ignored.
	_, rerr := dispatch("tools/call", json.RawMessage(`{"name":"recall","arguments":{"workspace_id":"ws"},"extra":1}`))
	if rerr == nil || rerr.Code != -32602 {
		t.Fatalf("expected -32602 for stray top-level field, got %v", rerr)
	}
}

// Real MCP clients (Codex, Claude Code, opencode) include a spec-standard `_meta`
// field in tools/call params. Strict DisallowUnknownFields must NOT reject it, or
// every compliant client gets -32602 (regression: this shipped in item B).
func TestDispatchToolsCallAcceptsMetaField(t *testing.T) {
	params := json.RawMessage(`{"name":"recall","arguments":{"workspace_id":"ws"},"_meta":{"progressToken":"abc-123"}}`)
	res, rerr := dispatch("tools/call", params)
	if rerr != nil {
		t.Fatalf("_meta must be accepted, got rpc error %v", rerr)
	}
	// It reaches the tool (which errors only because no API server is up here) — the
	// point is it is NOT rejected as an unknown-field param error.
	if m, _ := res.(map[string]any); m == nil {
		t.Fatalf("expected a tool result, got %v", res)
	}
}

func TestDispatchToolsCallMalformedParams(t *testing.T) {
	_, rerr := dispatch("tools/call", json.RawMessage(`{"name":`))
	if rerr == nil || rerr.Code != -32602 {
		t.Fatalf("expected -32602 for malformed params, got %v", rerr)
	}
}

// MCP 2025-11-25: an unknown tool is a protocol error (-32602), while an argument
// the tool rejects is a tool result (isError) the model can correct.
func TestDispatchToolsCallUnknownToolIsProtocolError(t *testing.T) {
	_, rerr := dispatch("tools/call", json.RawMessage(`{"name":"nope","arguments":{}}`))
	if rerr == nil || rerr.Code != -32602 || !strings.Contains(rerr.Message, "Unknown tool: nope") {
		t.Fatalf("expected -32602 for an unknown tool, got %v", rerr)
	}
	res, rerr := dispatch("tools/call", json.RawMessage(`{"name":"impact","arguments":{"bogus":"x"}}`))
	if rerr != nil {
		t.Fatalf("an unknown argument must be a tool result, got JSON-RPC error %v", rerr)
	}
	if m, _ := res.(map[string]any); m["isError"] != true {
		t.Fatalf("expected an isError tool result for an unknown argument, got %v", res)
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

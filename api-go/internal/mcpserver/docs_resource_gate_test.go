package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The promoted-title index is recall(names_only) behind a resource URI. A deployment
// that refuses recall must not serve its titles: the resource read surfaces the API's
// refusal, naming the reason, and never an index — not an empty one, which a caller
// could not tell from a workspace with no promoted memory.
func TestMemoryIndexResourceIsRefusedWhenRecallIsRefused(t *testing.T) {
	for _, reason := range []string{"tool_disabled", "role"} {
		t.Run(reason, func(t *testing.T) {
			api := &fakeAPI{handle: func(r Request) *APIResponse {
				if strings.Contains(r.Path, "/context/active") {
					return &APIResponse{Status: 403, Body: `{"error":"` + reason + `","reason":"` + reason + `"}`}
				}
				return nil
			}}
			s := newSession(t, api, Options{WorkspaceID: "bound"}, nil, LatestProtocolVersion)
			res, rerr := s.Handle(context.Background(), "resources/read", json.RawMessage(`{"uri":"`+MemoryIndexURI+`"}`))
			if res != nil {
				t.Fatalf("a refused recall returned a result: %v", res)
			}
			if rerr == nil || rerr.Code != CodeInternal || !strings.Contains(rerr.Message, reason) {
				t.Fatalf("want a refusal carrying %q, got %v", reason, rerr)
			}
		})
	}
}

// A resource read is a tool call the client did not name, so the read-only mode's gate
// applies to it through the same predicate, and refuses in the same shape: a tool result
// with isError, never a JSON-RPC error. recall serves reads and is served here, as it is
// by name; what this pins is that a resource cannot serve a call the named call refuses,
// and that it refuses the way the named call does.
func TestResourceReadsApplyTheReadOnlyGate(t *testing.T) {
	// the predicate, as the named call applies it
	cases := []struct {
		name string
		tool *Tool
		args map[string]string
		want bool // a named tools/call is refused
	}{
		{"read only", recallTool, map[string]string{}, false},
		{"write args set", whyFailedTool, map[string]string{"command": "go test ./..."}, true},
		{"read args only", whyFailedTool, map[string]string{"run_id": "oc_1"}, false},
	}
	for _, tc := range cases {
		refused := resourceToolRefusal(true, tc.tool, tc.args)
		if (refused != nil) != tc.want {
			t.Fatalf("%s: resource gate refused=%v, want %v", tc.name, refused != nil, tc.want)
		}
		if refused != nil {
			msg := text(refused)
			if refused["isError"] != true || !strings.Contains(msg, "mode="+ModeReadOnly) || !strings.Contains(msg, tc.tool.Name) {
				t.Fatalf("%s: a refusal is a tool result with isError, naming the tool and %s, got %v", tc.name, ModeReadOnly, refused)
			}
		}
		// a full connection serves both
		if resourceToolRefusal(false, tc.tool, tc.args) != nil {
			t.Fatalf("%s refused on a full connection", tc.name)
		}
	}
	// the served surface: a read-only connection serves memory/core, because recall
	// serves reads, and it costs exactly the one projection read it always cost
	api := &fakeAPI{handle: func(r Request) *APIResponse {
		if strings.Contains(r.Path, "/memory/core") {
			return &APIResponse{Status: 200, Body: `{"workspace_id":"bound","text":"[xmustard memory] standing","revision":1}`}
		}
		return nil
	}}
	s := newSession(t, api, Options{WorkspaceID: "bound", ReadOnly: true}, nil, LatestProtocolVersion)
	if _, rerr := s.Handle(context.Background(), "resources/read", json.RawMessage(`{"uri":"`+MemoryCoreURI+`"}`)); rerr != nil {
		t.Fatalf("memory/core refused on a read-only connection that serves recall: %v", rerr)
	}
	if n := len(api.toolRequests()); n != 1 {
		t.Fatalf("memory/core made %d API calls, want the one projection read", n)
	}
}

// The gate is recall's, not a new rule: a full connection serves both memory
// resources, each reading exactly the one API call it read before.
func TestMemoryResourcesAreServedOnAFullConnection(t *testing.T) {
	api := &fakeAPI{handle: func(r Request) *APIResponse {
		switch {
		case strings.Contains(r.Path, "/context/active"):
			return &APIResponse{Status: 200, Body: `{"render":"names","entries":[{"id":"e1","title":"t","state":"served"}]}`}
		case strings.Contains(r.Path, "/memory/core"):
			return &APIResponse{Status: 200, Body: `{"workspace_id":"bound","text":"[xmustard memory] standing","revision":1}`}
		}
		return nil
	}}
	s := newSession(t, api, Options{WorkspaceID: "bound"}, nil, LatestProtocolVersion)
	for _, uri := range []string{MemoryIndexURI, MemoryCoreURI} {
		if _, rerr := s.Handle(context.Background(), "resources/read", json.RawMessage(`{"uri":"`+uri+`"}`)); rerr != nil {
			t.Fatalf("%s refused on a full connection: %v", uri, rerr)
		}
	}
	if n := len(api.toolRequests()); n != 2 {
		t.Fatalf("two resources made %d API calls, want one each", n)
	}
}

// A resource read that a read-only connection owes a refusal gets the same
// client-visible shape a named tools/call gets: a tool result with isError, never a
// JSON-RPC error (MCP 2025-11-25). The shape is asserted on the refusal the resource
// path builds, since memoryIndex and memoryCore each re-wrap it in their own resource
// error; what this pins is that neither re-wrap starts from a protocol error, and that
// the reason travels in the message. No resource is backed by a write tool today, so
// why_failed stands in for one.
func TestResourceRefusalIsAToolResultNotAProtocolError(t *testing.T) {
	refused := resourceToolRefusal(true, whyFailedTool, map[string]string{"command": "go test ./..."})
	if refused == nil {
		t.Fatal("a command on a read-only connection must be refused")
	}
	if _, isRPC := any(refused).(*RPCError); isRPC {
		t.Fatal("a refusal is a tool result, never a protocol error")
	}
	if refused["isError"] != true {
		t.Fatalf("a refusal carries isError, got %v", refused)
	}
	for _, want := range []string{"why_failed", "mode=" + ModeReadOnly, "command"} {
		if !strings.Contains(text(refused), want) {
			t.Errorf("a refusal names %q, got %q", want, text(refused))
		}
	}
	// the unserved-tool arm carries its own reason, not the write-args one
	unserved := resourceToolRefusal(true, rememberTool, map[string]string{"content": "x"})
	if unserved == nil || unserved["isError"] != true || !strings.Contains(text(unserved), "changes shared memory") {
		t.Fatalf("a write tool is refused as changing shared memory, got %v", unserved)
	}
	if n := len(unserved["content"].([]map[string]any)); n != 1 {
		t.Fatalf("a refusal is one text block, got %d", n)
	}
}

// With recall served, the index read is exactly what it was: the same request path,
// the same contents and a _meta carrying the workspace. This is the guard that keeps
// any gate on this route a refusal of a refused read rather than a new denial of a
// working one.
func TestMemoryIndexResourceIsUnchangedWhenRecallIsServed(t *testing.T) {
	api := &fakeAPI{handle: func(r Request) *APIResponse {
		if strings.Contains(r.Path, "/context/active") {
			return &APIResponse{Status: 200, Body: `{"render":"names","entries":[{"id":"e1","title":"t","state":"served"}]}`}
		}
		return nil
	}}
	s := newSession(t, api, Options{WorkspaceID: "bound"}, nil, LatestProtocolVersion)
	res, rerr := s.Handle(context.Background(), "resources/read", json.RawMessage(`{"uri":"`+MemoryIndexURI+`"}`))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if p := api.lastTool(t).Path; p != "/api/workspaces/bound/context/active?limit=50&names_only=true&max_chars=10000" {
		t.Fatalf("index read %s", p)
	}
	c := res.(map[string]any)["contents"].([]map[string]any)[0]
	if c["uri"] != MemoryIndexURI || c["mimeType"] != "application/json" || !strings.Contains(c["text"].(string), `"id":"e1"`) {
		t.Fatalf("index contents: %v", c)
	}
	if meta, _ := res.(map[string]any)["_meta"].(map[string]any); meta == nil || meta["xmustard/workspace"] == nil {
		t.Fatalf("index _meta: %v", res)
	}
}

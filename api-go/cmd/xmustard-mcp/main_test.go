package main

import "testing"

// The MCP surface is deliberately small (see docs/RETHINK.md): governed memory +
// grounding + narrow retrieval, not a sprawling platform.
func TestToolsListIsSharpSurface(t *testing.T) {
	res := toolsListResult()
	list, _ := res["tools"].([]map[string]any)
	if len(list) != 8 {
		t.Fatalf("expected a sharp 8-tool surface, got %d", len(list))
	}
	want := map[string]bool{
		"ground": true, "recall": true, "remember": true, "verify": true,
		"search": true, "explain": true, "impact": true, "diagnostics": true,
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
	method, path := tl.Build(map[string]string{"workspace_id": "ws1", "path": "src/a.go"})
	if method != "GET" || path != "/api/workspaces/ws1/explain-path?path=src%2Fa.go" {
		t.Fatalf("bad url: %s %s", method, path)
	}
}

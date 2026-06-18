package main

import "testing"

func TestToolsListHasFullSurface(t *testing.T) {
	res := toolsListResult()
	list, _ := res["tools"].([]map[string]any)
	if len(list) != 32 {
		t.Fatalf("expected 32 tools, got %d", len(list))
	}
	want := map[string]bool{
		"repo_state": true, "repo_summary": true, "changed_since": true, "drift": true,
		"definitions": true, "diagnostics": true, "impact": true, "run_targets": true,
		"verify_targets": true, "issue_context_packet": true, "recent_failures": true,
		"code_explainer": true, "subsystem_explainer": true,
		"hotspots": true, "blast_radius": true, "symbol_graph": true, "issue_symbol_edges": true,
		"search_repo": true, "wiki": true,
		"session_grounding": true, "subsystems": true, "owners": true, "lineage": true, "pg_search": true,
		"pg_runs": true, "pg_issue_search": true, "lsp_document_symbols": true,
		"context_active": true, "context_propose": true, "context_verify": true, "provider_chat": true,
		"route_model": true,
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
	r := callTool("issue_context_packet", map[string]string{"workspace_id": "ws"}) // missing issue_id
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
	tl, ok := toolByName("code_explainer")
	if !ok {
		t.Fatal("code_explainer missing")
	}
	method, path := tl.Build(map[string]string{"workspace_id": "ws1", "path": "src/a.go"})
	if method != "GET" || path != "/api/workspaces/ws1/explain-path?path=src%2Fa.go" {
		t.Fatalf("bad url: %s %s", method, path)
	}
}

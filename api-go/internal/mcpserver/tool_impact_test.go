package mcpserver

import (
	"strings"
	"testing"
)

// Impact v2 (WS-35): each form forwards its own filters and paging as query
// parameters; a uid folds onto symbol, and the depth alias onto max_depth.
func TestImpactForwardsEachFormsArguments(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	cases := []struct {
		args map[string]any
		path string
	}{
		{map[string]any{"symbol": "Store.Put", "direction": "downstream", "max_depth": 5, "relation_types": []any{"CALLS", "ACCESSES"},
			"min_confidence": 0.9, "include_tests": true, "limit": 10, "offset": 20, "summary_only": true, "timeout_ms": 500},
			"/api/workspaces/ws/changes/since-index?symbol=Store.Put&direction=downstream&relation_types=CALLS%2CACCESSES" +
				"&min_confidence=0.9&include_tests=true&depth=5&limit=10&offset=20&summary_only=true&timeout_ms=500"},
		{map[string]any{"uid": "Method:a.ts:A.b#0"}, "/api/workspaces/ws/changes/since-index?symbol=Method%3Aa.ts%3AA.b%230"},
		{map[string]any{"from": "a", "to": "b", "depth": 12, "min_confidence": 0.5},
			"/api/workspaces/ws/changes/since-index?from=a&to=b&min_confidence=0.5&depth=12"},
		{map[string]any{"scope": "range", "base": "main", "head": "HEAD", "limit": 5},
			"/api/workspaces/ws/changes/since-index?scope=range&base=main&head=HEAD&limit=5"},
		{map[string]any{}, "/api/workspaces/ws/changes/since-index"},
	}
	for _, c := range cases {
		c.args["workspace_id"] = "ws"
		res, rerr := call(t, s, "impact", c.args)
		if rerr != nil || res["isError"] == true {
			t.Fatalf("%v refused: %v %v", c.args, rerr, res)
		}
		if p := api.lastTool(t).Path; p != c.path {
			t.Fatalf("%v forwarded\n %q\nwant\n %q", c.args, p, c.path)
		}
	}
}

// Values outside their declared ranges are rejected before any call, never clamped.
func TestImpactRejectsOutOfRangeArguments(t *testing.T) {
	s := newSession(t, &fakeAPI{}, Options{}, nil, LatestProtocolVersion)
	for _, bad := range []map[string]any{
		{"symbol": "x", "min_confidence": 1.5},
		{"symbol": "x", "max_depth": 33},
		{"symbol": "x", "direction": "sideways"},
		{"symbol": "x", "relation_types": "CALLS,NOPE"},
		{"scope": "everything"},
		{"symbol": "x", "limit": 0},
	} {
		bad["workspace_id"] = "ws"
		if _, rerr := call(t, s, "impact", bad); rerr == nil || rerr.Code != CodeInvalidParams {
			t.Fatalf("%v: want invalid params, got %v", bad, rerr)
		}
	}
}

// The v2 arguments are Advanced: the full profile lists them, with min_confidence a
// number from 0 to 1, and the lean profile does not.
func TestImpactAdvancedArgumentsAreListedOnlyInTheFullProfile(t *testing.T) {
	lean := impactTool.inputSchema(SchemaLean)["properties"].(map[string]any)
	full := impactTool.inputSchema(SchemaFull)["properties"].(map[string]any)
	for _, a := range impactTool.Advanced {
		if _, ok := lean[a.Name]; ok {
			t.Fatalf("lean lists advanced %s", a.Name)
		}
		if _, ok := full[a.Name]; !ok {
			t.Fatalf("full does not list %s", a.Name)
		}
	}
	mc := full["min_confidence"].(map[string]any)
	if mc["type"] != "number" || mc["minimum"] != 0 || mc["maximum"] != 1 {
		t.Fatalf("min_confidence schema: %v", mc)
	}
	if !strings.Contains(toolsDoc(), "scope= reads a diff") {
		t.Fatal("the docs resource does not document impact v2")
	}
}

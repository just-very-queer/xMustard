package mcpserver

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
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

// An argument of another form reaches the API, which refuses the call, so a caller never
// gets a different read than it asked for (a staged-scoped or directed read answered
// as a full blast radius). The fake API applies the route's own check.
func TestImpactRefusesArgumentsOfAnotherForm(t *testing.T) {
	api := &fakeAPI{handle: func(r Request) *APIResponse {
		u, err := url.Parse(r.Path)
		if err != nil || !strings.HasSuffix(u.Path, "/changes/since-index") {
			return nil
		}
		params := map[string]string{}
		for k := range u.Query() {
			params[k] = u.Query().Get(k)
		}
		if _, _, err := workspaceops.CheckImpactRequest(params); err != nil {
			body, _ := json.Marshal(map[string]string{"error": err.Error()})
			return &APIResponse{Status: 400, Body: string(body)}
		}
		return nil
	}}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	for _, c := range []struct {
		args map[string]any
		path string
	}{
		{map[string]any{"symbol": "X", "scope": "staged"}, "?symbol=X&scope=staged"},
		{map[string]any{"from": "a", "to": "b", "direction": "downstream", "limit": 5}, "?from=a&to=b&direction=downstream&limit=5"},
		{map[string]any{"scope": "all", "direction": "downstream"}, "?direction=downstream&scope=all"},
		{map[string]any{"direction": "downstream"}, "?direction=downstream"},
		{map[string]any{"path": "a.go", "direction": "upstream"}, "?path=a.go&direction=upstream"},
	} {
		c.args["workspace_id"] = "ws"
		res, rerr := call(t, s, "impact", c.args)
		if rerr != nil || res["isError"] != true || !strings.Contains(text(res), "does not apply to impact") {
			t.Fatalf("%v: want a refusal, got %v %v", c.args, rerr, res)
		}
		if p := api.lastTool(t).Path; p != "/api/workspaces/ws/changes/since-index"+c.path {
			t.Fatalf("%v forwarded %q", c.args, p)
		}
	}
}

// Build forwards every argument the tool declares.
func TestImpactForwardsEveryDeclaredArgument(t *testing.T) {
	for _, a := range append(append([]Arg{}, impactTool.Args...), impactTool.Advanced...) {
		if a.Name != workspaceArg.Name && !slices.Contains(impactQuery, a.Name) {
			t.Fatalf("Build does not forward %s", a.Name)
		}
	}
}

// Values outside their declared ranges are rejected before any call, never clamped
// (an isError argument error since WS-FIX-05).
func TestImpactRejectsOutOfRangeArguments(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	for _, bad := range []map[string]any{
		{"symbol": "x", "min_confidence": 1.5},
		{"symbol": "x", "max_depth": 33},
		{"symbol": "x", "direction": "sideways"},
		{"symbol": "x", "relation_types": "CALLS,NOPE"},
		{"scope": "everything"},
		{"symbol": "x", "limit": 0},
	} {
		bad["workspace_id"] = "ws"
		argErr(t, s, "impact", bad)
	}
	if reqs := api.toolRequests(); len(reqs) != 0 {
		t.Fatalf("a rejected argument reached the API: %v", reqs)
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

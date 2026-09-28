package mcpserver

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"xmustard/api-go/internal/toolbudget"
)

// search, explain and impact always send max_chars (their default when omitted), their
// new arguments reach the API under their own names, and the lean schema lists none of
// them (WS-36).
func TestBudgetedToolsSendMaxCharsAndTheirArguments(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	for _, c := range []struct {
		tool string
		args map[string]any
		want map[string]string
	}{
		{"search", map[string]any{"q": "x"}, map[string]string{"q": "x", "max_chars": "8000"}},
		{"search", map[string]any{"q": "Store/Capture", "mode": "symbol", "kinds": "Function,-Variable", "depth": 2,
			"include_body": true, "include_info": true, "substring": true, "path_glob": "api-go/**", "max_chars": 3000},
			map[string]string{"q": "Store/Capture", "mode": "symbol", "kinds": "Function,-Variable", "depth": "2", "include_body": "true",
				"include_info": "true", "substring": "true", "path_glob": "api-go/**", "max_chars": "3000"}},
		{"explain", map[string]any{"path": "api-go"}, map[string]string{"path": "api-go", "max_chars": "6000"}},
		{"explain", map[string]any{"path": "api-go", "limit": 5, "cursor": "abc", "depth": 0},
			map[string]string{"path": "api-go", "limit": "5", "cursor": "abc", "depth": "0", "max_chars": "6000"}},
		{"explain", map[string]any{"symbol": "Store/Capture", "chain_depth": 2, "include_content": true},
			map[string]string{"symbol": "Store/Capture", "chain_depth": "2", "include_content": "true", "max_chars": "6000"}},
		{"explain", map[string]any{"uid": "Method:a.go:Store.Capture#1"}, map[string]string{"uid": "Method:a.go:Store.Capture#1", "max_chars": "6000"}},
		{"impact", map[string]any{"symbol": "S", "max_chars": 65536}, map[string]string{"symbol": "S", "max_chars": "65536"}},
		{"impact", map[string]any{}, map[string]string{"max_chars": "8000"}},
	} {
		c.args["workspace_id"] = "ws"
		res, rerr := call(t, s, c.tool, c.args)
		if rerr != nil || res["isError"] != false {
			t.Fatalf("%s %v: %v %v", c.tool, c.args, rerr, res)
		}
		u, err := url.Parse(api.lastTool(t).Path)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range c.want {
			if got := u.Query().Get(k); got != v {
				t.Errorf("%s %v: %s=%q, want %q (%s)", c.tool, c.args, k, got, v, u)
			}
		}
	}
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"search", map[string]any{"q": "x", "max_chars": toolbudget.MinMaxChars - 1}, fmt.Sprintf("between %d and %d", toolbudget.MinMaxChars, toolbudget.MaxMaxChars)},
		{"explain", map[string]any{"path": "a", "limit": toolbudget.MaxLimit + 1}, fmt.Sprintf("between 1 and %d", toolbudget.MaxLimit)},
		{"explain", map[string]any{"symbol": "a", "chain_depth": 4}, "between 0 and 3"},
		{"search", map[string]any{"q": "x", "depth": 4}, "between 0 and 3"},
		{"impact", map[string]any{"max_chars": 70000}, "between"},
	} {
		c.args["workspace_id"] = "ws"
		if msg, _ := argErr(t, s, c.tool, c.args); !strings.Contains(msg, c.want) {
			t.Errorf("%s %v: %q, want %q", c.tool, c.args, msg, c.want)
		}
	}
	for _, name := range toolbudget.Tools() {
		tl, _ := ToolByName(name)
		props := tl.InputSchema()["properties"].(map[string]any)
		for _, a := range tl.Advanced {
			if _, listed := props[a.Name]; listed {
				t.Errorf("%s lists advanced argument %s in the lean schema", name, a.Name)
			}
		}
	}
}

// The docs resource carries each budgeted tool's ladders, generated from the tables,
// and the symbol navigation documentation.
func TestDocsDocumentEveryLadder(t *testing.T) {
	text := toolsDoc()
	for _, name := range toolbudget.Tools() {
		spec, _ := toolbudget.For(name)
		if !strings.Contains(text, spec.Markdown()) {
			t.Errorf("docs miss %s's output budget", name)
		}
	}
	for _, want := range []string{"### mode=symbol", "### Outline and symbol context", "`signal_flags`", "`one_snippet`",
		"`has_more`", "`next_cursor`", "recover_uri"} {
		if !strings.Contains(text, want) {
			t.Errorf("docs must state %q", want)
		}
	}
}

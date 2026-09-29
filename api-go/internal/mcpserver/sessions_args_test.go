package mcpserver

import (
	"net/url"
	"strings"
	"testing"
)

// WS-34: recall(source=sessions, ...) and ground(query=...) are reachable through the MCP
// tools, not only over HTTP: BuildArgs accepts them and Build forwards them.
func TestSessionArgumentsReachTheAPI(t *testing.T) {
	for name, c := range map[string]struct {
		tool *Tool
		raw  map[string]any
		want url.Values
	}{
		"recall sessions": {recallTool, map[string]any{"source": "sessions", "q": "parser", "agent": "codex", "conversation": "codex:c1"},
			url.Values{"source": {"sessions"}, "query": {"parser"}, "agent": {"codex"}, "conversation": {"codex:c1"}}},
		"ground query": {groundTool, map[string]any{"query": "invoice rounding"}, url.Values{"query": {"invoice rounding"}}},
	} {
		args, _, aerr := BuildArgs(c.tool, c.raw)
		if aerr != nil {
			t.Fatalf("%s: %v", name, aerr.Message)
		}
		_, target, _ := c.tool.Build(args)
		u, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range c.want {
			if got := u.Query().Get(k); got != v[0] {
				t.Errorf("%s: %s = %q in %s, want %q", name, k, got, target, v[0])
			}
		}
	}
	for name, c := range map[string]struct {
		tool *Tool
		raw  map[string]any
	}{
		"unknown source": {recallTool, map[string]any{"source": "web"}},
		"long query":     {groundTool, map[string]any{"query": strings.Repeat("q", 513)}},
	} {
		if _, _, aerr := BuildArgs(c.tool, c.raw); aerr == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

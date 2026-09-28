package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// A live agent called impact(path=...) on the June install, got JSON-RPC -32602 with
// no content, and its client showed "Empty response". Every argument error is now a
// tool result (isError) naming what was wrong and what the tool takes (MCP
// 2025-11-25); an unknown tool and a malformed request stay protocol errors.
func TestArgumentErrorsAreToolResultsTheAgentCanRead(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	for _, c := range []struct {
		tool   string
		args   map[string]any
		reason string
		want   []string
	}{
		{"impact", map[string]any{"workspace_id": "ws", "bogus": "x"}, "unknown",
			[]string{`unknown argument "bogus" for tool impact.`, "Arguments of impact: workspace_id, symbol, path, from, to, max_depth (1-4)."}},
		{"impact", map[string]any{"workspace_id": "ws", "symbol": 3}, "must be a string", []string{`"symbol"`, "must be a string"}},
		{"search", map[string]any{"workspace_id": "ws", "q": "x", "mode": "regex"}, "must be one of: hybrid, pattern",
			[]string{"Arguments of search: workspace_id, q (required), mode (hybrid|pattern), lang, seed, limit (1-50).",
				"Also accepted (see " + DocsURI + "): cursor, path_glob."}},
		{"recall", map[string]any{"workspace_id": "ws", "limit": 0}, "must be between", []string{"must be between 1 and 50 (got 0)"}},
		{"search", map[string]any{"workspace_id": "ws"}, "required", []string{`missing required argument "q" for search.`, "q (required)"}},
		{"explain", map[string]any{"workspace_id": "ws", "symbol": "Add"}, "unknown",
			[]string{"Hint: explain takes a file or directory path; for a symbol, search(q=...) finds it and impact(symbol=...) gives its blast radius."}},
	} {
		msg, d := argErr(t, s, c.tool, c.args)
		if !strings.HasPrefix(d["reason"].(string), c.reason) || d["tool"] != c.tool {
			t.Errorf("%s %v: data %v, want reason %q", c.tool, c.args, d, c.reason)
		}
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%s %v: text must contain %q:\n%s", c.tool, c.args, w, msg)
			}
		}
	}
	if n := len(api.toolRequests()); n != 0 {
		t.Fatalf("rejected calls reached the API %d times", n)
	}
	// a hint travels in _meta too
	if _, d := argErr(t, s, "explain", map[string]any{"workspace_id": "ws", "q": "Add"}); d["hint"] == nil {
		t.Fatalf("hint missing from the error data: %v", d)
	}
	// protocol problems stay JSON-RPC errors
	res, rerr := s.Handle(context.Background(), "tools/call", json.RawMessage(`{"name":"nope","arguments":{}}`))
	if rerr == nil || rerr.Code != CodeInvalidParams || !strings.Contains(rerr.Message, "Unknown tool: nope") || !strings.Contains(rerr.Message, "impact") {
		t.Fatalf("unknown tool: want -32602 naming the tools, got %v %v", res, rerr)
	}
	for _, params := range []string{`{"name":"recall","arguments":"x"}`, `{"name":"recall","arguments":{},"extra":1}`, `{"arguments":{}}`} {
		if _, rerr := s.Handle(context.Background(), "tools/call", json.RawMessage(params)); rerr == nil || rerr.Code != CodeInvalidParams {
			t.Fatalf("%s: want -32602, got %v", params, rerr)
		}
	}
}

// Each did-you-mean hint names an argument its tool rejects even after toolcompat's
// repairs, so a hint never shadows an argument the tool has since accepted.
func TestArgHintsNameArgumentsTheToolRejects(t *testing.T) {
	s := newSession(t, &fakeAPI{}, Options{}, nil, LatestProtocolVersion)
	for key, hint := range argHints {
		if _, ok := ToolByName(key.tool); !ok {
			t.Fatalf("hint for unknown tool %s", key.tool)
		}
		_, d := argErr(t, s, key.tool, map[string]any{"workspace_id": "ws", key.token: "x"})
		if d["hint"] != hint || d["argument"] != key.token {
			t.Errorf("%s(%s): data %v, want hint %q", key.tool, key.token, d, hint)
		}
	}
}

// impact(path=) is file-level blast radius: the path travels to the API with the
// depth, an absolute path inside the workspace is made relative, and a path mixed
// with symbol or from/to reaches the API whole so it can refuse the mix.
func TestImpactTakesAFilePath(t *testing.T) {
	root := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "repo-ws", Root: root}}}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"workspace_id": "ws", "path": "api-go/internal/govstore/entries.go"},
			"/api/workspaces/ws/changes/since-index?path=api-go%2Finternal%2Fgovstore%2Fentries.go"},
		{map[string]any{"workspace_id": "ws", "path": "a.go", "max_depth": 2}, "/api/workspaces/ws/changes/since-index?path=a.go&depth=2"},
		{map[string]any{"workspace_id": "ws", "path": "a.go", "symbol": "Add"}, "/api/workspaces/ws/changes/since-index?path=a.go&symbol=Add"},
		{map[string]any{"path": filepath.Join(root, "pkg", "a.go")}, "/api/workspaces/repo-ws/changes/since-index?path=pkg%2Fa.go"},
	} {
		res, rerr := call(t, s, "impact", c.args)
		if rerr != nil || res["isError"] != false {
			t.Fatalf("impact %v: %v %v", c.args, rerr, res)
		}
		if p := api.lastTool(t).Path; p != c.want {
			t.Fatalf("impact %v forwarded %s, want %s", c.args, p, c.want)
		}
	}
	// file_path, the spelling agents also try, is repaired onto path
	res, _ := call(t, s, "impact", map[string]any{"workspace_id": "ws", "file_path": "a.go"})
	if p := api.lastTool(t).Path; res["isError"] != false || p != "/api/workspaces/ws/changes/since-index?path=a.go" {
		t.Fatalf("file_path: %v %s", res, p)
	}
}

// The June install's agents, prompts and skills still send its argument names. Every
// argument it advertised is accepted and reaches the API (testdata/old_install_args.json),
// renamed ones through their hidden alias.
func TestOldInstallArgumentsStillAccepted(t *testing.T) {
	raw, err := os.ReadFile("testdata/old_install_args.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Tools map[string]struct {
			Advertised []string         `json:"advertised"`
			Calls      []map[string]any `json:"calls"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(fixture.Tools)); !slices.Equal(got, slices.Sorted(slices.Values(toolNames()))) {
		t.Fatalf("the fixture covers %v, want every tool", got)
	}
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	for name, tc := range fixture.Tools {
		used := map[string]bool{}
		for _, args := range tc.Calls {
			res, rerr := call(t, s, name, maps.Clone(args))
			if rerr != nil || res["isError"] != false {
				t.Fatalf("%s %v: the June install accepted this call: %v %v", name, args, rerr, res)
			}
			req := api.lastTool(t)
			sent := req.Path + " " + req.Body
			for k, v := range args {
				used[k] = true
				if want := fmt.Sprint(v); !strings.Contains(sent, url.QueryEscape(want)) && !strings.Contains(sent, want) {
					t.Errorf("%s: %s=%v did not reach the API: %s", name, k, v, sent)
				}
			}
		}
		keys := slices.Collect(maps.Keys(used))
		sort.Strings(keys)
		if !slices.Equal(keys, tc.Advertised) {
			t.Errorf("%s: the calls use %v, want every advertised argument %v", name, keys, tc.Advertised)
		}
	}
}

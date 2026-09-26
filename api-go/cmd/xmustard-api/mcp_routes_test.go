package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"xmustard/api-go/internal/mcpserver"
	"xmustard/api-go/internal/workspaceops"
)

// mcpServer runs the full API stack with a seeded workspace and minted tokens.
func mcpServer(t *testing.T, p exposurePosture, tokens map[string]string) (*httptest.Server, string, map[string]string) {
	t.Helper()
	clearProfileEnv(t)
	dir := t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	t.Setenv("XMUSTARD_CORE_BIN", "/nonexistent/xmustard-core")
	t.Setenv("XMUSTARD_AUTH_TOKENS", "")
	seedCoreWorkspace(t, dir, "ws")
	raw := map[string]string{}
	for id, role := range tokens {
		tok, err := workspaceops.MintToken(dir, id, role)
		if err != nil {
			t.Fatal(err)
		}
		raw[id] = tok
	}
	p.Loopback = true
	cfg := serverConfig{host: "127.0.0.1", authMode: "auto", dataDir: dir, posture: p}
	srv := httptest.NewServer(buildHandler(cfg, newAPIHandlerFor(p)))
	t.Cleanup(srv.Close)
	return srv, dir, raw
}

type mcpHTTPClient struct {
	t     *testing.T
	url   string
	token string
	sid   string
	hdr   map[string]string
}

func (c *mcpHTTPClient) post(msg map[string]any) (int, map[string]any) {
	c.t.Helper()
	b, _ := json.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.sid != "" {
		req.Header.Set(mcpserver.HeaderSessionID, c.sid)
	}
	for k, v := range c.hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if id := resp.Header.Get(mcpserver.HeaderSessionID); id != "" {
		c.sid = id
	}
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (c *mcpHTTPClient) initialize() map[string]any {
	c.t.Helper()
	code, out := c.post(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "initialize",
		"params": map[string]any{"protocolVersion": mcpserver.LatestProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}})
	if code != http.StatusOK || c.sid == "" {
		c.t.Fatalf("initialize: %d %v", code, out)
	}
	c.post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return out
}

func (c *mcpHTTPClient) call(id int, tool string, args map[string]any) map[string]any {
	c.t.Helper()
	_, out := c.post(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	res, _ := out["result"].(map[string]any)
	if res == nil {
		c.t.Fatalf("%s: %v", tool, out)
	}
	return res
}

func (c *mcpHTTPClient) toolNames() []string {
	c.t.Helper()
	_, out := c.post(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	var names []string
	for _, tl := range out["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	return names
}

func resultText(res map[string]any) string {
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	s, _ := content[0].(map[string]any)["text"].(string)
	return s
}

// Four agents on four concurrent MCP sessions: every memory is attributed to the
// principal of the connection that proposed it, and a session answers only the
// principal that opened it.
func TestMCPHTTPPrincipalPerConnection(t *testing.T) {
	agents := map[string]string{"alice": "agent", "bob": "agent", "carol": "agent", "dave": "agent"}
	srv, dir, tokens := mcpServer(t, exposurePosture{}, agents)
	var wg sync.WaitGroup
	clients := map[string]*mcpHTTPClient{}
	for id := range agents {
		c := &mcpHTTPClient{t: t, url: srv.URL + "/mcp", token: tokens[id]}
		clients[id] = c
		wg.Add(1)
		go func(id string, c *mcpHTTPClient) {
			defer wg.Done()
			c.initialize()
			if names := c.toolNames(); len(names) != 9 {
				t.Errorf("%s tools/list: %v", id, names)
			}
			res := c.call(2, "remember", map[string]any{"workspace_id": "ws", "content": "fact from " + id})
			if res["isError"] == true {
				t.Errorf("%s remember: %v", id, res)
			}
		}(id, c)
	}
	wg.Wait()
	entries, err := workspaceops.ListContextEntries(dir, "ws", "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		if e.Content != "fact from "+e.Source {
			t.Errorf("entry %q attributed to %q", e.Content, e.Source)
		}
		got = append(got, e.Source)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "alice,bob,carol,dave" {
		t.Fatalf("authors: %v", got)
	}
	// bob presenting alice's session id is refused
	stolen := &mcpHTTPClient{t: t, url: srv.URL + "/mcp", token: tokens["bob"], sid: clients["alice"].sid}
	if code, _ := stolen.post(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"}); code != http.StatusNotFound {
		t.Fatalf("session used by another principal: %d", code)
	}
	// no token: the endpoint is behind auth like every other route
	anon := &mcpHTTPClient{t: t, url: srv.URL + "/mcp"}
	if code, _ := anon.post(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "initialize"}); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated initialize: %d", code)
	}
}

// A reader token sees only the read tools (caller-scoped tools/list), and mode=readonly
// narrows an agent the same way and refuses writes before the API.
func TestMCPHTTPReadOnlyAndCallerScope(t *testing.T) {
	srv, dir, tokens := mcpServer(t, exposurePosture{}, map[string]string{"rita": "readonly", "alice": "agent"})
	reader := &mcpHTTPClient{t: t, url: srv.URL + "/mcp", token: tokens["rita"]}
	reader.initialize()
	for _, n := range reader.toolNames() {
		if n == "remember" || n == "verify" {
			t.Fatalf("reader offered %s", n)
		}
	}
	ro := &mcpHTTPClient{t: t, url: srv.URL + "/mcp?mode=readonly&client=codex", token: tokens["alice"]}
	ro.initialize()
	if names := ro.toolNames(); len(names) != 7 {
		t.Fatalf("readonly tools/list: %v", names)
	}
	res := ro.call(2, "remember", map[string]any{"workspace_id": "ws", "content": "x"})
	if res["isError"] != true || !strings.Contains(resultText(res), "read-only") {
		t.Fatalf("readonly remember: %v", res)
	}
	if entries, _ := workspaceops.ListContextEntries(dir, "ws", ""); len(entries) != 0 {
		t.Fatalf("a read-only session wrote memory: %v", entries)
	}
	// an unknown mode or client is refused at initialize
	for _, q := range []string{"?mode=admin", "?client=netscape", "?schema=huge"} {
		bad := &mcpHTTPClient{t: t, url: srv.URL + "/mcp" + q, token: tokens["alice"]}
		if code, _ := bad.post(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "initialize"}); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, code)
		}
	}
}

// The workspace binding (query or header) resolves calls that name no workspace;
// a binding the token or the deployment cannot use is refused at initialize.
func TestMCPHTTPWorkspaceBinding(t *testing.T) {
	srv, _, tokens := mcpServer(t, exposurePosture{Workspaces: map[string]bool{"ws": true}}, map[string]string{"alice": "agent"})
	for _, c := range []*mcpHTTPClient{
		{t: t, url: srv.URL + "/mcp?workspace=ws", token: tokens["alice"]},
		{t: t, url: srv.URL + "/mcp", token: tokens["alice"], hdr: map[string]string{mcpWorkspaceHeader: "ws"}},
	} {
		c.initialize()
		res := c.call(2, "recall", map[string]any{})
		ws := res["_meta"].(map[string]any)["xmustard/workspace"].(map[string]any)
		if res["isError"] == true || ws["workspace_id"] != "ws" || ws["source"] != mcpserver.SourceBinding {
			t.Fatalf("binding: %v", res)
		}
	}
	for _, q := range []string{"?workspace=other", "?workspace=../etc"} {
		bad := &mcpHTTPClient{t: t, url: srv.URL + "/mcp" + q, token: tokens["alice"]}
		if code, out := bad.post(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "initialize"}); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %v", q, code, out)
		}
	}
}

// Misspelled arguments are normalized through toolcompat, recorded in the result, and
// counted in the endpoint's usage block on /api/health.
func TestMCPHTTPToolcompatAndUsage(t *testing.T) {
	srv, _, tokens := mcpServer(t, exposurePosture{}, map[string]string{"alice": "agent"})
	c := &mcpHTTPClient{t: t, url: srv.URL + "/mcp?client=pi", token: tokens["alice"]}
	c.initialize()
	res := c.call(2, "recall", map[string]any{"workspaceId": "ws"})
	norms, _ := res["_meta"].(map[string]any)["xmustard/normalized"].([]any)
	if res["isError"] == true || len(norms) != 1 || !strings.Contains(fmt.Sprint(norms[0]), "workspaceId") {
		t.Fatalf("normalization not applied or recorded: %v", res)
	}
	code, health := getJSON(t, srv.URL+"/api/health", tokens["alice"])
	usage, _ := health["mcp_usage"].(map[string]any)
	if code != http.StatusOK || usage == nil {
		t.Fatalf("health: %d %v", code, health)
	}
	recall := usage["tools"].(map[string]any)["recall"].(map[string]any)
	pi := usage["clients"].(map[string]any)["pi"].(map[string]any)
	if recall["calls"].(float64) < 1 || recall["normalized_args"].(float64) < 1 || pi["calls"].(float64) < 1 {
		t.Fatalf("usage: %v", usage)
	}
	// unauthenticated health shows no activity
	if _, anon := getJSON(t, srv.URL+"/api/health", ""); anon["mcp_usage"] != nil {
		t.Fatalf("usage leaked to an anonymous caller: %v", anon["mcp_usage"])
	}
}

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/workspaceops"
)

// fakeAPI is an in-memory Backend: it records every request, serves the workspace
// listing and registration routes, and answers tool routes with handle or an echo.
type fakeAPI struct {
	mu         sync.Mutex
	reqs       []Request
	workspaces []registeredWorkspace
	loadStatus int // status answered to POST /api/workspaces/load (0: 200)
	handle     func(r Request) *APIResponse
}

func (f *fakeAPI) Do(_ context.Context, r Request) (*APIResponse, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.mu.Unlock()
	if f.handle != nil {
		if resp := f.handle(r); resp != nil {
			return resp, nil
		}
	}
	switch {
	case r.Method == "GET" && r.Path == "/api/workspaces":
		f.mu.Lock()
		b, _ := json.Marshal(f.workspaces)
		f.mu.Unlock()
		return &APIResponse{Status: 200, Body: string(b)}, nil
	case r.Method == "POST" && r.Path == "/api/workspaces/load":
		if f.loadStatus >= 400 {
			return &APIResponse{Status: f.loadStatus, Body: `{"detail":"Not Found"}`}, nil
		}
		var req struct {
			RootPath string `json:"root_path"`
			AutoScan bool   `json:"auto_scan"`
		}
		_ = json.Unmarshal([]byte(r.Body), &req)
		f.mu.Lock()
		f.workspaces = append(f.workspaces, registeredWorkspace{ID: WorkspaceIDForPath(req.RootPath), Root: req.RootPath})
		f.mu.Unlock()
		return &APIResponse{Status: 200}, nil
	}
	b, _ := json.Marshal(map[string]any{"ok": true, "path": r.Path})
	return &APIResponse{Status: 200, Body: string(b)}, nil
}

// toolRequests returns the recorded requests other than workspace listing/registration.
func (f *fakeAPI) toolRequests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Request
	for _, r := range f.reqs {
		if r.Path != "/api/workspaces" && r.Path != "/api/workspaces/load" {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeAPI) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func (f *fakeAPI) lastTool(t *testing.T) Request {
	t.Helper()
	reqs := f.toolRequests()
	if len(reqs) == 0 {
		t.Fatal("no tool request reached the API")
	}
	return reqs[len(reqs)-1]
}

// rootsClient answers roots/list with fixed roots.
type rootsClient struct {
	roots []string
	calls int
	mu    sync.Mutex
}

func (c *rootsClient) Request(_ context.Context, method string, _ any) (json.RawMessage, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if method != "roots/list" {
		return nil, fmt.Errorf("unexpected %s", method)
	}
	var list []map[string]string
	for _, r := range c.roots {
		list = append(list, map[string]string{"uri": (&url.URL{Scheme: "file", Path: r}).String(), "name": filepath.Base(r)})
	}
	return json.Marshal(map[string]any{"roots": list})
}

func noEnv(string) string { return "" }

// newSession initializes a session at version (with the roots capability when client
// is set) and completes the handshake.
func newSession(t *testing.T, api Backend, opts Options, client ClientRequester, version string) *Session {
	t.Helper()
	opts.Backend = api
	if opts.Getenv == nil {
		opts.Getenv = noEnv
	}
	s := New(opts).NewSession(client)
	caps := map[string]any{}
	if client != nil {
		caps["roots"] = map[string]any{"listChanged": true}
	}
	params, _ := json.Marshal(map[string]any{"protocolVersion": version, "capabilities": caps, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if _, rerr := s.Handle(context.Background(), "initialize", params); rerr != nil {
		t.Fatal(rerr)
	}
	s.Notify("notifications/initialized", nil)
	return s
}

func call(t *testing.T, s *Session, name string, args map[string]any) (map[string]any, *RPCError) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	res, rerr := s.Handle(context.Background(), "tools/call", params)
	if rerr != nil {
		return nil, rerr
	}
	return res.(map[string]any), nil
}

func text(res map[string]any) string {
	content := res["content"].([]map[string]any)
	return content[0]["text"].(string)
}

func allText(res map[string]any) string {
	var parts []string
	for _, c := range res["content"].([]map[string]any) {
		parts = append(parts, c["text"].(string))
	}
	return strings.Join(parts, "\n")
}

// --- tool table and tools/list ---

// The MCP surface is deliberately small (see docs/RETHINK.md): governed memory +
// grounding + narrow retrieval, not a sprawling platform.
func TestToolsListIsSharpSurface(t *testing.T) {
	s := New(Options{Backend: &fakeAPI{}}).NewSession(nil)
	list := s.ToolsList()["tools"].([]map[string]any)
	if len(list) != 9 {
		t.Fatalf("expected a sharp 9-tool surface, got %d", len(list))
	}
	want := map[string]bool{
		"ground": true, "recall": true, "remember": true, "verify": true,
		"search": true, "explain": true, "impact": true, "diagnostics": true, "why_failed": true,
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

const goldenPath = "testdata/tools_list.json"

// goldenToolsList is the tools/list answer to a current (2025-06-18) client, the
// snapshot that integrations/pi/test/unit.test.ts checks its mirror against.
func goldenToolsList() []byte {
	s := New(Options{Backend: &fakeAPI{}}).NewSession(nil)
	s.version = LatestProtocolVersion
	out := s.ToolsList()
	out["protocolVersion"] = LatestProtocolVersion
	b, _ := json.MarshalIndent(out, "", "  ")
	return append(b, '\n')
}

// tools/list snapshot: annotations, outputSchema and closed input schemas for all nine
// tools. Regenerate with XMUSTARD_UPDATE_GOLDEN=1 go test ./internal/mcpserver, then
// update integrations/pi/src/tools.ts until its unit test passes.
func TestToolsListSnapshot(t *testing.T) {
	got := goldenToolsList()
	if os.Getenv("XMUSTARD_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s (regenerate with XMUSTARD_UPDATE_GOLDEN=1): %v", goldenPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("tools/list drifted from %s; regenerate with XMUSTARD_UPDATE_GOLDEN=1 and update the Pi mirror.\ngot:\n%s", goldenPath, got)
	}
	var doc struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	// why_failed reads a run without a write argument, but can run a command: not read-only
	reads := map[string]bool{"ground": true, "recall": true, "search": true, "explain": true, "impact": true, "diagnostics": true}
	for _, tl := range doc.Tools {
		name := tl["name"].(string)
		in := tl["inputSchema"].(map[string]any)
		if in["type"] != "object" || in["additionalProperties"] != false {
			t.Errorf("%s input schema is not a closed object: %v", name, in)
		}
		props := in["properties"].(map[string]any)
		if _, ok := props["workspace_id"]; !ok {
			t.Errorf("%s does not accept workspace_id", name)
		}
		if req, _ := in["required"].([]any); containsAny(req, "workspace_id") {
			t.Errorf("%s still requires workspace_id", name)
		}
		if _, ok := props["query"]; ok {
			t.Errorf("%s advertises an argument named query, which Claude Code drops", name)
		}
		out := tl["outputSchema"].(map[string]any)
		if out["type"] != "object" {
			t.Errorf("%s outputSchema must be an object schema: %v", name, out)
		}
		ann := tl["annotations"].(map[string]any)
		if ann["title"] == "" || ann["openWorldHint"] != false {
			t.Errorf("%s annotations incomplete: %v", name, ann)
		}
		if ann["readOnlyHint"] != reads[name] {
			t.Errorf("%s readOnlyHint = %v, want %v", name, ann["readOnlyHint"], reads[name])
		}
	}
	byName := map[string]map[string]any{}
	for _, tl := range doc.Tools {
		byName[tl["name"].(string)] = tl["annotations"].(map[string]any)
	}
	if a := byName["remember"]; a["destructiveHint"] != false || a["idempotentHint"] != false {
		t.Errorf("remember is additive and not idempotent: %v", a)
	}
	if a := byName["verify"]; a["destructiveHint"] != true || a["idempotentHint"] != true {
		t.Errorf("verify can demote (destructive) and replaces the caller's vote (idempotent): %v", a)
	}
	if a := byName["why_failed"]; a["destructiveHint"] != true || a["idempotentHint"] != false {
		t.Errorf("why_failed runs commands that can change the tree and records a new outcome each run: %v", a)
	}
}

func containsAny(list []any, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Fields a 2024-11-05 client does not know are not sent to it; the input schema is
// the same in every version.
func TestLegacyClientGetsOnlyLegacyFields(t *testing.T) {
	s := newSession(t, &fakeAPI{}, Options{}, nil, "2024-11-05")
	for _, tl := range s.ToolsList()["tools"].([]map[string]any) {
		for _, k := range []string{"annotations", "outputSchema", "_meta", "title"} {
			if _, ok := tl[k]; ok {
				t.Errorf("%s: %s sent to a 2024-11-05 client", tl["name"], k)
			}
		}
		cur, _ := ToolByName(tl["name"].(string))
		if !reflect.DeepEqual(tl["inputSchema"], cur.InputSchema()) {
			t.Errorf("%s input schema differs by version", tl["name"])
		}
	}
	res, rerr := call(t, s, "ground", map[string]any{"workspace_id": "ws"})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if _, ok := res["structuredContent"]; ok {
		t.Fatal("structuredContent sent to a 2024-11-05 client")
	}
}

// The advertised schemas use only keywords every targeted client (Claude, OpenAI/Codex
// function schemas, Gemini-style subsets) accepts.
func TestSchemasUsePortableKeywords(t *testing.T) {
	allowed := map[string]bool{"type": true, "description": true, "properties": true, "required": true,
		"additionalProperties": true, "enum": true, "minimum": true, "maximum": true, "maxLength": true}
	types := map[string]bool{"object": true, "string": true, "boolean": true, "integer": true, "array": true, "number": true}
	var walk func(tool, where string, s map[string]any)
	walk = func(tool, where string, s map[string]any) {
		for k, v := range s {
			if !allowed[k] {
				t.Errorf("%s %s uses non-portable keyword %q", tool, where, k)
			}
			if k == "type" {
				if ts, ok := v.(string); !ok || !types[ts] {
					t.Errorf("%s %s: type %v must be a single portable type", tool, where, v)
				}
			}
		}
		if props, ok := s["properties"].(map[string]any); ok {
			for name, p := range props {
				ps := p.(map[string]any)
				if _, ok := ps["type"]; !ok {
					t.Errorf("%s %s.%s has no type (Codex requires one)", tool, where, name)
				}
				walk(tool, where+"."+name, ps)
			}
		}
	}
	for _, tl := range Tools() {
		walk(tl.Name, "inputSchema", tl.InputSchema())
		walk(tl.Name, "outputSchema", tl.OutputSchema())
		if req, ok := tl.InputSchema()["required"].([]string); ok && len(req) == 0 {
			t.Errorf("%s emits an empty required list", tl.Name)
		}
	}
}

// --- protocol negotiation ---

func TestNegotiateVersion(t *testing.T) {
	cases := map[string]string{
		"2025-06-18": "2025-06-18", // current: echoed
		"2024-11-05": "2024-11-05", // legacy: echoed
		"2025-11-25": "2025-06-18", // newer than we implement: our latest
		"2025-03-26": "2024-11-05", // older, unsupported (batching): newest not newer than it
		"2024-10-07": "2024-11-05",
		"":           "2024-11-05", // no version named: the legacy answer
		"latest":     "2024-11-05",
	}
	for req, want := range cases {
		if got := NegotiateVersion(req); got != want {
			t.Errorf("NegotiateVersion(%q) = %q, want %q", req, got, want)
		}
	}
}

func TestInitializeNegotiatesAndServesInstructions(t *testing.T) {
	for _, v := range []string{"2025-06-18", "2024-11-05"} {
		s := New(Options{Backend: &fakeAPI{}, Resources: nil}).NewSession(nil)
		params := json.RawMessage(`{"protocolVersion":"` + v + `","capabilities":{},"clientInfo":{"name":"c","version":"1"}}`)
		res, rerr := s.Handle(context.Background(), "initialize", params)
		if rerr != nil {
			t.Fatal(rerr)
		}
		m := res.(map[string]any)
		if m["protocolVersion"] != v || s.Version() != v {
			t.Fatalf("%s: negotiated %v / %s", v, m["protocolVersion"], s.Version())
		}
		if m["instructions"] != Instructions {
			t.Fatalf("%s: instructions not served", v)
		}
		caps := m["capabilities"].(map[string]any)
		if _, ok := caps["tools"]; !ok {
			t.Fatalf("tools capability missing: %v", caps)
		}
		// the built-in docs resources are served with or without an evidence provider
		if _, ok := caps["resources"]; !ok {
			t.Fatalf("resources capability missing: %v", caps)
		}
	}
	s := New(Options{Backend: &fakeAPI{}}).NewSession(nil)
	if _, rerr := s.Handle(context.Background(), "initialize", json.RawMessage(`[1,2]`)); rerr == nil || rerr.Code != CodeInvalidParams {
		t.Fatalf("malformed initialize params: want -32602, got %v", rerr)
	}
}

// The instructions carry the workflow order and stay small: they are read once per
// session, but still cost context.
func TestInstructionsStateWorkflowWithinBudget(t *testing.T) {
	const budget = 1400
	if len(Instructions) > budget {
		t.Fatalf("instructions are %d bytes, over the %d-byte budget", len(Instructions), budget)
	}
	last := -1
	for _, step := range []string{"ground", "recall", "search", "impact", "remember", "verify"} {
		i := strings.Index(Instructions, step+" ")
		if i < 0 || i < last {
			t.Fatalf("workflow step %q missing or out of order", step)
		}
		last = i
	}
	// auto-registration writes xMustard's store even from read tools, and a non-admin
	// token registers only under the operator's roots: say so once here
	for _, p := range []string{"not instructions", "leads to confirm", "workspace_id is optional", "registers and indexes it", "XMUSTARD_MCP_AUTO_REGISTER=0", "XMUSTARD_REGISTER_ROOTS", "resources/read", DocsURI} {
		if !strings.Contains(Instructions, p) {
			t.Errorf("instructions must state %q", p)
		}
	}
}

// --- argument validation ---

func TestBuildArgsRejectsUnknownArgument(t *testing.T) {
	tl, _ := ToolByName("recall")
	_, _, rerr := BuildArgs(tl, map[string]any{"workspace_id": "ws", "bogus": "x"})
	if rerr == nil || rerr.Code != CodeInvalidParams {
		t.Fatalf("expected -32602 for unknown argument, got %v", rerr)
	}
}

func TestBuildArgsRejectsNonScalar(t *testing.T) {
	tl, _ := ToolByName("recall")
	_, _, rerr := BuildArgs(tl, map[string]any{"workspace_id": map[string]any{"nested": 1}})
	if rerr == nil || rerr.Code != CodeInvalidParams {
		t.Fatalf("expected -32602 for object-valued arg, got %v", rerr)
	}
	_, _, rerr = BuildArgs(tl, map[string]any{"workspace_id": "ws", "q": []any{"a", "b"}})
	if rerr == nil || rerr.Code != CodeInvalidParams {
		t.Fatalf("expected -32602 for array-valued arg, got %v", rerr)
	}
}

func TestBuildArgsBooleanCoercion(t *testing.T) {
	tl, _ := ToolByName("verify")
	args, _, rerr := BuildArgs(tl, map[string]any{"workspace_id": "ws", "entry_id": "ctx_1", "approve": false})
	if rerr != nil {
		t.Fatalf("unexpected error: %v", rerr)
	}
	if args["approve"] != "false" {
		t.Fatalf("boolean false should coerce to \"false\", got %q", args["approve"])
	}
	// a boolean passed as a string is a type error, not silently accepted
	_, _, rerr = BuildArgs(tl, map[string]any{"workspace_id": "ws", "entry_id": "ctx_1", "approve": "false"})
	if rerr == nil || rerr.Code != CodeInvalidParams {
		t.Fatalf("expected -32602 for string-typed boolean, got %v", rerr)
	}
}

func TestBuildArgsEnforcesEnum(t *testing.T) {
	tl, _ := ToolByName("search")
	if _, _, rerr := BuildArgs(tl, map[string]any{"workspace_id": "ws", "q": "x", "mode": "pattern"}); rerr != nil {
		t.Fatalf("valid enum value rejected: %v", rerr)
	}
	_, _, rerr := BuildArgs(tl, map[string]any{"workspace_id": "ws", "q": "x", "mode": "bogus"})
	if rerr == nil || rerr.Code != CodeInvalidParams {
		t.Fatalf("expected -32602 for out-of-enum mode, got %v", rerr)
	}
}

// Out-of-range bounds are rejected with -32602 naming the range, never clamped; the
// in-range edge values reach the API unchanged.
func TestOutOfRangeArgumentsAreRejectedNotClamped(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, "2025-06-18")
	bad := []struct {
		tool string
		args map[string]any
	}{
		{"recall", map[string]any{"limit": 0}},
		{"recall", map[string]any{"limit": 51}},
		{"recall", map[string]any{"limit": 2.5}},
		{"recall", map[string]any{"limit": "5"}},
		{"recall", map[string]any{"limit": true}},
		{"search", map[string]any{"q": "x", "limit": 51}},
		{"search", map[string]any{"q": "x", "limit": -1}},
		{"impact", map[string]any{"symbol": "S", "max_depth": 5}},
		{"impact", map[string]any{"symbol": "S", "depth": 0}}, // through the hidden alias too
		{"verify", map[string]any{"entry_id": "e", "note": strings.Repeat("n", maxVerifyNote+1)}},
		{"recall", map[string]any{"session_id": strings.Repeat("s", recallMaxSessionID+1)}},
	}
	for _, c := range bad {
		c.args["workspace_id"] = "ws"
		_, rerr := call(t, s, c.tool, c.args)
		if rerr == nil || rerr.Code != CodeInvalidParams {
			t.Fatalf("%s %v: want -32602, got %v", c.tool, c.args, rerr)
		}
	}
	if n := len(api.toolRequests()); n != 0 {
		t.Fatalf("rejected calls reached the API %d times", n)
	}
	var e *RPCError
	_, e = call(t, s, "recall", map[string]any{"workspace_id": "ws", "limit": 51})
	if d := e.Data.(map[string]any); d["minimum"] != 1 || d["maximum"] != maxRecallLimit || d["argument"] != "limit" {
		t.Fatalf("error data must name the argument and range: %v", e.Data)
	}
	good := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"recall", map[string]any{"limit": 50}, "limit=50"},
		{"recall", map[string]any{"limit": 1}, "limit=1"},
		{"recall", map[string]any{"session_id": strings.Repeat("s", recallMaxSessionID)}, "session_id=" + strings.Repeat("s", recallMaxSessionID)},
		{"search", map[string]any{"q": "x", "limit": 50}, "limit=50"},
		{"impact", map[string]any{"symbol": "S", "max_depth": 1}, "depth=1"},
		{"impact", map[string]any{"symbol": "S", "max_depth": 4}, "depth=4"},
	}
	for _, c := range good {
		c.args["workspace_id"] = "ws"
		if _, rerr := call(t, s, c.tool, c.args); rerr != nil {
			t.Fatalf("%s %v rejected: %v", c.tool, c.args, rerr)
		}
		if p := api.lastTool(t).Path; !strings.Contains(p, c.want) {
			t.Fatalf("%s: %q not forwarded unchanged in %s", c.tool, c.want, p)
		}
	}
	if _, rerr := call(t, s, "verify", map[string]any{"workspace_id": "ws", "entry_id": "e", "note": strings.Repeat("é", maxVerifyNote)}); rerr != nil {
		t.Fatalf("a %d-character note is within bounds: %v", maxVerifyNote, rerr)
	}
}

// The API bounds MCP used to hide are exposed and forwarded.
func TestHiddenAPIBoundsAreExposed(t *testing.T) {
	for tool, arg := range map[string]string{"recall": "limit", "search": "limit", "impact": "max_depth", "verify": "note"} {
		tl, _ := ToolByName(tool)
		if _, ok := tl.InputSchema()["properties"].(map[string]any)[arg]; !ok {
			t.Errorf("%s does not advertise %s", tool, arg)
		}
	}
}

// --- hidden aliases ---

// Claude Code drops a tool argument named exactly "query". search and recall
// advertise q instead and still accept query from clients that send it.
func TestHiddenQueryAliasAcceptedButNotAdvertised(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, "2025-06-18")
	for _, tl := range s.ToolsList()["tools"].([]map[string]any) {
		b, _ := json.Marshal(tl["inputSchema"])
		if strings.Contains(string(b), `"query"`) {
			t.Fatalf("%s advertises query: %s", tl["name"], b)
		}
	}
	// a client that sends the legacy name still works, and the fold is reported
	res, rerr := call(t, s, "search", map[string]any{"workspace_id": "ws", "query": "Handler"})
	if rerr != nil || res["isError"] != false {
		t.Fatalf("query alias rejected: %v %v", rerr, res)
	}
	if p := api.lastTool(t).Path; p != "/api/workspaces/ws/search?q=Handler" {
		t.Fatalf("alias not folded onto q: %s", p)
	}
	norms := res["_meta"].(map[string]any)["xmustard/normalized"].([]Normalization)
	if len(norms) != 1 || norms[0] != (Normalization{Argument: "q", Kind: "alias", From: "query", To: "q"}) {
		t.Fatalf("alias fold not recorded: %v", norms)
	}
	if _, rerr := call(t, s, "recall", map[string]any{"workspace_id": "ws", "query": "auth"}); rerr != nil {
		t.Fatal(rerr)
	}
	if p := api.lastTool(t).Path; p != "/api/workspaces/ws/context/active?query=auth&max_chars=4000" {
		t.Fatalf("recall alias: %s", p)
	}
	// the same value under both names is fine; different values are a conflict
	if _, rerr := call(t, s, "search", map[string]any{"workspace_id": "ws", "q": "a", "query": "a"}); rerr != nil {
		t.Fatalf("agreeing alias rejected: %v", rerr)
	}
	if _, rerr := call(t, s, "search", map[string]any{"workspace_id": "ws", "q": "a", "query": "b"}); rerr == nil || rerr.Code != CodeInvalidParams {
		t.Fatalf("conflicting alias accepted: %v", rerr)
	}
	// impact's API name "depth" folds onto max_depth
	if _, rerr := call(t, s, "impact", map[string]any{"workspace_id": "ws", "symbol": "S", "depth": 2}); rerr != nil {
		t.Fatal(rerr)
	}
	if p := api.lastTool(t).Path; p != "/api/workspaces/ws/changes/since-index?symbol=S&depth=2" {
		t.Fatalf("depth alias: %s", p)
	}
}

// The Claude Code drop case end to end: a client that strips every argument named
// "query" before sending still gets a working search from the advertised schema.
func TestClaudeCodeQueryDropCase(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, "2025-06-18")
	claudeCode := func(args map[string]any) map[string]any {
		delete(args, "query")
		return args
	}
	search, _ := ToolByName("search")
	args := map[string]any{"workspace_id": "ws"}
	for _, r := range search.InputSchema()["required"].([]string) {
		args[r] = "Handler" // what a model fills in from the advertised schema
	}
	res, rerr := call(t, s, "search", claudeCode(args))
	if rerr != nil || res["isError"] != false {
		t.Fatalf("search from the advertised schema failed after the drop: %v %v", rerr, res)
	}
	// had the schema advertised "query", the dropped call would miss its query
	res, _ = call(t, s, "search", claudeCode(map[string]any{"workspace_id": "ws", "query": "Handler"}))
	if res["isError"] != true || !strings.Contains(text(res), `missing required argument "q"`) {
		t.Fatalf("a dropped query must surface as a missing argument, got %v", res)
	}
}

// --- request building ---

func TestToolURLBuilding(t *testing.T) {
	tl, ok := ToolByName("explain")
	if !ok {
		t.Fatal("explain missing")
	}
	method, path, body := tl.Build(map[string]string{"workspace_id": "ws1", "path": "src/a.go"})
	if method != "GET" || path != "/api/workspaces/ws1/explain-path?path=src%2Fa.go" || body != "" {
		t.Fatalf("bad url: %s %s %q", method, path, body)
	}
}

// remember must ship the memory content in a JSON BODY, never the URL query, so
// durable text isn't leaked into access logs / error strings (XM-NEW-018).
func TestRememberSendsContentInBody(t *testing.T) {
	tl, ok := ToolByName("remember")
	if !ok {
		t.Fatal("remember missing")
	}
	method, path, body := tl.Build(map[string]string{
		"workspace_id": "ws1",
		"content":      "watch out: line one\nline\ttwo with \"quotes\" & <html>",
		"title":        "gotcha",
		"paths":        "a.go, b.go ,, c.go",
	})
	if method != "POST" {
		t.Fatalf("expected POST, got %s", method)
	}
	if path != "/api/workspaces/ws1/context" {
		t.Fatalf("content must not be in the URL; got path %q", path)
	}
	var got struct {
		Content string   `json:"content"`
		Title   string   `json:"title"`
		Paths   []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body is not valid JSON (%v): %q", err, body)
	}
	if got.Content != "watch out: line one\nline\ttwo with \"quotes\" & <html>" || got.Title != "gotcha" {
		t.Fatalf("content/title round-trip failed: %+v", got)
	}
	if len(got.Paths) != 3 || got.Paths[0] != "a.go" || got.Paths[2] != "c.go" {
		t.Fatalf("paths not split/trimmed into body: %#v", got.Paths)
	}
}

// verify's note is stored text: it travels in the body; approve stays in the query.
func TestVerifyNoteTravelsInBody(t *testing.T) {
	tl, _ := ToolByName("verify")
	method, path, body := tl.Build(map[string]string{"workspace_id": "w", "entry_id": "e1", "approve": "false", "note": "stale: a.go changed"})
	if method != "POST" || path != "/api/workspaces/w/context/e1/verify?approve=false" {
		t.Fatalf("bad verify call: %s %s", method, path)
	}
	if strings.Contains(path, "stale") || body != `{"note":"stale: a.go changed"}` {
		t.Fatalf("note must be in the body only: %s %q", path, body)
	}
	if _, _, body := tl.Build(map[string]string{"workspace_id": "w", "entry_id": "e1"}); body != "" {
		t.Fatalf("no note, no body: %q", body)
	}
}

func TestCallToolMissingArg(t *testing.T) {
	s := newSession(t, &fakeAPI{}, Options{}, nil, "2025-06-18")
	res, rerr := call(t, s, "explain", map[string]any{"workspace_id": "ws"}) // missing path
	if rerr != nil || res["isError"] != true {
		t.Fatalf("expected isError for missing arg, got %v %v", res, rerr)
	}
}

// --- structured output ---

// structuredContent is the JSON object of the first text block plus the reserved
// _xmustard member; text stays for clients that read only content.
func TestStructuredContentMirrorsText(t *testing.T) {
	api := &fakeAPI{handle: func(r Request) *APIResponse {
		switch {
		case strings.Contains(r.Path, "/session-grounding?"):
			return &APIResponse{Status: 200, Body: `{"workspace_id":"ws","changed_files":3,"summary":"3 changed"}`}
		case strings.Contains(r.Path, "/why-failed"):
			return &APIResponse{Status: 200, Body: `["not","an","object"]`}
		case strings.HasSuffix(r.Path, "/diagnostics"):
			return &APIResponse{Status: 404, Body: `{"error":"Workspace not found"}`}
		}
		return nil
	}}
	s := newSession(t, api, Options{}, nil, "2025-06-18")
	res, rerr := call(t, s, "ground", map[string]any{"workspace_id": "ws"})
	if rerr != nil {
		t.Fatal(rerr)
	}
	sc := decodeStructured(t, res)
	meta := sc[resultMetaMember].(map[string]any)
	delete(sc, resultMetaMember)
	var fromText map[string]any
	if err := json.Unmarshal([]byte(text(res)), &fromText); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sc, fromText) {
		t.Fatalf("structuredContent %v != text %v", sc, fromText)
	}
	if ws := meta["workspace"].(map[string]any); ws["workspace_id"] != "ws" || ws["source"] != SourceArgument {
		t.Fatalf("workspace echo missing: %v", meta)
	}
	// a non-object result is wrapped, never dropped
	res, _ = call(t, s, "why_failed", map[string]any{"workspace_id": "ws", "run_id": "r1"})
	if sc := decodeStructured(t, res); !reflect.DeepEqual(sc["result"], []any{"not", "an", "object"}) {
		t.Fatalf("non-object result not wrapped: %v", sc)
	}
	// errors carry no structured content
	res, _ = call(t, s, "diagnostics", map[string]any{"workspace_id": "ws"})
	if _, ok := res["structuredContent"]; ok || res["isError"] != true {
		t.Fatalf("error result: %v", res)
	}
	// every success validates against its tool's outputSchema (type object)
	for _, tl := range Tools() {
		if tl.OutputSchema()["type"] != "object" {
			t.Fatalf("%s outputSchema", tl.Name)
		}
	}
}

// A reduced result keeps its recovery handle in structuredContent too: a client that
// shows the model only structuredContent (Codex) must still see how to expand it.
func TestStructuredContentCarriesEvidenceHandle(t *testing.T) {
	api := &fakeAPI{handle: func(r Request) *APIResponse {
		return &APIResponse{Status: 200, Header: http.Header{"X-Test-Envelope": {"1"}}, Body: `{"hits":[{"path":"a.go"}]}`}
	}}
	s := newSession(t, api, Options{Delivery: fakeDelivery{}}, nil, "2025-06-18")
	res, rerr := call(t, s, "search", map[string]any{"workspace_id": "ws", "q": "x"})
	if rerr != nil {
		t.Fatal(rerr)
	}
	sc := decodeStructured(t, res)
	ev := sc[resultMetaMember].(map[string]any)["evidence"].(map[string]any)
	if ev["handle"] != "xm1.H" || !strings.Contains(allText(res), "xmustard://evidence/xm1.H") {
		t.Fatalf("evidence handle lost: %v / %s", ev, allText(res))
	}
	delete(sc, resultMetaMember)
	var fromText map[string]any
	_ = json.Unmarshal([]byte(text(res)), &fromText)
	if !reflect.DeepEqual(sc, fromText) {
		t.Fatalf("structured %v != projection %v", sc, fromText)
	}
}

type fakeDelivery struct{}

func (fakeDelivery) Headers(context.Context) map[string]string {
	return map[string]string{"X-Test": "1"}
}
func (fakeDelivery) Result(_ context.Context, resp *APIResponse, ws string) (map[string]any, *RPCError, bool) {
	if resp.Header.Get("X-Test-Envelope") == "" {
		return nil, nil, false
	}
	res := TextResult(resp.Body, false)
	res["content"] = append(res["content"].([]map[string]any), map[string]any{"type": "text", "text": "[xmustard evidence] read xmustard://evidence/xm1.H"})
	res["_meta"] = map[string]any{"xmustard/evidence": map[string]any{"handle": "xm1.H", "resource_uri": "xmustard://evidence/xm1.H?workspace_id=" + ws}}
	return res, nil, true
}

func decodeStructured(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	raw, ok := res["structuredContent"].(json.RawMessage)
	if !ok {
		t.Fatalf("no structuredContent: %v", res)
	}
	var sc map[string]any
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("structuredContent is not a JSON object: %v (%s)", err, raw)
	}
	return sc
}

// Every declared outputSchema member has the declared JSON type in real API results
// (testdata/results, captured from a live API over a git fixture), and the
// structuredContent built from each result is an object with the _xmustard member.
func TestOutputSchemasMatchRecordedResults(t *testing.T) {
	files, err := filepath.Glob("testdata/results/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no recorded results: %v", err)
	}
	covered := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var sample struct {
			Tool   string          `json:"tool"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &sample); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		tl, ok := ToolByName(sample.Tool)
		if !ok {
			t.Fatalf("%s: unknown tool %s", f, sample.Tool)
		}
		covered[tl.Name] = true
		var obj map[string]any
		if err := json.Unmarshal(sample.Result, &obj); err != nil {
			t.Fatalf("%s: result is not an object: %v", f, err)
		}
		for name, want := range tl.Output {
			v, present := obj[name]
			if present && jsonType(v) != want && !(want == "number" && jsonType(v) == "integer") {
				t.Errorf("%s: %s.%s is %s, outputSchema says %s", f, tl.Name, name, jsonType(v), want)
			}
		}
		sc, err := structured(context.Background(), string(sample.Result), map[string]any{"workspace": Workspace{ID: "w", Source: SourceArgument}})
		if err != nil {
			t.Fatal(err)
		}
		var back map[string]any
		if err := json.Unmarshal(sc, &back); err != nil {
			t.Fatalf("%s: structuredContent invalid: %v", f, err)
		}
		if _, ok := back[resultMetaMember].(map[string]any); !ok {
			t.Fatalf("%s: %s member missing", f, resultMetaMember)
		}
		delete(back, resultMetaMember)
		if !reflect.DeepEqual(back, obj) {
			t.Fatalf("%s: structuredContent does not mirror the result", f)
		}
	}
	// diagnostics needs Postgres and why_failed a platform run: no local sample yet
	for _, name := range []string{"ground", "recall", "remember", "verify", "search", "explain", "impact"} {
		if !covered[name] {
			t.Errorf("no recorded result for %s", name)
		}
	}
}

// ground reports a count or flag it cannot determine as null (listed under
// "unknown"), and the portable schema subset has no null type, so outputSchema
// declares only ground members that can never be null: a client that validates
// structuredContent must never see a null where a type was promised.
func TestGroundOutputSchemaDeclaresNoNullableMember(t *testing.T) {
	tl, _ := ToolByName("ground")
	fields := map[string]reflect.Kind{}
	var collect func(rt reflect.Type)
	collect = func(rt reflect.Type) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				collect(f.Type)
				continue
			}
			if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
				fields[name] = f.Type.Kind()
			}
		}
	}
	collect(reflect.TypeOf(workspaceops.SessionGrounding{}))
	for name := range tl.Output {
		switch kind, ok := fields[name]; {
		case !ok:
			t.Errorf("ground outputSchema declares %s, which SessionGrounding does not have", name)
		case kind == reflect.Pointer || kind == reflect.Slice || kind == reflect.Map || kind == reflect.Interface:
			t.Errorf("ground.%s can be null but outputSchema declares it %s", name, tl.Output[name])
		}
	}
}

func jsonType(v any) string {
	switch x := v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if x == float64(int64(x)) {
			return "integer"
		}
		return "number"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

// --- workspace resolution ---

func requiredArgs(tl *Tool) map[string]any {
	args := map[string]any{}
	for _, a := range tl.Args {
		if a.Required {
			args[a.Name] = "x"
		}
	}
	return args
}

// Every tool resolves an omitted workspace_id from XMUSTARD_WORKSPACE_ID and says so,
// echoing the workspace's root like any other source.
func TestEveryToolResolvesWorkspaceFromEnv(t *testing.T) {
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "bound-ws", Root: "/r/bound"}, {ID: "explicit", Root: "/r/explicit"}}}
	env := func(k string) string {
		if k == "XMUSTARD_WORKSPACE_ID" {
			return "bound-ws"
		}
		return ""
	}
	s := newSession(t, api, Options{Getenv: env}, nil, "2025-06-18")
	for _, tl := range Tools() {
		res, rerr := call(t, s, tl.Name, requiredArgs(tl))
		if rerr != nil || res["isError"] != false {
			t.Fatalf("%s: %v %v", tl.Name, rerr, res)
		}
		if p := api.lastTool(t).Path; !strings.HasPrefix(p, "/api/workspaces/bound-ws/") {
			t.Fatalf("%s did not use the env workspace: %s", tl.Name, p)
		}
		ws := res["_meta"].(map[string]any)["xmustard/workspace"].(Workspace)
		if ws.ID != "bound-ws" || ws.Source != SourceEnv || ws.Root != "/r/bound" {
			t.Fatalf("%s: env resolution not echoed with its root: %+v", tl.Name, ws)
		}
		if sc := string(res["structuredContent"].(json.RawMessage)); !strings.Contains(sc, `"workspace":{"workspace_id":"bound-ws","root":"/r/bound","source":"env"}`) {
			t.Fatalf("%s: structuredContent lacks the workspace root: %s", tl.Name, sc)
		}
		// the text echo is paid once per session, not on every call
		if echoed := strings.Contains(allText(res), "[xmustard workspace] bound-ws (/r/bound), resolved from env"); echoed != (tl.Name == "ground") {
			t.Fatalf("%s: text echo = %v (want it on the first call only)", tl.Name, echoed)
		}
	}
	// an explicit argument wins over the binding, adds no echo line, and still reports
	// its root
	res, _ := call(t, s, "ground", map[string]any{"workspace_id": "explicit"})
	if p := api.lastTool(t).Path; p != "/api/workspaces/explicit/session-grounding?max_chars=6000" || strings.Contains(allText(res), "[xmustard workspace]") {
		t.Fatalf("explicit workspace_id: %s / %s", p, allText(res))
	}
	if ws := res["_meta"].(map[string]any)["xmustard/workspace"].(Workspace); ws.Root != "/r/explicit" || ws.Source != SourceArgument {
		t.Fatalf("explicit workspace_id: root not reported: %+v", ws)
	}
	// the listing that supplies roots is read once per session
	if n := api.count("GET", "/api/workspaces"); n != 1 {
		t.Fatalf("listing read %d times", n)
	}
}

func tempRepo(t *testing.T) string {
	t.Helper()
	dir := canonicalPath(t.TempDir())
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Every tool resolves from the client's roots (roots/list, asked once and cached).
func TestEveryToolResolvesWorkspaceFromRoots(t *testing.T) {
	repo := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "other", Root: "/nonexistent/other"}, {ID: "repo-ws", Root: repo}}}
	client := &rootsClient{roots: []string{repo}}
	s := newSession(t, api, Options{Cwd: "/"}, client, "2025-06-18")
	for _, tl := range Tools() {
		res, rerr := call(t, s, tl.Name, requiredArgs(tl))
		if rerr != nil || res["isError"] != false {
			t.Fatalf("%s: %v %v", tl.Name, rerr, res)
		}
		if p := api.lastTool(t).Path; !strings.HasPrefix(p, "/api/workspaces/repo-ws/") {
			t.Fatalf("%s did not use the root's workspace: %s", tl.Name, p)
		}
		ws := res["_meta"].(map[string]any)["xmustard/workspace"].(Workspace)
		if ws.Source != SourceRoots || ws.Root != repo {
			t.Fatalf("%s: %+v", tl.Name, ws)
		}
	}
	if client.calls != 1 {
		t.Fatalf("roots/list asked %d times; it is cached until roots/list_changed", client.calls)
	}
	s.Notify("notifications/roots/list_changed", nil)
	_, _ = call(t, s, "ground", map[string]any{})
	if client.calls != 2 {
		t.Fatalf("roots/list_changed must invalidate the cached roots (calls=%d)", client.calls)
	}
}

// Every tool resolves from the working directory: the registered workspace with the
// longest root containing it.
func TestEveryToolResolvesWorkspaceFromCwd(t *testing.T) {
	repo := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "outer", Root: filepath.Dir(repo)}, {ID: "inner", Root: repo}}}
	s := newSession(t, api, Options{Cwd: filepath.Join(repo, "src", "pkg")}, nil, "2025-06-18")
	for _, tl := range Tools() {
		res, rerr := call(t, s, tl.Name, requiredArgs(tl))
		if rerr != nil || res["isError"] != false {
			t.Fatalf("%s: %v %v", tl.Name, rerr, res)
		}
		if p := api.lastTool(t).Path; !strings.HasPrefix(p, "/api/workspaces/inner/") {
			t.Fatalf("%s: longest containing root not chosen: %s", tl.Name, p)
		}
	}
	ws := func() Workspace {
		res, _ := call(t, s, "ground", map[string]any{})
		return res["_meta"].(map[string]any)["xmustard/workspace"].(Workspace)
	}()
	if ws.Source != SourceCWD || ws.Root != repo {
		t.Fatalf("cwd resolution: %+v", ws)
	}
	if n := api.count("GET", "/api/workspaces"); n != 1 {
		t.Fatalf("the workspace listing was read %d times for one session; it is cached", n)
	}
}

// With no signal the call fails as a tool error that names every signal tried and the
// registered workspaces, so the agent can pass workspace_id.
func TestUnresolvableWorkspaceFailsClearly(t *testing.T) {
	notRepo := canonicalPath(t.TempDir())
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "alpha-1", Root: "/nonexistent/alpha"}}}
	for _, opts := range []Options{{Cwd: notRepo}, {}} {
		s := newSession(t, api, opts, nil, "2025-06-18")
		for _, tl := range Tools() {
			res, rerr := call(t, s, tl.Name, requiredArgs(tl))
			if rerr != nil || res["isError"] != true {
				t.Fatalf("%s: want a tool error, got %v %v", tl.Name, rerr, res)
			}
			msg := text(res)
			for _, want := range []string{"no workspace resolved", "XMUSTARD_WORKSPACE_ID is unset", "the client offers no roots", "Pass workspace_id", "alpha-1 (/nonexistent/alpha)"} {
				if !strings.Contains(msg, want) {
					t.Fatalf("%s: error %q lacks %q", tl.Name, msg, want)
				}
			}
			if opts.Cwd != "" && !strings.Contains(msg, "not inside a registered workspace or a git repository") {
				t.Fatalf("cwd failure reason missing: %s", msg)
			}
		}
	}
	if n := len(api.toolRequests()); n != 0 {
		t.Fatalf("unresolved calls reached tool routes %d times", n)
	}
}

// A git repository in cwd that no workspace contains is registered once (lazily: the
// API scans only when it has no snapshot) and then used.
func TestUnregisteredRepoIsAutoRegistered(t *testing.T) {
	repo := tempRepo(t)
	api := &fakeAPI{}
	s := newSession(t, api, Options{Cwd: filepath.Join(repo, "src"), AutoRegister: true}, nil, "2025-06-18")
	res, rerr := call(t, s, "ground", map[string]any{})
	if rerr != nil || res["isError"] != false {
		t.Fatalf("%v %v", rerr, res)
	}
	want := WorkspaceIDForPath(repo)
	if p := api.lastTool(t).Path; p != "/api/workspaces/"+want+"/session-grounding?max_chars=6000" {
		t.Fatalf("registered workspace not used: %s", p)
	}
	ws := res["_meta"].(map[string]any)["xmustard/workspace"].(Workspace)
	if !ws.Registered || ws.Root != repo || !strings.Contains(allText(res), "registered with the API by this call") {
		t.Fatalf("registration not reported: %+v", ws)
	}
	var loads []Request
	for _, r := range api.reqs {
		if r.Path == "/api/workspaces/load" {
			loads = append(loads, r)
		}
	}
	if len(loads) != 1 || !loads[0].Discard || !strings.Contains(loads[0].Body, `"root_path":"`+repo+`"`) {
		t.Fatalf("expected one discarded load of the repo root: %+v", loads)
	}
	// later calls reuse it without registering again
	_, _ = call(t, s, "recall", map[string]any{})
	if n := api.count("POST", "/api/workspaces/load"); n != 1 {
		t.Fatalf("registered %d times", n)
	}
	// one listing to find no match, one to pick up the registration
	if n := api.count("GET", "/api/workspaces"); n != 2 {
		t.Fatalf("listing read %d times", n)
	}
}

// Auto-registration is refused for the home directory, off when disabled, and never
// done for a model-supplied path; a refused registration is a clear error.
func TestAutoRegistrationGuards(t *testing.T) {
	repo := tempRepo(t)
	cases := []struct {
		name string
		opts Options
		api  *fakeAPI
		want string
	}{
		{"home", Options{Cwd: repo, AutoRegister: true, HomeDir: repo}, &fakeAPI{}, "never auto-registered"},
		{"off", Options{Cwd: repo}, &fakeAPI{}, "auto-registration is off"},
		{"refused", Options{Cwd: repo, AutoRegister: true}, &fakeAPI{loadStatus: 404}, "API refused to register it (404 Not Found"},
	}
	for _, c := range cases {
		s := newSession(t, c.api, c.opts, nil, "2025-06-18")
		res, _ := call(t, s, "ground", map[string]any{})
		if res["isError"] != true || !strings.Contains(text(res), c.want) {
			t.Fatalf("%s: %v", c.name, text(res))
		}
	}
	api := &fakeAPI{}
	s := newSession(t, api, Options{AutoRegister: true}, nil, "2025-06-18")
	res, _ := call(t, s, "explain", map[string]any{"path": filepath.Join(repo, "src", "a.go")})
	if res["isError"] != true || strings.Contains(fmt.Sprint(api.reqs), "/api/workspaces/load") {
		t.Fatalf("a path argument must not register a repository: %v", api.reqs)
	}
}

// An absolute path argument selects the registered workspace containing it and is
// sent to the API workspace-relative; the rewrite is reported, not silent.
func TestAbsolutePathArgumentResolvesAndIsRelativized(t *testing.T) {
	repo := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "repo-ws", Root: repo}}}
	s := newSession(t, api, Options{}, nil, "2025-06-18")
	abs := filepath.Join(repo, "src", "pkg", "a.go")
	res, rerr := call(t, s, "explain", map[string]any{"path": abs})
	if rerr != nil || res["isError"] != false {
		t.Fatalf("%v %v", rerr, res)
	}
	if p := api.lastTool(t).Path; p != "/api/workspaces/repo-ws/explain-path?path=src%2Fpkg%2Fa.go" {
		t.Fatalf("path not relativized: %s", p)
	}
	norms := res["_meta"].(map[string]any)["xmustard/normalized"].([]Normalization)
	if len(norms) != 1 || norms[0].Kind != "relative_path" || norms[0].From != abs || norms[0].To != "src/pkg/a.go" {
		t.Fatalf("rewrite not reported: %v", norms)
	}
	// with an explicit workspace the absolute path is still made relative to its root
	_, _ = call(t, s, "explain", map[string]any{"workspace_id": "repo-ws", "path": abs})
	if p := api.lastTool(t).Path; !strings.HasSuffix(p, "path=src%2Fpkg%2Fa.go") {
		t.Fatalf("explicit workspace + absolute path: %s", p)
	}
}

// Several roots: the one containing the working directory wins; otherwise the call
// fails naming them.
func TestMultipleRootsNeedTheWorkingDirectory(t *testing.T) {
	a, b := tempRepo(t), tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "a", Root: a}, {ID: "b", Root: b}}}
	s := newSession(t, api, Options{Cwd: filepath.Join(b, "src")}, &rootsClient{roots: []string{a, b}}, "2025-06-18")
	if _, rerr := call(t, s, "ground", map[string]any{}); rerr != nil {
		t.Fatal(rerr)
	}
	if p := api.lastTool(t).Path; !strings.HasPrefix(p, "/api/workspaces/b/") {
		t.Fatalf("root containing cwd not chosen: %s", p)
	}
	s = newSession(t, api, Options{}, &rootsClient{roots: []string{a, b}}, "2025-06-18")
	res, _ := call(t, s, "ground", map[string]any{})
	if res["isError"] != true || !strings.Contains(text(res), "the client has 2 roots") {
		t.Fatalf("ambiguous roots: %v", text(res))
	}
}

// Roots are never requested before the client finishes initialization.
func TestRootsNotRequestedBeforeInitialized(t *testing.T) {
	client := &rootsClient{roots: []string{"/x"}}
	s := New(Options{Backend: &fakeAPI{}, Getenv: noEnv}).NewSession(client)
	_, _ = s.Handle(context.Background(), "initialize", json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{"roots":{}}}`))
	res, _ := call(t, s, "ground", map[string]any{})
	if client.calls != 0 || !strings.Contains(text(res), "not finished initialization") {
		t.Fatalf("roots requested early (calls=%d): %s", client.calls, text(res))
	}
}

func TestWithinAndMatch(t *testing.T) {
	list := []registeredWorkspace{{ID: "r", Root: "/a/b"}, {ID: "rr", Root: "/a/b/c"}, {ID: "x", Root: "/a/bc"}}
	cases := map[string]string{"/a/b": "r", "/a/b/c/d.go": "rr", "/a/bc/x": "x", "/a/b/cd": "r"}
	for p, want := range cases {
		w, ok := match(list, p)
		if !ok || w.ID != want {
			t.Errorf("match(%s) = %v %v, want %s", p, w.ID, ok, want)
		}
	}
	if _, ok := match(list, "/a"); ok {
		t.Error("a parent of every root matched")
	}
}

// structured() shapes: objects keep their members, anything else is wrapped, and the
// result is always one valid JSON object carrying _xmustard.
func TestStructuredShapes(t *testing.T) {
	cases := []struct {
		text string
		want map[string]any
	}{
		{`{}`, map[string]any{}},
		{"  {\"a\":1}\n", map[string]any{"a": float64(1)}},
		{`[1,2]`, map[string]any{"result": []any{float64(1), float64(2)}}},
		{`"s"`, map[string]any{"result": "s"}},
		{`{"a":1},{"b":2}`, map[string]any{"text": `{"a":1},{"b":2}`}},
		{`not json`, map[string]any{"text": "not json"}},
		{``, map[string]any{"text": ""}},
		{`{"_xmustard":1}`, map[string]any{"result": map[string]any{"_xmustard": float64(1)}}},
	}
	for _, c := range cases {
		raw, err := structured(context.Background(), c.text, map[string]any{"k": "v"})
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%q: invalid structuredContent %s: %v", c.text, raw, err)
		}
		if !reflect.DeepEqual(got[resultMetaMember], map[string]any{"k": "v"}) {
			t.Fatalf("%q: %s member: %v", c.text, resultMetaMember, got)
		}
		delete(got, resultMetaMember)
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%q: got %v, want %v", c.text, got, c.want)
		}
	}
}

// errBackend fails every request with err.
type errBackend struct{ err error }

func (b errBackend) Do(context.Context, Request) (*APIResponse, error) { return nil, b.err }

// Admission refusal while resolving is a retryable protocol overload, not a tool
// error, whether the shim's pool or the API refused; an unreachable API during
// resolution is a tool error that says so.
func TestResolutionFailuresKeepTheirKind(t *testing.T) {
	repo := tempRepo(t)
	s := newSession(t, errBackend{fmt.Errorf("API GET /api/workspaces: %w", budget.ErrOverloaded)}, Options{Cwd: repo}, nil, "2025-06-18")
	if _, rerr := call(t, s, "ground", map[string]any{}); rerr == nil || rerr.Code != CodeOverloaded {
		t.Fatalf("overload during resolution: want -32000, got %v", rerr)
	}
	// the API's ingress admission answers 503 {"overloaded":true}
	overloaded := &APIResponse{Status: http.StatusServiceUnavailable, Body: `{"error":"xmustard overloaded: retry shortly","overloaded":true}`}
	for _, route := range []string{"GET /api/workspaces", "POST /api/workspaces/load"} {
		api := &fakeAPI{handle: func(r Request) *APIResponse {
			if r.Method+" "+r.Path == route {
				return overloaded
			}
			return nil
		}}
		s = newSession(t, api, Options{Cwd: repo, AutoRegister: true}, nil, "2025-06-18")
		if res, rerr := call(t, s, "ground", map[string]any{}); rerr == nil || rerr.Code != CodeOverloaded {
			t.Fatalf("API refused %s: want -32000, got %v %v", route, rerr, res)
		}
	}
	s = newSession(t, errBackend{fmt.Errorf("xmustard API unreachable at http://127.0.0.1:9 (refused)")}, Options{Cwd: repo}, nil, "2025-06-18")
	res, rerr := call(t, s, "ground", map[string]any{})
	if rerr != nil || res["isError"] != true || !strings.Contains(text(res), "listing workspaces: xmustard API unreachable") {
		t.Fatalf("unreachable API during resolution: %v %v", rerr, res)
	}
}

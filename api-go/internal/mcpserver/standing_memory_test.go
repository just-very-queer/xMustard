package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectionAPI answers the projection route for workspace ws with text.
func projectionAPI(ws, text string) *fakeAPI {
	return &fakeAPI{handle: func(r Request) *APIResponse {
		if r.Path == "/api/workspaces/"+ws+"/memory/core?max_chars=6000" {
			b, _ := json.Marshal(map[string]any{"revision": 7, "text": text})
			return &APIResponse{Status: 200, Body: string(b)}
		}
		if strings.Contains(r.Path, "/memory/core") {
			return &APIResponse{Status: 404, Body: `{"error":"not found"}`}
		}
		return nil
	}}
}

func initInstructions(t *testing.T, s *Session) string {
	t.Helper()
	res, rerr := s.Handle(context.Background(), "initialize",
		json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}`))
	if rerr != nil {
		t.Fatal(rerr)
	}
	m := res.(map[string]any)
	if _, ok := m["capabilities"].(map[string]any)["prompts"]; !ok {
		t.Fatalf("the prompts capability is not advertised: %v", m["capabilities"])
	}
	return m["instructions"].(string)
}

func pathsCalled(api *fakeAPI) []string {
	api.mu.Lock()
	defer api.mu.Unlock()
	var out []string
	for _, r := range api.reqs {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// initialize appends the standing memory of the workspace the session resolves without
// a tool call (binding, env, a registered workspace holding the cwd), within the
// static instructions' budget plus the projection's; it never registers, and without
// the option or a projection it serves the static instructions alone.
func TestInitializeAppendsStandingMemory(t *testing.T) {
	const text = "[xmustard memory] workspace bound at memory revision 7: core memory is human-approved standing guidance"
	api := projectionAPI("bound", text)
	s := New(Options{Backend: api, WorkspaceID: "bound", StandingMemory: true, Getenv: noEnv}).NewSession(nil)
	if got := initInstructions(t, s); got != Instructions+"\n\n"+text {
		t.Fatalf("instructions with standing memory:\n%s", got)
	}
	if len(Instructions) > 1400 {
		t.Fatalf("the static instructions grew past 1,400 bytes: %d", len(Instructions))
	}
	// the option is off: no request, static text
	off := projectionAPI("bound", text)
	s = New(Options{Backend: off, WorkspaceID: "bound", Getenv: noEnv}).NewSession(nil)
	if got := initInstructions(t, s); got != Instructions || len(pathsCalled(off)) != 0 {
		t.Fatalf("option off: %q %v", got, pathsCalled(off))
	}
	// XMUSTARD_WORKSPACE_ID names it
	env := projectionAPI("fromenv", text)
	s = New(Options{Backend: env, StandingMemory: true, Getenv: func(k string) string {
		if k == "XMUSTARD_WORKSPACE_ID" {
			return "fromenv"
		}
		return ""
	}}).NewSession(nil)
	if got := initInstructions(t, s); !strings.HasSuffix(got, text) {
		t.Fatalf("env workspace: %q", got)
	}
	// a registered workspace holding the cwd; an unregistered repository is never
	// registered at initialize, and the static text is served
	root := t.TempDir()
	cwd := projectionAPI("wsCwd", text)
	cwd.workspaces = []registeredWorkspace{{ID: "wsCwd", Root: root}}
	s = New(Options{Backend: cwd, Cwd: root, StandingMemory: true, AutoRegister: true, Getenv: noEnv}).NewSession(nil)
	if got := initInstructions(t, s); !strings.HasSuffix(got, text) {
		t.Fatalf("cwd workspace: %q", got)
	}
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	unregistered := projectionAPI("x", text)
	s = New(Options{Backend: unregistered, Cwd: repo, StandingMemory: true, AutoRegister: true, Getenv: noEnv}).NewSession(nil)
	if got := initInstructions(t, s); got != Instructions {
		t.Fatalf("unregistered cwd: %q", got)
	}
	for _, p := range pathsCalled(unregistered) {
		if strings.HasPrefix(p, "POST") {
			t.Fatalf("initialize registered a repository: %v", pathsCalled(unregistered))
		}
	}
	// the API cannot answer: static text
	s = New(Options{Backend: projectionAPI("other", text), WorkspaceID: "bound", StandingMemory: true, Getenv: noEnv}).NewSession(nil)
	if got := initInstructions(t, s); got != Instructions {
		t.Fatalf("failed projection: %q", got)
	}
}

// The projection is a resource too, for clients that drop the instructions.
func TestMemoryCoreResource(t *testing.T) {
	api := projectionAPI("bound", "standing text")
	s := newSession(t, api, Options{WorkspaceID: "bound"}, nil, LatestProtocolVersion)
	res, rerr := s.Handle(context.Background(), "resources/list", nil)
	if rerr != nil || !strings.Contains(mustJSON(res), MemoryCoreURI) {
		t.Fatalf("core resource not listed: %v %v", res, rerr)
	}
	res, rerr = s.Handle(context.Background(), "resources/read", json.RawMessage(`{"uri":"`+MemoryCoreURI+`"}`))
	if rerr != nil {
		t.Fatal(rerr)
	}
	c := res.(map[string]any)["contents"].([]map[string]any)[0]
	if c["uri"] != MemoryCoreURI || !strings.Contains(c["text"].(string), `"text":"standing text"`) {
		t.Fatalf("core resource: %v", c)
	}
	s = newSession(t, projectionAPI("elsewhere", "x"), Options{WorkspaceID: "bound"}, nil, LatestProtocolVersion)
	if _, rerr = s.Handle(context.Background(), "resources/read", json.RawMessage(`{"uri":"`+MemoryCoreURI+`"}`)); rerr == nil || rerr.Code != CodeInternal {
		t.Fatalf("a failed projection read must be an error: %v", rerr)
	}
}

// prompts/list names the two prompts; prompts/get fills them, orient with the standing
// memory (the initial_instructions fallback), and refuses what the prompt does not take.
func TestPromptsListAndGet(t *testing.T) {
	api := projectionAPI("bound", "STANDING MEMORY TEXT")
	s := newSession(t, api, Options{WorkspaceID: "bound"}, nil, LatestProtocolVersion)
	res, rerr := s.Handle(context.Background(), "prompts/list", nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var names []string
	for _, p := range res.(map[string]any)["prompts"].([]map[string]any) {
		names = append(names, p["name"].(string))
	}
	if strings.Join(names, ",") != "orient,pre_commit_check" {
		t.Fatalf("prompts: %v", names)
	}
	get := func(params string) (string, *RPCError) {
		t.Helper()
		res, rerr := s.Handle(context.Background(), "prompts/get", json.RawMessage(params))
		if rerr != nil {
			return "", rerr
		}
		msgs := res.(map[string]any)["messages"].([]map[string]any)
		return msgs[0]["content"].(map[string]any)["text"].(string), nil
	}
	text, rerr := get(`{"name":"orient","arguments":{"task":"fix the billing rounding"}}`)
	if rerr != nil || !strings.HasPrefix(text, Instructions) || !strings.Contains(text, "STANDING MEMORY TEXT") ||
		!strings.Contains(text, `q="fix the billing rounding"`) {
		t.Fatalf("orient:\n%s %v", text, rerr)
	}
	text, rerr = get(`{"name":"pre_commit_check","arguments":{"paths":"src/billing.go"}}`)
	if rerr != nil || !strings.Contains(text, `paths="src/billing.go"`) || !strings.Contains(text, "impact") ||
		!strings.Contains(text, "diagnostics") || strings.Contains(text, "STANDING MEMORY TEXT") {
		t.Fatalf("pre_commit_check:\n%s %v", text, rerr)
	}
	if text, rerr = get(`{"name":"pre_commit_check"}`); rerr != nil || !strings.Contains(text, "the paths the change touches") {
		t.Fatalf("pre_commit_check without paths: %q %v", text, rerr)
	}
	for _, bad := range []string{`{"name":"nope"}`, `{"name":"orient","arguments":{"bogus":"x"}}`,
		`{"name":"orient","arguments":{"task":"` + strings.Repeat("x", maxPromptArg+1) + `"}}`, `{"name":"orient","arguments":{"task":3}}`} {
		if _, rerr := get(bad); rerr == nil || rerr.Code != CodeInvalidParams {
			t.Fatalf("%s: want invalid params, got %v", bad[:min(len(bad), 60)], rerr)
		}
	}
}

// The WS-31 arguments are Advanced: validated on tools/call and forwarded.
func TestTierAndSinceRevisionArguments(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	if res, rerr := call(t, s, "ground", map[string]any{"workspace_id": "ws", "since_revision": 42}); rerr != nil || res["isError"] == true {
		t.Fatalf("ground since_revision: %v %v", res, rerr)
	}
	if p := api.lastTool(t).Path; !strings.Contains(p, "since_revision=42") {
		t.Fatalf("ground forwarded %s", p)
	}
	if msg, _ := argErr(t, s, "ground", map[string]any{"workspace_id": "ws", "since_revision": -1}); !strings.Contains(msg, "since_revision") {
		t.Fatalf("a negative since_revision must be refused: %s", msg)
	}
	if _, rerr := call(t, s, "recall", map[string]any{"workspace_id": "ws", "tier": "core"}); rerr != nil {
		t.Fatal(rerr)
	}
	if p := api.lastTool(t).Path; !strings.Contains(p, "tier=core") {
		t.Fatalf("recall forwarded %s", p)
	}
	if msg, _ := argErr(t, s, "recall", map[string]any{"workspace_id": "ws", "tier": "hot"}); !strings.Contains(msg, "tier") {
		t.Fatalf("an unknown tier must be refused: %s", msg)
	}
	if _, rerr := call(t, s, "remember", map[string]any{"workspace_id": "ws", "op": "tier", "entry_id": "ctx_1", "tier": "core",
		"reason": "standing rule"}); rerr != nil {
		t.Fatal(rerr)
	}
	body := api.lastTool(t).Body
	for _, want := range []string{`"op":"tier"`, `"tier":"core"`, `"entry_id":"ctx_1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("remember body %s lacks %s", body, want)
		}
	}
}

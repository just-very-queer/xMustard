package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A misspelled argument key is repaired through toolcompat, reaches the API under its
// canonical name, and the repair is recorded in _meta, never silent.
func TestToolcompatRepairsAndRecordsNormalizations(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	res, rerr := call(t, s, "explain", map[string]any{"workspace_id": "ws", "filePath": "cmd/main.go"})
	if rerr != nil || res["isError"] == true {
		t.Fatalf("filePath was not repaired: %v %v", rerr, res)
	}
	if got := api.lastTool(t).Path; !strings.Contains(got, "path=cmd%2Fmain.go") {
		t.Fatalf("canonical path not sent: %s", got)
	}
	norms, _ := res["_meta"].(map[string]any)["xmustard/normalized"].([]Normalization)
	if len(norms) != 1 || norms[0].Kind != "alias" || norms[0].From != "filePath" || norms[0].To != "path" {
		t.Fatalf("repair not recorded: %+v", norms)
	}
}

// The write tools are never repaired: the call fails with an error naming the
// canonical field, and nothing reaches the API.
func TestToolcompatNeverRewritesWriteTools(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	_, rerr := call(t, s, "remember", map[string]any{"workspace_id": "ws", "text": "a fact"})
	if rerr == nil || rerr.Code != CodeInvalidParams || !strings.Contains(rerr.Message, `"content"`) {
		t.Fatalf("want -32602 naming content, got %+v", rerr)
	}
	if n := len(api.toolRequests()); n != 0 {
		t.Fatalf("a refused write reached the API %d times", n)
	}
	// a number spelled as a string stays a strict rejection (no coercion)
	if _, rerr := call(t, s, "recall", map[string]any{"workspace_id": "ws", "limit": "5"}); rerr == nil {
		t.Fatal("a string integer was coerced")
	}
}

// A read-only connection lists only the read-only tools and refuses a write before
// any API call.
func TestReadOnlyConnection(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{ReadOnly: true}, nil, LatestProtocolVersion)
	res, _ := s.Handle(context.Background(), "tools/list", nil)
	for _, tool := range res.(map[string]any)["tools"].([]map[string]any) {
		if name := tool["name"].(string); name == "remember" || name == "verify" {
			t.Fatalf("read-only tools/list offers %s", name)
		}
	}
	if n := len(res.(map[string]any)["tools"].([]map[string]any)); n != 7 {
		t.Fatalf("read-only tools/list has %d tools, want 7", n)
	}
	out, rerr := call(t, s, "remember", map[string]any{"workspace_id": "ws", "content": "x"})
	if rerr != nil || out["isError"] != true || !strings.Contains(text(out), "read-only") {
		t.Fatalf("write not refused: %v %v", rerr, out)
	}
	if n := api.count("POST", "/api/workspaces/ws/context"); n != 0 {
		t.Fatalf("refused write reached the API")
	}
}

// The connection's workspace binding resolves calls that name no workspace, ahead of
// XMUSTARD_WORKSPACE_ID, and is echoed with its source.
func TestWorkspaceBinding(t *testing.T) {
	api := &fakeAPI{}
	env := func(k string) string {
		if k == "XMUSTARD_WORKSPACE_ID" {
			return "from-env"
		}
		return ""
	}
	s := newSession(t, api, Options{WorkspaceID: "bound", Getenv: env}, nil, LatestProtocolVersion)
	res, rerr := call(t, s, "ground", map[string]any{})
	if rerr != nil {
		t.Fatal(rerr)
	}
	ws := res["_meta"].(map[string]any)["xmustard/workspace"].(Workspace)
	if ws.ID != "bound" || ws.Source != SourceBinding {
		t.Fatalf("binding not used: %+v", ws)
	}
}

// Every tools/call is counted per tool and per client profile.
func TestUsageCounters(t *testing.T) {
	before := Usage()
	s := newSession(t, &fakeAPI{}, Options{Client: "codex"}, nil, LatestProtocolVersion)
	if _, rerr := call(t, s, "search", map[string]any{"workspace_id": "ws", "query": "needle"}); rerr != nil {
		t.Fatal(rerr)
	}
	_, _ = call(t, s, "search", map[string]any{"workspace_id": "ws", "limit": 99, "query": "x"})
	after := Usage()
	tool, client := after.Tools["search"], after.Clients["codex"]
	prevTool, prevClient := before.Tools["search"], before.Clients["codex"]
	if tool.Calls-prevTool.Calls != 2 || tool.Errors-prevTool.Errors != 1 || client.Calls-prevClient.Calls != 2 {
		t.Fatalf("usage not counted: before %+v %+v after %+v %+v", prevTool, prevClient, tool, client)
	}
	if tool.ArgBytes <= prevTool.ArgBytes || tool.ResponseBytes <= prevTool.ResponseBytes || tool.TokensEst <= prevTool.TokensEst {
		t.Fatalf("bytes not counted: %+v", tool)
	}
	if b, _ := json.Marshal(after); !strings.Contains(string(b), `"response_tokens_est"`) {
		t.Fatalf("snapshot encoding: %s", b)
	}
}

func TestParseConnectionOptions(t *testing.T) {
	for in, want := range map[string]bool{"": false, "full": false, "readonly": true, "READ-ONLY": true} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Fatalf("ParseMode(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseMode("admin"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if c, err := ParseClientProfile("Codex"); err != nil || c != "codex" {
		t.Fatalf("ParseClientProfile: %q %v", c, err)
	}
	if _, err := ParseClientProfile("netscape"); err == nil {
		t.Fatal("unknown client accepted")
	}
}

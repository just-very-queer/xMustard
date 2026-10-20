package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"xmustard/api-go/internal/hooks"
)

// Every Cursor event has exactly one way to be answered, a gate row, and a route
// (WS-40c-b). The gate row is not optional: a route with none is refused.
func TestCursorHookTablesAndGatesAgree(t *testing.T) {
	for _, e := range hooks.CursorEvents {
		_, answers := hookHandlers[e.Key()]
		_, queued := hookEnqueued[e.Key()]
		if answers == queued || queued != (e.Mode == hooks.Enqueue) {
			t.Errorf("%s: handler %v, queue effect %v, mode %v", e.Name, answers, queued, e.Mode)
		}
		if _, ok := routeGateTable["POST /api/hooks/cursor/"+e.Name]; !ok {
			t.Errorf("%s has no gate row", e.Name)
		}
	}
}

// postCursor posts one Cursor event, pinned to a workspace by the header the client
// sends (the id the daemon validates).
func (f *hookFixture) postCursor(t testing.TB, token, event, pin, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/hooks/cursor/"+event, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if pin != "" {
		req.Header.Set("X-Xmustard-Workspace", pin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b
}

// The daemon answers a Cursor event in Cursor's schema: one flat object whose members
// are the keys Cursor reads.
func TestCursorHookAnswersInCursorsSchema(t *testing.T) {
	f := newHookFixture(t)
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "sessionStart",
		"conversation_id": "cursor-1",
		"workspace_roots": []string{f.root},
	})
	code, out := f.postCursor(t, f.agent, "sessionStart", f.ws, string(body))
	if code != http.StatusOK {
		t.Fatalf("sessionStart: %d %s", code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("sessionStart: the answer is not a JSON object: %v (%s)", err, out)
	}
	for k := range m {
		if k != "additional_context" && k != "updated_mcp_tool_output" {
			t.Errorf("sessionStart wrote %q, which Cursor does not read", k)
		}
	}
	if _, ok := m["additional_context"]; !ok {
		t.Errorf("sessionStart wrote no additional_context: %s", out)
	}
}

// The workspace is bound by the pin the client sends, and a pin the daemon cannot
// validate binds nothing: the answer says nothing and the user keeps their own output.
func TestCursorHookBindsOnlyAValidatedPin(t *testing.T) {
	f := newHookFixture(t)
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "preToolUse",
		"conversation_id": "cursor-2",
		"tool_name":       "Read",
		"tool_input":      map[string]any{"file_path": f.root + "/src/billing.go"},
	})
	if _, out := f.postCursor(t, f.agent, "preToolUse", f.ws, string(body)); !strings.Contains(string(out), "billing rounding") {
		t.Fatalf("a pinned agent gets the path-bound memory: %s", out)
	}
	// A pin that is not a workspace id binds nothing, and the answer is empty.
	if code, out := f.postCursor(t, f.agent, "preToolUse", "../../etc", string(body)); code != http.StatusOK || len(out) != 0 {
		t.Fatalf("an unvalidated pin got %d %s", code, out)
	}
}

// A body that names another event is refused, and the client keeps its own output.
func TestCursorHookRefusesAnotherEventsBody(t *testing.T) {
	f := newHookFixture(t)
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "postToolUse",
		"conversation_id": "cursor-3",
		"tool_name":       "MCP:ci_logs",
		"tool_output":     `{"content":"the log"}`,
	})
	if code, out := f.postCursor(t, f.agent, "sessionStart", f.ws, string(body)); code != http.StatusOK || len(out) != 0 {
		t.Fatalf("a body naming another event got %d %s", code, out)
	}
}

// An MCP tool's result is replaced by the projection, in the key Cursor reads.
func TestCursorHookReplacesAnMCPToolResult(t *testing.T) {
	f := newHookFixture(t)
	big := strings.Repeat("x", 40000)
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "postToolUse",
		"conversation_id": "cursor-4",
		"tool_name":       "MCP:ci_logs",
		"tool_output":     `{"content":"` + big + `"}`,
	})
	code, out := f.postCursor(t, f.agent, "postToolUse", f.ws, string(body))
	if code != http.StatusOK {
		t.Fatalf("postToolUse: %d %s", code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("postToolUse: the answer is not a JSON object: %v (%s)", err, out)
	}
	replaced, ok := m["updated_mcp_tool_output"].(string)
	if !ok {
		t.Fatalf("postToolUse wrote no updated_mcp_tool_output: %s", out)
	}
	if len(replaced) >= len(big) {
		t.Errorf("the projection carries %d characters, the un-reduced output %d", len(replaced), len(big))
	}
}

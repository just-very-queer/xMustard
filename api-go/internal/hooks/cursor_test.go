package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

// The four events Cursor routes are the adapter's own names, and each is answered by
// the shared handler the daemon already has (WS-40c-b).
func TestCursorEventsNameTheirSharedHandler(t *testing.T) {
	want := map[string]string{
		"sessionStart": "SessionStart",
		"preToolUse":   "PreToolUse",
		"postToolUse":  "PostToolUse",
		"preCompact":   "PreCompact",
	}
	if len(CursorEvents) != len(want) {
		t.Fatalf("the cursor event table has %d rows, want %d", len(CursorEvents), len(want))
	}
	for _, e := range CursorEvents {
		handler, ok := want[e.Name]
		if !ok {
			t.Errorf("%s is not one of the four events Cursor routes", e.Name)
			continue
		}
		if e.Key() != handler {
			t.Errorf("%s answers the %s handler, want %s", e.Name, e.Key(), handler)
		}
	}
	if Cursor.Name != "cursor" || Cursor.Format != "cursor" {
		t.Fatalf("the cursor client is %q/%q, want cursor/cursor", Cursor.Name, Cursor.Format)
	}
}

// The answer is one flat object whose members are the keys Cursor reads, and a member
// is written only when it has a value: a key with no value is an invalid response.
func TestCursorReplyWritesTheKeysCursorReads(t *testing.T) {
	ev := func(name string) Event {
		for _, e := range CursorEvents {
			if e.Name == name {
				return e
			}
		}
		t.Fatalf("no %s event", name)
		return Event{}
	}
	out := func(t *testing.T, e Event, r Result) map[string]any {
		t.Helper()
		reply := Cursor.Reply(e, r)
		if reply == nil {
			t.Fatalf("%s: the answer said nothing", e.Name)
		}
		var m map[string]any
		if err := json.Unmarshal(reply.Body, &m); err != nil {
			t.Fatalf("%s: the answer is not a JSON object: %v (%s)", e.Name, err, reply.Body)
		}
		return m
	}

	m := out(t, ev("sessionStart"), Result{Context: "the context"})
	if m["additional_context"] != "the context" {
		t.Errorf("sessionStart: additional_context is %v", m["additional_context"])
	}
	if len(m) != 1 {
		t.Errorf("sessionStart wrote %d members, want 1: %v", len(m), m)
	}

	// The MCP result the daemon shaped is the only replacement Cursor takes.
	payload, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": "the projection"}},
		"isError": false,
	})
	m = out(t, ev("postToolUse"), Result{Context: "the context", Updated: payload})
	if m["updated_mcp_tool_output"] != "the projection" {
		t.Errorf("postToolUse: updated_mcp_tool_output is %v", m["updated_mcp_tool_output"])
	}
	if m["additional_context"] != "the context" {
		t.Errorf("postToolUse: additional_context is %v", m["additional_context"])
	}
	if len(m) != 2 {
		t.Errorf("postToolUse wrote %d members, want 2: %v", len(m), m)
	}

	// The daemon never rewrites a tool's input, so the user's own input is what runs.
	m = out(t, ev("preToolUse"), Result{Context: "the context"})
	if _, ok := m["updated_input"]; ok {
		t.Errorf("preToolUse wrote updated_input: %v", m)
	}
}

// Cursor's injector throws an over-cap value away whole rather than truncating it, so
// the daemon caps what it sends instead of letting Cursor drop the answer.
func TestCursorReplyCapsTheContextAtCursorsCeiling(t *testing.T) {
	long := strings.Repeat("x", CursorContextChars*2)
	reply := Cursor.Reply(CursorEvents[0], Result{Context: long})
	if reply == nil {
		t.Fatal("the answer said nothing")
	}
	var m struct {
		AdditionalContext string `json:"additional_context"`
	}
	if err := json.Unmarshal(reply.Body, &m); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(m.AdditionalContext)); n != CursorContextChars {
		t.Fatalf("the context carries %d characters, want the ceiling %d", n, CursorContextChars)
	}
	// A context inside the ceiling is untouched.
	short := "short"
	reply = Cursor.Reply(CursorEvents[0], Result{Context: short})
	_ = json.Unmarshal(reply.Body, &m)
	if m.AdditionalContext != short {
		t.Fatalf("a context inside the ceiling was changed to %q", m.AdditionalContext)
	}
}

// A payload the daemon did not shape is not offered: the client keeps the tool's own
// output rather than being handed a projection xMustard did not write.
func TestCursorProjectionReadsOnlyTheShapeItKnows(t *testing.T) {
	for _, bad := range []string{`{"content":"not blocks"}`, `{"nothing":1}`, `not json`, ``} {
		if text, ok := cursorProjection(json.RawMessage(bad)); ok {
			t.Errorf("%q was read as a projection: %q", bad, text)
		}
	}
	good := `{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"isError":false}`
	text, ok := cursorProjection(json.RawMessage(good))
	if !ok || text != "ab" {
		t.Fatalf("the projection is %q (%v), want %q", text, ok, "ab")
	}
}

// Cursor's built-in tools have their own names and its MCP tools are namespaced, so
// the class depends on the client and not on the tool name alone.
func TestCursorToolClassesFollowTheClient(t *testing.T) {
	if got := ClassOfFor("cursor", "Shell"); got != ToolShell {
		t.Errorf("Cursor's shell is %v, want %v", got, ToolShell)
	}
	if got := ClassOfFor("cursor", "MCP:ci_logs"); got != ToolCapture {
		t.Errorf("a namespaced Cursor tool is %v, want %v", got, ToolCapture)
	}
	if got := ClassOfFor("cursor", "SomeEditorTool"); got != ToolOther {
		t.Errorf("an unknown Cursor tool is %v, want %v", got, ToolOther)
	}
	if !CapturedFor("cursor", "Shell") || !CapturedFor("cursor", "MCP:ci_logs") {
		t.Error("a Cursor shell and a Cursor MCP tool are captured")
	}
	if CapturedFor("cursor", "SomeEditorTool") {
		t.Error("an unknown Cursor tool is captured")
	}
	// The other clients keep their own classes.
	if got := ClassOfFor("claude", "Bash"); got != ToolShell {
		t.Errorf("Claude's shell is %v, want %v", got, ToolShell)
	}
	if got := ClassOfFor("codex", "apply_patch"); got != ToolEdit {
		t.Errorf("Codex's apply_patch is %v, want %v", got, ToolEdit)
	}
}

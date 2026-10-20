package hooks

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"xmustard/api-go/internal/evidence"
)

// Cursor is Cursor's hook protocol (WS-40c-b; PAR-ADP-10, PAR-ADP-14, PAR-CTX-03).
// Cursor has no http and no mcp_tool hook type, so the daemon answers a command
// hook: the client posts the event to /api/hooks/cursor/<event> and reads a flat
// JSON object whose members are the keys Cursor reads.
var Cursor = Client{Name: "cursor", Format: "cursor", Events: CursorEvents, reply: cursorReply}

// CursorEvents are the four events Cursor routes, in Cursor's own names (WS-40c-a):
// its hooks carry Claude Code's hooks.json shape, and the adapter takes these four
// and declines the rest. The Handler field names the shared handler each one answers
// with, so the daemon's event handlers stay one set: a Cursor preToolUse is answered
// by the same code as a Claude Code PreToolUse, and only the answer's shape differs.
var CursorEvents = []Event{
	{Name: "sessionStart", Handler: "SessionStart", Context: true},
	{Name: "preToolUse", Handler: "PreToolUse", Context: true},
	{Name: "postToolUse", Handler: "PostToolUse", Context: true, Replaces: true},
	{Name: "preCompact", Handler: "PreCompact", Mode: Enqueue},
}

// CursorContextChars is Cursor's own ceiling on injected additional context, measured
// from the installed 3.22.7 bundle (limits.ts, Cbt=1e4). Cursor's injector throws an
// over-cap value away whole rather than truncating it, so the daemon caps what it
// sends instead of letting Cursor drop the answer.
const CursorContextChars = 10000

// cursorOutput is the answer in Cursor's schema: one flat object whose members are
// the keys Cursor reads. A member is written only when it has a value, because a
// permission hook that returns a key it has no value for is an invalid response that
// blocks the user's tool call.
//
// The daemon writes no updated_input: it never rewrites a tool's input, so the
// user's own input is what Cursor runs. Which commands a client matches is the
// client's own matcher, not the daemon's answer.
type cursorOutput struct {
	AdditionalContext    string `json:"additional_context,omitempty"`
	UpdatedMCPToolOutput string `json:"updated_mcp_tool_output,omitempty"`
}

// cursorReply writes the answer in Cursor's schema. It says nothing when there is
// nothing to say: the client then leaves the user's own output in place.
func cursorReply(ev Event, r Result) *Reply {
	var out cursorOutput
	if ev.Context {
		out.AdditionalContext = capCursorContext(r.Context)
	}
	if ev.Key() == "PostToolUse" {
		// Cursor replaces an MCP tool's result with text; a built-in tool is
		// observe-only and the daemon never offers one.
		if text, ok := cursorProjection(r.Updated); ok {
			out.UpdatedMCPToolOutput = text
		}
	}
	if out.AdditionalContext == "" && out.UpdatedMCPToolOutput == "" {
		return nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return &Reply{Body: body, Context: out.AdditionalContext}
}

// capCursorContext caps injected context at Cursor's own ceiling, counted in
// characters: the client counts characters too, and a value past the ceiling is
// dropped whole by Cursor rather than truncated.
func capCursorContext(s string) string {
	if utf8.RuneCountInString(s) <= CursorContextChars {
		return s
	}
	return string([]rune(s)[:CursorContextChars])
}

// cursorProjection reads the text out of a shaped MCP result, the only shape the
// cursor shaper builds ({"content":[{"type":"text","text":...}],"isError":...}). A
// payload it cannot read says nothing, so the client keeps the tool's own output
// rather than being handed a projection xMustard did not write.
func cursorProjection(payload json.RawMessage) (string, bool) {
	if len(payload) == 0 {
		return "", false
	}
	var p struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return "", false
	}
	var b strings.Builder
	for _, c := range p.Content {
		if c.Type != "" && c.Type != "text" {
			continue
		}
		b.WriteString(c.Text)
	}
	if b.Len() == 0 {
		return "", false
	}
	return b.String(), true
}

// cursorToolClasses maps Cursor's built-in tools. Cursor renames Claude Code's tools
// (Bash is Shell, Edit is Write, and Glob is not supported), so the classes come
// from Cursor's own names and not from the shared table. A built-in tool's result is
// observe-only (the cursor shaper replaces MCP results only), but a file tool's path
// is still what the daemon grounds its context on.
var cursorToolClasses = map[string]ToolClass{
	"Shell": ToolShell,
	"Read":  ToolRead,
	"Write": ToolEdit,
	"Grep":  ToolSearch,
}

// ClassOfFor returns a tool's class for the client that posted it (WS-40c-b). Cursor's
// built-in tools have their own names and its MCP tools are namespaced
// ("MCP:server__tool"), which the mcp__ rule does not match, so the class depends on
// the client and not on the tool name alone. The namespaced rule is the evidence
// package's, the same one the cursor shaper decides with, so what is captured and
// what is replaced are one decision.
func ClassOfFor(client, tool string) ToolClass {
	if client == "cursor" {
		if c, ok := cursorToolClasses[tool]; ok {
			return c
		}
		if evidence.NamespacedTool(tool) {
			return ToolCapture
		}
		return ToolOther
	}
	return ClassOf(tool)
}

// CapturedFor reports whether a PostToolUse captures and reduces a tool's output for
// the client that posted it.
func CapturedFor(client, tool string) bool {
	switch ClassOfFor(client, tool) {
	case ToolSearch, ToolRead, ToolShell, ToolCapture:
		return true
	}
	return false
}

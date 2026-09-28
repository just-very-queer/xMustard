// Package hooks is the client-hook protocol of the daemon (WS-23; PAR-HAR-01,
// PAR-HAR-02, PAR-ADP-06): the Claude Code hook events the API answers at
// /api/hooks/claude/<event>, the part of each event's JSON it reads, the JSON it
// answers with, and the steering state it keeps per session. The routes
// (cmd/xmustard-api/hooks_routes.go) do the I/O; this package holds no store.
//
// Shapes follow the Claude Code hooks reference (code.claude.com/docs/en/hooks,
// read 2026-09-28): common input fields (session_id, cwd, hook_event_name, agent_id,
// agent_type), each event's own fields, and the JSON output (hookSpecificOutput with
// hookEventName, additionalContext, updatedToolOutput, watchPaths). An answer never
// blocks or decides a permission: xMustard adds context, replaces an output with its
// shape-matched reduction, or says nothing.
package hooks

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// Client is the hook client this file speaks for.
const Client = "claude"

// Mode is how the service answers an event.
type Mode int

const (
	// Respond runs the event's handler within the hook budget and returns its output.
	Respond Mode = iota
	// Enqueue records the event for asynchronous work and answers at once with no
	// output (Stop, SubagentStop and SessionEnd: Claude Code gives every SessionEnd
	// hook a shared 1.5 s budget).
	Enqueue
)

// Event is one Claude Code hook event the service answers.
type Event struct {
	Name string
	Mode Mode
	// Context says whether the event's output may carry additionalContext.
	Context bool
	// WatchPaths says whether the event's output may carry watchPaths. Claude Code
	// replaces its dynamic watch list with a CwdChanged answer's watchPaths, and an
	// empty list clears it.
	WatchPaths bool
	// Replaces says whether the event's output may carry updatedToolOutput.
	Replaces bool
}

// Events is the table of Claude Code events with a route. WorktreeCreate is absent on
// purpose: a WorktreeCreate hook replaces Claude Code's own git worktree creation and
// must return the new worktree's path, so no observer can hook it. PermissionRequest,
// Notification and the other events carry nothing xMustard uses.
var Events = []Event{
	{Name: "SessionStart", Context: true, WatchPaths: true},
	{Name: "SubagentStart", Context: true},
	{Name: "UserPromptSubmit", Context: true},
	{Name: "PreToolUse", Context: true},
	{Name: "PostToolUse", Context: true, Replaces: true},
	{Name: "PostToolUseFailure", Context: true},
	{Name: "PostToolBatch", Context: true},
	{Name: "CwdChanged", WatchPaths: true},
	{Name: "FileChanged"},
	{Name: "WorktreeRemove"},
	{Name: "PreCompact", Mode: Enqueue},
	{Name: "PostCompact", Mode: Enqueue},
	{Name: "Stop", Mode: Enqueue},
	{Name: "SubagentStop", Mode: Enqueue},
	{Name: "SessionEnd", Mode: Enqueue},
}

// EventByName returns the event with a route, if any.
func EventByName(name string) (Event, bool) {
	for _, e := range Events {
		if e.Name == name {
			return e, true
		}
	}
	return Event{}, false
}

// Input is the part of a hook body the service reads. Tool responses are not decoded
// here: PostToolUse capture stream-decodes the body itself (evidence.DecodeHookBody),
// and a batch's responses are never needed.
type Input struct {
	SessionID string    `json:"session_id"`
	Cwd       string    `json:"cwd"`
	Event     string    `json:"hook_event_name"`
	AgentID   string    `json:"agent_id"`
	AgentType string    `json:"agent_type"`
	ToolName  string    `json:"tool_name"`
	ToolInput ToolInput `json:"tool_input"`
	ToolUseID string    `json:"tool_use_id"`
	// Prompt is UserPromptSubmit's submitted text.
	Prompt string `json:"prompt"`
	// Source is SessionStart's startup, resume, clear, compact or fork.
	Source string `json:"source"`
	// FilePath and FileEvent are FileChanged's changed file and change/add/unlink.
	FilePath  string `json:"file_path"`
	FileEvent string `json:"event"`
	// OldCwd and NewCwd are CwdChanged's directories.
	OldCwd string `json:"old_cwd"`
	NewCwd string `json:"new_cwd"`
	// WorktreePath is WorktreeRemove's worktree.
	WorktreePath string `json:"worktree_path"`
	// Reason is SessionEnd's exit reason; Trigger is PreCompact/PostCompact's.
	Reason  string `json:"reason"`
	Trigger string `json:"trigger"`
	// ToolCalls is PostToolBatch's batch.
	ToolCalls []BatchCall `json:"tool_calls"`
}

// ToolInput is the part of a tool's input the service reads. A Write's content or an
// Edit's strings are not kept.
type ToolInput struct {
	Command      string `json:"command"`
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Pattern      string `json:"pattern"`
	Path         string `json:"path"`
}

// TargetPath is the file a file tool reads or changes.
func (in ToolInput) TargetPath() string {
	if in.FilePath != "" {
		return in.FilePath
	}
	return in.NotebookPath
}

// BatchCall is one call of a PostToolBatch.
type BatchCall struct {
	ToolName  string    `json:"tool_name"`
	ToolUseID string    `json:"tool_use_id"`
	ToolInput ToolInput `json:"tool_input"`
}

// Tool classes: what the service does with a tool's calls.
type ToolClass int

const (
	ToolOther ToolClass = iota
	// ToolSearch tools look for a pattern: index hits and pattern memories before them.
	ToolSearch
	// ToolRead tools read a file: its path-bound memories before them.
	ToolRead
	// ToolEdit tools change a file: path-bound memories and a syntax baseline before,
	// the dirty set and the syntax delta after.
	ToolEdit
	// ToolShell runs commands: a search pattern before when it runs rg or grep, capture
	// and the post-git notice after.
	ToolShell
	// ToolCapture tools have outputs worth reducing (Read, Grep, Glob, WebFetch, MCP).
	ToolCapture
)

// toolClasses maps the built-in tools. An MCP tool (mcp__server__tool) is ToolCapture
// unless it is xMustard's own (OwnTool), whose results are already reduced.
var toolClasses = map[string]ToolClass{
	"Grep": ToolSearch, "Glob": ToolSearch,
	"Read": ToolRead,
	"Edit": ToolEdit, "Write": ToolEdit, "NotebookEdit": ToolEdit,
	"Bash":     ToolShell,
	"WebFetch": ToolCapture,
}

// ClassOf returns a tool's class.
func ClassOf(tool string) ToolClass {
	if c, ok := toolClasses[tool]; ok {
		return c
	}
	if strings.HasPrefix(tool, "mcp__") && !OwnTool(tool) {
		return ToolCapture
	}
	return ToolOther
}

// Captured reports whether PostToolUse captures and reduces a tool's output.
func Captured(tool string) bool {
	switch ClassOf(tool) {
	case ToolSearch, ToolRead, ToolShell, ToolCapture:
		return true
	}
	return false
}

// OwnTool reports whether tool is one of xMustard's MCP tools, configured by hand
// (mcp__xmustard__*) or by this plugin (mcp__plugin_xmustard_xmustard__*).
func OwnTool(tool string) bool {
	return strings.HasPrefix(tool, "mcp__xmustard__") || strings.HasPrefix(tool, "mcp__plugin_xmustard_")
}

// Output is a hook answer.
type Output struct {
	HookSpecificOutput *Specific `json:"hookSpecificOutput,omitempty"`
}

// Specific is the event-specific part of an answer.
type Specific struct {
	HookEventName     string          `json:"hookEventName"`
	AdditionalContext string          `json:"additionalContext,omitempty"`
	UpdatedToolOutput json.RawMessage `json:"updatedToolOutput,omitempty"`
	// WatchPaths is a pointer so that an empty list is sent ([] clears Claude Code's
	// dynamic watch list) while a nil one is left out.
	WatchPaths *[]string `json:"watchPaths,omitempty"`
}

// Answer builds an event's output from what the handler produced, dropping what the
// event cannot carry. A nil watch says nothing about the watch list; a non-nil empty
// one sends watchPaths: [] (CwdChanged into a directory with nothing to watch). It
// returns nil when there is nothing to say: the route then answers 200 with an empty
// body, which Claude Code reads as success with no output.
func Answer(ev Event, context string, updated json.RawMessage, watch []string) *Output {
	s := &Specific{HookEventName: ev.Name}
	if ev.Context {
		s.AdditionalContext = CapContext(context)
	}
	if ev.Replaces {
		s.UpdatedToolOutput = updated
	}
	if ev.WatchPaths && watch != nil {
		s.WatchPaths = &watch
	}
	if s.AdditionalContext == "" && len(s.UpdatedToolOutput) == 0 && s.WatchPaths == nil {
		return nil
	}
	return &Output{HookSpecificOutput: s}
}

// MaxContextChars is Claude Code's cap on one additionalContext string: past it the
// text is written to a file and only a 2,000-character preview reaches the model.
const MaxContextChars = 10000

// CapContext keeps a context within MaxContextChars, cutting at a paragraph when it can.
func CapContext(s string) string {
	if utf8.RuneCountInString(s) <= MaxContextChars {
		return s
	}
	r := []rune(s)[:MaxContextChars-1]
	cut := string(r)
	if i := strings.LastIndex(cut, "\n\n"); i > MaxContextChars/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// Size is the room a context part takes when composed: its characters and a
// separator; nothing for a blank part.
func Size(part string) int {
	if part = strings.TrimSpace(part); part == "" {
		return 0
	}
	return utf8.RuneCountInString(part) + 2
}

// Room is the context left for more parts after these.
func Room(parts ...string) int {
	n := MaxContextChars
	for _, p := range parts {
		n -= Size(p)
	}
	return n
}

// Compose joins context parts in priority order within MaxContextChars.
func Compose(parts ...string) string { return ComposeWithin(MaxContextChars, parts...) }

// ComposeWithin joins context parts in priority order, keeping whole parts while they
// fit in room characters; a part that does not fit is left out, not cut.
func ComposeWithin(room int, parts ...string) string {
	var kept []string
	n := 0
	for _, p := range parts {
		size := Size(p)
		if size == 0 || n+size > room {
			continue
		}
		kept, n = append(kept, strings.TrimSpace(p)), n+size
	}
	return strings.Join(kept, "\n\n")
}

// Decode reads a hook body. A field of an unexpected type (an MCP tool's own "pattern"
// that is a number, say) is skipped rather than failing the body: only malformed JSON
// is an error.
func Decode(body []byte) (Input, error) {
	var in Input
	err := json.Unmarshal(body, &in)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		err = nil
	}
	return in, err
}

package hooks

import (
	"bytes"
	"encoding/json"
	"path"
)

// The Cursor hook client's hooks.json (WS-40c-b, WS-44).
//
// Cursor loads Claude Code's hooks.json shape natively, so the file is that shape
// with Cursor's own event names, its own matchers and its own keys (`failClosed`,
// `loop_limit`). The client is a command hook: Cursor has no http and no `mcp_tool`
// hook type, so it spawns `xmustard-cursor` per event and the client posts the event
// to the daemon.
//
// The four events are the ones WS-40c-a named and this adapter routes; the matchers
// are the ones the row's own fixtures carry: a Shell call for preToolUse, and an
// MCP tool for postToolUse (Cursor replaces an MCP tool's result only, and a
// built-in tool is observed and left as Cursor produced it).

// CursorClient is the command hook Cursor spawns.
const CursorClient = "xmustard-cursor"

// CursorTimeout is each hook's timeout in seconds: Cursor's own default for a
// command hook, and it covers the client's own budget (200 ms for the whole run).
const CursorTimeout = 5

// CursorLoopLimit is Cursor's own default for a hook that loops.
const CursorLoopLimit = 5

// cursorHooksDescription states the policy the file was written against, in the file
// itself: the failure policy is the owner's answer (fail open), and the replacement
// seam is Cursor's own.
const cursorHooksDescription = "xMustard hook client (WS-40c-b). Cursor has no http and no mcp_tool hook type, " +
	"so every event spawns xmustard-cursor, which posts the event to the daemon and prints the answer. failClosed is false " +
	"and the client exits non-zero and never 2 on a failure, so a stopped daemon, an unreadable event or a repository " +
	"outside the token's workspaces costs an unreduced tool result and never the user's tool call. Only updated_mcp_tool_output " +
	"is replaced, and only for MCP tools: a Cursor built-in (Shell, Read, Grep, Write, Task) is observed and left as Cursor " +
	"produced it. preToolUse is registered for Shell alone, because updated_input is documented on the generic hook but is " +
	"reported to be discarded outside Write and Task."

// cursorMatchers are the matchers the events carry. An event without one matches
// every call, which is what Cursor's own shape means for sessionStart and preCompact.
var cursorMatchers = map[string]string{
	"preToolUse":  "Shell",
	"postToolUse": "MCP:.+",
}

type cursorHooksFile struct {
	Description string             `json:"description"`
	Hooks       cursorHooksEntries `json:"hooks"`
}

// cursorHooksEntries are the events in the order the row names them, rather than a
// map's sorted order: the file is read by people, and its order is the adapter's.
type cursorHooksEntries []cursorHooksEntry

// cursorHooksEntry is one event and the matcher groups that answer it.
type cursorHooksEntry struct {
	Event  string
	Groups []cursorMatcherGroup
}

// MarshalJSON writes the events in the adapter's order.
func (e cursorHooksEntries) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, entry := range e {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(entry.Event)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		groups, err := json.Marshal(entry.Groups)
		if err != nil {
			return nil, err
		}
		b.Write(groups)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

type cursorMatcherGroup struct {
	Matcher string       `json:"matcher,omitempty"`
	Hooks   []cursorHook `json:"hooks"`
}

type cursorHook struct {
	Type       string `json:"type"`
	Command    string `json:"command"`
	FailClosed bool   `json:"failClosed"`
	LoopLimit  int    `json:"loop_limit"`
	Timeout    int    `json:"timeout"`
}

// CursorHooksJSON is the hooks.json that registers every Cursor event xMustard
// routes: bin is the client (ValidCursorClient), workspace an optional pin
// (transport.ValidWorkspaceID). The pin is what the daemon binds the event to, and
// the client refuses to send anything when the roots and the project directory
// disagree, so a pinned file names one repository.
func CursorHooksJSON(bin, workspace string) []byte {
	file := cursorHooksFile{Description: cursorHooksDescription, Hooks: cursorHooksEntries{}}
	for _, ev := range CursorEvents {
		group := cursorMatcherGroup{Hooks: []cursorHook{{
			Type:       "command",
			Command:    CursorCommand(bin, ev.Name, workspace),
			FailClosed: false,
			LoopLimit:  CursorLoopLimit,
			Timeout:    CursorTimeout,
		}}}
		if m, ok := cursorMatchers[ev.Name]; ok {
			group.Matcher = m
		}
		file.Hooks = append(file.Hooks, cursorHooksEntry{Event: ev.Name, Groups: []cursorMatcherGroup{group}})
	}
	out, _ := json.MarshalIndent(file, "", "  ") // only strings, bools and ints: cannot fail
	return append(out, '\n')
}

// CursorCommand is the command that runs the client for one event.
func CursorCommand(bin, event, workspace string) string {
	cmd := ShellQuote(bin) + " " + event
	if workspace != "" {
		cmd += " --workspace " + ShellQuote(workspace)
	}
	return cmd
}

// ValidCursorClient reports a client a Cursor hooks.json may run: CursorClient itself,
// or an absolute path whose last element is CursorClient and whose every character is
// printable. A relative path (bin/xmustard-cursor) or a NAME=value word would run
// what the hook's cwd, the repository, holds; a control character could repaint the
// line that shows the command.
func ValidCursorClient(bin string) bool {
	if bin == CursorClient {
		return true
	}
	return path.IsAbs(bin) && path.Base(bin) == CursorClient && printable(bin)
}

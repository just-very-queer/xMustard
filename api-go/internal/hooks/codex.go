package hooks

import (
	"bytes"
	"encoding/json"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Codex's hook protocol (WS-40a; PAR-ADP-07), from codex-rs at 44fe510 and checked
// against codex-cli 0.156.1. hooks/src/schema.rs has the input and output wires,
// hooks/src/engine/output_parser.rs what an answer may carry, and
// hooks/src/output_spill.rs the size past which Codex spills hook text to a file.
//
// Codex runs only command and mcp_tool handlers (config/src/hook_config.rs); there is
// no http handler, so hooks.json runs the static client for every event
// (`xmustard-hook codex <Event>`), which posts it to /api/hooks/codex/<Event>. An
// mcp_tool handler could reach the daemon with no process, and Codex does read its
// text as command output (core/src/hook_mcp_executor.rs), so it can return a block.
// It is not used: its arguments are templated field by field and a missing field fails
// the hook, so the root agent's calls (which carry no agent_id) could not name their
// subagent; and it would need a tool outside the nine.
//
// Codex's hook outputs deny unknown fields: an answer with any other key (Claude Code's
// watchPaths or updatedToolOutput, say) is invalid, and Codex fails the hook run and
// drops the whole answer. codexReply writes exactly the contract keys.

// CodexEvents are the Codex events with a route. Stop and PreCompact are left out: they
// would only mark the queue until WS-33 gives them work, and each hook costs Codex a
// login shell and a client start. Codex has no PostToolUseFailure (a failed call never
// reaches a hook), PostToolBatch, CwdChanged, FileChanged or WorktreeRemove.
var CodexEvents = []Event{
	{Name: "SessionStart", Context: true},
	{Name: "SubagentStart", Context: true},
	{Name: "UserPromptSubmit", Context: true},
	{Name: "PreToolUse", Context: true},
	{Name: "PostToolUse", Context: true, Replaces: true},
	{Name: "PostCompact", Mode: Enqueue},
	{Name: "SubagentStop", Mode: Enqueue},
	{Name: "SessionEnd", Mode: Enqueue},
}

// Codex is Codex's hook protocol.
var Codex = Client{Name: "codex", Format: "codex", Events: CodexEvents, reply: codexReply}

// CodexHookTextBytes is the most hook text Codex passes on whole: past 2,500
// approximate tokens of 4 bytes it writes the text to a file and gives the model a
// head-and-tail preview (output_spill.rs DEFAULT_HOOK_OUTPUT_TOKEN_LIMIT;
// utils/string approx_token_count). It bounds a block reason and each
// additionalContext alike.
const CodexHookTextBytes = 2500 * 4

// codexOutput is every output key xMustard writes. A PostToolUse block's reason
// replaces the tool output the model sees (Codex hands it over as a failed call's
// output); hookSpecificOutput carries additionalContext, which every Context event's
// schema takes.
type codexOutput struct {
	Decision           string         `json:"decision,omitempty"`
	Reason             string         `json:"reason,omitempty"`
	HookSpecificOutput *codexSpecific `json:"hookSpecificOutput,omitempty"`
}

type codexSpecific struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// codexReply writes an answer in Codex's schema. Watch paths have no Codex key.
func codexReply(ev Event, r Result) *Reply {
	var out codexOutput
	if ev.Replaces {
		out.Decision, out.Reason = codexBlock(r.Updated)
	}
	context := ""
	if ev.Context {
		context = CapContextBytes(r.Context, CodexHookTextBytes)
	}
	if context != "" {
		out.HookSpecificOutput = &codexSpecific{HookEventName: ev.Name, AdditionalContext: context}
	}
	if out.Decision == "" && out.HookSpecificOutput == nil {
		return nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return &Reply{Body: body, Context: context}
}

// codexBlock reads the codex shaper's payload, which must be exactly a "block"
// decision with a non-empty reason (Codex fails a block without one); anything else
// blocks nothing and the model keeps Codex's own output.
func codexBlock(payload json.RawMessage) (decision, reason string) {
	var p struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if len(payload) == 0 || dec.Decode(&p) != nil || p.Decision != "block" || strings.TrimSpace(p.Reason) == "" {
		return "", ""
	}
	return p.Decision, p.Reason
}

// CapContextBytes keeps a context within n bytes, cutting at a paragraph when it can
// and on a rune boundary otherwise.
func CapContextBytes(s string, n int) string {
	const ellipsis = "…"
	if len(s) <= n {
		return s
	}
	cut := strings.ToValidUTF8(s[:n-len(ellipsis)], "")
	if i := strings.LastIndex(cut, "\n\n"); i > n/2 {
		cut = cut[:i]
	}
	return cut + ellipsis
}

// patchHeaders open the apply_patch hunks that name a changed file (codex-rs
// apply-patch/src/parser.rs).
var patchHeaders = []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "}

// maxPatchPaths bounds the files taken from one patch.
const maxPatchPaths = 16

// PatchPaths lists the files an apply_patch patch changes, in patch order and each
// once; a relative path is joined to cwd, the directory Codex resolves it against.
func PatchPaths(patch, cwd string) []string {
	var out []string
	for _, line := range strings.Split(patch, "\n") {
		for _, h := range patchHeaders {
			p, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), h)
			if p = strings.TrimSpace(p); !ok || p == "" {
				continue
			}
			if !filepath.IsAbs(p) && cwd != "" {
				p = filepath.Join(cwd, p)
			}
			if len(out) < maxPatchPaths && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// --- hooks.json ---

// codexMatchers select the tools the service acts on: before a call, a Bash search or
// an apply_patch (memories, the syntax baseline); after it, a Bash or MCP capture or an
// apply_patch edit. Codex takes '|'-joined names as exact matches and anything else as
// a regex (hooks/src/events/common.rs).
var codexMatchers = map[string]string{"PreToolUse": "Bash|apply_patch", "PostToolUse": "^(Bash|apply_patch|mcp__.+)$"}

// codexTimeout is each hook's timeout in seconds. Codex runs a command hook through
// `$SHELL -lc`, so it covers a login shell's start as well as the client's 200 ms
// budget; Codex caps SessionEnd hooks at 3 s.
const codexTimeout = 5

var codexTimeouts = map[string]int{"SessionEnd": 2}

type codexHooksFile struct {
	Description string                         `json:"description"`
	Hooks       map[string][]codexMatcherGroup `json:"hooks"`
}

type codexMatcherGroup struct {
	Matcher string         `json:"matcher,omitempty"`
	Hooks   []codexHandler `json:"hooks"`
}

type codexHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// CodexHooksJSON is the hooks.json that registers every Codex event with a route: bin
// is the client (a path or a name on PATH), workspace an optional pin.
func CodexHooksJSON(bin, workspace string) []byte {
	file := codexHooksFile{Description: "xMustard hook client (xmustard-ops codex-hooks config)", Hooks: map[string][]codexMatcherGroup{}}
	for _, ev := range CodexEvents {
		timeout, ok := codexTimeouts[ev.Name]
		if !ok {
			timeout = codexTimeout
		}
		h := codexHandler{Type: "command", Command: CodexCommand(bin, ev.Name, workspace), Timeout: timeout}
		file.Hooks[ev.Name] = []codexMatcherGroup{{Matcher: codexMatchers[ev.Name], Hooks: []codexHandler{h}}}
	}
	out, _ := json.MarshalIndent(file, "", "  ") // only strings and ints: cannot fail
	return append(out, '\n')
}

// CodexCommand is the command that runs the client for one event.
func CodexCommand(bin, event, workspace string) string {
	cmd := ShellQuote(bin) + " codex " + event
	if workspace != "" {
		cmd += " --workspace " + ShellQuote(workspace)
	}
	return cmd
}

// ParseCodexCommand recognizes a command CodexCommand wrote: a client binary named
// xmustard-hook, "codex", a routed event and an optional workspace pin. Any other
// command, or any other shell syntax, is not xMustard's.
func ParseCodexCommand(cmd string) (bin, event, workspace string, ok bool) {
	w, ok := shellWords(cmd)
	if !ok || len(w) < 3 || path.Base(w[0]) != "xmustard-hook" || w[1] != "codex" {
		return "", "", "", false
	}
	if _, routed := Codex.Event(w[2]); !routed {
		return "", "", "", false
	}
	switch rest := w[3:]; {
	case len(rest) == 0:
		return w[0], w[2], "", true
	case len(rest) == 2 && rest[0] == "--workspace" && rest[1] != "":
		return w[0], w[2], rest[1], true
	}
	return "", "", "", false
}

// plainShellByte is a byte a shell word may carry unquoted.
func plainShellByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("_./:@%+=,-", c) >= 0
}

// ShellQuote writes s as one POSIX shell word: as is when every byte is plain, else
// single-quoted, a quote inside closing the quotes, escaped, and reopening them.
func ShellQuote(s string) string {
	plain := s != ""
	for i := 0; i < len(s) && plain; i++ {
		plain = plainShellByte(s[i])
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellWords splits what ShellQuote writes, joined by spaces: plain bytes,
// single-quoted runs and \' between them; any other syntax fails.
func shellWords(s string) ([]string, bool) {
	var words []string
	var w strings.Builder
	inWord, quoted := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted && c == '\'':
			quoted = false
		case quoted:
			w.WriteByte(c)
		case c == '\'':
			quoted, inWord = true, true
		case c == '\\' && i+1 < len(s) && s[i+1] == '\'':
			w.WriteByte('\'')
			i, inWord = i+1, true
		case c == ' ' && inWord:
			words, inWord = append(words, w.String()), false
			w.Reset()
		case c == ' ':
		case plainShellByte(c):
			w.WriteByte(c)
			inWord = true
		default:
			return nil, false
		}
	}
	if quoted {
		return nil, false
	}
	if inWord {
		words = append(words, w.String())
	}
	return words, true
}

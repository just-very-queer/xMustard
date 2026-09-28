package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Per-client budgets and output-shape adapters (PAR-CTX-03). A projection only helps
// if the client accepts it: Claude Code ignores an updatedToolOutput for a built-in
// tool unless it matches that tool's output schema, Codex substitutes a PostToolUse
// "block" reason, Pi and OpenCode take their own result objects, Cursor can replace
// MCP results only. ShapeOutput builds the client payload from the reducer's
// per-section projections and the decoded body's skeleton, validates it with a
// validator that is independent of the builder, and on a mismatch falls back: the
// client keeps its original output when that fits the client budget, else it gets an
// explicit size error. The nine xMustard tools are not shaped here.
//
// Each client is one entry of clientPolicies: its budget and its shaper, which names,
// builds and validates the client's payload and knows how the client names another
// MCP server's tools. Adding a client adds one entry.

// ClientPolicy is one client's delivery budget and replacement seam.
type ClientPolicy struct {
	Client string `json:"client"`
	// Aliases are other names of the same client (the MCP client profile claude-code).
	Aliases []string `json:"aliases,omitempty"`
	// Target: outputs at or below it pass unchanged; larger are reduced to about it.
	Target int `json:"projection_target_bytes"`
	// MaxChars caps the text a replacement carries: characters, or UTF-8 bytes when
	// MaxUnit is UnitBytes.
	MaxChars int    `json:"max_chars"`
	MaxUnit  string `json:"max_chars_unit,omitempty"`
	// ContextChars / ContextTokens cap injected additional context.
	ContextChars  int    `json:"additional_context_chars,omitempty"`
	ContextTokens int    `json:"additional_context_tokens,omitempty"`
	Replacement   string `json:"replacement"`
	shaper        shaper
}

// UnitBytes is the MaxUnit of a client that measures replacement text in bytes.
const UnitBytes = "bytes"

// Codex spills hook text past 2,500 approximate tokens of 4 bytes each: the reason is
// written to a temp file and the model gets a head-and-tail preview plus the file's
// path (codex-rs hooks/src/output_spill.rs DEFAULT_HOOK_OUTPUT_TOKEN_LIMIT;
// utils/string/src/truncate.rs approx_token_count). A whole block reason must fit in
// codexHookTextBytes; the projection leaves codexFooterReserve of it for the recovery
// line and the data-framing note.
const (
	codexHookTextBytes = 2500 * 4
	codexFooterReserve = 1536
)

// clientPolicies is the adapter policy table. Targets lower the 64 KiB default where
// the client's own limits make a smaller projection strictly better; caps come from
// the clients' documented behavior (see docs/research/PARITY_REQUIREMENTS §4.11,
// §6.10 and §12.2) and, for Codex, its source.
var clientPolicies = map[string]ClientPolicy{
	// Claude Code clamps Bash output at 30,000 characters and caps hook
	// additionalContext at 10,000; built-in replacements must match the tool schema
	"claude": {Client: "claude", Aliases: []string{"claude-code"}, Target: 16 << 10, MaxChars: 30000, ContextChars: 10000,
		Replacement: "PostToolUse hookSpecificOutput.updatedToolOutput (schema-matched for built-ins)", shaper: claudeShaper{}},
	// Codex: PostToolUse decision "block" substitutes the reason as the result;
	// additionalContextLimit defaults to 2,500 tokens and never raises the spill limit
	"codex": {Client: "codex", Target: codexHookTextBytes - codexFooterReserve, MaxChars: codexHookTextBytes, MaxUnit: UnitBytes,
		ContextTokens: 2500, Replacement: "PostToolUse decision=block reason", shaper: codexShaper{}},
	// Cursor replaces MCP results only (updated_mcp_tool_output); built-ins are observe-only
	"cursor": {Client: "cursor", Target: 16 << 10, MaxChars: 30000, Replacement: "postToolUse updated_mcp_tool_output (MCP tools only)",
		shaper: cursorShaper{}},
	// Pi truncates its own tool output at 50 KB / 2,000 lines
	"pi": {Client: "pi", Target: 32 << 10, MaxChars: 50000, Replacement: "tool_result {content, details, isError}", shaper: piShaper{}},
	// OpenCode tool.execute.after mutates {title, output, metadata}
	"opencode": {Client: "opencode", Target: 32 << 10, MaxChars: 50000, Replacement: "tool.execute.after {title, output, metadata}",
		shaper: opencodeShaper{}},
	// Letta Code reaches MCP through `letta mcp call` in Bash, clamped at 30,000 chars
	"letta": {Client: "letta", Target: 24 << 10, MaxChars: 30000, ContextChars: 10000, Replacement: "MCP result via letta mcp call (30k Bash clamp)",
		shaper: mcpShaper{}},
	// MCP clients persist results above 25k tokens instead of showing them
	"mcp":  {Client: "mcp", Target: DefaultProjectionTarget, MaxChars: 80000, Replacement: "MCP CallToolResult content", shaper: mcpShaper{}},
	"http": {Client: "http", Target: DefaultProjectionTarget, MaxChars: 1 << 20, Replacement: "evidence envelope", shaper: mcpShaper{}},
}

// clientNames resolves every client name and alias to its clientPolicies key.
var clientNames = func() map[string]string {
	names := map[string]string{}
	for key, p := range clientPolicies {
		names[key] = key
		for _, a := range p.Aliases {
			names[a] = key
		}
	}
	return names
}()

// LookupClient returns the policy of a known client name or alias.
func LookupClient(client string) (ClientPolicy, bool) {
	key, ok := clientNames[strings.ToLower(strings.TrimSpace(client))]
	return clientPolicies[key], ok
}

// ClientNames lists every client name and alias LookupClient accepts, sorted.
func ClientNames() []string { return slices.Sorted(maps.Keys(clientNames)) }

// PolicyFor returns a client's policy (the http policy for unknown clients).
func PolicyFor(client string) ClientPolicy {
	if p, ok := LookupClient(client); ok {
		return p
	}
	return clientPolicies["http"]
}

// ClientPolicies lists the policy table in client order.
func ClientPolicies() []ClientPolicy {
	out := make([]ClientPolicy, 0, len(clientPolicies))
	for _, p := range clientPolicies {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Client < out[j].Client })
	return out
}

// ForeignTool reports a tool of another MCP server, by the client's naming.
func (p ClientPolicy) ForeignTool(tool string) bool { return p.shaper.foreignTool(tool) }

// size measures payload text in the policy's unit.
func (p ClientPolicy) size(payload []byte) (int, string) {
	if p.MaxUnit == UnitBytes {
		return textSize(payload, func(s string) int { return len(s) }), UnitBytes
	}
	return textSize(payload, utf8.RuneCountInString), "characters"
}

// capContext bounds an injected notice: ContextChars, else ContextTokens at a
// conservative 3 characters (bytes, for a byte-measured client) per token; neither
// set leaves it whole.
func (p ClientPolicy) capContext(s string) string {
	n := p.ContextChars
	if n == 0 {
		n = p.ContextTokens * 3
	}
	if p.MaxUnit == UnitBytes {
		return capBytes(s, n)
	}
	return capChars(s, n)
}

// Shape modes.
const (
	ShapeReplace   = "replace"           // Payload replaces the tool output
	ShapeUnchanged = "unchanged"         // nothing was reduced: the client keeps its output
	ShapeObserve   = "observe"           // the client cannot replace this tool's output
	ShapeFallback  = "fallback_original" // the shaped payload was rejected; the original fits the budget
	ShapeSizeError = "size_error"        // rejected, and the original exceeds the budget
)

// ShapeResult is a client-shaped delivery.
type ShapeResult struct {
	Client  string          `json:"client"`
	Shape   string          `json:"shape"`
	Mode    string          `json:"mode"`
	Payload json.RawMessage `json:"payload,omitempty"`
	// Notice is the text to inject instead (additional context) on fallback or size
	// error, within the client's context cap.
	Notice string `json:"notice,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Chars is the number of characters of text the payload delivers.
	Chars int `json:"chars,omitempty"`
}

var errUnshapable = errors.New("payload cannot be rebuilt")

// ShapeInput is what ShapeOutput needs.
type ShapeInput struct {
	Client   string
	Tool     string
	Body     *HookBody   // decoded body (nil for raw captures)
	Proj     *Projection // the reducer's result
	Reduced  bool        // something was omitted
	Footer   string      // "[xmustard evidence] {...}" recovery line
	Note     string      // data-framing line for instruction-like text (WS-56), or ""
	RawBytes int64       // size of the captured original
	// IsError is the capture's error status (a failing hook body, is_error, a non-zero
	// exit code, a failing status member): every isError field of a payload says it.
	IsError bool
}

// footer is what a payload ends with: the recovery line, then the data-framing note.
func (in ShapeInput) footer() string { return joinLines(in.Footer, in.Note) }

// text is the projection followed by the footer.
func (in ShapeInput) text() string { return in.Proj.Text + "\n" + in.footer() }

func (in ShapeInput) isError() bool { return in.IsError || (in.Body != nil && in.Body.IsError) }

// responseMember is the member of an object response named key, or nil.
func (in ShapeInput) responseMember(key string) *Node {
	if in.Body == nil || in.Body.Response == nil || in.Body.Response.Kind != 'o' {
		return nil
	}
	for _, k := range in.Body.Response.Kids {
		if k.Key == key {
			return k
		}
	}
	return nil
}

func joinLines(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n")
}

// ShapeOutput builds and validates the client payload for one reduced capture.
func ShapeOutput(in ShapeInput) *ShapeResult {
	pol := PolicyFor(in.Client)
	name, replaceable := pol.shaper.shape(in.Tool)
	res := &ShapeResult{Client: pol.Client, Shape: name}
	switch {
	case !in.Reduced:
		res.Mode = ShapeUnchanged
		return res
	case !replaceable:
		res.Mode = ShapeObserve
		res.Notice = pol.capContext(in.footer())
		return res
	}
	payload, err := pol.shaper.build(in)
	if err == nil {
		err = validatePayload(pol, in.Tool, payload)
	}
	if err == nil {
		err = checkSize(pol, payload)
	}
	if err == nil {
		res.Mode, res.Payload, res.Chars = ShapeReplace, payload, textSize(payload, utf8.RuneCountInString)
		return res
	}
	res.Reason = err.Error()
	notice := fmt.Sprintf("[xmustard] the %d-byte %s output could not be shaped for %s (%s). %s", in.RawBytes, in.Tool, pol.Client, err, in.footer())
	if in.RawBytes <= int64(pol.MaxChars) {
		res.Mode = ShapeFallback // the client keeps its original output
	} else {
		res.Mode = ShapeSizeError
	}
	res.Notice = pol.capContext(notice)
	return res
}

// shapeName names the payload for a client tool.
func shapeName(client, tool string) string {
	name, _ := PolicyFor(client).shaper.shape(tool)
	return name
}

func capChars(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// capBytes cuts s to at most n bytes on a rune boundary, the last three an ellipsis.
func capBytes(s string, n int) string {
	const ellipsis = "…"
	if n <= len(ellipsis) || len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n-len(ellipsis)], "") + ellipsis
}

// --- the per-client shapers ---

// shaper renders one client's replacement payload. build and validate are
// independent: validate checks the payload against the client's schema without
// trusting what build produced.
type shaper interface {
	// shape names the payload for a tool; replaceable is false when the client can
	// only observe that tool's output.
	shape(tool string) (name string, replaceable bool)
	build(in ShapeInput) (json.RawMessage, error)
	// validate checks a decoded payload (json.Number for numbers).
	validate(tool string, v any) error
	// foreignTool reports another MCP server's tool, by the client's naming.
	foreignTool(tool string) bool
}

// namespaced is the tool naming of clients whose MCP tools carry a server namespace
// (NamespacedTool).
type namespaced struct{}

func (namespaced) foreignTool(tool string) bool { return NamespacedTool(tool) }

// textOnly refuses a body that a payload of projection text cannot carry whole.
func textOnly(in ShapeInput) error {
	switch {
	case in.Body == nil:
		return nil
	case in.Body.StatusDropped:
		// the text carries the status line; a status member it could not hold would be lost
		return fmt.Errorf("%w: the tool response has status members the projection cannot carry", errUnshapable)
	case in.Body.Dropped > 0:
		return fmt.Errorf("%w: %d image blocks cannot be carried by a text projection", errUnshapable, in.Body.Dropped)
	}
	return nil
}

func textContent(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text}}
}

// mcpShaper replaces a result with one MCP text content block: MCP clients, Letta
// (through `letta mcp call`) and the http envelope.
type mcpShaper struct{ namespaced }

func (mcpShaper) shape(string) (string, bool) { return "mcp.call_tool_result", true }

func (mcpShaper) build(in ShapeInput) (json.RawMessage, error) {
	if err := textOnly(in); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"content": textContent(in.text()), "isError": in.isError()})
}

func (mcpShaper) validate(_ string, v any) error {
	return fields(asObject(v), "mcp", req("content", isContentBlocks(false)), opt("isError", isBool))
}

// cursorShaper replaces MCP results only; Cursor's built-in tools are observe-only.
type cursorShaper struct{ mcpShaper }

func (c cursorShaper) shape(tool string) (string, bool) {
	if c.foreignTool(tool) {
		return "cursor.mcp_result", true
	}
	return "cursor.observe", false
}

// codexShaper returns a PostToolUse decision=block whose reason replaces the result.
type codexShaper struct{ namespaced }

func (codexShaper) shape(string) (string, bool) { return "codex.decision_block", true }

func (codexShaper) build(in ShapeInput) (json.RawMessage, error) {
	if err := textOnly(in); err != nil {
		return nil, err
	}
	// the recovery line goes last: a reason past the spill limit keeps its head and
	// tail, so the handle survives the cut
	return json.Marshal(map[string]any{"decision": "block", "reason": in.Proj.Text + "\n" + joinLines(in.Note, in.Footer)})
}

func (codexShaper) validate(_ string, v any) error {
	obj := asObject(v)
	if err := fields(obj, "codex", req("decision", isString), req("reason", isString)); err != nil {
		return err
	}
	if obj["decision"] != "block" || obj["reason"] == "" {
		return errors.New(`shape: codex replacement needs decision "block" and a reason`)
	}
	return nil
}

// piShaper returns a tool_result {content, details, isError}; details keep their
// structure with each text section projected.
type piShaper struct{ namespaced }

func (piShaper) shape(string) (string, bool) { return "pi.tool_result", true }

func (piShaper) build(in ShapeInput) (json.RawMessage, error) {
	if err := textOnly(in); err != nil {
		return nil, err
	}
	if in.Body != nil && in.Body.Incomplete {
		return nil, fmt.Errorf("%w: details exceed the skeleton bounds", errUnshapable)
	}
	out := map[string]any{"content": textContent(in.text()), "isError": in.isError()}
	if k := in.responseMember("details"); k != nil {
		raw, err := rebuild(k, in, nil)
		if err != nil {
			return nil, err
		}
		out["details"] = raw
	}
	return json.Marshal(out)
}

func (piShaper) validate(_ string, v any) error {
	return fields(asObject(v), "pi", req("content", isContentBlocks(true)), opt("isError", isBool))
}

// opencodeShaper returns tool.execute.after's {title, output, metadata}.
type opencodeShaper struct{}

func (opencodeShaper) shape(string) (string, bool) { return "opencode.output", true }

func (opencodeShaper) build(in ShapeInput) (json.RawMessage, error) {
	if err := textOnly(in); err != nil {
		return nil, err
	}
	if in.Body != nil && in.Body.Incomplete {
		return nil, fmt.Errorf("%w: metadata exceeds the skeleton bounds", errUnshapable)
	}
	title := in.Tool
	if k := in.responseMember("title"); k != nil && k.Kind == 'v' {
		var s string
		if json.Unmarshal(k.Raw, &s) == nil {
			title = s
		}
	}
	var meta json.RawMessage = []byte("{}")
	if k := in.responseMember("metadata"); k != nil {
		raw, err := rebuild(k, in, nil)
		if err != nil {
			return nil, err
		}
		meta = raw
	}
	return json.Marshal(map[string]any{"title": title, "output": in.text(), "metadata": meta})
}

func (opencodeShaper) validate(_ string, v any) error {
	return fields(asObject(v), "opencode", req("title", isString), req("output", isString), req("metadata", isObject))
}

// openCodeUnderscored are the OpenCode built-ins whose names hold an underscore. Any
// other underscored name is another server's MCP tool, named <server>_<tool>
// (opencode mcp/catalog.ts toolName), or a plugin's <namespace>_<tool>.
var openCodeUnderscored = map[string]bool{"apply_patch": true, "plan_enter": true, "plan_exit": true}

func (opencodeShaper) foreignTool(tool string) bool {
	return NamespacedTool(tool) || strings.Contains(tool, "_") && !openCodeUnderscored[strings.ToLower(tool)]
}

// claudeShaper rebuilds the tool_response with each section projected: Claude Code
// takes a built-in's updatedToolOutput only when it matches the tool's output schema,
// and any other tool's when it keeps the original structure.
type claudeShaper struct{ namespaced }

// claudeTool is one Claude Code built-in: validate checks its payload schema; fixups
// adjust the scalars that describe the replaced text (Read numLines, Grep numLines,
// Glob truncated) so the payload stays self-consistent.
type claudeTool struct {
	validate func(v any) error
	fixups   func(b *HookBody) map[string]fixup
}

// fixup renders a scalar from the rendered text of the sections, by name.
type fixup func(parts map[string]string) json.RawMessage

func linesOf(section string) fixup {
	return func(p map[string]string) json.RawMessage {
		return json.RawMessage(fmt.Sprint(countLines([]byte(p[section]))))
	}
}

var claudeBuiltins = map[string]claudeTool{
	"Bash": {validate: func(v any) error {
		return fields(asObject(v), "Bash", req("stdout", isString), req("stderr", isString), req("interrupted", isBool), opt("isImage", isBool))
	}},
	"Read": {validate: validateClaudeRead, fixups: func(*HookBody) map[string]fixup {
		return map[string]fixup{"file.numLines": linesOf("file.content")}
	}},
	"Grep": {validate: validateClaudeGrep, fixups: func(b *HookBody) map[string]fixup {
		if b.Scalar("numLines") == "" {
			return nil
		}
		return map[string]fixup{"numLines": linesOf("content")}
	}},
	"Glob": {validate: func(v any) error {
		return fields(asObject(v), "Glob", req("filenames", isStringArray), req("numFiles", isNumber), req("truncated", isBool), opt("durationMs", isNumber))
	}, fixups: func(*HookBody) map[string]fixup {
		return map[string]fixup{"truncated": func(map[string]string) json.RawMessage { return json.RawMessage("true") }}
	}},
	"LS":       {validate: notNull},
	"WebFetch": {validate: notNull},
}

func (claudeShaper) shape(tool string) (string, bool) {
	if _, ok := claudeBuiltins[tool]; ok {
		return "claude." + tool, true
	}
	return "claude.tool_response", true
}

func (claudeShaper) build(in ShapeInput) (json.RawMessage, error) {
	if in.Body == nil || in.Body.Response == nil {
		return nil, fmt.Errorf("%w: no tool_response in the hook body", errUnshapable)
	}
	if in.Body.Incomplete || in.Body.Dropped > 0 {
		return nil, fmt.Errorf("%w: tool_response has binary or unbounded structure", errUnshapable)
	}
	t, builtin := claudeBuiltins[in.Tool]
	var fix map[string]fixup
	if t.fixups != nil {
		fix = t.fixups(in.Body)
	}
	out, err := rebuild(in.Body.Response, in, fix)
	if err != nil || builtin {
		return out, err
	}
	// any other tool: every original member is back with the same JSON kind
	return out, sameShape(in.Body.Response, out)
}

func (claudeShaper) validate(tool string, v any) error {
	if t, ok := claudeBuiltins[tool]; ok {
		return t.validate(v)
	}
	return notNull(v)
}

func validateClaudeRead(v any) error {
	obj := asObject(v)
	if err := fields(obj, "Read", req("type", isString), req("file", isObject)); err != nil {
		return err
	}
	if obj["type"] != "text" {
		return fmt.Errorf("shape: Read type %v is not replaceable", obj["type"])
	}
	return fields(asObject(obj["file"]), "Read.file", req("filePath", isString), req("content", isString), req("numLines", isNumber),
		req("startLine", isNumber), req("totalLines", isNumber))
}

var claudeGrepModes = map[string]bool{"content": true, "files_with_matches": true, "count": true}

func validateClaudeGrep(v any) error {
	obj := asObject(v)
	if err := fields(obj, "Grep", req("numFiles", isNumber), req("filenames", isStringArray), opt("mode", isString),
		opt("content", isString), opt("numLines", isNumber)); err != nil {
		return err
	}
	if m, ok := obj["mode"].(string); ok && !claudeGrepModes[m] {
		return fmt.Errorf("shape: Grep mode %q", m)
	}
	return nil
}

func notNull(v any) error {
	if v == nil {
		return errors.New("shape: empty payload")
	}
	return nil
}

// primarySection is the largest output section: the recovery line goes there.
func primarySection(b *HookBody) int {
	best := -1
	for i, s := range b.Sections {
		if s.Status {
			continue
		}
		if best < 0 || s.End-s.Start > b.Sections[best].End-b.Sections[best].Start {
			best = i
		}
	}
	return best
}

// rebuild renders a skeleton value with each section replaced by its projection.
// The footer is appended to the first text section (the primary output).
func rebuild(root *Node, in ShapeInput, fix map[string]fixup) (json.RawMessage, error) {
	parts := map[string]string{} // rendered text per section name
	primary := -1
	if in.Body != nil {
		primary = primarySection(in.Body)
	}
	footer := in.footer()
	var buf bytes.Buffer
	var walk func(n *Node, path string) error
	walk = func(n *Node, path string) error {
		switch n.Kind {
		case 'o':
			buf.WriteByte('{')
			for i, k := range n.Kids {
				if i > 0 {
					buf.WriteByte(',')
				}
				key, _ := json.Marshal(k.Key)
				buf.Write(key)
				buf.WriteByte(':')
				if err := walk(k, joinPath(path, k.Key)); err != nil {
					return err
				}
			}
			buf.WriteByte('}')
		case 'a':
			buf.WriteByte('[')
			for i, k := range n.Kids {
				if i > 0 {
					buf.WriteByte(',')
				}
				if err := walk(k, path); err != nil {
					return err
				}
			}
			buf.WriteByte(']')
		case 's', 'l':
			if in.Body == nil || n.Section >= len(in.Body.Sections) {
				return errUnshapable
			}
			name := in.Body.Sections[n.Section].Name
			text, ok := in.Proj.Parts[name]
			if !ok {
				return fmt.Errorf("%w: no projection for section %q", errUnshapable, name)
			}
			if n.Section == primary && footer != "" {
				text = strings.TrimRight(text, "\n") + "\n" + footer
			}
			parts[name] = text
			if n.Kind == 'l' {
				lines := []string{} // an empty list stays []
				if t := strings.TrimRight(text, "\n"); t != "" {
					lines = strings.Split(t, "\n")
				}
				raw, _ := json.Marshal(lines)
				buf.Write(raw)
				return nil
			}
			raw, _ := json.Marshal(text)
			buf.Write(raw)
		case 'v':
			if f := fix[path]; f != nil {
				buf.WriteString("\x00" + path + "\x00") // placeholder, filled after all parts are known
				return nil
			}
			buf.Write(n.Raw)
		default:
			return fmt.Errorf("%w: dropped value at %q", errUnshapable, path)
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	for path, f := range fix {
		out = bytes.Replace(out, []byte("\x00"+path+"\x00"), f(parts), 1)
	}
	if !json.Valid(out) {
		return nil, fmt.Errorf("%w: rebuilt payload is not valid JSON", errUnshapable)
	}
	return out, nil
}

// --- validators (independent of the builder) ---

// ValidateShape checks that payload is a valid replacement for the client tool.
func ValidateShape(client, tool string, payload []byte) error {
	pol, ok := LookupClient(client)
	if !ok {
		return fmt.Errorf("shape: unknown client %q", client)
	}
	return validatePayload(pol, tool, payload)
}

func validatePayload(pol ClientPolicy, tool string, payload []byte) error {
	var v any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("shape: payload is not JSON: %w", err)
	}
	return pol.shaper.validate(tool, v)
}

func checkSize(pol ClientPolicy, payload []byte) error {
	if pol.MaxChars <= 0 {
		return nil
	}
	if n, unit := pol.size(payload); n > pol.MaxChars {
		return fmt.Errorf("shape: %d %s exceed the client cap of %d", n, unit, pol.MaxChars)
	}
	return nil
}

type fieldRule struct {
	name     string
	required bool
	check    func(any) bool
}

func req(name string, check func(any) bool) fieldRule { return fieldRule{name, true, check} }
func opt(name string, check func(any) bool) fieldRule { return fieldRule{name, false, check} }

func fields(obj map[string]any, what string, rules ...fieldRule) error {
	if obj == nil {
		return fmt.Errorf("shape: %s payload must be a JSON object", what)
	}
	for _, r := range rules {
		v, ok := obj[r.name]
		if !ok {
			if r.required {
				return fmt.Errorf("shape: %s.%s is required", what, r.name)
			}
			continue
		}
		if !r.check(v) {
			return fmt.Errorf("shape: %s.%s has the wrong type", what, r.name)
		}
	}
	return nil
}

func asObject(v any) map[string]any { obj, _ := v.(map[string]any); return obj }

func isString(v any) bool { _, ok := v.(string); return ok }
func isBool(v any) bool   { _, ok := v.(bool); return ok }
func isNumber(v any) bool { _, ok := v.(json.Number); return ok }
func isObject(v any) bool { _, ok := v.(map[string]any); return ok }
func isStringArray(v any) bool {
	a, ok := v.([]any)
	if !ok {
		return false
	}
	for _, e := range a {
		if !isString(e) {
			return false
		}
	}
	return true
}

// isContentBlocks accepts MCP/Pi content arrays: text blocks, and image blocks when
// images are allowed.
func isContentBlocks(images bool) func(any) bool {
	return func(v any) bool {
		a, ok := v.([]any)
		if !ok || len(a) == 0 {
			return false
		}
		for _, e := range a {
			b, ok := e.(map[string]any)
			if !ok {
				return false
			}
			switch b["type"] {
			case "text":
				if !isString(b["text"]) {
					return false
				}
			case "image":
				if !images || !isString(b["data"]) || !isString(b["mimeType"]) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
}

// sameShape checks a rebuilt payload against the original skeleton: every original
// member is present with the same JSON kind.
func sameShape(n *Node, payload []byte) error {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return err
	}
	return matchNode(n, v, "")
}

func matchNode(n *Node, v any, path string) error {
	bad := func() error {
		return fmt.Errorf("shape: %s differs from the original structure", strings.TrimPrefix(path, "."))
	}
	switch n.Kind {
	case 'o':
		m, ok := v.(map[string]any)
		if !ok {
			return bad()
		}
		for _, k := range n.Kids {
			kv, ok := m[k.Key]
			if !ok {
				return fmt.Errorf("shape: %s.%s missing", strings.TrimPrefix(path, "."), k.Key)
			}
			if err := matchNode(k, kv, path+"."+k.Key); err != nil {
				return err
			}
		}
	case 'a':
		a, ok := v.([]any)
		if !ok || len(a) != len(n.Kids) {
			return bad()
		}
		for i, k := range n.Kids {
			if err := matchNode(k, a[i], fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case 's':
		if !isString(v) {
			return bad()
		}
	case 'l':
		if !isStringArray(v) {
			return bad()
		}
	case 'v':
		var orig any
		_ = json.Unmarshal(n.Raw, &orig)
		if fmt.Sprintf("%T", orig) != fmt.Sprintf("%T", v) {
			return bad()
		}
	}
	return nil
}

// textSize sums the size of every string in a payload, measured by size (len for
// bytes, utf8.RuneCountInString for characters).
func textSize(payload []byte, size func(string) int) int {
	var v any
	if json.Unmarshal(payload, &v) != nil {
		return 0
	}
	n := 0
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			n += size(t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return n
}

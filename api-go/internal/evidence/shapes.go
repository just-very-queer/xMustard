package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// ClientPolicy is one client's delivery budget and replacement seam.
type ClientPolicy struct {
	Client string `json:"client"`
	// Target: outputs at or below it pass unchanged; larger are reduced to about it.
	Target int `json:"projection_target_bytes"`
	// MaxChars caps the text a replacement carries (characters).
	MaxChars int `json:"max_chars"`
	// ContextChars / ContextTokens cap injected additional context.
	ContextChars  int    `json:"additional_context_chars,omitempty"`
	ContextTokens int    `json:"additional_context_tokens,omitempty"`
	Replacement   string `json:"replacement"`
}

// clientPolicies is the adapter policy table. Targets lower the 64 KiB default where
// the client's own limits make a smaller projection strictly better; caps come from
// the clients' documented behavior (see docs/research/PARITY_REQUIREMENTS §4.11,
// §6.10 and §12.2).
var clientPolicies = map[string]ClientPolicy{
	// Claude Code clamps Bash output at 30,000 characters and caps hook
	// additionalContext at 10,000; built-in replacements must match the tool schema
	"claude": {Client: "claude", Target: 16 << 10, MaxChars: 30000, ContextChars: 10000, Replacement: "PostToolUse hookSpecificOutput.updatedToolOutput (schema-matched for built-ins)"},
	// Codex: PostToolUse decision "block" substitutes the reason as the result;
	// additionalContextLimit defaults to 2,500 tokens
	"codex": {Client: "codex", Target: 16 << 10, MaxChars: 30000, ContextTokens: 2500, Replacement: "PostToolUse decision=block reason"},
	// Cursor replaces MCP results only (updated_mcp_tool_output); built-ins are observe-only
	"cursor": {Client: "cursor", Target: 16 << 10, MaxChars: 30000, Replacement: "postToolUse updated_mcp_tool_output (MCP tools only)"},
	// Pi truncates its own tool output at 50 KB / 2,000 lines
	"pi": {Client: "pi", Target: 32 << 10, MaxChars: 50000, Replacement: "tool_result {content, details, isError}"},
	// OpenCode tool.execute.after mutates {title, output, metadata}
	"opencode": {Client: "opencode", Target: 32 << 10, MaxChars: 50000, Replacement: "tool.execute.after {title, output, metadata}"},
	// Letta Code reaches MCP through `letta mcp call` in Bash, clamped at 30,000 chars
	"letta": {Client: "letta", Target: 24 << 10, MaxChars: 30000, ContextChars: 10000, Replacement: "MCP result via letta mcp call (30k Bash clamp)"},
	// MCP clients persist results above 25k tokens instead of showing them
	"mcp":  {Client: "mcp", Target: DefaultProjectionTarget, MaxChars: 80000, Replacement: "MCP CallToolResult content"},
	"http": {Client: "http", Target: DefaultProjectionTarget, MaxChars: 1 << 20, Replacement: "evidence envelope"},
}

// PolicyFor returns a client's policy (the http policy for unknown clients).
func PolicyFor(client string) ClientPolicy {
	if p, ok := clientPolicies[strings.ToLower(client)]; ok {
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

// shapeName names the payload for a client tool.
func shapeName(client, tool string) string {
	switch client {
	case "claude":
		if t := claudeBuiltin(tool); t != "" {
			return "claude." + t
		}
		return "claude.tool_response"
	case "codex":
		return "codex.decision_block"
	case "cursor":
		if isMCPTool(tool) {
			return "cursor.mcp_result"
		}
		return "cursor.observe"
	case "pi":
		return "pi.tool_result"
	case "opencode":
		return "opencode.output"
	}
	return "mcp.call_tool_result"
}

func claudeBuiltin(tool string) string {
	switch tool {
	case "Bash", "Read", "Grep", "Glob", "LS", "WebFetch":
		return tool
	}
	return ""
}

func isMCPTool(tool string) bool {
	return strings.HasPrefix(tool, "mcp__") || strings.HasPrefix(tool, "MCP:") || strings.HasPrefix(strings.ToLower(tool), "mcp_")
}

// ShapeInput is what ShapeOutput needs.
type ShapeInput struct {
	Client   string
	Tool     string
	Body     *HookBody   // decoded body (nil for raw captures)
	Proj     *Projection // the reducer's result
	Reduced  bool        // something was omitted
	Footer   string      // "[xmustard evidence] {...}" recovery line
	RawBytes int64       // size of the captured original
}

// ShapeOutput builds and validates the client payload for one reduced capture.
func ShapeOutput(in ShapeInput) *ShapeResult {
	pol := PolicyFor(in.Client)
	res := &ShapeResult{Client: pol.Client, Shape: shapeName(pol.Client, in.Tool)}
	switch {
	case !in.Reduced:
		res.Mode = ShapeUnchanged
		return res
	case res.Shape == "cursor.observe":
		res.Mode = ShapeObserve
		res.Notice = capChars(in.Footer, pol.ContextChars)
		return res
	}
	payload, err := buildPayload(pol, in)
	if err == nil {
		err = ValidateShape(pol.Client, in.Tool, payload)
	}
	if err == nil {
		err = checkChars(payload, pol.MaxChars)
	}
	if err == nil && in.Body != nil && in.Body.Response != nil && pol.Client == "claude" && res.Shape == "claude.tool_response" {
		err = sameShape(in.Body.Response, payload)
	}
	if err == nil {
		res.Mode, res.Payload, res.Chars = ShapeReplace, payload, textChars(payload)
		return res
	}
	res.Reason = err.Error()
	notice := fmt.Sprintf("[xmustard] the %d-byte %s output could not be shaped for %s (%s). %s", in.RawBytes, in.Tool, pol.Client, err, in.Footer)
	if in.RawBytes <= int64(pol.MaxChars) {
		res.Mode = ShapeFallback // the client keeps its original output
	} else {
		res.Mode = ShapeSizeError
	}
	cap := pol.ContextChars
	if cap == 0 && pol.ContextTokens > 0 {
		cap = pol.ContextTokens * 3 // conservative characters per token
	}
	res.Notice = capChars(notice, cap)
	return res
}

func capChars(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// buildPayload renders the client payload.
func buildPayload(pol ClientPolicy, in ShapeInput) (json.RawMessage, error) {
	text := in.Proj.Text + "\n" + in.Footer
	switch pol.Client {
	case "codex":
		return json.Marshal(map[string]any{"decision": "block", "reason": text})
	case "cursor", "mcp", "letta", "http":
		return json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": in.Body != nil && in.Body.IsError})
	case "pi":
		if in.Body != nil && in.Body.Dropped > 0 {
			return nil, fmt.Errorf("%w: %d image blocks cannot be carried by a text projection", errUnshapable, in.Body.Dropped)
		}
		if in.Body != nil && in.Body.Incomplete {
			return nil, fmt.Errorf("%w: details exceed the skeleton bounds", errUnshapable)
		}
		out := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": in.Body != nil && in.Body.IsError}
		if in.Body != nil && in.Body.Response != nil {
			for _, k := range in.Body.Response.Kids {
				if k.Key == "details" {
					raw, err := rebuild(k, in, nil)
					if err != nil {
						return nil, err
					}
					out["details"] = json.RawMessage(raw)
				}
			}
		}
		return json.Marshal(out)
	case "opencode":
		if in.Body != nil && in.Body.Incomplete {
			return nil, fmt.Errorf("%w: metadata exceeds the skeleton bounds", errUnshapable)
		}
		title := in.Tool
		var meta json.RawMessage = []byte("{}")
		if in.Body != nil && in.Body.Response != nil && in.Body.Response.Kind == 'o' {
			for _, k := range in.Body.Response.Kids {
				switch {
				case k.Key == "title" && k.Kind == 'v':
					var s string
					if json.Unmarshal(k.Raw, &s) == nil {
						title = s
					}
				case k.Key == "metadata":
					raw, err := rebuild(k, in, nil)
					if err != nil {
						return nil, err
					}
					meta = raw
				}
			}
		}
		return json.Marshal(map[string]any{"title": title, "output": text, "metadata": meta})
	case "claude":
		if in.Body == nil || in.Body.Response == nil {
			return nil, fmt.Errorf("%w: no tool_response in the hook body", errUnshapable)
		}
		if in.Body.Incomplete || in.Body.Dropped > 0 {
			return nil, fmt.Errorf("%w: tool_response has binary or unbounded structure", errUnshapable)
		}
		fix := claudeFixups(in)
		return rebuild(in.Body.Response, in, fix)
	}
	return nil, fmt.Errorf("%w: unknown client %q", errUnshapable, pol.Client)
}

// claudeFixups adjusts scalars that describe the replaced text (Read numLines, Grep
// numLines, Glob truncated) so the payload stays self-consistent. Each fixup reads
// the rendered text of the section it describes, by name.
func claudeFixups(in ShapeInput) map[string]func(parts map[string]string) json.RawMessage {
	fix := map[string]func(map[string]string) json.RawMessage{}
	lines := func(s string) json.RawMessage { return json.RawMessage(fmt.Sprint(countLines([]byte(s)))) }
	switch in.Tool {
	case "Read":
		fix["file.numLines"] = func(p map[string]string) json.RawMessage { return lines(p["file.content"]) }
	case "Grep":
		if in.Body.Scalar("numLines") != "" {
			fix["numLines"] = func(p map[string]string) json.RawMessage { return lines(p["content"]) }
		}
	case "Glob":
		fix["truncated"] = func(map[string]string) json.RawMessage { return json.RawMessage("true") }
	}
	return fix
}

// primarySection is the largest output section: the recovery line goes there.
func primarySection(b *HookBody) int {
	best := -1
	for i, s := range b.Sections {
		if best < 0 || s.End-s.Start > b.Sections[best].End-b.Sections[best].Start {
			best = i
		}
	}
	return best
}

// rebuild renders a skeleton value with each section replaced by its projection.
// The footer is appended to the first text section (the primary output).
func rebuild(root *Node, in ShapeInput, fix map[string]func(map[string]string) json.RawMessage) (json.RawMessage, error) {
	parts := map[string]string{} // rendered text per section name
	primary := -1
	if in.Body != nil {
		primary = primarySection(in.Body)
	}
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
			if n.Section == primary && in.Footer != "" {
				text = strings.TrimRight(text, "\n") + "\n" + in.Footer
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
	var v any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("shape: payload is not JSON: %w", err)
	}
	obj, _ := v.(map[string]any)
	switch client {
	case "claude":
		switch claudeBuiltin(tool) {
		case "Bash":
			return fields(obj, "Bash", req("stdout", isString), req("stderr", isString), req("interrupted", isBool), opt("isImage", isBool))
		case "Read":
			if err := fields(obj, "Read", req("type", isString), req("file", isObject)); err != nil {
				return err
			}
			if obj["type"] != "text" {
				return fmt.Errorf("shape: Read type %v is not replaceable", obj["type"])
			}
			file, _ := obj["file"].(map[string]any)
			return fields(file, "Read.file", req("filePath", isString), req("content", isString), req("numLines", isNumber),
				req("startLine", isNumber), req("totalLines", isNumber))
		case "Grep":
			if err := fields(obj, "Grep", req("numFiles", isNumber), req("filenames", isStringArray), opt("mode", isString),
				opt("content", isString), opt("numLines", isNumber)); err != nil {
				return err
			}
			if m, ok := obj["mode"].(string); ok && m != "content" && m != "files_with_matches" && m != "count" {
				return fmt.Errorf("shape: Grep mode %q", m)
			}
			return nil
		case "Glob":
			return fields(obj, "Glob", req("filenames", isStringArray), req("numFiles", isNumber), req("truncated", isBool), opt("durationMs", isNumber))
		case "LS", "WebFetch":
			if v == nil {
				return errors.New("shape: empty payload")
			}
			return nil
		}
		if v == nil {
			return errors.New("shape: empty payload")
		}
		return nil
	case "codex":
		if err := fields(obj, "codex", req("decision", isString), req("reason", isString)); err != nil {
			return err
		}
		if obj["decision"] != "block" || obj["reason"] == "" {
			return errors.New(`shape: codex replacement needs decision "block" and a reason`)
		}
		return nil
	case "pi":
		if err := fields(obj, "pi", req("content", isContentBlocks(true)), opt("isError", isBool)); err != nil {
			return err
		}
		return nil
	case "opencode":
		return fields(obj, "opencode", req("title", isString), req("output", isString), req("metadata", isObject))
	case "cursor", "mcp", "letta", "http":
		return fields(obj, client, req("content", isContentBlocks(false)), opt("isError", isBool))
	}
	return fmt.Errorf("shape: unknown client %q", client)
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

// textChars counts the characters of every string in a payload.
func textChars(payload []byte) int {
	var v any
	if json.Unmarshal(payload, &v) != nil {
		return 0
	}
	n := 0
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			n += utf8.RuneCountInString(t)
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

func checkChars(payload []byte, maxChars int) error {
	if maxChars > 0 {
		if n := textChars(payload); n > maxChars {
			return fmt.Errorf("shape: %d characters exceed the client cap of %d", n, maxChars)
		}
	}
	return nil
}

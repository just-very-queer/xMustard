// Package toolcompat normalizes and validates tool-call arguments before a tool
// runs or an adapter reads them.
//
// Agents misspell arguments ("cmd" for "command", "filePath" for "path",
// "read_file@filesystem" for a tool plus server). Normalize maps each argument
// key to its canonical name, applies per-tool repairs, checks required fields
// and types, and returns every change it made as a Normalization record so the
// caller can attach them to evidence. Nothing is changed silently.
//
// Two kinds of spec are supported:
//
//   - A closed spec (Spec.Fields set) describes one of xMustard's nine MCP
//     tools. Keys resolve against the spec's own fields, per-tool vocabulary,
//     and the harness alias table; unknown keys are rejected.
//   - An open spec (no Fields) describes a harness tool family (shell, read_file,
//     apply_patch, call_mcp_tool, ...). Keys resolve through the alias table,
//     unknown keys are kept, and the ported per-kind repairs and required-field
//     checks apply.
//
// Mutating tools (remember and verify always, or any spec with Mutating set)
// never have their arguments changed, keys included: Result.Args is the input
// as given. Every normalization another tool would get (key rename, duplicate
// drop, value repair, type coercion) is only reported, with Applied=false, and
// the call fails validation: with the ordinary type, enum, required or unknown
// error when the canonical spelling is itself invalid, and otherwise with
// CodeNeedsRepair naming the canonical field. A mutating call therefore either
// validates with no normalizations and its arguments exactly as sent, or fails.
//
// The alias table, per-kind repairs and required-field rules are ported from the
// owner's cursor-bridge (bridge_tool_compat.go); validation signatures use
// SHA-256 instead of cursor-bridge's SHA-1.
//
// Intended callers:
//
//   - MCP tools/call: build a closed Spec per tool from the tool table (fields,
//     types, enums, required), Normalize the decoded arguments, answer a
//     ValidationError as invalid params with the error as data, and attach
//     Result.Normalizations to the call's evidence metadata.
//   - Hook adapters: KindForName(tool_name), then Normalize(Spec{Kind: k},
//     tool_input) to read canonical fields (command, cwd, path, pattern,
//     glob_pattern, tool, server). The result describes the call; do not write
//     it back into a client's tool input without reporting the change.
//   - Loop guards: ValidationError.Signature identifies a repeated failure.
package toolcompat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"xmustard/api-go/internal/redact"
)

// Kind names a tool family whose arguments toolcompat can repair and validate.
type Kind string

// xMustard's nine MCP tools.
const (
	KindGround      Kind = "ground"
	KindRecall      Kind = "recall"
	KindRemember    Kind = "remember"
	KindVerify      Kind = "verify"
	KindSearch      Kind = "search"
	KindExplain     Kind = "explain"
	KindImpact      Kind = "impact"
	KindDiagnostics Kind = "diagnostics"
	KindWhyFailed   Kind = "why_failed"
)

// Harness tool families, as seen in hook payloads and client tool calls.
const (
	KindShell            Kind = "shell"
	KindShellStdin       Kind = "write_shell_stdin"
	KindKillShell        Kind = "kill_shell"
	KindAwaitTask        Kind = "await_task"
	KindReadFile         Kind = "read_file"
	KindWriteFile        Kind = "write_file"
	KindDeleteFile       Kind = "delete_file"
	KindApplyPatch       Kind = "apply_patch"
	KindListDir          Kind = "list_dir"
	KindGlob             Kind = "glob"
	KindGrep             Kind = "grep"
	KindSearchSymbols    Kind = "search_symbols"
	KindSemanticSearch   Kind = "semantic_search"
	KindWebFetch         Kind = "web_fetch"
	KindTask             Kind = "task"
	KindCallMCPTool      Kind = "call_mcp_tool"
	KindGetMCPServer     Kind = "get_mcp_server"
	KindListMCPTools     Kind = "list_mcp_tools"
	KindListMCPResources Kind = "list_mcp_resources"
	KindReadMCPResource  Kind = "read_mcp_resource"
	KindListMCPPrompts   Kind = "list_mcp_prompts"
	KindGetMCPPrompt     Kind = "get_mcp_prompt"
)

// IsMutating reports whether a kind writes governed state. Normalize never
// rewrites the argument values of a mutating kind.
func IsMutating(k Kind) bool { return k == KindRemember || k == KindVerify }

// Type is the JSON type a field accepts.
type Type string

// Field types. The zero Type accepts any JSON value.
const (
	TypeString  Type = "string"
	TypeBoolean Type = "boolean"
	TypeInteger Type = "integer"
	TypeNumber  Type = "number"
	TypeArray   Type = "array"
	TypeObject  Type = "object"
)

// Field is one canonical argument of a closed spec.
type Field struct {
	Name     string
	Type     Type
	Enum     []string
	Required bool
}

// Spec describes the arguments of one tool.
type Spec struct {
	// Kind selects the per-kind repairs, required-field rules and mutating policy.
	Kind Kind
	// Tool names the tool in messages and signatures; it defaults to Kind.
	Tool string
	// Fields makes the spec closed: only these arguments are accepted. Leave it
	// empty for a harness tool family whose arguments are open-ended.
	Fields []Field
	// Mutating marks a tool whose argument values must never be rewritten. It is
	// always true for remember and verify.
	Mutating bool
}

func (s Spec) toolName() string {
	if s.Tool != "" {
		return s.Tool
	}
	return string(s.Kind)
}

func (s Spec) closed() bool   { return len(s.Fields) > 0 }
func (s Spec) mutating() bool { return s.Mutating || IsMutating(s.Kind) }

func (s Spec) field(name string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// Normalization operations.
const (
	OpAlias  = "alias"  // key renamed to its canonical field; the value is untouched
	OpDrop   = "drop"   // duplicate argument for a field that was already set
	OpDerive = "derive" // canonical field filled from another argument
	OpRepair = "repair" // value rewritten (path cleaned, glob fixed, tool ref split, ...)
	OpCoerce = "coerce" // value converted to the field's type
	OpRemove = "remove" // placeholder argument removed
)

// Normalization records one change Normalize made or, for a mutating tool,
// would have made. It carries argument names and a rule id, never argument
// values, so it is safe to store in evidence.
type Normalization struct {
	Op      string `json:"op"`
	Field   string `json:"field"`
	From    string `json:"from,omitempty"`
	Rule    string `json:"rule,omitempty"`
	Applied bool   `json:"applied"`
}

// Validation error codes.
const (
	CodeRequired  = "required"
	CodeType      = "type"
	CodeEnum      = "enum"
	CodeUnknown   = "unknown"
	CodeAmbiguous = "ambiguous"
	// CodeNeedsRepair: a mutating tool's argument is not in canonical form
	// (an alias key, a duplicate, or a value another tool would have repaired).
	// Field names the canonical field; Expected names the repair rule, if any.
	CodeNeedsRepair = "needs_repair"
)

// ValidationError is a structured argument error. Field is always the
// canonical field name. Signature identifies the failure (tool, normalized
// arguments and message) so a caller can detect an agent repeating it.
type ValidationError struct {
	Tool      string `json:"tool"`
	Field     string `json:"field"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Expected  string `json:"expected,omitempty"`
	Signature string `json:"signature"`
}

func (e *ValidationError) Error() string { return e.Message }

// Result is the outcome of Normalize.
type Result struct {
	// Args are the normalized arguments: a fresh deep copy; the input map is
	// never modified. For a mutating tool they are the arguments as given.
	Args map[string]any `json:"-"`
	// Normalizations lists every change, in the order it was made.
	Normalizations []Normalization `json:"normalizations,omitempty"`
	// Err is the first validation failure, or nil.
	Err *ValidationError `json:"error,omitempty"`
}

// Applied reports whether any normalization changed the arguments.
func (r Result) Applied() bool {
	for _, n := range r.Normalizations {
		if n.Applied {
			return true
		}
	}
	return false
}

// Normalize canonicalizes, repairs and validates one tool call's arguments.
func Normalize(spec Spec, args map[string]any) Result {
	tool := spec.toolName()
	mutating := spec.mutating()
	input := deepCopyMap(args)
	canon, norms, amb := canonicalizeKeys(spec, input)
	if amb != nil {
		// nothing is applied when the spellings conflict: Args is the input as given
		amb.Tool = tool
		amb.Signature = Signature(tool, input, amb.Message)
		return Result{Args: input, Err: amb}
	}

	// Repairs run on the canonical spelling; for a mutating tool, on a scratch
	// copy, so they are reported without touching the arguments.
	work := canon
	if mutating {
		work = deepCopyMap(canon)
	}
	st := &state{args: work}
	repairKind(spec.Kind, st)
	if spec.closed() {
		repairFields(spec, st)
	}
	norms = append(norms, st.norms...)
	for i := range norms {
		norms[i].Applied = !mutating
	}
	res := Result{Args: work, Normalizations: norms}
	check := work
	if mutating {
		// Validate the canonical spelling so errors name canonical fields.
		res.Args, check = input, canon
	}

	verr := validate(spec, check)
	switch {
	case verr != nil && mutating:
		for _, n := range norms {
			if n.Field == verr.Field {
				verr.Message += fmt.Sprintf(" (not repaired: %s is a mutating tool)", tool)
				break
			}
		}
	case verr == nil && mutating && len(norms) > 0:
		verr = needsRepair(tool, norms, work)
	}
	if verr != nil {
		verr.Tool = tool
		verr.Signature = Signature(tool, res.Args, verr.Message)
		res.Err = verr
	}
	return res
}

// needsRepair is the error for a mutating call whose arguments are valid in
// canonical form but were not sent that way. norms are the normalizations the
// call would have needed, and repaired the arguments with them applied.
func needsRepair(tool string, norms []Normalization, repaired map[string]any) *ValidationError {
	n := norms[0]
	var msg string
	switch n.Op {
	case OpAlias:
		msg = fmt.Sprintf("argument %q for %s must be sent as %q", n.From, tool, n.Field)
	case OpDrop:
		msg = fmt.Sprintf("argument %q for %s repeats %q; send it once, as %q", n.From, tool, n.Field, n.Field)
	case OpRemove:
		msg = fmt.Sprintf("argument %q for %s is a placeholder (%s); leave it out", n.Field, tool, n.Rule)
	default:
		if n.From != "" {
			msg = fmt.Sprintf("argument %q for %s must be sent as %q (%s)", n.From, tool, n.Field, n.Rule)
		} else {
			msg = fmt.Sprintf("argument %q for %s is not in canonical form (%s)", n.Field, tool, n.Rule)
		}
		if v, ok := canonicalValue(n.Field, norms, repaired); ok {
			msg += "; send " + v
		} else {
			msg += "; send the canonical value"
		}
	}
	msg += fmt.Sprintf(" (%s is a mutating tool: its arguments are never rewritten)", tool)
	return &ValidationError{Field: n.Field, Code: CodeNeedsRepair, Expected: n.Rule, Message: msg}
}

// structuralRules rewrite a path, glob, identifier, enum value or scalar type,
// never free text, so their result may be quoted back in an error: it tells
// the agent exactly what to send instead of inviting another guess.
var structuralRules = map[string]bool{
	"local_path": true, "glob": true, "trim": true, "list_to_csv": true,
	"enum_case": true, "integer": true, "number": true, "boolean": true,
}

// maxEchoedValue bounds a canonical value quoted in an error.
const maxEchoedValue = 512

// canonicalValue returns field's repaired value as JSON when every repair of
// the field is structural; a field that may hold user content (memory text, a
// command, a URL) is never quoted back, and neither is a value that holds a
// credential (see echoable).
func canonicalValue(field string, norms []Normalization, repaired map[string]any) (string, bool) {
	for _, n := range norms {
		if n.Field == field && !structuralRules[n.Rule] {
			return "", false
		}
	}
	v, ok := repaired[field]
	if !ok {
		return "", false
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil || b.Len() > maxEchoedValue {
		return "", false
	}
	if out := strings.TrimSuffix(b.String(), "\n"); echoable(out) {
		return out, true
	}
	return "", false
}

// userinfo is the "user:password@" of a URL or scp-style remote, with or
// without a scheme.
var userinfo = regexp.MustCompile(`[^\s/:@"]+:[^\s/@"]*@`)

// echoable reports whether a value may be quoted back in an error message,
// which callers may log: it holds nothing the redact package takes for a
// secret, and no userinfo.
func echoable(s string) bool {
	return !userinfo.MatchString(s) && redact.Default().Check(s) == nil
}

// NormalizeJSON is Normalize over a JSON object. It returns the normalized
// arguments re-encoded as JSON. An empty or null input is an empty object; any
// other non-object input, or trailing data after the object, is an error.
// Numbers decode as json.Number, so integers of any size round-trip unchanged.
func NormalizeJSON(spec Spec, raw json.RawMessage) (json.RawMessage, Result, error) {
	args := map[string]any{}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && string(trimmed) != "null" {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return nil, Result{}, fmt.Errorf("tool arguments must be a JSON object: %w", err)
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return nil, Result{}, errors.New("tool arguments must be a single JSON object")
		}
	}
	res := Normalize(spec, args)
	out, err := json.Marshal(res.Args)
	if err != nil {
		return nil, res, err
	}
	return out, res, nil
}

// Signature is the SHA-256 identity of a validation failure: tool name,
// arguments (encoded with sorted keys) and message. Equal failures have equal
// signatures regardless of argument order.
func Signature(tool string, args map[string]any, message string) string {
	payload, _ := json.Marshal(args)
	h := sha256.New()
	h.Write([]byte(tool))
	h.Write([]byte{0x1f})
	h.Write(payload)
	h.Write([]byte{0x1f})
	h.Write([]byte(message))
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalizeKeys renames every key to its canonical field. An argument
// already spelled canonically wins over an alias for the same field; the alias
// is dropped and recorded. For a mutating spec, two spellings of one field with
// different values are an ambiguity error rather than a silent choice.
func canonicalizeKeys(spec Spec, args map[string]any) (map[string]any, []Normalization, *ValidationError) {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	resolved := make(map[string]string, len(keys))
	for _, k := range keys {
		resolved[k] = resolveKey(spec, k)
	}

	out := make(map[string]any, len(args))
	source := make(map[string]string, len(args))
	for _, k := range keys {
		if resolved[k] == k {
			out[k] = args[k]
			source[k] = k
		}
	}
	var norms []Normalization
	for _, k := range keys {
		c := resolved[k]
		if c == k {
			continue
		}
		if existing, ok := out[c]; ok {
			if spec.mutating() && !reflect.DeepEqual(existing, args[k]) {
				return out, norms, &ValidationError{
					Field:   c,
					Code:    CodeAmbiguous,
					Message: fmt.Sprintf("arguments %q and %q both set %q for %s with different values", source[c], k, c, spec.toolName()),
				}
			}
			norms = append(norms, Normalization{Op: OpDrop, Field: c, From: k, Rule: "duplicate"})
			continue
		}
		out[c] = args[k]
		source[c] = k
		norms = append(norms, Normalization{Op: OpAlias, Field: c, From: k})
	}
	return out, norms, nil
}

func resolveKey(spec Spec, key string) string {
	if !spec.closed() {
		return CanonicalKey(key)
	}
	if _, ok := spec.field(key); ok {
		return key
	}
	t := Token(key)
	for _, f := range spec.Fields {
		if Token(f.Name) == t {
			return f.Name
		}
	}
	for _, table := range []map[string]string{kindAliases[spec.Kind], commonAliases, argAliases} {
		if c, ok := table[t]; ok {
			if _, known := spec.field(c); known {
				return c
			}
		}
	}
	return key
}

// state accumulates repairs over one argument map.
type state struct {
	args  map[string]any
	norms []Normalization
}

func (s *state) str(key string) string {
	v, _ := s.args[key].(string)
	return v
}

// first returns the first key whose string value is not blank, and that value.
func (s *state) first(keys ...string) (string, string) {
	for _, k := range keys {
		if v := s.str(k); strings.TrimSpace(v) != "" {
			return k, v
		}
	}
	return "", ""
}

// put sets field to v and records the change. A field that was absent or blank
// is derived; one that held a different value is repaired.
func (s *state) put(field string, v any, rule, from string) {
	old, had := s.args[field]
	if had && reflect.DeepEqual(old, v) {
		return
	}
	op := OpRepair
	if !had || isBlank(old) {
		op = OpDerive
	}
	if from == field {
		from = ""
	}
	s.args[field] = v
	s.norms = append(s.norms, Normalization{Op: op, Field: field, From: from, Rule: rule})
}

func (s *state) coerce(field string, v any, rule string) {
	s.args[field] = v
	s.norms = append(s.norms, Normalization{Op: OpCoerce, Field: field, Rule: rule})
}

func (s *state) remove(field, rule string) {
	if _, ok := s.args[field]; !ok {
		return
	}
	delete(s.args, field)
	s.norms = append(s.norms, Normalization{Op: OpRemove, Field: field, Rule: rule})
}

// repairKind applies the per-kind repairs ported from cursor-bridge's
// normalizeBridgeToolArgs, plus repairs for kinds cursor-bridge does not have.
func repairKind(k Kind, s *state) {
	switch k {
	case KindShellStdin:
		if key, taskID := s.first("task_id", "id"); taskID != "" && strings.TrimSpace(s.str("session_id")) == "" {
			s.put("task_id", taskID, "task_id", key)
			s.put("session_id", taskID, "session_from_task_id", key)
		}
	case KindShell:
		if key, command := s.first("command", "prompt"); command != "" {
			s.put("command", command, "command", key)
		}
		if key, cwd := s.first("cwd", "directory", "path"); cwd != "" {
			s.put("cwd", cwd, "cwd", key)
		}
	case KindReadFile, KindWriteFile, KindDeleteFile, KindApplyPatch:
		key, raw := s.first("path", "file_path", "target_file", "relative_workspace_path", "relative_path", "file_name", "uri")
		if p := CleanLocalPath(raw); p != "" {
			s.put("path", p, "local_path", key)
		}
		if k == KindApplyPatch {
			repairApplyPatch(s)
		}
	case KindListDir:
		key, raw := s.first("directory", "path", "relative_workspace_path", "relative_path", "uri")
		if dir := CleanLocalPath(raw); dir != "" {
			s.put("directory", dir, "local_path", key)
		}
	case KindGlob:
		if key, pattern := s.first("glob_pattern", "pattern", "query"); pattern != "" {
			s.put("glob_pattern", RepairGlob(pattern), "glob", key)
		}
		key, raw := s.first("target_directory", "directory", "path")
		if dir := CleanLocalPath(raw); dir != "" {
			s.put("target_directory", dir, "local_path", key)
		}
	case KindGrep:
		if key, pattern := s.first("pattern", "query", "search_term"); pattern != "" {
			s.put("pattern", pattern, "pattern", key)
		}
		if raw := s.str("path"); strings.TrimSpace(raw) != "" {
			if p := CleanLocalPath(raw); p != "" {
				s.put("path", p, "local_path", "")
			}
		}
		for _, g := range []string{"glob", "include"} {
			if raw := s.str(g); strings.TrimSpace(raw) != "" {
				s.put(g, RepairGlob(raw), "glob", "")
			}
		}
	case KindWebFetch:
		if key, u := s.first("url", "uri", "link"); u != "" {
			s.put("url", strings.TrimSpace(u), "url", key)
		}
	case KindCallMCPTool:
		repairMCPToolRef(s)
		if key, tool := s.first("tool", "name"); tool != "" {
			s.put("tool", tool, "tool", key)
		}
	case KindGetMCPServer, KindListMCPTools, KindListMCPResources, KindReadMCPResource, KindListMCPPrompts, KindGetMCPPrompt:
		serverKeys := []string{"server", "name"}
		if k == KindGetMCPPrompt {
			serverKeys = serverKeys[:1] // "name" is the prompt's name here
		}
		if key, server := s.first(serverKeys...); server != "" {
			s.put("server", server, "server", key)
		}
		if raw := s.str("server"); strings.TrimSpace(raw) != "" {
			if normalized, placeholder := requestedMCPServer(raw); placeholder {
				s.remove("server", "placeholder_server")
			} else {
				s.put("server", normalized, "trim", "")
			}
		}
		if k == KindReadMCPResource {
			if key, uri := mcpResourceURI(s.args); uri != "" {
				s.put("uri", uri, "uri", key)
			}
		}
		if k == KindGetMCPPrompt {
			if key, prompt := s.first("prompt", "name", "prompt_name"); prompt != "" {
				s.put("prompt", prompt, "prompt", key)
			}
		}
	case KindTask:
		if key, description := s.first("description", "prompt", "task", "message"); description != "" {
			s.put("description", description, "description", key)
		}
		if key, agent := s.first("subagent_type", "agent_type", "type"); agent != "" {
			s.put("subagent_type", agent, "subagent_type", key)
		}
		if key, model := modelSelection(s); model != "" {
			s.put("model", model, "model", key)
		}
	case KindAwaitTask:
		if key, taskID := taskIDArg(s.args); taskID != "" {
			s.put("task_id", taskID, "task_id", key)
		}
		coerceIntArg(s, "timeout_ms")
		coerceIntArg(s, "timeout_seconds")
	}
}

// repairApplyPatch normalizes notebook-edit arguments and the keys nested in
// string_replacements and edits.
func repairApplyPatch(s *state) {
	if isNotebookEdit(s.args) {
		if key, raw := s.first("cell_type", "cell_language"); raw != "" {
			if cellType := notebookCellType(raw); cellType != "" {
				s.put("cell_type", cellType, "notebook_cell_type", key)
			}
		}
		if boolFromAny(s.args["is_new_cell"]) {
			if _, ok := s.args["create"]; !ok {
				s.put("create", true, "notebook_new_cell", "is_new_cell")
			}
			if _, ok := s.args["insert_cell_after"]; !ok {
				if idx, ok := intAny(s.args["cell_index"]); ok {
					s.put("insert_cell_after", idx-1, "notebook_new_cell", "cell_index")
				}
			}
		}
		if key, source := s.first("cell_source", "new_string"); source != "" {
			s.put("cell_source", source, "notebook_cell_source", key)
		}
	}
	for _, list := range []string{"string_replacements", "edits"} {
		items, ok := s.args[list].([]any)
		if !ok {
			continue
		}
		for i, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			out := make(map[string]any, len(m))
			for _, k := range keys {
				if CanonicalKey(k) == k {
					out[k] = m[k]
				}
			}
			for _, k := range keys {
				c := CanonicalKey(k)
				if c == k {
					continue
				}
				field := fmt.Sprintf("%s[%d].%s", list, i, c)
				if _, exists := out[c]; exists {
					s.norms = append(s.norms, Normalization{Op: OpDrop, Field: field, From: k, Rule: "duplicate"})
					continue
				}
				out[c] = m[k]
				s.norms = append(s.norms, Normalization{Op: OpAlias, Field: field, From: k})
			}
			items[i] = out
		}
	}
}

func isNotebookEdit(args map[string]any) bool {
	if p, _ := args["path"].(string); strings.HasSuffix(strings.ToLower(strings.TrimSpace(p)), ".ipynb") {
		return true
	}
	for _, k := range []string{"cell_index", "cell_type", "cell_language", "cell_source", "is_new_cell", "insert_cell_after"} {
		if _, ok := args[k]; ok {
			return true
		}
	}
	return false
}

func notebookCellType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "markdown", "md":
		return "markdown"
	case "raw":
		return "raw"
	default:
		return "code"
	}
}

// repairMCPToolRef splits "tool@server" and "server/tool" references, unwraps a
// tool object, and drops placeholder server names.
func repairMCPToolRef(s *state) {
	if toolObj, ok := s.args["tool"].(map[string]any); ok {
		if key, tool := firstIn(toolObj, "name", "tool", "id"); tool != "" {
			s.put("tool", tool, "tool_object", "tool."+key)
		}
		if strings.TrimSpace(s.str("server")) == "" {
			if key, server := firstIn(toolObj, "server", "mcp_server"); server != "" {
				if normalized, placeholder := requestedMCPServer(server); !placeholder {
					s.put("server", normalized, "tool_object", "tool."+key)
				}
			}
		}
	}
	if raw := s.str("server"); strings.TrimSpace(raw) != "" {
		if normalized, placeholder := requestedMCPServer(raw); placeholder {
			s.remove("server", "placeholder_server")
		} else {
			s.put("server", normalized, "trim", "")
		}
	}
	key, ref := s.first("tool", "name")
	if ref == "" {
		return
	}
	ref = strings.TrimSpace(ref)
	switch {
	case strings.Contains(ref, "@"):
		parts := strings.SplitN(ref, "@", 2)
		s.put("tool", strings.TrimSpace(parts[0]), "tool_at_server", key)
		if strings.TrimSpace(s.str("server")) == "" {
			if normalized, placeholder := requestedMCPServer(parts[1]); !placeholder {
				s.put("server", normalized, "tool_at_server", key)
			}
		}
	case strings.Count(ref, "/") == 1:
		parts := strings.SplitN(ref, "/", 2)
		left, right := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if left != "" && right != "" && strings.TrimSpace(s.str("server")) == "" {
			if normalized, placeholder := requestedMCPServer(left); !placeholder {
				s.put("server", normalized, "server_slash_tool", key)
				s.put("tool", right, "server_slash_tool", key)
			}
		}
	default:
		s.put("tool", ref, "tool", key)
	}
}

// requestedMCPServer trims a server name and reports whether it is a
// placeholder ("default", "auto", "none", ...) rather than a real server.
func requestedMCPServer(requested string) (string, bool) {
	name := strings.TrimSpace(requested)
	if name == "" {
		return "", false
	}
	switch strings.ToLower(name) {
	case "dummy", "default", "auto", "automatic", "none", "null", "nil", "unknown", "placeholder", "mcp", "server":
		return "", true
	default:
		return name, false
	}
}

func mcpResourceURI(args map[string]any) (string, string) {
	for _, k := range []string{"uri", "resource_uri", "resource"} {
		if v, _ := args[k].(string); strings.TrimSpace(v) != "" {
			return k, strings.TrimSpace(v)
		}
	}
	if resource, ok := args["resource"].(map[string]any); ok {
		if key, uri := firstIn(resource, "uri", "resource_uri"); uri != "" {
			return "resource." + key, strings.TrimSpace(uri)
		}
	}
	return "", ""
}

func taskIDArg(args map[string]any) (string, string) {
	for _, k := range []string{"task_id", "id"} {
		if v, _ := args[k].(string); strings.TrimSpace(v) != "" {
			return k, strings.TrimSpace(v)
		}
	}
	if task, ok := args["task"].(map[string]any); ok {
		if key, id := firstIn(task, "id", "task_id"); id != "" {
			return "task." + key, strings.TrimSpace(id)
		}
	}
	return "", ""
}

// modelSelection joins a model with its provider as "provider/model" unless the
// model is already qualified.
func modelSelection(s *state) (string, string) {
	key, model := s.first("model", "submodel", "requested_model", "model_name")
	model = strings.TrimSpace(model)
	if model == "" {
		if nested, ok := s.args["model"].(map[string]any); ok {
			var nkey string
			nkey, model = firstIn(nested, "id", "name", "model")
			model = strings.TrimSpace(model)
			key = "model." + nkey
			if strings.TrimSpace(s.str("provider")) == "" {
				if pkey, provider := firstIn(nested, "provider", "vendor"); strings.TrimSpace(provider) != "" {
					s.put("provider", strings.TrimSpace(provider), "model_object", "model."+pkey)
				}
			}
		}
	}
	if model == "" {
		return "", ""
	}
	_, provider := s.first("provider", "model_provider", "provider_id", "vendor")
	provider = strings.TrimSpace(provider)
	if provider == "" || strings.Contains(model, "/") || strings.HasPrefix(model, provider+"-") {
		return key, model
	}
	return key, provider + "/" + model
}

// coerceIntArg converts a numeric string, or a JSON number written with a
// fraction or exponent but an integral value ("1.5e3"), to an int.
func coerceIntArg(s *state, key string) {
	switch v := s.args[key].(type) {
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			s.coerce(key, n, "integer")
		}
	case json.Number:
		if n, ok := integralNumber(v); ok {
			s.coerce(key, n, "integer")
		}
	}
}

// integralNumber reports whether a JSON number that is not written as an
// integer ("5.0", "1e2") has an integral value that a float64 holds exactly,
// and returns it. JSON Schema counts such a number as an integer.
func integralNumber(v json.Number) (int, bool) {
	if _, err := v.Int64(); err == nil {
		return 0, false // already written as an integer
	}
	f, err := v.Float64()
	if err != nil || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return 0, false
	}
	return int(f), true
}

// Field classes for the generic repairs of closed specs.
var (
	pathFields  = map[string]bool{"path": true, "cwd": true, "directory": true, "target_directory": true}
	globFields  = map[string]bool{"glob_pattern": true, "path_glob": true, "glob": true, "include": true}
	trimFields  = map[string]bool{"workspace_id": true, "entry_id": true, "run_id": true, "session_id": true, "task_id": true, "symbol": true, "from": true, "to": true, "seed": true, "lang": true, "mode": true, "uid": true}
	pathListKey = "paths"
)

// repairFields applies field-class repairs and type coercions to a closed spec.
func repairFields(spec Spec, s *state) {
	for _, f := range spec.Fields {
		v, ok := s.args[f.Name]
		if !ok {
			continue
		}
		switch {
		case f.Name == pathListKey:
			repairPathList(f, s)
		case pathFields[f.Name]:
			if raw, ok := v.(string); ok && strings.TrimSpace(raw) != "" {
				if p := CleanLocalPath(raw); p != "" {
					s.put(f.Name, p, "local_path", "")
				}
			}
		case globFields[f.Name]:
			if raw, ok := v.(string); ok && strings.TrimSpace(raw) != "" {
				s.put(f.Name, RepairGlob(raw), "glob", "")
			}
		case trimFields[f.Name]:
			if raw, ok := v.(string); ok && strings.TrimSpace(raw) != raw {
				s.put(f.Name, strings.TrimSpace(raw), "trim", "")
			}
		}
		coerceField(f, s)
	}
}

// repairPathList cleans a comma-separated path list, or joins a JSON list of
// paths into one when the field is string-typed.
func repairPathList(f Field, s *state) {
	switch v := s.args[f.Name].(type) {
	case []any:
		if f.Type != TypeString {
			return
		}
		parts := make([]string, 0, len(v))
		for _, item := range v {
			p, ok := item.(string)
			if !ok {
				return
			}
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, cleanListPath(p))
			}
		}
		s.coerce(f.Name, strings.Join(parts, ","), "list_to_csv")
	case string:
		items := strings.Split(v, ",")
		parts := make([]string, 0, len(items))
		for _, p := range items {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, cleanListPath(p))
			}
		}
		if joined := strings.Join(parts, ","); joined != v {
			s.put(f.Name, joined, "local_path", "")
		}
	}
}

func cleanListPath(p string) string {
	if c := CleanLocalPath(p); c != "" {
		return c
	}
	return p
}

// coerceField converts a value to its field's type when the conversion is
// lossless and unambiguous, and folds enum values that differ only in case.
func coerceField(f Field, s *state) {
	v := s.args[f.Name]
	switch f.Type {
	case TypeBoolean:
		if str, ok := v.(string); ok {
			switch strings.ToLower(strings.TrimSpace(str)) {
			case "true":
				s.coerce(f.Name, true, "boolean")
			case "false":
				s.coerce(f.Name, false, "boolean")
			}
		}
	case TypeInteger:
		switch x := v.(type) {
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
				s.coerce(f.Name, n, "integer")
			}
		case json.Number:
			if n, ok := integralNumber(x); ok {
				s.coerce(f.Name, n, "integer")
			}
		}
	case TypeNumber:
		if str, ok := v.(string); ok {
			if n, err := strconv.ParseFloat(strings.TrimSpace(str), 64); err == nil {
				s.coerce(f.Name, n, "number")
			}
		}
	case TypeString:
		switch n := v.(type) {
		case float64:
			s.coerce(f.Name, strconv.FormatFloat(n, 'f', -1, 64), "string")
		case json.Number:
			s.coerce(f.Name, n.String(), "string")
		}
		if str, ok := s.args[f.Name].(string); ok && len(f.Enum) > 0 && !contains(f.Enum, str) {
			for _, e := range f.Enum {
				if strings.EqualFold(strings.TrimSpace(str), e) {
					s.put(f.Name, e, "enum_case", "")
					break
				}
			}
		}
	}
}

// validate checks unknown arguments, types and enums, then required fields for
// a closed spec, or the ported per-kind required-field rules for an open one.
func validate(spec Spec, args map[string]any) *ValidationError {
	tool := spec.toolName()
	if !spec.closed() {
		return validateKind(spec.Kind, args)
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := spec.field(k); !ok {
			return &ValidationError{Field: k, Code: CodeUnknown, Message: fmt.Sprintf("unknown argument %q for tool %s", k, tool)}
		}
	}
	for _, f := range spec.Fields {
		v, ok := args[f.Name]
		if !ok || v == nil {
			continue
		}
		if f.Type != "" && !hasType(v, f.Type) {
			return &ValidationError{Field: f.Name, Code: CodeType, Expected: string(f.Type),
				Message: fmt.Sprintf("argument %q for tool %s must be %s %s", f.Name, tool, article(f.Type), f.Type)}
		}
		if str, ok := v.(string); ok && len(f.Enum) > 0 && !contains(f.Enum, str) {
			return &ValidationError{Field: f.Name, Code: CodeEnum, Expected: strings.Join(f.Enum, "|"),
				Message: fmt.Sprintf("argument %q for tool %s must be one of: %s", f.Name, tool, strings.Join(f.Enum, ", "))}
		}
	}
	for _, f := range spec.Fields {
		if f.Required && isBlank(args[f.Name]) {
			return &ValidationError{Field: f.Name, Code: CodeRequired, Message: fmt.Sprintf("missing required argument %q for %s", f.Name, tool)}
		}
	}
	return nil
}

// validateKind is the required-field check for harness tool families, ported
// from cursor-bridge's validateBridgeToolArgs.
func validateKind(k Kind, args map[string]any) *ValidationError {
	has := func(keys ...string) bool {
		for _, key := range keys {
			if v, _ := args[key].(string); strings.TrimSpace(v) != "" {
				return true
			}
		}
		return false
	}
	required := func(field, message string) *ValidationError {
		return &ValidationError{Field: field, Code: CodeRequired, Message: message}
	}
	switch k {
	case KindReadFile, KindWriteFile, KindDeleteFile:
		if !has("path", "file_path", "target_file") {
			return required("path", "path is required")
		}
	case KindApplyPatch:
		if !has("patch", "diff", "input") && !has("path", "file_path", "target_file") {
			return required("path", "path is required")
		}
	case KindGrep:
		if !has("pattern", "query", "search_term") {
			return required("pattern", "pattern is required")
		}
	case KindSearchSymbols:
		if !has("query", "symbol") {
			return required("query", "query is required")
		}
	case KindSemanticSearch:
		if !has("query", "content", "pattern") { // "text" is canonicalized to "content"
			return required("query", "query is required")
		}
	case KindShell:
		if !has("command") && !nonEmptyList(args["command"]) {
			return required("command", "command is required")
		}
	case KindShellStdin, KindKillShell:
		if !has("session_id", "task_id", "id") {
			return required("session_id", "session_id or task_id is required")
		}
	case KindWebFetch:
		if !has("url") {
			return required("url", "url is required")
		}
	case KindCallMCPTool:
		if !has("tool", "name") {
			return required("tool", "tool is required")
		}
	case KindReadMCPResource:
		if !has("uri") {
			return required("uri", "uri is required")
		}
	case KindGetMCPPrompt:
		if !has("prompt") {
			return required("prompt", "prompt is required")
		}
	case KindTask:
		if !has("description") {
			return required("description", "description is required")
		}
	case KindAwaitTask:
		if !has("task_id") {
			return required("task_id", "task_id is required")
		}
	}
	return nil
}

// CleanLocalPath turns a path argument into a clean local path. A file:// URI
// loses its scheme and an optional "localhost" authority, and its percent
// escapes are decoded when they are well formed ("file:///a%20b" → "/a b"). The
// rest is taken as a path, as cursor-bridge does: "file://src/main.go" stays
// relative, and '#' and '?' stay part of the name. A file URI whose authority
// holds userinfo ("file://user:pass@host/a.go") names no local path, and other
// URIs are not local either: both return "". The result goes through
// filepath.Clean.
func CleanLocalPath(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	if hasPrefixFold(p, "file://") {
		p = p[len("file://"):]
		if hasPrefixFold(p, "localhost/") {
			p = p[len("localhost"):]
		}
		if dec, err := url.PathUnescape(p); err == nil {
			p = dec
		}
		authority, _, _ := strings.Cut(p, "/")
		if strings.IndexByte(authority, '@') > 0 {
			return "" // user@host, not a path ("@types/node" is one)
		}
	}
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.Contains(p, "://") && !strings.HasPrefix(p, "/") {
		return ""
	}
	return filepath.Clean(p)
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// RepairGlob fixes common glob mistakes: a bare extension segment becomes a
// wildcard (".go" → "*.go", "**/.md" → "**/*.md"), a leading slash before a
// wildcard or extension segment is dropped, and doubled slashes collapse.
func RepairGlob(raw string) string {
	pattern := strings.TrimSpace(raw)
	if pattern == "" {
		return ""
	}
	if trimGlobLeadingSlash(pattern) {
		pattern = strings.TrimPrefix(pattern, "/")
	}
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		token := strings.TrimSpace(part)
		if strings.HasPrefix(token, ".") && strings.Count(token, ".") == 1 && len(token) > 1 {
			parts[i] = "*" + token
		}
	}
	pattern = strings.Join(parts, "/")
	for strings.Contains(pattern, "//") {
		pattern = strings.ReplaceAll(pattern, "//", "/")
	}
	return strings.TrimSpace(pattern)
}

func trimGlobLeadingSlash(pattern string) bool {
	if !strings.HasPrefix(pattern, "/") {
		return false
	}
	trimmed := strings.TrimPrefix(pattern, "/")
	if trimmed == "" {
		return false
	}
	first := trimmed
	if i := strings.Index(first, "/"); i != -1 {
		first = first[:i]
	}
	return strings.ContainsAny(first, "*?{[") || (strings.HasPrefix(first, ".") && strings.Count(first, ".") == 1)
}

func firstIn(m map[string]any, keys ...string) (string, string) {
	for _, k := range keys {
		if v, _ := m[k].(string); strings.TrimSpace(v) != "" {
			return k, v
		}
	}
	return "", ""
}

func hasType(v any, t Type) bool {
	switch t {
	case TypeString:
		_, ok := v.(string)
		return ok
	case TypeBoolean:
		_, ok := v.(bool)
		return ok
	case TypeInteger:
		switch n := v.(type) {
		case int, int32, int64:
			return true
		case float64:
			return n == float64(int64(n))
		case json.Number:
			if _, err := n.Int64(); err == nil {
				return true
			}
			_, ok := integralNumber(n)
			return ok
		}
		return false
	case TypeNumber:
		switch v.(type) {
		case int, int32, int64, float32, float64, json.Number:
			return true
		}
		return false
	case TypeArray:
		_, ok := v.([]any)
		return ok
	case TypeObject:
		_, ok := v.(map[string]any)
		return ok
	}
	return true
}

func article(t Type) string {
	if t == TypeArray || t == TypeInteger || t == TypeObject {
		return "an"
	}
	return "a"
}

func isBlank(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	}
	return false
}

func nonEmptyList(v any) bool {
	list, ok := v.([]any)
	return ok && len(list) > 0
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func boolFromAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	}
	return false
}

func intAny(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return i, true
		}
	}
	return 0, false
}

// deepCopyMap copies a decoded-JSON value tree so repairs never alias the
// caller's maps or slices.
func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopy(v)
	}
	return out
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return deepCopyMap(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = deepCopy(item)
		}
		return out
	}
	return v
}

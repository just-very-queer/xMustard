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
	"slices"
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
	return canonicalizeMap(args, keyRenamer{
		resolve: func(k string) string { return resolveKey(spec, k) },
		field:   func(c string) string { return c },
		conflict: func(c, first, k string, existing, v any) *ValidationError {
			if !spec.mutating() || reflect.DeepEqual(existing, v) {
				return nil
			}
			return &ValidationError{
				Field:   c,
				Code:    CodeAmbiguous,
				Message: fmt.Sprintf("arguments %q and %q both set %q for %s with different values", first, k, c, spec.toolName()),
			}
		},
	})
}

// keyRenamer is how canonicalizeMap renames the keys of one map: resolve names
// a key's canonical field, field names that field in a Normalization, and
// conflict decides whether a second spelling of a field is an error.
type keyRenamer struct {
	resolve  func(key string) string
	field    func(canonical string) string
	conflict func(canonical, first, key string, existing, v any) *ValidationError
}

// canonicalizeMap renames the keys of m in sorted order, canonical spellings
// first, so which alias wins does not depend on map order.
func canonicalizeMap(m map[string]any, r keyRenamer) (map[string]any, []Normalization, *ValidationError) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	resolved := make(map[string]string, len(keys))
	out := make(map[string]any, len(m))
	source := make(map[string]string, len(m))
	for _, k := range keys {
		if resolved[k] = r.resolve(k); resolved[k] == k {
			out[k], source[k] = m[k], k
		}
	}
	var norms []Normalization
	for _, k := range keys {
		c := resolved[k]
		if c == k {
			continue
		}
		if existing, ok := out[c]; ok {
			if err := r.conflict(c, source[c], k, existing, m[k]); err != nil {
				return out, norms, err
			}
			norms = append(norms, Normalization{Op: OpDrop, Field: r.field(c), From: k, Rule: "duplicate"})
			continue
		}
		out[c], source[c] = m[k], k
		norms = append(norms, Normalization{Op: OpAlias, Field: r.field(c), From: k})
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
func (s *state) first(keys ...string) (string, string) { return firstIn(s.args, keys...) }

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

// repair is one step of a kind's argument repair.
type repair func(*state)

// promote copies the first non-blank value among keys into field, through
// clean when it is set; a value that clean empties (a URI that is not a local
// path) is left alone.
func promote(field, rule string, clean func(string) string, keys ...string) repair {
	return func(s *state) {
		key, v := s.first(keys...)
		if v != "" && clean != nil {
			v = clean(v)
		}
		if v != "" {
			s.put(field, v, rule, key)
		}
	}
}

// promoteFrom is promote for a value that a lookup finds (nested objects,
// joined model names) rather than a plain key list.
func promoteFrom(field, rule string, find func(*state) (string, string)) repair {
	return func(s *state) {
		if key, v := find(s); v != "" {
			s.put(field, v, rule, key)
		}
	}
}

var (
	pathKeys          = []string{"path", "file_path", "target_file", "relative_workspace_path", "relative_path", "file_name", "uri"}
	filePathRepair    = promote("path", "local_path", CleanLocalPath, pathKeys...)
	serverRepair      = promote("server", "server", nil, "server", "name")
	mcpServerRepairs  = []repair{serverRepair, repairServerName}
	mcpResourceRepair = promoteFrom("uri", "uri", func(s *state) (string, string) { return mcpResourceArg.find(s.args) })
)

// kindRepairs are the per-kind repairs ported from cursor-bridge's
// normalizeBridgeToolArgs, plus repairs for kinds cursor-bridge does not have,
// applied in order. For get_mcp_prompt "name" is the prompt's name, not the
// server's.
var kindRepairs = map[Kind][]repair{
	KindShellStdin: {repairStdinSession},
	KindShell: {
		promote("command", "command", nil, "command", "prompt"),
		promote("cwd", "cwd", nil, "cwd", "directory", "path"),
	},
	KindReadFile:   {filePathRepair},
	KindWriteFile:  {filePathRepair},
	KindDeleteFile: {filePathRepair},
	KindApplyPatch: {filePathRepair, repairApplyPatch},
	KindListDir:    {promote("directory", "local_path", CleanLocalPath, "directory", "path", "relative_workspace_path", "relative_path", "uri")},
	KindGlob: {
		promote("glob_pattern", "glob", RepairGlob, "glob_pattern", "pattern", "query"),
		promote("target_directory", "local_path", CleanLocalPath, "target_directory", "directory", "path"),
	},
	KindGrep: {
		promote("pattern", "pattern", nil, "pattern", "query", "search_term"),
		promote("path", "local_path", CleanLocalPath, "path"),
		promote("glob", "glob", RepairGlob, "glob"),
		promote("include", "glob", RepairGlob, "include"),
	},
	KindWebFetch:         {promote("url", "url", strings.TrimSpace, "url", "uri", "link")},
	KindCallMCPTool:      {repairMCPToolRef, promote("tool", "tool", nil, "tool", "name")},
	KindGetMCPServer:     mcpServerRepairs,
	KindListMCPTools:     mcpServerRepairs,
	KindListMCPResources: mcpServerRepairs,
	KindListMCPPrompts:   mcpServerRepairs,
	KindReadMCPResource:  {serverRepair, repairServerName, mcpResourceRepair},
	KindGetMCPPrompt: {
		promote("server", "server", nil, "server"),
		repairServerName,
		promote("prompt", "prompt", nil, "prompt", "name", "prompt_name"),
	},
	KindTask: {
		promote("description", "description", nil, "description", "prompt", "task", "message"),
		promote("subagent_type", "subagent_type", nil, "subagent_type", "agent_type", "type"),
		promoteFrom("model", "model", modelSelection),
	},
	KindAwaitTask: {
		promoteFrom("task_id", "task_id", func(s *state) (string, string) { return taskIDArg.find(s.args) }),
		func(s *state) { coerceIntArg(s, "timeout_ms") },
		func(s *state) { coerceIntArg(s, "timeout_seconds") },
	},
}

// repairKind applies k's repairs.
func repairKind(k Kind, s *state) {
	for _, r := range kindRepairs[k] {
		r(s)
	}
}

// repairStdinSession names the shell session after a task id when no session
// id was given.
func repairStdinSession(s *state) {
	if key, taskID := s.first("task_id", "id"); taskID != "" && strings.TrimSpace(s.str("session_id")) == "" {
		s.put("task_id", taskID, "task_id", key)
		s.put("session_id", taskID, "session_from_task_id", key)
	}
}

// repairServerName trims the server name and drops a placeholder one.
func repairServerName(s *state) {
	raw := s.str("server")
	if strings.TrimSpace(raw) == "" {
		return
	}
	if normalized, placeholder := requestedMCPServer(raw); placeholder {
		s.remove("server", "placeholder_server")
	} else {
		s.put("server", normalized, "trim", "")
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
			out, norms, _ := canonicalizeMap(m, keyRenamer{
				resolve:  CanonicalKey,
				field:    func(c string) string { return fmt.Sprintf("%s[%d].%s", list, i, c) },
				conflict: func(string, string, string, any, any) *ValidationError { return nil },
			})
			items[i] = out
			s.norms = append(s.norms, norms...)
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
	repairServerName(s)
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

// argLookup finds a string argument under one of its flat keys, or else under
// one of nestedKeys inside the object at nested ("resource": {"uri": ...}).
type argLookup struct {
	flat       []string
	nested     string
	nestedKeys []string
}

// find returns the key path it read ("resource.uri" for a nested one) and the
// trimmed value, or two empty strings.
func (l argLookup) find(args map[string]any) (string, string) {
	if key, v := firstIn(args, l.flat...); v != "" {
		return key, strings.TrimSpace(v)
	}
	if obj, ok := args[l.nested].(map[string]any); ok {
		if key, v := firstIn(obj, l.nestedKeys...); v != "" {
			return l.nested + "." + key, strings.TrimSpace(v)
		}
	}
	return "", ""
}

var (
	mcpResourceArg = argLookup{flat: []string{"uri", "resource_uri", "resource"}, nested: "resource", nestedKeys: []string{"uri", "resource_uri"}}
	taskIDArg      = argLookup{flat: []string{"task_id", "id"}, nested: "task", nestedKeys: []string{"id", "task_id"}}
	modelArg       = argLookup{flat: []string{"model", "submodel", "requested_model", "model_name"}, nested: "model", nestedKeys: []string{"id", "name", "model"}}
)

// modelSelection joins a model with its provider as "provider/model" unless the
// model is already qualified. A model given as an object may carry its
// provider, which fills a blank provider argument.
func modelSelection(s *state) (string, string) {
	key, model := modelArg.find(s.args)
	if nested, ok := s.args[modelArg.nested].(map[string]any); ok && !slices.Contains(modelArg.flat, key) && strings.TrimSpace(s.str("provider")) == "" {
		if pkey, provider := firstIn(nested, "provider", "vendor"); provider != "" {
			s.put("provider", strings.TrimSpace(provider), "model_object", modelArg.nested+"."+pkey)
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
	req, ok := kindRequired[k]
	if !ok || (req.list && nonEmptyList(args[req.field])) {
		return nil
	}
	for _, key := range req.keys {
		if v, _ := args[key].(string); strings.TrimSpace(v) != "" {
			return nil
		}
	}
	message := req.message
	if message == "" {
		message = req.field + " is required"
	}
	return &ValidationError{Field: req.field, Code: CodeRequired, Message: message}
}

// requirement is a kind's required argument: field is reported missing unless
// one of keys holds a non-blank string (or, with list, field holds a non-empty
// list). message defaults to "<field> is required".
type requirement struct {
	field   string
	keys    []string
	list    bool
	message string
}

var (
	pathRequired    = requirement{field: "path", keys: []string{"path", "file_path", "target_file"}}
	sessionRequired = requirement{field: "session_id", keys: []string{"session_id", "task_id", "id"}, message: "session_id or task_id is required"}
)

// requireField is the requirement that field itself holds a value.
func requireField(field string) requirement { return requirement{field: field, keys: []string{field}} }

var kindRequired = map[Kind]requirement{
	KindReadFile:   pathRequired,
	KindWriteFile:  pathRequired,
	KindDeleteFile: pathRequired,
	// a patch names its files itself
	KindApplyPatch:      {field: "path", keys: []string{"patch", "diff", "input", "path", "file_path", "target_file"}},
	KindGrep:            {field: "pattern", keys: []string{"pattern", "query", "search_term"}},
	KindSearchSymbols:   {field: "query", keys: []string{"query", "symbol"}},
	KindSemanticSearch:  {field: "query", keys: []string{"query", "content", "pattern"}}, // "text" is canonicalized to "content"
	KindShell:           {field: "command", keys: []string{"command"}, list: true},
	KindShellStdin:      sessionRequired,
	KindKillShell:       sessionRequired,
	KindWebFetch:        requireField("url"),
	KindCallMCPTool:     {field: "tool", keys: []string{"tool", "name"}},
	KindReadMCPResource: requireField("uri"),
	KindGetMCPPrompt:    requireField("prompt"),
	KindTask:            requireField("description"),
	KindAwaitTask:       requireField("task_id"),
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
	switch t {
	case TypeArray, TypeInteger, TypeObject:
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

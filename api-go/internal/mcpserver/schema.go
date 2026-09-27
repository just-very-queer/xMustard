package mcpserver

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Argument types a tool may declare. The advertised schemas use only keywords every
// client we target accepts (type, description, enum, minimum, maximum, maxLength,
// properties, required, additionalProperties): no $ref, oneOf/anyOf, format or
// default, which OpenAI/Codex function schemas and several MCP clients reject or
// silently drop.
const (
	typeString  = "string"
	typeBoolean = "boolean"
	typeInteger = "integer"
)

// Arg is one tool argument.
type Arg struct {
	Name     string
	Type     string // typeString | typeBoolean | typeInteger
	Required bool
	Enum     []string
	Min, Max int // inclusive bounds of an integer argument
	MaxLen   int // maximum length of a string argument in characters (0: unbounded)
	// List marks a comma-separated list (a JSON array of strings is also accepted);
	// with Enum, every element must be one of it. It travels as one canonical string.
	List bool
	Desc string
}

// Annotations are the MCP ToolAnnotations hints (2025-03-26 and later).
type Annotations struct {
	Title       string
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
}

// Tool is one of the nine agent-facing tools and the API call it maps to.
type Tool struct {
	Name        string
	Description string
	Args        []Arg
	// Advanced arguments are accepted and validated on tools/call exactly like Args
	// but are not advertised in the lean tools/list (SchemaLean), which every session
	// pays for in its prompt prefix; they are documented at DocsURI instead.
	Advanced []Arg
	// Aliases maps a hidden argument name to its canonical one. Aliases are accepted
	// on tools/call and never advertised in tools/list.
	Aliases map[string]string
	// Doc is markdown the docs resource (DocsURI) prints after the tool's Advanced
	// arguments; tools/list never carries it.
	Doc string
	// WriteArgs name the arguments that make a call of a tool that otherwise only reads
	// a write (why_failed runs a command). A read-only connection lists such a tool and
	// serves its calls without them; its annotations describe the writing calls.
	WriteArgs   []string
	Annotations Annotations
	// Output lists the documented top-level members of the tool's JSON result, for
	// outputSchema. Results may carry more members, so the schema stays open.
	Output map[string]string
	// MaxResultChars, when set, is advertised as _meta["anthropic/maxResultSizeChars"]:
	// the largest bounded result the tool is meant to deliver inline.
	MaxResultChars int
	// PathArg names an argument holding a repository path; an absolute value can
	// resolve the workspace and is rewritten relative to its root.
	PathArg string
	// Build maps validated arguments (always including workspace_id) to the API call:
	// method, path with query, and a JSON body ("" for none).
	Build func(args map[string]string) (method, path, body string)
}

// servesReads reports whether some calls of t only read.
func (t *Tool) servesReads() bool { return t.Annotations.ReadOnly || len(t.WriteArgs) > 0 }

// writes reports whether the call with args changes state.
func (t *Tool) writes(args map[string]string) bool {
	if !t.servesReads() {
		return true
	}
	return writesAny(args, t.WriteArgs)
}

// writesAny reports whether any of the write arguments is set.
func writesAny(args map[string]string, writeArgs []string) bool {
	for _, name := range writeArgs {
		if args[name] != "" {
			return true
		}
	}
	return false
}

func (t *Tool) arg(name string) (Arg, bool) {
	for _, list := range [][]Arg{t.Args, t.Advanced} {
		for _, a := range list {
			if a.Name == name {
				return a, true
			}
		}
	}
	return Arg{}, false
}

// SchemaProfile selects how much of each tool's argument surface tools/list
// advertises. Every profile accepts the same arguments on tools/call.
type SchemaProfile string

const (
	// SchemaLean (the default) advertises only Args; Advanced arguments are
	// documented at DocsURI. tools/list stays under a tested byte cap.
	SchemaLean SchemaProfile = "lean"
	// SchemaFull also advertises Advanced arguments, for clients that validate calls
	// against the closed inputSchema before sending them.
	SchemaFull SchemaProfile = "full"
)

// ParseSchemaProfile reads the XMUSTARD_MCP_SCHEMA setting: blank is lean.
func ParseSchemaProfile(v string) (SchemaProfile, error) {
	switch p := SchemaProfile(strings.ToLower(strings.TrimSpace(v))); p {
	case "":
		return SchemaLean, nil
	case SchemaLean, SchemaFull:
		return p, nil
	}
	return "", fmt.Errorf("invalid XMUSTARD_MCP_SCHEMA=%q; use lean or full", v)
}

// InputSchema is the closed JSON Schema a client validates tools/call arguments
// with, as the lean profile advertises it.
func (t *Tool) InputSchema() map[string]any { return t.inputSchema(SchemaLean) }

func (t *Tool) inputSchema(profile SchemaProfile) map[string]any {
	props := map[string]any{}
	var required []string
	args := t.Args
	if profile == SchemaFull {
		args = append(append([]Arg{}, t.Args...), t.Advanced...)
	}
	for _, a := range args {
		p := map[string]any{"type": a.Type, "description": a.Desc}
		if len(a.Enum) > 0 && !a.List { // a list's elements are checked on tools/call
			p["enum"] = a.Enum
		}
		if a.Type == typeInteger {
			p["minimum"], p["maximum"] = a.Min, a.Max
		}
		if a.MaxLen > 0 {
			p["maxLength"] = a.MaxLen
		}
		props[a.Name] = p
		if a.Required {
			required = append(required, a.Name)
		}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 { // an empty "required" is invalid in draft-04 validators
		s["required"] = required
	}
	return s
}

// resultMetaMember is the reserved member of structuredContent that carries delivery
// metadata (resolved workspace, evidence recovery handle). Clients that read only
// structuredContent (Codex prefers it over text) still see how to recover a reduced
// result.
const resultMetaMember = "_xmustard"

// OutputSchema describes structuredContent: the tool's JSON result object plus the
// reserved _xmustard member. It is open (no additionalProperties:false, no required
// members) because a reduced projection may omit members.
func (t *Tool) OutputSchema() map[string]any {
	props := map[string]any{resultMetaMember: map[string]any{"type": "object"}}
	for name, typ := range t.Output {
		props[name] = map[string]any{"type": typ}
	}
	return map[string]any{"type": "object", "properties": props}
}

// listEntry renders the tool for tools/list under the negotiated protocol version
// and schema profile.
func (t *Tool) listEntry(version string, profile SchemaProfile) map[string]any {
	e := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.inputSchema(profile)}
	if !atLeast(version, version20250618) {
		return e // annotations, outputSchema and _meta postdate 2024-11-05
	}
	a := t.Annotations
	ann := map[string]any{"title": a.Title, "readOnlyHint": a.ReadOnly, "openWorldHint": false}
	if !a.ReadOnly { // destructive/idempotent hints are meaningful only for writes
		ann["destructiveHint"], ann["idempotentHint"] = a.Destructive, a.Idempotent
	}
	e["annotations"] = ann
	e["outputSchema"] = t.OutputSchema()
	if t.MaxResultChars > 0 {
		e["_meta"] = map[string]any{"anthropic/maxResultSizeChars": t.MaxResultChars}
	}
	return e
}

// invalidParams is the JSON-RPC -32602 error for a rejected argument; data names the
// tool and argument so a client can point at the offending field.
func invalidParams(tool, arg, reason string, extra map[string]any) *RPCError {
	data := map[string]any{"tool": tool, "argument": arg, "reason": reason}
	for k, v := range extra {
		data[k] = v
	}
	return &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("argument %q for tool %s %s", arg, tool, reason), Data: data}
}

// BuildArgs strictly validates raw tools/call arguments against the tool's declared
// surface and coerces them into canonical strings. It rejects unknown arguments,
// wrong types, non-scalar values, out-of-enum values, strings over their length cap
// and integers outside their bounds (never clamping), and folds hidden aliases onto
// their canonical names. The returned map records each alias that was used.
func BuildArgs(t *Tool, raw map[string]any) (args map[string]string, aliased map[string]string, rerr *RPCError) {
	args = map[string]string{}
	from := map[string]string{} // canonical name -> the key that supplied it
	for k, v := range raw {
		name := k
		spec, ok := t.arg(k)
		if !ok {
			canon, isAlias := t.Aliases[k]
			if !isAlias {
				return nil, nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("unknown argument %q for tool %s", k, t.Name),
					Data: map[string]any{"tool": t.Name, "argument": k, "reason": "unknown"}}
			}
			name = canon
			spec, _ = t.arg(canon)
		}
		val, rerr := coerce(t.Name, k, spec, v)
		if rerr != nil {
			return nil, nil, rerr
		}
		if prev, dup := from[name]; dup {
			if args[name] != val {
				return nil, nil, invalidParams(t.Name, k, fmt.Sprintf("conflicts with %q", prev), nil)
			}
		} else {
			from[name] = k
		}
		args[name] = val
		if name != k {
			if aliased == nil {
				aliased = map[string]string{}
			}
			aliased[k] = name
		}
	}
	return args, aliased, nil
}

func coerce(tool, key string, spec Arg, v any) (string, *RPCError) {
	switch spec.Type {
	case typeBoolean:
		b, ok := v.(bool)
		if !ok {
			return "", invalidParams(tool, key, "must be a boolean", nil)
		}
		return strconv.FormatBool(b), nil
	case typeInteger:
		var f float64
		switch n := v.(type) {
		case float64:
			f = n
		case json.Number:
			var err error
			if f, err = n.Float64(); err != nil {
				return "", invalidParams(tool, key, "must be an integer", nil)
			}
		default:
			return "", invalidParams(tool, key, "must be an integer", nil)
		}
		if f != math.Trunc(f) || math.IsInf(f, 0) || math.IsNaN(f) {
			return "", invalidParams(tool, key, "must be an integer", nil)
		}
		if f < float64(spec.Min) || f > float64(spec.Max) {
			return "", invalidParams(tool, key, fmt.Sprintf("must be between %d and %d (got %s); out-of-range values are rejected, not clamped",
				spec.Min, spec.Max, strconv.FormatFloat(f, 'f', -1, 64)), map[string]any{"minimum": spec.Min, "maximum": spec.Max})
		}
		return strconv.FormatInt(int64(f), 10), nil
	default: // string
		if spec.List {
			return coerceList(tool, key, spec, v)
		}
		s, ok := v.(string)
		if !ok {
			return "", invalidParams(tool, key, "must be a string", nil)
		}
		if len(spec.Enum) > 0 && !contains(spec.Enum, s) {
			return "", invalidParams(tool, key, "must be one of: "+strings.Join(spec.Enum, ", "), map[string]any{"enum": spec.Enum})
		}
		if spec.MaxLen > 0 && utf8.RuneCountInString(s) > spec.MaxLen {
			return "", invalidParams(tool, key, fmt.Sprintf("must be at most %d characters", spec.MaxLen), map[string]any{"maxLength": spec.MaxLen})
		}
		return s, nil
	}
}

// coerceList validates a comma-separated list, or a JSON array of strings, and
// returns its elements trimmed and comma-joined.
func coerceList(tool, key string, spec Arg, v any) (string, *RPCError) {
	var elems []string
	switch x := v.(type) {
	case string:
		if spec.MaxLen > 0 && utf8.RuneCountInString(x) > spec.MaxLen {
			return "", invalidParams(tool, key, fmt.Sprintf("must be at most %d characters", spec.MaxLen), map[string]any{"maxLength": spec.MaxLen})
		}
		elems = strings.Split(x, ",")
	case []any:
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return "", invalidParams(tool, key, "must be a comma-separated string or an array of strings", nil)
			}
			elems = append(elems, s)
		}
	default:
		return "", invalidParams(tool, key, "must be a comma-separated string or an array of strings", nil)
	}
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		if len(spec.Enum) > 0 && !contains(spec.Enum, e) {
			return "", invalidParams(tool, key, fmt.Sprintf("names %q; each element must be one of: %s", e, strings.Join(spec.Enum, ", ")),
				map[string]any{"enum": spec.Enum})
		}
		out = append(out, e)
	}
	joined := strings.Join(out, ",")
	if spec.MaxLen > 0 && utf8.RuneCountInString(joined) > spec.MaxLen {
		return "", invalidParams(tool, key, fmt.Sprintf("must be at most %d characters", spec.MaxLen), map[string]any{"maxLength": spec.MaxLen})
	}
	return joined, nil
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

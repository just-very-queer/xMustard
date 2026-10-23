package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/workspaceops"
)

// callTool handles tools/call. A malformed request or an unknown tool is a protocol
// error (JSON-RPC -32602); argument validation and the tool run fail as tool results
// (isError) the agent can act on, as MCP 2025-11-25 specifies.
func (s *Session) callTool(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	// decoding arguments copies their text once more: reserve before decoding
	if scope, owned := budget.ScopeFor(ctx); !owned {
		if err := scope.Acquire(int64(len(params))); err != nil {
			return nil, AdmissionError(err)
		}
	} else {
		scope.Close()
	}
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields() // reject stray top-level fields instead of ignoring them
	dec.UseNumber()             // integers are checked exactly, not through float64 rounding
	var p struct {
		Name      string          `json:"name"`
		Arguments map[string]any  `json:"arguments"`
		Meta      json.RawMessage `json:"_meta"`         // MCP-standard request metadata (progress tokens, etc.) — accepted + ignored, NOT a stray field
		Progress  json.RawMessage `json:"progressToken"` // some clients hoist the progress token to params level; accept + ignore
	}
	if err := dec.Decode(&p); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "invalid params: missing tool name"}
	}
	t, ok := ToolByName(p.Name)
	if !ok {
		return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("Unknown tool: %s (tools: %s)", p.Name, strings.Join(toolNames(), ", ")),
			Data: map[string]any{"tool": p.Name, "reason": "unknown_tool"}}
	}
	start := time.Now()
	res, norms, rerr := s.callKnownTool(ctx, t, p.Arguments)
	s.recordUsage(t.Name, len(params), res, rerr, start, norms)
	if rerr != nil {
		return nil, rerr
	}
	return res, nil
}

// readOnlyRefusal is the message a read-only connection owes a tool, "" when the
// connection serves it. It is the one place the rule is written, because two surfaces
// apply it and a resource read must refuse exactly as a named call does: callKnownTool
// returns it as a plain tool result, and resourceToolRefusal returns it marked.
func readOnlyRefusal(readOnly bool, t *Tool, args map[string]string) string {
	if !readOnly {
		return ""
	}
	if !t.servesReads() {
		return fmt.Sprintf("tool %s is not served on a read-only connection (mode=%s); it changes shared memory", t.Name, ModeReadOnly)
	}
	if t.writes(args) {
		return fmt.Sprintf("tool %s is served on a read-only connection (mode=%s) only without %s, which write",
			t.Name, ModeReadOnly, strings.Join(t.WriteArgs, ", "))
	}
	return ""
}

// resourceToolRefusal reports the refusal a read-only connection owes a tool a
// resource read stands for, or nil when the connection serves it. It is readOnlyRefusal's
// rule and message, marked so a resource can tell its refusal from a refused API call and
// surface it as the tool result it is. Without the marker the two are indistinguishable and
// one of them has to be mislabelled. The return is any, not map[string]any, so the shape is
// a property a test can observe rather than a property of the signature.
func resourceToolRefusal(readOnly bool, t *Tool, args map[string]string) any {
	text := readOnlyRefusal(readOnly, t, args)
	if text == "" {
		return nil
	}
	return markedRefusal(text)
}

// readOnlyRefusalMeta marks a refusal the read-only gate produced, so a resource can
// tell it from a refused API call and surface it as the tool result it is. Without the
// marker the two are indistinguishable and one of them has to be mislabelled.
const readOnlyRefusalMeta = "xmustard/refused_read_only"

// markedRefusal adds the marker to a refusal tool result.
func markedRefusal(text string) map[string]any {
	res := TextResult(text, true)
	res["_meta"] = map[string]any{readOnlyRefusalMeta: true}
	return res
}

// refusalMessage is the message of a refusal tool result, for a resource that reports
// the refusal in its own error shape. (internal/rustcore's refusalText is unrelated: it
// trims a subprocess's stderr, and the two are in different packages.)
func refusalMessage(res map[string]any) string {
	content, _ := res["content"].([]map[string]any)
	if len(content) == 0 {
		return ""
	}
	s, _ := content[0]["text"].(string)
	return s
}

// serveResourceTool runs a tool a resource read stands for, refusing it on a read-only
// connection first. RunTool alone would skip that gate, so a resource read would be a
// way around the mode.
func (s *Session) serveResourceTool(ctx context.Context, t *Tool, args map[string]string) (map[string]any, *RPCError) {
	if refused := resourceToolRefusal(s.srv.opts.ReadOnly, t, args); refused != nil {
		res, _ := refused.(map[string]any)
		return res, nil
	}
	return s.RunTool(ctx, t, args, nil)
}

// callKnownTool refuses a write on a read-only connection (a write tool before its
// arguments are read, a writing call of a reading tool after), validates (with the
// toolcompat repair fallback) and runs the call. It returns how many arguments were
// normalized, for usage accounting.
func (s *Session) callKnownTool(ctx context.Context, t *Tool, raw map[string]any) (map[string]any, int, *RPCError) {
	if s.srv.opts.ReadOnly && !t.servesReads() {
		return TextResult(readOnlyRefusal(true, t, nil), true), 0, nil
	}
	args, norms, aerr := buildArgsCompat(t, raw)
	if aerr != nil {
		return argErrorResult(t, aerr), 0, nil
	}
	if text := readOnlyRefusal(s.srv.opts.ReadOnly, t, args); text != "" {
		return TextResult(text, true), 0, nil
	}
	res, rerr := s.runTool(ctx, t, args, norms)
	return res, len(norms), rerr
}

// Normalization records one argument the server changed before the call, so the
// change is never silent: an alias folded onto its canonical name, or an absolute
// path made workspace-relative.
type Normalization struct {
	Argument string `json:"argument"`
	// Kind is alias or relative_path, or a toolcompat op (derive, repair, drop,
	// remove) whose Rule names the repair.
	Kind string `json:"kind"`
	From string `json:"from"`
	To   string `json:"to"`
	Rule string `json:"rule,omitempty"`
}

// RunTool runs a validated call (BuildArgs output). A nil *RPCError with an isError
// result is a tool failure; a non-nil one is a protocol error (admission refusal).
func (s *Session) RunTool(ctx context.Context, t *Tool, args map[string]string, aliased map[string]string) (map[string]any, *RPCError) {
	return s.runTool(ctx, t, args, aliasNorms(aliased))
}

func (s *Session) runTool(ctx context.Context, t *Tool, args map[string]string, norms []Normalization) (map[string]any, *RPCError) {
	for _, a := range t.Args {
		if a.Required && strings.TrimSpace(args[a.Name]) == "" {
			return argErrorResult(t, &ArgError{Tool: t.Name, Argument: a.Name, Reason: "required",
				Message: fmt.Sprintf("missing required argument %q for %s", a.Name, t.Name)}), nil
		}
	}
	ws, err := s.resolveWorkspace(ctx, t, args)
	if err != nil {
		if errors.Is(err, budget.ErrOverloaded) {
			return nil, OverloadError(err)
		}
		var authErr *authRefusalError
		if errors.As(err, &authErr) {
			return TextResult(authFailureMessage(authErr.status, authErr.body), true), nil
		}
		return TextResult("xmustard: "+err.Error(), true), nil
	}
	args["workspace_id"] = ws.ID
	if n, ok := s.relativizePath(t, args, ws); ok {
		norms = append(norms, n)
	}
	// Building the request copies argument text (JSON body marshal + string): reserve
	// it before it is allocated.
	var argBytes int64
	for _, v := range args {
		argBytes += int64(len(v))
	}
	if scope, owned := budget.ScopeFor(ctx); !owned {
		if err := scope.Acquire(2 * argBytes); err != nil {
			return nil, AdmissionError(err)
		}
	} else {
		scope.Close() // no request ledger (direct callers/tests): nothing to hold
	}
	method, path, body := t.Build(args)
	// every call names its issuer, so the API can hold a human approver's write for
	// confirmation (elicitation.go) with or without evidence delivery
	headers := map[string]string{IssuerHeader: IssuerMCP}
	if d := s.srv.opts.Delivery; d != nil {
		maps.Copy(headers, d.Headers(ctx))
	}
	resp, refusal, err := s.call(ctx, Request{Method: method, Path: path, Body: body, Headers: headers})
	if errors.Is(err, budget.ErrOverloaded) {
		return nil, OverloadError(err)
	}
	if err != nil {
		return TextResult(err.Error(), true), nil
	}
	if refusal != nil { // a human approver's write the human did not confirm
		if err := s.finish(ctx, refusal, ws, norms); err != nil {
			return ReplyRefused(err)
		}
		return refusal, nil
	}
	if isOverloadBody(resp.Status, resp.Body) {
		return nil, OverloadError(budget.ErrOverloaded)
	}
	if isAuthFailure(resp.Status, resp.Body) {
		return TextResult(authFailureMessage(resp.Status, resp.Body), true), nil
	}
	var res map[string]any
	if d := s.srv.opts.Delivery; d != nil {
		var rerr *RPCError
		var ok bool
		if res, rerr, ok = d.Result(ctx, resp, ws.ID); ok && rerr != nil {
			return nil, rerr
		}
		if !ok {
			res = nil
		}
	}
	if res == nil {
		if resp.Status >= 400 {
			return TextResult(fmt.Sprintf("API %s %s -> %d: %s", method, path, resp.Status, strings.TrimSpace(resp.Body)), true), nil
		}
		// the reply text and its encoding copy the body again: reserve before building it
		if err := ReserveReply(ctx, len(resp.Body)); err != nil {
			return ReplyRefused(err)
		}
		res = TextResult(resp.Body, false)
	}
	if err := s.finish(ctx, res, ws, norms); err != nil {
		return ReplyRefused(err)
	}
	return res, nil
}

func isAuthFailure(status int, body string) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	if status != http.StatusForbidden {
		return false
	}
	refusal, ok := parseAuthRefusal(body)
	return ok && (refusal.MissingRole != "" || refusal.Reason == "missing_role" || refusal.Reason == "presence_only_token" ||
		refusal.Refusal == "token_scope" || strings.HasPrefix(refusal.Error, "token not scoped to workspace "))
}

type authRefusal struct {
	Reason      string `json:"reason"`
	MissingRole string `json:"missing_role"`
	Refusal     string `json:"refusal"`
	Error       string `json:"error"`
}

func parseAuthRefusal(body string) (authRefusal, bool) {
	var refusal authRefusal
	if err := json.Unmarshal([]byte(body), &refusal); err != nil {
		return authRefusal{}, false
	}
	return refusal, true
}

func authFailureMessage(status int, body string) string {
	refusal, _ := parseAuthRefusal(body)
	if refusal.Reason == "presence_only_token" {
		return "xMustard grounding is unavailable: this token is presence-only and is accepted only when typed at the xmustard-ops prompt. Do not rotate or export it as XMUSTARD_API_TOKEN; use a separate agent-readable bearer principal."
	}
	if status == http.StatusForbidden {
		if refusal.MissingRole != "" {
			if role, err := workspaceops.ParseRoleSpec(refusal.MissingRole); err == nil {
				return fmt.Sprintf("xMustard grounding is unavailable: this token is missing the %q role. Check the principal's configured role without broadening workspace scope or lifetime. If its secret is also expired, rotate it with %s; rotation does not change the role.", role, tokenRotationCommand)
			}
		}
		switch {
		case refusal.Reason == "missing_role":
			return fmt.Sprintf("xMustard grounding is unavailable: this token lacks a required role. Check the principal's configured role without broadening workspace scope or lifetime. If its secret is also expired, rotate it with %s; rotation does not change the role.", tokenRotationCommand)
		case refusal.Refusal == "token_scope" || strings.HasPrefix(refusal.Error, "token not scoped to workspace "):
			return fmt.Sprintf("xMustard grounding is unavailable: this token is not scoped to the requested workspace. Use a principal authorized for that workspace; do not replace it with an unrestricted token. If its secret is also expired, rotate it with %s; rotation preserves the current workspace scope.", tokenRotationCommand)
		}
	}
	return fmt.Sprintf("xMustard grounding is unavailable: authentication failed (HTTP %d). For a missing or expired file-backed token, rotate its existing principal with %s, then update XMUSTARD_API_TOKEN for this client and restart it. For an XMUSTARD_AUTH_TOKENS principal, an operator must run `xmustard-api mint-token <id> <role>`, replace that principal's `id:role:token` entry in the environment value supplied to the API service with the new token, then restart the API. Rotating a same-ID file record does not replace or revoke the environment token. This preserves the principal's role, workspace scope, identity, and lifetime policy. For authorization refusals, use a principal with the required role and workspace scope; rotation cannot fix authorization.", status, tokenRotationCommand)
}

const tokenRotationCommand = "`xmustard-api rotate-token <id> --data-dir <absolute-running-api-data-dir>`"

type authRefusalError struct {
	status int
	body   string
}

func (e *authRefusalError) Error() string { return authFailureMessage(e.status, e.body) }

// finish adds what every result carries: the workspace echo and any normalizations
// in _meta; a one-line text echo the first time an implicitly resolved workspace is
// used in the session (or registered), so the model knows which repository answered
// without paying for it on every call; and, for 2025-06-18 sessions,
// structuredContent that mirrors the first text block.
func (s *Session) finish(ctx context.Context, res map[string]any, ws Workspace, norms []Normalization) error {
	meta, _ := res["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["xmustard/workspace"] = ws
	if len(norms) > 0 {
		meta["xmustard/normalized"] = norms
	}
	res["_meta"] = meta
	content, _ := res["content"].([]map[string]any)
	if ws.Source != SourceArgument && s.firstEcho(ws) {
		content = append(content, map[string]any{"type": "text", "text": ws.echo()})
		res["content"] = content
	}
	if isErr, _ := res["isError"].(bool); isErr || !atLeast(s.Version(), version20250618) || len(content) == 0 {
		return nil
	}
	text, _ := content[0]["text"].(string)
	extra := map[string]any{"workspace": ws}
	if ev, ok := meta["xmustard/evidence"]; ok {
		extra["evidence"] = ev
	}
	if len(norms) > 0 {
		extra["normalized"] = norms
	}
	sc, err := structured(ctx, text, extra)
	if err != nil {
		return err
	}
	res["structuredContent"] = sc
	return nil
}

func (s *Session) firstEcho(ws Workspace) bool {
	key := ws.ID + "\x00" + ws.Source
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.echoed[key] && !ws.Registered {
		return false
	}
	s.echoed[key] = true
	return true
}

// structured builds structuredContent from a result's text without decoding it into
// Go values (a projection can be up to 1 MiB, a raw body 16 MiB): a JSON object is
// reused as is with the reserved _xmustard member spliced in; any other JSON value or
// text is wrapped as {"result": value} or {"text": text}. The spliced bytes are
// validated once, in place, so the text is copied exactly once.
func structured(ctx context.Context, text string, extra map[string]any) (json.RawMessage, error) {
	meta, err := json.Marshal(extra)
	if err != nil {
		return nil, err
	}
	// the spliced copy, and the encoder's compaction of it, each copy the text
	if err := ReserveReply(ctx, 2*(len(text)+len(meta))+64); err != nil {
		return nil, err
	}
	t := strings.TrimSpace(text)
	tail := `"` + resultMetaMember + `":`
	buf := make([]byte, 0, len(t)+len(meta)+len(tail)+16)
	finish := func(buf []byte) []byte {
		buf = append(buf, tail...)
		buf = append(buf, meta...)
		return append(buf, '}')
	}
	if len(t) >= 2 && t[0] == '{' && t[len(t)-1] == '}' && !strings.Contains(t, `"`+resultMetaMember+`"`) {
		body := strings.TrimSpace(t[1 : len(t)-1])
		buf = append(buf, '{')
		if body != "" {
			buf = append(append(buf, body...), ',')
		}
		if buf = finish(buf); json.Valid(buf) {
			return buf, nil
		}
		buf = buf[:0]
	}
	if t != "" {
		buf = append(append(append(buf, `{"result":`...), t...), ',')
		if buf = finish(buf); json.Valid(buf) {
			return buf, nil
		}
		buf = buf[:0]
	}
	quoted, _ := json.Marshal(text)
	buf = append(append(append(buf, `{"text":`...), quoted...), ',')
	return finish(buf), nil
}

// statusText names an HTTP status for error messages.
func statusText(code int) string {
	if t := http.StatusText(code); t != "" {
		return fmt.Sprintf("%d %s", code, t)
	}
	return fmt.Sprint(code)
}

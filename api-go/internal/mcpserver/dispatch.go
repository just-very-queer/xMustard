package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"xmustard/api-go/internal/budget"
)

// callTool handles tools/call: strict argument validation (a protocol error), then
// the tool run, whose failures are tool results (isError) the agent can act on.
func (s *Session) callTool(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	// decoding arguments copies their text once more: reserve before decoding
	if scope, owned := budget.ScopeFor(ctx); !owned {
		if err := scope.Acquire(int64(len(params))); err != nil {
			return nil, OverloadError(err)
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
		// Unknown tool is reported as a tool result (isError) so the agent can
		// self-correct, matching MCP's tool-error convention.
		return TextResult(fmt.Sprintf("unknown tool %q", p.Name), true), nil
	}
	args, aliased, rerr := BuildArgs(t, p.Arguments)
	if rerr != nil {
		return nil, rerr
	}
	res, rerr := s.RunTool(ctx, t, args, aliased)
	if rerr != nil {
		return nil, rerr
	}
	return res, nil
}

// Normalization records one argument the server changed before the call, so the
// change is never silent: an alias folded onto its canonical name, or an absolute
// path made workspace-relative.
type Normalization struct {
	Argument string `json:"argument"`
	Kind     string `json:"kind"` // alias | relative_path
	From     string `json:"from"`
	To       string `json:"to"`
}

// RunTool runs a validated call (BuildArgs output). A nil *RPCError with an isError
// result is a tool failure; a non-nil one is a protocol error (admission refusal).
func (s *Session) RunTool(ctx context.Context, t *Tool, args map[string]string, aliased map[string]string) (map[string]any, *RPCError) {
	var norms []Normalization
	for from, to := range aliased {
		norms = append(norms, Normalization{Argument: to, Kind: "alias", From: from, To: to})
	}
	sort.Slice(norms, func(i, j int) bool { return norms[i].From < norms[j].From })
	for _, a := range t.Args {
		if a.Required && strings.TrimSpace(args[a.Name]) == "" {
			return TextResult(fmt.Sprintf("missing required argument %q for %s", a.Name, t.Name), true), nil
		}
	}
	ws, err := s.resolveWorkspace(ctx, t, args)
	if err != nil {
		if errors.Is(err, budget.ErrOverloaded) {
			return nil, OverloadError(err)
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
			return nil, OverloadError(err)
		}
	} else {
		scope.Close() // no request ledger (direct callers/tests): nothing to hold
	}
	method, path, body := t.Build(args)
	var headers map[string]string
	if d := s.srv.opts.Delivery; d != nil {
		headers = d.Headers(ctx)
	}
	resp, err := s.srv.opts.Backend.Do(ctx, Request{Method: method, Path: path, Body: body, Headers: headers})
	if errors.Is(err, budget.ErrOverloaded) {
		return nil, OverloadError(err)
	}
	if err != nil {
		return TextResult(err.Error(), true), nil
	}
	if isOverloadBody(resp.Status, resp.Body) {
		return nil, OverloadError(budget.ErrOverloaded)
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
			return nil, OverloadError(err)
		}
		res = TextResult(resp.Body, false)
	}
	if err := s.finish(ctx, res, ws, norms); err != nil {
		return nil, OverloadError(err)
	}
	return res, nil
}

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
// text is wrapped as {"result": value} or {"text": text}.
func structured(ctx context.Context, text string, extra map[string]any) (json.RawMessage, error) {
	meta, err := json.Marshal(extra)
	if err != nil {
		return nil, err
	}
	// the spliced copy, and the encoder's compaction of it, each copy the text
	if err := ReserveReply(ctx, 2*(len(text)+len(meta))+64); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace([]byte(text))
	var out bytes.Buffer
	switch {
	case len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed) && !bytes.Contains(trimmed, []byte(`"`+resultMetaMember+`"`)):
		body := bytes.TrimSpace(trimmed[1 : len(trimmed)-1])
		out.WriteByte('{')
		if len(body) > 0 {
			out.Write(body)
			out.WriteByte(',')
		}
	case len(trimmed) > 0 && json.Valid(trimmed):
		out.WriteString(`{"result":`)
		out.Write(trimmed)
		out.WriteByte(',')
	default:
		quoted, _ := json.Marshal(text)
		out.WriteString(`{"text":`)
		out.Write(quoted)
		out.WriteByte(',')
	}
	out.WriteString(`"` + resultMetaMember + `":`)
	out.Write(meta)
	out.WriteByte('}')
	return json.RawMessage(out.Bytes()), nil
}

// statusText names an HTTP status for error messages.
func statusText(code int) string {
	if t := http.StatusText(code); t != "" {
		return fmt.Sprintf("%d %s", code, t)
	}
	return fmt.Sprint(code)
}

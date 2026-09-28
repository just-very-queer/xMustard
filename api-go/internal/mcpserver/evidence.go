package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"xmustard/api-go/internal/budget"
)

// Evidence delivery over MCP. Tool calls ask the API for the evidence envelope: the
// agent receives a bounded projection plus, when anything was omitted, an
// xmustard://evidence/{handle} resource it pages through with resources/read. The nine
// tool names and schemas are unchanged; expansion is a resource, not a tenth tool.
// Evidence is both the Delivery and the Resources of one MCP connection (the stdio
// process, or one Streamable HTTP session), so every transport serves it identically.

const (
	DeliveryHeader  = "X-Xmustard-Delivery"
	DeliveryVersion = "xmustard.evidence/v1"
	ResourceScheme  = "xmustard://evidence/"
	// PageBytes is the largest page resources/read returns.
	PageBytes        = 64 << 10
	maxListedHandles = 100
)

// Evidence delivers enveloped tool results and serves the retained originals of one
// MCP connection.
type Evidence struct {
	backend Backend
	// SessionID identifies the connection in evidence audit metadata (never identity).
	SessionID string
	getenv    func(string) string

	mu    sync.Mutex
	ws    map[string]string // handle -> workspace it was issued in
	order []string
	meta  map[string]map[string]any
}

// NewEvidence returns the evidence delivery of one connection. getenv reads
// XMUSTARD_WORKSPACE_ID for bare URIs (nil: no fallback).
func NewEvidence(b Backend, getenv func(string) string) *Evidence {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return &Evidence{backend: b, SessionID: NewSessionID("mcp-"), getenv: getenv, ws: map[string]string{}, meta: map[string]map[string]any{}}
}

// NewSessionID returns prefix plus 128 random bits in hex.
func NewSessionID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

type callIDKey struct{}

// WithCallID records the JSON-RPC id of the request ctx serves, sent as the evidence
// call id.
func WithCallID(ctx context.Context, id json.RawMessage) context.Context {
	return context.WithValue(ctx, callIDKey{}, string(id))
}

// Headers asks the API for the evidence envelope.
func (e *Evidence) Headers(ctx context.Context) map[string]string {
	h := map[string]string{DeliveryHeader: DeliveryVersion, IssuerHeader: IssuerMCP, "X-Xmustard-Session-Id": e.SessionID}
	if id, ok := ctx.Value(callIDKey{}).(string); ok && id != "" {
		h["X-Xmustard-Call-Id"] = id
	}
	return h
}

// Result turns a delivered envelope into the tool result.
func (e *Evidence) Result(ctx context.Context, resp *APIResponse, ws string) (map[string]any, *RPCError, bool) {
	if resp.Status != http.StatusOK || resp.Header.Get(DeliveryHeader) != DeliveryVersion {
		return nil, nil, false
	}
	res, rerr := e.EnvelopeResult(ctx, resp.Body, ws)
	return res, rerr, true
}

type envelope struct {
	Tool             string            `json:"tool"`
	CallID           string            `json:"call_id"`
	Status           int               `json:"status"`
	IsError          bool              `json:"is_error"`
	ContentType      string            `json:"content_type"`
	Reduced          bool              `json:"reduced"`
	Projection       string            `json:"projection"`
	Handle           string            `json:"handle"`
	ResourceURI      string            `json:"resource_uri"`
	ExpiresAt        string            `json:"expires_at"`
	RawBytes         int64             `json:"raw_bytes"`
	RawSHA256        string            `json:"raw_sha256"`
	ProjectedBytes   int               `json:"projected_bytes"`
	ProjectionMode   string            `json:"projection_mode"`
	CapturedIdentity string            `json:"captured_identity"`
	Omissions        []json.RawMessage `json:"omissions"`
	TokensEst        int               `json:"delivered_tokens_est"`
}

// EnvelopeResult turns an evidence envelope into the MCP tool result: the projection
// as the tool text (errors stay errors), plus an expansion note and _meta when reduced.
func (e *Evidence) EnvelopeResult(ctx context.Context, body, ws string) (map[string]any, *RPCError) {
	// decoding copies the projection once and encoding the reply copies it again
	if err := ReserveReply(ctx, 2*len(body)); err != nil {
		return ReplyRefused(err)
	}
	var env envelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return TextResult("xmustard: undecodable evidence envelope: "+err.Error(), true), nil
	}
	if env.Status == http.StatusServiceUnavailable && strings.Contains(env.Projection, `"overloaded":true`) {
		return nil, OverloadError(budget.ErrOverloaded) // retryable: a protocol overload, not a tool error
	}
	content := []map[string]any{{"type": "text", "text": env.Projection}}
	res := map[string]any{"content": content, "isError": env.IsError}
	if !env.Reduced || env.Handle == "" {
		return res, nil
	}
	meta := map[string]any{
		"handle": env.Handle, "resource_uri": env.ResourceURI, "raw_bytes": env.RawBytes,
		"raw_sha256": env.RawSHA256, "projected_bytes": env.ProjectedBytes, "expires_at": env.ExpiresAt,
		"projection_mode": env.ProjectionMode, "captured_identity": env.CapturedIdentity,
		"omissions": len(env.Omissions), "tool": env.Tool, "call_id": env.CallID, "status": env.Status,
		"delivered_tokens_est": env.TokensEst,
	}
	e.remember(env.Handle, ws, meta)
	note := fmt.Sprintf("[xmustard evidence] %s result reduced from %d to %d bytes (%d omitted regions, identity %s). "+
		"The exact original is retained until %s: read it with resources/read uri=%s (pages of at most %d bytes; add &offset=N&length=N, or search it with &pattern=RE2 or &lines=A-B).",
		env.Tool, env.RawBytes, env.ProjectedBytes, len(env.Omissions), env.CapturedIdentity, env.ExpiresAt, env.ResourceURI, PageBytes)
	res["content"] = append(content, map[string]any{"type": "text", "text": note})
	res["_meta"] = map[string]any{"xmustard/evidence": meta}
	return res, nil
}

// remember keeps which workspace each handle this connection received belongs to, so
// resources/read can address the workspace-scoped API route from the bare URI.
func (e *Evidence) remember(handle, ws string, meta map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.ws[handle]; !ok {
		e.order = append(e.order, handle)
		if len(e.order) > 4*maxListedHandles {
			old := e.order[0]
			e.order = e.order[1:]
			delete(e.ws, old)
			delete(e.meta, old)
		}
	}
	e.ws[handle], e.meta[handle] = ws, meta
}

// List is resources/list: the most recently issued handles.
func (e *Evidence) List(context.Context) any {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := []map[string]any{}
	for i := len(e.order) - 1; i >= 0 && len(list) < maxListedHandles; i-- {
		m := e.meta[e.order[i]]
		list = append(list, map[string]any{
			"uri": m["resource_uri"], "name": fmt.Sprintf("%v result %v", m["tool"], m["call_id"]),
			"mimeType":    "application/octet-stream",
			"description": fmt.Sprintf("original %v bytes, expires %v", m["raw_bytes"], m["expires_at"]),
		})
	}
	return map[string]any{"resources": list}
}

// Templates is resources/templates/list.
func (e *Evidence) Templates(context.Context) any {
	return map[string]any{"resourceTemplates": []map[string]any{{
		"uriTemplate": ResourceScheme + "{handle}{?offset,length,workspace_id,pattern,query,lines,max_matches,context,start_line}",
		"name":        "xMustard evidence original",
		"description": "Exact bytes of a reduced xMustard tool result, in pages of at most 64 KiB (base64 blob); with pattern (RE2), query or lines=A-B, the matching lines as JSON.",
		"mimeType":    "application/octet-stream",
	}}}
}

func invalidResourceParams(msg string) *RPCError {
	return &RPCError{Code: CodeInvalidParams, Message: "invalid params: " + msg}
}

// Read answers resources/read for xmustard://evidence/{handle}?offset&length, or a
// search inside the original when the query asks for one.
func (e *Evidence) Read(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.URI == "" {
		return nil, invalidResourceParams("uri is required")
	}
	if !strings.HasPrefix(p.URI, ResourceScheme) {
		return nil, &RPCError{Code: CodeResourceNotFound, Message: "resource not found: " + p.URI, Data: map[string]any{"uri": p.URI, "reason": "unknown_scheme"}}
	}
	handle, rawQuery, _ := strings.Cut(strings.TrimPrefix(p.URI, ResourceScheme), "?")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, invalidResourceParams("bad resource query")
	}
	offset, length := int64(0), PageBytes
	if v := q.Get("offset"); v != "" {
		if offset, err = strconv.ParseInt(v, 10, 64); err != nil || offset < 0 {
			return nil, invalidResourceParams("offset")
		}
	}
	if v := q.Get("length"); v != "" {
		if length, err = strconv.Atoi(v); err != nil || length <= 0 || length > PageBytes {
			return nil, invalidResourceParams(fmt.Sprintf("length must be 1..%d", PageBytes))
		}
	}
	ws := e.workspaceOf(handle, q.Get("workspace_id"))
	if ws == "" {
		return nil, &RPCError{Code: CodeResourceNotFound, Message: "resource not found: handle not issued in this session; pass ?workspace_id=",
			Data: map[string]any{"uri": p.URI, "reason": "unknown_workspace"}}
	}
	if isSearch(q) {
		return e.search(ctx, p.URI, handle, ws, q)
	}
	path := fmt.Sprintf("/api/workspaces/%s/evidence/%s?offset=%d&length=%d", url.PathEscape(ws), url.PathEscape(handle), offset, length)
	resp, rerr := e.get(ctx, p.URI, path)
	if rerr != nil {
		return nil, rerr
	}
	var page map[string]any
	if err := json.Unmarshal([]byte(resp.Body), &page); err != nil {
		return nil, &RPCError{Code: CodeInternal, Message: "undecodable evidence page"}
	}
	data, _ := page["data"].(string)
	delete(page, "data")
	mime, _ := page["content_type"].(string)
	if mime == "" {
		mime = "application/octet-stream"
	}
	return map[string]any{
		"contents": []map[string]any{{"uri": p.URI, "mimeType": mime, "blob": data}},
		"_meta":    map[string]any{"xmustard/page": page},
	}, nil
}

// workspaceOf picks the workspace of a handle: the URI's own (it works unchanged after
// a restart), the one this connection issued it in, then XMUSTARD_WORKSPACE_ID.
func (e *Evidence) workspaceOf(handle, fromURI string) string {
	if fromURI != "" {
		return fromURI
	}
	e.mu.Lock()
	ws := e.ws[handle]
	e.mu.Unlock()
	if ws != "" {
		return ws
	}
	return strings.TrimSpace(e.getenv("XMUSTARD_WORKSPACE_ID"))
}

// get performs one evidence GET, mapping transport failures and refusals to errors.
func (e *Evidence) get(ctx context.Context, uri, path string) (*APIResponse, *RPCError) {
	resp, err := e.backend.Do(ctx, Request{Method: http.MethodGet, Path: path})
	switch {
	case errors.Is(err, budget.ErrOverloaded):
		return nil, OverloadError(err)
	case err != nil:
		return nil, &RPCError{Code: CodeInternal, Message: err.Error()}
	case resp.Status != http.StatusOK:
		return nil, evidenceRPCError(uri, resp)
	}
	return resp, nil
}

// Search inside a retained original through resources/read (PAR-CTX-04):
// xmustard://evidence/{handle}?pattern=RE2|query=text|lines=A-B[&max_matches&context]
// answers with the matching lines (line numbers, byte offsets, context) instead of a
// byte page. The API enforces workspace, principal and expiry exactly as for pages.

// searchParams are the resource query parameters forwarded to the search route.
var searchParams = []string{"pattern", "query", "lines", "max_matches", "context", "offset", "start_line"}

// isSearch reports whether a resource query asks for search rather than a page.
func isSearch(q url.Values) bool {
	return q.Get("pattern") != "" || q.Get("query") != "" || q.Get("lines") != ""
}

func (e *Evidence) search(ctx context.Context, uri, handle, ws string, q url.Values) (any, *RPCError) {
	fwd := url.Values{"handle": {handle}}
	for _, k := range searchParams {
		if v := q.Get(k); v != "" {
			fwd.Set(k, v)
		}
	}
	resp, rerr := e.get(ctx, uri, fmt.Sprintf("/api/workspaces/%s/evidence/search?%s", url.PathEscape(ws), fwd.Encode()))
	if rerr != nil {
		return nil, rerr
	}
	var res struct {
		Matches         int    `json:"matches"`
		MatchCapReached bool   `json:"match_cap_reached"`
		NextOffset      int64  `json:"next_offset"`
		NextLine        int    `json:"next_line"`
		EOF             bool   `json:"eof"`
		Freshness       string `json:"freshness"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &res); err != nil {
		return nil, &RPCError{Code: CodeInternal, Message: "undecodable evidence search result"}
	}
	return map[string]any{
		"contents": []map[string]any{{"uri": uri, "mimeType": "application/json", "text": resp.Body}},
		"_meta": map[string]any{"xmustard/search": map[string]any{"matches": res.Matches, "match_cap_reached": res.MatchCapReached,
			"next_offset": res.NextOffset, "next_line": res.NextLine, "eof": res.EOF, "freshness": res.Freshness}},
	}, nil
}

// evidenceRefusalCodes maps an evidence route refusal status to its resources/read
// error: missing, expired, revoked and denied are resource-not-found (never
// substituted); anything unlisted is internal.
var evidenceRefusalCodes = map[int]int{
	http.StatusNotFound:                     CodeResourceNotFound,
	http.StatusGone:                         CodeResourceNotFound,
	http.StatusForbidden:                    CodeResourceNotFound,
	http.StatusUnauthorized:                 CodeResourceNotFound,
	http.StatusRequestedRangeNotSatisfiable: CodeInvalidParams,
	http.StatusBadRequest:                   CodeInvalidParams,
	http.StatusServiceUnavailable:           CodeOverloaded,
}

func evidenceRPCError(uri string, resp *APIResponse) *RPCError {
	var e struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(resp.Body), &e)
	code, ok := evidenceRefusalCodes[resp.Status]
	if !ok {
		code = CodeInternal
	}
	return &RPCError{Code: code, Message: fmt.Sprintf("evidence %s: %s", e.Reason, e.Error),
		Data: map[string]any{"uri": uri, "status": resp.Status, "reason": e.Reason}}
}

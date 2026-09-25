package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/mcpserver"
)

// Evidence delivery over MCP. Tool calls ask the API for the evidence envelope: the
// agent receives a bounded projection plus, when anything was omitted, an
// xmustard://evidence/{handle} resource it pages through with resources/read. The nine
// tool names and schemas are unchanged; expansion is a resource, not a tenth tool.
// evidenceDelivery and evidenceResources plug this into the mcpserver session.

// evidenceDelivery asks every tool call for the evidence envelope and turns it into
// the tool result.
type evidenceDelivery struct{}

func (evidenceDelivery) Headers(ctx context.Context) map[string]string { return deliveryHeaders(ctx) }

func (evidenceDelivery) Result(ctx context.Context, resp *mcpserver.APIResponse, ws string) (map[string]any, *rpcError, bool) {
	if resp.Status != http.StatusOK || resp.Header.Get(deliveryHeader) != deliveryVersion {
		return nil, nil, false
	}
	res, rerr := evidenceResult(ctx, resp.Body, ws)
	return res, rerr, true
}

// evidenceResources serves the retained originals as xmustard://evidence resources.
type evidenceResources struct{}

func (evidenceResources) List(context.Context) any      { return resourcesListResult() }
func (evidenceResources) Templates(context.Context) any { return resourceTemplatesResult() }
func (evidenceResources) Read(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	return readResource(ctx, params)
}

// apiResponse is one admitted API response.
type apiResponse struct {
	status int
	header http.Header
	body   string
}

// callAPIResp performs one API request with extra headers and returns the admitted
// response whatever its status; transport, admission and size failures are errors.
func callAPIResp(ctx context.Context, method, path, body string, headers map[string]string) (*apiResponse, error) {
	resp, err := backend.Do(ctx, mcpserver.Request{Method: method, Path: path, Body: body, Headers: headers})
	if err != nil {
		return nil, err
	}
	return &apiResponse{status: resp.Status, header: resp.Header, body: resp.Body}, nil
}

const (
	deliveryHeader  = "X-Xmustard-Delivery"
	deliveryVersion = "xmustard.evidence/v1"
	resourceScheme  = "xmustard://evidence/"
	pageBytes       = 64 << 10
	// resourceNotFound is MCP's resource-not-found error code.
	resourceNotFound = -32002
	maxListedHandles = 100
)

// sessionID identifies this shim process in evidence audit metadata (never identity).
var sessionID = func() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "mcp-" + hex.EncodeToString(b[:])
}()

type callIDKey struct{}

func withCallID(ctx context.Context, id json.RawMessage) context.Context {
	return context.WithValue(ctx, callIDKey{}, string(id))
}

func deliveryHeaders(ctx context.Context) map[string]string {
	h := map[string]string{deliveryHeader: deliveryVersion, "X-Xmustard-Issuer": "mcp", "X-Xmustard-Session-Id": sessionID}
	if id, ok := ctx.Value(callIDKey{}).(string); ok && id != "" {
		h["X-Xmustard-Call-Id"] = id
	}
	return h
}

// issued remembers which workspace each handle this session received belongs to, so
// resources/read can address the workspace-scoped API route from the bare URI.
var issued = struct {
	sync.Mutex
	ws    map[string]string
	order []string
	meta  map[string]map[string]any
}{ws: map[string]string{}, meta: map[string]map[string]any{}}

func rememberHandle(handle, ws string, meta map[string]any) {
	issued.Lock()
	defer issued.Unlock()
	if _, ok := issued.ws[handle]; !ok {
		issued.order = append(issued.order, handle)
		if len(issued.order) > 4*maxListedHandles {
			old := issued.order[0]
			issued.order = issued.order[1:]
			delete(issued.ws, old)
			delete(issued.meta, old)
		}
	}
	issued.ws[handle], issued.meta[handle] = ws, meta
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

// evidenceResult turns an evidence envelope into the MCP tool result: the projection
// as the tool text (errors stay errors), plus an expansion note and _meta when reduced.
func evidenceResult(ctx context.Context, body, ws string) (map[string]any, *rpcError) {
	// decoding copies the projection once and encoding the reply copies it again
	if err := reserveReply(ctx, 2*len(body)); err != nil {
		return replyRefused(err)
	}
	var env envelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return mcpText("xmustard: undecodable evidence envelope: "+err.Error(), true), nil
	}
	if env.Status == http.StatusServiceUnavailable && strings.Contains(env.Projection, `"overloaded":true`) {
		return nil, overloadError(budget.ErrOverloaded) // retryable: a protocol overload, not a tool error
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
	rememberHandle(env.Handle, ws, meta)
	note := fmt.Sprintf("[xmustard evidence] %s result reduced from %d to %d bytes (%d omitted regions, identity %s). "+
		"The exact original is retained until %s: read it with resources/read uri=%s (pages of at most %d bytes; add &offset=N&length=N, or search it with &pattern=RE2 or &lines=A-B).",
		env.Tool, env.RawBytes, env.ProjectedBytes, len(env.Omissions), env.CapturedIdentity, env.ExpiresAt, env.ResourceURI, pageBytes)
	res["content"] = append(content, map[string]any{"type": "text", "text": note})
	res["_meta"] = map[string]any{"xmustard/evidence": meta}
	return res, nil
}

// reserveReply reserves n bytes of reply construction in the request's ledger (held
// until the reply is written). Direct callers without a ledger reserve nothing.
func reserveReply(ctx context.Context, n int) error { return mcpserver.ReserveReply(ctx, n) }

func resourcesListResult() map[string]any {
	issued.Lock()
	defer issued.Unlock()
	list := []map[string]any{}
	for i := len(issued.order) - 1; i >= 0 && len(list) < maxListedHandles; i-- {
		h := issued.order[i]
		m := issued.meta[h]
		list = append(list, map[string]any{
			"uri": m["resource_uri"], "name": fmt.Sprintf("%v result %v", m["tool"], m["call_id"]),
			"mimeType":    "application/octet-stream",
			"description": fmt.Sprintf("original %v bytes, expires %v", m["raw_bytes"], m["expires_at"]),
		})
	}
	return map[string]any{"resources": list}
}

func resourceTemplatesResult() map[string]any {
	return map[string]any{"resourceTemplates": []map[string]any{{
		"uriTemplate": resourceScheme + "{handle}{?offset,length,workspace_id,pattern,query,lines,max_matches,context,start_line}",
		"name":        "xMustard evidence original",
		"description": "Exact bytes of a reduced xMustard tool result, in pages of at most 64 KiB (base64 blob); with pattern (RE2), query or lines=A-B, the matching lines as JSON.",
		"mimeType":    "application/octet-stream",
	}}}
}

// readResource answers resources/read for xmustard://evidence/{handle}?offset&length.
func readResource(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.URI == "" {
		return nil, &rpcError{Code: -32602, Message: "invalid params: uri is required"}
	}
	if !strings.HasPrefix(p.URI, resourceScheme) {
		return nil, &rpcError{Code: resourceNotFound, Message: "resource not found: " + p.URI, Data: map[string]any{"uri": p.URI, "reason": "unknown_scheme"}}
	}
	rest := strings.TrimPrefix(p.URI, resourceScheme)
	handle, rawQuery, _ := strings.Cut(rest, "?")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: bad resource query"}
	}
	offset, length := int64(0), pageBytes
	if v := q.Get("offset"); v != "" {
		if offset, err = strconv.ParseInt(v, 10, 64); err != nil || offset < 0 {
			return nil, &rpcError{Code: -32602, Message: "invalid params: offset"}
		}
	}
	if v := q.Get("length"); v != "" {
		if length, err = strconv.Atoi(v); err != nil || length <= 0 || length > pageBytes {
			return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("invalid params: length must be 1..%d", pageBytes)}
		}
	}
	// the issued URI names its workspace, so it works unchanged after a restart; the
	// session map covers bare URIs issued to this process
	ws := q.Get("workspace_id")
	if ws == "" {
		issued.Lock()
		ws = issued.ws[handle]
		issued.Unlock()
	}
	if ws == "" {
		ws = strings.TrimSpace(os.Getenv("XMUSTARD_WORKSPACE_ID"))
	}
	if ws == "" {
		return nil, &rpcError{Code: resourceNotFound, Message: "resource not found: handle not issued in this session; pass ?workspace_id=",
			Data: map[string]any{"uri": p.URI, "reason": "unknown_workspace"}}
	}
	if isSearch(q) {
		return searchResource(ctx, p.URI, handle, ws, q)
	}
	path := fmt.Sprintf("/api/workspaces/%s/evidence/%s?offset=%d&length=%d", url.PathEscape(ws), url.PathEscape(handle), offset, length)
	resp, err := callAPIResp(ctx, "GET", path, "", nil)
	if err != nil {
		if strings.Contains(err.Error(), budget.ErrOverloaded.Error()) {
			return nil, overloadError(err)
		}
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	if resp.status != http.StatusOK {
		return nil, evidenceRPCError(p.URI, resp)
	}
	var page map[string]any
	if err := json.Unmarshal([]byte(resp.body), &page); err != nil {
		return nil, &rpcError{Code: -32603, Message: "undecodable evidence page"}
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

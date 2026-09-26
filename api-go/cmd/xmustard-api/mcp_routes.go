package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/mcpserver"
	"xmustard/api-go/internal/workspaceops"
)

// MCP over Streamable HTTP (PAR-RT-03, PAR-ADP-05). The API serves the nine tools at
// /mcp from its own process, so an agent needs no per-agent shim: a URL-capable client
// connects directly, a stdio-only one through the native relay
// (rust-core/src/bin/xmustard-relay.rs). Each MCP session is an mcpserver.Session, and
// every tool call re-enters this API through the full middleware stack (exposure,
// auth, route gates, evidence delivery) as the caller, carrying the caller's bearer
// token: caller-scoped tools/list, the route gates, output budgets, evidence resources
// and scoped auto-registration apply exactly as for the stdio shim.
//
// Query parameters (none is a credential; the token stays in Authorization):
//
//	workspace=<id>  bind the session to a workspace (or X-Xmustard-Workspace)
//	mode=full|readonly
//	client=claude-code|codex|cursor|opencode|pi|letta
//	schema=lean|full

// mcpWorkspaceHeader binds a session to a workspace, like ?workspace=.
const mcpWorkspaceHeader = "X-Xmustard-Workspace"

func registerMCPRoutes(mux routeRegistrar) {
	endpoint := mcpserver.NewStreamableHTTP(mcpserver.HTTPOptions{Open: openMCPSession, Owner: mcpOwner})
	serve := func(w http.ResponseWriter, r *http.Request) {
		endpoint.ServeHTTP(w, r.WithContext(withMCPCaller(r)))
	}
	for _, pattern := range []string{"POST /mcp", "GET /mcp", "DELETE /mcp"} {
		mux.HandleFunc(pattern, serve)
	}
}

// mcpOwner is the principal a session belongs to; in open mode every caller is the
// one local identity.
func mcpOwner(r *http.Request) string {
	if p := principalFromContext(r.Context()); p != nil {
		return "principal:" + p.ID
	}
	return "open:" + workspaceops.OpenModeIdentity
}

// openMCPSession builds a session's server from its initialize request. The
// workspace binding is refused up front when the deployment or the caller's token
// cannot use it; the API re-checks it on every call regardless.
func openMCPSession(r *http.Request) (*mcpserver.Server, error) {
	q := r.URL.Query()
	readOnly, err := mcpserver.ParseMode(q.Get("mode"))
	if err != nil {
		return nil, err
	}
	client, err := mcpserver.ParseClientProfile(q.Get("client"))
	if err != nil {
		return nil, err
	}
	schema, err := mcpserver.ParseSchemaProfile(q.Get("schema"))
	if err != nil {
		return nil, errors.New(strings.Replace(err.Error(), "XMUSTARD_MCP_SCHEMA", "schema", 1))
	}
	ws := strings.TrimSpace(q.Get("workspace"))
	if ws == "" {
		ws = strings.TrimSpace(r.Header.Get(mcpWorkspaceHeader))
	}
	if ws != "" {
		if !workspaceops.IsSafeID(ws) {
			return nil, fmt.Errorf("invalid workspace binding %q", ws)
		}
		if !postureFrom(r).allowsWorkspace(ws) {
			return nil, fmt.Errorf("workspace %s is not served by this deployment", ws)
		}
		if p := principalFromContext(r.Context()); p != nil && !p.AllowsWorkspace(ws) {
			return nil, fmt.Errorf("token not scoped to workspace %s", ws)
		}
	}
	backend := loopbackBackend{}
	home, _ := os.UserHomeDir()
	evidence := mcpserver.NewEvidence(backend, nil)
	return mcpserver.New(mcpserver.Options{
		Backend:     backend,
		Delivery:    evidence,
		Resources:   evidence,
		Schema:      schema,
		Getenv:      func(string) string { return "" }, // the API's environment is not the client's
		WorkspaceID: ws,
		ReadOnly:    readOnly,
		Client:      client,
		// roots-based registration goes through POST /api/workspaces/load as the
		// caller, so the registration scope (WS-09B) decides it
		AutoRegister: strings.TrimSpace(os.Getenv("XMUSTARD_MCP_AUTO_REGISTER")) != "0",
		HomeDir:      home,
	}), nil
}

// mcpCaller is what a tool call needs of the MCP request it serves: the full handler
// stack to re-enter, and the caller's credentials and origin.
type mcpCaller struct {
	handler       http.Handler
	authorization string
	host          string
	remoteAddr    string
}

type mcpCallerKey struct{}

func withMCPCaller(r *http.Request) context.Context {
	return context.WithValue(r.Context(), mcpCallerKey{}, mcpCaller{
		handler: loopbackFrom(r.Context()), authorization: r.Header.Get("Authorization"), host: r.Host, remoteAddr: r.RemoteAddr,
	})
}

// loopbackKey carries the outermost API handler, so /mcp can re-enter it.
type loopbackKey struct{}

func loopbackFrom(ctx context.Context) http.Handler {
	h, _ := ctx.Value(loopbackKey{}).(http.Handler)
	return h
}

// withLoopback is the outermost wrapper of the API handler: it records itself in the
// request context for the MCP endpoint's tool calls.
func withLoopback(next http.Handler) http.Handler {
	var self http.Handler
	self = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), loopbackKey{}, self)))
	})
	return self
}

// loopbackBackend performs a session's API calls in process, through the full
// handler stack, as the MCP caller.
type loopbackBackend struct{}

func (loopbackBackend) Do(ctx context.Context, req mcpserver.Request) (*mcpserver.APIResponse, error) {
	caller, _ := ctx.Value(mcpCallerKey{}).(mcpCaller)
	if caller.handler == nil {
		return nil, errors.New("xmustard: the MCP endpoint is not wired to the API handler")
	}
	// the inner request authenticates itself from the forwarded token: no principal
	// resolved for the /mcp request itself leaks into it
	ctx = context.WithValue(ctx, principalCtxKey, (*workspaceops.Principal)(nil))
	var body io.Reader
	if req.Body != "" {
		body = strings.NewReader(req.Body)
	}
	inner, err := http.NewRequestWithContext(ctx, req.Method, "http://"+caller.host+req.Path, body)
	if err != nil {
		return nil, err
	}
	inner.Host, inner.RemoteAddr = caller.host, caller.remoteAddr
	if req.Body != "" {
		inner.Header.Set("Content-Type", "application/json")
	}
	for k, v := range req.Headers {
		inner.Header.Set(k, v)
	}
	if caller.authorization != "" {
		inner.Header.Set("Authorization", caller.authorization)
	}
	scope, owned := budget.ScopeFor(ctx)
	if owned {
		defer scope.Close()
	}
	rec := &boundedRecorder{header: http.Header{}, scope: scope, limit: mcpserver.MaxResponseBytes, discard: req.Discard}
	caller.handler.ServeHTTP(rec, inner)
	switch {
	case errors.Is(rec.err, budget.ErrOverloaded):
		return nil, fmt.Errorf("API %s %s: %w", req.Method, req.Path, budget.ErrOverloaded)
	case errors.Is(rec.err, budget.ErrNeverFits):
		return nil, fmt.Errorf("API %s %s response is larger than the transient budget; narrow the query", req.Method, req.Path)
	case rec.err != nil:
		return nil, fmt.Errorf("API %s %s response exceeded %d bytes; narrow the query", req.Method, req.Path, mcpserver.MaxResponseBytes)
	}
	return &mcpserver.APIResponse{Status: rec.status(), Header: rec.header, Body: rec.buf.String()}, nil
}

// boundedRecorder buffers an in-process response under admission: each chunk is
// reserved before it is kept, and past the cap the response fails instead of being
// truncated. A discarded success body is not kept at all.
type boundedRecorder struct {
	header  http.Header
	code    int
	buf     bytes.Buffer
	scope   *budget.Scope
	limit   int
	discard bool
	err     error
}

func (b *boundedRecorder) Header() http.Header { return b.header }

func (b *boundedRecorder) WriteHeader(code int) {
	if b.code == 0 {
		b.code = code
	}
}

func (b *boundedRecorder) status() int {
	if b.code == 0 {
		return http.StatusOK
	}
	return b.code
}

func (b *boundedRecorder) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	switch {
	case b.err != nil, b.discard && b.code < 400:
	case b.buf.Len()+len(p) > b.limit:
		b.err, b.buf = errors.New("too large"), bytes.Buffer{}
	default:
		if err := b.scope.Acquire(int64(len(p))); err != nil {
			b.err, b.buf = err, bytes.Buffer{}
			break
		}
		b.buf.Write(p)
	}
	return len(p), nil
}

// mcpUsageFor is the /api/health "mcp_usage" block: the endpoint's per-tool and
// per-client counters, shown when the budget block is (they are activity).
func mcpUsageFor(r *http.Request) any {
	if !healthBudgetVisible(r) {
		return nil
	}
	return mcpserver.Usage()
}

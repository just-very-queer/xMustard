package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"xmustard/api-go/internal/budget"
)

// Request is one xMustard API call made on behalf of an MCP request.
type Request struct {
	Method  string
	Path    string // path with query, relative to the API base
	Body    string // JSON body; "" sends none
	Headers map[string]string
	// Discard drains a successful body (bounded) instead of buffering it, for calls
	// whose answer is only the status (workspace registration returns a snapshot).
	Discard bool
}

// APIResponse is one admitted API response.
type APIResponse struct {
	Status int
	Header http.Header
	Body   string
}

// Backend performs API calls. The stdio shim uses HTTPBackend; an in-process
// transport can call the handlers directly.
type Backend interface {
	Do(ctx context.Context, req Request) (*APIResponse, error)
}

// MaxResponseBytes bounds a single API response the MCP server will buffer.
const MaxResponseBytes = 16 << 20 // 16 MiB

// maxDiscardBytes bounds how much of a discarded body is drained before the
// connection is dropped instead.
const maxDiscardBytes = 64 << 20

// HTTPBackend calls the xMustard HTTP API. Base and token are read per call (from
// XMUSTARD_API_BASE and XMUSTARD_API_TOKEN unless overridden), so one process can be
// repointed by its environment.
type HTTPBackend struct {
	Base    func() string
	Token   func() string
	Timeout time.Duration
}

// APIBase returns XMUSTARD_API_BASE without a trailing slash, or the local default.
func APIBase() string {
	if v := os.Getenv("XMUSTARD_API_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:8042"
}

func (b *HTTPBackend) base() string {
	if b.Base != nil {
		return b.Base()
	}
	return APIBase()
}

func (b *HTTPBackend) token() string {
	if b.Token != nil {
		return b.Token()
	}
	return strings.TrimSpace(os.Getenv("XMUSTARD_API_TOKEN"))
}

// Do performs one API request bound to ctx: cancelling ctx (an MCP
// notifications/cancelled) aborts the in-flight HTTP request, and the API handler's
// request context and the processes under it are cancelled in turn. The response is
// returned whatever its status; transport, admission and size failures are errors.
func (b *HTTPBackend) Do(ctx context.Context, r Request) (*APIResponse, error) {
	var bodyReader io.Reader
	if r.Body != "" {
		bodyReader = strings.NewReader(r.Body) // no second copy of the argument payload
	}
	base := b.base()
	req, err := http.NewRequestWithContext(ctx, r.Method, base+r.Path, bodyReader)
	if err != nil {
		return nil, err
	}
	if r.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	// Each agent runs its own MCP server; XMUSTARD_API_TOKEN is that agent's bearer
	// token, so the API resolves a real per-agent identity (and the multi-agent
	// verification gate counts distinct authenticated principals).
	if tok := b.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("xmustard API unreachable at %s (%w)", base, err)
	}
	defer resp.Body.Close()
	if r.Discard && resp.StatusCode < 400 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDiscardBytes))
		return &APIResponse{Status: resp.StatusCode, Header: resp.Header}, nil
	}
	// Bound the response read: an unbounded read lets a huge/hostile API response
	// allocate without limit (the egress analogue of the 8 MiB request framing cap,
	// XM-PRO-009). Bytes are reserved against the transient pool before they are
	// buffered and held until the reply is written; past the cap we fail loudly
	// rather than return truncated JSON.
	scope, owned := budget.ScopeFor(ctx)
	if owned {
		defer scope.Close()
	}
	raw, rerr := budget.ReadAllAdmitted(scope, resp.Body, MaxResponseBytes)
	switch {
	case errors.Is(rerr, budget.ErrOverloaded):
		return nil, fmt.Errorf("API %s %s: %w", r.Method, r.Path, budget.ErrOverloaded)
	case errors.Is(rerr, budget.ErrNeverFits):
		return nil, fmt.Errorf("API %s %s response is larger than this shim's transient budget; narrow the query", r.Method, r.Path)
	case errors.Is(rerr, budget.ErrTooLarge):
		return nil, fmt.Errorf("API %s %s response exceeded %d bytes; narrow the query", r.Method, r.Path, MaxResponseBytes)
	case rerr != nil:
		return nil, fmt.Errorf("API %s %s: read response: %w", r.Method, r.Path, rerr)
	}
	return &APIResponse{Status: resp.StatusCode, Header: resp.Header, Body: string(raw)}, nil
}

// isOverloadBody reports whether an API error body is an admission refusal.
func isOverloadBody(status int, body string) bool {
	return status == http.StatusServiceUnavailable && strings.Contains(body, `"overloaded":true`)
}

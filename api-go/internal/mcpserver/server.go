// Package mcpserver is xMustard's MCP server core: the nine-tool table, argument
// validation, protocol negotiation, workspace resolution and result shaping. It is
// transport-free: the stdio shim (cmd/xmustard-mcp) frames JSON-RPC over
// stdin/stdout and hands each message to a Session; an HTTP endpoint or a hook
// handler can drive the same Session API.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"xmustard/api-go/internal/budget"
)

// JSON-RPC and MCP error codes.
const (
	CodeParseError       = -32700
	CodeInvalidRequest   = -32600
	CodeMethodNotFound   = -32601
	CodeInvalidParams    = -32602
	CodeInternal         = -32603
	CodeOverloaded       = -32000 // server error: admission refused, retry shortly
	CodeResourceNotFound = -32002 // MCP resource-not-found
)

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

// OverloadError is the JSON-RPC answer to an admission refusal.
func OverloadError(err error) *RPCError { return &RPCError{Code: CodeOverloaded, Message: err.Error()} }

// CodeTooLarge answers a request that can never be admitted: past the framing cap, or
// holding more than the whole transient pool. Unlike CodeOverloaded it is permanent, so
// a client does not retry it.
const CodeTooLarge = CodeInvalidRequest

// AdmissionError answers a refused reservation: permanently when the request could
// never fit the pool (budget.ErrNeverFits wraps budget.ErrTooLarge), with the retryable
// overload when it does not fit for now.
func AdmissionError(err error) *RPCError {
	if errors.Is(err, budget.ErrTooLarge) {
		return &RPCError{Code: CodeTooLarge, Message: "request exceeds max message size: " + err.Error()}
	}
	return OverloadError(err)
}

// ReplyRefused answers a refused reply reservation: an API result the server could never
// relay is a tool error (narrow the query); a pool busy for now is the retryable overload.
func ReplyRefused(err error) (map[string]any, *RPCError) {
	if errors.Is(err, budget.ErrTooLarge) {
		return TextResult("xmustard: the result is larger than this shim's transient budget; narrow the query ("+err.Error()+")", true), nil
	}
	return nil, OverloadError(err)
}

// Protocol versions this server implements, newest first. 2025-03-26 is not listed:
// it requires accepting JSON-RPC batches, which this server does not.
const (
	version20241105 = "2024-11-05"
	version20250618 = "2025-06-18"
	// LatestProtocolVersion is offered to clients that ask for a newer version.
	LatestProtocolVersion = version20250618
	// LegacyProtocolVersion is the version every client was answered with before
	// negotiation existed; it is kept for clients that name no version.
	LegacyProtocolVersion = version20241105
)

var supportedVersions = []string{version20250618, version20241105}

// NegotiateVersion picks the protocol version answered to an initialize request. A
// supported version is echoed. A newer one gets the latest we implement (the client
// decides whether to continue). An older unsupported one (such as 2025-03-26) gets
// the newest supported version not newer than it, so a client limited to older
// revisions is not handed one it cannot speak. A missing or malformed version gets the
// legacy version.
func NegotiateVersion(requested string) string {
	if contains(supportedVersions, requested) {
		return requested
	}
	if !isVersionDate(requested) {
		return LegacyProtocolVersion
	}
	if requested > LatestProtocolVersion {
		return LatestProtocolVersion
	}
	for _, v := range supportedVersions {
		if v <= requested {
			return v
		}
	}
	return LegacyProtocolVersion
}

func isVersionDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range s {
		if i != 4 && i != 7 && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// atLeast reports whether negotiated version v includes the features of min.
func atLeast(v, min string) bool { return v >= min }

// Delivery is the optional evidence-delivery extension: it adds request headers to
// each tool call and turns a delivered envelope into the tool result.
type Delivery interface {
	Headers(ctx context.Context) map[string]string
	// Result converts resp into the tool result when it carries a delivery envelope;
	// ok is false when it does not.
	Result(ctx context.Context, resp *APIResponse, workspaceID string) (res map[string]any, rerr *RPCError, ok bool)
}

// Resources serves resources/list, resources/templates/list and resources/read.
type Resources interface {
	List(ctx context.Context) any
	Templates(ctx context.Context) any
	Read(ctx context.Context, params json.RawMessage) (any, *RPCError)
}

// ClientRequester sends a server-to-client request (roots/list) and waits for the
// answer. A transport that cannot do so leaves it nil.
type ClientRequester interface {
	Request(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// Options configure a Server.
type Options struct {
	Backend   Backend
	Delivery  Delivery  // optional
	Resources Resources // optional; resources capability is advertised only when set
	// Cwd is the server process's working directory, the last workspace signal. An
	// HTTP transport has none and leaves it empty.
	Cwd string
	// Getenv reads XMUSTARD_WORKSPACE_ID (os.Getenv when nil).
	Getenv func(string) string
	// AutoRegister lets a git repository found from roots, cwd or a path argument be
	// registered with the API when no registered workspace contains it.
	AutoRegister bool
	// HomeDir is never auto-registered, even when it is a git repository.
	HomeDir string
	Name    string
	Version string
}

// Server holds what every session shares.
type Server struct{ opts Options }

// New returns a Server. Backend is required.
func New(opts Options) *Server {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Name == "" {
		opts.Name = "xmustard"
	}
	if opts.Version == "" {
		opts.Version = "0.1.0"
	}
	return &Server{opts: opts}
}

// Session is one MCP connection: its negotiated version, the client's declared
// capabilities, and caches that live as long as it does.
type Session struct {
	srv    *Server
	client ClientRequester

	mu          sync.Mutex
	version     string // "" until initialize
	rootsCap    bool   // the client declared the roots capability
	initialized bool   // notifications/initialized received: server requests allowed
	roots       *rootsState
	rootsGen    uint64 // bumped by roots/list_changed and initialize; guards s.roots
	listing     []registeredWorkspace
	listedAt    time.Time
	byPath      map[string]Workspace // resolved roots and working directories (bounded)
	echoed      map[string]bool      // workspace+source pairs already echoed in text

	resolving chan struct{} // serializes path-based resolution (it may register)
}

// NewSession starts a session; client may be nil.
func (s *Server) NewSession(client ClientRequester) *Session {
	return &Session{srv: s, client: client, byPath: map[string]Workspace{}, echoed: map[string]bool{}, resolving: make(chan struct{}, 1)}
}

// Version is the negotiated protocol version; before initialize the latest is assumed.
func (s *Session) Version() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.version == "" {
		return LatestProtocolVersion
	}
	return s.version
}

// Handle answers one JSON-RPC request.
func (s *Session) Handle(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	switch method {
	case "initialize":
		return s.initialize(params)
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return s.toolsList(s.callerTools(ctx)), nil
	case "tools/call":
		return s.callTool(ctx, params)
	case "resources/list", "resources/templates/list", "resources/read":
		r := s.srv.opts.Resources
		if r == nil {
			break
		}
		switch method {
		case "resources/list":
			return r.List(ctx), nil
		case "resources/templates/list":
			return r.Templates(ctx), nil
		default:
			return r.Read(ctx, params)
		}
	}
	return nil, &RPCError{Code: CodeMethodNotFound, Message: "method not found: " + method}
}

// Notify handles a client notification the session cares about. Cancellation is the
// transport's job (it owns the in-flight registry).
func (s *Session) Notify(method string, _ json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case "notifications/initialized":
		s.initialized = true
	case "notifications/roots/list_changed":
		// a roots/list answer still in flight describes the old roots: the generation
		// bump keeps it from being cached
		s.roots = nil
		s.rootsGen++
	}
}

func (s *Session) initialize(params json.RawMessage) (any, *RPCError) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Roots *json.RawMessage `json:"roots"`
		} `json:"capabilities"`
	}
	if len(params) > 0 && strings.TrimSpace(string(params)) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "invalid params: " + err.Error()}
		}
	}
	v := NegotiateVersion(p.ProtocolVersion)
	s.mu.Lock()
	s.version = v
	s.rootsCap = p.Capabilities.Roots != nil
	s.roots = nil
	s.rootsGen++
	s.mu.Unlock()
	caps := map[string]any{"tools": map[string]any{}}
	if s.srv.opts.Resources != nil {
		caps["resources"] = map[string]any{}
	}
	return map[string]any{
		"protocolVersion": v,
		"capabilities":    caps,
		"serverInfo":      map[string]any{"name": s.srv.opts.Name, "version": s.srv.opts.Version},
		"instructions":    Instructions,
	}, nil
}

// ToolsList is the tools/list result under the negotiated version, every tool listed.
func (s *Session) ToolsList() map[string]any { return s.toolsList(nil) }

// toolsList lists the tools in allowed, or every tool when allowed is nil.
func (s *Session) toolsList(allowed map[string]bool) map[string]any {
	v := s.Version()
	list := []map[string]any{}
	for _, t := range Tools() {
		if allowed == nil || allowed[t.Name] {
			list = append(list, t.listEntry(v))
		}
	}
	return map[string]any{"tools": list}
}

// callerToolsTimeout bounds the tools/list posture lookup so a slow API can't stall
// session start; on any failure the full list is advertised (the API still enforces).
const callerToolsTimeout = 2 * time.Second

// callerTools asks the API which tools this caller can use on this deployment
// (GET /api/auth/whoami "tools": the caller's roles, read-only mode, disabled tools
// and profile applied), so tools/list does not offer remember/verify under a reader
// token or in read-only mode. It returns nil when the API can't say (unreachable, an
// older API without the field), and tools/list then advertises every tool.
func (s *Session) callerTools(ctx context.Context) map[string]bool {
	ctx, cancel := context.WithTimeout(ctx, callerToolsTimeout)
	defer cancel()
	resp, err := s.srv.opts.Backend.Do(ctx, Request{Method: http.MethodGet, Path: "/api/auth/whoami"})
	if err != nil || resp.Status != http.StatusOK {
		return nil
	}
	var who struct {
		Tools *[]string `json:"tools"`
	}
	if json.Unmarshal([]byte(resp.Body), &who) != nil || who.Tools == nil {
		return nil
	}
	allowed := map[string]bool{} // non-nil even when empty: the caller can use no tool
	for _, t := range *who.Tools {
		allowed[t] = true
	}
	return allowed
}

// TextResult is a tools/call result carrying one text block.
func TextResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

// ReserveReply reserves n bytes of reply construction in the request's ledger (held
// until the reply is written). Callers without a ledger reserve nothing.
func ReserveReply(ctx context.Context, n int) error {
	scope, owned := budget.ScopeFor(ctx)
	if owned {
		scope.Close()
		return nil
	}
	return scope.Acquire(int64(n))
}

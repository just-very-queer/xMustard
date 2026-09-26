package mcpserver

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Streamable HTTP transport (MCP 2025-06-18, PAR-RT-03). One POST carries one
// JSON-RPC message:
//
//   - initialize opens a session and answers with its Mcp-Session-Id; every later
//     message must carry that header and come from the caller that opened it.
//   - a request is answered as application/json, or as a text/event-stream when the
//     client accepts one and the call may need to ask the client something first
//     (tools/call resolving the workspace from roots/list). The stream carries those
//     server requests and then the response.
//   - a notification or a response to a server request is answered 202 Accepted;
//     notifications/cancelled aborts the named in-flight request.
//
// DELETE ends the session. There is no standalone server stream (GET answers 405).
// Batches are refused, as over stdio. Each session is a Session, so tools/list,
// validation, workspace resolution, evidence and usage behave exactly as on stdio.

// Streamable HTTP header names.
const (
	HeaderSessionID       = "Mcp-Session-Id"
	HeaderProtocolVersion = "Mcp-Protocol-Version"
)

// HTTPOptions configure a StreamableHTTP endpoint.
type HTTPOptions struct {
	// Open builds the server of a new session from its initialize request (the
	// caller's backend, workspace binding, mode and client profile). An error is
	// answered 400 and opens nothing.
	Open func(r *http.Request) (*Server, error)
	// Owner names the caller of r. A session answers only the caller that opened it.
	Owner func(r *http.Request) string
	// MaxSessions bounds open sessions (default 64); IdleTimeout ends a session
	// unused that long (default 30m); MaxInflight bounds one session's concurrent
	// requests (default 8); MaxMessageBytes bounds one message (default 8 MiB).
	MaxSessions     int
	IdleTimeout     time.Duration
	MaxInflight     int
	MaxMessageBytes int64
}

// StreamableHTTP is the MCP endpoint's session table and handler.
type StreamableHTTP struct {
	opts     HTTPOptions
	mu       sync.Mutex
	sessions map[string]*httpSession
}

// NewStreamableHTTP returns an endpoint. Open and Owner are required.
func NewStreamableHTTP(opts HTTPOptions) *StreamableHTTP {
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = 64
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 30 * time.Minute
	}
	if opts.MaxInflight <= 0 {
		opts.MaxInflight = 8
	}
	if opts.MaxMessageBytes <= 0 {
		opts.MaxMessageBytes = 8 << 20
	}
	return &StreamableHTTP{opts: opts, sessions: map[string]*httpSession{}}
}

// Sessions reports how many sessions are open.
func (h *StreamableHTTP) Sessions() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

type httpSession struct {
	id, owner string
	sess      *Session
	client    *httpClient
	slots     chan struct{}

	mu       sync.Mutex
	lastUsed time.Time
	inflight map[string]context.CancelFunc
}

// rpcMessage is any incoming message: a request, a notification, or the client's
// response to a server request.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (m *rpcMessage) isResponse() bool {
	return m.Method == "" && len(m.ID) > 0 && (m.Result != nil || m.Error != nil)
}

func reply(id json.RawMessage, result any, rerr *RPCError) rpcResponse {
	if rerr != nil {
		return rpcResponse{JSONRPC: "2.0", ID: id, Error: rerr}
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func writeRPC(w http.ResponseWriter, status int, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *StreamableHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.post(w, r)
	case http.MethodDelete:
		h.end(w, r)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		writeRPC(w, http.StatusMethodNotAllowed, reply(nil, nil, &RPCError{Code: CodeInvalidRequest, Message: "no server-initiated stream: POST messages, DELETE to end the session"}))
	}
}

func (h *StreamableHTTP) post(w http.ResponseWriter, r *http.Request) {
	if v := r.Header.Get(HeaderProtocolVersion); v != "" && !contains(supportedVersions, v) {
		writeRPC(w, http.StatusBadRequest, reply(nil, nil, &RPCError{Code: CodeInvalidRequest, Message: "unsupported " + HeaderProtocolVersion + ": " + v}))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, h.opts.MaxMessageBytes+1))
	switch {
	case err != nil:
		writeRPC(w, http.StatusBadRequest, reply(nil, nil, &RPCError{Code: CodeParseError, Message: "read error: " + err.Error()}))
		return
	case int64(len(body)) > h.opts.MaxMessageBytes:
		writeRPC(w, http.StatusRequestEntityTooLarge, reply(nil, nil, &RPCError{Code: CodeTooLarge, Message: "request exceeds max message size"}))
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		writeRPC(w, http.StatusBadRequest, reply(nil, nil, &RPCError{Code: CodeInvalidRequest, Message: "JSON-RPC batches are not supported"}))
		return
	}
	var m rpcMessage
	if json.Unmarshal(body, &m) != nil {
		writeRPC(w, http.StatusBadRequest, reply(nil, nil, &RPCError{Code: CodeParseError, Message: "parse error"}))
		return
	}
	if m.Method == "initialize" && len(m.ID) > 0 {
		h.initialize(w, r, m)
		return
	}
	hs, status, why := h.lookup(r)
	if hs == nil {
		writeRPC(w, status, reply(m.ID, nil, &RPCError{Code: CodeInvalidRequest, Message: why}))
		return
	}
	switch {
	case m.isResponse():
		hs.client.deliver(m)
		w.WriteHeader(http.StatusAccepted)
	case len(m.ID) == 0:
		hs.notify(m)
		w.WriteHeader(http.StatusAccepted)
	default:
		h.request(w, r, hs, m)
	}
}

func (h *StreamableHTTP) initialize(w http.ResponseWriter, r *http.Request, m rpcMessage) {
	srv, err := h.opts.Open(r)
	if err != nil {
		writeRPC(w, http.StatusBadRequest, reply(m.ID, nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}))
		return
	}
	client := newHTTPClient()
	hs := &httpSession{id: NewSessionID(""), owner: h.opts.Owner(r), sess: srv.NewSession(client), client: client,
		slots: make(chan struct{}, h.opts.MaxInflight), lastUsed: time.Now(), inflight: map[string]context.CancelFunc{}}
	result, rerr := hs.sess.Handle(r.Context(), m.Method, m.Params)
	if rerr != nil {
		writeRPC(w, http.StatusOK, reply(m.ID, nil, rerr))
		return
	}
	if !h.open(hs) {
		writeRPC(w, http.StatusServiceUnavailable, reply(m.ID, nil, &RPCError{Code: CodeOverloaded,
			Message: fmt.Sprintf("xmustard: %d MCP sessions are open; end one (DELETE) or retry after one idles out", h.opts.MaxSessions)}))
		return
	}
	w.Header().Set(HeaderSessionID, hs.id)
	writeRPC(w, http.StatusOK, reply(m.ID, result, nil))
}

// open stores a new session, first dropping the idle ones; false when the table is full.
func (h *StreamableHTTP) open(hs *httpSession) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	if len(h.sessions) >= h.opts.MaxSessions {
		return false
	}
	h.sessions[hs.id] = hs
	return true
}

func (h *StreamableHTTP) pruneLocked() {
	cutoff := time.Now().Add(-h.opts.IdleTimeout)
	for id, hs := range h.sessions {
		if hs.idleSince(cutoff) {
			delete(h.sessions, id)
			hs.cancelAll()
		}
	}
}

// lookup finds the caller's session. A missing header is 400; an unknown, expired or
// someone else's session is 404 (the client re-initializes; the reply does not say
// whether the id exists).
func (h *StreamableHTTP) lookup(r *http.Request) (*httpSession, int, string) {
	id := r.Header.Get(HeaderSessionID)
	if id == "" {
		return nil, http.StatusBadRequest, "missing " + HeaderSessionID + " header: initialize first"
	}
	owner := h.opts.Owner(r)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	hs := h.sessions[id]
	if hs == nil || subtle.ConstantTimeCompare([]byte(hs.owner), []byte(owner)) != 1 {
		return nil, http.StatusNotFound, "unknown or expired MCP session: initialize again"
	}
	hs.touch()
	return hs, 0, ""
}

// end is DELETE: the caller ends its own session and its in-flight requests.
func (h *StreamableHTTP) end(w http.ResponseWriter, r *http.Request) {
	hs, status, why := h.lookup(r)
	if hs == nil {
		writeRPC(w, status, reply(nil, nil, &RPCError{Code: CodeInvalidRequest, Message: why}))
		return
	}
	h.mu.Lock()
	delete(h.sessions, hs.id)
	h.mu.Unlock()
	hs.cancelAll()
	w.WriteHeader(http.StatusNoContent)
}

func (hs *httpSession) touch() {
	hs.mu.Lock()
	hs.lastUsed = time.Now()
	hs.mu.Unlock()
}

// idleSince reports whether the session has been unused since before cutoff with no
// request in flight.
func (hs *httpSession) idleSince(cutoff time.Time) bool {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return len(hs.inflight) == 0 && hs.lastUsed.Before(cutoff)
}

func (hs *httpSession) cancelAll() {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	for id, cancel := range hs.inflight {
		cancel()
		delete(hs.inflight, id)
	}
}

// notify handles a notification: cancellation here (the session's in-flight table),
// everything else in the Session.
func (hs *httpSession) notify(m rpcMessage) {
	if m.Method != "notifications/cancelled" {
		hs.sess.Notify(m.Method, m.Params)
		return
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(m.Params, &p) != nil || len(p.RequestID) == 0 {
		return
	}
	hs.mu.Lock()
	cancel := hs.inflight[string(p.RequestID)]
	delete(hs.inflight, string(p.RequestID))
	hs.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// wantsStream reports whether a request is answered as an event stream: the client
// accepts one, and either the call may ask the client for its roots first or the
// client does not accept plain JSON.
func wantsStream(r *http.Request, method string) bool {
	accept := r.Header.Get("Accept")
	sse := strings.Contains(accept, "text/event-stream")
	plain := accept == "" || strings.Contains(accept, "application/json") || strings.Contains(accept, "*/*")
	return sse && (method == "tools/call" || !plain)
}

func (h *StreamableHTTP) request(w http.ResponseWriter, r *http.Request, hs *httpSession, m rpcMessage) {
	select {
	case hs.slots <- struct{}{}:
		defer func() { <-hs.slots }()
	default:
		writeRPC(w, http.StatusOK, reply(m.ID, nil, &RPCError{Code: CodeOverloaded,
			Message: fmt.Sprintf("xmustard overloaded: %d requests already in flight on this session; retry shortly", cap(hs.slots))}))
		return
	}
	ctx, cancel := context.WithCancel(WithCallID(r.Context(), m.ID))
	defer cancel()
	key := string(m.ID)
	hs.mu.Lock()
	hs.inflight[key] = cancel
	hs.mu.Unlock()
	defer func() {
		hs.mu.Lock()
		delete(hs.inflight, key)
		hs.mu.Unlock()
	}()
	if !wantsStream(r, m.Method) {
		result, rerr := hs.sess.Handle(ctx, m.Method, m.Params)
		writeRPC(w, http.StatusOK, reply(m.ID, result, rerr))
		return
	}
	stream := newEventStream(w)
	result, rerr := hs.sess.Handle(withStream(ctx, stream), m.Method, m.Params)
	if ctx.Err() != nil {
		return // cancelled by the client, or it went away: no response is owed
	}
	stream.send(reply(m.ID, result, rerr))
}

// eventStream writes JSON-RPC messages as server-sent events on one POST's response.
type eventStream struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func newEventStream(w http.ResponseWriter) *eventStream {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	s := &eventStream{w: w, f: f}
	s.flush()
	return s
}

func (s *eventStream) flush() {
	if s.f != nil {
		s.f.Flush()
	}
}

func (s *eventStream) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, "event: message\ndata: %s\n\n", b); err != nil {
		return err
	}
	s.flush()
	return nil
}

type streamKey struct{}

func withStream(ctx context.Context, s *eventStream) context.Context {
	return context.WithValue(ctx, streamKey{}, s)
}

// httpClient sends server-to-client requests (roots/list) on the event stream of the
// request being served and matches the client's answers, POSTed separately, back to
// the waiting call. With no stream (a plain JSON exchange) there is no way to ask.
type httpClient struct {
	next    atomic.Int64
	mu      sync.Mutex
	pending map[string]chan rpcMessage
}

func newHTTPClient() *httpClient { return &httpClient{pending: map[string]chan rpcMessage{}} }

var errNoStream = errors.New("no event stream open on this request to ask the client")

func (c *httpClient) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	stream, _ := ctx.Value(streamKey{}).(*eventStream)
	if stream == nil {
		return nil, errNoStream
	}
	id := "xmustard-" + strconv.FormatInt(c.next.Add(1), 10)
	ch := make(chan rpcMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := stream.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("client error %d: %w", m.Error.Code, m.Error)
		}
		return m.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// deliver routes a client response to the call waiting for it; unknown ids (answers
// after a timeout) are dropped.
func (c *httpClient) deliver(m rpcMessage) {
	var id string
	if json.Unmarshal(m.ID, &id) != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	c.mu.Unlock()
	if ch != nil {
		select {
		case ch <- m:
		default:
		}
	}
}

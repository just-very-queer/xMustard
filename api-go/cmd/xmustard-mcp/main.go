// xmustard-mcp is an MCP (Model Context Protocol) stdio server that exposes
// xMustard's repo intelligence + runtime memory to agents as typed tools — the
// "context engine" surface. It bridges newline-delimited JSON-RPC 2.0 over
// stdin/stdout to the running xMustard HTTP API (XMUSTARD_API_BASE, default
// http://127.0.0.1:8042), so agents share durable state across agents/modules
// instead of markdown, and a reconnecting agent re-checks current state.
//
// This file is the stdio transport only: framing, admission, in-flight
// cancellation and server-to-client requests. The tool table, validation,
// protocol negotiation and result shaping live in internal/mcpserver; evidence
// delivery and resources in evidence.go.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/mcpserver"
)

type rpcError = mcpserver.RPCError

// overloadCode is the JSON-RPC server-error code used for admission refusal.
const overloadCode = mcpserver.CodeOverloaded

func overloadError(err error) *rpcError { return mcpserver.OverloadError(err) }

func mcpText(text string, isError bool) map[string]any { return mcpserver.TextResult(text, isError) }

// One stdio process is one MCP session.
var (
	backend = &mcpserver.HTTPBackend{}
	client  = newStdioClient()
	session = mcpserver.New(mcpserver.Options{
		Backend:      backend,
		Delivery:     evidenceDelivery{},
		Resources:    evidenceResources{},
		Cwd:          workingDir(),
		AutoRegister: strings.TrimSpace(os.Getenv("XMUSTARD_MCP_AUTO_REGISTER")) != "0",
		HomeDir:      homeDir(),
	}).NewSession(client)
)

func workingDir() string {
	if d, err := os.Getwd(); err == nil {
		return d
	}
	return ""
}

func homeDir() string {
	if d, err := os.UserHomeDir(); err == nil {
		return d
	}
	return ""
}

// dispatch handles one JSON-RPC method, returning a result.
func dispatch(method string, params json.RawMessage) (any, *rpcError) {
	return dispatchCtx(context.Background(), method, params)
}

func dispatchCtx(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	return session.Handle(ctx, method, params)
}

// --- JSON-RPC ---

// rpcMessage is any incoming frame: a request, a notification, or the client's
// response to a server-to-client request (roots/list).
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// isResponse reports whether the frame answers a request this server sent.
func (m *rpcMessage) isResponse() bool {
	return m.Method == "" && len(m.ID) > 0 && (m.Result != nil || m.Error != nil)
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// stdioClient sends server-to-client requests over stdout and matches the client's
// responses, read by the main loop, back to the waiting caller.
type stdioClient struct {
	mu      sync.Mutex
	send    func(any) // set once the transport runs; nil means no client to ask
	next    atomic.Int64
	pending map[string]chan rpcMessage
}

func newStdioClient() *stdioClient { return &stdioClient{pending: map[string]chan rpcMessage{}} }

func (c *stdioClient) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	send := c.send
	c.mu.Unlock()
	if send == nil {
		return nil, errors.New("no client connection")
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
	send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("client error %d: %s", m.Error.Code, m.Error.Message)
		}
		return m.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// deliver routes a client response to the request waiting for it; unknown ids
// (answers after a timeout) are dropped, never answered.
func (c *stdioClient) deliver(m rpcMessage) {
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

// maxInflight bounds concurrently outstanding id-bearing requests; beyond it the shim
// answers immediately with an overload error instead of starting another worker.
func maxInflight() int {
	if v := strings.TrimSpace(os.Getenv("XMUSTARD_MCP_MAX_INFLIGHT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 8
}

// maxMessageBytes bounds a single newline-delimited JSON-RPC message. Without it,
// ReadBytes('\n') accumulates a newline-less stream unboundedly and OOM-kills the
// server (XM-NEW-020). 8 MiB comfortably fits any legitimate tool call.
const maxMessageBytes = 8 << 20

// readBoundedLine reads one '\n'-terminated message, capped at maxMessageBytes. If
// the line exceeds the cap it is drained to the newline and reported truncated, so a
// hostile huge frame stays bounded instead of allocating without limit.
func readBoundedLine(r *bufio.Reader) (line []byte, truncated bool, err error) {
	f := readAdmittedLine(r, nil)
	return f.line, f.truncated, f.err
}

// frame is one newline-delimited request as read under admission.
type frame struct {
	line      []byte
	probe     []byte // first idProbeBytes of the frame (fixed, for recovering the id)
	truncated bool   // exceeded maxMessageBytes; drained, not buffered
	refused   bool   // the transient pool refused it; drained, not buffered
	headroom  bool   // pool refused, but it fit the fixed control headroom (see below)
	err       error
}

const idProbeBytes = 1024

// controlHeadroom lets a small frame through when the pool is saturated, so a client
// can still cancel (notifications/cancelled), ping, or answer a server request while
// calls hold the pool. Frames are read one at a time, so this is a fixed 4 KiB, not a
// second pool; only notifications, ping and responses are serviced from it — anything
// else is refused with its id.
const controlHeadroom = 4 << 10

// readAdmittedLine reads one frame, reserving each chunk in scope BEFORE appending it.
// A frame over maxMessageBytes, or one the pool refuses, is drained to its newline
// without being buffered. scope nil means unadmitted (tests of the framing alone).
func readAdmittedLine(r *bufio.Reader, scope *budget.Scope) frame {
	var f frame
	for {
		chunk, e := r.ReadSlice('\n')
		if len(chunk) > 0 {
			if room := idProbeBytes - len(f.probe); room > 0 {
				f.probe = append(f.probe, chunk[:min(room, len(chunk))]...)
			}
			switch {
			case f.truncated || f.refused:
			case len(f.line)+len(chunk) > maxMessageBytes:
				f.truncated, f.line = true, nil
			case f.headroom || (scope != nil && scope.Acquire(int64(len(chunk))) != nil):
				if len(f.line)+len(chunk) <= controlHeadroom {
					f.headroom = true
					f.line = append(f.line, chunk...)
				} else {
					f.refused, f.headroom, f.line = true, false, nil
				}
			default:
				f.line = append(f.line, chunk...)
			}
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		f.err = e
		return f
	}
}

// probeID recovers the TOP-LEVEL JSON-RPC id from the bounded prefix of a frame that
// was not decoded, by parsing the prefix's top-level members in order. If the id is
// not reached and fully parsed inside the prefix, it returns nil (a null-id error):
// guessing could correlate the error with a different live call.
func probeID(probe []byte) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(probe))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil // value runs past the prefix (or is malformed)
		}
		if key == "id" {
			var s string
			var n json.Number
			if json.Unmarshal(val, &s) == nil || json.Unmarshal(val, &n) == nil {
				return val
			}
			return nil
		}
	}
	return nil
}

// inflight tracks cancel funcs for in-progress requests by their JSON-RPC id, so an MCP
// `notifications/cancelled` can abort the matching tool call mid-flight. Bounded by the
// number of concurrently outstanding requests one well-behaved client has open.
type inflightRegistry struct {
	mu sync.Mutex
	m  map[string]context.CancelFunc
}

func newInflight() *inflightRegistry { return &inflightRegistry{m: map[string]context.CancelFunc{}} }

func (r *inflightRegistry) add(id string, cancel context.CancelFunc) {
	r.mu.Lock()
	r.m[id] = cancel
	r.mu.Unlock()
}
func (r *inflightRegistry) done(id string) {
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

// cancel aborts the in-flight request with this id (no-op if already finished/unknown).
func (r *inflightRegistry) cancel(id string) {
	r.mu.Lock()
	cancel := r.m[id]
	delete(r.m, id)
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancelledRequestID extracts the target request id from a notifications/cancelled
// params object: {"requestId": <id>, "reason": "..."}. The id is matched by its raw JSON
// encoding so numeric and string ids both round-trip exactly.
func cancelledRequestID(params json.RawMessage) (string, bool) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.RequestID) == 0 {
		return "", false
	}
	return string(p.RequestID), true
}

// notify handles a notification: cancellation here (this loop owns the in-flight
// registry), everything else in the session.
func notify(inflight *inflightRegistry, m rpcMessage) {
	if m.Method == "notifications/cancelled" {
		if id, ok := cancelledRequestID(m.Params); ok {
			inflight.cancel(id)
		}
		return
	}
	session.Notify(m.Method, m.Params)
}

func main() {
	reader := bufio.NewReaderSize(os.Stdin, 64<<10)
	writer := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(writer)
	// stdout is shared by the read loop, the per-request worker goroutines and
	// server-to-client requests, so every write is serialized.
	var sendMu sync.Mutex
	write := func(v any) {
		sendMu.Lock()
		_ = enc.Encode(v)
		_ = writer.Flush()
		sendMu.Unlock()
	}
	send := func(resp rpcResponse) { write(resp) }
	client.mu.Lock()
	client.send = write
	client.mu.Unlock()
	inflight := newInflight()
	slots := make(chan struct{}, maxInflight())
	var workers sync.WaitGroup
	defer workers.Wait()

	for {
		// Every frame is admitted against the shim's transient pool before it is
		// buffered, again before it is decoded (the decoded params are a second copy),
		// and the reservation is held by the worker until its reply is written.
		scope := budget.NewScope(nil)
		f := readAdmittedLine(reader, scope)
		err := f.err
		line := bytes.TrimSpace(f.line)
		if f.truncated {
			scope.Close()
			// can't trust the (partial) body to parse an id; reply with a null-id error.
			send(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: mcpserver.CodeInvalidRequest, Message: "request exceeds max message size"}})
		} else if f.headroom {
			// served from the fixed control headroom: only control frames proceed
			scope.Close()
			var m rpcMessage
			if json.Unmarshal(line, &m) != nil {
				send(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: mcpserver.CodeParseError, Message: "parse error"}})
			} else if m.isResponse() {
				client.deliver(m)
			} else if len(m.ID) == 0 && strings.HasPrefix(m.Method, "notifications/") {
				notify(inflight, m)
			} else if m.Method == "ping" {
				send(rpcResponse{JSONRPC: "2.0", ID: m.ID, Result: map[string]any{}})
			} else {
				send(rpcResponse{JSONRPC: "2.0", ID: m.ID, Error: overloadError(budget.ErrOverloaded)})
			}
		} else if f.refused || (len(line) > 0 && scope.Acquire(int64(len(line))) != nil) {
			scope.Close()
			send(rpcResponse{JSONRPC: "2.0", ID: probeID(f.probe), Error: overloadError(budget.ErrOverloaded)})
		} else if len(line) == 0 {
			scope.Close()
		} else {
			var m rpcMessage
			jsonErr := json.Unmarshal(line, &m)
			f.line, line = nil, nil // the raw frame is no longer referenced
			if jsonErr != nil {
				scope.Close()
				// malformed JSON → structured parse error rather than a silent drop.
				send(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: mcpserver.CodeParseError, Message: "parse error"}})
			} else if m.isResponse() {
				scope.Close()
				// the client answering a server request (roots/list): never answered back
				client.deliver(m)
			} else if len(m.ID) == 0 && strings.HasPrefix(m.Method, "notifications/") {
				scope.Close()
				// notifications have no id and expect no response. A cancellation aborts
				// the matching in-flight request so the loop stays responsive to it.
				notify(inflight, m)
			} else if m.Method == "initialize" {
				// Negotiation is answered before the next frame is read, so requests the
				// client pipelines behind it see the negotiated version. It does no I/O.
				result, rerr := dispatchCtx(budget.WithScope(context.Background(), scope), m.Method, m.Params)
				scope.Close()
				resp := rpcResponse{JSONRPC: "2.0", ID: m.ID, Result: result}
				if rerr != nil {
					resp.Result, resp.Error = nil, rerr
				}
				send(resp)
			} else {
				// Run each id-bearing request on its own goroutine with a cancelable
				// context registered by id, so the read loop keeps reading (and can
				// service a cancellation or a roots/list answer) while the tool call is
				// outstanding. JSON-RPC permits out-of-order responses; the client
				// matches by id.
				select {
				case slots <- struct{}{}:
				default:
					scope.Close()
					send(rpcResponse{JSONRPC: "2.0", ID: m.ID, Error: &rpcError{Code: overloadCode,
						Message: fmt.Sprintf("xmustard overloaded: %d requests already in flight; retry shortly", cap(slots))}})
					continue // a final frame at EOF is followed by an empty EOF read
				}
				// The worker owns the frame's ledger (ingress, decode, argument and
				// response bytes) until its reply is sent.
				ctx, cancel := context.WithCancel(withCallID(budget.WithScope(context.Background(), scope), m.ID))
				idKey := string(m.ID)
				inflight.add(idKey, cancel)
				workers.Add(1)
				go func(m rpcMessage) {
					defer workers.Done()
					defer func() { <-slots }()
					defer scope.Close()
					defer inflight.done(idKey)
					defer cancel()
					result, rerr := dispatchCtx(ctx, m.Method, m.Params)
					resp := rpcResponse{JSONRPC: "2.0", ID: m.ID}
					if rerr != nil {
						resp.Error = rerr
					} else {
						resp.Result = result
					}
					send(resp)
				}(m)
			}
		}
		if err != nil { // io.EOF or a read error: stop
			break
		}
	}
}

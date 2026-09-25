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

// tooLargeCode answers a request this shim can never admit: past maxMessageBytes, or
// holding more than its whole transient pool. Unlike overloadCode it is permanent, so a
// client does not retry it.
const tooLargeCode = mcpserver.CodeTooLarge

func admissionError(err error) *rpcError { return mcpserver.AdmissionError(err) }

func replyRefused(err error) (map[string]any, *rpcError) { return mcpserver.ReplyRefused(err) }

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
	// dropped, set by the transport (never decoded), fails a server request whose
	// answer arrived but could not be read (over the size cap or refused admission).
	dropped error
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
		if m.dropped != nil {
			return nil, m.dropped
		}
		if m.Error != nil {
			// wrapped, so the session can tell method-not-found from a failure
			return nil, fmt.Errorf("client error %d: %w", m.Error.Code, m.Error)
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

func (c *stdioClient) isPending(id json.RawMessage) bool {
	var key string
	if json.Unmarshal(id, &key) != nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending[key] != nil
}

// answerUndecoded returns the reply owed to a frame that was not decoded (over the
// size cap or the whole pool, or refused admission), or nil when none is owed. A
// client's response to a server request (roots/list) is never answered: it is dropped,
// and the request waiting for it fails now instead of timing out. A request is answered
// with the id a top-level parse of the bounded prefix recovers, else a null id.
func (c *stdioClient) answerUndecoded(probe []byte, rerr *rpcError) *rpcResponse {
	p := probeFrame(probe)
	if !p.method && (p.answer || (p.id != nil && c.isPending(p.id))) {
		if p.id != nil {
			c.deliver(rpcMessage{ID: p.id, dropped: fmt.Errorf("the client's answer was dropped unread: %s", rerr.Message)})
		}
		return nil
	}
	return &rpcResponse{JSONRPC: "2.0", ID: p.id, Error: rerr}
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
	neverFits bool   // larger than the whole pool: its refusal is permanent
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
// A frame over maxMessageBytes or larger than the whole pool (it can never be admitted:
// truncated, answered permanently), or one the pool refuses for now (refused, answered
// with the retryable overload), is drained to its newline without being buffered.
// scope nil means unadmitted (tests of the framing alone).
func readAdmittedLine(r *bufio.Reader, scope *budget.Scope) frame {
	var f frame
	for {
		chunk, e := r.ReadSlice('\n')
		if len(chunk) > 0 {
			if room := idProbeBytes - len(f.probe); room > 0 {
				f.probe = append(f.probe, chunk[:min(room, len(chunk))]...)
			}
			var aerr error
			if !f.truncated && !f.refused && !f.headroom && scope != nil && len(f.line)+len(chunk) <= maxMessageBytes {
				aerr = scope.Acquire(int64(len(chunk)))
				f.neverFits = errors.Is(aerr, budget.ErrNeverFits)
			}
			switch {
			case f.truncated || f.refused:
			case len(f.line)+len(chunk) > maxMessageBytes:
				f.truncated, f.headroom, f.line = true, false, nil
			case f.headroom || aerr != nil:
				switch {
				case len(f.line)+len(chunk) <= controlHeadroom:
					f.headroom = true
					f.line = append(f.line, chunk...)
				case f.neverFits:
					f.truncated, f.headroom, f.line = true, false, nil
				default:
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

// admitDecode reserves the decoded copy of an admitted frame before it is unmarshalled.
// A frame the pool refused while it was read gets the retryable overload.
func admitDecode(scope *budget.Scope, refused bool, line []byte) *rpcError {
	if refused {
		return overloadError(budget.ErrOverloaded)
	}
	if len(line) > 0 {
		if err := scope.Acquire(int64(len(line))); err != nil {
			return admissionError(err)
		}
	}
	return nil
}

// probeID recovers the TOP-LEVEL JSON-RPC id from the bounded prefix of a frame that
// was not decoded, by parsing the prefix's top-level members in order. If the id is
// not reached and fully parsed inside the prefix, it returns nil (a null-id error):
// guessing could correlate the error with a different live call.
func probeID(probe []byte) json.RawMessage { return probeFrame(probe).id }

// probed is what the bounded prefix of an undecoded frame shows.
type probed struct {
	id     json.RawMessage // the top-level id, when fully inside the prefix and scalar
	method bool            // a top-level method member was seen: a request or notification
	answer bool            // a top-level result or error member was seen: a response
}

// probeFrame reads the prefix's top-level members in order. A member's key counts even
// when its value runs past the prefix (a large roots/list result).
func probeFrame(probe []byte) probed {
	var p probed
	dec := json.NewDecoder(bytes.NewReader(probe))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return p
	}
	seenID := false
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			break
		}
		switch key {
		case "method":
			p.method = true
		case "result", "error":
			p.answer = true
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			break // value runs past the prefix (or is malformed)
		}
		if key == "id" && !seenID {
			seenID = true
			var s string
			var n json.Number
			if json.Unmarshal(val, &s) == nil || json.Unmarshal(val, &n) == nil {
				p.id = val
			}
		}
	}
	return p
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
		rawBytes := int64(len(f.line)) // reserved while reading
		line := bytes.TrimSpace(f.line)
		if f.truncated {
			scope.Close()
			// permanent (past the size cap or the whole pool): a request is answered with
			// the id from a top-level parse of the bounded prefix, else null; a response to
			// a server request is dropped, never answered.
			if resp := client.answerUndecoded(f.probe, &rpcError{Code: tooLargeCode, Message: "request exceeds max message size"}); resp != nil {
				send(*resp)
			}
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
			} else if f.neverFits {
				send(rpcResponse{JSONRPC: "2.0", ID: m.ID, Error: admissionError(budget.ErrNeverFits)})
			} else {
				send(rpcResponse{JSONRPC: "2.0", ID: m.ID, Error: overloadError(budget.ErrOverloaded)})
			}
		} else if aerr := admitDecode(scope, f.refused, line); aerr != nil {
			scope.Close()
			if resp := client.answerUndecoded(f.probe, aerr); resp != nil {
				send(*resp)
			}
		} else if len(line) == 0 {
			scope.Close()
		} else {
			var m rpcMessage
			jsonErr := json.Unmarshal(line, &m)
			f.line, line = nil, nil // the raw frame is no longer referenced
			scope.Release(rawBytes) // ...so its reservation ends; the decoded copy stays held
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

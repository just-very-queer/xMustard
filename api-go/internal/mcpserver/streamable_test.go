package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// httpPeer is a minimal Streamable HTTP client: it POSTs one message and returns the
// JSON-RPC messages of the answer (one JSON body, or every event of a stream).
type httpPeer struct {
	t      *testing.T
	url    string
	owner  string
	sid    string
	accept string
	// onEvent answers server requests that arrive on a stream (roots/list).
	onEvent func(m map[string]any)
}

func (p *httpPeer) post(msg map[string]any) (int, []map[string]any) {
	p.t.Helper()
	b, _ := json.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, p.url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", p.accept)
	req.Header.Set("X-Owner", p.owner)
	if p.sid != "" {
		req.Header.Set(HeaderSessionID, p.sid)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	if id := resp.Header.Get(HeaderSessionID); id != "" {
		p.sid = id
	}
	var out []map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(data), &m); err != nil {
				p.t.Fatalf("bad event %q: %v", data, err)
			}
			if _, isReq := m["method"]; isReq && p.onEvent != nil {
				p.onEvent(m)
			}
			out = append(out, m)
		}
		return resp.StatusCode, out
	}
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		out = append(out, m)
	}
	return resp.StatusCode, out
}

func (p *httpPeer) initialize(caps map[string]any) {
	p.t.Helper()
	code, msgs := p.post(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": LatestProtocolVersion, "capabilities": caps, "clientInfo": map[string]any{"name": "t", "version": "1"}}})
	if code != http.StatusOK || p.sid == "" || len(msgs) != 1 || msgs[0]["result"] == nil {
		p.t.Fatalf("initialize: %d sid=%q %v", code, p.sid, msgs)
	}
	if code, _ := p.post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); code != http.StatusAccepted {
		p.t.Fatalf("initialized: %d", code)
	}
}

func newStreamableServer(t *testing.T, api Backend, opts Options) *httptest.Server {
	t.Helper()
	h := NewStreamableHTTP(HTTPOptions{
		Open: func(r *http.Request) (*Server, error) {
			o := opts
			o.Backend = api
			ev := NewEvidence(api, nil) // per session, as the API endpoint builds it
			o.Delivery, o.Resources = ev, ev
			if o.Getenv == nil {
				o.Getenv = noEnv
			}
			return New(o), nil
		},
		Owner:       func(r *http.Request) string { return r.Header.Get("X-Owner") },
		MaxSessions: 4,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestStreamableHTTPProtocol(t *testing.T) {
	api := &fakeAPI{}
	srv := newStreamableServer(t, api, Options{})
	p := &httpPeer{t: t, url: srv.URL, owner: "alice", accept: "application/json, text/event-stream"}

	// no session yet: only initialize is accepted
	if code, _ := p.post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}); code != http.StatusBadRequest {
		t.Fatalf("request without a session: %d", code)
	}
	p.initialize(map[string]any{})
	code, msgs := p.post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if code != http.StatusOK || len(msgs[0]["result"].(map[string]any)["tools"].([]any)) != 9 {
		t.Fatalf("tools/list: %d %v", code, msgs)
	}
	// tools/call answers on a stream when the client accepts one
	code, msgs = p.post(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "search", "arguments": map[string]any{"workspace_id": "ws", "query": "q"}}})
	if code != http.StatusOK || len(msgs) != 1 || msgs[0]["id"] != float64(3) || msgs[0]["result"].(map[string]any)["isError"] != false {
		t.Fatalf("tools/call: %d %v", code, msgs)
	}

	// another caller cannot use the session; a batch, an unknown protocol version and
	// GET are refused
	bob := &httpPeer{t: t, url: srv.URL, owner: "bob", sid: p.sid, accept: "application/json"}
	if code, _ := bob.post(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/list"}); code != http.StatusNotFound {
		t.Fatalf("foreign session: %d", code)
	}
	for _, c := range []struct {
		method, body, version string
		want                  int
	}{
		{http.MethodPost, `[{"jsonrpc":"2.0","id":5,"method":"ping"}]`, "", http.StatusBadRequest},
		{http.MethodPost, `{"jsonrpc":"2.0","id":5,"method":"ping"}`, "1999-01-01", http.StatusBadRequest},
		{http.MethodPost, `{not json`, "", http.StatusBadRequest},
		{http.MethodGet, ``, "", http.StatusMethodNotAllowed},
	} {
		req, _ := http.NewRequest(c.method, srv.URL, strings.NewReader(c.body))
		req.Header.Set("X-Owner", "alice")
		req.Header.Set(HeaderSessionID, p.sid)
		if c.version != "" {
			req.Header.Set(HeaderProtocolVersion, c.version)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Fatalf("%s %s: want %d, got %d", c.method, c.body, c.want, resp.StatusCode)
		}
	}
	// DELETE ends the session
	req, _ := http.NewRequest(http.MethodDelete, srv.URL, nil)
	req.Header.Set("X-Owner", "alice")
	req.Header.Set(HeaderSessionID, p.sid)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %v %v", resp, err)
	}
	if code, _ := p.post(map[string]any{"jsonrpc": "2.0", "id": 6, "method": "ping"}); code != http.StatusNotFound {
		t.Fatalf("ended session still answers: %d", code)
	}
}

// roots/list conformance over HTTP: a client that declares roots and accepts a stream
// is asked for its roots on the tools/call stream, answers with a separate POST, and
// the call resolves its workspace from that root. A client that accepts only JSON
// cannot be asked, and a session binding needs no roots at all.
func TestStreamableHTTPRootsListConformance(t *testing.T) {
	root := t.TempDir()
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "from-roots", Root: root}}}
	srv := newStreamableServer(t, api, Options{})
	p := &httpPeer{t: t, url: srv.URL, owner: "alice", accept: "application/json, text/event-stream"}
	var asked int
	p.onEvent = func(m map[string]any) {
		if m["method"] != "roots/list" {
			t.Errorf("unexpected server request %v", m)
			return
		}
		asked++
		answer := &httpPeer{t: t, url: srv.URL, owner: "alice", sid: p.sid, accept: "application/json"}
		code, _ := answer.post(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{
			"roots": []map[string]any{{"uri": (&url.URL{Scheme: "file", Path: root}).String(), "name": "r"}}}})
		if code != http.StatusAccepted {
			t.Errorf("roots answer: %d", code)
		}
	}
	p.initialize(map[string]any{"roots": map[string]any{"listChanged": true}})
	_, msgs := p.post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "search", "arguments": map[string]any{"query": "q"}}})
	last := msgs[len(msgs)-1]
	ws := last["result"].(map[string]any)["_meta"].(map[string]any)["xmustard/workspace"].(map[string]any)
	if asked != 1 || ws["workspace_id"] != "from-roots" || ws["source"] != SourceRoots {
		t.Fatalf("roots not used: asked=%d %v", asked, ws)
	}

	// JSON only: roots cannot be requested, the call says so instead of hanging
	j := &httpPeer{t: t, url: srv.URL, owner: "carol", accept: "application/json"}
	j.initialize(map[string]any{"roots": map[string]any{}})
	start := time.Now()
	_, msgs = j.post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "search", "arguments": map[string]any{"query": "q"}}})
	if time.Since(start) > 3*time.Second || !strings.Contains(firstText(msgs[0]), "roots/list failed") {
		t.Fatalf("JSON-only roots: %v", msgs)
	}

	// a bound session resolves without asking
	bound := newStreamableServer(t, api, Options{WorkspaceID: "bound"})
	b := &httpPeer{t: t, url: bound.URL, owner: "dave", accept: "application/json, text/event-stream"}
	b.onEvent = func(m map[string]any) { t.Errorf("bound session asked the client: %v", m) }
	b.initialize(map[string]any{"roots": map[string]any{}})
	_, msgs = b.post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "search", "arguments": map[string]any{"query": "q"}}})
	ws = msgs[0]["result"].(map[string]any)["_meta"].(map[string]any)["xmustard/workspace"].(map[string]any)
	if ws["workspace_id"] != "bound" || ws["source"] != SourceBinding {
		t.Fatalf("binding not used: %v", ws)
	}
}

func firstText(m map[string]any) string {
	res, _ := m["result"].(map[string]any)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	s, _ := content[0].(map[string]any)["text"].(string)
	return s
}

// blockingAPI holds tool calls until their context ends.
type blockingAPI struct {
	fakeAPI
	started chan struct{}
	ended   chan error
}

func (b *blockingAPI) Do(ctx context.Context, r Request) (*APIResponse, error) {
	if !strings.HasSuffix(strings.SplitN(r.Path, "?", 2)[0], "/search") {
		return b.fakeAPI.Do(ctx, r)
	}
	b.started <- struct{}{}
	<-ctx.Done()
	b.ended <- ctx.Err()
	return nil, ctx.Err()
}

// notifications/cancelled aborts the named in-flight call: its API request's context
// ends, and the stream closes with no response.
func TestStreamableHTTPCancellation(t *testing.T) {
	api := &blockingAPI{started: make(chan struct{}, 1), ended: make(chan error, 1)}
	srv := newStreamableServer(t, api, Options{})
	p := &httpPeer{t: t, url: srv.URL, owner: "alice", accept: "application/json, text/event-stream"}
	p.initialize(map[string]any{})
	var wg sync.WaitGroup
	var msgs []map[string]any
	wg.Add(1)
	go func() {
		defer wg.Done()
		call := *p
		_, msgs = call.post(map[string]any{"jsonrpc": "2.0", "id": "c1", "method": "tools/call",
			"params": map[string]any{"name": "search", "arguments": map[string]any{"workspace_id": "ws", "query": "q"}}})
	}()
	<-api.started
	if code, _ := p.post(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": "c1", "reason": "user"}}); code != http.StatusAccepted {
		t.Fatalf("cancel: %d", code)
	}
	select {
	case err := <-api.ended:
		if err != context.Canceled {
			t.Fatalf("API call ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not reach the API call")
	}
	wg.Wait()
	if len(msgs) != 0 {
		t.Fatalf("a cancelled call was answered: %v", msgs)
	}
}

// Evidence resources are served per session over HTTP: a reduced result names its
// handle, resources/list shows it, and resources/read pages the original.
func TestStreamableHTTPEvidenceResources(t *testing.T) {
	api := &fakeAPI{handle: func(r Request) *APIResponse {
		path := strings.SplitN(r.Path, "?", 2)[0]
		switch {
		case strings.HasSuffix(path, "/search") && r.Headers[DeliveryHeader] == DeliveryVersion:
			body, _ := json.Marshal(map[string]any{"tool": "search", "status": 200, "reduced": true, "projection": `{"hits":[]}`,
				"handle": "xm1.H", "resource_uri": ResourceScheme + "xm1.H", "raw_bytes": 300000, "projected_bytes": 11})
			return &APIResponse{Status: 200, Header: http.Header{DeliveryHeader: {DeliveryVersion}}, Body: string(body)}
		case strings.HasSuffix(path, "/evidence/xm1.H"):
			q, _ := url.ParseQuery(strings.SplitN(r.Path, "?", 2)[1])
			return &APIResponse{Status: 200, Body: `{"handle":"xm1.H","offset":` + q.Get("offset") + `,"length":3,"eof":false,"data":"YWJj"}`}
		}
		return nil
	}}
	srv := newStreamableServer(t, api, Options{})
	p := &httpPeer{t: t, url: srv.URL, owner: "alice", accept: "application/json"}
	p.initialize(map[string]any{})
	_, msgs := p.post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "search", "arguments": map[string]any{"workspace_id": "ws", "query": "q"}}})
	if !strings.Contains(firstText(msgs[0]), `{"hits":[]}`) {
		t.Fatalf("projection not delivered: %v", msgs)
	}
	_, msgs = p.post(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "resources/list"})
	if !strings.Contains(mustJSON(msgs), ResourceScheme+"xm1.H") {
		t.Fatalf("handle not listed: %v", msgs)
	}
	_, msgs = p.post(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "resources/read", "params": map[string]any{"uri": ResourceScheme + "xm1.H?offset=65536&length=3"}})
	res := msgs[0]["result"].(map[string]any)
	if res["contents"].([]any)[0].(map[string]any)["blob"] != "YWJj" || res["_meta"].(map[string]any)["xmustard/page"].(map[string]any)["offset"] != float64(65536) {
		t.Fatalf("page: %v", msgs)
	}
	// another session did not receive the handle and cannot address it by bare URI
	q := &httpPeer{t: t, url: srv.URL, owner: "alice", accept: "application/json"}
	q.initialize(map[string]any{})
	_, msgs = q.post(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "resources/read", "params": map[string]any{"uri": ResourceScheme + "xm1.H"}})
	if e, _ := msgs[0]["error"].(map[string]any); e == nil || e["code"] != float64(CodeResourceNotFound) {
		t.Fatalf("foreign bare handle: %v", msgs)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The session table is bounded: past MaxSessions a new initialize is refused.
func TestStreamableHTTPSessionCap(t *testing.T) {
	srv := newStreamableServer(t, &fakeAPI{}, Options{})
	for i := 0; i < 4; i++ {
		(&httpPeer{t: t, url: srv.URL, owner: "a", accept: "application/json"}).initialize(map[string]any{})
	}
	p := &httpPeer{t: t, url: srv.URL, owner: "a", accept: "application/json"}
	code, _ := p.post(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": LatestProtocolVersion}})
	if code != http.StatusServiceUnavailable || p.sid != "" {
		t.Fatalf("fifth session: %d %q", code, p.sid)
	}
}

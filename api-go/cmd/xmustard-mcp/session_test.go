package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
)

// shimProc runs the built shim with a line reader over its stdout.
type shimProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan map[string]any
}

func startShim(t *testing.T, dir string, env ...string) *shimProc {
	t.Helper()
	cmd := exec.Command(buildShim(t))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &shimProc{cmd: cmd, stdin: stdin, lines: make(chan map[string]any, 32)}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				p.lines <- m
			}
		}
		close(p.lines)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return p
}

func (p *shimProc) send(t *testing.T, format string, a ...any) {
	t.Helper()
	if _, err := fmt.Fprintf(p.stdin, format+"\n", a...); err != nil {
		t.Fatal(err)
	}
}

func (p *shimProc) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case m, ok := <-p.lines:
		if !ok {
			t.Fatal("shim exited")
		}
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no message from the shim within 10s")
	}
	return nil
}

// A client that declares roots is asked roots/list over stdio; the shim routes the
// answer back to the waiting tool call (never answering the response itself) and
// resolves the workspace from it.
func TestShimResolvesWorkspaceFromClientRoots(t *testing.T) {
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	var mu sync.Mutex
	var paths []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/api/workspaces" {
			_ = json.NewEncoder(w).Encode([]map[string]string{{"workspace_id": "from-roots", "root_path": repo}})
			return
		}
		_, _ = w.Write([]byte(`{"workspace_id":"from-roots","summary":"ok"}`))
	}))
	defer api.Close()
	p := startShim(t, "/", "XMUSTARD_API_BASE="+api.URL, "XMUSTARD_WORKSPACE_ID=")
	p.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"roots":{"listChanged":true}},"clientInfo":{"name":"t","version":"1"}}}`)
	if init := p.next(t); init["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %v", init)
	}
	p.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	p.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ground","arguments":{}}}`)
	req := p.next(t)
	if req["method"] != "roots/list" || req["id"] == nil {
		t.Fatalf("expected a roots/list request, got %v", req)
	}
	id, _ := json.Marshal(req["id"])
	p.send(t, `{"jsonrpc":"2.0","id":%s,"result":{"roots":[{"uri":"file://%s","name":"repo"}]}}`, id, repo)
	res := p.next(t)
	if res["id"] != float64(2) || res["error"] != nil {
		t.Fatalf("the roots answer must not be answered, and the call must succeed: %v", res)
	}
	result := res["result"].(map[string]any)
	ws := result["_meta"].(map[string]any)["xmustard/workspace"].(map[string]any)
	if ws["workspace_id"] != "from-roots" || ws["source"] != "roots" || result["structuredContent"] == nil {
		t.Fatalf("workspace not resolved from roots: %v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if paths[len(paths)-1] != "/api/workspaces/from-roots/session-grounding" {
		t.Fatalf("tool call went to %v", paths)
	}
}

// initialize is answered before the next frame is read, so a client that pipelines
// tools/list behind it gets the list for the negotiated (legacy) version.
func TestShimNegotiatesBeforePipelinedRequests(t *testing.T) {
	p := startShim(t, "/", "XMUSTARD_API_BASE=http://127.0.0.1:9")
	p.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	p.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	p.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	// a stray response to nothing is dropped, never answered
	p.send(t, `{"jsonrpc":"2.0","id":"xmustard-999","result":{}}`)
	p.send(t, `{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	got := map[string]map[string]any{}
	for len(got) < 3 {
		m := p.next(t)
		key, _ := json.Marshal(m["id"])
		got[string(key)] = m
	}
	if got["1"]["result"].(map[string]any)["protocolVersion"] != "2024-11-05" {
		t.Fatalf("initialize: %v", got["1"])
	}
	for _, tl := range got["2"]["result"].(map[string]any)["tools"].([]any) {
		if _, ok := tl.(map[string]any)["annotations"]; ok {
			t.Fatal("tools/list raced ahead of initialize: annotations sent to a 2024-11-05 client")
		}
	}
	if _, ok := got["3"]; !ok || strings.Contains(fmt.Sprint(got), "xmustard-999") {
		t.Fatalf("the stray response was answered: %v", got)
	}
	select {
	case m := <-p.lines:
		t.Fatalf("unexpected extra message (a response must never be answered): %v", m)
	case <-time.After(300 * time.Millisecond):
	}
}

// A client's answer to a server request that is not decoded (over the size cap, or
// refused admission) is still a response: it is never answered back, and the request
// waiting for it fails at once instead of timing out. Requests keep their error reply.
func TestUndecodedClientResponseIsNeverAnswered(t *testing.T) {
	c := newStdioClient()
	sent := make(chan map[string]any, 1)
	c.send = func(v any) { sent <- v.(map[string]any) }
	errc := make(chan error, 1)
	go func() {
		_, err := c.Request(context.Background(), "roots/list", map[string]any{})
		errc <- err
	}()
	req := <-sent
	id, _ := json.Marshal(req["id"])
	refused := overloadError(budget.ErrOverloaded)
	big := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"roots":[{"uri":"file:///%s"}]}}`, id, strings.Repeat("a", 8<<10))
	if resp := c.answerUndecoded([]byte(big[:idProbeBytes]), refused); resp != nil {
		t.Fatalf("a client response was answered: %+v", resp)
	}
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "dropped unread") {
			t.Fatalf("waiting request: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting request was not failed")
	}
	cases := []struct {
		probe string
		want  string // "" means no reply
	}{
		// a stray answer (nothing pending), and one whose result lies past the prefix
		{`{"jsonrpc":"2.0","id":"xmustard-99","result":{"roots":[`, ""},
		{`{"jsonrpc":"2.0","id":"xmustard-98","error":{"code":-1`, ""},
		// requests keep their probed id; one whose id lies past the prefix gets a null id
		{`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"x":"`, "7"},
		{`{"jsonrpc":"2.0","method":"tools/call","params":{"x":"`, "null"},
		// a request that also carries a result member is still a request
		{`{"jsonrpc":"2.0","id":8,"method":"ping","result":{`, "8"},
	}
	for _, tc := range cases {
		resp := c.answerUndecoded([]byte(tc.probe), refused)
		switch {
		case tc.want == "" && resp != nil:
			t.Errorf("%s: answered %+v", tc.probe, resp)
		case tc.want != "" && resp == nil:
			t.Errorf("%s: no reply", tc.probe)
		case tc.want != "":
			if got, _ := json.Marshal(resp.ID); string(got) != tc.want && !(tc.want == "null" && resp.ID == nil) {
				t.Errorf("%s: reply id %s, want %s", tc.probe, got, tc.want)
			}
		}
	}
}

// End to end: a roots/list answer over the 8 MiB frame cap produces no reply of any
// kind, and the tool call that asked finishes at once, naming the dropped answer.
func TestShimDropsAnOversizedRootsAnswer(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer api.Close()
	p := startShim(t, "/", "XMUSTARD_API_BASE="+api.URL, "XMUSTARD_WORKSPACE_ID=")
	p.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"roots":{}},"clientInfo":{"name":"t","version":"1"}}}`)
	p.next(t)
	p.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	p.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ground","arguments":{}}}`)
	req := p.next(t)
	if req["method"] != "roots/list" {
		t.Fatalf("expected roots/list, got %v", req)
	}
	id, _ := json.Marshal(req["id"])
	start := time.Now()
	p.send(t, `{"jsonrpc":"2.0","id":%s,"result":{"roots":[{"uri":"file:///%s"}]}}`, id, strings.Repeat("a", maxMessageBytes))
	res := p.next(t)
	if res["id"] != float64(2) {
		t.Fatalf("the oversized answer was answered (or the call lost): %v", res)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("the call waited %s for an answer that was already dropped", took)
	}
	if msg := fmt.Sprint(res["result"]); !strings.Contains(msg, "dropped unread") {
		t.Fatalf("the call does not name the dropped answer: %s", msg)
	}
	select {
	case m := <-p.lines:
		t.Fatalf("unexpected extra message (a response must never be answered): %v", m)
	case <-time.After(300 * time.Millisecond):
	}
}

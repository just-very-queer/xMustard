package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xmustard/api-go/internal/conformance"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/hooks"
)

// Every Cursor event has exactly one way to be answered, a gate row, and a route
// (WS-40c-b). The gate row is not optional: a route with none is refused.
func TestCursorHookTablesAndGatesAgree(t *testing.T) {
	for _, e := range hooks.CursorEvents {
		_, answers := hookHandlers[e.Key()]
		_, queued := hookEnqueued[e.Key()]
		if answers == queued || queued != (e.Mode == hooks.Enqueue) {
			t.Errorf("%s: handler %v, queue effect %v, mode %v", e.Name, answers, queued, e.Mode)
		}
		if _, ok := routeGateTable["POST /api/hooks/cursor/"+e.Name]; !ok {
			t.Errorf("%s has no gate row", e.Name)
		}
	}
}

// postCursor posts one Cursor event, pinned to a workspace by the header the client
// sends (the id the daemon validates).
func (f *hookFixture) postCursor(t testing.TB, token, event, pin, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/hooks/cursor/"+event, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if pin != "" {
		req.Header.Set("X-Xmustard-Workspace", pin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b
}

// Pinned source-derived inputs are replayed through the production route. The object
// Cursor would pass to its next model request is the route's updated result when present;
// otherwise Cursor retains its original tool output. preCompact is enqueue-mode silence,
// not a cancelled in-flight request.
func TestCursorConformanceFixturesThroughProductionRoute(t *testing.T) {
	f := newHookFixture(t)
	fixtureDir := filepath.Join("..", "..", "..", "integrations", "cursor", "testdata")
	if err := conformance.RunCursorFixtures(fixtureDir); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range conformance.CursorFixtures {
		body, err := conformance.ReadCursorFixture(fixtureDir, fixture)
		if err != nil {
			t.Fatal(err)
		}
		var source struct {
			ToolOutput string `json:"tool_output"`
		}
		if err := json.Unmarshal(body, &source); err != nil {
			t.Fatal(err)
		}
		code, out := f.postCursor(t, f.agent, fixture.Event, f.ws, string(body))
		if code != http.StatusOK {
			t.Fatalf("%s returned %d: %s", fixture.Input, code, out)
		}
		var response struct {
			Context  string `json:"additional_context"`
			Replaced string `json:"updated_mcp_tool_output"`
		}
		allowedKeys := map[string]map[string]bool{
			"sessionStart": {"additional_context": true},
			"preToolUse":   {},
			"postToolUse":  {"additional_context": true, "updated_mcp_tool_output": true},
			"preCompact":   {},
		}
		if len(out) != 0 {
			if err := json.Unmarshal(out, &response); err != nil {
				t.Fatalf("%s route response is invalid: %s: %v", fixture.Input, out, err)
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(out, &keys); err != nil {
				t.Fatal(err)
			}
			for key := range keys {
				if !allowedKeys[fixture.Event][key] {
					t.Fatalf("%s returned unexpected key %q", fixture.Input, key)
				}
			}
		}
		checks := map[string]func(){
			"sessionStart": func() {
				if response.Context == "" {
					t.Fatalf("%s gave Cursor no model context", fixture.Input)
				}
			},
			// The fixture produces no hits or triggered memories, so there is nothing to
			// say; writeHookAnswer's contract is "200 with an empty body when there is
			// nothing to say" (hooks_routes.go).
			"preToolUse": func() {
				if len(out) != 0 {
					t.Fatalf("%s expected no context, got %s", fixture.Input, out)
				}
			},
			"postToolUse": func() {
				modelOutput := source.ToolOutput
				if response.Replaced != "" {
					modelOutput = response.Replaced
				}
				var modelResult map[string]json.RawMessage
				if err := json.Unmarshal([]byte(modelOutput), &modelResult); err != nil || len(modelResult) == 0 {
					t.Fatalf("client model result is not the original or a shaped replacement: %q", modelOutput)
				}
			},
			"preCompact": func() {
				if len(out) != 0 {
					t.Fatalf("preCompact is enqueue-mode silence, got %s", out)
				}
			},
		}
		if check := checks[fixture.Event]; check != nil {
			check()
		} else {
			t.Fatalf("no conformance expectation for event %s", fixture.Event)
		}
	}
}

type gatedCaptureRedactor struct {
	writer  io.Writer
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *gatedCaptureRedactor) Write(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	return r.writer.Write(p)
}

func (*gatedCaptureRedactor) Flush() error { return nil }

// A cancelled PostToolUse capture is neither retained nor sent back as a replacement.
func TestCursorCancelledCaptureRetainsNoEvidenceOrReplacement(t *testing.T) {
	f := newHookFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	oldRedactor := captureRedactor
	captureRedactor = func(w io.Writer) evidence.StreamRedactor {
		return &gatedCaptureRedactor{writer: w, started: started, release: release}
	}
	t.Cleanup(func() { captureRedactor = oldRedactor })
	output := `{"content":[{"type":"text","text":"captured"}],"isError":false}`
	body, err := json.Marshal(map[string]any{
		"hook_event_name": "postToolUse",
		"conversation_id": "cancelled",
		"tool_name":       "MCP:ci_logs",
		"tool_output":     output,
		"cwd":             f.root,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/cursor/postToolUse", strings.NewReader(string(body))).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+f.agent)
	req.Header.Set("X-Xmustard-Workspace", f.ws)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		f.srv.Config.Handler.ServeHTTP(response, req)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		close(release)
		<-done
		t.Fatal("capture did not reach the redactor")
	}
	cancel()
	close(release)
	<-done
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("cancelled capture returned %d %q; want empty fail-open answer", response.Code, response.Body.String())
	}
	entries, err := os.ReadDir(filepath.Join(f.dir, "evidence", f.ws))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled capture left evidence entries %v (read err %v)", entries, err)
	}
}

func TestCursorHookAnswersInCursorsSchema(t *testing.T) {
	f := newHookFixture(t)
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "sessionStart",
		"conversation_id": "cursor-1",
		"workspace_roots": []string{f.root},
	})
	code, out := f.postCursor(t, f.agent, "sessionStart", f.ws, string(body))
	if code != http.StatusOK {
		t.Fatalf("sessionStart: %d %s", code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("sessionStart: the answer is not a JSON object: %v (%s)", err, out)
	}
	for k := range m {
		if k != "additional_context" && k != "updated_mcp_tool_output" {
			t.Errorf("sessionStart wrote %q, which Cursor does not read", k)
		}
	}
	if _, ok := m["additional_context"]; !ok {
		t.Errorf("sessionStart wrote no additional_context: %s", out)
	}
}

// The workspace is bound by the pin the client sends, and a pin the daemon cannot
// validate binds nothing: the answer says nothing and the user keeps their own output.
func TestCursorHookBindsOnlyAValidatedPin(t *testing.T) {
	f := newHookFixture(t)
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "preToolUse",
		"conversation_id": "cursor-2",
		"tool_name":       "Read",
		"tool_input":      map[string]any{"file_path": f.root + "/src/billing.go"},
	})
	if _, out := f.postCursor(t, f.agent, "preToolUse", f.ws, string(body)); !strings.Contains(string(out), "billing rounding") {
		t.Fatalf("a pinned agent gets the path-bound memory: %s", out)
	}
	// A pin that is not a workspace id binds nothing, and the answer is empty.
	if code, out := f.postCursor(t, f.agent, "preToolUse", "../../etc", string(body)); code != http.StatusOK || len(out) != 0 {
		t.Fatalf("an unvalidated pin got %d %s", code, out)
	}
}

// A body that names another event is refused, and the client keeps its own output.
func TestCursorHookRefusesAnotherEventsBody(t *testing.T) {
	f := newHookFixture(t)
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "postToolUse",
		"conversation_id": "cursor-3",
		"tool_name":       "MCP:ci_logs",
		"tool_output":     `{"content":"the log"}`,
	})
	if code, out := f.postCursor(t, f.agent, "sessionStart", f.ws, string(body)); code != http.StatusOK || len(out) != 0 {
		t.Fatalf("a body naming another event got %d %s", code, out)
	}
}

// An MCP tool's result is replaced by the projection, in the key Cursor reads.
func TestCursorHookReplacesAnMCPToolResult(t *testing.T) {
	f := newHookFixture(t)
	big := strings.Repeat("x", 40000)
	result := `{"content":[{"type":"text","text":"` + big + `"}],"isError":true}`
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "postToolUse",
		"conversation_id": "cursor-4",
		"tool_name":       "MCP:ci_logs",
		"tool_output":     result,
	})
	code, out := f.postCursor(t, f.agent, "postToolUse", f.ws, string(body))
	if code != http.StatusOK {
		t.Fatalf("postToolUse: %d %s", code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("postToolUse: the answer is not a JSON object: %v (%s)", err, out)
	}
	replaced, ok := m["updated_mcp_tool_output"].(string)
	if !ok {
		t.Fatalf("postToolUse wrote no updated_mcp_tool_output: %s", out)
	}
	if len(replaced) >= len(big) {
		t.Errorf("the projection carries %d characters, the un-reduced output %d", len(replaced), len(big))
	}
	var shaped struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal([]byte(replaced), &shaped); err != nil || len(shaped.Content) == 0 {
		t.Fatalf("production capture/reduction lost the Cursor result shape: %s", replaced)
	}
	if !shaped.IsError {
		t.Fatalf("production replacement lost top-level error state: %s", replaced)
	}
	modelResult := strings.SplitN(shaped.Content[0].Text, "\n[xmustard evidence]", 2)[0]
	var original struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal([]byte(modelResult), &original); err != nil || !original.IsError {
		t.Fatalf("production capture/reduction lost the tool error state: %s", modelResult)
	}
}

func TestCursorHookReplacementKeepsSuccessState(t *testing.T) {
	f := newHookFixture(t)
	result := `{"content":[{"type":"text","text":"` + strings.Repeat("x", 40000) + `"}],"isError":false}`
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "postToolUse",
		"conversation_id": "cursor-success",
		"tool_name":       "MCP:ci_logs",
		"tool_output":     result,
	})
	code, out := f.postCursor(t, f.agent, "postToolUse", f.ws, string(body))
	if code != http.StatusOK {
		t.Fatalf("postToolUse: %d %s", code, out)
	}
	var response struct {
		Replaced string `json:"updated_mcp_tool_output"`
	}
	if err := json.Unmarshal(out, &response); err != nil || response.Replaced == "" {
		t.Fatalf("postToolUse gave no replacement: %s (%v)", out, err)
	}
	var shaped struct {
		IsError *bool `json:"isError"`
	}
	if err := json.Unmarshal([]byte(response.Replaced), &shaped); err != nil {
		t.Fatalf("successful result is invalid JSON: %v: %s", err, response.Replaced)
	}
	if shaped.IsError == nil || *shaped.IsError {
		t.Fatalf("successful result has isError=%v: %s", shaped.IsError, response.Replaced)
	}
}

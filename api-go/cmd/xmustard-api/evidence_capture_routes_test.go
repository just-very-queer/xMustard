package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

// identityRedactor enables the capture route in tests that are not about redaction;
// it redacts nothing.
type identityRedactor struct{ io.Writer }

func (identityRedactor) Flush() error { return nil }

// withCaptureRedactor installs a capture redactor for one test.
func withCaptureRedactor(t *testing.T, f func(io.Writer) evidence.StreamRedactor) {
	t.Helper()
	prev := captureRedactor
	if f == nil {
		f = func(w io.Writer) evidence.StreamRedactor { return identityRedactor{w} }
	}
	captureRedactor = f
	t.Cleanup(func() { captureRedactor = prev })
}

func claudeBashBody(stdout string) string {
	raw, _ := json.Marshal(stdout)
	return `{"session_id":"sess-1","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."},` +
		`"tool_response":{"stdout":` + string(raw) + `,"stderr":"","interrupted":false,"isImage":false},"tool_use_id":"toolu_1"}`
}

func goTestLog(pass int) string {
	var b strings.Builder
	for i := 0; i < pass; i++ {
		fmt.Fprintf(&b, "=== RUN   TestCase%04d\n--- PASS: TestCase%04d (0.00s)\n", i, i)
		if i == pass/2 {
			b.WriteString("=== RUN   TestParse\n    parse_test.go:17: got 3, want 4\n--- FAIL: TestParse (0.00s)\n")
		}
	}
	b.WriteString("FAIL\nFAIL\texample.com/pkg\t0.412s\nFAIL\n")
	return b.String()
}

// A Claude Code PostToolUse body for a native Bash test run is captured, reduced by
// the test family and returned as a schema-matched updatedToolOutput value; the
// handle then serves pages and search, bound to the capturing principal.
func TestCaptureRouteClaudeBashAndSearch(t *testing.T) {
	withCaptureRedactor(t, nil)
	f := newEvidenceFixture(t, true)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	bob, _ := workspaceops.MintToken(f.dir, "bob", "agent")
	reader, _ := workspaceops.MintToken(f.dir, "rita", "readonly")
	log := goTestLog(4000)
	path := "/api/workspaces/" + f.ws + "/evidence/capture?format=claude&client=claude&tool_version=2.1"
	code, b, _ := f.do(t, "POST", path, alice, strings.NewReader(claudeBashBody(log)), nil)
	if code != 200 {
		t.Fatalf("capture: %d %s", code, b)
	}
	var res struct {
		evidence.Delivery
		Capture            evidence.CaptureMeta  `json:"capture"`
		Family             string                `json:"family"`
		Facts              evidence.Facts        `json:"facts"`
		DeliveredTokensEst int                   `json:"delivered_tokens_est"`
		Shape              *evidence.ShapeResult `json:"shape"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if res.Family != "test" || res.Reducer != "xm-test/1" || res.Handle == "" || res.CapturedIdentity != "unknown" ||
		res.Capture.Principal == "" || res.Capture.ToolVersion != "2.1" || res.Capture.CallID != "toolu_1" ||
		res.Facts.Failed != 1 || res.Facts.Passed != 4000 || res.DeliveredTokensEst <= 0 {
		t.Fatalf("envelope: %+v", res)
	}
	if res.Shape == nil || res.Shape.Mode != evidence.ShapeReplace || evidence.ValidateShape("claude", "Bash", res.Shape.Payload) != nil {
		t.Fatalf("shape: %+v", res.Shape)
	}
	// search the original: the failing test, then a line range
	s := "/api/workspaces/" + f.ws + "/evidence/search?handle=" + res.Handle
	code, b, _ = f.do(t, "GET", s+"&pattern="+url.QueryEscape("--- FAIL"), alice, nil, nil)
	var sr evidence.SearchResult
	_ = json.Unmarshal(b, &sr)
	if code != 200 || sr.Matches != 1 || !strings.Contains(sr.Lines[0].Text, "--- FAIL: TestParse") || sr.ChunkSize != evidence.SearchChunk {
		t.Fatalf("search: %d %s", code, b)
	}
	code, b, _ = f.do(t, "GET", s+"&lines="+fmt.Sprint(sr.Lines[0].Line-1)+"-"+fmt.Sprint(sr.Lines[0].Line), alice, nil, nil)
	_ = json.Unmarshal(b, &sr)
	if code != 200 || len(sr.Lines) != 2 || !strings.Contains(sr.Lines[0].Text, "got 3, want 4") {
		t.Fatalf("lines: %d %s", code, b)
	}
	// fail closed: another principal, no principal, another workspace, bad requests
	if code, _, _ := f.do(t, "GET", s+"&pattern=FAIL", bob, nil, nil); code != http.StatusForbidden {
		t.Fatalf("other principal: %d", code)
	}
	if code, _, _ := f.do(t, "GET", s+"&pattern=FAIL", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", code)
	}
	if code, _, _ := f.do(t, "GET", "/api/workspaces/otherWs/evidence/search?handle="+res.Handle+"&pattern=x", alice, nil, nil); code != http.StatusNotFound {
		t.Fatalf("other workspace: %d", code)
	}
	for _, q := range []string{"&pattern=(", "&max_matches=9999", "&lines=9-3", "", "&context=x"} {
		if code, b, _ := f.do(t, "GET", s+q, alice, nil, nil); code != http.StatusBadRequest {
			t.Fatalf("%q: want 400, got %d %s", q, code, b)
		}
	}
	// a readonly principal cannot capture; malformed bodies and formats are 400
	if code, _, _ := f.do(t, "POST", path, reader, strings.NewReader(claudeBashBody(log)), nil); code != http.StatusForbidden {
		t.Fatalf("readonly capture: %d", code)
	}
	if code, b, _ := f.do(t, "POST", path, alice, strings.NewReader(`{"tool_name":"Bash","tool_response":`), nil); code != http.StatusBadRequest || !strings.Contains(string(b), "bad_body") {
		t.Fatalf("malformed body: %d %s", code, b)
	}
	if code, _, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?format=xml", alice, strings.NewReader("x"), nil); code != http.StatusBadRequest {
		t.Fatalf("bad format accepted")
	}
	// an unknown client would silently get another client's payload shape: refused; the
	// MCP client profile name claude-code is Claude's policy
	if code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?format=claude&client=netscape", alice, strings.NewReader(claudeBashBody(log)), nil); code != http.StatusBadRequest ||
		!strings.Contains(string(b), "invalid_client") || !strings.Contains(string(b), "claude-code") {
		t.Fatalf("unknown client: %d %s", code, b)
	}
	code, b, _ = f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?format=claude&client=claude-code", alice, strings.NewReader(claudeBashBody(log)), nil)
	_ = json.Unmarshal(b, &res)
	if code != 200 || res.Capture.Client != "claude" || res.Shape.Shape != "claude.Bash" || res.Shape.Mode != evidence.ShapeReplace {
		t.Fatalf("claude-code capture: %d %+v", code, res.Shape)
	}
	// raw capture of any tool, with the caller's metadata
	code, b, _ = f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?client=pi&tool=bash&command=go%20test&exit_code=1&call_id=c9", alice, strings.NewReader(log), nil)
	_ = json.Unmarshal(b, &res)
	if code != 200 || res.Family != "test" || res.Capture.ExitCode == nil || *res.Capture.ExitCode != 1 || !res.Capture.IsError || res.Shape.Mode != evidence.ShapeReplace {
		t.Fatalf("raw capture: %d %s", code, b[:min(len(b), 400)])
	}
}

// A capture body is streamed, not reserved or buffered: a 16 MiB chunked hook body
// (no Content-Length) goes through the production redactor while the transient pool
// never holds more than the capture window, where the generic route would have to
// buffer it whole.
func TestCaptureRouteStreamsLargeChunkedBody(t *testing.T) {
	prev := budget.TransientBytes
	pool := budget.NewByteBudget(8 << 20)
	budget.TransientBytes = pool
	defer func() { budget.TransientBytes = prev }()
	f := newEvidenceFixture(t, true)
	var stdout strings.Builder
	for stdout.Len() < 15<<20 {
		stdout.WriteString("--- PASS: TestCase (0.00s)\n")
	}
	stdout.WriteString("--- FAIL: TestLast (0.00s)\nFAIL\n")
	body := claudeBashBody(stdout.String())
	pr, pw := io.Pipe()
	go func() {
		for off := 0; off < len(body); off += 64 << 10 {
			_, _ = pw.Write([]byte(body[off:min(len(body), off+64<<10)]))
		}
		pw.Close()
	}()
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/workspaces/"+f.ws+"/evidence/capture?format=claude&client=claude", pr)
	req.ContentLength = -1 // chunked
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("chunked 16 MiB capture: %d %s", resp.StatusCode, b[:min(len(b), 300)])
	}
	var res struct {
		evidence.Delivery
		Facts evidence.Facts `json:"facts"`
	}
	_ = json.Unmarshal(b, &res)
	if res.RawBytes < 15<<20 || res.Handle == "" || res.Facts.Failed != 1 {
		t.Fatalf("capture: raw %d handle %q facts %+v", res.RawBytes, res.Handle, res.Facts)
	}
	if pool.Peak() > captureWindowBytes+4<<20 || pool.InUse() != 0 {
		t.Fatalf("transient pool peak %d (in use after %d): the body was reserved or buffered", pool.Peak(), pool.InUse())
	}
	t.Logf("16 MiB chunked capture: pool peak %d KiB", pool.Peak()>>10)
}

// Capture fails closed without a streaming redactor: originals are retained for the
// retention window and searchable, so nothing is captured unredacted.
func TestCaptureRouteRefusesWithoutRedactor(t *testing.T) {
	prev := captureRedactor
	captureRedactor = nil
	t.Cleanup(func() { captureRedactor = prev })
	f := newEvidenceFixture(t, true)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?format=claude&client=claude", alice,
		strings.NewReader(claudeBashBody(goTestLog(4000))), nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(string(b), "redaction_unavailable") {
		t.Fatalf("capture without a redactor: %d %s", code, b)
	}
	entries, _ := filepath.Glob(filepath.Join(f.dir, "evidence", f.ws, "*"))
	for _, e := range entries {
		if fi, err := os.Stat(filepath.Join(e, "raw.bin")); err == nil && fi.Size() > 0 {
			t.Fatalf("an original was retained: %s", e)
		}
	}
}

// A secret in any tool output is neither retained nor searchable through the route
// (the production redactor; see capture_redactor_test.go for the rule set).
func TestCaptureRouteRedactsSecrets(t *testing.T) {
	secret := "gh" + "p_0123456789abcdefghijklmnopqrstuvwxyzAB"
	f := newEvidenceFixture(t, true)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	log := strings.Replace(goTestLog(4000), "got 3, want 4", "got 3, want 4 (token "+secret+")", 1)
	code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?format=claude&client=claude", alice, strings.NewReader(claudeBashBody(log)), nil)
	if code != 200 || strings.Contains(string(b), secret) {
		t.Fatalf("capture: %d (secret in reply: %v)", code, strings.Contains(string(b), secret))
	}
	var res evidence.Delivery
	_ = json.Unmarshal(b, &res)
	code, b, _ = f.do(t, "GET", "/api/workspaces/"+f.ws+"/evidence/search?handle="+res.Handle+"&query=ghp_", alice, nil, nil)
	var sr evidence.SearchResult
	_ = json.Unmarshal(b, &sr)
	if code != 200 || sr.Matches != 0 {
		t.Fatalf("the secret is searchable: %d %s", code, b)
	}
	code, b, _ = f.do(t, "GET", "/api/workspaces/"+f.ws+"/evidence/search?handle="+res.Handle+"&query=REDACTED", alice, nil, nil)
	_ = json.Unmarshal(b, &sr)
	if code != 200 || sr.Matches != 1 {
		t.Fatalf("the redacted line: %d %s", code, b)
	}
}

// 100,000 content blocks through the real route: a 200 with a projection within the
// client target, transient memory admitted by the fixed window, and a small reply.
func TestCaptureRouteManyContentBlocks(t *testing.T) {
	withCaptureRedactor(t, nil)
	prev := budget.TransientBytes
	pool := budget.NewByteBudget(16 << 20)
	budget.TransientBytes = pool
	defer func() { budget.TransientBytes = prev }()
	f := newEvidenceFixture(t, true)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	var body strings.Builder
	body.WriteString(`{"tool_name":"mcp__gh__list_issues","tool_input":{"repo":"x"},"tool_response":[`)
	for i := 0; i < 100000; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"type":"text","text":"issue #%d: something broke in module %d"}`, i, i%50)
	}
	body.WriteString(`]}`)
	for _, client := range []string{"claude", "codex"} {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?format=claude&client="+client, alice, strings.NewReader(body.String()), nil)
		runtime.ReadMemStats(&after)
		if code != 200 {
			t.Fatalf("%s: %d %s", client, code, b[:min(len(b), 300)])
		}
		var res struct {
			evidence.Delivery
			Shape *evidence.ShapeResult `json:"shape"`
		}
		_ = json.Unmarshal(b, &res)
		t.Logf("%s: %d-byte body, reply %d bytes, projection %d, shape %s, %d KiB allocated (test and server), pool peak %d KiB",
			client, body.Len(), len(b), len(res.Projection), res.Shape.Mode, (after.TotalAlloc-before.TotalAlloc)>>10, pool.Peak()>>10)
		if len(res.Projection) > evidence.PolicyFor(client).Target || res.Handle == "" || len(b) > 256<<10 {
			t.Fatalf("%s: projection %d bytes, reply %d bytes", client, len(res.Projection), len(b))
		}
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 128<<20 {
			t.Fatalf("%s: one capture allocated %d bytes", client, alloc)
		}
	}
}

// A hook body past the request cap is a 413, never a malformed body.
func TestCaptureRouteOversizeHookBodyIs413(t *testing.T) {
	withCaptureRedactor(t, nil)
	f := newEvidenceFixture(t, true)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	head := `{"tool_name":"Bash","tool_input":{"command":"yes"},"tool_response":{"stdout":"`
	line := strings.Repeat("y\\n", 1<<14)
	n := (maxRequestBodyBytes+(8<<20))/len(line) + 1
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(head))
		for i := 0; i < n; i++ {
			if _, err := pw.Write([]byte(line)); err != nil {
				return
			}
		}
		_, _ = pw.Write([]byte(`"}}`))
		pw.Close()
	}()
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/workspaces/"+f.ws+"/evidence/capture?format=claude&client=claude", pr)
	req.Header.Set("Authorization", "Bearer "+alice)
	req.ContentLength = -1
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	pr.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge || strings.Contains(string(b), "bad_body") {
		t.Fatalf("oversize hook body: %d %s", resp.StatusCode, b)
	}
}

func TestCaptureRoutesAreCoreSurface(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("XMUSTARD_DATA_DIR", t.TempDir())
	t.Setenv("XMUSTARD_CORE_BIN", "/nonexistent/xmustard-core") // no real core spawns
	for _, c := range []struct{ pattern, method, path string }{
		{"POST /api/workspaces/{workspace_id}/evidence/capture", "POST", "/api/workspaces/ws1/evidence/capture"},
		{"GET /api/workspaces/{workspace_id}/evidence/search", "GET", "/api/workspaces/ws1/evidence/search?handle=ev_missing&pattern=x"},
	} {
		if !routeGateTable[c.pattern].Core {
			t.Fatalf("%s must be a core route in routeGateTable", c.pattern)
		}
		if code, body := serve(t, postureFromEnv(), c.method, c.path); isPlatformRefusal(code, body) {
			t.Fatalf("%s %s must be served in the core profile, got %d %v", c.method, c.path, code, body)
		}
	}
	// capture stores only the caller's own tool output: read-only mode still serves it
	if code, body := serve(t, exposurePosture{ReadOnly: true}, "POST", "/api/workspaces/ws1/evidence/capture"); body["reason"] == "read_only" {
		t.Fatalf("capture must be served in read-only mode, got %d %v", code, body)
	}
	r, _ := http.NewRequest("POST", "http://x/api/workspaces/ws1/evidence/capture", bytes.NewReader(nil))
	if !streamsRequestBody(r) {
		t.Fatal("capture body must stream")
	}
	for _, u := range []string{"http://x/api/workspaces/ws1/evidence", "http://x/api/workspaces/ws1/context"} {
		r, _ = http.NewRequest("POST", u, bytes.NewReader(nil))
		if streamsRequestBody(r) {
			t.Fatalf("%s must keep full-body admission", u)
		}
	}
}

// Existing evidence envelopes (nine-tool delivery and the Pi POST) carry
// delivered_tokens_est; the projection itself is unchanged.
func TestEvidenceEnvelopesCarryTokenEstimate(t *testing.T) {
	f := newEvidenceFixture(t, true)
	code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence?tool=search", "", bytes.NewReader(f.big), nil)
	var env struct {
		evidence.Delivery
		DeliveredTokensEst int    `json:"delivered_tokens_est"`
		TokenEstimator     string `json:"token_estimator"`
	}
	if err := json.Unmarshal(b, &env); err != nil || code != 200 {
		t.Fatalf("pi capture: %d %v", code, err)
	}
	if env.DeliveredTokensEst != evidence.EstimateTokens(env.Projection) || env.DeliveredTokensEst == 0 || env.TokenEstimator != evidence.TokenEstimator {
		t.Fatalf("token estimate: %d (%s)", env.DeliveredTokensEst, env.TokenEstimator)
	}
	code, b, _ = f.do(t, "GET", "/api/workspaces/"+f.ws+"/search?q=x", "", nil, deliver)
	_ = json.Unmarshal(b, &env)
	if code != 200 || env.DeliveredTokensEst == 0 || env.Reducer != evidence.ReducerVersion {
		t.Fatalf("tool delivery: %d est %d reducer %s", code, env.DeliveredTokensEst, env.Reducer)
	}
}

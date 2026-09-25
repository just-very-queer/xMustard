package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

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
	// raw capture of any tool, with the caller's metadata
	code, b, _ = f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?client=pi&tool=bash&command=go%20test&exit_code=1&call_id=c9", alice, strings.NewReader(log), nil)
	_ = json.Unmarshal(b, &res)
	if code != 200 || res.Family != "test" || res.Capture.ExitCode == nil || *res.Capture.ExitCode != 1 || !res.Capture.IsError || res.Shape.Mode != evidence.ShapeReplace {
		t.Fatalf("raw capture: %d %s", code, b[:min(len(b), 400)])
	}
}

// A capture body is streamed, not reserved or buffered: a 16 MiB chunked hook body
// (no Content-Length) goes through while the transient pool never holds more than
// the capture window, where the generic route would have to buffer it whole.
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

func TestCaptureRoutesAreCoreSurface(t *testing.T) {
	for _, p := range []string{"/api/workspaces/ws1/evidence/capture", "/api/workspaces/ws1/evidence/search"} {
		if !isCorePath(p) {
			t.Fatalf("%s must stay available in CORE_ONLY mode", p)
		}
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

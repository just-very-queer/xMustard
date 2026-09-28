package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

// Test secrets are assembled at run time so the source holds no literal credential.
func captureSecrets(rng *rand.Rand) (text string, secrets, rules []string) {
	tok := func(alphabet string, n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(b)
	}
	const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	gh := "gh" + "p_" + tok(alnum, 36)
	aws := "AK" + "IA" + tok("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", 16)
	bearer := tok(alnum, 40)
	pw := "hunter2-" + tok(alnum, 8)
	dbpw := "Tr0ub4dor-" + tok(alnum, 6)
	keyLines := []string{tok(alnum+"+/", 64), tok(alnum+"+/", 64), tok(alnum+"+/", 20) + "=="}
	pem := "-----BEGIN " + "RSA PRIVATE KEY-----\n" + strings.Join(keyLines, "\n") + "\n-----END " + "RSA PRIVATE KEY-----"
	text = "clone with token " + gh + "\n" +
		"aws_access_key_id = " + aws + "\n" +
		"curl -H 'Authorization: Bearer " + bearer + "' https://api.example.test\n" +
		`{"user":"deploy","password":"` + pw + `"}` + "\n" +
		"DATABASE_URL=postgres://admin:" + dbpw + "@db:5432/app\n" +
		pem + "\n"
	return text, []string{gh, aws, bearer, pw, dbpw, keyLines[0], keyLines[1]},
		[]string{"github_token", "aws_access_key_id", "bearer_token", "secret_field", "url_credentials", "private_key"}
}

// The default build redacts every capture with the WS-05 rules (no test redactor is
// installed here): the reply, the retained original and search never show a secret,
// for a Claude hook body, a Pi tool_result and a raw body, with the secrets placed
// past the decoder's 32 KiB writes and the redactor's first 128 KiB window.
func TestCaptureRouteRedactsWithTheProductionRedactor(t *testing.T) {
	f := newEvidenceFixture(t, true)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	rng := rand.New(rand.NewSource(1))
	var lead strings.Builder
	for i := 0; lead.Len() < 150<<10; i++ {
		fmt.Fprintf(&lead, "ok line %05d padding-padding-padding-padding\n", i)
	}
	text, secrets, rules := captureSecrets(rng)
	output := lead.String() + text + "--- FAIL: TestLast (0.00s)\nFAIL\n"
	escaped, _ := json.Marshal(output)
	piBody := `{"type":"tool_result","toolName":"bash","toolCallId":"c1","input":{"command":"make test"},` +
		`"content":[{"type":"text","text":` + string(escaped) + `}],"isError":true}`
	cases := []struct{ name, query, body string }{
		{"claude hook", "format=claude&client=claude", claudeBashBody(output)},
		{"pi tool_result", "format=pi&client=pi", piBody},
		{"raw", "client=pi&tool=bash&command=make%20test&exit_code=2", output},
	}
	for _, tc := range cases {
		code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?"+tc.query, alice, strings.NewReader(tc.body), nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", tc.name, code, b[:min(len(b), 300)])
		}
		var res evidence.Delivery
		_ = json.Unmarshal(b, &res)
		orig, _ := f.expandAll(t, res.Handle, alice)
		for _, s := range secrets {
			if bytes.Contains(b, []byte(s)) || bytes.Contains(orig, []byte(s)) {
				t.Fatalf("%s: a secret survived (reply %v, original %v)", tc.name, bytes.Contains(b, []byte(s)), bytes.Contains(orig, []byte(s)))
			}
		}
		for _, rule := range rules {
			if !bytes.Contains(orig, []byte("[REDACTED:"+rule+"]")) {
				t.Fatalf("%s: no %s marker in the retained original", tc.name, rule)
			}
		}
		if !bytes.Contains(orig, []byte("ok line 03000 padding")) || !bytes.Contains(orig, []byte("--- FAIL: TestLast")) {
			t.Fatalf("%s: the redactor changed text that holds no secret", tc.name)
		}
		code, b, _ = f.do(t, "GET", "/api/workspaces/"+f.ws+"/evidence/search?handle="+res.Handle+"&query=ghp_", alice, nil, nil)
		var sr evidence.SearchResult
		_ = json.Unmarshal(b, &sr)
		if code != 200 || sr.Matches != 0 {
			t.Fatalf("%s: the secret is searchable: %d %s", tc.name, code, b)
		}
	}
}

// panicRedactor stands for a redactor with a bug.
type panicRedactor struct{ io.Writer }

func (panicRedactor) Write([]byte) (int, error) { panic("detector bug") }
func (panicRedactor) Flush() error              { return nil }

// retainedOriginals counts the originals kept for the fixture's workspace.
func retainedOriginals(f *evidenceFixture) int {
	n := 0
	entries, _ := filepath.Glob(filepath.Join(f.dir, "evidence", f.ws, "*"))
	for _, e := range entries {
		if fi, err := os.Stat(filepath.Join(e, "raw.bin")); err == nil && fi.Size() > 0 {
			n++
		}
	}
	return n
}

// A redactor that fails refuses the capture (503 redaction_failed), answers instead
// of dropping the connection, and retains nothing.
func TestCaptureRouteRefusesWhenTheRedactorFails(t *testing.T) {
	withCaptureRedactor(t, func(w io.Writer) evidence.StreamRedactor { return panicRedactor{w} })
	f := newEvidenceFixture(t, true)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	for _, q := range []string{"format=claude&client=claude", "client=pi&tool=bash"} {
		body := claudeBashBody(goTestLog(4000))
		if !strings.HasPrefix(q, "format=") {
			body = goTestLog(4000)
		}
		code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?"+q, alice, strings.NewReader(body), nil)
		if code != http.StatusServiceUnavailable || !strings.Contains(string(b), `"redaction_failed"`) {
			t.Fatalf("%s: %d %s", q, code, b)
		}
	}
	if n := retainedOriginals(f); n != 0 {
		t.Fatalf("%d originals retained after a redactor failure", n)
	}
}

// The output of a secret path (WS-72 denylist) is refused with 422 secret_path and
// never retained, wherever the body names the path; templates stay capturable.
func TestCaptureRouteRefusesSecretPaths(t *testing.T) {
	f := newEvidenceFixture(t, true)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	content, _ := json.Marshal(strings.Repeat("API_URL=https://internal.example.test\nFEATURE=on\n", 1000))
	read := func(path string, inputFirst bool) string {
		input := `"tool_input":{"file_path":"` + path + `"}`
		response := `"tool_response":{"type":"text","file":{"filePath":"` + path + `","content":` + string(content) + `}}`
		if !inputFirst {
			input, response = response, input
		}
		return `{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Read",` + input + `,` + response + `,"tool_use_id":"t1"}`
	}
	refused := []struct{ query, body string }{
		{"format=claude&client=claude", read("/home/dev/app/.env", true)},
		{"format=claude&client=claude", read("/home/dev/app/.env.production", false)},
		{"format=claude&client=claude", read("/home/dev/.ssh/config", true)},
		{"client=pi&tool=read&path=deploy/.npmrc&target=1024", string(content)},
	}
	for _, tc := range refused {
		code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?"+tc.query, alice, strings.NewReader(tc.body), nil)
		if code != http.StatusUnprocessableEntity || !strings.Contains(string(b), `"secret_path"`) {
			t.Fatalf("%s: %d %s", tc.query, code, b[:min(len(b), 300)])
		}
	}
	if n := retainedOriginals(f); n != 0 {
		t.Fatalf("%d originals of secret paths retained", n)
	}
	code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence/capture?format=claude&client=claude", alice,
		strings.NewReader(read("/home/dev/app/.env.example", true)), nil)
	if code != 200 {
		t.Fatalf(".env.example: %d %s", code, b[:min(len(b), 300)])
	}
}

// The production redactor fits the capture window: 16 MiB of nothing but secrets, in
// the decoder's 32 KiB writes, grows the live heap by less than captureWindowBytes.
func TestCaptureRedactorFitsTheCaptureWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 16 MiB")
	}
	chunk := bytes.Repeat([]byte("password=Zx9!Zx9!\n"), (32<<10)/18)
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	base, peak := ms.HeapAlloc, uint64(0)
	red := captureRedactor(io.Discard)
	for written := 0; written < 16<<20; written += len(chunk) {
		if _, err := red.Write(chunk); err != nil {
			t.Fatal(err)
		}
		if written%(1<<20) < len(chunk) {
			runtime.GC()
			runtime.ReadMemStats(&ms)
			peak = max(peak, ms.HeapAlloc)
		}
	}
	if err := red.Flush(); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(red)
	growth := int64(peak) - int64(base)
	t.Logf("16 MiB of secrets: live heap growth peak %d KiB (capture window %d KiB)", growth>>10, captureWindowBytes>>10)
	if growth >= captureWindowBytes {
		t.Fatalf("the capture redactor holds %d KiB, beyond the %d KiB capture window", growth>>10, captureWindowBytes>>10)
	}
}

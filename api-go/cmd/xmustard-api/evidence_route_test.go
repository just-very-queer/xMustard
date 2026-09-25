package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

type evidenceFixture struct {
	srv     *httptest.Server
	dir     string
	ws      string
	big     []byte
	keyFile string
}

// newEvidenceFixture serves the real handler stack over a temp data dir with a fake
// Rust core: `search` prints a large JSON result containing one failure among many
// passes, and `repo-key` reports the content of keyFile as a complete identity.
func newEvidenceFixture(t *testing.T, withRepoKey bool) *evidenceFixture {
	t.Helper()
	f := &evidenceFixture{ws: "wsEv"}
	f.dir = t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", f.dir)
	seedCoreWorkspace(t, f.dir, f.ws)
	hits := make([]map[string]any, 3000)
	for i := range hits {
		hits[i] = map[string]any{"kind": "symbol", "name": fmt.Sprintf("ok%d", i), "path": fmt.Sprintf("src/f%04d.go", i), "line": i + 1, "score": 1.0, "reason": "lexical match"}
	}
	hits[2222] = map[string]any{"kind": "symbol", "name": "broken", "path": "src/broken.go", "line": 7, "score": 0.5, "reason": "panic: assertion failed"}
	f.big, _ = json.Marshal(map[string]any{"query": "x", "hits": hits})
	bigFile := filepath.Join(t.TempDir(), "big.json")
	_ = os.WriteFile(bigFile, f.big, 0o644)
	f.keyFile = filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(f.keyFile, []byte("rev-1"), 0o644)
	repoKey := `repo-key) echo "unknown command" >&2; exit 2 ;;`
	if withRepoKey {
		repoKey = `repo-key) printf '{"key":"%s","identity_complete":true}' "$(cat ` + f.keyFile + `)" ;;`
	}
	core := writeScript(t, `case "$1" in
search) cat `+bigFile+` ;;
`+repoKey+`
changetrack) printf '{"head_sha":"abc","content_hash":"h","dirty":false,"changed_files":[]}' ;;
*) echo '{}' ;;
esac
`)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	f.srv = httptest.NewServer(bodyLimitMiddleware(authMiddleware(f.dir, "auto", newAPIHandler())))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *evidenceFixture) do(t *testing.T, method, path, token string, body io.Reader, hdr map[string]string) (int, []byte, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

var deliver = map[string]string{deliveryHeader: evidence.DeliveryVersion, "X-Xmustard-Call-Id": "7", "X-Xmustard-Session-Id": "sess", "X-Xmustard-Issuer": "mcp"}

func (f *evidenceFixture) expandAll(t *testing.T, handle, token string) ([]byte, map[string]any) {
	t.Helper()
	var out []byte
	var last map[string]any
	off := 0
	for {
		code, b, _ := f.do(t, "GET", fmt.Sprintf("/api/workspaces/%s/evidence/%s?offset=%d", f.ws, handle, off), token, nil, nil)
		if code != 200 {
			t.Fatalf("expand at %d: %d %s", off, code, b)
		}
		_ = json.Unmarshal(b, &last)
		data, err := base64.StdEncoding.DecodeString(last["data"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > evidence.DefaultPageSize {
			t.Fatalf("page of %d bytes exceeds 64 KiB", len(data))
		}
		out = append(out, data...)
		if last["eof"] == true {
			return out, last
		}
		off = int(last["next_offset"].(float64))
	}
}

// Stage 2: the MCP delivery path returns a bounded projection that keeps the one
// failure, and the handle pages back the exact original through the shared endpoint.
func TestToolDeliveryProjectsAndExpandsExactOriginal(t *testing.T) {
	f := newEvidenceFixture(t, true)
	code, b, h := f.do(t, "GET", "/api/workspaces/"+f.ws+"/search?q=x", "", nil, deliver)
	if code != 200 || h.Get(deliveryHeader) != evidence.DeliveryVersion {
		t.Fatalf("delivery: %d %s", code, b[:min(len(b), 300)])
	}
	var d evidence.Delivery
	_ = json.Unmarshal(b, &d)
	if !d.Reduced || d.Handle == "" || d.Tool != "search" || d.CallID != "7" || d.IsError || d.Status != 200 {
		t.Fatalf("envelope: %+v", d)
	}
	if d.ProjectedBytes > evidence.DefaultProjectionTarget || !strings.Contains(d.Projection, "src/broken.go") {
		t.Fatalf("projection %d bytes, failure kept=%v", d.ProjectedBytes, strings.Contains(d.Projection, "src/broken.go"))
	}
	orig, last := f.expandAll(t, d.Handle, "")
	sum := sha256.Sum256(orig)
	var got struct {
		Hits []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal(orig, &got); err != nil || len(got.Hits) != 3000 || int64(len(orig)) != d.RawBytes || hex.EncodeToString(sum[:]) != d.RawSHA256 {
		t.Fatalf("expanded original differs from the captured handler output (%d vs %d bytes, %d hits)", len(orig), d.RawBytes, len(got.Hits))
	}
	if last["freshness"] != "current" || last["stale"] != false || last["captured_key"] != "rev-1" {
		t.Fatalf("freshness labels: %v", last)
	}
	// plain callers (no delivery header) are unchanged
	code, b, h = f.do(t, "GET", "/api/workspaces/"+f.ws+"/search?q=x", "", nil, nil)
	if code != 200 || h.Get(deliveryHeader) != "" || !json.Valid(b) || len(b) < 100000 {
		t.Fatalf("plain route changed: %d %d bytes", code, len(b))
	}
}

func TestDeliveredErrorsStayErrors(t *testing.T) {
	f := newEvidenceFixture(t, true)
	code, b, _ := f.do(t, "GET", "/api/workspaces/missingWs/session-grounding", "", nil, deliver)
	var d evidence.Delivery
	_ = json.Unmarshal(b, &d)
	if code != 200 || !d.IsError || d.Status != 404 || d.Tool != "ground" || !strings.Contains(d.Projection, "error") {
		t.Fatalf("error must stay an error in the envelope: %d %+v", code, d)
	}
}

func TestStaleAndUnknownFreshnessLabels(t *testing.T) {
	f := newEvidenceFixture(t, true)
	_, b, _ := f.do(t, "GET", "/api/workspaces/"+f.ws+"/search?q=x", "", nil, deliver)
	var d evidence.Delivery
	_ = json.Unmarshal(b, &d)
	_ = os.WriteFile(f.keyFile, []byte("rev-2"), 0o644) // repository mutated
	_, last := f.expandAll(t, d.Handle, "")
	if last["stale"] != true || last["freshness"] != "stale" || last["current_key"] != "rev-2" {
		t.Fatalf("mutation must label stale: %v", last)
	}
	// without the Rust repo-key command, the fingerprint fallback is incomplete:
	// freshness is unknown, never current
	g := newEvidenceFixture(t, false)
	_, b, _ = g.do(t, "GET", "/api/workspaces/"+g.ws+"/search?q=x", "", nil, deliver)
	_ = json.Unmarshal(b, &d)
	_, last = g.expandAll(t, d.Handle, "")
	if last["freshness"] != "unknown" || last["stale"] != true {
		t.Fatalf("fallback identity must be unknown: %v", last)
	}
}

// Enforced auth binds evidence to the stable Principal.ID: a rotated token for the
// same identity still reads, another principal is denied, a token scoped to another
// workspace is refused, and a handle does not resolve in another workspace.
func TestEvidencePrincipalBindingAndTokenRotation(t *testing.T) {
	f := newEvidenceFixture(t, true)
	alice1, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	bob, _ := workspaceops.MintToken(f.dir, "bob", "agent")
	code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence?tool=search&call_id=c1&issuer=pi", alice1, bytes.NewReader(f.big), nil)
	var d evidence.Delivery
	_ = json.Unmarshal(b, &d)
	if code != 200 || d.Handle == "" {
		t.Fatalf("pi capture: %d %s", code, b[:min(300, len(b))])
	}
	alice2, _ := workspaceops.MintToken(f.dir, "alice", "agent") // rotation: new secret, same ID
	if got, _ := f.expandAll(t, d.Handle, alice2); !bytes.Equal(got, f.big) {
		t.Fatalf("rotated token for the same principal must read the exact original")
	}
	path := "/api/workspaces/" + f.ws + "/evidence/" + d.Handle
	if code, _, _ := f.do(t, "GET", path, bob, nil, nil); code != http.StatusForbidden {
		t.Fatalf("other principal: want 403, got %d", code)
	}
	if code, _, _ := f.do(t, "GET", path, "", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: want 401, got %d", code)
	}
	if code, _, _ := f.do(t, "GET", "/api/workspaces/otherWs/evidence/"+d.Handle, alice2, nil, nil); code != http.StatusNotFound {
		t.Fatalf("cross-workspace: want 404, got %d", code)
	}
	tampered := d.Handle[:len(d.Handle)-3] + "abc"
	if code, _, _ := f.do(t, "GET", "/api/workspaces/"+f.ws+"/evidence/"+tampered, alice2, nil, nil); code != http.StatusNotFound {
		t.Fatalf("tampered handle: want 404, got %d", code)
	}
	if code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence?tool=bash", alice2, strings.NewReader("x"), nil); code != 400 {
		t.Fatalf("non-xMustard tool must be rejected: %d %s", code, b)
	}
}

func TestEvidenceExpiryRestartAndQuota(t *testing.T) {
	t.Setenv("XMUSTARD_EVIDENCE_RETENTION_SECONDS", "2")
	f := newEvidenceFixture(t, true)
	_, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence?tool=search", "", bytes.NewReader(f.big), nil)
	var d evidence.Delivery
	_ = json.Unmarshal(b, &d)
	// restart: a fresh handler stack over the same data dir still serves the handle
	f.srv.Close()
	f.srv = httptest.NewServer(bodyLimitMiddleware(authMiddleware(f.dir, "auto", newAPIHandler())))
	if got, _ := f.expandAll(t, d.Handle, ""); !bytes.Equal(got, f.big) {
		t.Fatalf("original not readable after restart")
	}
	time.Sleep(2100 * time.Millisecond)
	code, body, _ := f.do(t, "GET", "/api/workspaces/"+f.ws+"/evidence/"+d.Handle, "", nil, nil)
	if code != http.StatusGone || !strings.Contains(string(body), "expired") {
		t.Fatalf("after expiry: want 410 expired, got %d %s", code, body)
	}

	t.Setenv("XMUSTARD_EVIDENCE_WORKSPACE_QUOTA_BYTES", fmt.Sprint(len(f.big)+100))
	q := newEvidenceFixture(t, true)
	if code, _, _ := q.do(t, "POST", "/api/workspaces/"+q.ws+"/evidence?tool=search", "", bytes.NewReader(q.big), nil); code != 200 {
		t.Fatalf("first capture: %d", code)
	}
	code, body, _ = q.do(t, "POST", "/api/workspaces/"+q.ws+"/evidence?tool=search", "", bytes.NewReader(q.big), nil)
	if code != http.StatusInsufficientStorage || !strings.Contains(string(body), "quota_full") {
		t.Fatalf("full quota: want 507 quota_full, got %d %s", code, body)
	}
}

// Plan workload: five concurrent 16 MiB capture attempts against the 64 MiB transient
// pool. Every body is reserved before it is read, so at least one attempt is refused
// with 503 while the others are in flight, and in-use bytes never exceed the pool.
func TestFiveConcurrent16MiBCapturesAgainst64MiBPool(t *testing.T) {
	prev := budget.TransientBytes
	pool := budget.NewByteBudget(64 << 20)
	budget.TransientBytes = pool
	defer func() { budget.TransientBytes = prev }()
	f := newEvidenceFixture(t, true)
	const size = 16<<20 - 1024
	payload := bytes.Repeat([]byte("line of tool output ok\n"), size/23)
	release := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pr, pw := io.Pipe()
			go func() {
				// send a first chunk, then hold the rest until one attempt is refused
				_, _ = pw.Write(payload[:1<<20])
				<-release
				_, _ = pw.Write(payload[1<<20:])
				pw.Close()
			}()
			req, _ := http.NewRequestWithContext(context.Background(), "POST", f.srv.URL+"/api/workspaces/"+f.ws+"/evidence?tool=search", pr)
			req.ContentLength = int64(len(payload))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				codes[i] = -1
				once.Do(func() { close(release) })
				return
			}
			resp.Body.Close()
			codes[i] = resp.StatusCode
			once.Do(func() { close(release) })
		}(i)
	}
	go func() { time.Sleep(10 * time.Second); once.Do(func() { close(release) }) }()
	wg.Wait()
	refused, ok := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusServiceUnavailable:
			refused++
		case http.StatusOK:
			ok++
		default:
			t.Fatalf("every capture attempt must be admitted (200) or refused (503), got %v", codes)
		}
	}
	if refused < 1 || ok < 1 {
		t.Fatalf("want at least one refusal and one success, codes=%v", codes)
	}
	if pool.Peak() > pool.Max() || pool.InUse() != 0 {
		t.Fatalf("pool peak %d (max %d), in use after %d", pool.Peak(), pool.Max(), pool.InUse())
	}
	t.Logf("codes=%v pool peak=%d max=%d", codes, pool.Peak(), pool.Max())
}

// A capture whose request is cancelled mid-stream retains nothing and releases its
// reservation.
func TestCancelledCaptureRetainsNothing(t *testing.T) {
	prev := budget.TransientBytes
	pool := budget.NewByteBudget(64 << 20)
	budget.TransientBytes = pool
	defer func() { budget.TransientBytes = prev }()
	f := newEvidenceFixture(t, true)
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", f.srv.URL+"/api/workspaces/"+f.ws+"/evidence?tool=search", pr)
	req.ContentLength = 8 << 20
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	_, _ = pw.Write(bytes.Repeat([]byte("x"), 1<<20))
	cancel()
	pw.CloseWithError(context.Canceled)
	<-done
	deadline := time.Now().Add(3 * time.Second)
	for pool.InUse() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pool.InUse() != 0 {
		t.Fatalf("cancelled capture leaked %d reserved bytes", pool.InUse())
	}
	ents, _ := os.ReadDir(filepath.Join(f.dir, "evidence", f.ws))
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("cancelled capture retained %s", e.Name())
		}
		if strings.HasPrefix(e.Name(), ".spool-") {
			t.Fatalf("cancelled capture left spool %s", e.Name())
		}
	}
}

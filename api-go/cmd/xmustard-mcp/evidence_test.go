package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"testing"
	"xmustard/api-go/internal/budget"
)

// fakeEvidenceAPI mimics the API's evidence contract (evidence_routes.go).
func fakeEvidenceAPI(t *testing.T, seen *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/search"):
			*seen = r.Header.Clone()
			if r.Header.Get(deliveryHeader) != deliveryVersion {
				_, _ = w.Write([]byte(`{"hits":"raw"}`))
				return
			}
			w.Header().Set(deliveryHeader, deliveryVersion)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"delivery": deliveryVersion, "tool": "search", "call_id": r.Header.Get("X-Xmustard-Call-Id"),
				"status": 200, "is_error": false, "reduced": true, "projection": `{"hits":[{"path":"a.go"}]}`,
				"handle": "xm1.H", "resource_uri": resourceScheme + "xm1.H?workspace_id=ws1", "raw_bytes": 900000,
				"projected_bytes": 25, "expires_at": "2026-09-25T00:00:00Z", "captured_identity": "bound",
				"omissions": []any{map[string]any{"kind": "array_items"}},
			})
		case strings.HasSuffix(r.URL.Path, "/session-grounding"):
			w.Header().Set(deliveryHeader, deliveryVersion)
			_ = json.NewEncoder(w).Encode(map[string]any{"tool": "ground", "status": 404, "is_error": true, "reduced": false, "projection": `{"error":"Missing resource"}`})
		case strings.Contains(r.URL.Path, "/evidence/xm1.H"):
			_ = json.NewEncoder(w).Encode(map[string]any{"handle": "xm1.H", "offset": 0, "length": 3, "total_bytes": 3,
				"eof": true, "encoding": "base64", "data": "YWJj", "content_type": "application/json", "freshness": "stale", "stale": true})
		case strings.Contains(r.URL.Path, "/evidence/xm1.GONE"):
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"error":"evidence expired","reason":"expired"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInitializeAdvertisesResources(t *testing.T) {
	res, _ := dispatch("initialize", nil)
	caps := res.(map[string]any)["capabilities"].(map[string]any)
	if _, ok := caps["resources"]; !ok {
		t.Fatalf("resources capability not advertised: %v", caps)
	}
}

func TestToolCallDeliversProjectionAndResource(t *testing.T) {
	var seen http.Header
	srv := fakeEvidenceAPI(t, &seen)
	t.Setenv("XMUSTARD_API_BASE", srv.URL)
	ctx := withCallID(context.Background(), json.RawMessage(`17`))
	res, rerr := dispatchCtx(ctx, "tools/call", json.RawMessage(`{"name":"search","arguments":{"workspace_id":"ws1","query":"q"}}`))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if seen.Get(deliveryHeader) != deliveryVersion || seen.Get("X-Xmustard-Call-Id") != "17" || seen.Get("X-Xmustard-Issuer") != "mcp" || seen.Get("X-Xmustard-Session-Id") == "" {
		t.Fatalf("delivery headers not sent: %v", seen)
	}
	m := res.(map[string]any)
	content := m["content"].([]map[string]any)
	if m["isError"] != false || content[0]["text"] != `{"hits":[{"path":"a.go"}]}` || !strings.Contains(content[1]["text"].(string), resourceScheme+"xm1.H") {
		t.Fatalf("projection/expansion note wrong: %v", m)
	}
	if m["_meta"].(map[string]any)["xmustard/evidence"].(map[string]any)["handle"] != "xm1.H" {
		t.Fatalf("_meta missing handle")
	}
	list := resourcesListResult()["resources"].([]map[string]any)
	if len(list) == 0 || list[0]["uri"] != resourceScheme+"xm1.H?workspace_id=ws1" {
		t.Fatalf("issued handle not listed: %v", list)
	}
	// the session knows the workspace: the bare URI pages the original
	page, rerr := dispatchCtx(ctx, "resources/read", json.RawMessage(`{"uri":"xmustard://evidence/xm1.H?offset=0&length=65536"}`))
	if rerr != nil {
		t.Fatal(rerr)
	}
	c := page.(map[string]any)["contents"].([]map[string]any)[0]
	meta := page.(map[string]any)["_meta"].(map[string]any)["xmustard/page"].(map[string]any)
	if c["blob"] != "YWJj" || meta["freshness"] != "stale" || meta["stale"] != true {
		t.Fatalf("page: %v %v", c, meta)
	}
}

func TestDeliveredErrorStaysError(t *testing.T) {
	var seen http.Header
	srv := fakeEvidenceAPI(t, &seen)
	t.Setenv("XMUSTARD_API_BASE", srv.URL)
	res, _ := dispatch("tools/call", json.RawMessage(`{"name":"ground","arguments":{"workspace_id":"missing"}}`))
	if res.(map[string]any)["isError"] != true {
		t.Fatalf("error must stay an error: %v", res)
	}
}

func TestResourceReadErrorsAreProtocolErrors(t *testing.T) {
	var seen http.Header
	srv := fakeEvidenceAPI(t, &seen)
	t.Setenv("XMUSTARD_API_BASE", srv.URL)
	cases := map[string]int{
		`{"uri":"xmustard://evidence/xm1.UNKNOWN"}`:                          resourceNotFound, // not issued, no workspace
		`{"uri":"xmustard://evidence/xm1.GONE?workspace_id=ws1"}`:            resourceNotFound, // expired
		`{"uri":"file:///etc/passwd"}`:                                       resourceNotFound,
		`{"uri":"xmustard://evidence/xm1.H?workspace_id=ws1&length=999999"}`: -32602,
		`{}`: -32602,
	}
	for params, want := range cases {
		_, rerr := dispatch("resources/read", json.RawMessage(params))
		if rerr == nil || rerr.Code != want {
			t.Fatalf("%s: want %d, got %+v", params, want, rerr)
		}
	}
}

// Fable evidence F7 (shim): decoding the envelope and encoding the reply copy the
// projection again, so those copies are admitted before they are built. When other calls
// hold the pool the call is refused with the retryable -32000; when the copies could
// never fit the whole pool it gets a permanent tool error instead of an overload that an
// idle shim would repeat forever. Neither answer is built from unreserved memory.
func TestShimResponseConstructionIsAdmitted(t *testing.T) {
	big := strings.Repeat("p", 200<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(deliveryHeader, deliveryVersion)
		_ = json.NewEncoder(w).Encode(map[string]any{"tool": "search", "status": 200, "projection": big})
	}))
	defer srv.Close()
	t.Setenv("XMUSTARD_API_BASE", srv.URL)
	prev := budget.TransientBytes
	defer func() { budget.TransientBytes = prev }()
	call := func() (map[string]any, *rpcError) {
		scope := budget.NewScope(nil)
		defer scope.Close()
		ctx := budget.WithScope(context.Background(), scope)
		res, rerr := dispatchCtx(ctx, "tools/call", json.RawMessage(`{"name":"search","arguments":{"workspace_id":"ws","query":"q"}}`))
		m, _ := res.(map[string]any)
		return m, rerr
	}

	budget.TransientBytes = budget.NewByteBudget(2 << 20)
	other := budget.NewScope(nil)
	if err := other.Acquire(1600 << 10); err != nil {
		t.Fatal(err)
	}
	if _, rerr := call(); rerr == nil || rerr.Code != overloadCode {
		t.Fatalf("reply copies while another call holds the pool: want -32000, got %+v", rerr)
	}
	other.Close()
	if res, rerr := call(); rerr != nil || res["isError"] == true {
		t.Fatalf("the same call on an idle 2 MiB pool must succeed: %v %+v", res, rerr)
	}

	budget.TransientBytes = budget.NewByteBudget(300 << 10)
	res, rerr := call()
	text, _ := json.Marshal(res)
	if rerr != nil || res["isError"] != true || !strings.Contains(string(text), "transient budget") || strings.Contains(string(text), big[:1024]) {
		t.Fatalf("reply copies larger than the whole pool: want a permanent tool error, got %.300s %+v", text, rerr)
	}
}

// Fable evidence F2 (shim): an envelope that carries an overload status is a protocol
// overload, not a tool error.
func TestShimMapsEnvelopeOverloadToProtocolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(deliveryHeader, deliveryVersion)
		_ = json.NewEncoder(w).Encode(map[string]any{"tool": "search", "status": 503, "is_error": true, "projection": `{"error":"xmustard overloaded","overloaded":true}`})
	}))
	defer srv.Close()
	t.Setenv("XMUSTARD_API_BASE", srv.URL)
	_, rerr := dispatch("tools/call", json.RawMessage(`{"name":"search","arguments":{"workspace_id":"ws","query":"q"}}`))
	if rerr == nil || rerr.Code != overloadCode {
		t.Fatalf("envelope overload: want -32000, got %+v", rerr)
	}
}

// On the default 24 MiB pool a reply is held as the API response plus the reply built
// from it. A 10 MiB response is relayed; a 13 MiB one (under the 16 MiB response cap)
// could never be held twice, so it is a permanent tool error, not a -32000 overload an
// idle shim would repeat forever.
func TestShimResponseSizesOnTheDefaultPool(t *testing.T) {
	size := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"pad":"` + strings.Repeat("r", size) + `"}`))
	}))
	defer srv.Close()
	t.Setenv("XMUSTARD_API_BASE", srv.URL)
	prev := budget.TransientBytes
	budget.TransientBytes = budget.NewByteBudget(budget.DefaultTransientBudgetBytes)
	defer func() { budget.TransientBytes = prev }()
	for _, tc := range []struct {
		size int
		ok   bool
	}{{10 << 20, true}, {13 << 20, false}} {
		size = tc.size
		scope := budget.NewScope(nil)
		res, rerr := dispatchCtx(budget.WithScope(context.Background(), scope), "tools/call",
			json.RawMessage(`{"name":"search","arguments":{"workspace_id":"ws","query":"q"}}`))
		scope.Close()
		m, _ := res.(map[string]any)
		text, _ := json.Marshal(m)
		if rerr != nil {
			t.Fatalf("%d-byte response: want a tool result, got JSON-RPC error %+v", tc.size, rerr)
		}
		if tc.ok && (m["isError"] == true || len(text) < tc.size) {
			t.Fatalf("%d-byte response fits the default pool: %.200s", tc.size, text)
		}
		if !tc.ok && (m["isError"] != true || !strings.Contains(string(text), "transient budget")) {
			t.Fatalf("%d-byte response can never be held twice in 24 MiB: want a permanent tool error, got %.200s", tc.size, text)
		}
		if budget.TransientBytes.InUse() != 0 {
			t.Fatalf("reservation leaked: %d", budget.TransientBytes.InUse())
		}
	}
}

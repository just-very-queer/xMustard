package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resources/read with pattern/query/lines searches the original through the API's
// search route (forwarding only the search parameters) and maps refusals like pages.
func TestResourceReadSearchesOriginal(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		switch r.URL.Query().Get("handle") {
		case "xm1.OK":
			_, _ = w.Write([]byte(`{"handle":"xm1.OK","matches":1,"match_cap_reached":false,"next_offset":90,"next_line":4,"eof":true,` +
				`"freshness":"unknown","lines":[{"line":3,"offset":40,"text":"--- FAIL: TestX","match":true}]}`))
		case "xm1.DENIED":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"evidence access denied","reason":"denied"}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid evidence search: pattern","reason":"invalid_search"}`))
		}
	}))
	defer srv.Close()
	t.Setenv("XMUSTARD_API_BASE", srv.URL)
	res, rerr := readResource(context.Background(), json.RawMessage(`{"uri":"xmustard://evidence/xm1.OK?workspace_id=ws1&pattern=FAIL&max_matches=5&length=10"}`))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if got.URL.Path != "/api/workspaces/ws1/evidence/search" || got.URL.Query().Get("pattern") != "FAIL" ||
		got.URL.Query().Get("max_matches") != "5" || got.URL.Query().Get("handle") != "xm1.OK" || got.URL.Query().Has("length") {
		t.Fatalf("forwarded request: %s", got.URL)
	}
	m := res.(map[string]any)
	c := m["contents"].([]map[string]any)[0]
	if c["mimeType"] != "application/json" || !strings.Contains(c["text"].(string), "--- FAIL: TestX") {
		t.Fatalf("contents: %v", c)
	}
	if meta := m["_meta"].(map[string]any)["xmustard/search"].(map[string]any); meta["matches"] != 1 || meta["next_line"] != 4 {
		t.Fatalf("meta: %v", meta)
	}
	if _, rerr := readResource(context.Background(), json.RawMessage(`{"uri":"xmustard://evidence/xm1.DENIED?workspace_id=ws1&lines=1-5"}`)); rerr == nil || rerr.Code != resourceNotFound {
		t.Fatalf("denied search must be resource-not-found: %+v", rerr)
	}
	if _, rerr := readResource(context.Background(), json.RawMessage(`{"uri":"xmustard://evidence/xm1.BAD?workspace_id=ws1&pattern=("}`)); rerr == nil || rerr.Code != -32602 {
		t.Fatalf("invalid search must be invalid params: %+v", rerr)
	}
	tmpl := resourceTemplatesResult()["resourceTemplates"].([]map[string]any)[0]["uriTemplate"].(string)
	if !strings.Contains(tmpl, "pattern") || !strings.Contains(tmpl, "lines") {
		t.Fatalf("template does not advertise search: %s", tmpl)
	}
}

func TestEvidenceMetaCarriesTokenEstimate(t *testing.T) {
	body := `{"tool":"search","status":200,"reduced":true,"projection":"{}","handle":"xm1.H","resource_uri":"xmustard://evidence/xm1.H?workspace_id=w",` +
		`"raw_bytes":900000,"projected_bytes":2,"delivered_tokens_est":321}`
	res, rerr := evidenceResult(context.Background(), body, "w")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if res["_meta"].(map[string]any)["xmustard/evidence"].(map[string]any)["delivered_tokens_est"] != 321 {
		t.Fatalf("meta: %v", res["_meta"])
	}
}

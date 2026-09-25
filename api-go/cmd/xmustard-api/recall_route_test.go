package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// newRouteServer serves the real route table (plus auth middleware) over a temp data dir.
func newRouteServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	srv := httptest.NewServer(bodyLimitMiddleware(authMiddleware(dir, "auto", newAPIHandler())))
	t.Cleanup(srv.Close)
	return srv, dir
}

// seedRecallWorkspace promotes n single-agent memories in a workspace with a real
// repo root; entry i mentions "topic<i>" and the last one references hot.go.
func seedRecallWorkspace(t *testing.T, dir, ws string, n int) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hot.go"), []byte("package hot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": root}}
	b, _ := json.Marshal(snap)
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", ws, "snapshot.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	off := false
	settings, _ := json.Marshal(map[string]any{"require_multi_agent_verification": off})
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), settings, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		req := workspaceops.ProposeContextRequest{
			Title: fmt.Sprintf("fact %d", i), Content: fmt.Sprintf("topic%d detail", i), Source: "solo",
		}
		if i == n-1 {
			req.Paths = []string{"hot.go"}
		}
		if _, err := workspaceops.ProposeContext(dir, ws, req); err != nil {
			t.Fatal(err)
		}
	}
}

func getJSON(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func entryCount(m map[string]any) int {
	list, _ := m["entries"].([]any)
	return len(list)
}

// Audit Go #1: plain `recall` (GET /context/active with no query/paths) must use the
// bounded ranked path, not dump every promoted entry.
func TestRecallRouteNoArgumentIsBounded(t *testing.T) {
	srv, dir := newRouteServer(t)
	seedRecallWorkspace(t, dir, "wsBig", 40)

	code, body := getJSON(t, srv.URL+"/api/workspaces/wsBig/context/active", "")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if n := entryCount(body); n != 8 {
		t.Fatalf("plain recall returned %d entries of 40; want bounded top-8", n)
	}
	if body["total_active"] != float64(40) || body["bounded"] != true {
		t.Fatalf("expected total_active=40 bounded=true, got total_active=%v bounded=%v", body["total_active"], body["bounded"])
	}
	if dc, _ := body["drift_checked"].(float64); dc > 32 {
		t.Fatalf("drift check not bounded: %v", dc)
	}
}

func TestRecallRouteEmptyQueriedAndPathFiltered(t *testing.T) {
	srv, dir := newRouteServer(t)

	code, body := getJSON(t, srv.URL+"/api/workspaces/wsEmpty/context/active", "")
	if code != 200 || entryCount(body) != 0 {
		t.Fatalf("empty workspace recall: %d %v", code, body)
	}

	seedRecallWorkspace(t, dir, "wsQ", 20)
	_, q := getJSON(t, srv.URL+"/api/workspaces/wsQ/context/active?query=topic7", "")
	list := q["entries"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["content"] != "topic7 detail" {
		t.Fatalf("queried recall: %v", q)
	}
	_, p := getJSON(t, srv.URL+"/api/workspaces/wsQ/context/active?paths=hot.go", "")
	list = p["entries"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["content"] != "topic19 detail" {
		t.Fatalf("path-filtered recall: %v", p)
	}
}

// The full promoted history remains available only as an explicit administrative read.
func TestRecallRouteFullHistoryIsAdministrative(t *testing.T) {
	srv, dir := newRouteServer(t)
	seedRecallWorkspace(t, dir, "wsAdmin", 30)
	admin, err := workspaceops.MintToken(dir, "root", "admin")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := workspaceops.MintToken(dir, "worker", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := getJSON(t, srv.URL+"/api/workspaces/wsAdmin/context/active?scope=all", agent); code != http.StatusForbidden {
		t.Fatalf("agent full-history read: want 403, got %d", code)
	}
	code, body := getJSON(t, srv.URL+"/api/workspaces/wsAdmin/context/active?scope=all", admin)
	if code != 200 || entryCount(body) != 30 {
		t.Fatalf("admin full-history read: %d entries=%d", code, entryCount(body))
	}
	code, body = getJSON(t, srv.URL+"/api/workspaces/wsAdmin/context/active", agent)
	if code != 200 || entryCount(body) != 8 {
		t.Fatalf("agent plain recall: %d entries=%d", code, entryCount(body))
	}
}

// Fable review #2: the sibling list route exposed the complete history (every filter,
// including the default) to non-admin principals, bypassing the bounded recall.
func TestContextListRouteIsAdministrative(t *testing.T) {
	srv, dir := newRouteServer(t)
	seedRecallWorkspace(t, dir, "wsList", 12)
	admin, _ := workspaceops.MintToken(dir, "root", "admin")
	agent, _ := workspaceops.MintToken(dir, "worker", "agent")
	reader, _ := workspaceops.MintToken(dir, "viewer", "readonly")
	for _, filter := range []string{"", "all", "promoted", "active", "verified", "pending", "rejected"} {
		for _, tok := range []string{agent, reader} {
			if code, _ := getJSON(t, srv.URL+"/api/workspaces/wsList/context?filter="+filter, tok); code != http.StatusForbidden {
				t.Fatalf("non-admin GET /context?filter=%q: want 403, got %d", filter, code)
			}
		}
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/workspaces/wsList/context?filter=promoted", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list []any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(list) != 12 {
		t.Fatalf("admin list: %d entries=%d", resp.StatusCode, len(list))
	}
	// POST remember stays an agent action.
	preq, _ := http.NewRequest("POST", srv.URL+"/api/workspaces/wsList/context", strings.NewReader(`{"content":"new fact"}`))
	preq.Header.Set("Authorization", "Bearer "+agent)
	presp, err := http.DefaultClient.Do(preq)
	if err != nil {
		t.Fatal(err)
	}
	presp.Body.Close()
	if presp.StatusCode != 200 {
		t.Fatalf("agent remember: %d", presp.StatusCode)
	}
}

// Fable review #3: an excessive limit is clamped to the maximum, not silently reset.
func TestRecallRouteClampsExcessiveLimit(t *testing.T) {
	srv, dir := newRouteServer(t)
	seedRecallWorkspace(t, dir, "wsClamp", 60)
	_, body := getJSON(t, srv.URL+"/api/workspaces/wsClamp/context/active?limit=500", "")
	if n := entryCount(body); n != maxRecallLimit || body["limit"] != float64(maxRecallLimit) {
		t.Fatalf("limit=500 returned %d entries (limit=%v); want clamp to %d", n, body["limit"], maxRecallLimit)
	}
}

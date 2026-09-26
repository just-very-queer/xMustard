package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// WS-19B over HTTP: tokens minted with an owner, the transport's session and tool-call
// ids bound to the write, the owner-distinct policy answering 403, and fetch-by-id
// showing provenance, verification basis and feedback.
func TestMemoryProvenanceOverHTTP(t *testing.T) {
	srv, dir := newRouteServer(t)
	settings := `{"require_multi_agent_verification":true,"context_verification_threshold":2,"principal_distinctness":"owner"}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := workspaceops.MintToken(dir, "root", "admin")
	if err != nil {
		t.Fatal(err)
	}
	tok := map[string]string{}
	for id, owner := range map[string]string{"alice-1": "alice", "alice-2": "alice", "bob": "", "carol": ""} {
		code, out := sendJSON(t, "POST", srv.URL+"/api/auth/tokens", root, `{"id":"`+id+`","role":"agent","owner":"`+owner+`"}`)
		if code != http.StatusOK {
			t.Fatalf("mint %s: %d %v", id, code, out)
		}
		tok[id] = out["token"].(string)
	}
	if code, _ := sendJSON(t, "POST", srv.URL+"/api/auth/tokens", root, `{"id":"x","role":"agent","kind":"robot"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown kind minted: %d", code)
	}

	base := srv.URL + "/api/workspaces/wsProv"
	send := func(who, method, path, body string, headers map[string]string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok[who])
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != want {
			t.Fatalf("%s %s as %s: want %d, got %d %v", method, path, who, want, resp.StatusCode, out)
		}
		return out
	}
	mcp := map[string]string{"X-Xmustard-Session-Id": "mcp-abc", "X-Xmustard-Call-Id": `"call-1"`}
	e := send("alice-1", "POST", "/context", `{"content":"listen on 8042"}`, mcp, http.StatusOK)
	id := e["id"].(string)
	// a long JSON-RPC id is recorded as a bounded label, not refused; control characters are refused
	long := send("alice-1", "POST", "/context", `{"content":"long ids"}`, map[string]string{"X-Xmustard-Call-Id": `"` + strings.Repeat("c", 300) + `"`}, http.StatusOK)
	longProv := send("alice-1", "GET", "/context/active?entry_id="+long["id"].(string), "", nil, http.StatusOK)["provenance"].(map[string]any)
	if cid, _ := longProv["derived_from"].(map[string]any)["call_id"].(string); len(cid) > 128 || !strings.Contains(cid, "~sha256:") {
		t.Fatalf("long call id not bounded: %q", cid)
	}
	send("alice-1", "POST", "/context", `{"content":"x"}`, map[string]string{"X-Xmustard-Session-Id": "a\tb"}, http.StatusBadRequest)

	// alice-2 is a distinct token of the same owner: refused under the owner policy
	if out := send("alice-2", "POST", "/context/"+id+"/verify", `{"outcome":"approve"}`, nil, http.StatusForbidden); !strings.Contains(out["error"].(string), "owner-distinct") {
		t.Fatalf("same-owner refusal: %v", out)
	}
	send("bob", "POST", "/context/"+id+"/verify", `{"outcome":"approve","evidence_handle":"ev_missing"}`, nil, http.StatusBadRequest)
	send("bob", "POST", "/context/"+id+"/verify", `{"outcome":"approve"}`, nil, http.StatusOK)
	if v := send("carol", "POST", "/context/"+id+"/verify", `{"outcome":"approve"}`, nil, http.StatusOK); v["promoted"] != true {
		t.Fatalf("distinct owners did not promote: %v", v)
	}
	send("alice-2", "POST", "/context/"+id+"/verify", `{"outcome":"helpful","note":"it was right"}`, nil, http.StatusOK)

	got := send("bob", "GET", "/context/active?entry_id="+id, "", nil, http.StatusOK)
	prov := got["provenance"].(map[string]any)
	if prov["session_id"] != "mcp-abc" || prov["source_owner"] != "alice" ||
		prov["derived_from"].(map[string]any)["call_id"] != `"call-1"` {
		t.Fatalf("provenance: %v", prov)
	}
	if basis := got["verification_basis"].([]any); len(basis) != 2 || basis[0].(map[string]any)["kind"] != "agent" {
		t.Fatalf("verification basis: %v", basis)
	}
	if fb := got["feedback"].(map[string]any); fb["helpful"] != float64(1) {
		t.Fatalf("feedback: %v", fb)
	}
}

// An unknown principal_distinctness is the operator's typo: 400, not a server fault.
func TestSettingsRejectsUnknownDistinctnessAsBadRequest(t *testing.T) {
	t.Setenv("XMUSTARD_PROFILE", "platform")
	srv, dir := newRouteServer(t)
	root, err := workspaceops.MintToken(dir, "root", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if code, out := sendJSON(t, "POST", srv.URL+"/api/settings", root, `{"principal_distinctness":"team"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown principal_distinctness: %d %v", code, out)
	}
}

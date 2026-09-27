package main

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// The verification queue is a reviewer's read over HTTP: recall(status=pending |
// awaiting_me, include_pending) needs the verifier or human-approver role, labels each
// entry unverified with votes_needed, and awaiting_me leaves out the caller's own.
func TestRecallVerificationQueueOverHTTP(t *testing.T) {
	srv, dir := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsQueueHTTP"
	tok := map[string]string{}
	for id, role := range map[string]string{"alice": "agent", "bob": "agent", "rita": "reader"} {
		raw, err := workspaceops.MintToken(dir, id, role)
		if err != nil {
			t.Fatal(err)
		}
		tok[id] = raw
	}
	call := func(who, method, path, body string, want int) map[string]any {
		t.Helper()
		code, out := sendJSON(t, method, base+path, tok[who], body)
		if code != want {
			t.Fatalf("%s %s as %s: want %d, got %d %v", method, path, who, want, code, out)
		}
		return out
	}
	e := call("alice", "POST", "/context", `{"content":"the queue drains nightly","kind":"decision","tags":["ops"]}`, http.StatusOK)
	id := e["id"].(string)
	for _, q := range []string{"?status=awaiting_me", "?status=pending", "?include_pending=true"} {
		call("rita", "GET", "/context/active"+q, "", http.StatusForbidden)
	}
	if got := call("rita", "GET", "/context/active?query=queue", "", http.StatusOK); got["returned"] != float64(0) {
		t.Fatalf("default recall must be verified-only: %v", got)
	}
	got := call("bob", "GET", "/context/active?status=awaiting_me&kind=decision&tags=ops,other", "", http.StatusOK)
	entries := got["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("awaiting_me for bob: %v", got)
	}
	if en := entries[0].(map[string]any); en["id"] != id || en["trust"] != "unverified" || en["votes_needed"] != float64(2) || en["state"] != "pending" {
		t.Fatalf("pending label: %v", en)
	}
	if got := call("alice", "GET", "/context/active?status=awaiting_me", "", http.StatusOK); got["returned"] != float64(0) {
		t.Fatalf("awaiting_me must leave out the caller's own entries: %v", got)
	}
	call("bob", "GET", "/context/active?max_chars=5", "", http.StatusBadRequest)
	call("bob", "GET", "/context/active?max_chars=x", "", http.StatusBadRequest)
	call("bob", "GET", "/context/active?status=everything", "", http.StatusBadRequest)
	call("bob", "GET", "/context/active?session_id="+strings.Repeat("s", workspaceops.RecallMaxSessionID+1), "", http.StatusBadRequest)
	call("bob", "GET", "/context/active?session_id="+strings.Repeat("s", workspaceops.RecallMaxSessionID), "", http.StatusOK)

	// XMUSTARD_AUTH=off while tokens exist: the anonymous caller is a reader, so the
	// unverified reads fail closed (401) and verified recall still works
	off := httptest.NewServer(bodyLimitMiddleware(newAPIHandler())) // buildHandler skips auth when off
	t.Cleanup(off.Close)
	for _, q := range []string{"?status=pending", "?status=awaiting_me", "?include_pending=true", "?entry_id=" + id + "&history=true"} {
		if code, out := sendJSON(t, "GET", off.URL+"/api/workspaces/wsQueueHTTP/context/active"+q, "", ""); code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: want 401, got %d %v", q, code, out)
		}
	}
	if code, out := sendJSON(t, "GET", off.URL+"/api/workspaces/wsQueueHTTP/context/active?query=queue", "", ""); code != http.StatusOK {
		t.Fatalf("anonymous verified recall: %d %v", code, out)
	}
}

// include_superseded serves a superseded entry only if it was once promoted: a pending
// or rejected proposal that a promoted entry replaced is still unverified text, and a
// reader never receives it (review round 2, M1).
func TestRecallIncludeSupersededNeverServesUnverifiedText(t *testing.T) {
	srv, dir := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsSupersededHTTP"
	tok := map[string]string{}
	for id, role := range map[string]string{"alice": "agent", "bob": "agent", "carol": "agent", "rita": "reader"} {
		raw, err := workspaceops.MintToken(dir, id, role)
		if err != nil {
			t.Fatal(err)
		}
		tok[id] = raw
	}
	call := func(who, method, path, body string) map[string]any {
		t.Helper()
		code, out := sendJSON(t, method, base+path, tok[who], body)
		if code != http.StatusOK {
			t.Fatalf("%s %s as %s: want 200, got %d %v", method, path, who, code, out)
		}
		return out
	}
	propose := func(body string) string { return call("alice", "POST", "/context", body)["id"].(string) }
	settle := func(id, outcome string) {
		for _, peer := range []string{"bob", "carol"} {
			call(peer, "POST", "/context/"+id+"/verify", `{"outcome":"`+outcome+`"}`)
		}
	}
	pending := propose(`{"content":"UNVERIFIED secret claim zebra"}`)
	rejected := propose(`{"content":"REJECTED wrong claim zebra"}`)
	settle(rejected, "reject")
	served := propose(`{"content":"VERIFIED earlier claim zebra"}`)
	settle(served, "approve")
	replacement := propose(`{"content":"current claim zebra","supersedes":["` + pending + `","` + rejected + `","` + served + `"]}`)
	settle(replacement, "approve")

	got := call("rita", "GET", "/context/active?include_superseded=true&query=zebra", "")
	states := map[string]string{}
	for _, e := range got["entries"].([]any) {
		en := e.(map[string]any)
		states[en["id"].(string)] = en["state"].(string)
		if strings.Contains(en["content"].(string), "UNVERIFIED") || strings.Contains(en["content"].(string), "REJECTED") {
			t.Errorf("a reader received never-promoted text: %v", en)
		}
	}
	if want := map[string]string{replacement: "served", served: "superseded"}; !maps.Equal(states, want) {
		t.Fatalf("include_superseded as a reader: states %v, want %v (pending %s, rejected %s)", states, want, pending, rejected)
	}
}

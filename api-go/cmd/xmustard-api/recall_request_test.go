package main

import (
	"net/http"
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
}

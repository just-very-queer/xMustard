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
	for _, q := range []string{"?status=awaiting_me", "?status=pending", "?include_pending=true", "?include_merged=true"} {
		call("rita", "GET", "/context/active"+q, "", http.StatusForbidden)
	}
	call("bob", "GET", "/context/active?include_merged=true&subject=queue&predicate=runs_with", "", http.StatusOK)
	call("bob", "GET", "/context/active?predicate=runs_with", "", http.StatusBadRequest)
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
	// WS-28: as_of names a time or a commit of the repository, valid_only keeps open intervals
	call("bob", "GET", "/context/active?as_of=not-a-commit", "", http.StatusBadRequest)
	call("bob", "GET", "/context/active?as_of=2020-01-01&valid_only=true", "", http.StatusOK)
	call("bob", "GET", "/context/active?as_of="+strings.Repeat("a", 300), "", http.StatusBadRequest)
	call("bob", "GET", "/context/active?session_id="+strings.Repeat("s", workspaceops.RecallMaxSessionID+1), "", http.StatusBadRequest)
	call("bob", "GET", "/context/active?session_id="+strings.Repeat("s", workspaceops.RecallMaxSessionID), "", http.StatusOK)

	// XMUSTARD_AUTH=off while tokens exist: the anonymous caller is a reader, so the
	// unverified reads fail closed (401) and verified recall still works
	off := httptest.NewServer(bodyLimitMiddleware(newAPIHandler())) // buildHandler skips auth when off
	t.Cleanup(off.Close)
	for _, q := range []string{"?status=pending", "?status=awaiting_me", "?include_pending=true", "?include_merged=true", "?entry_id=" + id + "&history=true"} {
		if code, out := sendJSON(t, "GET", off.URL+"/api/workspaces/wsQueueHTTP/context/active"+q, "", ""); code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: want 401, got %d %v", q, code, out)
		}
	}
	if code, out := sendJSON(t, "GET", off.URL+"/api/workspaces/wsQueueHTTP/context/active?query=queue", "", ""); code != http.StatusOK {
		t.Fatalf("anonymous verified recall: %d %v", code, out)
	}
}

// entryList reads a response's entries, treating an absent field as none: a recall
// that served nothing omits the key rather than returning an empty array, and an
// assertion that indexes it would panic instead of reporting the miss.
func entryList(t *testing.T, got map[string]any) []map[string]any {
	t.Helper()
	raw, _ := got["entries"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("recall entry is not an object: %v", e)
		}
		out = append(out, m)
	}
	return out
}

// A private memory proposed over HTTP is served to its author and to nobody else on
// any of the three read paths, admin included, and the by-id refusal is the
// not-found shape. The reader's own keys reach the by-id path as query parameters,
// so the author reads a directory-scoped memory from inside its directory (WS-29b).
func TestAPrivateMemoryIsNotDisclosedOnAnyHTTPReadPath(t *testing.T) {
	srv, dir := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsPrivateHTTP"
	tok := map[string]string{}
	for id, role := range map[string]string{"alice": "agent", "bob": "agent", "op": "admin"} {
		raw, err := workspaceops.MintToken(dir, id, role)
		if err != nil {
			t.Fatal(err)
		}
		tok[id] = raw
	}
	call := func(who, method, path, body string) (int, map[string]any) {
		t.Helper()
		return sendJSON(t, method, base+path, tok[who], body)
	}
	code, out := call("alice", "POST", "/context",
		`{"content":"the staging credentials live in vault omega","visibility":"private"}`)
	if code != http.StatusOK {
		t.Fatalf("propose: %d %v", code, out)
	}
	id := out["id"].(string)
	if out["scope"] != "private" {
		t.Fatalf("the stored scope must be reported as private: %v", out)
	}
	// ranked recall
	for _, who := range []string{"bob", "op"} {
		code, got := call(who, "GET", "/context/active?query=omega", "")
		if code != http.StatusOK {
			t.Fatalf("recall as %s: %d %v", who, code, got)
		}
		for _, e := range entryList(t, got) {
			if e["id"] == id {
				t.Errorf("%s was served another principal's private memory over ranked recall", who)
			}
		}
	}
	// by id, including the reviewer and admin paths
	for _, who := range []string{"bob", "op"} {
		code, got := call(who, "GET", "/context/active?entry_id="+id, "")
		if code != http.StatusNotFound {
			t.Errorf("by-id fetch as %s: want 404, got %d %v", who, code, got)
		}
		if code, got = call(who, "GET", "/context/active?entry_id="+id+"&history=true", ""); code != http.StatusOK &&
			code != http.StatusNotFound {
			t.Errorf("by-id history fetch as %s: %d %v", who, code, got)
		}
	}
	if code, got := call("alice", "GET", "/context/active?entry_id="+id, ""); code != http.StatusOK {
		t.Fatalf("the author must fetch its own private entry by id: %d %v", code, got)
	}
	// the listing paths
	for _, who := range []string{"bob", "op"} {
		for _, filter := range []string{"", "?filter=all", "?filter=promoted", "?filter=pending"} {
			code, got := call(who, "GET", "/context"+filter, "")
			if code != http.StatusOK {
				continue // the admin-only listing refuses a non-admin reader; that is its own gate
			}
			list := entryList(t, got)
			for _, e := range list {
				if e["id"] == id {
					t.Errorf("%s was served the private memory through the listing %q", who, filter)
				}
			}
		}
	}
	// the author's keys reach the by-id path, so a directory-scoped memory is
	// readable inside its directory and refused outside it
	code, out = call("alice", "POST", "/context",
		`{"content":"the resolver drops unexported edges omega","visibility":"dir","dir":"api-go/internal"}`)
	if code != http.StatusOK || out["scope_key"] != "api-go/internal" {
		t.Fatalf("propose a dir-scoped memory: %d %v", code, out)
	}
	dirID := out["id"].(string)
	for _, c := range []struct {
		query string
		want  int
	}{
		{"?entry_id=" + dirID + "&dir=api-go/internal", http.StatusOK},
		{"?entry_id=" + dirID + "&dir=api-go/internal/govstore", http.StatusOK},
		{"?entry_id=" + dirID + "&dir=api-go/cmd", http.StatusNotFound},
		{"?entry_id=" + dirID, http.StatusNotFound},
	} {
		if code, got := call("alice", "GET", "/context/active"+c.query, ""); code != c.want {
			t.Errorf("author reading its dir-scoped entry %q: want %d, got %d %v", c.query, c.want, code, got)
		}
	}
	// The same key has to reach RANKED recall, not only the by-id path: a parser that
	// read dir for one route and not the other would leave a dir-scoped memory
	// unreachable from the tool an agent actually calls.
	for _, c := range []struct {
		query string
		want  int
	}{
		{"?query=omega&dir=api-go/internal", http.StatusOK},
		{"?query=omega&dir=api-go/cmd", http.StatusOK},
	} {
		code, got := call("alice", "GET", "/context/active"+c.query, "")
		if code != c.want {
			t.Errorf("ranked recall %q: want %d, got %d %v", c.query, c.want, code, got)
		}
		served := false
		for _, e := range entryList(t, got) {
			if e["id"] == dirID {
				served = true
			}
		}
		if want := c.query == "?query=omega&dir=api-go/internal"; served != want {
			t.Errorf("ranked recall %q served the dir-scoped entry: %v, want %v", c.query, served, want)
		}
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

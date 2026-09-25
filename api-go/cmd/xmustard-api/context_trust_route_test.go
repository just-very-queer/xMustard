package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// sendJSON issues method url with an optional raw body and bearer token and decodes a
// JSON object response.
func sendJSON(t *testing.T, method, url, token, body string) (int, map[string]any) {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	var req *http.Request
	if rd != nil {
		req, _ = http.NewRequest(method, url, rd)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, url, nil)
	}
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

// Open mode: with no tokens every caller is one identity, so the default 2-approval
// quorum (author excluded) can never form. remember must promote at once, labelled
// self-asserted, and recall must return it with that label.
func TestOpenModeRememberIsRecalledAsSelfAsserted(t *testing.T) {
	srv, _ := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsOpenRoute"

	code, e := sendJSON(t, "POST", base+"/context", "", `{"title":"api base","content":"the api base is /api"}`)
	if code != http.StatusOK {
		t.Fatalf("remember: %d %v", code, e)
	}
	if e["promoted"] != true || e["verification_mode"] != workspaceops.VerificationSelfAssertedOpen || e["source"] != workspaceops.OpenModeIdentity {
		t.Fatalf("open-mode remember must promote as self-asserted by %q, got %v", workspaceops.OpenModeIdentity, e)
	}

	code, rec := getJSON(t, base+"/context/active?query=api", "")
	if code != http.StatusOK || entryCount(rec) != 1 {
		t.Fatalf("recall after open-mode remember: %d %v", code, rec)
	}
	entry := rec["entries"].([]any)[0].(map[string]any)
	if entry["verification_mode"] != workspaceops.VerificationSelfAssertedOpen {
		t.Fatalf("recalled entry must carry its mode, got %v", entry)
	}
	modes, _ := rec["verification_modes"].(map[string]any)
	if modes[workspaceops.VerificationSelfAssertedOpen] != float64(1) || modes[workspaceops.VerificationPeer] != float64(0) {
		t.Fatalf("recall verification_modes = %v", rec["verification_modes"])
	}
}

// Authenticated mode needs distinct principals with the author excluded, and a client
// cannot claim open mode in the body.
func TestAuthenticatedRememberStillNeedsDistinctPeers(t *testing.T) {
	srv, dir := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsAuthRoute"
	tok := map[string]string{}
	for _, id := range []string{"alice", "bob", "carol"} {
		raw, err := workspaceops.MintToken(dir, id, "agent")
		if err != nil {
			t.Fatal(err)
		}
		tok[id] = raw
	}

	code, e := sendJSON(t, "POST", base+"/context", tok["alice"], `{"content":"quorum fact","OpenMode":true,"open_mode":true}`)
	if code != http.StatusOK || e["promoted"] != false || e["verification_mode"] != "" || e["source"] != "alice" {
		t.Fatalf("authenticated remember must stay pending, got %d %v", code, e)
	}
	id := e["id"].(string)
	verify := func(who string) map[string]any {
		code, v := sendJSON(t, "POST", base+"/context/"+id+"/verify?approve=true", tok[who], "")
		if code != http.StatusOK {
			t.Fatalf("verify by %s: %d %v", who, code, v)
		}
		return v
	}
	if v := verify("alice"); v["promoted"] != false {
		t.Fatalf("author self-approval must not count: %v", v)
	}
	if v := verify("bob"); v["promoted"] != false {
		t.Fatalf("one peer must not meet threshold 2: %v", v)
	}
	if v := verify("carol"); v["promoted"] != true || v["verification_mode"] != workspaceops.VerificationPeer {
		t.Fatalf("two distinct peers must promote as peer_verified: %v", v)
	}
	_, rec := getJSON(t, base+"/context/active?query=quorum", tok["bob"])
	if entryCount(rec) != 1 || rec["entries"].([]any)[0].(map[string]any)["verification_mode"] != workspaceops.VerificationPeer {
		t.Fatalf("recall must show the peer-verified mode: %v", rec)
	}
}

// An edit resets verification, so PUT context/{id} is limited to agents, and among
// them to the entry's author or an admin.
func TestContextEditRequiresAuthorOrAdmin(t *testing.T) {
	srv, dir := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsEditRoute"
	tok := map[string]string{}
	for id, role := range map[string]string{"alice": "agent", "mallory": "agent", "root": "admin", "viewer": "readonly"} {
		raw, err := workspaceops.MintToken(dir, id, role)
		if err != nil {
			t.Fatal(err)
		}
		tok[id] = raw
	}
	code, e := sendJSON(t, "POST", base+"/context", tok["alice"], `{"content":"v1","permission":"readwrite"}`)
	if code != http.StatusOK {
		t.Fatalf("remember: %d %v", code, e)
	}
	entryURL := base + "/context/" + e["id"].(string)

	for who, want := range map[string]int{"": http.StatusUnauthorized, "viewer": http.StatusForbidden, "mallory": http.StatusForbidden} {
		if code, body := sendJSON(t, "PUT", entryURL, tok[who], `{"content":"hijacked"}`); code != want {
			t.Fatalf("PUT as %q: want %d, got %d %v", who, want, code, body)
		}
	}
	code, got := sendJSON(t, "PUT", entryURL, tok["alice"], `{"content":"v2"}`)
	if code != http.StatusOK || got["content"] != "v2" || got["promoted"] != false || got["verification_mode"] != "" {
		t.Fatalf("author edit: %d %v", code, got)
	}
	if code, got = sendJSON(t, "PUT", entryURL, tok["root"], `{"content":"v3"}`); code != http.StatusOK || got["content"] != "v3" {
		t.Fatalf("admin edit: %d %v", code, got)
	}
}

// Open mode keeps edits usable for the single local identity, and the edit still
// resets verification; the same identity can then re-assert it.
func TestOpenModeEditResetsVerification(t *testing.T) {
	srv, _ := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsOpenEdit"
	_, e := sendJSON(t, "POST", base+"/context", "", `{"content":"v1","permission":"readwrite"}`)
	entryURL := base + "/context/" + e["id"].(string)

	code, got := sendJSON(t, "PUT", entryURL, "", `{"content":"v2"}`)
	if code != http.StatusOK || got["promoted"] != false || got["verification_mode"] != "" || len(got["verifications"].([]any)) != 0 {
		t.Fatalf("open-mode edit must succeed and reset verification: %d %v", code, got)
	}
	code, got = sendJSON(t, "POST", entryURL+"/verify?approve=true", "", "")
	if code != http.StatusOK || got["promoted"] != true || got["verification_mode"] != workspaceops.VerificationSelfAssertedOpen {
		t.Fatalf("open-mode re-assertion: %d %v", code, got)
	}
}

// A malformed JSON body is a 400 rather than a silent fall back to query params; an
// empty body still falls back.
func TestContextHandlersRejectMalformedJSON(t *testing.T) {
	srv, _ := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsBadJSON"

	if code, body := sendJSON(t, "POST", base+"/context?content=from-query", "", `{"content":`); code != http.StatusBadRequest {
		t.Fatalf("remember with malformed body: want 400, got %d %v", code, body)
	}
	code, e := sendJSON(t, "POST", base+"/context?content=from-query&permission=readwrite", "", "")
	if code != http.StatusOK || e["content"] != "from-query" {
		t.Fatalf("remember with empty body must use the query fallback: %d %v", code, e)
	}
	entryURL := base + "/context/" + e["id"].(string)
	if code, body := sendJSON(t, "POST", entryURL+"/verify?approve=true", "", `{"approve":tru`); code != http.StatusBadRequest {
		t.Fatalf("verify with malformed body: want 400, got %d %v", code, body)
	}
	if code, body := sendJSON(t, "POST", entryURL+"/verify?approve=true", "", `{"approve":true} trailing`); code != http.StatusBadRequest {
		t.Fatalf("verify with trailing garbage: want 400, got %d %v", code, body)
	}
	if code, body := sendJSON(t, "POST", entryURL+"/verify?approve=true", "", ""); code != http.StatusOK {
		t.Fatalf("verify with empty body must use the query fallback: %d %v", code, body)
	}
	if code, body := sendJSON(t, "PUT", entryURL, "", `{"content":`); code != http.StatusBadRequest {
		t.Fatalf("edit with malformed body: want 400, got %d %v", code, body)
	}
}

// Startup explains open mode once, and only when no credentials are configured.
func TestStartupLogsOpenModeOnlyWithoutCredentials(t *testing.T) {
	var buf bytes.Buffer
	prev, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(prevFlags) })

	cases := []struct {
		mode       string
		configured bool
		want       bool
	}{
		{"auto", false, true},
		{"off", false, true},
		{"auto", true, false},
		{"required", true, false},
		{"off", true, false},
	}
	for _, c := range cases {
		buf.Reset()
		buildHandler(serverConfig{authMode: c.mode, authConfigured: c.configured, dataDir: t.TempDir()}, http.NotFoundHandler())
		n := strings.Count(buf.String(), "memory: open mode")
		if (n == 1) != c.want || n > 1 {
			t.Fatalf("mode=%s configured=%v: open-mode line printed %d times, want %v; log:\n%s", c.mode, c.configured, n, c.want, buf.String())
		}
		if c.want && !strings.Contains(buf.String(), workspaceops.VerificationSelfAssertedOpen) {
			t.Fatalf("open-mode line must name the self-asserted label; log:\n%s", buf.String())
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// securityServer runs the full middleware stack (exposure, body limit, auth, gates)
// on a loopback listener with the given posture and a fresh data dir.
func securityServer(t *testing.T, p exposurePosture) (*httptest.Server, string) {
	t.Helper()
	clearProfileEnv(t)
	dir := t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	t.Setenv("XMUSTARD_CORE_BIN", "/nonexistent/xmustard-core")
	t.Setenv("XMUSTARD_AUTH_TOKENS", "")
	p.Loopback = true
	cfg := serverConfig{host: "127.0.0.1", authMode: "auto", dataDir: dir, posture: p}
	srv := httptest.NewServer(buildHandler(cfg, newAPIHandlerFor(p)))
	t.Cleanup(srv.Close)
	return srv, dir
}

func mint(t *testing.T, dir, id, role string) string {
	t.Helper()
	raw, err := workspaceops.MintToken(dir, id, role)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// call sends one request with optional token, headers and body.
func call(t *testing.T, method, url, token, body string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req, _ = http.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, url, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
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

func concretePath(pattern string) string {
	return strings.NewReplacer("{workspace_id}", "ws1", "{entry_id}", "ctx_1").Replace(
		stripWildcards(patternPath(pattern)))
}

// stripWildcards fills every remaining {name} with "id1".
func stripWildcards(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && s != "{workspace_id}" && s != "{entry_id}" {
			segs[i] = "id1"
		}
	}
	return strings.Join(segs, "/")
}

var roleSpecs = []string{"reader", "proposer", "verifier", "human-approver", "indexer", "admin", "agent", "readonly"}

// PAR-SEC-02 role matrix: every route in the table is allowed or denied per role
// exactly as its gate says, and denials name the missing role. Stub handlers stand in
// for the real ones so no route's side effects run.
func TestRoleMatrixEveryRoute(t *testing.T) {
	t.Setenv("XMUSTARD_DATA_DIR", t.TempDir())
	stub := newGatedMux()
	for pattern := range routeGateTable {
		stub.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	}
	h := routeGateMiddleware(exposurePosture{Profile: profilePlatform}, stub, stub)
	// explicit expectations for the routes whose gate is the point of PAR-SEC-02
	explicit := map[string][]string{
		"POST /api/workspaces/{workspace_id}/context/{entry_id}/verify":                 {"verifier", "admin", "agent"},
		"POST /api/workspaces/{workspace_id}/context":                                   {"proposer", "admin", "agent"},
		"POST /api/workspaces/{workspace_id}/index":                                     {"indexer", "admin"},
		"PUT /api/workspaces/{workspace_id}/policy":                                     {"human-approver", "admin"},
		"POST /api/workspaces/{workspace_id}/runs/{run_id}/plan/approve":                {"human-approver", "admin"},
		"PUT /api/workspaces/{workspace_id}/security/findings/{finding_id}/disposition": {"human-approver", "admin"},
		"POST /api/auth/tokens":                                                         {"admin"},
		"POST /api/settings":                                                            {"admin"},
		"POST /api/terminal/open":                                                       {"admin"},
		"GET /api/workspaces/{workspace_id}/context/active":                             roleSpecs,
	}
	for pattern, g := range routeGateTable {
		method := patternMethod(pattern)
		if method == "" {
			method = http.MethodGet
		}
		for _, spec := range roleSpecs {
			pr := &workspaceops.Principal{ID: "p-" + spec, Role: spec, Roles: workspaceops.ExpandRoles(spec)}
			req := httptest.NewRequest(method, concretePath(pattern), nil)
			req = req.WithContext(context.WithValue(req.Context(), principalCtxKey, pr))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			want := pr.Has(g.Role)
			if allowed, ok := explicit[pattern]; ok {
				want = slices.Contains(allowed, spec)
			}
			switch {
			case want && rec.Code != 299:
				t.Errorf("%s as %s: want the handler, got %d %s", pattern, spec, rec.Code, rec.Body.String())
			case !want:
				var body map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &body)
				if rec.Code != http.StatusForbidden || body["missing_role"] != g.Role || !strings.Contains(body["error"].(string), g.Role+" role required") {
					t.Errorf("%s as %s: want 403 naming %s, got %d %v", pattern, spec, g.Role, rec.Code, body)
				}
			}
		}
	}
}

// The tools each role can use (what tools/list shows through the MCP shim).
func TestToolMatrixPerRole(t *testing.T) {
	read := []string{"ground", "recall", "search", "explain", "impact", "diagnostics", "why_failed"}
	want := map[string][]string{
		"reader":         read,
		"readonly":       read,
		"human-approver": read,
		"indexer":        read,
		"proposer":       {"ground", "recall", "remember", "search", "explain", "impact", "diagnostics", "why_failed"},
		"verifier":       {"ground", "recall", "verify", "search", "explain", "impact", "diagnostics", "why_failed"},
		"agent":          mcpToolOrder,
		"admin":          mcpToolOrder,
	}
	core := &exposurePosture{}
	for spec, tools := range want {
		got := usableTools(core, callerView{Roles: workspaceops.ExpandRoles(spec)})
		if !slices.Equal(got, tools) {
			t.Errorf("%s: tools %v, want %v", spec, got, tools)
		}
	}
	ro := &exposurePosture{ReadOnly: true}
	if got := usableTools(ro, callerView{Roles: workspaceops.ExpandRoles("admin")}); !slices.Equal(got, read) {
		t.Errorf("read-only mode must hide remember and verify even from admin: %v", got)
	}
	off := &exposurePosture{DisabledTools: map[string]bool{"impact": true, "why_failed": true}}
	if got := usableTools(off, callerView{Roles: workspaceops.ExpandRoles("agent")}); slices.Contains(got, "impact") || slices.Contains(got, "why_failed") || len(got) != 7 {
		t.Errorf("disabled tools must be hidden: %v", got)
	}
}

// verify needs the verifier role: a proposer-only token is refused with an error
// naming the missing role; a verifier-only token verifies but cannot propose.
func TestVerifyRequiresVerifierRole(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{})
	proposer := mint(t, dir, "pat", "proposer")
	verifier := mint(t, dir, "vera", "verifier")
	base := srv.URL + "/api/workspaces/wsRoles"

	code, e := call(t, "POST", base+"/context", proposer, `{"content":"role fact"}`, nil)
	if code != http.StatusOK || e["source"] != "pat" {
		t.Fatalf("proposer remember: %d %v", code, e)
	}
	id := e["id"].(string)
	code, body := call(t, "POST", base+"/context/"+id+"/verify?approve=true", proposer, "", nil)
	if code != http.StatusForbidden || body["missing_role"] != "verifier" || !strings.Contains(body["error"].(string), "verifier role required") {
		t.Fatalf("proposer-only verify: want 403 naming verifier, got %d %v", code, body)
	}
	if code, body := call(t, "POST", base+"/context/"+id+"/verify?approve=true", verifier, "", nil); code != http.StatusOK {
		t.Fatalf("verifier verify: %d %v", code, body)
	}
	if code, body := call(t, "POST", base+"/context", verifier, `{"content":"x"}`, nil); code != http.StatusForbidden || body["missing_role"] != "proposer" {
		t.Fatalf("verifier-only remember: want 403 naming proposer, got %d %v", code, body)
	}
	// an in-handler gate names the role too
	if code, body := call(t, "GET", srv.URL+"/api/auth/principals", verifier, "", nil); code != http.StatusForbidden || body["missing_role"] != "admin" {
		t.Fatalf("principals as verifier: want 403 naming admin, got %d %v", code, body)
	}
}

// POST /index is core, denied to the agent role and allowed to admin and indexer.
func TestIndexRebaselineIsRoleGatedAndCore(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{})
	url := srv.URL + "/api/workspaces/wsIdx/index"
	code, body := call(t, "POST", url, mint(t, dir, "agentA", "agent"), "", nil)
	if code != http.StatusForbidden || body["missing_role"] != "indexer" {
		t.Fatalf("agent POST /index: want 403 naming indexer, got %d %v", code, body)
	}
	for _, role := range []string{"indexer", "admin"} {
		code, body := call(t, "POST", url, mint(t, dir, "who-"+role, role), "", nil)
		if code == http.StatusForbidden || code == http.StatusUnauthorized || body["reason"] == "platform_route" {
			t.Fatalf("%s POST /index must reach the handler in the core profile, got %d %v", role, code, body)
		}
	}
}

// Read-only mode refuses mutating routes with 403 and hides write tools.
func TestReadOnlyMode(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{ReadOnly: true})
	tok := mint(t, dir, "agentRO", "agent")
	base := srv.URL + "/api/workspaces/wsRO"
	if code, body := call(t, "POST", base+"/context", tok, `{"content":"x"}`, nil); code != http.StatusForbidden || body["reason"] != "read_only" {
		t.Fatalf("remember in read-only mode: want 403 read_only, got %d %v", code, body)
	}
	if code, body := call(t, "POST", base+"/context/ctx_1/verify", tok, "", nil); code != http.StatusForbidden || body["reason"] != "read_only" {
		t.Fatalf("verify in read-only mode: want 403 read_only, got %d %v", code, body)
	}
	if code, body := call(t, "GET", base+"/context/active", tok, "", nil); code != http.StatusOK {
		t.Fatalf("recall in read-only mode: %d %v", code, body)
	}
	if code, body := call(t, "POST", base+"/evidence?tool=search", tok, `{"hits":[]}`, nil); body["reason"] == "read_only" {
		t.Fatalf("capturing the caller's own result is not a shared-state write: %d %v", code, body)
	}
	// deleting every principal's evidence is a shared-state write
	admin := mint(t, dir, "rootRO", "admin")
	if code, body := call(t, "DELETE", base+"/evidence", admin, "", nil); code != http.StatusForbidden || body["reason"] != "read_only" {
		t.Fatalf("workspace-wide evidence purge in read-only mode: want 403 read_only, got %d %v", code, body)
	}
	if code, body := call(t, "POST", srv.URL+"/api/auth/tokens", admin, `{"id":"new","role":"agent"}`, nil); code != http.StatusForbidden || body["reason"] != "read_only" {
		t.Fatalf("minting in read-only mode: want 403 read_only, got %d %v", code, body)
	}
	// a leaked token can still be rotated and revoked: both only withdraw access
	leaked := mint(t, dir, "leaked", "agent")
	if code, body := call(t, "POST", srv.URL+"/api/auth/tokens/leaked/rotate", admin, "", nil); code != http.StatusOK {
		t.Fatalf("rotating in read-only mode: %d %v", code, body)
	}
	if workspaceops.ResolveToken(dir, leaked) != nil {
		t.Fatal("the rotated-out secret must stop resolving")
	}
	if code, body := call(t, "DELETE", srv.URL+"/api/auth/tokens/leaked", admin, "", nil); code != http.StatusOK {
		t.Fatalf("revoking in read-only mode: %d %v", code, body)
	}
	_, who := call(t, "GET", srv.URL+"/api/auth/whoami", tok, "", nil)
	tools := who["tools"].([]any)
	for _, tl := range tools {
		if tl == "remember" || tl == "verify" {
			t.Fatalf("read-only whoami must not offer %v: %v", tl, tools)
		}
	}
	if who["read_only"] != true || len(tools) != 7 {
		t.Fatalf("whoami posture: %v", who)
	}
}

// Host/Origin allowlist on loopback, and no credentials in the query string.
func TestExposureMiddleware(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{AllowedHosts: map[string]bool{"proxy.internal": true}, AllowedOrigins: map[string]bool{"https://ui.example.com": true}})
	tok := mint(t, dir, "agentX", "agent")
	health := srv.URL + "/api/health"
	cases := []struct {
		name    string
		url     string
		headers map[string]string
		code    int
		reason  string
	}{
		{"loopback host", health, nil, 200, ""},
		{"localhost host", health, map[string]string{"Host": "localhost:8042"}, 200, ""},
		{"ipv6 loopback host", health, map[string]string{"Host": "[::1]:8042"}, 200, ""},
		{"allowlisted host", health, map[string]string{"Host": "proxy.internal"}, 200, ""},
		{"rebinding host", health, map[string]string{"Host": "attacker.example:8042"}, 403, "host_not_allowed"},
		{"rebinding host without port", health, map[string]string{"Host": "evil.example"}, 403, "host_not_allowed"},
		{"dev UI origin", health, map[string]string{"Origin": "http://localhost:5173"}, 200, ""},
		{"allowlisted origin", health, map[string]string{"Origin": "https://ui.example.com"}, 200, ""},
		{"foreign origin", health, map[string]string{"Origin": "https://evil.example"}, 403, "origin_not_allowed"},
		{"null origin", health, map[string]string{"Origin": "null"}, 403, "origin_not_allowed"},
		{"api_key in query", srv.URL + "/api/workspaces?api_key=" + tok, nil, 400, "query_credentials"},
		{"token in query", srv.URL + "/api/workspaces/ws/search?q=x&token=" + tok, nil, 400, "query_credentials"},
		{"access_token in query", srv.URL + "/api/workspaces?ACCESS_TOKEN=abc", nil, 400, "query_credentials"},
	}
	for _, c := range cases {
		code, body := call(t, "GET", c.url, "", "", c.headers)
		if code != c.code || (c.reason != "" && body["reason"] != c.reason) {
			t.Errorf("%s: want %d %s, got %d %v", c.name, c.code, c.reason, code, body)
		}
	}
	// a query-string key is refused even alongside a valid bearer header
	if code, _ := call(t, "GET", srv.URL+"/api/workspaces?api_key=x", tok, "", nil); code != http.StatusBadRequest {
		t.Fatalf("query key with bearer: want 400, got %d", code)
	}

	// non-loopback: any Host unless an allowlist is configured
	open := exposurePosture{}
	if !open.hostAllowed("api.example.com") {
		t.Fatal("a non-loopback bind without an allowlist accepts any Host")
	}
	pinned := exposurePosture{AllowedHosts: map[string]bool{"api.example.com": true}}
	if !pinned.hostAllowed("API.example.com:443") || pinned.hostAllowed("other.example.com") {
		t.Fatal("a configured host allowlist applies on any bind")
	}
	if !open.originAllowed("https://api.example.com", "api.example.com") || open.originAllowed("https://evil.example", "api.example.com") {
		t.Fatal("non-loopback origins must be same-origin or allowlisted")
	}
}

// The workspace allowlist confines routes, the workspace list and registration.
func TestWorkspaceAllowlist(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{Workspaces: map[string]bool{"wsA": true}})
	reg, _ := json.Marshal([]map[string]any{
		{"workspace_id": "wsA", "name": "a", "root_path": filepath.Join(dir, "a")},
		{"workspace_id": "wsB", "name": "b", "root_path": filepath.Join(dir, "b")},
	})
	if err := os.WriteFile(filepath.Join(dir, "workspaces.json"), reg, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := call(t, "GET", srv.URL+"/api/workspaces/wsB/context/active", "", "", nil); code != http.StatusForbidden || body["reason"] != "workspace_not_allowed" {
		t.Fatalf("outside the allowlist: want 403, got %d %v", code, body)
	}
	if code, body := call(t, "GET", srv.URL+"/api/workspaces/wsA/context/active", "", "", nil); code != http.StatusOK {
		t.Fatalf("inside the allowlist: %d %v", code, body)
	}
	resp, err := http.Get(srv.URL + "/api/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0]["workspace_id"] != "wsA" {
		t.Fatalf("the workspace list must be filtered: %v", list)
	}
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", "", `{"root_path":"`+filepath.Join(dir, "b")+`"}`, nil); code != http.StatusForbidden || body["reason"] != "workspace_not_allowed" {
		t.Fatalf("registering a root outside the allowlist: want 403, got %d %v", code, body)
	}
	// an encoded slash can't smuggle a second segment past the check
	if code, body := call(t, "GET", srv.URL+"/api/workspaces/wsA%2FwsB/context/active", "", "", nil); code != http.StatusBadRequest || body["reason"] != "invalid_id" {
		t.Fatalf("encoded delimiter in a workspace id: want 400 invalid_id, got %d %v", code, body)
	}
}

func TestDisabledToolIsRefused(t *testing.T) {
	srv, _ := securityServer(t, exposurePosture{DisabledTools: map[string]bool{"impact": true}})
	if code, body := call(t, "GET", srv.URL+"/api/workspaces/ws1/changes/since-index", "", "", nil); code != http.StatusForbidden || body["reason"] != "tool_disabled" {
		t.Fatalf("disabled impact: want 403 tool_disabled, got %d %v", code, body)
	}
	_, who := call(t, "GET", srv.URL+"/api/auth/whoami", "", "", nil)
	if slices.Contains(who["tools"].([]any), any("impact")) || !slices.Contains(who["disabled_tools"].([]any), any("impact")) {
		t.Fatalf("whoami must hide and report the disabled tool: %v", who)
	}
	t.Setenv("XMUSTARD_DISABLED_TOOLS", "impact,teleport")
	if _, err := loadExposurePosture(); err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("an unknown tool name must be a startup error: %v", err)
	}
}

// whoami and ground report the caller's principal and roles.
func TestCallerPrincipalReported(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{})
	_, who := call(t, "GET", srv.URL+"/api/auth/whoami", "", "", nil)
	if who["open_mode"] != true || who["id"] != workspaceops.OpenModeIdentity || len(who["tools"].([]any)) != 9 || who["profile"] != profileCore {
		t.Fatalf("open-mode whoami: %v", who)
	}
	tok := mint(t, dir, "vera", "verifier")
	_, who = call(t, "GET", srv.URL+"/api/auth/whoami", tok, "", nil)
	if who["id"] != "vera" || who["open_mode"] != false || !slices.Equal(who["roles"].([]any), []any{"verifier", "reader"}) {
		t.Fatalf("authenticated whoami: %v", who)
	}

	req := httptest.NewRequest("GET", "/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), principalCtxKey, &workspaceops.Principal{ID: "vera", Role: "verifier"}))
	ground := &workspaceops.SessionGrounding{WorkspaceID: "ws1"}
	ground.Summary = "ok" // promoted from the session section (grounding_session.go)
	raw, err := json.Marshal(groundResponse(req, ground))
	if err != nil {
		t.Fatal(err)
	}
	var g map[string]any
	_ = json.Unmarshal(raw, &g)
	principal, _ := g["principal"].(map[string]any)
	if g["workspace_id"] != "ws1" || g["summary"] != "ok" || principal["id"] != "vera" || !slices.Equal(principal["roles"].([]any), []any{"verifier", "reader"}) {
		t.Fatalf("ground must keep its fields and add the principal: %s", raw)
	}
	if groundResponse(req, nil) != nil {
		t.Fatal("a failed ground has no body to decorate")
	}
}

// The mint API accepts a role list or a "+" spec and refuses unknown roles.
func TestMintAPIRoles(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{})
	admin := mint(t, dir, "root", "admin")
	code, body := call(t, "POST", srv.URL+"/api/auth/tokens", admin, `{"id":"pair","roles":["verifier","proposer"]}`, nil)
	if code != http.StatusOK {
		t.Fatalf("mint with roles: %d %v", code, body)
	}
	if p := workspaceops.ResolveToken(dir, body["token"].(string)); p == nil || !p.Has("verifier") || !p.Has("proposer") || p.Has("admin") {
		t.Fatalf("minted roles: %+v", p)
	}
	for _, bad := range []string{`{"id":"x","role":"root"}`, `{"id":"x","role":"agent","roles":["reader"]}`} {
		if code, body := call(t, "POST", srv.URL+"/api/auth/tokens", admin, bad, nil); code != http.StatusBadRequest {
			t.Fatalf("mint %s: want 400, got %d %v", bad, code, body)
		}
	}
}

func TestPatternValuesFailClosed(t *testing.T) {
	const pattern = "GET /api/workspaces/{workspace_id}/context/{entry_id}/verify"
	if v, ok := patternValues(pattern, "/api/workspaces/ws%2Fx/context/ctx_1/verify"); !ok || v["workspace_id"] != "ws/x" || v["entry_id"] != "ctx_1" {
		t.Fatalf("escaped segment must decode per segment: %v %v", v, ok)
	}
	if _, ok := patternValues(pattern, "/api/workspaces/ws/extra/context/ctx_1/verify"); ok {
		t.Fatal("a misaligned path must not yield values")
	}
	if _, ok := patternValues(pattern, "/api/workspaces/ws%zz/context/ctx_1/verify"); ok {
		t.Fatal("a malformed escape must not yield values")
	}
}

// Every path wildcard is validated, not only the workspace id: an encoded "../" in
// the core why_failed route's run_id must not read another workspace's run (past the
// workspace allowlist and the token's scope) or a planted record outside runs/.
func TestEveryPathWildcardIsValidated(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{Workspaces: map[string]bool{"wsA": true}})
	reg, _ := json.Marshal([]map[string]any{
		{"workspace_id": "wsA", "name": "a", "root_path": filepath.Join(dir, "a")},
		{"workspace_id": "wsB", "name": "b", "root_path": filepath.Join(dir, "b")},
	})
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("workspaces.json", string(reg))
	for _, ws := range []string{"wsA", "wsB"} {
		write(filepath.Join("workspaces", ws, "snapshot.json"), `{"workspace_id":"`+ws+`"}`)
	}
	run := func(id, out string) string {
		raw, _ := json.Marshal(map[string]any{"run_id": id, "status": "failed", "output_path": out})
		return string(raw)
	}
	write("secret-b.log", "error: SECRET_FROM_WSB\n")
	write("mine-a.log", "error: OWN_RUN_OUTPUT\n")
	write(filepath.Join("workspaces", "wsB", "runs", "run_b.json"), run("run_b", filepath.Join(dir, "secret-b.log")))
	write(filepath.Join("workspaces", "wsA", "evil.json"), run("evil", filepath.Join(dir, "secret-b.log")))
	write(filepath.Join("workspaces", "wsA", "runs", "run_a.json"), run("run_a", filepath.Join(dir, "mine-a.log")))
	tok, err := workspaceops.MintScopedToken(dir, "scoped", "agent", 0, []string{"wsA"})
	if err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/api/workspaces/wsA/runs/"
	code, body := call(t, "GET", base+"run_a/why-failed", tok, "", nil)
	if code != http.StatusOK || !strings.Contains(fmt.Sprint(body["error_lines"]), "OWN_RUN_OUTPUT") {
		t.Fatalf("own run: want 200 with its output, got %d %v", code, body)
	}
	for _, runID := range []string{"..%2F..%2FwsB%2Fruns%2Frun_b", "..%2Fevil", "%2E%2E", "run_a%5Cx", "run_a%00"} {
		code, body := call(t, "GET", base+runID+"/why-failed", tok, "", nil)
		if code != http.StatusBadRequest || body["reason"] != "invalid_id" || strings.Contains(fmt.Sprint(body), "SECRET") {
			t.Errorf("run_id %s: want 400 invalid_id, got %d %v", runID, code, body)
		}
	}
	// a literal dot segment is cleaned by the mux (redirect, then no route)
	if code, body := call(t, "GET", base+"../../wsB/runs/run_b/why-failed", tok, "", nil); code == http.StatusOK || strings.Contains(fmt.Sprint(body), "SECRET") {
		t.Errorf("literal ../ in the path: got %d %v", code, body)
	}
	// defence in depth: the run store refuses the id too
	if _, err := workspaceops.ReadRun(dir, "wsA", "../evil"); !workspaceops.IsInvalidInput(err) {
		t.Fatalf("ReadRun with a traversing id: want invalid input, got %v", err)
	}
}

// Platform wildcards (issue, view, provider and other ids) must be one safe path
// segment; free-form characters that stay inside a segment are kept.
func TestPlatformWildcardsAreOneSafeSegment(t *testing.T) {
	t.Setenv("XMUSTARD_DATA_DIR", t.TempDir())
	stub := newGatedMux()
	for pattern := range routeGateTable {
		stub.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	}
	h := routeGateMiddleware(exposurePosture{Profile: profilePlatform}, stub, stub)
	cases := []struct {
		path string
		code int
	}{
		{"/api/workspaces/ws1/issues/P1_ABC-12/quality", 299},
		{"/api/workspaces/ws1/views/wip%2526done-1a2b3c4d", 299}, // a query-escaped view slug
		{"/api/providers/openai-compatible%20local/models", 299},
		{"/api/workspaces/ws1/issues/..%2F..%2Fx/quality", 400},
		{"/api/workspaces/ws1/issues/../quality", 400}, // cleaned by the mux, then refused as misaligned
		{"/api/workspaces/ws1/issues/%2E/quality", 400},
		{"/api/workspaces/ws1/issues/a%5Cb/quality", 400},
		{"/api/workspaces/ws1/issues/a%0Ab/quality", 400},
		{"/api/providers/..%2F..%2Fsettings/models", 400},
		{"/api/workspaces/ws1/goals/g..1", 400},
	}
	for _, c := range cases {
		method := http.MethodGet
		if strings.Contains(c.path, "/views/") {
			method = http.MethodDelete
		}
		req := httptest.NewRequest(method, c.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if c.code == 400 && (rec.Code == 299 || rec.Code == 200) {
			t.Errorf("%s: an unsafe segment reached the handler (%d)", c.path, rec.Code)
		}
		if c.code == 299 && rec.Code != 299 {
			t.Errorf("%s: want the handler, got %d %s", c.path, rec.Code, rec.Body.String())
		}
	}
}

// A pattern the mux matches without a gate row is refused, never served ungated.
func TestUnclassifiedMatchFailsClosed(t *testing.T) {
	m := newGatedMux()
	m.mux.HandleFunc("GET /api/workspaces/{workspace_id}/unclassified", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	h := routeGateMiddleware(exposurePosture{Profile: profilePlatform}, m, m)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/workspaces/ws1/unclassified", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "unclassified_route") {
		t.Fatalf("an ungated match must fail closed, got %d %s", rec.Code, rec.Body.String())
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Handle must refuse a route without a gate row")
		}
	}()
	m.Handle("GET /api/workspaces/{workspace_id}/unclassified-too", http.NotFoundHandler())
}

// Reader-only tokens reach the route gate, so a write names the role it lacks.
func TestReaderWritesNameMissingRole(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{})
	base := srv.URL + "/api/workspaces/wsR"
	for _, spec := range []string{"reader", "readonly"} {
		tok := mint(t, dir, "rita-"+spec, spec)
		for _, c := range []struct{ path, body, role string }{
			{"/context", `{"content":"x"}`, "proposer"},
			{"/context/ctx_1/verify?approve=true", "", "verifier"},
			{"/index", "", "indexer"},
		} {
			code, body := call(t, "POST", base+c.path, tok, c.body, nil)
			if code != http.StatusForbidden || body["missing_role"] != c.role || body["reason"] != "missing_role" {
				t.Errorf("%s POST %s: want 403 naming %s, got %d %v", spec, c.path, c.role, code, body)
			}
		}
		if code, body := call(t, "GET", base+"/context/active", tok, "", nil); code != http.StatusOK {
			t.Errorf("%s recall: %d %v", spec, code, body)
		}
	}
}

// With the auth middleware's reader short-circuit gone, the gate table alone keeps
// reader-only tokens read-only: no non-GET route may grant reader. The MCP endpoint
// is the one exemption: its messages change nothing themselves, and every tool call
// re-enters the API through that tool's own gate as the caller (mcp_routes.go).
func TestNoWriteRouteGrantsReader(t *testing.T) {
	reentrant := map[string]bool{"POST /mcp": true, "DELETE /mcp": true}
	for pattern, g := range routeGateTable {
		switch patternMethod(pattern) {
		case "", http.MethodGet, http.MethodHead:
			continue
		}
		if reentrant[pattern] {
			if !g.ReadSafe || !g.Core {
				t.Errorf("%s: the MCP endpoint must be core and read-safe", pattern)
			}
			continue
		}
		if g.Role == roleReader {
			t.Errorf("%s: a non-GET route must need more than reader", pattern)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/mcpserver"
	"xmustard/api-go/internal/workspaceops"
)

// registerFixture is a filesystem laid out for registration checks: a registration
// root holding a repository, a plain directory and a symlink that escapes to a
// repository outside the root.
type registerFixture struct {
	allowed, repo, plain, secret, escape string
}

func newRegisterFixture(t *testing.T) registerFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := registerFixture{allowed: filepath.Join(base, "code")}
	f.repo = gitLayout(t, filepath.Join(f.allowed, "repo"))
	f.secret = gitLayout(t, filepath.Join(base, "private", "secret"))
	f.plain = filepath.Join(f.allowed, "notes")
	if err := os.MkdirAll(f.plain, 0o755); err != nil {
		t.Fatal(err)
	}
	f.escape = filepath.Join(f.allowed, "escape")
	if err := os.Symlink(f.secret, f.escape); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return f
}

// gitLayout makes dir look like the top level of a git work tree.
func gitLayout(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// seedSnapshot caches a snapshot for root, so a load that prefers the cache answers
// without the Rust core (the test server has none); a load that scans fails.
func seedSnapshot(t *testing.T, dataDir, root string) string {
	t.Helper()
	id, err := workspaceops.WorkspaceIDForRoot(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"scanner_version": workspaceops.ScannerVersion,
		"workspace": map[string]any{"workspace_id": id, "name": filepath.Base(root), "root_path": root}})
	if err := os.MkdirAll(filepath.Join(dataDir, "workspaces", id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "workspaces", id, "snapshot.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

func loadBody(root string, extra ...string) string {
	body := map[string]any{"root_path": root, "auto_scan": true, "prefer_cached_snapshot": true}
	for i := 0; i+1 < len(extra); i += 2 {
		switch extra[i+1] {
		case "false":
			body[extra[i]] = false
		default:
			body[extra[i]] = extra[i+1]
		}
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

// registered returns the registry as root -> record.
func registered(t *testing.T, dataDir string) map[string]map[string]any {
	t.Helper()
	var items []map[string]any
	raw, err := os.ReadFile(filepath.Join(dataDir, "workspaces.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatal(err)
		}
	}
	out := map[string]map[string]any{}
	for _, it := range items {
		out[it["root_path"].(string)] = it
	}
	return out
}

// registerEvents returns the auth audit's register events.
func registerEvents(dataDir string) []workspaceops.AuthAuditEvent {
	var out []workspaceops.AuthAuditEvent
	for _, ev := range workspaceops.ListAuthAudit(dataDir, 0) {
		if ev.Action == "register" {
			out = append(out, ev)
		}
	}
	return out
}

// An agent token registers the top level of a git work tree under a registration
// root; a symlink inside the root registers its resolved target; the audit log
// names the agent.
func TestAgentRegistersWorkTreeUnderRegisterRoot(t *testing.T) {
	f := newRegisterFixture(t)
	srv, dir := securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}})
	agent := mint(t, dir, "ada", "agent")
	id := seedSnapshot(t, dir, f.repo)
	code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(f.repo), nil)
	if code != http.StatusOK {
		t.Fatalf("agent inside the registration root: want 200, got %d %v", code, body)
	}
	if reg := registered(t, dir); reg[f.repo] == nil || reg[f.repo]["workspace_id"] != id {
		t.Fatalf("repository not registered under %s: %v", id, reg)
	}
	events := registerEvents(dir)
	if len(events) != 1 || events[0].Actor != "ada" || !strings.Contains(events[0].Detail, f.repo) || !strings.Contains(events[0].Detail, "XMUSTARD_REGISTER_ROOTS") {
		t.Fatalf("the registration must be audited with its principal: %+v", events)
	}

	// through a symlink inside the root: the resolved path is what gets registered
	other := gitLayout(t, filepath.Join(f.allowed, "other"))
	alias := filepath.Join(f.allowed, "alias")
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	seedSnapshot(t, dir, other)
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(alias), nil); code != http.StatusOK {
		t.Fatalf("symlink resolving inside the root: want 200, got %d %v", code, body)
	}
	if reg := registered(t, dir); reg[other] == nil || reg[alias] != nil {
		t.Fatalf("the registry must hold the resolved root, not the link: %v", reg)
	}
}

// Every path an agent may not register is refused with 403 and the reason, and
// nothing reaches the registry.
func TestAgentRegistrationRefusals(t *testing.T) {
	f := newRegisterFixture(t)
	srv, dir := securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}})
	agent := mint(t, dir, "ada", "agent")
	seedSnapshot(t, dir, f.secret) // admitted, the load would succeed from cache
	bare := filepath.Join(f.allowed, "mirror.git")
	if err := os.MkdirAll(filepath.Join(bare, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, root, refusal string }{
		{"outside the root", f.secret, workspaceops.RefusalOutsideRoots},
		{"symlink escaping the root", f.escape, workspaceops.RefusalOutsideRoots},
		{"dot-dot out of the root", f.allowed + string(filepath.Separator) + filepath.Join("..", "private", "secret"), workspaceops.RefusalOutsideRoots},
		{"not a repository", f.plain, workspaceops.RefusalNotGitWorkTree},
		{".git directory", filepath.Join(f.repo, ".git"), workspaceops.RefusalGitInternals},
		{"inside .git", filepath.Join(f.repo, ".git", "objects"), workspaceops.RefusalGitInternals},
		{"subdirectory of a work tree", filepath.Join(f.repo, "src"), workspaceops.RefusalNotTopLevel},
		{"bare repository", bare, workspaceops.RefusalBareRepository},
		{"relative path", "code/repo", workspaceops.RefusalInvalidPath},
	}
	for _, c := range cases {
		code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(c.root), nil)
		if code != http.StatusForbidden || body["reason"] != registrationNotAllowed || body["refusal"] != c.refusal {
			t.Errorf("%s: want 403 %s/%s, got %d %v", c.name, registrationNotAllowed, c.refusal, code, body)
		}
	}
	if reg := registered(t, dir); len(reg) != 0 {
		t.Fatalf("a refused registration must not reach the registry: %v", reg)
	}
	if ev := registerEvents(dir); len(ev) != 0 {
		t.Fatalf("no registration happened, none may be audited: %+v", ev)
	}
}

// Without XMUSTARD_REGISTER_ROOTS only admins register; a reader token needs the
// proposer role; a workspace-scoped token registers only inside its scope; read-only
// mode refuses registration.
func TestRegistrationRolesAndScopes(t *testing.T) {
	f := newRegisterFixture(t)
	srv, dir := securityServer(t, exposurePosture{})
	agent := mint(t, dir, "ada", "agent")
	code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(f.repo), nil)
	if code != http.StatusForbidden || body["refusal"] != workspaceops.RefusalNoRegisterRoots || !strings.Contains(body["error"].(string), "XMUSTARD_REGISTER_ROOTS") {
		t.Fatalf("no registration roots: want 403 %s naming the setting, got %d %v", workspaceops.RefusalNoRegisterRoots, code, body)
	}

	srv, dir = securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}})
	reader := mint(t, dir, "rita", "reader")
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", reader, loadBody(f.repo), nil); code != http.StatusForbidden || body["missing_role"] != workspaceops.RoleProposer {
		t.Fatalf("reader: want 403 missing proposer, got %d %v", code, body)
	}
	scoped, err := workspaceops.MintScopedToken(dir, "sam", "agent", 0, []string{"other-ws"})
	if err != nil {
		t.Fatal(err)
	}
	seedSnapshot(t, dir, f.repo)
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", scoped, loadBody(f.repo), nil); code != http.StatusForbidden || body["refusal"] != refusalTokenScope {
		t.Fatalf("scoped token outside its scope: want 403 token_scope, got %d %v", code, body)
	}
	id, _ := workspaceops.WorkspaceIDForRoot(dir, f.repo)
	inScope, err := workspaceops.MintScopedToken(dir, "sue", "agent", 0, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", inScope, loadBody(f.repo), nil); code != http.StatusOK {
		t.Fatalf("scoped token registering its own workspace: want 200, got %d %v", code, body)
	}

	srv, dir = securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}, ReadOnly: true})
	agent = mint(t, dir, "ada", "agent")
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(f.repo), nil); code != http.StatusForbidden || body["reason"] != "read_only" {
		t.Fatalf("read-only mode: want 403 read_only, got %d %v", code, body)
	}
}

// An admin still registers any directory, and open mode is unchanged: neither is
// held to the registration roots or the git checks. Both are audited.
func TestAdminAndOpenModeRegistrationUnchanged(t *testing.T) {
	f := newRegisterFixture(t)
	srv, dir := securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}})
	admin := mint(t, dir, "root-op", "admin")
	for _, root := range []string{f.secret, f.plain} {
		seedSnapshot(t, dir, root)
		if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", admin, loadBody(root), nil); code != http.StatusOK {
			t.Fatalf("admin registering %s: want 200, got %d %v", root, code, body)
		}
	}
	events := registerEvents(dir)
	if len(events) != 2 || events[0].Actor != "root-op" || strings.Contains(events[0].Detail, "XMUSTARD_REGISTER_ROOTS") {
		t.Fatalf("admin registrations must be audited with the admin: %+v", events)
	}

	for _, roots := range [][]string{nil, {f.allowed}} {
		srv, dir := securityServer(t, exposurePosture{RegisterRoots: roots})
		for _, root := range []string{f.secret, f.plain, f.escape} {
			seedSnapshot(t, dir, root)
			if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", "", loadBody(root), nil); code != http.StatusOK {
				t.Fatalf("open mode (roots %v) registering %s: want 200, got %d %v", roots, root, code, body)
			}
		}
		if reg := registered(t, dir); reg[f.escape] == nil {
			t.Fatalf("open mode registers the path as given: %v", reg)
		}
		if ev := registerEvents(dir); len(ev) != 3 || ev[0].Actor != workspaceops.OpenModeIdentity {
			t.Fatalf("open-mode registrations are audited as %s: %+v", workspaceops.OpenModeIdentity, ev)
		}
	}
}

// A non-admin load of a registered workspace keeps its name and never forces a
// rescan (the test server has no Rust core, so a scan would fail).
func TestAgentReloadKeepsNameAndCachedSnapshot(t *testing.T) {
	f := newRegisterFixture(t)
	srv, dir := securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}})
	admin, agent := mint(t, dir, "root-op", "admin"), mint(t, dir, "ada", "agent")
	seedSnapshot(t, dir, f.repo)
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", admin, loadBody(f.repo, "name", "Pretty"), nil); code != http.StatusOK {
		t.Fatalf("admin registration: %d %v", code, body)
	}
	code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(f.repo, "name", "evil", "prefer_cached_snapshot", "false"), nil)
	if code != http.StatusOK {
		t.Fatalf("agent reload must be served from the cache, got %d %v", code, body)
	}
	if reg := registered(t, dir); reg[f.repo]["name"] != "Pretty" {
		t.Fatalf("an agent reload renamed the workspace: %v", reg[f.repo])
	}
	if ev := registerEvents(dir); len(ev) != 1 || ev[0].Actor != "root-op" {
		t.Fatalf("only the first load registers: %+v", ev)
	}
}

// The MCP shim's first tool call auto-registers the repository it runs in with an
// agent token, end to end through the real API (the WS-04/WS-09 seam).
func TestMCPShimAutoRegistersWithAgentToken(t *testing.T) {
	f := newRegisterFixture(t)
	shim := func(srv string, token string) *mcpserver.Session {
		s := mcpserver.New(mcpserver.Options{
			Backend:      &mcpserver.HTTPBackend{Base: func() string { return srv }, Token: func() string { return token }},
			Cwd:          filepath.Join(f.repo, "src"),
			AutoRegister: true,
			Getenv:       func(string) string { return "" },
		}).NewSession(nil)
		params, _ := json.Marshal(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}})
		if _, rerr := s.Handle(context.Background(), "initialize", params); rerr != nil {
			t.Fatal(rerr)
		}
		s.Notify("notifications/initialized", nil)
		return s
	}
	recall := func(s *mcpserver.Session) (bool, string) {
		params, _ := json.Marshal(map[string]any{"name": "recall", "arguments": map[string]any{}})
		res, rerr := s.Handle(context.Background(), "tools/call", params)
		if rerr != nil {
			t.Fatal(rerr)
		}
		raw, _ := json.Marshal(res)
		return res.(map[string]any)["isError"] == true, string(raw)
	}

	srv, dir := securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}})
	seedSnapshot(t, dir, f.repo)
	isErr, out := recall(shim(srv.URL, mint(t, dir, "ada", "agent")))
	if isErr || !strings.Contains(out, "registered with the API by this call") {
		t.Fatalf("agent token under a registration root must auto-register: %s", out)
	}
	if ev := registerEvents(dir); len(ev) != 1 || ev[0].Actor != "ada" {
		t.Fatalf("the shim's registration must be audited as the agent: %+v", ev)
	}

	srv, dir = securityServer(t, exposurePosture{})
	isErr, out = recall(shim(srv.URL, mint(t, dir, "ada", "agent")))
	if !isErr || !strings.Contains(out, "XMUSTARD_REGISTER_ROOTS") {
		t.Fatalf("without registration roots the refusal must say how to allow it: %s", out)
	}
}

// XMUSTARD_REGISTER_ROOTS is part of the exposure posture; a malformed value stops
// the API at startup.
func TestRegisterRootsPosture(t *testing.T) {
	clearProfileEnv(t)
	root := t.TempDir()
	t.Setenv("XMUSTARD_REGISTER_ROOTS", root+string(filepath.ListSeparator)+root)
	if p, err := loadExposurePosture(); err != nil || len(p.RegisterRoots) != 1 || p.RegisterRoots[0] != filepath.Clean(root) {
		t.Fatalf("want [%s], got %v %v", root, p.RegisterRoots, err)
	}
	for _, bad := range []string{"relative/code", string(filepath.Separator)} {
		t.Setenv("XMUSTARD_REGISTER_ROOTS", bad)
		if err := validateStartup(loadServerConfig(t.TempDir())); err == nil || !strings.Contains(err.Error(), "XMUSTARD_REGISTER_ROOTS") {
			t.Fatalf("%q must stop the API at startup: %v", bad, err)
		}
	}

	t.Setenv("XMUSTARD_REGISTER_ROOTS", root)
	if p, err := loadExposurePosture(); err != nil || p.registerLimit() != workspaceops.DefaultRegisterLimit {
		t.Fatalf("unset XMUSTARD_REGISTER_LIMIT: want %d, got %d %v", workspaceops.DefaultRegisterLimit, p.registerLimit(), err)
	}
	t.Setenv("XMUSTARD_REGISTER_LIMIT", "3")
	if p, err := loadExposurePosture(); err != nil || p.registerLimit() != 3 {
		t.Fatalf("XMUSTARD_REGISTER_LIMIT=3: got %d %v", p.registerLimit(), err)
	}
	for _, bad := range []string{"0", "-2", "lots"} {
		t.Setenv("XMUSTARD_REGISTER_LIMIT", bad)
		if err := validateStartup(loadServerConfig(t.TempDir())); err == nil || !strings.Contains(err.Error(), "XMUSTARD_REGISTER_LIMIT") {
			t.Fatalf("XMUSTARD_REGISTER_LIMIT=%q must stop the API at startup: %v", bad, err)
		}
	}
}

// A workspace-scoped admin token registers only inside its scope:
// /api/workspaces/load is not a workspace route, so the handler applies the scope to
// every principal, admins included.
func TestScopedAdminRegistersOnlyInsideScope(t *testing.T) {
	f := newRegisterFixture(t)
	srv, dir := securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}})
	scoped, err := workspaceops.MintScopedToken(dir, "scoped-op", "admin", 0, []string{"only-this-ws"})
	if err != nil {
		t.Fatal(err)
	}
	seedSnapshot(t, dir, f.secret)
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", scoped, loadBody(f.secret), nil); code != http.StatusForbidden || body["refusal"] != refusalTokenScope {
		t.Fatalf("scoped admin outside its scope: want 403 token_scope, got %d %v", code, body)
	}
	if reg := registered(t, dir); len(reg) != 0 {
		t.Fatalf("a refused registration must not reach the registry: %v", reg)
	}
	id := seedSnapshot(t, dir, f.plain)
	inScope, err := workspaceops.MintScopedToken(dir, "scoped-in", "admin", 0, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", inScope, loadBody(f.plain), nil); code != http.StatusOK {
		t.Fatalf("scoped admin inside its scope registers any directory: want 200, got %d %v", code, body)
	}
}

// sameDirSpellings returns other spellings of dir's last element that name the same
// directory (a case-insensitive filesystem), or nil.
func sameDirSpellings(t *testing.T, dir string) []string {
	t.Helper()
	base := filepath.Base(dir)
	var out []string
	for _, v := range []string{strings.ToUpper(base), strings.ToUpper(base[:1]) + base[1:]} {
		alt := filepath.Join(filepath.Dir(dir), v)
		a, errA := os.Stat(dir)
		b, errB := os.Stat(alt)
		if v != base && errA == nil && errB == nil && os.SameFile(a, b) {
			out = append(out, alt)
		}
	}
	return out
}

func loadedID(body map[string]any) string {
	ws, _ := body["workspace"].(map[string]any)
	id, _ := ws["workspace_id"].(string)
	return id
}

// A non-admin load of a directory that is already registered, through a symlink or
// under another spelling on a case-insensitive filesystem, reuses that workspace and
// never registers the directory a second time.
func TestAgentLoadReusesRegisteredDirectory(t *testing.T) {
	f := newRegisterFixture(t)
	srv, dir := securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}})
	admin, agent := mint(t, dir, "root-op", "admin"), mint(t, dir, "ada", "agent")
	link := filepath.Join(filepath.Dir(f.allowed), "codelink")
	if err := os.Symlink(f.allowed, link); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(link, "repo")
	id := seedSnapshot(t, dir, viaLink)
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", admin, loadBody(viaLink, "name", "Pretty"), nil); code != http.StatusOK {
		t.Fatalf("admin registration through a link: %d %v", code, body)
	}
	code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(f.repo), nil)
	if code != http.StatusOK || loadedID(body) != id {
		t.Fatalf("agent load of the admin's workspace by its real path: want 200 %s, got %d %v", id, code, body)
	}
	if reg := registered(t, dir); len(reg) != 1 || reg[viaLink]["name"] != "Pretty" {
		t.Fatalf("the directory must stay registered once, as the admin named it: %v", reg)
	}

	other := gitLayout(t, filepath.Join(f.allowed, "other"))
	otherID := seedSnapshot(t, dir, other)
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(other), nil); code != http.StatusOK || loadedID(body) != otherID {
		t.Fatalf("agent registration: %d %v", code, body)
	}
	for _, alt := range sameDirSpellings(t, other) {
		if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(alt), nil); code != http.StatusOK || loadedID(body) != otherID {
			t.Fatalf("case variant %s: want 200 %s, got %d %v", alt, otherID, code, body)
		}
	}
	if reg := registered(t, dir); len(reg) != 2 {
		t.Fatalf("every spelling of one directory is one workspace: %v", reg)
	}
	if ev := registerEvents(dir); len(ev) != 2 {
		t.Fatalf("two directories, two registrations: %+v", ev)
	}
}

// A non-admin load never decides whether to scan. Registering a new root always
// scans it, whatever auto_scan says (the test server has no Rust core, so that scan
// answers 500). Loading a registered root never rescans it, even when its cached
// snapshot cannot be reused: the answer is 404, and admin, indexer or /scan rescans.
func TestAgentLoadScanPolicy(t *testing.T) {
	f := newRegisterFixture(t)
	srv, dir := securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}})
	admin, agent := mint(t, dir, "root-op", "admin"), mint(t, dir, "ada", "agent")
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(f.repo, "auto_scan", "false"), nil); code != http.StatusInternalServerError {
		t.Fatalf("an agent registration comes with its first scan: want the scan's 500, got %d %v", code, body)
	}

	other := gitLayout(t, filepath.Join(f.allowed, "other"))
	id := seedSnapshot(t, dir, other)
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", admin, loadBody(other), nil); code != http.StatusOK {
		t.Fatalf("admin registration: %d %v", code, body)
	}
	stale, _ := json.Marshal(map[string]any{"scanner_version": workspaceops.ScannerVersion - 1,
		"workspace": map[string]any{"workspace_id": id, "name": "other", "root_path": other}})
	if err := os.WriteFile(filepath.Join(dir, "workspaces", id, "snapshot.json"), stale, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(other, "prefer_cached_snapshot", "false"), nil); code != http.StatusNotFound {
		t.Fatalf("an agent load of a registered root must not rescan it: want 404, got %d %v", code, body)
	}
}

// XMUSTARD_REGISTER_LIMIT caps how many workspaces one non-admin principal
// registers; reloading its own workspaces and admin registrations are not capped.
func TestAgentRegistrationLimit(t *testing.T) {
	f := newRegisterFixture(t)
	srv, dir := securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}, RegisterLimit: 2})
	admin, ada, bob := mint(t, dir, "root-op", "admin"), mint(t, dir, "ada", "agent"), mint(t, dir, "bob", "agent")
	var repos []string
	for _, name := range []string{"r1", "r2", "r3", "r4"} {
		repo := gitLayout(t, filepath.Join(f.allowed, name))
		seedSnapshot(t, dir, repo)
		repos = append(repos, repo)
	}
	for _, repo := range repos[:2] {
		if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", ada, loadBody(repo), nil); code != http.StatusOK {
			t.Fatalf("within the limit: %d %v", code, body)
		}
	}
	code, body := call(t, "POST", srv.URL+"/api/workspaces/load", ada, loadBody(repos[2]), nil)
	if code != http.StatusForbidden || body["refusal"] != workspaceops.RefusalRegisterLimit || !strings.Contains(fmt.Sprint(body["error"]), "XMUSTARD_REGISTER_LIMIT") {
		t.Fatalf("over the limit: want 403 %s, got %d %v", workspaceops.RefusalRegisterLimit, code, body)
	}
	if reg := registered(t, dir); reg[repos[2]] != nil {
		t.Fatalf("a refused registration must not reach the registry: %v", reg)
	}
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", ada, loadBody(repos[0]), nil); code != http.StatusOK {
		t.Fatalf("reloading an own workspace is not a new registration: %d %v", code, body)
	}
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", bob, loadBody(repos[2]), nil); code != http.StatusOK {
		t.Fatalf("the limit is per principal: %d %v", code, body)
	}
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", admin, loadBody(repos[3]), nil); code != http.StatusOK {
		t.Fatalf("admins are not capped: %d %v", code, body)
	}
}

// After an agent registers a work tree, swapping the directory for a symlink, or
// repointing its .git, makes xMustard refuse the workspace (409) instead of reading
// wherever the link now leads. A root an admin registered is not held to this.
func TestSwappedAgentRootIsRefused(t *testing.T) {
	f := newRegisterFixture(t)
	srv, dir := securityServer(t, exposurePosture{RegisterRoots: []string{f.allowed}})
	admin, agent := mint(t, dir, "root-op", "admin"), mint(t, dir, "ada", "agent")
	nested := gitLayout(t, filepath.Join(f.repo, "vendor", "x"))
	if err := os.WriteFile(filepath.Join(nested, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := seedSnapshot(t, dir, nested)
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", agent, loadBody(nested), nil); code != http.StatusOK {
		t.Fatalf("agent registration: %d %v", code, body)
	}
	remember := func(token string) (int, map[string]any) {
		return call(t, "POST", srv.URL+"/api/workspaces/"+id+"/context", token, `{"content":"a fact","paths":["README"]}`, nil)
	}
	if code, body := remember(agent); code >= 300 {
		t.Fatalf("remember before the swap: %d %v", code, body)
	}

	if err := os.RemoveAll(nested); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.secret, nested); err != nil {
		t.Fatal(err)
	}
	if code, body := remember(agent); code != http.StatusConflict {
		t.Fatalf("remember after the root became a symlink: want 409, got %d %v", code, body)
	}
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", admin, loadBody(nested), nil); code != http.StatusConflict {
		t.Fatalf("loading a swapped root: want 409, got %d %v", code, body)
	}

	// restored, then its .git repointed outside the registration root
	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}
	gitLayout(t, nested)
	if code, body := remember(agent); code >= 300 {
		t.Fatalf("remember once the directory is restored: %d %v", code, body)
	}
	if err := os.RemoveAll(filepath.Join(nested, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: "+filepath.Join(f.secret, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := remember(agent); code != http.StatusConflict {
		t.Fatalf("remember after .git was repointed outside the root: want 409, got %d %v", code, body)
	}

	// an admin-registered root is used as registered, links and all
	adminID := seedSnapshot(t, dir, f.escape)
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/load", admin, loadBody(f.escape), nil); code != http.StatusOK {
		t.Fatalf("admin registration of a link: %d %v", code, body)
	}
	if code, body := call(t, "POST", srv.URL+"/api/workspaces/"+adminID+"/context", admin, `{"content":"a fact","paths":["src"]}`, nil); code >= 300 {
		t.Fatalf("an admin-registered root is not pinned: %d %v", code, body)
	}
}

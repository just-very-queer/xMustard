package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// clearProfileEnv unsets every posture setting so a test sees the built-in default.
func clearProfileEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"XMUSTARD_PROFILE", "XMUSTARD_PLATFORM", "XMUSTARD_CORE_ONLY", "XMUSTARD_READ_ONLY",
		"XMUSTARD_DISABLED_TOOLS", "XMUSTARD_WORKSPACE_ALLOWLIST", "XMUSTARD_ALLOWED_HOSTS", "XMUSTARD_ALLOWED_ORIGINS",
		"XMUSTARD_REGISTER_ROOTS", "XMUSTARD_REGISTER_LIMIT"} {
		t.Setenv(k, "")
	}
}

func TestCoreProfileIsTheDefault(t *testing.T) {
	clearProfileEnv(t)
	p, err := loadExposurePosture()
	if err != nil || p.platform() || p.profile() != profileCore {
		t.Fatalf("default posture must be the core profile: %+v %v", p, err)
	}
}

func TestProfileSelection(t *testing.T) {
	cases := []struct {
		env     map[string]string
		want    string
		wantErr string
	}{
		{map[string]string{"XMUSTARD_PROFILE": "platform"}, profilePlatform, ""},
		{map[string]string{"XMUSTARD_PROFILE": "CORE"}, profileCore, ""},
		{map[string]string{"XMUSTARD_PLATFORM": "1"}, profilePlatform, ""},
		{map[string]string{"XMUSTARD_CORE_ONLY": "0"}, profilePlatform, ""}, // legacy opt-out keeps working
		{map[string]string{"XMUSTARD_CORE_ONLY": "1"}, profileCore, ""},
		{map[string]string{"XMUSTARD_PROFILE": "platform", "XMUSTARD_CORE_ONLY": "0", "XMUSTARD_PLATFORM": "1"}, profilePlatform, ""},
		{map[string]string{"XMUSTARD_PROFILE": "core", "XMUSTARD_PLATFORM": "1"}, "", "conflicting profile settings"},
		{map[string]string{"XMUSTARD_CORE_ONLY": "1", "XMUSTARD_PLATFORM": "1"}, "", "conflicting profile settings"},
		{map[string]string{"XMUSTARD_PROFILE": "everything"}, "", "invalid XMUSTARD_PROFILE"},
		{map[string]string{"XMUSTARD_CORE_ONLY": "yes"}, "", "invalid XMUSTARD_CORE_ONLY"},
	}
	for _, c := range cases {
		clearProfileEnv(t)
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		p, err := loadExposurePosture()
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("%v: want error %q, got %v", c.env, c.wantErr, err)
			}
			if cfg := loadServerConfig(t.TempDir()); validateStartup(cfg) == nil {
				t.Fatalf("%v: startup must refuse a bad profile setting", c.env)
			}
			continue
		}
		if err != nil || p.profile() != c.want {
			t.Fatalf("%v: want profile %s, got %s (%v)", c.env, c.want, p.profile(), err)
		}
	}
}

// serve sends one request through the gated route table with the given posture.
func serve(t *testing.T, p exposurePosture, method, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	newAPIHandlerFor(p).ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func isPlatformRefusal(code int, body map[string]any) bool {
	return code == http.StatusNotFound && body["reason"] == "platform_route"
}

// The core profile (the default) serves the nine tools, health, auth, memory and
// evidence; every platform route answers 404 unless the platform profile is chosen.
func TestCoreProfileServesToolsHealthAndEvidenceOnly(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("XMUSTARD_DATA_DIR", t.TempDir())
	t.Setenv("XMUSTARD_CORE_BIN", "/nonexistent/xmustard-core") // no real core spawns
	core := []struct{ method, path string }{
		{"GET", "/api/health"},
		{"GET", "/api/workspaces"},
		{"GET", "/api/auth/whoami"},
		{"GET", "/api/workspaces/ws1/session-grounding"},
		{"POST", "/api/workspaces/ws1/context"},
		{"GET", "/api/workspaces/ws1/context/active"},
		{"POST", "/api/workspaces/ws1/context/ctx_1/verify"},
		{"GET", "/api/workspaces/ws1/search?q=x"},
		{"GET", "/api/workspaces/ws1/explain-path?path=a.go"},
		{"GET", "/api/workspaces/ws1/changes/since-index"},
		{"GET", "/api/workspaces/ws1/diagnostics"},
		{"GET", "/api/workspaces/ws1/runs/run-1/why-failed"},
		{"POST", "/api/workspaces/ws1/index"},
		{"GET", "/api/workspaces/ws1/evidence/ev_missing"},
	}
	platform := []struct{ method, path string }{
		{"GET", "/api/workspaces/ws1/subsystems"},
		{"GET", "/api/workspaces/ws1/eval-timeline"},
		{"GET", "/api/providers"},
		{"POST", "/api/route"},
		{"GET", "/api/settings"},
		{"GET", "/api/workspaces/ws1/hotspots"},
		{"POST", "/api/terminal/open"},
		// the old substring gate (XM-PRO-011) wrongly admitted these lookalikes
		{"POST", "/api/workspaces/ws1/diagnostics/run"},
		{"GET", "/api/workspaces/ws1/goals/g1/context"},
		{"GET", "/api/workspaces/ws1/issues/i1/context-replays"},
		{"GET", "/api/workspaces/ws1/pg/search"},
	}
	corePosture := postureFromEnv()
	for _, c := range core {
		if code, body := serve(t, corePosture, c.method, c.path); isPlatformRefusal(code, body) {
			t.Errorf("%s %s must be served in the core profile, got %d %v", c.method, c.path, code, body)
		}
	}
	for _, c := range platform {
		if code, body := serve(t, corePosture, c.method, c.path); !isPlatformRefusal(code, body) {
			t.Errorf("%s %s must 404 in the core profile, got %d %v", c.method, c.path, code, body)
		}
	}
	t.Setenv("XMUSTARD_PLATFORM", "1")
	platformPosture := postureFromEnv()
	for _, c := range append(core, platform...) {
		if c.method == "POST" && (strings.HasPrefix(c.path, "/api/terminal") || strings.HasSuffix(c.path, "/diagnostics/run") || c.path == "/api/route") {
			continue // side effects; their gate decisions are covered by the role matrix
		}
		if code, body := serve(t, platformPosture, c.method, c.path); isPlatformRefusal(code, body) {
			t.Errorf("%s %s must be served with XMUSTARD_PLATFORM=1, got %d %v", c.method, c.path, code, body)
		}
	}
}

// Every registered route has exactly one row in the gate table and every row names a
// registered route, so a route can be neither served unclassified nor left behind.
func TestRouteGateTableMatchesRegisteredRoutes(t *testing.T) {
	t.Setenv("XMUSTARD_DATA_DIR", t.TempDir())
	mux := newGatedMux()
	registerRoutes(mux)
	registerEvidenceRoutes(mux, nil)
	registerEvidenceCaptureRoutes(mux, nil)
	registerOutcomeRoutes(mux, nil)
	registerHookRoutes(mux, nil)
	var missing, stale []string
	for p := range routeGateTable {
		if _, ok := mux.gates[p]; !ok {
			stale = append(stale, p)
		}
	}
	for p := range mux.gates {
		if _, ok := routeGateTable[p]; !ok {
			missing = append(missing, p)
		}
	}
	sort.Strings(stale)
	sort.Strings(missing)
	if len(stale) > 0 || len(missing) > 0 {
		t.Fatalf("gate table out of sync: rows without a route %v; routes without a row %v", stale, missing)
	}
}

func TestUnclassifiedRoutePanicsAtRegistration(t *testing.T) {
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "GET /api/workspaces/{workspace_id}/brand-new") {
			t.Fatalf("registering an unclassified route must panic naming it, got %v", r)
		}
	}()
	newGatedMux().HandleFunc("GET /api/workspaces/{workspace_id}/brand-new", func(http.ResponseWriter, *http.Request) {})
}

// The nine tools map to core routes, and evidence delivery's route->tool mapping
// agrees with the gate table.
func TestToolGatesAreTheNineCoreTools(t *testing.T) {
	gates := toolGates()
	if len(gates) != len(mcpToolOrder) || len(coreTools) != len(mcpToolOrder) {
		t.Fatalf("tool gates %v, coreTools %v, order %v disagree", gates, coreTools, mcpToolOrder)
	}
	for _, tool := range mcpToolOrder {
		pattern, ok := gates[tool]
		if !ok || !coreTools[tool] {
			t.Fatalf("tool %s has no gate or is not a core tool", tool)
		}
		if !routeGateTable[pattern].Core {
			t.Fatalf("tool %s is served by platform route %s", tool, pattern)
		}
		path := strings.NewReplacer("{workspace_id}", "ws1", "{entry_id}", "ctx_1", "{run_id}", "r1").Replace(patternPath(pattern))
		if got := coreToolFor(patternMethod(pattern), path); got != tool {
			t.Fatalf("evidence delivery maps %s to %q, gate table says %q", pattern, got, tool)
		}
	}
}

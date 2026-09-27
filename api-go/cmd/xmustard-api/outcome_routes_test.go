package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// newOutcomeFixture serves the real handler stack with a fake core whose bounded
// runner reports a failing go test and whose working changes name x_test.go.
func newOutcomeFixture(t *testing.T, env map[string]string) *evidenceFixture {
	t.Helper()
	f := &evidenceFixture{ws: "wsOut", dir: t.TempDir()}
	t.Setenv("XMUSTARD_DATA_DIR", f.dir)
	t.Setenv("XMUSTARD_WHY_FAILED_COMMANDS", "1") // the fixture has no loopback bind
	for k, v := range env {
		t.Setenv(k, v)
	}
	seedCoreWorkspace(t, f.dir, f.ws)
	core := writeScript(t, `case "$1" in
run-managed-command) printf '{"command":"go test","cwd":"%s","exit_code":1,"success":false,"timed_out":false,"duration_ms":3,"stdout_excerpt":"--- FAIL: TestX (0.00s)\\n    x_test.go:3: boom\\nFAIL","stderr_excerpt":"","created_at":"2026-09-28T00:00:00Z"}' "$2" ;;
changetrack) printf '{"changed_files":[{"path":"x_test.go"}]}' ;;
*) echo '{}' ;;
esac
`)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	f.srv = httptest.NewServer(bodyLimitMiddleware(authMiddleware(f.dir, "auto", newAPIHandler())))
	t.Cleanup(f.srv.Close)
	return f
}

func decodeMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return m
}

// Running a command is an agent write; readers read the outcome it records, by id,
// in the outcome list and in ground's recent_failed_runs, without a platform run.
func TestWhyFailedRouteRecordsAndReadersRead(t *testing.T) {
	f := newOutcomeFixture(t, nil)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	rita, _ := workspaceops.MintToken(f.dir, "rita", "readonly")
	base := "/api/workspaces/" + f.ws
	cmd := `{"command":"go test ./..."}`
	if code, b, _ := f.do(t, "POST", base+"/why-failed", rita, strings.NewReader(cmd), nil); code != http.StatusForbidden {
		t.Fatalf("a reader runs no command: %d %s", code, b)
	}
	code, b, _ := f.do(t, "POST", base+"/why-failed", alice, strings.NewReader(cmd), nil)
	if code != http.StatusOK {
		t.Fatalf("record: %d %s", code, b)
	}
	rec := decodeMap(t, b)
	id, _ := rec["run_id"].(string)
	if !workspaceops.IsRunOutcomeID(id) || rec["failed"] != true || rec["created"] != true || rec["source"] != "command" ||
		fmt.Sprint(rec["implicated_paths"]) != "[x_test.go]" {
		t.Fatalf("recorded = %s", b)
	}
	code, b, _ = f.do(t, "GET", base+"/runs/"+id+"/why-failed", rita, nil, nil)
	if got := decodeMap(t, b); code != http.StatusOK || got["run_id"] != id || got["status"] != "failed" {
		t.Fatalf("reader GET: %d %s", code, b)
	}
	code, b, _ = f.do(t, "GET", base+"/outcomes?open=true", rita, nil, nil)
	if code != http.StatusOK || !strings.Contains(string(b), id) {
		t.Fatalf("outcome list: %d %s", code, b)
	}
	code, b, _ = f.do(t, "GET", base+"/session-grounding", rita, nil, nil)
	var g struct {
		RecentFailedRuns []string `json:"recent_failed_runs"`
	}
	if err := json.Unmarshal(b, &g); err != nil || code != http.StatusOK || !slices.Contains(g.RecentFailedRuns, id) {
		t.Fatalf("ground lists the core failure: %d %s", code, b)
	}
	// malformed requests are 4xx, never a run
	for body, want := range map[string]int{
		`{}`:                                 http.StatusBadRequest,
		`{"command":"go test","log":"x"}`:    http.StatusBadRequest,
		`{"command":"curl http://x"}`:        http.StatusBadRequest,
		`{"run_id":"run_1"}`:                 http.StatusBadRequest,
		`{"command":"go test","cwd":"../x"}`: http.StatusBadRequest,
		`not json`:                           http.StatusBadRequest,
		`{"evidence_handle":"xm1.nope"}`:     http.StatusBadRequest,
	} {
		if code, b, _ := f.do(t, "POST", base+"/why-failed", alice, strings.NewReader(body), nil); code != want {
			t.Errorf("%s: %d %s, want %d", body, code, b, want)
		}
	}
	if code, _, _ := f.do(t, "GET", base+"/outcomes?limit=0", rita, nil, nil); code != http.StatusBadRequest {
		t.Errorf("limit=0: %d", code)
	}
}

// Commands run on a loopback bind or with XMUSTARD_WHY_FAILED_COMMANDS=1 only; a log
// is explained either way.
func TestWhyFailedCommandsNeedLoopbackOrOptIn(t *testing.T) {
	for _, c := range []struct {
		posture exposurePosture
		want    bool
	}{
		{exposurePosture{Loopback: true}, true}, {exposurePosture{}, false},
		{exposurePosture{Commands: "1"}, true}, {exposurePosture{Loopback: true, Commands: "0"}, false},
	} {
		if got := c.posture.runsCommands(); got != c.want {
			t.Errorf("%+v: runsCommands %v, want %v", c.posture, got, c.want)
		}
	}
	t.Setenv("XMUSTARD_WHY_FAILED_COMMANDS", "yes")
	if _, err := loadExposurePosture(); err == nil {
		t.Fatal("a malformed XMUSTARD_WHY_FAILED_COMMANDS must stop startup")
	}
	off := newOutcomeFixture(t, map[string]string{"XMUSTARD_WHY_FAILED_COMMANDS": ""})
	base := "/api/workspaces/" + off.ws
	code, b, _ := off.do(t, "POST", base+"/why-failed", "", strings.NewReader(`{"command":"go test ./..."}`), nil)
	if code != http.StatusForbidden || decodeMap(t, b)["reason"] != "commands_disabled" {
		t.Fatalf("a non-loopback deployment ran a command: %d %s", code, b)
	}
	if code, b, _ := off.do(t, "POST", base+"/why-failed", "", strings.NewReader(`{"log":"FAIL"}`), nil); code != http.StatusOK {
		t.Fatalf("a log is explained without commands: %d %s", code, b)
	}
}

// Read-only mode refuses running a command and recording outcomes, and keeps reads;
// disabling why_failed refuses both its routes.
func TestWhyFailedRouteHonorsPosture(t *testing.T) {
	ro := newOutcomeFixture(t, map[string]string{"XMUSTARD_READ_ONLY": "1"})
	base := "/api/workspaces/" + ro.ws
	code, b, _ := ro.do(t, "POST", base+"/why-failed", "", strings.NewReader(`{"log":"FAIL"}`), nil)
	if code != http.StatusForbidden || decodeMap(t, b)["reason"] != "read_only" {
		t.Fatalf("read-only POST: %d %s", code, b)
	}
	if code, b, _ := ro.do(t, "GET", base+"/outcomes", "", nil, nil); code != http.StatusOK {
		t.Fatalf("read-only list: %d %s", code, b)
	}
	off := newOutcomeFixture(t, map[string]string{"XMUSTARD_DISABLED_TOOLS": "why_failed"})
	base = "/api/workspaces/" + off.ws
	for _, c := range []struct{ method, path, body string }{
		{"POST", base + "/why-failed", `{"log":"FAIL"}`}, {"GET", base + "/runs/oc_1/why-failed", ""},
	} {
		code, b, _ := off.do(t, c.method, c.path, "", strings.NewReader(c.body), nil)
		if code != http.StatusForbidden || decodeMap(t, b)["reason"] != "tool_disabled" {
			t.Errorf("%s %s with why_failed disabled: %d %s", c.method, c.path, code, b)
		}
	}
}

// A captured failing test run becomes an outcome; the same call captured again for
// retention does not add one; a read-only deployment records none.
func TestCaptureRecordsTestFailureOutcomes(t *testing.T) {
	withCaptureRedactor(t, nil)
	f := newOutcomeFixture(t, nil)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	path := "/api/workspaces/" + f.ws + "/evidence/capture?format=claude&client=claude"
	if code, b, _ := f.do(t, "POST", path, alice, strings.NewReader(claudeBashBody(goTestLog(2000))), nil); code != http.StatusOK {
		t.Fatalf("capture: %d %s", code, b)
	}
	list := func() []workspaceops.RunOutcomeSummary {
		code, b, _ := f.do(t, "GET", "/api/workspaces/"+f.ws+"/outcomes", alice, nil, nil)
		var out struct {
			Outcomes []workspaceops.RunOutcomeSummary `json:"outcomes"`
		}
		if err := json.Unmarshal(b, &out); err != nil || code != http.StatusOK {
			t.Fatalf("list: %d %s", code, b)
		}
		return out.Outcomes
	}
	got := list()
	if len(got) != 1 || got[0].Source != "capture" || !got[0].Failed || got[0].Command != "go test ./..." {
		t.Fatalf("outcomes after a failing capture = %+v", got)
	}
	code, b, _ := f.do(t, "GET", "/api/workspaces/"+f.ws+"/runs/"+got[0].ID+"/why-failed", alice, nil, nil)
	if exp := decodeMap(t, b); code != http.StatusOK || !strings.Contains(fmt.Sprint(exp["error_lines"]), "--- FAIL: TestParse") ||
		fmt.Sprint(exp["failing_tests"]) != "[TestParse]" {
		t.Fatalf("captured outcome explained: %d %s", code, b)
	}
	retain := "/api/workspaces/" + f.ws + "/evidence/capture?format=raw&client=pi&tool=bash&call_id=c9&is_error=true&command=go+test+./...&target=1024"
	if code, b, _ := f.do(t, "POST", retain, alice, strings.NewReader(goTestLog(100)), nil); code != http.StatusOK {
		t.Fatalf("retention capture: %d %s", code, b)
	}
	if n := len(list()); n != 1 {
		t.Fatalf("a retention capture recorded an outcome: %d outcomes", n)
	}

	ro := newOutcomeFixture(t, map[string]string{"XMUSTARD_READ_ONLY": "1"})
	path = "/api/workspaces/" + ro.ws + "/evidence/capture?format=claude&client=claude"
	if code, b, _ := ro.do(t, "POST", path, "", strings.NewReader(claudeBashBody(goTestLog(50))), nil); code != http.StatusOK {
		t.Fatalf("read-only capture: %d %s", code, b)
	}
	code, b, _ = ro.do(t, "GET", "/api/workspaces/"+ro.ws+"/outcomes", "", nil, nil)
	if code != http.StatusOK || strings.Contains(string(b), `"id"`) {
		t.Fatalf("read-only mode recorded an outcome: %d %s", code, b)
	}
}

// Through MCP: an agent's why_failed(log) is delivered like any tool result and
// records an outcome a reader then reads by id; a reader cannot record one.
func TestMCPWhyFailedRecordsAndReads(t *testing.T) {
	srv, _, tokens := mcpServer(t, exposurePosture{}, map[string]string{"alice": "agent", "rita": "readonly"})
	alice := &mcpHTTPClient{t: t, url: srv.URL + "/mcp", token: tokens["alice"]}
	alice.initialize()
	res := alice.call(2, "why_failed", map[string]any{"workspace_id": "ws", "log": "pkg/a.go:3: undefined: Foo\nFAIL\n"})
	sc, _ := res["structuredContent"].(map[string]any)
	id, _ := sc["run_id"].(string)
	if res["isError"] == true || !workspaceops.IsRunOutcomeID(id) || sc["failed"] != true {
		t.Fatalf("agent why_failed(log): %v", res)
	}
	rita := &mcpHTTPClient{t: t, url: srv.URL + "/mcp", token: tokens["rita"]}
	rita.initialize()
	read := rita.call(3, "why_failed", map[string]any{"workspace_id": "ws", "run_id": id})
	if read["isError"] == true || !strings.Contains(resultText(read), "undefined: Foo") {
		t.Fatalf("reader why_failed(run_id): %v", read)
	}
	if denied := rita.call(4, "why_failed", map[string]any{"workspace_id": "ws", "log": "FAIL"}); denied["isError"] != true {
		t.Fatalf("a reader recorded an outcome: %v", denied)
	}
}

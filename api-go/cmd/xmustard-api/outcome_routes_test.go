package main

import (
	"encoding/json"
	"errors"
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

// outcomeFixture is the real handler stack with a fake core whose bounded runner
// counts its invocations.
type outcomeFixture struct {
	*evidenceFixture
	runs string
}

// newOutcomeFixture serves the real handler stack with the operator's opt-in to
// commands (env overrides it) and a counting core (countingCore). The data directory
// and the core are process environment: use one fixture at a time.
func newOutcomeFixture(t *testing.T, env map[string]string) *outcomeFixture {
	t.Helper()
	f := &outcomeFixture{evidenceFixture: &evidenceFixture{ws: "wsOut", dir: t.TempDir()}}
	t.Setenv("XMUSTARD_DATA_DIR", f.dir)
	t.Setenv("XMUSTARD_WHY_FAILED_COMMANDS", "1")
	for k, v := range env {
		t.Setenv(k, v)
	}
	seedCoreWorkspace(t, f.dir, f.ws)
	var core string
	core, f.runs = countingCore(t)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	f.srv = httptest.NewServer(bodyLimitMiddleware(authMiddleware(f.dir, "auto", newAPIHandler())))
	t.Cleanup(f.srv.Close)
	return f
}

// countingCore writes a fake core whose bounded runner appends a line to the returned
// file on every invocation and reports a failing go test; its working changes name
// x_test.go.
func countingCore(t *testing.T) (core, runs string) {
	t.Helper()
	runs = filepath.Join(t.TempDir(), "runner-invocations")
	core = writeScript(t, `case "$1" in
run-managed-command) echo run >> '`+runs+`'; printf '{"command":"go test","cwd":"%s","exit_code":1,"success":false,"timed_out":false,"duration_ms":3,"stdout_excerpt":"--- FAIL: TestX (0.00s)\\n    x_test.go:3: boom\\nFAIL","stderr_excerpt":"","created_at":"2026-09-28T00:00:00Z"}' "$2" ;;
changetrack) printf '{"changed_files":[{"path":"x_test.go"}]}' ;;
*) echo '{}' ;;
esac
`)
	return core, runs
}

// runnerCalls is how many times the fake core's bounded runner was invoked.
func runnerCalls(t *testing.T, runs string) int {
	t.Helper()
	b, err := os.ReadFile(runs)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "run\n")
}

func decodeMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return m
}

// With the operator's opt-in, an admin runs a command; readers read the outcome it
// records, by id, in the outcome list and in ground's recent_failed_runs, without a
// platform run.
func TestWhyFailedRouteRecordsAndReadersRead(t *testing.T) {
	f := newOutcomeFixture(t, nil)
	root, _ := workspaceops.MintToken(f.dir, "root", "admin")
	rita, _ := workspaceops.MintToken(f.dir, "rita", "readonly")
	base := "/api/workspaces/" + f.ws
	cmd := `{"command":"go test ./..."}`
	if code, b, _ := f.do(t, "POST", base+"/why-failed", rita, strings.NewReader(cmd), nil); code != http.StatusForbidden {
		t.Fatalf("a reader runs no command: %d %s", code, b)
	}
	code, b, _ := f.do(t, "POST", base+"/why-failed", root, strings.NewReader(cmd), nil)
	if code != http.StatusOK {
		t.Fatalf("record: %d %s", code, b)
	}
	rec := decodeMap(t, b)
	id, _ := rec["run_id"].(string)
	if !workspaceops.IsRunOutcomeID(id) || rec["failed"] != true || rec["created"] != true || rec["source"] != "command" ||
		fmt.Sprint(rec["implicated_paths"]) != "[x_test.go]" || runnerCalls(t, f.runs) != 1 {
		t.Fatalf("recorded = %s after %d runner calls", b, runnerCalls(t, f.runs))
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
		if code, b, _ := f.do(t, "POST", base+"/why-failed", root, strings.NewReader(body), nil); code != want {
			t.Errorf("%s: %d %s, want %d", body, code, b, want)
		}
	}
	if n := runnerCalls(t, f.runs); n != 1 {
		t.Fatalf("malformed requests reached the runner: %d calls", n)
	}
	if code, _, _ := f.do(t, "GET", base+"/outcomes?limit=0", rita, nil, nil); code != http.StatusBadRequest {
		t.Errorf("limit=0: %d", code)
	}
}

// Command mode is off by default and admin-only. The operator's opt-in is read at
// startup (0|1, off when unset, never implied by a loopback bind), and every refused
// request answers before the bounded runner is invoked (the fake core counts its runs),
// whoever sends it and whatever else it carries: with the opt-in off, for an agent, a
// reader or an open-mode caller with it on, and for a command mixed with a log or an
// evidence handle.
func TestWhyFailedDeniedCommandsNeverReachTheRunner(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "1": true} {
		t.Setenv("XMUSTARD_WHY_FAILED_COMMANDS", v)
		if p, err := loadExposurePosture(); err != nil || p.runsCommands() != want {
			t.Fatalf("XMUSTARD_WHY_FAILED_COMMANDS=%q: runsCommands %v, %v; want %v", v, p.runsCommands(), err, want)
		}
	}
	t.Setenv("XMUSTARD_WHY_FAILED_COMMANDS", "yes")
	if _, err := loadExposurePosture(); err == nil {
		t.Fatal("a malformed XMUSTARD_WHY_FAILED_COMMANDS must stop startup")
	}
	if (&exposurePosture{Loopback: true}).runsCommands() || (*exposurePosture)(nil).runsCommands() {
		t.Fatal("a loopback bind or a missing posture must not enable commands")
	}

	type refusal struct {
		name, token, body string
		status            int
		reason            string
	}
	cmd := `{"command":"go test ./..."}`
	mixed := []string{
		`{"command":"go test ./...","log":"FAIL"}`, `{"command":"go test ./...","evidence_handle":"xm1.x"}`,
		`{"argv":["go","test","./..."],"log":"FAIL"}`, `{"argv":["go","test"],"evidence_handle":"xm1.x"}`,
		`{"command":"go test ./...","argv":["go","vet"]}`, `{"command":"go test ./...","run_id":"run_1"}`,
		`{"log":"FAIL","cwd":"pkg"}`, `{"evidence_handle":"xm1.x","timeout_seconds":5}`,
	}
	check := func(f *outcomeFixture, cases []refusal) {
		t.Helper()
		for _, c := range cases {
			code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/why-failed", c.token, strings.NewReader(c.body), nil)
			if code != c.status || (c.reason != "" && decodeMap(t, b)["reason"] != c.reason) {
				t.Errorf("%s %s: %d %s, want %d %s", c.name, c.body, code, b, c.status, c.reason)
			}
		}
		if n := runnerCalls(t, f.runs); n != 0 {
			t.Fatalf("refused requests invoked the runner %d times", n)
		}
		code, b, _ := f.do(t, "GET", "/api/workspaces/"+f.ws+"/outcomes", cases[0].token, nil, nil)
		if code != http.StatusOK || strings.Contains(string(b), `"id"`) {
			t.Fatalf("refused requests recorded an outcome: %d %s", code, b)
		}
	}
	mixedFor := func(token string) []refusal {
		var out []refusal
		for _, body := range mixed {
			out = append(out, refusal{"mixed", token, body, http.StatusBadRequest, ""})
		}
		return out
	}

	// the default: the operator did not opt in; even an admin runs nothing
	off := newOutcomeFixture(t, map[string]string{"XMUSTARD_WHY_FAILED_COMMANDS": ""})
	offRoot, _ := workspaceops.MintToken(off.dir, "root", "admin")
	offAlice, _ := workspaceops.MintToken(off.dir, "alice", "agent")
	check(off, append([]refusal{
		{"opt-in off, admin", offRoot, cmd, http.StatusForbidden, "commands_disabled"},
		{"opt-in off, admin, argv", offRoot, `{"argv":["go","test","./..."]}`, http.StatusForbidden, "commands_disabled"},
		{"opt-in off, agent", offAlice, cmd, http.StatusForbidden, "commands_disabled"},
	}, mixedFor(offRoot)...))

	// no credentials (open mode): no authenticated admin
	open := newOutcomeFixture(t, nil)
	check(open, append([]refusal{{"opt-in on, open mode", "", cmd, http.StatusForbidden, "admin_required"}}, mixedFor("")...))

	// the opt-in on: agents and readers still run nothing; mixed requests never run
	on := newOutcomeFixture(t, nil)
	root, _ := workspaceops.MintToken(on.dir, "root", "admin")
	alice, _ := workspaceops.MintToken(on.dir, "alice", "agent")
	rita, _ := workspaceops.MintToken(on.dir, "rita", "readonly")
	check(on, append([]refusal{
		{"opt-in on, agent", alice, cmd, http.StatusForbidden, "missing_role"},
		{"opt-in on, agent, argv", alice, `{"argv":["go","test","./..."]}`, http.StatusForbidden, "missing_role"},
		{"opt-in on, reader", rita, cmd, http.StatusForbidden, "missing_role"},
	}, mixedFor(root)...))
	code, b, _ := on.do(t, "POST", "/api/workspaces/"+on.ws+"/why-failed", root, strings.NewReader(cmd), nil)
	if rec := decodeMap(t, b); code != http.StatusOK || rec["source"] != "command" || runnerCalls(t, on.runs) != 1 {
		t.Fatalf("an admin with the opt-in: %d %s after %d runner calls", code, b, runnerCalls(t, on.runs))
	}
}

// With command mode off (the default), a pasted log and an evidence handle each record
// an outcome in the governance store, readable by id and listed by ground in core-only
// mode, and neither reaches the runner.
func TestWhyFailedLogAndEvidenceRecordWithoutCommands(t *testing.T) {
	withCaptureRedactor(t, nil)
	f := newOutcomeFixture(t, map[string]string{"XMUSTARD_WHY_FAILED_COMMANDS": ""})
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	rita, _ := workspaceops.MintToken(f.dir, "rita", "readonly")
	base := "/api/workspaces/" + f.ws
	// a failing read of a build log, retained as an original; a read is not a test,
	// build or lint family, so the capture itself records no outcome
	capture := base + "/evidence/capture?format=raw&client=pi&tool=bash&call_id=c1&is_error=true&command=cat+build.log"
	code, b, _ := f.do(t, "POST", capture, alice, strings.NewReader(goTestLog(2000)), nil)
	captured := decodeMap(t, b)
	handle, _ := captured["handle"].(string)
	if code != http.StatusOK || handle == "" || captured["family"] != "read" {
		t.Fatalf("capture: %d family %v handle %q", code, captured["family"], handle)
	}
	var ids []string
	for _, body := range []string{`{"log":"pkg/a.go:3: undefined: Foo\nFAIL\n"}`, `{"evidence_handle":"` + handle + `"}`} {
		code, b, _ := f.do(t, "POST", base+"/why-failed", alice, strings.NewReader(body), nil)
		rec := decodeMap(t, b)
		id, _ := rec["run_id"].(string)
		if code != http.StatusOK || !workspaceops.IsRunOutcomeID(id) || rec["failed"] != true || rec["created"] != true {
			t.Fatalf("%s: %d %s", body, code, b)
		}
		ids = append(ids, id)
	}
	stored, err := workspaceops.ListRunOutcomes(t.Context(), f.dir, f.ws, true, 0)
	if err != nil || len(stored) != 2 || stored[0].Source != "evidence" || stored[1].Source != "log" || stored[0].Principal != "alice" {
		t.Fatalf("govstore outcomes = %+v, %v", stored, err)
	}
	code, b, _ = f.do(t, "GET", base+"/session-grounding", rita, nil, nil)
	var g struct {
		RecentFailedRuns []string `json:"recent_failed_runs"`
	}
	if err := json.Unmarshal(b, &g); err != nil || code != http.StatusOK || !slices.Contains(g.RecentFailedRuns, ids[0]) || !slices.Contains(g.RecentFailedRuns, ids[1]) {
		t.Fatalf("ground lists the core failures %v: %d %s", ids, code, b)
	}
	if code, b, _ := f.do(t, "GET", "/api/auth/whoami", rita, nil, nil); code != http.StatusOK || decodeMap(t, b)["profile"] != "core" {
		t.Fatalf("the fixture is not core-only: %d %s", code, b)
	}
	if n := runnerCalls(t, f.runs); n != 0 {
		t.Fatalf("a log or an evidence handle invoked the runner %d times", n)
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

// Through MCP, the command argument meets the same guards as the HTTP route: with the
// operator's opt-in, an agent's command and a command mixed with a log are refused
// before the runner is invoked; an admin's runs.
func TestMCPWhyFailedCommandsNeedAnAdmin(t *testing.T) {
	srv, _, tokens := mcpServer(t, exposurePosture{Commands: true}, map[string]string{"alice": "agent", "root": "admin"})
	core, runs := countingCore(t)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	alice := &mcpHTTPClient{t: t, url: srv.URL + "/mcp", token: tokens["alice"]}
	alice.initialize()
	root := &mcpHTTPClient{t: t, url: srv.URL + "/mcp", token: tokens["root"]}
	root.initialize()
	for i, c := range []struct {
		client *mcpHTTPClient
		args   map[string]any
	}{
		{alice, map[string]any{"command": "go test ./..."}},
		{root, map[string]any{"command": "go test ./...", "log": "FAIL"}},
		{root, map[string]any{"command": "go test ./...", "evidence_handle": "xm1.x"}},
	} {
		c.args["workspace_id"] = "ws"
		if res := c.client.call(10+i, "why_failed", c.args); res["isError"] != true {
			t.Errorf("%v: %v, want a refusal", c.args, res)
		}
	}
	if n := runnerCalls(t, runs); n != 0 {
		t.Fatalf("refused MCP calls invoked the runner %d times", n)
	}
	res := root.call(20, "why_failed", map[string]any{"workspace_id": "ws", "command": "go test ./..."})
	if sc, _ := res["structuredContent"].(map[string]any); res["isError"] == true || sc["source"] != "command" || runnerCalls(t, runs) != 1 {
		t.Fatalf("an admin's command through MCP: %v after %d runner calls", res, runnerCalls(t, runs))
	}
}

// Revoking a captured original removes the outcome made from it, for every reader; an
// admin removes any other outcome, and nobody else can.
func TestOutcomesGoWithRevokedEvidenceAndAdminDelete(t *testing.T) {
	withCaptureRedactor(t, nil)
	f := newOutcomeFixture(t, nil)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	root, _ := workspaceops.MintToken(f.dir, "root", "admin")
	base := "/api/workspaces/" + f.ws
	if code, b, _ := f.do(t, "POST", base+"/evidence/capture?format=claude&client=claude", alice, strings.NewReader(claudeBashBody(goTestLog(2000))), nil); code != http.StatusOK {
		t.Fatalf("capture: %d %s", code, b)
	}
	outcomes := func() []workspaceops.RunOutcomeSummary {
		code, b, _ := f.do(t, "GET", base+"/outcomes", alice, nil, nil)
		var out struct {
			Outcomes []workspaceops.RunOutcomeSummary `json:"outcomes"`
		}
		if err := json.Unmarshal(b, &out); err != nil || code != http.StatusOK {
			t.Fatalf("list: %d %s", code, b)
		}
		return out.Outcomes
	}
	got := outcomes()
	if len(got) != 1 {
		t.Fatalf("outcomes after a failing capture = %+v", got)
	}
	_, b, _ := f.do(t, "GET", base+"/runs/"+got[0].ID+"/why-failed", alice, nil, nil)
	handle, _ := decodeMap(t, b)["evidence_handle"].(string)
	if handle == "" {
		t.Fatalf("the capture retained no original: %s", b)
	}
	if code, b, _ := f.do(t, "DELETE", base+"/evidence/"+handle, alice, nil, nil); code != http.StatusOK {
		t.Fatalf("revoke: %d %s", code, b)
	}
	if got := outcomes(); len(got) != 0 {
		t.Fatalf("the revoked original's outcome survived: %+v", got)
	}

	code, b, _ := f.do(t, "POST", base+"/why-failed", alice, strings.NewReader(`{"log":"FAIL: leaked"}`), nil)
	if code != http.StatusOK {
		t.Fatalf("log: %d %s", code, b)
	}
	id, _ := decodeMap(t, b)["run_id"].(string)
	if code, _, _ := f.do(t, "DELETE", base+"/outcomes/"+id, alice, nil, nil); code != http.StatusForbidden {
		t.Fatalf("an agent deleted an outcome: %d", code)
	}
	if code, b, _ := f.do(t, "DELETE", base+"/outcomes/"+id, root, nil, nil); code != http.StatusOK {
		t.Fatalf("admin delete: %d %s", code, b)
	}
	if code, _, _ := f.do(t, "DELETE", base+"/outcomes/"+id, root, nil, nil); code != http.StatusNotFound {
		t.Fatalf("deleting it again: %d", code)
	}
	if code, _, _ := f.do(t, "GET", base+"/runs/"+id+"/why-failed", alice, nil, nil); code != http.StatusNotFound {
		t.Fatalf("a deleted outcome is still read: %d", code)
	}
}

// A command outside the table, or a program that cannot start, is the caller's error
// (400), never a retryable runner failure, and never reaches the runner.
func TestWhyFailedRouteRefusesCommandsItCannotRun(t *testing.T) {
	f := newOutcomeFixture(t, nil)
	root, _ := workspaceops.MintToken(f.dir, "root", "admin")
	for _, body := range []string{
		`{"command":"go run github.com/evil/x@latest"}`, `{"command":"make -f /tmp/evil.mk test"}`,
		`{"command":"npm install evil-pkg"}`, `{"command":"pytest /tmp/evil_test.py"}`,
		`{"argv":["./gradlew","test"]}`, `{"command":"go test ./... 2>/dev/null"}`,
		`{"command":"go test -mod=mod github.com/evil/x"}`, `{"command":"mvn org.evil:plugin:1.0:test"}`,
		`{"argv":["./node_modules/.bin/jest"]}`,
	} {
		if code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/why-failed", root, strings.NewReader(body), nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", body, code, b)
		}
	}
	t.Setenv("PATH", t.TempDir()) // no test runner on PATH
	code, b, _ := f.do(t, "POST", "/api/workspaces/"+f.ws+"/why-failed", root, strings.NewReader(`{"command":"pytest -x"}`), nil)
	if code != http.StatusBadRequest || !strings.Contains(string(b), "not on the server's PATH") {
		t.Fatalf("a program not on PATH: %d %s, want 400", code, b)
	}
	if n := runnerCalls(t, f.runs); n != 0 {
		t.Fatalf("refused commands invoked the runner %d times", n)
	}
}

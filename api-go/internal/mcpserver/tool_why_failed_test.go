package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

// why_failed reads a run or an outcome by id with GET; a command, log or evidence
// handle goes to the recording route in the body (never the URL), and a run_id sent
// with a source travels along so the API refuses the mix instead of ignoring it.
func TestWhyFailedBuildsReadsAndRecordingCalls(t *testing.T) {
	cases := []struct {
		args       map[string]string
		method     string
		path, body string
	}{
		{map[string]string{"workspace_id": "w", "run_id": "oc_1"}, "GET", "/api/workspaces/w/runs/oc_1/why-failed", ""},
		{map[string]string{"workspace_id": "w", "command": "go test ./x", "cwd": "api-go", "timeout_seconds": "30"},
			"POST", "/api/workspaces/w/why-failed", `{"command":"go test ./x","cwd":"api-go","timeout_seconds":30}`},
		{map[string]string{"workspace_id": "w", "log": "FAIL x"}, "POST", "/api/workspaces/w/why-failed", `{"log":"FAIL x"}`},
		{map[string]string{"workspace_id": "w", "evidence_handle": "xm1.a", "run_id": "r"},
			"POST", "/api/workspaces/w/why-failed", `{"evidence_handle":"xm1.a","run_id":"r"}`},
		{map[string]string{"workspace_id": "w"}, "POST", "/api/workspaces/w/why-failed", `{}`},
	}
	for _, c := range cases {
		method, path, body := whyFailedTool.Build(c.args)
		if method != c.method || path != c.path {
			t.Errorf("%v: %s %s, want %s %s", c.args, method, path, c.method, c.path)
		}
		if c.body == "" {
			if body != "" {
				t.Errorf("%v: a read carries no body, got %s", c.args, body)
			}
			continue
		}
		var got, want map[string]any
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("%v: body %q: %v", c.args, body, err)
		}
		_ = json.Unmarshal([]byte(c.body), &want)
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		if string(gb) != string(wb) {
			t.Errorf("%v: body %s, want %s", c.args, gb, wb)
		}
	}
}

// A read-only connection lists why_failed and serves its reads, but refuses a call
// that would run a command or record an outcome before it reaches the API.
func TestReadOnlyConnectionServesWhyFailedReadsOnly(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{ReadOnly: true}, nil, LatestProtocolVersion)
	res, _ := s.Handle(t.Context(), "tools/list", nil)
	listed := false
	for _, tool := range res.(map[string]any)["tools"].([]map[string]any) {
		listed = listed || tool["name"] == "why_failed"
	}
	if !listed {
		t.Fatal("read-only tools/list must offer why_failed for run_id reads")
	}
	if out, rerr := call(t, s, "why_failed", map[string]any{"workspace_id": "ws", "run_id": "oc_1"}); rerr != nil || out["isError"] == true {
		t.Fatalf("a run_id read is served on a read-only connection: %v %v", rerr, out)
	}
	for _, arg := range []string{"command", "log", "evidence_handle"} {
		out, rerr := call(t, s, "why_failed", map[string]any{"workspace_id": "ws", arg: "go test ./..."})
		if rerr != nil || out["isError"] != true || !strings.Contains(text(out), "read-only") {
			t.Fatalf("%s: a writing call must be refused: %v %v", arg, rerr, out)
		}
	}
	if n := api.count("POST", "/api/workspaces/ws/why-failed"); n != 0 {
		t.Fatalf("a refused why_failed write reached the API %d times", n)
	}
	if n := api.count("GET", "/api/workspaces/ws/runs/oc_1/why-failed"); n != 1 {
		t.Fatalf("the read reached the API %d times, want 1", n)
	}
}

// The command timeout stops below the MCP clients' 60 s call timeout: a larger value
// is rejected, not clamped.
func TestWhyFailedTimeoutStaysUnderTheClientCallTimeout(t *testing.T) {
	s := newSession(t, &fakeAPI{}, Options{}, nil, LatestProtocolVersion)
	if _, d := argErr(t, s, "why_failed", map[string]any{"workspace_id": "ws", "command": "go test", "timeout_seconds": 55}); d["argument"] != "timeout_seconds" {
		t.Fatalf("a timeout past the client's call timeout must be rejected: %v", d)
	}
	if maxWhyFailedTimeout >= 60 {
		t.Fatalf("maxWhyFailedTimeout %d must stay below the 60 s client call timeout", maxWhyFailedTimeout)
	}
}

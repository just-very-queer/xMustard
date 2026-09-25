package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/groundbudget"
	"xmustard/api-go/internal/workspaceops"
)

// groundAPI serves the route table over a fake core whose working-changes answer
// carries breaks contract breaks, with failed runs seeded in the workspace. Every core
// invocation is appended to the returned log.
func groundAPI(t *testing.T, breaks, runs int) (base, coreLog string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	seedCoreWorkspace(t, dir, "ws")
	var dirty []map[string]any
	for i := 0; i < breaks; i++ {
		dirty = append(dirty, map[string]any{"path": fmt.Sprintf("internal/pkg/file_%04d.go", i), "symbol": fmt.Sprintf("Func%04d", i),
			"contract_break": true, "signature_change": "fn(a int) -> fn(a, b int)"})
	}
	changes, _ := json.Marshal(map[string]any{"changed_files": []string{"a.go"}, "contract_breaks": breaks, "dirty_symbols": dirty})
	drift := `{"workspace_id":"ws","has_baseline":true,"stale":true,"head_changed":true,"content_changed":false,"reasons":["HEAD moved"]}`
	fixtures := t.TempDir()
	for name, body := range map[string]string{"changes.json": string(changes), "drift.json": drift} {
		if err := os.WriteFile(filepath.Join(fixtures, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	coreLog = filepath.Join(t.TempDir(), "core.log")
	t.Setenv("XMUSTARD_CORE_BIN", writeScript(t, `echo "$@" >> `+coreLog+`
case "$1 $2" in
"changetrack drift") cat `+filepath.Join(fixtures, "drift.json")+` ;;
"changetrack working-changes") cat `+filepath.Join(fixtures, "changes.json")+` ;;
*) echo '{}' ;;
esac
`))
	runsDir := filepath.Join(dir, "workspaces", "ws", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < runs; i++ {
		id := fmt.Sprintf("run_%04d_9f3c2a7e41d8", i)
		rec, _ := json.Marshal(map[string]any{"run_id": id, "workspace_id": "ws", "status": "failed",
			"created_at": fmt.Sprintf("2026-09-25T00:%02d:%02dZ", i/60, i%60)})
		if err := os.WriteFile(filepath.Join(runsDir, id+".json"), rec, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(bodyLimitMiddleware(authMiddleware(dir, "auto", newAPIHandler())))
	t.Cleanup(srv.Close)
	return srv.URL + "/api/workspaces/ws/session-grounding", coreLog
}

func getGround(t *testing.T, url string) (int, []byte, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("ground answered non-JSON (%d): %s", resp.StatusCode, body)
	}
	return resp.StatusCode, body, m
}

// A bad budget is rejected with the argument and its bounds, before any core work;
// nothing is clamped.
func TestGroundRejectsBadBudgetBeforeWork(t *testing.T) {
	base, coreLog := groundAPI(t, 0, 0)
	for _, tc := range []struct{ query, arg string }{
		{"max_chars=10", "max_chars"},
		{"max_chars=65537", "max_chars"},
		{"max_chars=lots", "max_chars"},
		{"sections=runs,secrets", "sections"},
		{"max_chars=3000&max_chars=9000", "max_chars"},
	} {
		code, _, m := getGround(t, base+"?"+tc.query)
		if code != http.StatusBadRequest || m["argument"] != tc.arg {
			t.Fatalf("%s: want 400 naming %s, got %d %v", tc.query, tc.arg, code, m)
		}
	}
	if _, err := os.Stat(coreLog); !os.IsNotExist(err) {
		b, _ := os.ReadFile(coreLog)
		t.Fatalf("a rejected budget must not start core work; core ran: %s", b)
	}
	_, _, m := getGround(t, base+"?max_chars=1")
	if m["minimum"] != float64(groundbudget.MinMaxChars) || m["maximum"] != float64(groundbudget.MaxMaxChars) {
		t.Fatalf("the rejection must state the bounds: %v", m)
	}
}

// Without arguments the default budget holds a large ground to 6000 characters and
// says what it trimmed; a section requested alone comes back in full.
func TestGroundFitsDefaultBudgetAndPagesSections(t *testing.T) {
	base, _ := groundAPI(t, 150, 120)
	code, body, m := getGround(t, base)
	if code != http.StatusOK {
		t.Fatalf("ground: %d %s", code, body)
	}
	if len(body) > groundbudget.DefaultMaxChars+1 { // +1: the trailing newline
		t.Fatalf("default ground is %d bytes, over %d", len(body), groundbudget.DefaultMaxChars)
	}
	rep := m[groundbudget.ReportMember].(map[string]any)
	if rep["used_chars"] != float64(len(strings.TrimSpace(string(body)))) || rep["degraded_stage"] == "full" {
		t.Fatalf("report: %v", rep)
	}
	secs := rep["sections"].(map[string]any)
	idx := secs["index"].(map[string]any)
	if idx["total"].(map[string]any)["broken_contracts"] != 150.0 {
		t.Fatalf("index must count the broken contracts it left out: %v", idx)
	}
	runs := secs["runs"].(map[string]any)
	if runs["total"].(map[string]any)["recent_failed_runs"] != 120.0 {
		t.Fatalf("runs must count the failed runs it left out: %v", runs)
	}
	// the signals stay: counts and flags survive the trim
	if m["contract_breaks"] != 150.0 || m["blocked_by_failing_verification"] != true || m["principal"] == nil {
		t.Fatalf("signals lost: %v", m)
	}
	// newest run first
	if got := m["recent_failed_runs"].([]any); len(got) == 0 || got[0] != "run_0119_9f3c2a7e41d8" {
		t.Fatalf("trim keeps the newest runs: %v", got)
	}

	// one section, uncapped: every failed run
	_, _, m = getGround(t, base+"?sections=runs&max_chars=65536")
	if got := m["recent_failed_runs"].([]any); len(got) != 120 {
		t.Fatalf("sections=runs alone must return all 120 runs, got %d", len(got))
	}
	rep = m[groundbudget.ReportMember].(map[string]any)
	if _, ok := m["broken_contracts"]; ok {
		t.Fatal("index was not requested")
	}
	sig := rep["signals"].(map[string]any)
	if sig["contract_breaks"] != 150.0 || sig["drift.stale"] != true || sig["broken_contracts"] != 150.0 {
		t.Fatalf("unrequested sections must keep their signals: %v", sig)
	}
	if !slices.Equal(anyStrings(rep["not_requested"]), []string{"index", "memory", "drift", "principal"}) {
		t.Fatalf("not_requested: %v", rep["not_requested"])
	}
	// a repeated parameter selects each value
	_, _, m = getGround(t, base+"?sections=runs&sections=memory")
	rep = m[groundbudget.ReportMember].(map[string]any)
	if !slices.Equal(anyStrings(rep["not_requested"]), []string{"index", "drift", "principal"}) || m["stale_memory"] == nil {
		t.Fatalf("repeated sections: not_requested %v", rep["not_requested"])
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

// Every member the ground route returns belongs to a declared section, so the ladder
// knows its rank and whether it is a signal. A new member must be declared in
// groundbudget/sections.go.
func TestGroundMembersAreDeclaredInSections(t *testing.T) {
	g := &workspaceops.SessionGrounding{}
	fillNonZero(reflect.ValueOf(g).Elem())
	req := httptest.NewRequest("GET", "/api/workspaces/ws/session-grounding", nil)
	raw, err := json.Marshal(groundResponse(req, g))
	if err != nil {
		t.Fatal(err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatal(err)
	}
	if len(members) < 10 {
		t.Fatalf("expected every member populated, got %d: %s", len(members), raw)
	}
	for name := range members {
		if groundbudget.SectionOf(name) == "" {
			t.Errorf("ground member %q is in no section: declare it in internal/groundbudget/sections.go", name)
		}
	}
}

// fillNonZero sets every exported field (through embedded structs) to a non-zero
// value, so omitempty members are emitted too.
func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillNonZero(v.Field(i))
			}
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillNonZero(v.Elem())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 { // json.RawMessage
			v.SetBytes([]byte(`{"k":1}`))
			return
		}
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillNonZero(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fillNonZero(k)
		fillNonZero(e)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(1)
	}
}

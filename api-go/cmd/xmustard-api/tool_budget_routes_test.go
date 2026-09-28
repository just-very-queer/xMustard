package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// budgetServer serves the real handler stack over workerWorkspace's repository with
// the real core (one-shot calls, no resident worker).
func budgetServer(t *testing.T) (base string) {
	t.Helper()
	core := realCoreBinary(t)
	_, dir, ws := workerWorkspace(t)
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	srv := httptest.NewServer(bodyLimitMiddleware(authMiddleware(dir, "auto", newAPIHandler())))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { waitRegistrationBaselines(t) })
	return srv.URL + "/api/workspaces/" + ws
}

func budgetGet(t *testing.T, u string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	return call(t, "GET", u, "", "", hdr)
}

func report(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	rep, ok := out["output_budget"].(map[string]any)
	if !ok {
		t.Fatalf("no output_budget: %v", out)
	}
	return rep
}

// The budget is opt-in over HTTP (the Pi adapter and the UI keep their results), and a
// budgeted search, explain or impact ends with output_budget.
func TestToolBudgetIsOptInOverHTTP(t *testing.T) {
	base := budgetServer(t)
	for _, route := range []string{"/search?q=ComputeTotal", "/explain-path?path=src%2Fengine.go", "/changes/since-index?symbol=ComputeTotal"} {
		code, out := budgetGet(t, base+route, nil)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %v", route, code, out)
		}
		if _, ok := out["output_budget"]; ok {
			t.Fatalf("%s: budgeted without max_chars: %v", route, out)
		}
		code, out = budgetGet(t, base+route+"&max_chars=2000", nil)
		if code != http.StatusOK {
			t.Fatalf("%s budgeted: %d %v", route, code, out)
		}
		rep := report(t, out)
		if rep["max_chars"] != float64(2000) || rep["degraded_stage"] == nil {
			t.Fatalf("%s: report %v", route, rep)
		}
		if rep["over_budget"] != true && rep["used_chars"].(float64) > 2000 {
			t.Fatalf("%s: %v chars over 2000 without over_budget", route, rep["used_chars"])
		}
		if st, _ := rep["stages"].([]any); len(st) > 0 && !strings.HasPrefix(rep["recover_unavailable"].(string), "not a delivered call") {
			t.Fatalf("%s: a plain HTTP call is told how to get the original: %v", route, rep)
		}
	}
	// out-of-range values are rejected, never clamped, naming the argument
	for _, q := range []string{"/search?q=x&max_chars=1999", "/explain-path?path=src&max_chars=70000", "/explain-path?path=src&limit=0",
		"/explain-path?symbol=ComputeTotal&limit=2", "/changes/since-index?max_chars=abc", "/search?q=x&max_chars=2000&max_chars=3000"} {
		code, out := budgetGet(t, base+q, nil)
		if code != http.StatusBadRequest || out["argument"] == nil {
			t.Errorf("%s: want 400 naming the argument, got %d %v", q, code, out)
		}
	}
}

// explain answers a directory with its outline and pages its files; a cursor from
// another list is refused.
func TestExplainPagesADirectory(t *testing.T) {
	base := budgetServer(t)
	code, out := budgetGet(t, base+"/explain-path?path=src", nil)
	if code != http.StatusOK || out["kind"] != "directory" || out["files_total"] != float64(2) {
		t.Fatalf("directory: %d %v", code, out)
	}
	if !strings.Contains(out["summary"].(string), "2 indexed file(s)") {
		t.Fatalf("summary: %v", out["summary"])
	}
	code, first := budgetGet(t, base+"/explain-path?path=src&limit=1", nil)
	files, _ := first["files"].([]any)
	if code != http.StatusOK || len(files) != 1 || first["has_more"] != true || first["next_cursor"] == nil {
		t.Fatalf("first page: %d %v", code, first)
	}
	if _, ok := first["output_budget"]; ok {
		t.Fatal("paging alone adds no report")
	}
	cursor := url.QueryEscape(first["next_cursor"].(string))
	_, second := budgetGet(t, base+"/explain-path?path=src&limit=1&cursor="+cursor, nil)
	files2, _ := second["files"].([]any)
	if len(files2) != 1 || second["has_more"] != false || second["offset"] != float64(1) {
		t.Fatalf("second page: %v", second)
	}
	if files[0].(map[string]any)["path"] == files2[0].(map[string]any)["path"] {
		t.Fatal("pages must be disjoint")
	}
	if code, out := budgetGet(t, base+"/explain-path?path=src%2Fengine.go&cursor="+cursor, nil); code != http.StatusBadRequest {
		t.Fatalf("a cursor of another list: %d %v", code, out)
	}
	// a file answers its explanation with its outline
	_, f := budgetGet(t, base+"/explain-path?path=src%2Fengine.go", nil)
	syms, _ := f["symbols"].([]any)
	if len(syms) != 2 || f["symbols_by_kind"] == nil {
		t.Fatalf("file outline: %v", f)
	}
	if code, out := budgetGet(t, base+"/explain-path?path=nope", nil); code != http.StatusNotFound {
		t.Fatalf("missing path: %d %v", code, out)
	}
	if code, out := budgetGet(t, base+"/explain-path", nil); code != http.StatusBadRequest {
		t.Fatalf("no target: %d %v", code, out)
	}
}

// explain symbol= answers a symbol's context; search mode=symbol looks a name path up.
func TestSymbolNavigationRoutes(t *testing.T) {
	base := budgetServer(t)
	code, out := budgetGet(t, base+"/explain-path?symbol=ComputeTotal", nil)
	if code != http.StatusOK || out["found"] != true {
		t.Fatalf("context: %d %v", code, out)
	}
	incoming, _ := out["incoming"].([]any)
	if len(incoming) == 0 || incoming[0].(map[string]any)["name_path"] != "HandleRequest" {
		t.Fatalf("HandleRequest calls ComputeTotal: %v", out["incoming"])
	}
	if _, ok := out["memories"]; !ok {
		t.Fatalf("memories are attached (empty here): %v", out)
	}
	uid := out["symbol"].(map[string]any)["uid"].(string)
	if _, byUID := budgetGet(t, base+"/explain-path?uid="+url.QueryEscape(uid), nil); byUID["found"] != true {
		t.Fatalf("by uid: %v", byUID)
	}
	code, s := budgetGet(t, base+"/search?mode=symbol&q=ComputeTotal&include_info=true", nil)
	syms, _ := s["symbols"].([]any)
	if code != http.StatusOK || len(syms) != 1 {
		t.Fatalf("symbol search: %d %v", code, s)
	}
	info := syms[0].(map[string]any)["info"].(map[string]any)
	if !strings.HasPrefix(info["signature"].(string), "func ComputeTotal(a int) int") {
		t.Fatalf("info: %v", info)
	}
	if code, _ := budgetGet(t, base+"/search?mode=symbol&q=a%2F%2Fb", nil); code != http.StatusBadRequest {
		t.Fatalf("a malformed name path: %d", code)
	}
}

// A delivered (MCP) call that the budget reduced retains the unbudgeted result as
// evidence and names it in output_budget.
func TestBudgetedDeliveredCallNamesItsRecovery(t *testing.T) {
	base := budgetServer(t)
	code, out := budgetGet(t, base+"/search?q=ComputeTotal&max_chars=2000", deliver)
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out["projection"].(string)), &result); err != nil {
		t.Fatalf("projection: %v %v", err, out)
	}
	rep := report(t, result)
	if st, _ := rep["stages"].([]any); len(st) == 0 {
		t.Skipf("the result fit 2000 chars on this fixture: %v", rep)
	}
	if uri, _ := rep["recover_uri"].(string); !strings.HasPrefix(uri, "xmustard://evidence/") || rep["recover_handle"] == nil {
		t.Fatalf("recovery: %v", rep)
	}
}

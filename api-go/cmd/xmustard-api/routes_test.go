package main

import (
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"testing"
)

// registerRoutes mounts exactly the rows of the area tables: each row once, with a
// handler, and routed to itself, so no route is lost from a table or registered
// around one. With TestRouteGateTableMatchesRegisteredRoutes this ties the tables, the
// served routes and the gate table together.
func TestRegisteredRoutesAreTheRouteTables(t *testing.T) {
	t.Setenv("XMUSTARD_DATA_DIR", t.TempDir())
	var rows []string
	for _, area := range apiRouteAreas() {
		for _, s := range area {
			if s.handler == nil {
				t.Errorf("route %s has no handler", s.pattern)
			}
			rows = append(rows, s.pattern)
		}
	}
	sort.Strings(rows)
	for i := 1; i < len(rows); i++ {
		if rows[i] == rows[i-1] {
			t.Fatalf("route %s is in the tables twice", rows[i])
		}
	}

	mux := newGatedMux()
	registerRoutes(mux)
	var registered []string
	for p := range mux.gates {
		registered = append(registered, p)
	}
	sort.Strings(registered)
	if !slices.Equal(registered, rows) {
		t.Fatalf("registered routes differ from the tables:\nregistered %v\ntables     %v", registered, rows)
	}

	param := regexp.MustCompile(`\{[^}]+\}`)
	for _, p := range rows {
		method := patternMethod(p)
		if method == "" {
			method = "GET"
		}
		req := httptest.NewRequest(method, param.ReplaceAllString(patternPath(p), "x1"), nil)
		if _, matched := mux.Handler(req); matched != p {
			t.Errorf("a %s request to %s is routed to %q, not its own row %q", method, req.URL.Path, matched, p)
		}
	}
}

package main

import (
	"cmp"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Route registration (WS-CQ-01). Each area of the API keeps its routes in a table
// in its own <area>_routes.go file, and registerRoutes mounts every table through
// one loop. A row is the route's ServeMux pattern and its handler. The route's gate
// (profile, role, tool, read-only safety; WS-09) is the row routeGateTable keeps
// under the same pattern in route_gates.go: the gated mux looks it up when the route
// registers and panics on a route without one, so a new route needs both rows.

// routeSpec is one HTTP route.
type routeSpec struct {
	pattern string // "METHOD /path" as ServeMux reads it; without a method it serves every method
	handler http.HandlerFunc
}

// apiRouteAreas lists the route table of every area registerRoutes mounts. The
// evidence, capture, outcome and hook areas share the evidence store, so
// newAPIHandlerFor mounts them. Areas that register through a routeRegistrar join
// through routesOf.
func apiRouteAreas() [][]routeSpec {
	return [][]routeSpec{
		authRoutes,
		operatorRoutes,
		postgresRoutes,
		workspaceRoutes,
		routesOf(registerInlineRoutes),
	}
}

// registerRoutes mounts every area's routes on mux. It is separate from main so
// tests can exercise the real route table through httptest.
func registerRoutes(mux routeRegistrar) {
	for _, area := range apiRouteAreas() {
		for _, s := range area {
			mux.HandleFunc(s.pattern, s.handler)
		}
	}
}

// routeCollector is a routeRegistrar that keeps the routes registered on it.
type routeCollector []routeSpec

func (c *routeCollector) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	*c = append(*c, routeSpec{pattern, handler})
}

// routesOf returns the routes an area that registers through a routeRegistrar mounts.
func routesOf(register func(routeRegistrar)) []routeSpec {
	var c routeCollector
	register(&c)
	return c
}

// reply answers a finished call: 200 with its result, or fail's answer to its error.
func reply(w http.ResponseWriter, result any, err error, fail failure) {
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// failure answers a failed call. Each route keeps the answer it gave before the
// split: the typed mapper respondError, or one of the fixed mappings below.
type failure func(http.ResponseWriter, error)

// errorText, as notFound's message, answers os.ErrNotExist with the error's own text.
const errorText = ""

// The os.ErrNotExist answers most routes share.
var (
	notFoundWorkspace = notFound("Workspace not found")
	notFoundSnapshot  = notFound("Snapshot not found")
	notFoundResource  = notFound("Missing resource")
	notFoundRun       = notFound("Run not found")
	notFoundAsIs      = notFound(errorText)
)

// notFound answers os.ErrNotExist with 404 and msg, an error matching one of the
// invalid sentinels with 400, and any other error with 500; the 400 and 500 bodies
// carry the error's text.
func notFound(msg string, invalid ...error) failure {
	return notFoundElse(msg, func(w http.ResponseWriter, err error) {
		status := http.StatusInternalServerError
		if slices.ContainsFunc(invalid, func(target error) bool { return errors.Is(err, target) }) {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
	})
}

// notFoundOrInvalid answers os.ErrNotExist with 404 and msg and any other error with
// 400 and its text: a write whose only other failure is its input.
func notFoundOrInvalid(msg string) failure {
	return notFoundElse(msg, withStatus(http.StatusBadRequest))
}

// notFoundElse answers os.ErrNotExist with 404 and msg and hands any other error to other.
func notFoundElse(msg string, other failure) failure {
	return func(w http.ResponseWriter, err error) {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": cmp.Or(msg, err.Error())})
			return
		}
		other(w, err)
	}
}

// withStatus answers every error with status and the error's text.
func withStatus(status int) failure {
	return func(w http.ResponseWriter, err error) {
		writeJSON(w, status, map[string]any{"error": err.Error()})
	}
}

// invalidWhenMentioning answers an error whose lowercased text contains one of
// phrases with 400 and its text, and hands any other error to other: the routes whose
// operations report bad input only in untyped error text.
func invalidWhenMentioning(other failure, phrases ...string) failure {
	return func(w http.ResponseWriter, err error) {
		lowered := strings.ToLower(err.Error())
		if slices.ContainsFunc(phrases, func(p string) bool { return strings.Contains(lowered, p) }) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		other(w, err)
	}
}

// Handlers of the common route shape: call one workspaceops function with the data
// directory (read per request), the path's workspace id and, by constructor, one more
// path value (item), one query value (key) or the decoded JSON body (a malformed body
// is a 400 before the call), then reply with fail as the route's failure.

func dirCall[T any](op func(dataDir string) (T, error), fail failure) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := op(dataDir())
		reply(w, result, err, fail)
	}
}

func wsCall[T any](op func(dataDir, workspaceID string) (T, error), fail failure) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := op(dataDir(), r.PathValue("workspace_id"))
		reply(w, result, err, fail)
	}
}

func itemCall[T any](item string, op func(dataDir, workspaceID, id string) (T, error), fail failure) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := op(dataDir(), r.PathValue("workspace_id"), r.PathValue(item))
		reply(w, result, err, fail)
	}
}

func queryCall[T any](key string, op func(dataDir, workspaceID, value string) (T, error), fail failure) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := op(dataDir(), r.PathValue("workspace_id"), r.URL.Query().Get(key))
		reply(w, result, err, fail)
	}
}

func wsBodyCall[B, T any](op func(dataDir, workspaceID string, body B) (T, error), fail failure) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body B
		if !decodeJSONBody(w, r, &body) {
			return
		}
		result, err := op(dataDir(), r.PathValue("workspace_id"), body)
		reply(w, result, err, fail)
	}
}

func itemBodyCall[B, T any](item string, op func(dataDir, workspaceID, id string, body B) (T, error), fail failure) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body B
		if !decodeJSONBody(w, r, &body) {
			return
		}
		result, err := op(dataDir(), r.PathValue("workspace_id"), r.PathValue(item), body)
		reply(w, result, err, fail)
	}
}

// queryInt reads an integer query parameter, keeping def when it is absent or not a
// number.
func queryInt(r *http.Request, key string, def int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return n
	}
	return def
}

// requireQuery reads a query parameter the route cannot run without; a missing one is
// a 400 that names it.
func requireQuery(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	v := r.URL.Query().Get(key)
	if v == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": key + " query param required"})
	}
	return v, v != ""
}

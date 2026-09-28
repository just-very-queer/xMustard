package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"

	"xmustard/api-go/internal/workspaceops"
)

// The nine tools' routes that have no file of their own (core): search, explain,
// impact, recall and the admin's memory history, why_failed's run read, and the
// indexer's rebaseline. ground is in ground_routes.go, remember and verify in
// memory_routes.go, why_failed's record in outcome_routes.go and diagnostics in
// repo_diagnostics_routes.go.
var toolRoutes = []routeSpec{
	{"GET /api/workspaces/{workspace_id}/search", search},
	{"GET /api/workspaces/{workspace_id}/explain-path", explainPath},
	{"GET /api/workspaces/{workspace_id}/changes/since-index", impactSinceIndex},
	{"GET /api/workspaces/{workspace_id}/context/active", recall},
	{"GET /api/workspaces/{workspace_id}/context", listContextEntries},
	{"GET /api/workspaces/{workspace_id}/runs/{run_id}/why-failed", explainRunFailure},
	{"POST /api/workspaces/{workspace_id}/index", rebaselineIndex},
}

func search(w http.ResponseWriter, r *http.Request) {
	query, ok := requireQuery(w, r, "q")
	if !ok {
		return
	}
	limit := queryInt(r, "limit", 25)
	dd, ws, q := dataDir(), r.PathValue("workspace_id"), r.URL.Query()
	var result json.RawMessage
	var err error
	switch {
	case q.Get("mode") == "pattern":
		// structural AST search: the query is an ast-grep pattern (e.g. `$A && $A()`).
		pat, perr := workspaceops.SearchSemanticPatternCtx(r.Context(), dd, ws, query, q.Get("lang"), q.Get("path"), limit)
		if perr != nil {
			respondError(w, perr)
			return
		}
		result, err = json.Marshal(pat)
	case q.Get("rerank") != "":
		// optional neural lane: ?rerank=<provider>&embed_model=<model>
		result, err = workspaceops.WorkspaceSearchReranked(dd, ws, query, q.Get("rerank"), q.Get("embed_model"), limit)
	default:
		// default search fuses the agent-feedback boost and records retrieval.
		// optional ?seed=<symbol> activates the graph-proximity lane, ?path_glob=
		// filters paths and ?cursor= continues a page (next_cursor).
		result, err = workspaceops.WorkspaceSearchPage(r.Context(), dd, ws, workspaceops.SearchRequest{
			Query: query, Seed: q.Get("seed"), PathGlob: q.Get("path_glob"), Cursor: q.Get("cursor"), Limit: limit,
		})
	}
	reply(w, result, err, respondError)
}

// explainPath explains one file, enriched best-effort with its community cluster
// (its functional neighbourhood: "where does this file sit in the repo") and the
// freshness and coverage of the code graph behind it.
func explainPath(w http.ResponseWriter, r *http.Request) {
	ws, path := r.PathValue("workspace_id"), r.URL.Query().Get("path")
	result, err := workspaceops.ExplainPathCtx(r.Context(), dataDir(), ws, path)
	if err != nil {
		explainFailure(w, err)
		return
	}
	graph, gerr := workspaceops.PathGraphCtx(r.Context(), dataDir(), ws, path)
	switch {
	case gerr != nil:
		writeJSON(w, http.StatusOK, result)
	case graph.Cluster != nil:
		writeJSON(w, http.StatusOK, map[string]any{
			"explanation": result, "cluster": graph.Cluster,
			"freshness": graph.Freshness, "coverage": graph.Coverage,
		})
	default:
		result.Freshness, result.Coverage = graph.Freshness, graph.Coverage
		writeJSON(w, http.StatusOK, result)
	}
}

// explainFailure: a path that escapes the workspace (or other bad input) is a 400,
// not a 500.
func explainFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Workspace or path not found"})
	case workspaceops.IsInvalidInput(err):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	default:
		respondError(w, err)
	}
}

// impactSinceIndex is impact, enriched with the precomputed symbol graph: ?path= →
// the files referencing one file; ?from=&to= → the shortest dependency path;
// ?symbol= → blast radius; otherwise the dirty-symbols view since the index baseline.
// A path mixed with another form is refused, not silently outranked.
func impactSinceIndex(w http.ResponseWriter, r *http.Request) {
	dd, ws, q := dataDir(), r.PathValue("workspace_id"), r.URL.Query()
	if q.Get("path") != "" && (q.Get("symbol") != "" || q.Get("from") != "" || q.Get("to") != "") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "impact takes one of path=, symbol= or from=&to="})
		return
	}
	depth, _ := strconv.Atoi(q.Get("depth"))
	var result json.RawMessage
	var err error
	switch {
	case q.Get("path") != "":
		result, err = workspaceops.FileImpactCtx(r.Context(), dd, ws, q.Get("path"), depth)
	case q.Get("from") != "" && q.Get("to") != "":
		result, err = workspaceops.TraceSymbolsCtx(r.Context(), dd, ws, q.Get("from"), q.Get("to"))
	case q.Get("symbol") != "":
		result, err = workspaceops.SymbolImpactCtx(r.Context(), dd, ws, q.Get("symbol"), depth)
	default:
		result, err = workspaceops.WorkspaceChangesSinceIndexCtx(r.Context(), dd, ws)
	}
	reply(w, result, err, respondError)
}

// recall answers one of three reads. The agent-facing recall is always bounded and
// ranked; the complete promoted history (scope=all) is an administrative read; one
// entry by id is any reader's, with unverified text and history for reviewers only.
func recall(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch {
	case q.Get("scope") == "all":
		recallAllActive(w, r)
	case q.Get("entry_id") != "":
		recallEntry(w, r, q.Get("entry_id"))
	default:
		recallRanked(w, r)
	}
}

// recallAllActive is the complete promoted history: every entry, every drift hash.
func recallAllActive(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	result, err := workspaceops.GetActiveContext(dataDir(), r.PathValue("workspace_id"))
	reply(w, result, err, respondError)
}

// recallEntry fetches one entry by id in any lifecycle state (expired, superseded,
// retired, purged tombstone). A reader sees verified content only; unverified text and
// history=true (revisions and events) need a reviewer: the verifier or human-approver
// role (admin holds both; open mode is the operator).
func recallEntry(w http.ResponseWriter, r *http.Request, id string) {
	history := r.URL.Query().Get("history") == "true"
	if history && !requireReviewer(w, r) {
		return
	}
	result, err := workspaceops.GetContextEntry(dataDir(), r.PathValue("workspace_id"), id, history, isReviewer(r))
	reply(w, result, err, respondError)
}

func recallRanked(w http.ResponseWriter, r *http.Request) {
	req, err := recallRequest(r)
	if err != nil {
		respondError(w, err)
		return
	}
	// the verification queue is unverified text: a reviewer's read, like history
	if req.ReadsUnverified() && !requireReviewer(w, r) {
		return
	}
	result, err := workspaceops.RecallWith(r.Context(), dataDir(), r.PathValue("workspace_id"), req)
	reply(w, result, err, respondError)
}

// listContextEntries returns complete memory history with content under every filter
// (the default "all" too), so it is administrative; agents use the bounded recall.
func listContextEntries(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	result, err := workspaceops.ListContextEntries(dataDir(), r.PathValue("workspace_id"), r.URL.Query().Get("filter"))
	reply(w, result, err, respondError)
}

func explainRunFailure(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ExplainRunFailureCtx(r.Context(), dataDir(), r.PathValue("workspace_id"), r.PathValue("run_id"))
	reply(w, result, err, respondError)
}

// rebaselineIndex resets the index baseline. The route gate admits only the indexer
// and admin roles; the history records who.
func rebaselineIndex(w http.ResponseWriter, r *http.Request) {
	who := memoryCaller{principal: principalFromContext(r.Context())}.id()
	result, err := workspaceops.RebaselineIndex(r.Context(), dataDir(), r.PathValue("workspace_id"), who)
	reply(w, result, err, respondError)
}

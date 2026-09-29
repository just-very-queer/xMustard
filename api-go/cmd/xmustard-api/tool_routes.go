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
	limit := queryInt(r, "limit", workspaceops.DefaultSearchLimit)
	dd, ws, q := dataDir(), r.PathValue("workspace_id"), r.URL.Query()
	var result json.RawMessage
	var err error
	switch {
	case q.Get("mode") == "symbol":
		// name-path lookup over the code index (WS-36): q is a name path.
		var req workspaceops.SymbolSearchRequest
		if req, err = workspaceops.ParseSymbolSearch(q, limit); err == nil {
			result, err = workspaceops.SymbolSearchCtx(r.Context(), dd, ws, req)
		}
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

// explainPath explains one file (its explanation, outline and cluster) or a directory
// (its outline), or a symbol's context (?symbol= a name path, ?uid= a symbol's UID),
// with staged fallback (WS-36). The output budget and paging (max_chars, limit, cursor)
// are the tool budget middleware's (tool_budget_routes.go).
func explainPath(w http.ResponseWriter, r *http.Request) {
	req, err := workspaceops.ParseExplain(r.URL.Query(), callerOf(r).ID)
	if err != nil {
		respondError(w, err)
		return
	}
	result, err := workspaceops.ExplainCtx(r.Context(), dataDir(), r.PathValue("workspace_id"), req)
	reply(w, result, err, explainFailure)
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

// impactSinceIndex is impact (WS-35): ?from=&to= → the shortest path with per-hop
// edges; ?symbol= → blast radius by depth tier; ?path= → the files referencing one
// file (WS-FIX-05); ?scope= → the symbols a scoped diff touches; otherwise the changes
// since the index baseline. A parameter of another form (two targets among them), or
// a value out of range, is a 400 before any core runs.
func impactSinceIndex(w http.ResponseWriter, r *http.Request) {
	dd, ws, q := dataDir(), r.PathValue("workspace_id"), r.URL.Query()
	params := map[string]string{}
	for name := range q {
		params[name] = q.Get(name)
	}
	form, rest, err := workspaceops.CheckImpactRequest(params)
	if err != nil {
		respondError(w, err)
		return
	}
	var result json.RawMessage
	switch form {
	case workspaceops.ImpactTrace:
		result, err = workspaceops.TraceSymbolsCtx(r.Context(), dd, ws, q.Get("from"), q.Get("to"), rest)
	case workspaceops.ImpactBlast:
		result, err = workspaceops.SymbolImpactCtx(r.Context(), dd, ws, q.Get("symbol"), rest)
		if err == nil {
			result = workspaceops.WithImpactMemories(r.Context(), dd, ws, callerOf(r).ID, q.Get("symbol"), nil, result)
		}
	case workspaceops.ImpactFile:
		result, err = workspaceops.FileImpactCtx(r.Context(), dd, ws, q.Get("path"), rest)
		if err == nil {
			result = workspaceops.WithImpactMemories(r.Context(), dd, ws, callerOf(r).ID, "", []string{q.Get("path")}, result)
		}
	case workspaceops.ImpactChanges:
		result, err = workspaceops.DetectChangesCtx(r.Context(), dd, ws, rest)
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
	case q.Get("source") == "sessions":
		recallSessions(w, r)
	case q.Get("scope") == "all":
		recallAllActive(w, r)
	case q.Get("entry_id") != "":
		recallEntry(w, r, q.Get("entry_id"))
	default:
		recallRanked(w, r)
	}
}

// recallSessions searches past session transcripts (WS-34): raw observations, a
// reader's read like the rest of recall, labelled and injection-scanned.
func recallSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	result, err := workspaceops.SearchSessions(r.Context(), dataDir(), r.PathValue("workspace_id"), workspaceops.SessionSearch{
		Query: q.Get("query"), Since: q.Get("since"), Until: q.Get("until"), Agent: q.Get("agent"),
		Conversation: q.Get("conversation"), Cursor: q.Get("cursor"), Limit: min(limit, maxRecallLimit)})
	reply(w, result, err, respondError)
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

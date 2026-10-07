package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// `pattern` and `symbol` answer from their own surfaces, so they are dispatched
	// first. The lexical modes share one request. A mode the tool does not advertise is
	// bad input, not the hybrid by default.
	switch mode := q.Get("mode"); {
	case mode == "symbol":
		// name-path lookup over the code index (WS-36): q is a name path.
		var req workspaceops.SymbolSearchRequest
		if req, err = workspaceops.ParseSymbolSearch(q, limit); err == nil {
			result, err = workspaceops.SymbolSearchCtx(r.Context(), dd, ws, req)
		}
	case mode == "pattern":
		// structural AST search: the query is an ast-grep pattern (e.g. `$A && $A()`).
		// `lang` is ast-grep's own language here, not the lexical filter.
		pat, perr := workspaceops.SearchSemanticPatternCtx(r.Context(), dd, ws, query, q.Get("lang"), q.Get("path"), limit)
		if perr != nil {
			respondError(w, perr)
			return
		}
		result, err = json.Marshal(pat)
	case mode != "" && !workspaceops.IsSearchMode(mode):
		respondError(w, fmt.Errorf("mode %q is not a search mode: %w", mode, workspaceops.ErrInvalidInput))
		return
	case q.Get("rerank") != "":
		// The neural lane reranks a plain hybrid search and takes neither `mode` nor
		// `lang`, so honouring one of them here would answer from a search that never
		// applied it. Refuse rather than drop the filter silently. `mode=hybrid` is the
		// default the flag need not carry, so it composes as it always did; every named
		// mode (text, exact, regex) and any `lang` are refused.
		if (q.Get("mode") != "" && q.Get("mode") != workspaceops.SearchModeHybrid) || q.Get("lang") != "" {
			respondError(w, fmt.Errorf("rerank cannot be combined with %w: rerank does not apply the mode or language filter",
				workspaceops.ErrInvalidInput))
			return
		}
		// optional neural lane: ?rerank=<provider>&embed_model=<model>
		result, err = workspaceops.WorkspaceSearchReranked(dd, ws, query, q.Get("rerank"), q.Get("embed_model"), limit)
	default:
		// the default search fuses the agent-feedback boost and records retrieval.
		// ?seed=<symbol> activates the graph-proximity lane, ?lang= and ?path_glob=
		// filter, ?mode=text answers from the body lane alone, ?mode=exact|regex scans
		// the working tree's bytes, and ?cursor= continues a page (next_cursor).
		result, err = workspaceops.WorkspaceSearchPage(r.Context(), dd, ws, workspaceops.SearchRequest{
			Query: query, Seed: q.Get("seed"), PathGlob: q.Get("path_glob"), Lang: q.Get("lang"),
			Mode: q.Get("mode"), Cursor: q.Get("cursor"), Limit: limit,
			ScanLines: queryInt(r, "max_lines", 0), ScanBytes: queryInt(r, "max_bytes", 0),
			ScanTimeoutMS: queryInt(r, "timeout_ms", 0),
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
// a value out of range, is a 400 before any core runs. A cursor= continues the tier page
// the previous call handed back and becomes the core's offset (WS-59e); the paged forms
// report has_more and next_cursor when a tier was cut.
func impactSinceIndex(w http.ResponseWriter, r *http.Request) {
	dd, ws, q := dataDir(), r.PathValue("workspace_id"), r.URL.Query()
	params := map[string]string{}
	for name := range q {
		params[name] = q.Get(name)
	}
	// impact's tier cursor is decoded here, before the form is known, because
	// CheckImpactRequest must see one page position rather than two (WS-59e). An
	// occurrence cursor (op=, WS-59d) is a different key over a different list and is
	// signed and decoded by the occurrence read itself, so it is left for that call:
	// decoding it here would refuse every valid occurrence cursor as one signed for
	// another ranking.
	if q.Get("op") == "" {
		if err := workspaceops.ResolveImpactCursor(ws, q, params); err != nil {
			respondError(w, err)
			return
		}
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
		switch rest["op"] {
		case "":
			result, err = workspaceops.SymbolImpactCtx(r.Context(), dd, ws, q.Get("symbol"), rest)
			if err == nil {
				result = workspaceops.WithImpactMemories(r.Context(), dd, ws, callerOf(r).ID, q.Get("symbol"), nil, result)
				result = workspaceops.WithImpactPaging(ws, q, result)
			}
		default:
			// op= is an occurrence read (WS-59d), not the tier walk
			result, err = workspaceops.SymbolOccurrencesCtx(r.Context(), dd, ws, q.Get("symbol"), rest)
		}
	case workspaceops.ImpactFile:
		result, err = workspaceops.FileImpactCtx(r.Context(), dd, ws, q.Get("path"), rest)
		if err == nil {
			result = workspaceops.WithImpactMemories(r.Context(), dd, ws, callerOf(r).ID, "", []string{q.Get("path")}, result)
		}
	case workspaceops.ImpactChanges:
		result, err = workspaceops.DetectChangesCtx(r.Context(), dd, ws, rest)
		if err == nil {
			result = workspaceops.WithImpactPaging(ws, q, result)
		}
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
// The reader is the authenticated principal, so the scope rule applies here too
// (WS-29b): an admin is the operator but not a way round a private memory.
func recallAllActive(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	result, err := workspaceops.GetActiveContext(dataDir(), r.PathValue("workspace_id"), callerOf(r).ID)
	reply(w, result, err, respondError)
}

// recallEntry fetches one entry by id in any lifecycle state (expired, superseded,
// retired, purged tombstone). A reader sees verified content only; unverified text and
// history=true (revisions and events) need a reviewer: the verifier or human-approver
// role (admin holds both; open mode is the operator).
func recallEntry(w http.ResponseWriter, r *http.Request, id string) {
	q := r.URL.Query()
	history := q.Get("history") == "true"
	if history && !requireReviewer(w, r) {
		return
	}
	// the reader's own keys follow the principal, so a run, session or directory
	// scoped memory is fetchable by id from inside that key and by nobody else
	result, err := workspaceops.GetContextEntry(dataDir(), r.PathValue("workspace_id"), id, history, isReviewer(r),
		append([]string{callerOf(r).ID}, q.Get("dir"), q.Get("run_id"), q.Get("session_id"))...)
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
	result, err := workspaceops.ListContextEntries(dataDir(), r.PathValue("workspace_id"),
		r.URL.Query().Get("filter"), callerOf(r).ID)
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

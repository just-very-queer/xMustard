package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/groundbudget"
	"xmustard/api-go/internal/workspaceops"
)

// registerGroundRoutes mounts the `ground` route: session grounding, fitted to the
// output-budget contract (WS-54) when the caller asks for it. The budget is opt-in
// over HTTP: a caller that sends neither sections nor max_chars (the Pi adapter,
// whose closed mirror cannot send them, and the UI) gets the unbudgeted result, as
// before the budget existed. The MCP ground tool always sends max_chars. A budgeted
// result carries output_budget, which says what was reduced, keeps the signals of
// what was left out, and says how to get it in full.
func registerGroundRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/session-grounding", func(w http.ResponseWriter, r *http.Request) {
		// validate before any work: out-of-range values are rejected, never clamped
		// sections may repeat (?sections=runs&sections=index) or be comma-separated
		q := r.URL.Query()
		budgeted := q.Has("sections") || q.Has("max_chars")
		req, aerr := groundbudget.ParseRequest(strings.Join(q["sections"], ","), q.Get("max_chars"))
		if aerr == nil && len(q["max_chars"]) > 1 {
			aerr = &groundbudget.ArgError{Argument: "max_chars", Reason: "must be given once"}
		}
		if aerr != nil {
			body := map[string]any{"error": aerr.Error(), "argument": aerr.Argument, "reason": aerr.Reason}
			for k, v := range aerr.Extra {
				body[k] = v
			}
			writeJSON(w, http.StatusBadRequest, body)
			return
		}
		result, err := workspaceops.BuildSessionGroundingCtx(r.Context(), dataDir(), r.PathValue("workspace_id"))
		if err != nil {
			respondError(w, err)
			return
		}
		if !budgeted {
			writeJSON(w, http.StatusOK, groundResponse(r, result))
			return
		}
		raw, err := json.Marshal(groundResponse(r, result))
		if err != nil {
			respondError(w, err)
			return
		}
		// the result, its element tables and the fitted output are held until the
		// response is written: admit them first
		scope, owned := budget.ScopeFor(r.Context())
		if owned {
			defer scope.Close()
		}
		if err := scope.Acquire(groundbudget.WorkingSet(raw, req)); err != nil {
			writeOverloaded(w)
			return
		}
		out, err := groundbudget.ApplyRecoverable(raw, req, func() groundbudget.Recovery { return retainGround(r, raw) })
		if err != nil {
			respondError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, json.RawMessage(out))
	})
}

// retainGround keeps the unbudgeted ground result as evidence on a delivered call, so
// the budgeted reply can name its recovery handle; a plain HTTP call is told how to
// get the unbudgeted result instead.
func retainGround(r *http.Request, raw []byte) groundbudget.Recovery {
	keep := retainOriginalFrom(r.Context())
	if keep == nil {
		return groundbudget.Recovery{Unavailable: "not a delivered call: omit sections and max_chars for the unbudgeted result"}
	}
	d, err := keep(raw)
	if err != nil {
		_, reason := evidenceErrorStatus(err)
		return groundbudget.Recovery{Unavailable: "evidence " + reason}
	}
	return groundbudget.Recovery{Handle: d.Handle, URI: d.ResourceURI}
}

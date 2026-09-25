package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"xmustard/api-go/internal/groundbudget"
	"xmustard/api-go/internal/workspaceops"
)

// registerGroundRoutes mounts the `ground` route: session grounding fitted to the
// output-budget contract (WS-54). The caller may select sections and a total
// max_chars; every result carries output_budget, which says what was reduced, keeps
// the signals of what was left out, and says how to get it in full.
func registerGroundRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/session-grounding", func(w http.ResponseWriter, r *http.Request) {
		// validate before any work: out-of-range values are rejected, never clamped
		// sections may repeat (?sections=runs&sections=index) or be comma-separated
		q := r.URL.Query()
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
		raw, err := json.Marshal(groundResponse(r, result))
		if err == nil {
			raw, err = groundbudget.Apply(raw, req)
		}
		if err != nil {
			respondError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, json.RawMessage(raw))
	})
}

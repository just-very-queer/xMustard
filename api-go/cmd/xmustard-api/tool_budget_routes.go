package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/groundbudget"
	"xmustard/api-go/internal/mcpserver"
	"xmustard/api-go/internal/toolbudget"
	"xmustard/api-go/internal/workspaceops"
)

// toolBudgetMiddleware fits the search, explain and impact results to the output-budget
// contract (toolbudget, WS-36) when the caller asks for it with max_chars (and, for
// explain, pages them with limit and cursor). Like ground's, the budget is opt-in over
// HTTP: a caller that sends none of these arguments (the Pi adapter, the UI) gets the
// tool's result as before. The MCP tools always send max_chars. It sits inside the
// evidence delivery, so a delivered call retains the unbudgeted result as evidence and
// its reply names the recovery handle; the route handlers never see the arguments it
// owns.
func toolBudgetMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spec, ok := toolbudget.For(coreToolFor(r.Method, r.URL.Path))
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		q := r.URL.Query()
		req, aerr := spec.ParseRequest(q)
		if aerr != nil {
			writeBudgetArgError(w, aerr)
			return
		}
		if !req.Active() {
			next.ServeHTTP(w, r)
			return
		}
		ws := workspaceIDFromPath(r.URL.Path)
		page, err := budgetPage(spec, ws, q, req)
		if err != nil {
			respondError(w, err)
			return
		}
		inner := r.Clone(r.Context())
		inner.URL.RawQuery = without(q, spec.Owns()).Encode()
		scope, owned := budget.ScopeFor(r.Context())
		if owned {
			defer scope.Close()
		}
		rec := &boundedRecorder{header: http.Header{}, scope: scope, limit: mcpserver.MaxResponseBytes}
		next.ServeHTTP(rec, inner)
		switch {
		case errors.Is(rec.err, budget.ErrOverloaded):
			writeOverloaded(w)
			return
		case rec.err != nil:
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": spec.Tool + " result is too large to budget; narrow the call"})
			return
		case rec.status() != http.StatusOK || !strings.HasPrefix(rec.header.Get("Content-Type"), "application/json"):
			copyResponse(w, rec)
			return
		}
		raw := rec.buf.Bytes()
		if req.MaxChars > 0 {
			// the result's parse tables and the fitted output are held until it is written
			if err := scope.Acquire(int64(len(raw)) + int64(req.MaxChars) + budgetSlack); err != nil {
				writeOverloaded(w)
				return
			}
		}
		if spec.Tool == "search" {
			page.Offset = resultOffset(raw)
		}
		out, err := spec.Apply(raw, req, page, func() toolbudget.Recovery {
			return retainBudgeted(r, raw, "omit max_chars for the unbudgeted result")
		})
		if err != nil {
			copyResponse(w, rec)
			return
		}
		// impact's cursor continues at the core's next_offset, which describes the
		// core's page; when the budget cut that page the cursor is re-signed to
		// continue at the first item the caller did not receive, so the caller pages
		// without computing an offset (WS-59e).
		//
		// Each of the two passes reconciles the cursor against the page the budget
		// actually wrote and then settles the report over it. Settling can trim again, so
		// the second pass re-derives the cursor from what survived; the second reconcile
		// cannot grow the result, so it does not re-trim. Two passes, not a loop with a
		// convergence test: each pass either leaves the page alone or removes items, and a
		// third pass would only re-derive the same cursor.
		if spec.Tool == "impact" {
			for pass := 0; pass < 2; pass++ {
				out = workspaceops.ReconcileImpactPaging(ws, q, out)
				settled, serr := spec.Settle(out, req, page)
				if serr != nil {
					// Settle fails only on a result that is not valid JSON, which the
					// handler's own output cannot be. Refuse rather than write an answer
					// whose report would describe something other than the body.
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "impact result is too large to budget; narrow the call"})
					return
				}
				out = settled
			}
		}
		writeJSON(w, http.StatusOK, json.RawMessage(out))
	})
}

// budgetSlack bounds what fitting allocates besides the result and the output: the
// ladder's parse tables (a slice header per member and list element) and the report.
const budgetSlack = 256 << 10

// budgetPage says where the tool's paged list sits: explain pages its list here (the
// cursor is decoded before the handler runs); search pages in the core, so only its
// cursor signer is needed; impact is not paged here (WS-35 pages its depth tiers).
func budgetPage(spec *toolbudget.Spec, ws string, q url.Values, req toolbudget.Request) (toolbudget.Page, error) {
	switch spec.Tool {
	case "explain":
		offset, err := workspaceops.DecodeExplainCursor(ws, q, req.Cursor)
		return toolbudget.Page{Slice: true, Offset: offset, Limit: req.Limit, Cursor: workspaceops.ExplainCursor(ws, q)}, err
	case "search":
		if q.Get("rerank") != "" {
			// the rerank lane never reads cursor=, so a signed cursor here would not
			// advance the next page: issue none (has_more/recover_uri still report the cut).
			return toolbudget.Page{}, nil
		}
		return toolbudget.Page{Cursor: workspaceops.SearchCursor(ws, q)}, nil
	}
	return toolbudget.Page{}, nil
}

// resultOffset reads a producer-paged result's offset member.
func resultOffset(raw []byte) int {
	var page struct {
		Offset int `json:"offset"`
	}
	_ = json.Unmarshal(raw, &page)
	return page.Offset
}

// without is q minus the named arguments.
func without(q url.Values, names []string) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = v
	}
	for _, n := range names {
		out.Del(n)
	}
	return out
}

// copyResponse passes a recorded response through unchanged.
func copyResponse(w http.ResponseWriter, rec *boundedRecorder) {
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.status())
	_, _ = w.Write(rec.buf.Bytes())
}

// writeBudgetArgError answers a rejected budget argument: 400 naming it and its bounds.
func writeBudgetArgError(w http.ResponseWriter, aerr *toolbudget.ArgError) {
	body := map[string]any{"error": aerr.Error(), "argument": aerr.Argument, "reason": aerr.Reason}
	for k, v := range aerr.Extra {
		body[k] = v
	}
	writeJSON(w, http.StatusBadRequest, body)
}

// retainBudgeted keeps a budgeted handler's unbudgeted result as evidence on a
// delivered call, so the budgeted reply can name its recovery handle; a plain HTTP call
// is told how to get the unbudgeted result instead (plain).
func retainBudgeted(r *http.Request, raw []byte, plain string) groundbudget.Recovery {
	keep := retainOriginalFrom(r.Context())
	if keep == nil {
		return groundbudget.Recovery{Unavailable: "not a delivered call: " + plain}
	}
	d, err := keep(raw)
	if err != nil {
		_, reason := evidenceErrorStatus(err)
		return groundbudget.Recovery{Unavailable: "evidence " + reason}
	}
	return groundbudget.Recovery{Handle: d.Handle, URI: d.ResourceURI}
}

package main

import (
	"context"
	"log"
	"net/http"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

// Run-independent outcomes (WS-21; PAR-HAR-06, PAR-RET-11). why_failed works without
// platform runs:
//
//   POST /api/workspaces/{ws}/why-failed  {"command": "go test ./pkg/..."} | {"argv": [...]}
//        (+ "cwd", "timeout_seconds") | {"evidence_handle": "xm1..."} | {"log": "..."}
//   GET  /api/workspaces/{ws}/outcomes?open=true&limit=N
//
// The POST runs a test, build or lint command through the bounded Rust runner (argv
// exec, no shell, the working directory confined to the workspace root, a timeout that
// terminates the process group), or reads the last MiB of an evidence original the
// caller may read, or takes a pasted log; it records the outcome and answers the
// explanation. Running a command and recording an outcome are agent writes (proposer);
// readers only read outcomes: GET .../runs/{outcome_id}/why-failed (main.go) and the
// list above. Both routes are core and classified in routeGateTable.
//
// A captured test, build or lint output (POST .../evidence/capture) becomes an outcome
// too (recordCaptureOutcome), unless the deployment is read-only.

// maxWhyFailedBody bounds the POST body: a pasted log (analyzed from its last MiB) and
// its JSON escaping.
const maxWhyFailedBody = 8 << 20

// whyFailedWindowBytes is the transient memory one analysis holds: the 1 MiB tail as
// read, as text, and redacted (the analysis itself works line by line).
const whyFailedWindowBytes = 3 << 20

type whyFailedBody struct {
	Command        string   `json:"command"`
	Argv           []string `json:"argv"`
	Cwd            string   `json:"cwd"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	EvidenceHandle string   `json:"evidence_handle"`
	Log            string   `json:"log"`
	RunID          string   `json:"run_id"`
}

func registerOutcomeRoutes(mux routeRegistrar, store *evidence.Store) {
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/why-failed", func(w http.ResponseWriter, r *http.Request) {
		caller, ok := requireMemoryCaller(w, r, roleProposer)
		if !ok {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxWhyFailedBody)
		var body whyFailedBody
		if !requireJSONBody(w, r, &body) {
			return
		}
		if body.RunID != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "a run_id is read with GET .../runs/{run_id}/why-failed"})
			return
		}
		if !reserveWindow(w, r, whyFailedWindowBytes) {
			return
		}
		ws := r.PathValue("workspace_id")
		result, err := workspaceops.RecordFailureOutcome(r.Context(), dataDir(), ws, workspaceops.FailureRequest{
			Command: body.Command, Argv: body.Argv, Cwd: body.Cwd, TimeoutSeconds: body.TimeoutSeconds,
			EvidenceHandle: body.EvidenceHandle, Log: body.Log, Evidence: evidenceTail(r, store, ws), Actor: caller.actor(),
		})
		if err != nil {
			respondError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/outcomes", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, err := optionalInt(q.Get("limit"))
		if err != nil || (limit != nil && (*limit < 1 || *limit > 1000)) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "limit must be an integer from 1 to 1000"})
			return
		}
		ws, open := r.PathValue("workspace_id"), q.Get("open") == "true"
		list, err := workspaceops.ListRunOutcomes(r.Context(), dataDir(), ws, open, derefInt(limit))
		if err != nil {
			respondError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"workspace_id": ws, "open": open, "outcomes": list})
	})
}

// evidenceTail reads an evidence tail authorized as the request's caller.
func evidenceTail(r *http.Request, store *evidence.Store, ws string) workspaceops.EvidenceTailFunc {
	actor, enforced := principalScope(r)
	return func(ctx context.Context, handle string, maxBytes int64) (*evidence.Tail, error) {
		return store.Tail(ctx, evidence.ReadRequest{WorkspaceID: ws, Handle: handle, Actor: actor, AuthEnforced: enforced}, maxBytes)
	}
}

// reserveWindow admits n transient bytes for the request, answering 503 when the
// pool cannot hold them.
func reserveWindow(w http.ResponseWriter, r *http.Request, n int64) bool {
	scope, owned := budget.ScopeFor(r.Context())
	if owned {
		scope.Close() // no request ledger (direct callers/tests): nothing to hold
		return true
	}
	if err := scope.Acquire(n); err != nil {
		writeOverloaded(w)
		return false
	}
	return true
}

// recordCaptureOutcome turns a captured test, build or lint result into a run outcome.
// It is best effort: the capture answers either way, and a failure is logged. A
// read-only deployment records nothing (capture stays served there because it stores
// only the caller's own output), and neither does a retention capture (target set: an
// adapter re-capturing a result it already delivered, to mask or compact it).
func recordCaptureOutcome(r *http.Request, store *evidence.Store, ws string, retention bool, res *evidence.ObservationResult) {
	if postureFrom(r).readOnly() || retention || res == nil || res.Delivery == nil || !workspaceops.CapturesOutcome(res.Family) {
		return
	}
	exit := res.Capture.ExitCode // what the client reported, else what the reducer parsed
	if exit == nil {
		exit = res.Facts.ExitCode
	}
	failed := res.Capture.IsError || nonZero(exit) || res.Facts.Failed > 0
	p := principalFromContext(r.Context())
	actor := memoryCaller{principal: p, openMode: p == nil}.actor()
	actor.SessionID, _ = workspaceops.ProvenanceLabel("session_id", res.Capture.SessionID)
	actor.CallID, _ = workspaceops.ProvenanceLabel("call_id", res.Capture.CallID)
	c := workspaceops.CapturedOutcome{
		Family: res.Family, Command: res.Command, Failed: failed, Handle: res.Handle, Evidence: evidenceTail(r, store, ws),
		Tool: res.Capture.Tool, ExitCode: exit, FailingTests: res.Facts.FailingTests,
		Session: actor.SessionID, Call: actor.CallID, Actor: actor,
	}
	if res.Handle == "" {
		c.Text = res.Projection // nothing was retained: analyze what the agent saw
	}
	scope, owned := budget.ScopeFor(r.Context())
	if !owned {
		if err := scope.Acquire(whyFailedWindowBytes); err != nil {
			return // under memory pressure the capture answers without recording
		}
	} else {
		scope.Close()
	}
	if err := workspaceops.RecordCapturedOutcome(r.Context(), dataDir(), ws, c); err != nil {
		log.Printf("why_failed: capture outcome for workspace %s not recorded: %v", ws, err)
	}
}

func nonZero(code *int) bool { return code != nil && *code != 0 }

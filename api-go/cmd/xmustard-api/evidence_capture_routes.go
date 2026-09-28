package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

// Universal observation capture and search inside retained originals (WS-08;
// PAR-CTX-01, PAR-CTX-03, PAR-CTX-04). No MCP tool is added: hooks and adapters
// (Claude Code, Codex, Cursor, Pi, OpenCode) post any tool's output here and get a
// family-reduced, client-shaped projection with a recovery handle; every client
// recovers through byte pages (GET .../evidence/{handle}) or search.
//
//   POST /api/workspaces/{ws}/evidence/capture?format=raw|claude|codex|cursor|pi|opencode
//        &client=&tool=&tool_version=&call_id=&session_id=&agent_id=&args_digest=
//        &is_error=&exit_code=&content_type=&command=&path=&lines=A-B&start_line=&family=
//        &target=<bytes>
//   GET  /api/workspaces/{ws}/evidence/search?handle=&pattern=|query=&lines=A-B
//        &max_matches=&context=&offset=&start_line=
//
// The capture body is streamed to the spool in O(window) memory, so the body-limit
// middleware caps it without reserving its declared length or buffering a chunked
// body (streamsRequestBody); the handler reserves a fixed window instead. Capture
// never takes the heavy slot. (Security review: this exempts the capture route from
// the in-flight large-body semaphore; its memory is admitted by the budget instead.)
//
// Redaction (PAR-CTX-01 "Redaction applies") is fail-closed: an original is retained
// for the whole retention window and is searchable, so capture refuses with 503
// redaction_unavailable until a streaming redactor is wired (captureRedactor; the
// WS-05 redact.Stream plugs in there). Search stays available for originals that
// were captured redacted.
//
// target lowers the client's projection target for one capture (1 KiB..1 MiB; a value
// above the client policy's target changes nothing). The Pi adapter uses it to retain
// an older tool result it is about to mask or compact behind a handle (WS-24).
//
// Route gates (WS-09): both routes register on the gated mux and are classified in
// routeGateTable: capture is core, needs the proposer role and is served in
// read-only mode (it stores only the caller's own tool output); search is core and
// needs the reader role.
//
// A captured test, build or lint output also becomes a run-independent outcome that
// ground lists (WS-21, outcome_routes.go), except in read-only mode.

// captureRedactor wraps the capture spool writer with the streaming secret redactor.
// While it is nil, POST .../evidence/capture refuses (503 redaction_unavailable).
var captureRedactor func(io.Writer) evidence.StreamRedactor

// captureWindowBytes is the transient memory one capture holds while it decodes and
// reduces (read/write buffers, reducer windows, one long line).
const captureWindowBytes = 2 << 20

// searchWindowBytes is what one search holds: the chunk, a carried line and the result.
const searchWindowBytes = evidence.SearchChunk + 1<<20 + 256<<10

// streamsRequestBody reports routes whose handlers stream the request body to disk.
func streamsRequestBody(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	ws := workspaceIDFromPath(r.URL.Path)
	return ws != "" && r.URL.Path == "/api/workspaces/"+ws+"/evidence/capture"
}

func registerEvidenceCaptureRoutes(mux routeRegistrar, store *evidence.Store) {
	reg := evidence.DefaultRegistry()
	formats := map[evidence.HookFormat]bool{evidence.FormatRaw: true}
	for _, f := range evidence.HookFormats() {
		formats[f] = true
	}
	families := map[evidence.Family]bool{}
	for _, info := range reg.Reducers() {
		families[info.Family] = true
	}
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/evidence/capture", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "agent") {
			return
		}
		r = r.WithContext(budget.WithoutHeavyWait(r.Context())) // a capture path: never waits for the heavy slot (WS-06)
		redact := captureRedactor
		if redact == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"reason": "redaction_unavailable",
				"error": "capture is disabled until a streaming secret redactor is configured: captured originals are retained and searchable"})
			return
		}
		q := r.URL.Query()
		format := evidence.HookFormat(q.Get("format"))
		if format == "" {
			format = evidence.FormatRaw
		}
		if !formats[format] {
			badCapture(w, "invalid_format", fmt.Sprintf("format must be raw or one of %v", evidence.HookFormats()))
			return
		}
		meta := evidence.CaptureMeta{Client: q.Get("client"), Tool: q.Get("tool"), ToolVersion: q.Get("tool_version"),
			CallID: q.Get("call_id"), SessionID: q.Get("session_id"), AgentID: q.Get("agent_id"),
			ArgsDigest: q.Get("args_digest"), IsError: q.Get("is_error") == "true", ContentType: q.Get("content_type")}
		if meta.ContentType == "" && format == evidence.FormatRaw {
			meta.ContentType = r.Header.Get("Content-Type")
		}
		sel := evidence.Selector{Command: q.Get("command"), Path: q.Get("path"), Family: evidence.Family(q.Get("family"))}
		if sel.Family != "" && !families[sel.Family] {
			badCapture(w, "invalid_family", "unknown tool family "+string(sel.Family))
			return
		}
		var err error
		if meta.ExitCode, err = optionalInt(q.Get("exit_code")); err != nil {
			badCapture(w, "invalid_exit_code", err.Error())
			return
		}
		if sel.FromLine, sel.ToLine, err = lineRange(q.Get("lines")); err != nil {
			badCapture(w, "invalid_lines", err.Error())
			return
		}
		if v, err := optionalInt(q.Get("start_line")); err != nil || (v != nil && *v < 1) {
			badCapture(w, "invalid_start_line", "start_line must be a positive integer")
			return
		} else if v != nil {
			sel.StartLine = *v
		}
		target, err := optionalInt(q.Get("target"))
		if err != nil || (target != nil && (*target < evidence.MinCaptureTarget || *target > evidence.MaxCaptureTarget)) {
			badCapture(w, "invalid_target", fmt.Sprintf("target must be an integer from %d to %d bytes", evidence.MinCaptureTarget, evidence.MaxCaptureTarget))
			return
		}
		ws := r.PathValue("workspace_id")
		// the body streams to disk: only the decode/reduce window is reserved
		if scope, owned := budget.ScopeFor(r.Context()); !owned {
			if err := scope.Acquire(captureWindowBytes); err != nil {
				writeOverloaded(w)
				return
			}
		} else {
			scope.Close()
		}
		actor, enforced := principalScope(r)
		res, err := store.Observe(r.Context(), reg, evidence.ObservationInput{
			WorkspaceID: ws, RepoScope: workspaceops.WorkspaceRepoScope(dataDir(), ws), Actor: actor, AuthEnforced: enforced,
			Format: format, Body: r.Body, Meta: meta, Sel: sel, Redact: redact, Target: derefInt(target),
		})
		if err != nil {
			writeCaptureError(w, err)
			return
		}
		recordCaptureOutcome(r, store, ws, target != nil, res) // WS-21: test/build/lint outcomes
		// the reply encodes the projection, the shaped payload and the (bounded)
		// structured projection, facts and omissions again: admit it
		if scope, owned := budget.ScopeFor(r.Context()); !owned {
			if err := scope.Acquire(int64(2*len(res.Projection)+2*len(res.Shape.Payload)+len(res.Omissions)*96) + 64<<10); err != nil {
				writeOverloaded(w)
				return
			}
		} else {
			scope.Close()
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/evidence/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ws := r.PathValue("workspace_id")
		actor, enforced := principalScope(r)
		req := evidence.SearchRequest{Pattern: q.Get("pattern"), Query: q.Get("query"),
			ReadRequest: evidence.ReadRequest{WorkspaceID: ws, Handle: q.Get("handle"), Actor: actor, AuthEnforced: enforced, RepoKey: repoIdentityFunc(ws)}}
		ints := []struct {
			name string
			dst  *int
		}{{"max_matches", &req.MaxMatches}, {"context", &req.Context}, {"start_line", &req.StartLine}}
		for _, p := range ints {
			v, err := optionalInt(q.Get(p.name))
			if err != nil || (v != nil && *v < 0) {
				badCapture(w, "invalid_"+p.name, p.name+" must be a non-negative integer")
				return
			}
			if v != nil {
				*p.dst = *v
			}
		}
		if v := q.Get("offset"); v != "" {
			off, err := strconv.ParseInt(v, 10, 64)
			if err != nil || off < 0 {
				badCapture(w, "invalid_offset", "offset must be a non-negative integer")
				return
			}
			req.Offset = off
		}
		var err error
		if req.FromLine, req.ToLine, err = lineRange(q.Get("lines")); err != nil {
			badCapture(w, "invalid_lines", err.Error())
			return
		}
		// one pooled 1 MiB chunk, at most a 1 MiB partial line and a page-sized result
		if scope, owned := budget.ScopeFor(r.Context()); !owned {
			if err := scope.Acquire(searchWindowBytes); err != nil {
				writeOverloaded(w)
				return
			}
		} else {
			scope.Close()
		}
		res, err := store.Search(r.Context(), req)
		if err != nil {
			writeCaptureError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

// withTokenEstimate adds delivered_tokens_est (PAR-CTX-13, xm-tokens/1) to an
// evidence envelope: the estimated tokens of the projection a client receives.
func withTokenEstimate(d *evidence.Delivery) any {
	return struct {
		*evidence.Delivery
		DeliveredTokensEst int    `json:"delivered_tokens_est"`
		TokenEstimator     string `json:"token_estimator"`
	}{d, evidence.EstimateTokens(d.Projection), evidence.TokenEstimator}
}

func badCapture(w http.ResponseWriter, reason, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg, "reason": reason})
}

// writeCaptureError maps capture/search errors; the rest are the evidence errors.
func writeCaptureError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "request body too large", "reason": "too_large"})
	case errors.Is(err, io.ErrUnexpectedEOF):
		badCapture(w, "incomplete_body", "the request body ended early")
	case errors.Is(err, evidence.ErrBadBody):
		badCapture(w, "bad_body", err.Error())
	case errors.Is(err, evidence.ErrInvalidSearch):
		badCapture(w, "invalid_search", err.Error())
	default:
		writeEvidenceError(w, err)
	}
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func optionalInt(v string) (*int, error) {
	if v == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf("%q is not an integer", v)
	}
	return &n, nil
}

// lineRange parses "A-B", "A-" or "A" (1-based, inclusive).
func lineRange(v string) (int, int, error) {
	if v == "" {
		return 0, 0, nil
	}
	a, b, dash := strings.Cut(v, "-")
	from, err := strconv.Atoi(a)
	if err != nil || from < 1 {
		return 0, 0, fmt.Errorf("lines must be A-B with 1 <= A")
	}
	to := from
	if dash {
		to = 0
		if b != "" {
			if to, err = strconv.Atoi(b); err != nil || to < from {
				return 0, 0, fmt.Errorf("lines must be A-B with A <= B")
			}
		}
	}
	return from, to, nil
}

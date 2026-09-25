package main

import (
	"errors"
	"fmt"
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
//   GET  /api/workspaces/{ws}/evidence/search?handle=&pattern=|query=&lines=A-B
//        &max_matches=&context=&offset=&start_line=
//
// The capture body is streamed to the spool in O(window) memory, so the body-limit
// middleware caps it without reserving its declared length or buffering a chunked
// body (streamsRequestBody); the handler reserves a fixed window instead. Capture
// never takes the heavy slot.

// captureWindowBytes is the transient memory one capture holds while it decodes and
// reduces (read/write buffers, reducer windows, one long line).
const captureWindowBytes = 2 << 20

// streamsRequestBody reports routes whose handlers stream the request body to disk.
func streamsRequestBody(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	ws := workspaceIDFromPath(r.URL.Path)
	return ws != "" && r.URL.Path == "/api/workspaces/"+ws+"/evidence/capture"
}

func registerEvidenceCaptureRoutes(mux *http.ServeMux, store *evidence.Store) {
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
			Format: format, Body: r.Body, Meta: meta, Sel: sel,
		})
		if err != nil {
			writeCaptureError(w, err)
			return
		}
		// the reply encodes the projection and the shaped payload again: admit it
		if scope, owned := budget.ScopeFor(r.Context()); !owned {
			if err := scope.Acquire(int64(2*len(res.Projection)+2*len(res.Shape.Payload)) + 8192); err != nil {
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
		res, err := store.Search(r.Context(), req)
		if err != nil {
			writeCaptureError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
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
	case errors.Is(err, evidence.ErrBadBody):
		badCapture(w, "bad_body", err.Error())
	case errors.Is(err, evidence.ErrInvalidSearch):
		badCapture(w, "invalid_search", err.Error())
	default:
		writeEvidenceError(w, err)
	}
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

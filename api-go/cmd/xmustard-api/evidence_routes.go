package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

// Evidence delivery (plan Stage 2). The nine tool routes stay unchanged for plain
// callers. A caller that sends `X-Xmustard-Delivery: xmustard.evidence/v1` (the MCP shim)
// receives an evidence envelope instead: the handler's output is spooled to disk,
// captured, and replaced by a bounded projection plus a recovery handle when anything
// was omitted. Pi posts its xMustard tool results to POST .../evidence for the same
// projection. Both clients expand through GET .../evidence/{handle}.

const (
	deliveryHeader = "X-Xmustard-Delivery"
	toolVersion    = "api-go/xmustard-tools-1"
)

// newAPIHandler builds the route table with evidence delivery wired in.
func newAPIHandler() http.Handler {
	mux := http.NewServeMux()
	registerRoutes(mux)
	store := evidence.NewStore(dataDir(), evidence.LimitsFromEnv())
	registerEvidenceRoutes(mux, store)
	return evidenceDeliveryMiddleware(store, mux)
}

// coreTools maps the nine MCP tool names to the route that serves them.
var coreTools = map[string]bool{
	"ground": true, "recall": true, "remember": true, "verify": true, "search": true,
	"explain": true, "impact": true, "diagnostics": true, "why_failed": true,
}

// coreToolFor returns the tool served by (method, path), or "".
func coreToolFor(method, path string) string {
	ws := workspaceIDFromPath(path)
	if ws == "" {
		return ""
	}
	sub := strings.TrimPrefix(path, "/api/workspaces/"+ws+"/")
	switch {
	case method == http.MethodGet && sub == "session-grounding":
		return "ground"
	case method == http.MethodGet && sub == "context/active":
		return "recall"
	case method == http.MethodPost && sub == "context":
		return "remember"
	case method == http.MethodGet && sub == "search":
		return "search"
	case method == http.MethodGet && sub == "explain-path":
		return "explain"
	case method == http.MethodGet && sub == "changes/since-index":
		return "impact"
	case method == http.MethodGet && sub == "diagnostics":
		return "diagnostics"
	}
	seg := strings.Split(sub, "/")
	if len(seg) == 3 && seg[0] == "context" && seg[2] == "verify" && method == http.MethodPost {
		return "verify"
	}
	if len(seg) == 3 && seg[0] == "runs" && seg[2] == "why-failed" && method == http.MethodGet {
		return "why_failed"
	}
	return ""
}

// spoolWriter is the ResponseWriter a delivered handler writes into: the body goes to
// the capped on-disk spool, never into a second in-memory copy.
type spoolWriter struct {
	header http.Header
	status int
	spool  *evidence.Spool
}

func (s *spoolWriter) Header() http.Header { return s.header }
func (s *spoolWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
}
func (s *spoolWriter) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.spool.Write(p)
}

// hashingBody digests exactly the request body the handler consumed.
type hashingBody struct {
	io.ReadCloser
	h hash.Hash
}

func (b *hashingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.h.Write(p[:n])
	return n, err
}

func principalScope(r *http.Request) (actor string, enforced bool) {
	if p := principalFromContext(r.Context()); p != nil {
		return p.ID, true
	}
	return "", false
}

func repoIdentityFunc(ws string) func(ctx context.Context) evidence.Identity {
	return func(ctx context.Context) evidence.Identity {
		id, _ := workspaceops.WorkspaceRepoIdentity(ctx, dataDir(), ws)
		return toEvidenceIdentity(id)
	}
}

func toEvidenceIdentity(id workspaceops.RepoIdentity) evidence.Identity {
	lims := make([]string, 0, len(id.Limitations))
	for _, l := range id.Limitations {
		lims = append(lims, l.String())
	}
	return evidence.Identity{Key: id.Key, Complete: id.Complete, Limitations: lims}
}

func evidenceDeliveryMiddleware(store *evidence.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tool := coreToolFor(r.Method, r.URL.Path)
		if r.Header.Get(deliveryHeader) != evidence.DeliveryVersion || tool == "" {
			next.ServeHTTP(w, r)
			return
		}
		ws := workspaceIDFromPath(r.URL.Path)
		sp, err := store.NewSpool(ws)
		if err != nil {
			respondError(w, workspaceops.Invalid("invalid workspace id"))
			return
		}
		// released even if the handler panics or Capture is never reached (idempotent)
		defer sp.Discard()
		body := &hashingBody{ReadCloser: http.NoBody, h: sha256.New()}
		if r.Body != nil {
			body.ReadCloser = r.Body
		}
		r.Body = body
		// identity is sampled around execution: Capture binds it only when the
		// before and after samples are complete and equal.
		// one sample yields both the before-identity and the canonical trust scope
		beforeID, scope := workspaceops.WorkspaceRepoIdentity(r.Context(), dataDir(), ws)
		before := toEvidenceIdentity(beforeID)
		sw := &spoolWriter{header: http.Header{}, spool: sp}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		// an admission refusal is a retryable protocol answer, not a tool result:
		// pass it through unchanged (503 + Retry-After) instead of capturing it
		if sw.status == http.StatusServiceUnavailable && sw.header.Get("Retry-After") != "" {
			writeOverloaded(w)
			return
		}
		// argument digest: method, path, canonical (sorted) query and consumed body.
		argsHash := sha256.New()
		io.WriteString(argsHash, r.Method+" "+r.URL.Path+"?"+r.URL.Query().Encode()+"\n")
		argsHash.Write(body.h.Sum(nil))
		actor, enforced := principalScope(r)
		issuer := r.Header.Get("X-Xmustard-Issuer")
		if issuer == "" {
			issuer = "http"
		}
		d, err := store.Capture(r.Context(), sp, evidence.CaptureRequest{
			WorkspaceID: ws, RepoScope: scope, Actor: actor, AuthEnforced: enforced, Issuer: issuer,
			SessionID: r.Header.Get("X-Xmustard-Session-Id"), CallID: r.Header.Get("X-Xmustard-Call-Id"),
			Tool: tool, ToolVersion: toolVersion, ArgsDigest: hex.EncodeToString(argsHash.Sum(nil)),
			Status: sw.status, IsError: sw.status >= 400, ContentType: sw.header.Get("Content-Type"),
			BeforeKey: &before, RepoKey: repoIdentityFunc(ws),
		})
		if err != nil {
			writeEvidenceError(w, err)
			return
		}
		// the envelope encodes the projection again (JSON-escaped): admit it first
		if scope, owned := budget.ScopeFor(r.Context()); !owned {
			if err := scope.Acquire(int64(2*len(d.Projection)) + 4096); err != nil {
				writeOverloaded(w)
				return
			}
		} else {
			scope.Close()
		}
		w.Header().Set(deliveryHeader, evidence.DeliveryVersion)
		writeJSON(w, http.StatusOK, d)
	})
}

// writeEvidenceError maps evidence errors to explicit, protocol-correct HTTP answers.
func writeEvidenceError(w http.ResponseWriter, err error) {
	status, reason := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, evidence.ErrTooLarge):
		status, reason = http.StatusRequestEntityTooLarge, "too_large"
	case errors.Is(err, evidence.ErrQuotaFull):
		status, reason = http.StatusInsufficientStorage, "quota_full"
	case errors.Is(err, evidence.ErrExpired):
		status, reason = http.StatusGone, "expired"
	case errors.Is(err, evidence.ErrRevoked):
		status, reason = http.StatusGone, "revoked"
	case errors.Is(err, evidence.ErrDenied):
		status, reason = http.StatusForbidden, "denied"
	case errors.Is(err, evidence.ErrMissing):
		status, reason = http.StatusNotFound, "missing"
	case errors.Is(err, evidence.ErrInvalidHandle):
		status, reason = http.StatusNotFound, "invalid_handle"
	case errors.Is(err, evidence.ErrInvalidRange):
		status, reason = http.StatusRequestedRangeNotSatisfiable, "invalid_range"
	case errors.Is(err, evidence.ErrUnsupported):
		status, reason = http.StatusUnsupportedMediaType, "unsupported"
	case errors.Is(err, evidence.ErrCorrupt):
		status, reason = http.StatusInternalServerError, "corrupt"
		log.Printf("evidence: %v", err)
		writeJSON(w, status, map[string]any{"error": err.Error(), "reason": reason})
		return
	}
	if status == http.StatusInternalServerError {
		log.Printf("evidence: %v", err)
		writeJSON(w, status, map[string]any{"error": "internal error", "reason": reason})
		return
	}
	writeJSON(w, status, map[string]any{"error": err.Error(), "reason": reason})
}

func registerEvidenceRoutes(mux *http.ServeMux, store *evidence.Store) {
	// Pi (and any direct client) posts one xMustard tool result for projection. The
	// raw result is the request body; tool identity/call metadata are query params.
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/evidence", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "agent") {
			return
		}
		q := r.URL.Query()
		tool := q.Get("tool")
		if !coreTools[tool] {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tool must be one of the nine xMustard tools", "reason": "invalid_tool"})
			return
		}
		ws := r.PathValue("workspace_id")
		sp, err := store.NewSpool(ws)
		if err != nil {
			respondError(w, workspaceops.Invalid("invalid workspace id"))
			return
		}
		defer sp.Discard() // idempotent; covers panics before Capture
		h := sha256.New()
		if _, err := io.Copy(io.MultiWriter(sp, h), r.Body); err != nil {
			sp.Discard()
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
			return
		}
		status := http.StatusOK
		if v, err := strconv.Atoi(q.Get("status")); err == nil && v > 0 {
			status = v
		}
		isError := q.Get("is_error") == "true" || status >= 400
		actor, enforced := principalScope(r)
		scope := workspaceops.WorkspaceRepoScope(dataDir(), ws)
		ct := q.Get("content_type")
		if ct == "" {
			ct = r.Header.Get("Content-Type")
		}
		issuer := q.Get("issuer")
		if issuer == "" {
			issuer = "http"
		}
		d, err := store.Capture(r.Context(), sp, evidence.CaptureRequest{
			WorkspaceID: ws, RepoScope: scope, Actor: actor, AuthEnforced: enforced, Issuer: issuer,
			SessionID: q.Get("session_id"), CallID: q.Get("call_id"), Tool: tool,
			ToolVersion: q.Get("tool_version"), ArgsDigest: q.Get("args_digest"),
			// A result posted after the fact was produced at an unknown repository state:
			// no identity is sampled or attached, so its freshness is always "unknown".
			Status: status, IsError: isError, ContentType: ct,
		})
		if err != nil {
			writeEvidenceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	readReq := func(r *http.Request) evidence.ReadRequest {
		actor, enforced := principalScope(r)
		off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		length, _ := strconv.Atoi(r.URL.Query().Get("length"))
		ws := r.PathValue("workspace_id")
		return evidence.ReadRequest{WorkspaceID: ws, Handle: r.PathValue("handle"), Actor: actor,
			AuthEnforced: enforced, Offset: off, Length: length, RepoKey: repoIdentityFunc(ws)}
	}
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/evidence/{handle}", func(w http.ResponseWriter, r *http.Request) {
		page, err := store.Read(r.Context(), readReq(r))
		if err != nil {
			writeEvidenceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})
	mux.HandleFunc("DELETE /api/workspaces/{workspace_id}/evidence/{handle}", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "agent") {
			return
		}
		if err := store.Revoke(readReq(r)); err != nil {
			writeEvidenceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
	})
	// Workspace-level revocation (there is no workspace-deletion API): removes every
	// retained original of the workspace.
	mux.HandleFunc("DELETE /api/workspaces/{workspace_id}/evidence", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "admin") {
			return
		}
		if err := store.RevokeWorkspace(r.PathValue("workspace_id")); err != nil {
			writeEvidenceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
	})
}

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

// newAPIHandler builds the route table for the posture in the environment.
func newAPIHandler() http.Handler { return newAPIHandlerFor(postureFromEnv()) }

// newAPIHandlerFor builds the gated route table with evidence delivery wired in. The
// route gates run before delivery, so a refused call is never captured.
func newAPIHandlerFor(p exposurePosture) http.Handler {
	mux := newGatedMux()
	registerRoutes(mux)
	store := evidence.NewStore(dataDir(), evidence.LimitsFromEnv())
	registerEvidenceRoutes(mux, store)
	registerEvidenceCaptureRoutes(mux, store)
	registerOutcomeRoutes(mux, store)
	registerHookRoutes(mux, store)
	return routeGateMiddleware(p, mux, evidenceDeliveryMiddleware(store, mux))
}

// coreTools maps the nine MCP tool names to the route that serves them.
var coreTools = map[string]bool{
	"ground": true, "recall": true, "remember": true, "verify": true, "search": true,
	"explain": true, "impact": true, "diagnostics": true, "why_failed": true,
}

// identityFreeTools are the write tools: their results are memory records, not
// observations of the repository, so no repository identity is sampled for them.
var identityFreeTools = map[string]bool{"remember": true, "verify": true}

// coreToolRoutes maps "METHOD sub-path" under /api/workspaces/{ws}/ to the tool it
// serves; a {} segment matches one path segment.
var coreToolRoutes = map[string]string{
	"GET session-grounding":   "ground",
	"GET context/active":      "recall",
	"POST context":            "remember",
	"GET search":              "search",
	"GET explain-path":        "explain",
	"GET changes/since-index": "impact",
	"GET diagnostics":         "diagnostics",
	"POST context/{}/verify":  "verify",
	"GET runs/{}/why-failed":  "why_failed",
	"POST why-failed":         "why_failed",
}

// coreToolFor returns the tool served by (method, path), or "".
func coreToolFor(method, path string) string {
	ws := workspaceIDFromPath(path)
	if ws == "" {
		return ""
	}
	seg := strings.Split(strings.TrimPrefix(path, "/api/workspaces/"+ws+"/"), "/")
	if len(seg) == 3 {
		seg[1] = "{}" // the entry or run id
	}
	return coreToolRoutes[method+" "+strings.Join(seg, "/")]
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
	budget.NoteHashed(int64(n))
	return n, err
}

func principalScope(r *http.Request) (actor string, enforced bool) {
	if p := principalFromContext(r.Context()); p != nil {
		return p.ID, true
	}
	return "", false
}

// captureIdentity observes a delivered call's repository identity through its
// request context: before the handler runs (after=false), or after it (after=true)
// for binding the capture. A variable so tests can stand in an observer that
// attempts heavy work.
var captureIdentity = func(ctx context.Context, rc *workspaceops.RequestContext, after bool) evidence.Identity {
	if after {
		return toEvidenceIdentity(rc.IdentityAfter(ctx))
	}
	return toEvidenceIdentity(rc.Identity(ctx))
}

// repoIdentityFunc reads a workspace's current identity through the identity cache
// (an evidence page read), resolving the scope only when the identity is needed:
// no repo-key run while a fingerprint of the tree matches the cached pairing.
func repoIdentityFunc(ws string) func(ctx context.Context) evidence.Identity {
	return func(ctx context.Context) evidence.Identity {
		scope := workspaceops.WorkspaceRepoScope(dataDir(), ws)
		if scope == "" {
			return toEvidenceIdentity(workspaceops.RepoIdentity{Source: "unavailable",
				Limitations: []workspaceops.IdentityLimitation{{Reason: "workspace_root_unavailable"}}}, workspaceops.IdentityObservation{})
		}
		return toEvidenceIdentity(workspaceops.CurrentRepoIdentity(ctx, scope))
	}
}

func toEvidenceIdentity(id workspaceops.RepoIdentity, obs workspaceops.IdentityObservation) evidence.Identity {
	lims := make([]string, 0, len(id.Limitations))
	for _, l := range id.Limitations {
		lims = append(lims, l.String())
	}
	return evidence.Identity{Key: id.Key, Complete: id.Complete, Limitations: lims,
		Cached: obs.Cached, AgeMs: obs.Age.Milliseconds()}
}

func evidenceDeliveryMiddleware(store *evidence.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tool := coreToolFor(r.Method, r.URL.Path)
		if tool == "" {
			next.ServeHTTP(w, r)
			return
		}
		// the request context kernel: the workspace is resolved once (registry) and
		// the identity sampled at most once, for the handler and for capture alike
		ws := workspaceIDFromPath(r.URL.Path)
		rc := workspaceops.NewRequestContext(dataDir(), ws)
		r = r.WithContext(workspaceops.WithRequestContext(r.Context(), rc))
		if r.Header.Get(deliveryHeader) != evidence.DeliveryVersion {
			next.ServeHTTP(w, r)
			return
		}
		sp, err := store.NewSpool(ws)
		if err != nil {
			respondError(w, workspaceops.Invalid("invalid workspace id"))
			return
		}
		// released even if the handler panics or Capture is never reached (idempotent)
		defer sp.Discard()
		// Capture work (identity sampling, spooling, reduction) never waits for the heavy
		// slot; the tool itself runs on r's context as it would without delivery.
		captureCtx := budget.WithoutHeavyWait(r.Context())
		body := &hashingBody{ReadCloser: http.NoBody, h: sha256.New()}
		if r.Body != nil {
			body.ReadCloser = r.Body
		}
		r.Body = body
		// Identity is observed around execution and Capture binds it only when the
		// before and after identities are complete and equal. Read tools observe it
		// once, here. Capture asks for the after-identity only for a reduced result
		// with a complete before-identity; it then costs one fingerprint walk and no
		// repo-key run when the tree provably did not move during the handler, and
		// one run otherwise (or when the fingerprint is off for this root). Write
		// tools sample nothing. Both observations run on the capture context, so a
		// sample never waits for the heavy slot.
		scope := rc.Scope()
		var before *evidence.Identity
		var afterKey func(ctx context.Context) evidence.Identity
		if !identityFreeTools[tool] {
			b := captureIdentity(captureCtx, rc, false)
			before = &b
			afterKey = func(ctx context.Context) evidence.Identity { return captureIdentity(ctx, rc, true) }
		}
		captureReq := func(status int, contentType string) evidence.CaptureRequest {
			// argument digest: method, path, canonical (sorted) query and consumed body.
			argsHash := sha256.New()
			io.WriteString(argsHash, r.Method+" "+r.URL.Path+"?"+r.URL.Query().Encode()+"\n")
			argsHash.Write(body.h.Sum(nil))
			actor, enforced := principalScope(r)
			issuer := r.Header.Get("X-Xmustard-Issuer")
			if issuer == "" {
				issuer = "http"
			}
			return evidence.CaptureRequest{
				WorkspaceID: ws, RepoScope: scope, Actor: actor, AuthEnforced: enforced, Issuer: issuer,
				SessionID: r.Header.Get("X-Xmustard-Session-Id"), CallID: r.Header.Get("X-Xmustard-Call-Id"),
				Tool: tool, ToolVersion: toolVersion, ArgsDigest: hex.EncodeToString(argsHash.Sum(nil)),
				Status: status, IsError: status >= 400, ContentType: contentType,
				BeforeKey: before, RepoKey: afterKey,
			}
		}
		// A handler that projects its own result (ground's output budget) retains the
		// unprojected original through this, under the same scope, identity and
		// digest as the reply, and names the handle in its reply.
		r = r.WithContext(context.WithValue(r.Context(), retainOriginalKey{}, retainOriginal(func(raw []byte) (*evidence.Delivery, error) {
			osp, err := store.NewSpool(ws)
			if err != nil {
				return nil, err
			}
			defer osp.Discard()
			if _, err := osp.Write(raw); err != nil {
				return nil, err
			}
			req := captureReq(http.StatusOK, "application/json")
			req.Retain = true
			return store.Capture(captureCtx, osp, req)
		})))
		// a human approver's write held for the human's confirmation (human_presence.go)
		// is a protocol answer the bridge acts on, like an admission refusal: it passes
		// through unchanged, never captured
		var held map[string]any
		r = r.WithContext(withHeldWriteSlot(r.Context(), &held))
		sw := &spoolWriter{header: http.Header{}, spool: sp}
		next.ServeHTTP(sw, r)
		if held != nil {
			writeJSON(w, http.StatusForbidden, held)
			return
		}
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		// an admission refusal is a retryable protocol answer, not a tool result:
		// pass it through unchanged (503 + Retry-After) instead of capturing it
		if sw.status == http.StatusServiceUnavailable && sw.header.Get("Retry-After") != "" {
			writeOverloaded(w)
			return
		}
		d, err := store.Capture(captureCtx, sp, captureReq(sw.status, sw.header.Get("Content-Type")))
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
		writeJSON(w, http.StatusOK, withTokenEstimate(d))
	})
}

// retainOriginal keeps a delivered handler's unprojected result as evidence and
// returns the delivery naming its handle. It is in the request context only on a
// delivered call.
type retainOriginal func(raw []byte) (*evidence.Delivery, error)

type retainOriginalKey struct{}

func retainOriginalFrom(ctx context.Context) retainOriginal {
	f, _ := ctx.Value(retainOriginalKey{}).(retainOriginal)
	return f
}

// writeEvidenceError maps evidence errors to explicit, protocol-correct HTTP answers.
func writeEvidenceError(w http.ResponseWriter, err error) {
	status, reason := evidenceErrorStatus(err)
	if reason == "corrupt" {
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

// evidenceErrorStatus classifies an evidence error as an HTTP status and a reason.
func evidenceErrorStatus(err error) (int, string) {
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
	}
	return status, reason
}

func registerEvidenceRoutes(mux routeRegistrar, store *evidence.Store) {
	// Pi (and any direct client) posts one xMustard tool result for projection. The
	// raw result is the request body; tool identity/call metadata are query params.
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/evidence", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "agent") {
			return
		}
		r = r.WithContext(budget.WithoutHeavyWait(r.Context())) // a capture path: never waits for the heavy slot
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
		n, err := io.Copy(io.MultiWriter(sp, h), r.Body)
		budget.NoteHashed(n)
		if err != nil {
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
		writeJSON(w, http.StatusOK, withTokenEstimate(d))
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
		if !forgetEvidenceOutcomes(w, r, r.PathValue("handle")) {
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
		if !forgetEvidenceOutcomes(w, r, "") {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
	})
}

// forgetEvidenceOutcomes removes the run outcomes made from a revoked original (handle
// "": every original of the workspace), after the revocation, so an unauthorized
// revoke removes nothing. A failure answers 500; the revoke is idempotent, so a retry
// removes them.
func forgetEvidenceOutcomes(w http.ResponseWriter, r *http.Request, handle string) bool {
	if _, err := workspaceops.ForgetEvidenceOutcomes(r.Context(), dataDir(), r.PathValue("workspace_id"), handle); err != nil {
		log.Printf("evidence revoke: outcomes made from %q not removed: %v", handle, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "the original is revoked, but the outcomes made from it were not removed; retry the revoke"})
		return false
	}
	return true
}

package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// Liveness, the caller's identity and bearer-token administration (core). Every
// token route needs admin; the handlers check it again before they read the body.
var authRoutes = []routeSpec{
	{"/api/health", serveHealth},
	{"GET /api/auth/whoami", serveWhoami},
	{"GET /api/auth/principals", listPrincipals},
	{"POST /api/auth/tokens", mintToken},
	{"POST /api/auth/tokens/{id}/rotate", rotateToken},
	{"DELETE /api/auth/tokens/{id}", revokeToken},
	{"GET /api/auth/audit", listAuthAudit},
}

func serveHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse(r)) // health_budget.go
}

func serveWhoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, whoamiResponse(r))
}

func listPrincipals(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	writeJSON(w, http.StatusOK, workspaceops.ListPrincipals(dataDir()))
}

func mintToken(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	var req struct {
		ID         string   `json:"id"`
		Role       string   `json:"role"`  // one role, or several joined with "+"
		Roles      []string `json:"roles"` // alternative to role: a list of roles
		TTLSeconds int      `json:"ttl_seconds"`
		Workspaces []string `json:"workspaces"` // optional: confine the token to these workspaces
		// optional: who operates the token (default: the id) and whether it is a
		// human or an agent (default agent), for the owner-distinct policy
		workspaceops.TokenIdentity
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if len(req.Roles) > 0 {
		if req.Role != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "send role or roles, not both"})
			return
		}
		req.Role = strings.Join(req.Roles, "+")
	}
	raw, err := workspaceops.MintIdentityToken(dataDir(), req.ID, req.Role, req.TTLSeconds, req.Workspaces, req.TokenIdentity)
	if err != nil {
		withStatus(http.StatusBadRequest)(w, err)
		return
	}
	auditToken(r, "mint", req.ID, req.Role)
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "token": raw, "note": "store this now; it is not recoverable"})
}

func rotateToken(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	var req struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // body optional
	id := r.PathValue("id")
	raw, err := workspaceops.RotateToken(dataDir(), id, req.TTLSeconds)
	if err != nil {
		notFoundOrInvalid(errorText)(w, err)
		return
	}
	auditToken(r, "rotate", id, "")
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "token": raw, "note": "old token invalidated; store this now"})
}

func revokeToken(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	id := r.PathValue("id")
	if err := workspaceops.RevokeToken(dataDir(), id); err != nil {
		notFoundAsIs(w, err)
		return
	}
	auditToken(r, "revoke", id, "")
	writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
}

func listAuthAudit(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	writeJSON(w, http.StatusOK, workspaceops.ListAuthAudit(dataDir(), queryInt(r, "limit", 100)))
}

// auditToken records a token administration action by the calling admin ("system"
// when no principal is attached).
func auditToken(r *http.Request, action, tokenID, role string) {
	actor := "system"
	if p := principalFromContext(r.Context()); p != nil {
		actor = p.ID
	}
	workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
		Action: action, Actor: actor, TokenID: tokenID, Role: role,
		Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
	})
}

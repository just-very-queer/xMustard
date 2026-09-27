package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// registerMemoryRoutes mounts the governed-memory writes: remember (propose, supersede,
// edit, retire, restore), verify, the legacy in-place edit and the approver's
// retract/purge/restore. Every write is attributed to the authenticated principal, never to a
// caller-asserted name; open-mode callers collapse to one identity.
func registerMemoryRoutes(mux routeRegistrar) {
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/context", func(w http.ResponseWriter, r *http.Request) {
		caller, ok := requireMemoryCaller(w, r, workspaceops.RoleProposer)
		if !ok {
			return
		}
		var req workspaceops.RememberRequest
		// body optional (query params are the MCP-bridge path), but never malformed
		if !requireWellFormedJSON(w, r, &req) {
			return
		}
		q := r.URL.Query()
		for _, f := range []struct {
			dst *string
			key string
		}{{&req.Content, "content"}, {&req.Title, "title"}, {&req.Permission, "permission"}} {
			if *f.dst == "" {
				*f.dst = q.Get(f.key)
			}
		}
		if len(req.Paths) == 0 && q.Get("paths") != "" {
			req.Paths = strings.Split(q.Get("paths"), ",")
		}
		if !requireHumanPresence(w, r, caller, rememberAction(r.PathValue("workspace_id"), req)) {
			return
		}
		result, err := workspaceops.Remember(dataDir(), r.PathValue("workspace_id"), req, caller.actor())
		respondMemoryWrite(w, r, caller, result, err)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/context/{entry_id}/verify", func(w http.ResponseWriter, r *http.Request) {
		caller, ok := requireMemoryCaller(w, r, workspaceops.RoleVerifier)
		if !ok {
			return
		}
		var req struct {
			Approve        bool   `json:"approve"`
			Outcome        string `json:"outcome"`
			Revision       int64  `json:"revision"`
			Note           string `json:"note"`
			Target         string `json:"target"`
			EvidenceHandle string `json:"evidence_handle"`
		}
		// body optional (query params are the MCP-bridge path), but never malformed
		if !requireWellFormedJSON(w, r, &req) {
			return
		}
		q := r.URL.Query()
		if q.Has("approve") {
			req.Approve = q.Get("approve") == "true" // only a literal "true" approves; anything else is a reject
		}
		if req.Note == "" {
			req.Note = q.Get("note")
		}
		if req.Outcome == "" {
			req.Outcome = q.Get("outcome")
		}
		if req.Revision == 0 && q.Has("revision") {
			n, err := strconv.ParseInt(q.Get("revision"), 10, 64)
			if err != nil || n < 1 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "revision must be a positive integer"})
				return
			}
			req.Revision = n
		}
		if req.Outcome == "" && !req.Approve {
			req.Outcome = workspaceops.OutcomeReject
		}
		if !requireHumanPresence(w, r, caller, verifyAction(r.PathValue("workspace_id"), r.PathValue("entry_id"), req.Outcome, req.Revision)) {
			return
		}
		// The verifier is the AUTHENTICATED principal; a body "agent" is ignored. In
		// open mode all unauthenticated callers collapse to a single identity, so N
		// fabricated agent names cannot satisfy the multi-agent gate.
		result, err := workspaceops.VerifyContextOutcome(dataDir(), r.PathValue("workspace_id"), r.PathValue("entry_id"),
			caller.actor(), workspaceops.VerifyRequest{Outcome: req.Outcome, Revision: req.Revision, Note: req.Note,
				Target: req.Target, EvidenceHandle: req.EvidenceHandle})
		respondMemoryWrite(w, r, caller, result, err)
	})
	mux.HandleFunc("PUT /api/workspaces/{workspace_id}/context/{entry_id}", func(w http.ResponseWriter, r *http.Request) {
		// The legacy in-place edit, bound to the entry's author (or an admin): it serves
		// the new content at once and resets verification, so an unbound edit would let
		// any caller rewrite and demote any readwrite memory. remember(op=edit) is the
		// governed focused edit.
		caller, ok := requireMemoryCaller(w, r, workspaceops.RoleProposer)
		if !ok {
			return
		}
		var req struct {
			Content string `json:"content"`
		}
		// Same strict body handling as propose/verify, but required: 400 on an absent,
		// malformed or trailing-junk body, 413 past the cap. Empty content is a 400
		// from UpdateContextContent, so a misspelled field cannot blank a memory.
		if !requireJSONBody(w, r, &req) {
			return
		}
		result, err := workspaceops.UpdateContextContent(dataDir(), r.PathValue("workspace_id"), r.PathValue("entry_id"), req.Content, caller.actor())
		respondMemoryWrite(w, r, caller, result, err)
	})
	// The approver's lifecycle routes, not MCP tools: DELETE retracts at once, or with
	// purge=true deletes the entry's text (secrets, PII) and keeps a digest tombstone;
	// restore brings a retired, retracted, superseded or expired entry back.
	mux.HandleFunc("DELETE /api/workspaces/{workspace_id}/context/{entry_id}", func(w http.ResponseWriter, r *http.Request) {
		remove := workspaceops.RetractContext
		if r.URL.Query().Get("purge") == "true" {
			remove = workspaceops.PurgeContext
		}
		approverLifecycleWrite(w, r, remove)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/context/{entry_id}/restore", func(w http.ResponseWriter, r *http.Request) {
		approverLifecycleWrite(w, r, workspaceops.RestoreContext)
	})
}

// approverLifecycleWrite runs one reasoned lifecycle change as an admin or human
// approver. The reason comes from the body or the query.
func approverLifecycleWrite(w http.ResponseWriter, r *http.Request,
	change func(dataDir, workspaceID, entryID, reason string, actor workspaceops.ContextActor) (*workspaceops.ContextEntry, error)) {
	caller, ok := requireMemoryCaller(w, r, workspaceops.RoleHumanApprover)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !requireWellFormedJSON(w, r, &req) {
		return
	}
	if req.Reason == "" {
		req.Reason = r.URL.Query().Get("reason")
	}
	result, err := change(dataDir(), r.PathValue("workspace_id"), r.PathValue("entry_id"), req.Reason, caller.actor())
	respondMemoryWrite(w, r, caller, result, err)
}

// memoryAuthorityErrors are the refusals of a memory write that answer 403 and are
// audited: the caller is authenticated but lacks authority over this entry.
var memoryAuthorityErrors = []error{workspaceops.ErrNotEntryAuthor, workspaceops.ErrApproverRequired,
	workspaceops.ErrVerifierRequired, workspaceops.ErrSameOwner}

// respondMemoryWrite answers a governed-memory write.
func respondMemoryWrite(w http.ResponseWriter, r *http.Request, caller memoryCaller, result any, err error) {
	for _, denied := range memoryAuthorityErrors {
		if errors.Is(err, denied) {
			workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
				Action: "denied", Actor: caller.id(), Detail: err.Error(),
				Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
			})
			writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
			return
		}
	}
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

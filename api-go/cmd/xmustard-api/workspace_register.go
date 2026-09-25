package main

import (
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// Workspace registration policy for POST /api/workspaces/load. The route gate
// admits proposer, so an agent token reaches the handler (its MCP shim registers
// the repository it runs in on the first tool call); this decides what the caller
// may register:
//
//   - admin, open mode (no credentials) and XMUSTARD_AUTH=off: any root, as before.
//   - any other principal: only the top level of a git work tree that resolves,
//     symlinks evaluated, at or below a directory in XMUSTARD_REGISTER_ROOTS, with
//     its git directory there too (workspaceops.CheckRegistrationRoot). The
//     resolved path is what gets registered, and the record keeps the registration
//     root, so every later use re-checks it. A directory already registered, under
//     any spelling or link, is reused, keeps its name and is never rescanned; a new
//     one is registered with its first scan, up to XMUSTARD_REGISTER_LIMIT per
//     principal. The caller never chooses whether to scan.
//
// For every caller the root's workspace id must pass the deployment's workspace
// allowlist and the token's workspace scope, and a load that creates a registry
// entry is recorded in the auth audit log (GET /api/auth/audit) with the principal
// that made it.

// registrationNotAllowed is the reason a refused registration answers with;
// "refusal" in the body carries a workspaceops.Refusal* code, or refusalTokenScope.
const (
	registrationNotAllowed = "registration_not_allowed"
	refusalTokenScope      = "token_scope"
)

// workspaceLoadAdmission is an admitted load: what the registry held for the root
// beforehand, and whether the caller was held to the registration roots.
type workspaceLoadAdmission struct {
	before     workspaceops.WorkspaceRootLookup
	restricted bool // a non-admin principal
}

// admitWorkspaceLoad applies the registration policy to req, rewriting it for a
// non-admin caller, or answers the refusal and returns false.
func admitWorkspaceLoad(w http.ResponseWriter, r *http.Request, req *workspaceops.WorkspaceLoadRequest) (workspaceLoadAdmission, bool) {
	p := principalFromContext(r.Context())
	posture := postureFrom(r)
	adm := workspaceLoadAdmission{restricted: p != nil && !p.Has(workspaceops.RoleAdmin)}
	lookup, registerRoot := workspaceops.LookupWorkspaceRoot, ""
	if adm.restricted {
		path, root, err := workspaceops.CheckRegistrationRoot(req.RootPath, posture.registerRoots())
		if err != nil {
			denyRegistration(w, r, p, refusalCode(err), err.Error())
			return adm, false
		}
		req.RootPath, registerRoot = path, root
		lookup = workspaceops.LookupWorkspaceDir
	}
	before, err := lookup(dataDir(), req.RootPath)
	if err != nil || !posture.allowsWorkspace(before.ID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "workspace root is not served by this deployment", "reason": "workspace_not_allowed"})
		return adm, false
	}
	// /api/workspaces/load names no workspace, so the auth middleware leaves the
	// token's scope to this check, for admins too
	if p != nil && !p.AllowsWorkspace(before.ID) {
		denyRegistration(w, r, p, refusalTokenScope, req.RootPath+" registers as workspace "+before.ID+", outside this token's workspace scope")
		return adm, false
	}
	if adm.restricted {
		req.PreferCachedSnapshot = true
		req.AutoScan = !before.Registered
		if before.Registered {
			req.RootPath = before.RootPath
			name := before.Name
			req.Name = &name
		} else {
			req.Registration = &workspaceops.NonAdminRegistration{RegisterRoot: registerRoot, Principal: p.ID, Limit: posture.registerLimit()}
		}
	}
	adm.before = before
	return adm, true
}

// refusalCode is a refused registration's workspaceops.Refusal* code.
func refusalCode(err error) string {
	var refusal *workspaceops.RegistrationRefusal
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return workspaceops.RefusalInvalidPath
}

// finish audits the load and answers its error: a registration the registry
// refused (the principal's XMUSTARD_REGISTER_LIMIT) as a refused registration.
// It reports whether the load succeeded.
func (a workspaceLoadAdmission) finish(w http.ResponseWriter, r *http.Request, root string, err error) bool {
	a.audit(r, root)
	var refusal *workspaceops.RegistrationRefusal
	switch {
	case err == nil:
		return true
	case errors.As(err, &refusal) && a.restricted:
		denyRegistration(w, r, principalFromContext(r.Context()), refusal.Code, refusal.Message)
	default:
		respondError(w, err)
	}
	return false
}

// denyRegistration audits a refused non-admin registration and answers 403.
func denyRegistration(w http.ResponseWriter, r *http.Request, p *workspaceops.Principal, code, msg string) {
	workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
		Action: "denied", Actor: p.ID, Role: p.Role,
		Detail: "workspace registration refused (" + code + "): " + msg,
		Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
	})
	writeJSON(w, http.StatusForbidden, map[string]any{"error": msg, "reason": registrationNotAllowed, "refusal": code})
}

// audit records who registered root when this load created its registry entry
// (whether or not the scan that followed succeeded).
func (a workspaceLoadAdmission) audit(r *http.Request, root string) {
	if a.before.Registered {
		return
	}
	now, err := workspaceops.LookupWorkspaceRoot(dataDir(), root)
	if err != nil || !now.Registered {
		return
	}
	c := callerOf(r)
	// the root first: Detail is clipped, and the registration root is implied by it
	detail := "workspace " + now.ID + " at " + root
	if a.restricted {
		detail += " (non-admin, under XMUSTARD_REGISTER_ROOTS)"
	}
	workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
		Action: "register", Actor: c.ID, Role: c.Role, Detail: detail,
		Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
	})
}

// logRegisterRoots reports at startup where non-admin tokens may register.
func logRegisterRoots(p exposurePosture) {
	if len(p.RegisterRoots) > 0 {
		log.Printf("registration: non-admin tokens may register git work trees under %s (XMUSTARD_REGISTER_ROOTS), up to %d each (XMUSTARD_REGISTER_LIMIT)", strings.Join(p.RegisterRoots, string(filepath.ListSeparator)), p.registerLimit())
	}
}

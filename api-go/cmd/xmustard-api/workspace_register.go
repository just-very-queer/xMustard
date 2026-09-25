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
//     symlinks evaluated, at or below a directory in XMUSTARD_REGISTER_ROOTS
//     (workspaceops.CheckRegistrationRoot). The resolved path is what gets
//     registered. The load always prefers the cached snapshot, so a non-admin
//     never forces a rescan, and it keeps an existing workspace's name. A
//     workspace-scoped token registers only a workspace inside its scope.
//
// For every caller the root's workspace id must pass the deployment's workspace
// allowlist, and a load that creates a registry entry is recorded in the auth audit
// log (GET /api/auth/audit) with the principal that made it.

// registrationNotAllowed is the reason a refused non-admin registration answers
// with; "refusal" in the body carries a workspaceops.Refusal* code, or
// refusalTokenScope.
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
	adm := workspaceLoadAdmission{restricted: p != nil && !p.Has(workspaceops.RoleAdmin)}
	if adm.restricted {
		path, _, err := workspaceops.CheckRegistrationRoot(req.RootPath, postureFrom(r).registerRoots())
		if err != nil {
			code := workspaceops.RefusalInvalidPath
			var refusal *workspaceops.RegistrationRefusal
			if errors.As(err, &refusal) {
				code = refusal.Code
			}
			denyRegistration(w, r, p, code, err.Error())
			return adm, false
		}
		req.RootPath = path
		req.PreferCachedSnapshot = true
	}
	before, err := workspaceops.LookupWorkspaceRoot(dataDir(), req.RootPath)
	if err != nil || !postureFrom(r).allowsWorkspace(before.ID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "workspace root is not served by this deployment", "reason": "workspace_not_allowed"})
		return adm, false
	}
	if adm.restricted {
		if !p.AllowsWorkspace(before.ID) {
			denyRegistration(w, r, p, refusalTokenScope, req.RootPath+" registers as workspace "+before.ID+", outside this token's workspace scope")
			return adm, false
		}
		if before.Registered { // a non-admin load never renames a workspace
			name := before.Name
			req.Name = &name
		}
	}
	adm.before = before
	return adm, true
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
		log.Printf("registration: non-admin tokens may register git work trees under %s (XMUSTARD_REGISTER_ROOTS)", strings.Join(p.RegisterRoots, string(filepath.ListSeparator)))
	}
}

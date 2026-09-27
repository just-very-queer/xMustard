package main

import (
	"fmt"
	"net/http"

	"xmustard/api-go/internal/mcpserver"
	"xmustard/api-go/internal/workspaceops"
)

// Human approvers on the HTTP and MCP surfaces (WS-57). A human approver's token lets
// whoever holds it act as the human, and an MCP client runs a model that calls tools
// on its own. So a governed-memory write under a human approver's token that arrives
// through the MCP bridge (X-Xmustard-Issuer: mcp) is refused with reason
// human_presence_required unless the bridge says the human confirmed it through
// elicitation (X-Xmustard-Approval: elicitation); the bridge then asks the human and
// repeats the call. The header is the bridge's claim, not proof: any process that holds
// the token can send it. So every write a human approver makes over HTTP or MCP is
// labelled advisory, and only xmustard-ops with the token typed at the terminal records
// user_presence (workspaceops/human_approval.go).

// approvalLabel is the approval label of a human approver's write, "" for anyone else.
func approvalLabel(r *http.Request, p *workspaceops.Principal) string {
	if !workspaceops.IsHumanApprover(p) {
		return ""
	}
	surface := workspaceops.ApprovalSurfaceHTTP
	if humanConfirmed(r) {
		surface = workspaceops.ApprovalSurfaceMCP
	}
	return workspaceops.HumanApprovalLabel(surface, workspaceops.AssuranceAdvisory)
}

func humanConfirmed(r *http.Request) bool {
	return r.Header.Get(mcpserver.ApprovalHeader) == mcpserver.ApprovalElicited
}

// requireHumanPresence refuses a human approver's write that came through the MCP
// bridge unconfirmed, naming action for the confirmation the bridge asks for. It
// returns false once it has answered 403.
func requireHumanPresence(w http.ResponseWriter, r *http.Request, caller memoryCaller, action string) bool {
	if !workspaceops.IsHumanApprover(caller.principal) || r.Header.Get(mcpserver.IssuerHeader) != mcpserver.IssuerMCP ||
		humanConfirmed(r) {
		return true
	}
	workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
		Action: "denied", Actor: caller.id(), Detail: "human approver's MCP write without the human's confirmation: " + action,
		Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
	})
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": fmt.Sprintf("%s uses human approver %q's token through MCP and needs the human's confirmation "+
			"(MCP elicitation, or xmustard-ops approve|reject from a terminal)", action, caller.id()),
		"reason": mcpserver.ReasonHumanPresence,
		"action": action,
	})
	return false
}

// rememberAction and verifyAction describe a write for the human's confirmation. Only
// safe ids reach the text, so an agent cannot put free text in front of the human.
func rememberAction(workspaceID string, req workspaceops.RememberRequest) string {
	op := "propose"
	if req.Op != "" {
		op = shownID(req.Op)
	}
	if req.EntryID == "" {
		return fmt.Sprintf("remember (%s) a memory in workspace %s", op, shownID(workspaceID))
	}
	return fmt.Sprintf("remember (%s) memory %s in workspace %s", op, shownID(req.EntryID), shownID(workspaceID))
}

func verifyAction(workspaceID, entryID, outcome string, revision int64) string {
	if outcome == "" {
		outcome = workspaceops.OutcomeApprove
	}
	at := "its served revision"
	if revision > 0 {
		at = fmt.Sprintf("revision %d", revision)
	}
	return fmt.Sprintf("cast %s on memory %s (%s) in workspace %s", shownID(outcome), shownID(entryID), at, shownID(workspaceID))
}

func shownID(v string) string {
	if workspaceops.IsSafeID(v) {
		return v
	}
	return "(invalid)"
}

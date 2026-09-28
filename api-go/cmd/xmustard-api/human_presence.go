package main

import (
	"context"
	"fmt"
	"net/http"

	"xmustard/api-go/internal/mcpserver"
	"xmustard/api-go/internal/workspaceops"
)

// Human approvers on the HTTP and MCP surfaces (WS-57). A human approver's token lets
// whoever holds it act as the human, and an MCP client runs a model that calls tools
// on its own. So a governed-memory write under a human approver's token that arrives
// through the MCP bridge (X-Xmustard-Issuer: mcp) is held: refused with reason
// human_presence_required and the text the human must confirm (workspaceops
// DescribeRemember and DescribeVerify) and its digest. The bridge asks the human and
// repeats the call with X-Xmustard-Approval: elicitation and that digest; the write
// runs only when the digest still matches the text for the repeated call, and a verdict
// is pinned to the revision the human was shown. The headers are the bridge's claim,
// not proof: any process that holds the token can send them. So every write a human
// approver makes over HTTP or MCP is labelled advisory, and only xmustard-ops with a
// presence-only token typed at the terminal records user_presence
// (workspaceops/human_approval.go). A presence-only token never gets this far: the auth
// middleware refuses it as a bearer token.

// fromMCP reports whether the call names the MCP bridge as its issuer.
func fromMCP(r *http.Request) bool {
	return r.Header.Get(mcpserver.IssuerHeader) == mcpserver.IssuerMCP
}

// approvalLabel is the approval label of a human approver's write, "" for anyone else.
// Only a call from the bridge that says the human confirmed it is labelled
// mcp_elicitation.
func approvalLabel(r *http.Request, p *workspaceops.Principal) string {
	if !workspaceops.IsHumanApprover(p) {
		return ""
	}
	surface := workspaceops.ApprovalSurfaceHTTP
	if fromMCP(r) && r.Header.Get(mcpserver.ApprovalHeader) == mcpserver.ApprovalElicited {
		surface = workspaceops.ApprovalSurfaceMCP
	}
	return workspaceops.HumanApprovalLabel(surface, workspaceops.AssuranceAdvisory)
}

// holdForHuman holds a human approver's write that came through the MCP bridge until
// the human confirmed describe's text. It returns the confirmation the write runs under
// (nil when the write is not held) and false once it has answered: the write's own
// error for a write that cannot run, or the 403 that asks for the confirmation.
func holdForHuman(w http.ResponseWriter, r *http.Request, caller memoryCaller,
	describe func() (workspaceops.HumanConfirmation, error)) (*workspaceops.HumanConfirmation, bool) {
	if !workspaceops.IsHumanApprover(caller.principal) || !fromMCP(r) {
		return nil, true
	}
	c, err := describe()
	if err != nil {
		respondMemoryWrite(w, r, caller, nil, err)
		return nil, false
	}
	if r.Header.Get(mcpserver.ApprovalHeader) == mcpserver.ApprovalElicited &&
		r.Header.Get(mcpserver.ApprovalDigestHeader) == c.Digest {
		return &c, true
	}
	workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
		Action: "denied", Actor: caller.id(), Detail: "human approver's MCP write without the human's confirmation of digest " + c.Digest,
		Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
	})
	answerHeld(w, r, map[string]any{
		"error": fmt.Sprintf("this write uses human approver %q's token through MCP and needs the human's confirmation "+
			"(MCP elicitation, or xmustard-ops approve|reject from a terminal)", caller.id()),
		"reason":        mcpserver.ReasonHumanPresence,
		"action":        c.Text,
		"action_digest": c.Digest,
	})
	return nil, false
}

// heldWriteKey carries, on an evidence-delivered call, where a held write's refusal goes
// (see evidenceDeliveryMiddleware): the bridge acts on that 403, so it must reach the
// bridge as it is rather than as a captured tool result.
type heldWriteKey struct{}

func withHeldWriteSlot(ctx context.Context, slot *map[string]any) context.Context {
	return context.WithValue(ctx, heldWriteKey{}, slot)
}

// answerHeld answers 403 with a held write's refusal, handing it to the delivery
// middleware on a delivered call.
func answerHeld(w http.ResponseWriter, r *http.Request, refusal map[string]any) {
	if slot, ok := r.Context().Value(heldWriteKey{}).(*map[string]any); ok {
		*slot = refusal
		return
	}
	writeJSON(w, http.StatusForbidden, refusal)
}

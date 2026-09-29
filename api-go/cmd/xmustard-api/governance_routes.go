package main

import (
	"net/http"

	"xmustard/api-go/internal/workspaceops"
)

// Workspace governance (platform): run policy, the workspace audit log and security
// review (finding dispositions, the security review packet, acceptance criteria).
// Policy changes and risk acceptance need the human approver, audit writes admin;
// see route_gates.go.
var governanceRoutes = []routeSpec{
	{"GET /api/workspaces/{workspace_id}/policy", wsCall(workspaceops.GetWorkspacePolicy, respondError)},
	{"PUT /api/workspaces/{workspace_id}/policy", wsBodyCall(workspaceops.SetWorkspacePolicy, notFoundOrInvalid("Workspace not found"))},
	{"POST /api/workspaces/{workspace_id}/policy/evaluate", wsBodyCall(workspaceops.EvaluateRunAgainstPolicy, respondError)},
	{"GET /api/workspaces/{workspace_id}/audit-log", listAuditEvents},
	{"POST /api/workspaces/{workspace_id}/audit-log", wsBodyCall(workspaceops.RecordAuditEvent, notFoundOrInvalid("Workspace not found"))},
	{"GET /api/workspaces/{workspace_id}/security/dispositions", wsCall(workspaceops.ListSecurityDispositions, respondError)},
	{"GET /api/workspaces/{workspace_id}/security/findings/{finding_id}/disposition", itemCall("finding_id", workspaceops.GetSecurityDisposition, respondError)},
	{"PUT /api/workspaces/{workspace_id}/security/findings/{finding_id}/disposition", itemBodyCall("finding_id", workspaceops.SetSecurityDisposition, notFoundOrInvalid("Workspace not found"))},
	{"GET /api/workspaces/{workspace_id}/security/review-packet", wsCall(workspaceops.BuildSecurityReviewPacket, respondError)},
	{"GET /api/workspaces/{workspace_id}/security/acceptance-criteria", wsCall(workspaceops.GetSecurityAcceptanceCriteria, respondError)},
	{"PUT /api/workspaces/{workspace_id}/security/acceptance-criteria", wsBodyCall(workspaceops.SetSecurityAcceptanceCriteria, notFoundOrInvalid("Workspace not found"))},
	{"GET /api/workspaces/{workspace_id}/security/acceptance-evaluation", wsCall(workspaceops.EvaluateSecurityAcceptance, respondError)},
}

func listAuditEvents(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ListAuditEvents(dataDir(), r.PathValue("workspace_id"), queryInt(r, "limit", 0))
	reply(w, result, err, respondError)
}

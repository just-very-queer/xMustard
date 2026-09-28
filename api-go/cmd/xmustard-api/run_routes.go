package main

import (
	"net/http"
	"strconv"

	"xmustard/api-go/internal/workspaceops"
)

// Agent runs (platform): launching and probing runtimes, the run list and log,
// review and acceptance, plans, critiques and improvements, metrics and costs, the
// agent identity registry and the eval timeline. Approvals (accept, plan approve and
// reject) are gated to the human approver in route_gates.go; a plan approval never
// authorizes a merge.
var runRoutes = []routeSpec{
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/runs", startIssueRun},
	{"POST /api/workspaces/{workspace_id}/agent/probe", probeRuntime},
	{"POST /api/workspaces/{workspace_id}/agent/query", wsBodyCall(workspaceops.StartAgentQuery, respondError)},
	{"GET /api/workspaces/{workspace_id}/runs", wsCall(workspaceops.ListRuns, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/runs/{run_id}", itemCall("run_id", workspaceops.ReadRun, notFoundRun)},
	{"GET /api/workspaces/{workspace_id}/runs/{run_id}/log", readRunLog},
	{"GET /api/workspaces/{workspace_id}/review-queue", wsCall(workspaceops.ListReviewQueue, notFoundWorkspace)},
	{"POST /api/workspaces/{workspace_id}/runs/{run_id}/review", itemBodyCall("run_id", workspaceops.ReviewRun, runReviewFailure)},
	{"POST /api/workspaces/{workspace_id}/runs/{run_id}/accept", itemBodyCall("run_id", workspaceops.AcceptRunReview, runAcceptFailure)},
	{"POST /api/workspaces/{workspace_id}/runs/{run_id}/cancel", itemCall("run_id", workspaceops.CancelRun, notFoundRun)},
	{"POST /api/workspaces/{workspace_id}/runs/{run_id}/retry", itemCall("run_id", workspaceops.RetryRun, respondError)},
	{"POST /api/workspaces/{workspace_id}/runs/{run_id}/plan", itemCall("run_id", workspaceops.GenerateRunPlan, respondError)},
	{"GET /api/workspaces/{workspace_id}/runs/{run_id}/plan", itemCall("run_id", workspaceops.GetRunPlan, respondError)},
	{"POST /api/workspaces/{workspace_id}/runs/{run_id}/plan/approve", itemBodyCall("run_id", workspaceops.ApproveRunPlan, respondError)},
	{"POST /api/workspaces/{workspace_id}/runs/{run_id}/plan/reject", itemBodyCall("run_id", workspaceops.RejectRunPlan, notFoundAsIs)},
	{"GET /api/workspaces/{workspace_id}/runs/{run_id}/insights", itemCall("run_id", workspaceops.GetRunSessionInsight, notFoundRun)},
	{"GET /api/workspaces/{workspace_id}/runs/{run_id}/metrics", itemCall("run_id", workspaceops.GetRunMetrics, respondError)},
	{"GET /api/workspaces/{workspace_id}/metrics", wsCall(workspaceops.ListWorkspaceMetrics, notFoundAsIs)},
	{"GET /api/workspaces/{workspace_id}/costs", wsCall(workspaceops.GetWorkspaceCostSummary, notFoundAsIs)},
	{"POST /api/workspaces/{workspace_id}/runs/{run_id}/critique", itemCall("run_id", workspaceops.GeneratePatchCritique, respondError)},
	{"GET /api/workspaces/{workspace_id}/runs/{run_id}/critique", itemCall("run_id", workspaceops.GetPatchCritique, notFound("No critique found for this run"))},
	{"GET /api/workspaces/{workspace_id}/runs/{run_id}/improvements", itemCall("run_id", workspaceops.GetRunImprovements, notFoundAsIs)},
	{"POST /api/workspaces/{workspace_id}/runs/{run_id}/improvements/{suggestion_id}/dismiss", dismissImprovement},
	{"GET /api/workspaces/{workspace_id}/runs/{run_id}/confidence", itemCall("run_id", workspaceops.ScoreRunConfidence, respondError)},
	{"GET /api/workspaces/{workspace_id}/runs/{run_id}/brief", itemCall("run_id", workspaceops.BuildRunBrief, respondError)},
	{"GET /api/workspaces/{workspace_id}/eval-timeline", wsCall(workspaceops.BuildEvalTimeline, respondError)},
	{"GET /api/workspaces/{workspace_id}/agents", wsCall(workspaceops.ListAgentIdentities, respondError)},
	{"GET /api/workspaces/{workspace_id}/agents/{agent_id}", itemCall("agent_id", workspaceops.GetAgentIdentity, respondError)},
	{"POST /api/workspaces/{workspace_id}/agents/sync", wsCall(workspaceops.SyncAgentIdentitiesFromRuns, respondError)},
}

// The review operations report bad input only in their error text.
var (
	runReviewFailure = notFoundElse("Missing resource",
		invalidOnPhrase(withStatus(http.StatusInternalServerError), "workspace query", "only completed", "invalid disposition"))
	runAcceptFailure = notFoundElse("Missing resource",
		invalidOnPhrase(withStatus(http.StatusInternalServerError), "workspace query", "already has a recorded fix"))
)

// startIssueRun launches an agent run on an issue: an agent action, never a reader's.
func startIssueRun(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "agent") {
		return
	}
	var request workspaceops.RunRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	result, err := workspaceops.StartIssueRun(dataDir(), r.PathValue("workspace_id"), r.PathValue("issue_id"), request)
	reply(w, result, err, respondError)
}

func probeRuntime(w http.ResponseWriter, r *http.Request) {
	var request workspaceops.RuntimeProbeRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	result, err := workspaceops.ProbeRuntime(dataDir(), r.PathValue("workspace_id"), request.Runtime, request.Model)
	reply(w, result, err, respondError)
}

func readRunLog(w http.ResponseWriter, r *http.Request) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil {
		offset = 0 // absent or not a number: from the start
	}
	result, err := workspaceops.ReadRunLog(dataDir(), r.PathValue("workspace_id"), r.PathValue("run_id"), offset)
	reply(w, result, err, notFoundRun)
}

func dismissImprovement(w http.ResponseWriter, r *http.Request) {
	var request workspaceops.DismissImprovementRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	result, err := workspaceops.DismissImprovement(dataDir(), r.PathValue("workspace_id"), r.PathValue("run_id"), r.PathValue("suggestion_id"), request)
	reply(w, result, err, notFoundAsIs)
}

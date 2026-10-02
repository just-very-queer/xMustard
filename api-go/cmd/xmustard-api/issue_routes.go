package main

import (
	"net/http"
	"strconv"

	"xmustard/api-go/internal/workspaceops"
)

// Issues (platform): the issue queue, saved views, signals and fixes; the context an
// issue carries (tickets, browser dumps, context replays, threat models); and issue
// intelligence (quality, duplicates, triage, test suggestions, review packet,
// ownership, ticket ingestion, the versioned handoff).
var issueRoutes = []routeSpec{
	{"GET /api/workspaces/{workspace_id}/issues", listIssues},
	{"POST /api/workspaces/{workspace_id}/issues", wsBodyCall(workspaceops.CreateIssue, respondError)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}", itemCall("issue_id", workspaceops.ReadIssue, notFoundResource)},
	{"PATCH /api/workspaces/{workspace_id}/issues/{issue_id}", itemBodyCall("issue_id", workspaceops.UpdateIssue, notFoundResource)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/drift", itemCall("issue_id", workspaceops.ReadIssueDrift, notFoundResource)},
	{"GET /api/workspaces/{workspace_id}/views", wsCall(workspaceops.ListSavedViews, notFoundWorkspace)},
	{"POST /api/workspaces/{workspace_id}/views", wsBodyCall(workspaceops.CreateSavedView, respondError)},
	{"PUT /api/workspaces/{workspace_id}/views/{view_id}", itemBodyCall("view_id", workspaceops.UpdateSavedView, notFoundResource)},
	{"DELETE /api/workspaces/{workspace_id}/views/{view_id}", deleteSavedView},
	{"GET /api/workspaces/{workspace_id}/signals", listSignals},
	{"GET /api/workspaces/{workspace_id}/fixes", queryCall("issue_id", workspaceops.ListFixes, notFoundWorkspace)},
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/fixes", itemBodyCall("issue_id", workspaceops.RecordFix, notFoundResource)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/fix-draft", suggestFixDraft},

	// the context an issue carries
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/context", itemCall("issue_id", workspaceops.BuildIssueContextPacket, notFoundResource)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/work", readIssueWork},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context", itemCall("issue_id", workspaceops.ListTicketContexts, notFoundResource)},
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context", itemBodyCall("issue_id", workspaceops.SaveTicketContext, notFoundResource)},
	{"DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context/{context_id}", issueItemDelete("context_id", workspaceops.DeleteTicketContext)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps", itemCall("issue_id", workspaceops.ListBrowserDumps, notFoundResource)},
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps", itemBodyCall("issue_id", workspaceops.SaveBrowserDump, notFoundResource)},
	{"DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps/{dump_id}", issueItemDelete("dump_id", workspaceops.DeleteBrowserDump)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays", itemCall("issue_id", workspaceops.ListIssueContextReplays, notFoundResource)},
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays", captureContextReplay},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays/{replay_id}/compare", compareContextReplay},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models", itemCall("issue_id", workspaceops.ListThreatModels, notFoundResource)},
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models", itemBodyCall("issue_id", workspaceops.SaveThreatModel, notFoundResource)},
	{"DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models/{threat_model_id}", issueItemDelete("threat_model_id", workspaceops.DeleteThreatModel)},

	// issue intelligence
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/quality", itemCall("issue_id", workspaceops.ScoreIssueQuality, respondError)},
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/quality", itemCall("issue_id", workspaceops.ScoreIssueQuality, respondError)},
	{"POST /api/workspaces/{workspace_id}/quality/score-all", wsCall(workspaceops.ScoreAllIssueQuality, respondError)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/duplicates", itemCall("issue_id", workspaceops.FindDuplicates, respondError)},
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/triage", itemCall("issue_id", workspaceops.TriageIssue, respondError)},
	{"POST /api/workspaces/{workspace_id}/triage/all", wsCall(workspaceops.TriageAllIssues, respondError)},
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/test-suggestions", itemCall("issue_id", workspaceops.GenerateTestSuggestions, respondError)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/test-suggestions", itemCall("issue_id", workspaceops.ListTestSuggestions, respondError)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/review-packet", itemCall("issue_id", workspaceops.BuildReviewPacket, respondError)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/owner-suggestions", itemCall("issue_id", workspaceops.SuggestIssueOwners, respondError)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/ownership-history", itemCall("issue_id", workspaceops.BuildOwnershipHistory, respondError)},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/ingested-ticket", itemCall("issue_id", workspaceops.GetIngestedTicket, respondError)},
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/ingest-ticket", itemBodyCall("issue_id", workspaceops.IngestTicket, respondError)},
	// one versioned handoff merging review (verification and risk), security and the
	// latest run insight (acceptance and policy), for PR, Linear and Jira sync
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/handoff", itemCall("issue_id", workspaceops.BuildVersionedHandoff, respondError)},
}

func listIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	reviewReadyOnly, _ := strconv.ParseBool(q.Get("review_ready_only"))
	driftOnly, _ := strconv.ParseBool(q.Get("drift_only"))
	result, err := workspaceops.ListIssues(dataDir(), r.PathValue("workspace_id"), q.Get("q"),
		splitCSV(q.Get("severity")), splitCSV(q.Get("issue_status")), splitCSV(q.Get("source")), splitCSV(q.Get("label")),
		driftOnly, optionalBool(r, "needs_followup"), reviewReadyOnly)
	reply(w, result, err, notFoundSnapshot)
}

func listSignals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	result, err := workspaceops.ListSignals(dataDir(), r.PathValue("workspace_id"), q.Get("q"), q.Get("severity"), optionalBool(r, "promoted"))
	reply(w, result, err, notFoundSnapshot)
}

func deleteSavedView(w http.ResponseWriter, r *http.Request) {
	err := workspaceops.DeleteSavedView(dataDir(), r.PathValue("workspace_id"), r.PathValue("view_id"))
	reply(w, map[string]any{"ok": true}, err, notFoundResource)
}

func suggestFixDraft(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.SuggestFixDraft(dataDir(), r.PathValue("workspace_id"), r.PathValue("issue_id"), r.URL.Query().Get("run_id"))
	reply(w, result, err, notFoundResource)
}

func readIssueWork(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ReadIssueWork(dataDir(), r.PathValue("workspace_id"), r.PathValue("issue_id"), r.URL.Query().Get("runbook_id"))
	reply(w, result, err, notFoundResource)
}

func captureContextReplay(w http.ResponseWriter, r *http.Request) {
	var request workspaceops.IssueContextReplayRequest
	if !decodeJSONBodyIfPresent(w, r, &request) {
		return
	}
	result, err := workspaceops.CaptureIssueContextReplay(dataDir(), r.PathValue("workspace_id"), r.PathValue("issue_id"), request)
	reply(w, result, err, notFoundResource)
}

func compareContextReplay(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.CompareIssueContextReplay(dataDir(), r.PathValue("workspace_id"), r.PathValue("issue_id"), r.PathValue("replay_id"))
	reply(w, result, err, notFoundResource)
}

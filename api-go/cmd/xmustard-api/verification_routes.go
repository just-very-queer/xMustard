package main

import (
	"context"
	"net/http"
	"time"

	"xmustard/api-go/internal/workspaceops"
)

// Verification (platform): recorded verifications and outcomes, runbooks, saved
// verification profiles and their runs, coverage and verifier telemetry. Defining a
// profile (the commands a run executes) needs admin; see route_gates.go.
var verificationRoutes = []routeSpec{
	{"GET /api/workspaces/{workspace_id}/verifications", queryCall("issue_id", workspaceops.ListVerifications, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/verification-outcomes", wsCall(workspaceops.ReadVerificationOutcomes, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/runbooks", wsCall(workspaceops.ListRunbooks, notFoundWorkspace)},
	{"POST /api/workspaces/{workspace_id}/runbooks", wsBodyCall(workspaceops.SaveRunbook, notFoundWorkspace)},
	{"DELETE /api/workspaces/{workspace_id}/runbooks/{runbook_id}", itemDelete("runbook_id", workspaceops.DeleteRunbook)},
	{"GET /api/workspaces/{workspace_id}/verification-profiles", wsCall(workspaceops.ListVerificationProfiles, notFoundWorkspace)},
	{"POST /api/workspaces/{workspace_id}/verification-profiles", wsBodyCall(workspaceops.SaveVerificationProfile, notFoundWorkspace)},
	{"DELETE /api/workspaces/{workspace_id}/verification-profiles/{profile_id}", itemDelete("profile_id", workspaceops.DeleteVerificationProfile)},
	{"GET /api/workspaces/{workspace_id}/verification-profile-history", listVerificationProfileHistory},
	{"GET /api/workspaces/{workspace_id}/verification-profile-reports", queryCall("issue_id", workspaceops.ListVerificationProfileReports, notFoundWorkspace)},
	{"POST /api/workspaces/{workspace_id}/issues/{issue_id}/verification-profiles/{profile_id}/run", runVerificationProfile},
	{"POST /api/workspaces/{workspace_id}/coverage/parse", parseCoverageReport},
	{"GET /api/workspaces/{workspace_id}/coverage", readCoverage},
	{"GET /api/workspaces/{workspace_id}/issues/{issue_id}/coverage-delta", itemCall("issue_id", workspaceops.GetCoverageDelta, notFoundAsIs)},
	// verifier reliability and pairwise agreement: the evidence a future weighted quorum
	// would use; the gate itself stays an unweighted distinct-agent threshold
	{"GET /api/workspaces/{workspace_id}/verifier-telemetry", wsCall(workspaceops.ComputeVerifierTelemetry, respondError)},
}

func listVerificationProfileHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	result, err := workspaceops.ListVerificationProfileHistory(dataDir(), r.PathValue("workspace_id"), q.Get("profile_id"), q.Get("issue_id"))
	reply(w, result, err, notFoundWorkspace)
}

// runVerificationProfile runs a saved profile's commands for an issue, bounded to two
// minutes.
func runVerificationProfile(w http.ResponseWriter, r *http.Request) {
	workspaceID, issueID, profileID := r.PathValue("workspace_id"), r.PathValue("issue_id"), r.PathValue("profile_id")
	if workspaceID == "" || issueID == "" || profileID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing route parameters"})
		return
	}
	var request struct {
		RunID string `json:"run_id"`
	}
	if !decodeJSONBodyIfPresent(w, r, &request) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := workspaceops.RunIssueVerificationProfile(ctx, dataDir(), workspaceID, issueID, profileID, request.RunID)
	reply(w, result, err, notFoundAsIs)
}

// parseCoverageReport parses a coverage report inside the workspace, bounded to 30 s.
func parseCoverageReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	workspaceID, reportPath := r.PathValue("workspace_id"), q.Get("report_path")
	if workspaceID == "" || reportPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing workspace_id or report_path"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := workspaceops.ParseCoverageReport(ctx, dataDir(), workspaceID, reportPath, q.Get("run_id"), q.Get("issue_id"))
	reply(w, result, err, notFoundAsIs)
}

func readCoverage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	result, err := workspaceops.GetCoverage(dataDir(), r.PathValue("workspace_id"), q.Get("issue_id"), q.Get("run_id"))
	reply(w, result, err, notFound("No coverage data found"))
}

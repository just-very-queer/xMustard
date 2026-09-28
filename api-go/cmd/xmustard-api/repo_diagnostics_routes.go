package main

import (
	"errors"
	"net/http"
	"os"

	"xmustard/api-go/internal/workspaceops"
)

// Diagnostics wiring, all of it in this file: the diagnostics tool's route (core) and
// the platform's status, live and run routes, each handler with its error answer.
// Nothing else in the API registers or answers diagnostics, so work on diagnostics
// (the local diagnostics store, the live set) changes the handlers here.
var diagnosticsRoutes = []routeSpec{
	{"GET /api/workspaces/{workspace_id}/diagnostics", readDiagnostics},
	{"GET /api/workspaces/{workspace_id}/diagnostics/status", readDiagnosticsStatus},
	{"GET /api/workspaces/{workspace_id}/diagnostics/live", readLiveDiagnostics},
	{"POST /api/workspaces/{workspace_id}/diagnostics/run", runDiagnostics},
}

// readDiagnostics serves the diagnostics tool.
func readDiagnostics(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ReadDiagnosticsCtx(r.Context(), dataDir(), r.PathValue("workspace_id"), r.URL.Query().Get("diagnostic_run_id"))
	reply(w, result, err, diagnosticsReadFailure)
}

// diagnosticsReadFailure answers a failed diagnostics read: 404 for a missing
// workspace, 400 for an invalid request, the typed respondError otherwise.
func diagnosticsReadFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Workspace not found"})
	case errors.Is(err, workspaceops.ErrInvalidDiagnosticsRequest):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	default:
		respondError(w, err)
	}
}

func readDiagnosticsStatus(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ReadDiagnosticsStatus(dataDir(), r.PathValue("workspace_id"))
	reply(w, result, err, notFoundWorkspace)
}

func readLiveDiagnostics(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ReadLiveDiagnostics(dataDir(), r.PathValue("workspace_id"), r.URL.Query().Get("path"))
	reply(w, result, err, notFound("Workspace or path not found", workspaceops.ErrInvalidSemanticRequest))
}

func runDiagnostics(w http.ResponseWriter, r *http.Request) {
	var request workspaceops.DiagnosticsRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	result, err := workspaceops.RunDiagnostics(dataDir(), r.PathValue("workspace_id"), request)
	reply(w, result, err, notFound("Workspace not found", workspaceops.ErrInvalidDiagnosticsRequest))
}

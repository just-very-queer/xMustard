package main

import (
	"encoding/json"
	"net/http"

	"xmustard/api-go/internal/workspaceops"
)

// Workspace registry and repository reads: listing and registration (core), and the
// platform's snapshot, scan, activity, tree, guidance and repository views.
var workspaceRoutes = []routeSpec{
	{"GET /api/workspaces", listWorkspaces},
	{"POST /api/workspaces/load", loadWorkspace},
	{"GET /api/workspaces/{workspace_id}/snapshot", wsCall(workspaceops.ReadWorkspaceSnapshot, notFoundSnapshot)},
	{"POST /api/workspaces/{workspace_id}/scan", wsCall(workspaceops.ScanWorkspace, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/sources", wsCall(workspaceops.ReadSources, notFoundSnapshot)},
	{"GET /api/workspaces/{workspace_id}/drift", wsCall(workspaceops.ReadDriftSummary, notFoundSnapshot)},
	{"GET /api/workspaces/{workspace_id}/worktree", wsCall(workspaceops.ReadWorktreeStatus, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/repo-state", wsCall(workspaceops.ReadRepoToolState, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/repo-context", queryCall("base_ref", workspaceops.ReadRepoContext, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/project-info", wsCall(workspaceops.ReadProjectInfo, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/ingestion-plan", wsCall(workspaceops.ReadIngestionPlan, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/run-targets", wsCall(workspaceops.ReadRunTargets, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/verify-targets", wsCall(workspaceops.ReadVerifyTargets, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/tree", queryCall("relative_path", workspaceops.ListWorkspaceTree, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/export", wsCall(workspaceops.ExportWorkspace, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/activity", listActivity},
	{"GET /api/workspaces/{workspace_id}/activity/overview", readActivityOverview},
	{"GET /api/workspaces/{workspace_id}/dashboard", wsCall(workspaceops.BuildWorkspaceDashboard, respondError)},
	{"GET /api/workspaces/{workspace_id}/fingerprint", wsCall(workspaceops.WorkspaceFingerprint, respondError)},
	{"GET /api/workspaces/{workspace_id}/guidance", wsCall(workspaceops.ListWorkspaceGuidanceRecords, notFoundWorkspace)},
	{"POST /api/workspaces/{workspace_id}/guidance/customize", customizeGuidance},
	// a deterministic starter guidance file, written with confined no-follow writes
	{"POST /api/workspaces/{workspace_id}/guidance/starters", generateGuidanceStarter},
}

// listWorkspaces lists the workspaces the caller's token scope admits (a
// workspace-scoped token cannot enumerate other tenants' repository roots; an
// unscoped one sees all) and then those the deployment's allowlist serves.
func listWorkspaces(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ListWorkspacesScoped(dataDir(), principalFromContext(r.Context()))
	if err != nil {
		withStatus(http.StatusInternalServerError)(w, err)
		return
	}
	n := 0
	for _, it := range result {
		if postureFrom(r).allowsWorkspace(it.WorkspaceID) {
			result[n] = it
			n++
		}
	}
	writeJSON(w, http.StatusOK, result[:n])
}

func loadWorkspace(w http.ResponseWriter, r *http.Request) {
	var request workspaceops.WorkspaceLoadRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	admitted, ok := admitWorkspaceLoad(w, r, &request)
	if !ok {
		return
	}
	dir := dataDir()
	result, err := workspaceops.LoadWorkspace(dir, request)
	if !admitted.finish(w, r, request.RootPath, err) {
		return
	}
	// the automatic index baseline (PAR-FRESH-06), built after this answer
	workspaceops.StartRegistrationBaseline(dir, result.Workspace.WorkspaceID)
	writeJSON(w, http.StatusOK, result)
}

func listActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	result, err := workspaceops.ListWorkspaceActivity(dataDir(), r.PathValue("workspace_id"), q.Get("issue_id"), q.Get("run_id"), queryInt(r, "limit", 100))
	reply(w, result, err, notFoundWorkspace)
}

func readActivityOverview(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ReadActivityOverview(dataDir(), r.PathValue("workspace_id"), queryInt(r, "limit", 200))
	reply(w, result, err, notFoundWorkspace)
}

func customizeGuidance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	result, err := workspaceops.BuildGuidanceCustomization(dataDir(), r.PathValue("workspace_id"), body.Kind)
	reply(w, result, err, respondError)
}

func generateGuidanceStarter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TemplateID string `json:"template_id"`
		Overwrite  bool   `json:"overwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, workspaceops.Invalid("invalid JSON body"))
		return
	}
	result, err := workspaceops.GenerateGuidanceStarter(dataDir(), r.PathValue("workspace_id"), body.TemplateID, body.Overwrite)
	reply(w, result, err, respondError)
}

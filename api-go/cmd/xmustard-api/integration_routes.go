package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"xmustard/api-go/internal/rustcore"
	"xmustard/api-go/internal/workspaceops"
)

type integrationRouteSpec struct {
	Method string
	Path   string
}

func (spec integrationRouteSpec) Pattern() string {
	return spec.Method + " " + spec.Path
}

var (
	configureIntegrationRoute = integrationRouteSpec{
		Method: http.MethodPost,
		Path:   "/api/workspaces/{workspace_id}/integrations",
	}
	listIntegrationConfigsRoute = integrationRouteSpec{
		Method: http.MethodGet,
		Path:   "/api/workspaces/{workspace_id}/integrations",
	}
	testIntegrationRoute = integrationRouteSpec{
		Method: http.MethodPost,
		Path:   "/api/integrations/test",
	}
	importGitHubIssuesRoute = integrationRouteSpec{
		Method: http.MethodPost,
		Path:   "/api/workspaces/{workspace_id}/integrations/github/import",
	}
	createGitHubPRRoute = integrationRouteSpec{
		Method: http.MethodPost,
		Path:   "/api/workspaces/{workspace_id}/integrations/github/pr",
	}
	sendSlackNotificationRoute = integrationRouteSpec{
		Method: http.MethodPost,
		Path:   "/api/workspaces/{workspace_id}/integrations/slack/notify",
	}
	syncIssueToLinearRoute = integrationRouteSpec{
		Method: http.MethodPost,
		Path:   "/api/workspaces/{workspace_id}/integrations/linear/sync/{issue_id}",
	}
	syncIssueToJiraRoute = integrationRouteSpec{
		Method: http.MethodPost,
		Path:   "/api/workspaces/{workspace_id}/integrations/jira/sync/{issue_id}",
	}
)

func listIntegrationRouteSpecs() []integrationRouteSpec {
	return []integrationRouteSpec{
		configureIntegrationRoute,
		listIntegrationConfigsRoute,
		testIntegrationRoute,
		importGitHubIssuesRoute,
		createGitHubPRRoute,
		sendSlackNotificationRoute,
		syncIssueToLinearRoute,
		syncIssueToJiraRoute,
	}
}

func registerIntegrationRoutes(mux *http.ServeMux, dataDir string) {
	mux.HandleFunc(configureIntegrationRoute.Pattern(), func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		provider := strings.TrimSpace(r.URL.Query().Get("provider"))
		settings := map[string]any{}
		if err := decodeOptionalJSONBody(r, &settings); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		result, err := workspaceops.ConfigureIntegration(dataDir, workspaceID, provider, settings)
		if writeIntegrationError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc(listIntegrationConfigsRoute.Pattern(), func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.GetIntegrationConfigs(dataDir, workspaceID)
		if writeIntegrationError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc(testIntegrationRoute.Pattern(), func(w http.ResponseWriter, r *http.Request) {
		var request workspaceops.IntegrationTestRequest
		if !decodeJSONBody(w, r, &request) {
			return
		}
		writeJSON(w, http.StatusOK, workspaceops.TestIntegration(request))
	})

	mux.HandleFunc(importGitHubIssuesRoute.Pattern(), func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ImportGitHubIssues(dataDir, workspaceID, r.URL.Query().Get("repo"), r.URL.Query().Get("state"))
		if writeIntegrationError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc(createGitHubPRRoute.Pattern(), func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.GitHubPRCreate
		if !decodeJSONBody(w, r, &request) {
			return
		}
		request.WorkspaceID = workspaceID
		result, err := workspaceops.CreateGitHubPR(dataDir, workspaceID, request)
		if writeIntegrationError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc(sendSlackNotificationRoute.Pattern(), func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		event := strings.TrimSpace(r.URL.Query().Get("event"))
		message := strings.TrimSpace(r.URL.Query().Get("message"))
		result, err := workspaceops.SendSlackNotification(dataDir, workspaceID, event, optionalPtr(message))
		if writeIntegrationError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc(syncIssueToLinearRoute.Pattern(), func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		result, err := workspaceops.SyncIssueToLinear(dataDir, workspaceID, issueID)
		if writeIntegrationError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc(syncIssueToJiraRoute.Pattern(), func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		result, err := workspaceops.SyncIssueToJira(dataDir, workspaceID, issueID)
		if writeIntegrationError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
}

func buildAgentSurfacesPayload(contract *rustcore.ArchitectureContract) map[string]any {
	return map[string]any{
		"design_version":                 contract.DesignVersion,
		"control_plane_owner":            contract.ControlPlaneOwner,
		"core_owner":                     contract.CoreOwner,
		"python_end_state":               contract.PythonEndState,
		"steady_state_runtime_budget_mb": contract.SteadyStateRuntimeBudgetMB,
		"agent_surfaces":                 contract.AgentSurfaces,
		"next_removable_python_boundary": contract.NextRemovablePythonBoundary,
		"plugin_manifests":               workspaceops.ListIntegrationManifests(),
	}
}

func writeIntegrationError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return true
	}
	if workspaceops.IsIntegrationValidationError(err) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return true
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	return true
}

func decodeOptionalJSONBody(r *http.Request, target any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(payload))) == 0 {
		return nil
	}
	return json.Unmarshal(payload, target)
}

func optionalPtr(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

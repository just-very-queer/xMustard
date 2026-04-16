package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"xmustard/api-go/internal/workspaceops"
)

func registerEvalRoutes(mux *http.ServeMux, dataDir string) {
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/eval-scenarios", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ListEvalScenarios(dataDir, workspaceID, r.URL.Query().Get("issue_id"))
		if writeMissingResourceOrError(w, err, "Workspace not found") {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /api/workspaces/{workspace_id}/eval-scenarios", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.EvalScenarioUpsertRequest
		if !decodeJSONBody(w, r, &request) {
			return
		}
		result, err := workspaceops.SaveEvalScenario(dataDir, workspaceID, request)
		if writeMissingResourceOrError(w, err, "Missing resource") {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("DELETE /api/workspaces/{workspace_id}/eval-scenarios/{scenario_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		scenarioID := r.PathValue("scenario_id")
		err := workspaceops.DeleteEvalScenario(dataDir, workspaceID, scenarioID)
		if writeMissingResourceOrError(w, err, "Eval scenario not found") {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"scenario_id": scenarioID,
		})
	})

	mux.HandleFunc("GET /api/workspaces/{workspace_id}/eval-report", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.GetEvalReport(dataDir, workspaceID, r.URL.Query().Get("scenario_id"))
		if writeMissingResourceOrError(w, err, "Missing resource") {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/eval-scenarios/replay", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		var request workspaceops.EvalScenarioReplayRequest
		if !decodeJSONBody(w, r, &request) {
			return
		}
		result, err := workspaceops.ReplayEvalScenarios(dataDir, workspaceID, issueID, request)
		if writeMissingResourceOrError(w, err, "Missing resource") {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid JSON body",
		})
		return false
	}
	return true
}

func writeMissingResourceOrError(w http.ResponseWriter, err error, notFoundMessage string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": notFoundMessage,
		})
		return true
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"error": err.Error(),
	})
	return true
}

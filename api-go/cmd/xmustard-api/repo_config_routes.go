package main

import (
	"errors"
	"net/http"
	"os"

	"xmustard/api-go/internal/workspaceops"
)

func registerRepoConfigRoutes(mux *http.ServeMux, dataDir string) {
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/repo-config", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadWorkspaceRepoConfig(dataDir, workspaceID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace not found",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/repo-config/health", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.GetWorkspaceRepoConfigHealth(dataDir, workspaceID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace not found",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
}

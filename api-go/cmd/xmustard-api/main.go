package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"xmustard/api-go/internal/migration"
	"xmustard/api-go/internal/rustcore"
	"xmustard/api-go/internal/workspaceops"
)

type verificationRunRequest struct {
	WorkspaceRoot  string `json:"workspace_root"`
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type verificationProfileRunRequest struct {
	WorkspaceRoot string                            `json:"workspace_root"`
	Profile       rustcore.VerificationProfileInput `json:"profile"`
	RunID         string                            `json:"run_id"`
	IssueID       string                            `json:"issue_id"`
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "api-go",
		})
	})
	mux.HandleFunc("/api/migration/plan", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		contract, err := rustcore.ReadArchitectureContract(ctx)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, contract)
	})
	mux.HandleFunc("/api/migration/agent-surfaces", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		contract, err := rustcore.ReadArchitectureContract(ctx)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, buildAgentSurfacesPayload(contract))
	})
	mux.HandleFunc("/api/migration/routes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(migration.APIRouteGroupsJSON)
	})
	mux.HandleFunc("/api/migration/scan-signals", func(w http.ResponseWriter, r *http.Request) {
		rootPath := r.URL.Query().Get("root_path")
		if rootPath == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "missing root_path query parameter",
			})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		signals, err := rustcore.ScanSignals(ctx, rootPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, signals)
	})
	mux.HandleFunc("/api/migration/repo-map", func(w http.ResponseWriter, r *http.Request) {
		rootPath := r.URL.Query().Get("root_path")
		workspaceID := r.URL.Query().Get("workspace_id")
		if rootPath == "" || workspaceID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "missing workspace_id or root_path query parameter",
			})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		summary, err := rustcore.BuildRepoMap(ctx, workspaceID, rootPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, summary)
	})
	mux.HandleFunc("/api/migration/coverage/lcov", func(w http.ResponseWriter, r *http.Request) {
		reportPath := r.URL.Query().Get("report_path")
		workspaceID := r.URL.Query().Get("workspace_id")
		runID := r.URL.Query().Get("run_id")
		issueID := r.URL.Query().Get("issue_id")
		if reportPath == "" || workspaceID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "missing workspace_id or report_path query parameter",
			})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		result, err := rustcore.ParseLCOVCoverage(ctx, workspaceID, reportPath, runID, issueID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("/api/migration/coverage", func(w http.ResponseWriter, r *http.Request) {
		reportPath := r.URL.Query().Get("report_path")
		workspaceID := r.URL.Query().Get("workspace_id")
		runID := r.URL.Query().Get("run_id")
		issueID := r.URL.Query().Get("issue_id")
		if reportPath == "" || workspaceID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "missing workspace_id or report_path query parameter",
			})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		result, err := rustcore.ParseCoverage(ctx, workspaceID, reportPath, runID, issueID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("/api/migration/verification/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
				"error": "method not allowed",
			})
			return
		}

		var request verificationRunRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		if request.WorkspaceRoot == "" || request.Command == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "missing workspace_root or command",
			})
			return
		}
		if request.TimeoutSeconds < 1 {
			request.TimeoutSeconds = 30
		}

		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(request.TimeoutSeconds+5)*time.Second)
		defer cancel()

		result, err := rustcore.RunVerificationCommand(ctx, request.WorkspaceRoot, request.TimeoutSeconds, request.Command)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("/api/migration/verification/profile-run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
				"error": "method not allowed",
			})
			return
		}

		var request verificationProfileRunRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		if request.WorkspaceRoot == "" || request.Profile.ProfileID == "" || request.Profile.WorkspaceID == "" || request.Profile.TestCommand == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "missing workspace_root or required profile fields",
			})
			return
		}

		timeoutSeconds := request.Profile.MaxRuntimeSeconds
		if timeoutSeconds < 1 {
			timeoutSeconds = 30
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSeconds+5)*time.Second)
		defer cancel()

		result, err := rustcore.RunVerificationProfile(
			ctx,
			request.WorkspaceRoot,
			request.Profile,
			request.RunID,
			request.IssueID,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/runtimes", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.DetectRuntimes(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.GetSettings(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var request workspaceops.AppSettings
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.UpdateSettings(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			request,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/agent/capabilities", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.GetLocalAgentCapabilities(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/agent/surfaces", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		contract, err := rustcore.ReadArchitectureContract(ctx)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, buildAgentSurfacesPayload(contract))
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/agent/probe", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.RuntimeProbeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.ProbeRuntime(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			request.Runtime,
			request.Model,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
				})
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "model") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ListWorkspaces(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /api/workspaces/load", func(w http.ResponseWriter, r *http.Request) {
		var request workspaceops.WorkspaceLoadRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.LoadWorkspace(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace snapshot not found",
				})
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "root_path") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
				})
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "not yet migrated") {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadWorkspaceSnapshot(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Snapshot not found",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/worktree", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadWorktreeStatus(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/runs", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		var request workspaceops.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.StartIssueRun(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "model") ||
				strings.Contains(strings.ToLower(err.Error()), "runtime") ||
				strings.Contains(strings.ToLower(err.Error()), "instruction") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/agent/query", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.AgentQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.StartAgentQuery(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "prompt is required") ||
				strings.Contains(strings.ToLower(err.Error()), "model") ||
				strings.Contains(strings.ToLower(err.Error()), "runtime") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/scan", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ScanWorkspace(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/activity", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.URL.Query().Get("issue_id")
		runID := r.URL.Query().Get("run_id")
		limit := 100
		if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
			if parsed, err := strconv.Atoi(rawLimit); err == nil {
				limit = parsed
			}
		}
		result, err := workspaceops.ListWorkspaceActivity(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			runID,
			limit,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/activity/overview", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		limit := 200
		if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
			if parsed, err := strconv.Atoi(rawLimit); err == nil {
				limit = parsed
			}
		}
		result, err := workspaceops.ReadActivityOverview(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			limit,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		queryValues := r.URL.Query()
		var needsFollowup *bool
		if raw := queryValues.Get("needs_followup"); raw != "" {
			if parsed, err := strconv.ParseBool(raw); err == nil {
				needsFollowup = &parsed
			}
		}
		reviewReadyOnly, _ := strconv.ParseBool(queryValues.Get("review_ready_only"))
		driftOnly, _ := strconv.ParseBool(queryValues.Get("drift_only"))
		result, err := workspaceops.ListIssues(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			queryValues.Get("q"),
			splitCSV(queryValues.Get("severity")),
			splitCSV(queryValues.Get("issue_status")),
			splitCSV(queryValues.Get("source")),
			splitCSV(queryValues.Get("label")),
			driftOnly,
			needsFollowup,
			reviewReadyOnly,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Snapshot not found",
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.IssueCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.CreateIssue(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace not found",
				})
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "already exists") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		result, err := workspaceops.ReadIssue(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("PATCH /api/workspaces/{workspace_id}/issues/{issue_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		var request workspaceops.IssueUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.UpdateIssue(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/drift", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		result, err := workspaceops.ReadIssueDrift(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/views", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ListSavedViews(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/views", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.SavedIssueViewRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.CreateSavedView(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			request,
		)
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
	mux.HandleFunc("PUT /api/workspaces/{workspace_id}/views/{view_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		viewID := r.PathValue("view_id")
		var request workspaceops.SavedIssueViewRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.UpdateSavedView(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			viewID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("DELETE /api/workspaces/{workspace_id}/views/{view_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		viewID := r.PathValue("view_id")
		if err := workspaceops.DeleteSavedView(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			viewID,
		); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/signals", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		queryValues := r.URL.Query()
		var promoted *bool
		if raw := queryValues.Get("promoted"); raw != "" {
			if parsed, err := strconv.ParseBool(raw); err == nil {
				promoted = &parsed
			}
		}
		result, err := workspaceops.ListSignals(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			queryValues.Get("q"),
			queryValues.Get("severity"),
			promoted,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Snapshot not found",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/fixes", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.URL.Query().Get("issue_id")
		result, err := workspaceops.ListFixes(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/verifications", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.URL.Query().Get("issue_id")
		result, err := workspaceops.ListVerifications(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/review-queue", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ListReviewQueue(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ListRuns(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs/{run_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		result, err := workspaceops.ReadRun(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Run not found",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs/{run_id}/log", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		var offset int64
		if rawOffset := r.URL.Query().Get("offset"); rawOffset != "" {
			if parsed, err := strconv.ParseInt(rawOffset, 10, 64); err == nil {
				offset = parsed
			}
		}
		result, err := workspaceops.ReadRunLog(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
			offset,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Run not found",
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/runs/{run_id}/review", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		var request workspaceops.RunReviewRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.ReviewRun(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			lowered := strings.ToLower(err.Error())
			if strings.Contains(lowered, "workspace query") ||
				strings.Contains(lowered, "only completed") ||
				strings.Contains(lowered, "invalid disposition") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/runs/{run_id}/accept", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		var request workspaceops.RunAcceptRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.AcceptRunReview(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			lowered := strings.ToLower(err.Error())
			if strings.Contains(lowered, "workspace query") ||
				strings.Contains(lowered, "already has a recorded fix") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/runs/{run_id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		result, err := workspaceops.CancelRun(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Run not found",
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/runs/{run_id}/retry", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		result, err := workspaceops.RetryRun(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Run not found",
				})
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "model") ||
				strings.Contains(strings.ToLower(err.Error()), "runtime") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/runs/{run_id}/plan", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		result, err := workspaceops.GenerateRunPlan(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
				})
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "cannot generate plan") ||
				strings.Contains(strings.ToLower(err.Error()), "runtime") ||
				strings.Contains(strings.ToLower(err.Error()), "model") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs/{run_id}/plan", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		result, err := workspaceops.GetRunPlan(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || strings.Contains(strings.ToLower(err.Error()), "no plan found") {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "No plan found for this run",
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/runs/{run_id}/plan/approve", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		var request workspaceops.PlanApproveRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.ApproveRunPlan(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || strings.Contains(strings.ToLower(err.Error()), "no plan found") {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
				})
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "awaiting approval") ||
				strings.Contains(strings.ToLower(err.Error()), "runtime") ||
				strings.Contains(strings.ToLower(err.Error()), "model") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/runs/{run_id}/plan/reject", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		var request workspaceops.PlanRejectRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.RejectRunPlan(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs/{run_id}/insights", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		result, err := workspaceops.GetRunSessionInsight(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Run not found",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs/{run_id}/metrics", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		result, err := workspaceops.GetRunMetrics(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || strings.Contains(strings.ToLower(err.Error()), "no metrics found") {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/metrics", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ListWorkspaceMetrics(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/costs", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.GetWorkspaceCostSummary(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/runs/{run_id}/critique", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		result, err := workspaceops.GeneratePatchCritique(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
				})
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "cannot critique run in status") {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs/{run_id}/critique", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		result, err := workspaceops.GetPatchCritique(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "No critique found for this run",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs/{run_id}/improvements", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		result, err := workspaceops.GetRunImprovements(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/runs/{run_id}/improvements/{suggestion_id}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runID := r.PathValue("run_id")
		suggestionID := r.PathValue("suggestion_id")
		var request workspaceops.DismissImprovementRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.DismissImprovement(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runID,
			suggestionID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/sources", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadSources(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Snapshot not found",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/drift", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadDriftSummary(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Snapshot not found",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/tree", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		relativePath := r.URL.Query().Get("relative_path")
		result, err := workspaceops.ListWorkspaceTree(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			relativePath,
		)
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/fixes", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		var request workspaceops.FixRecordRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.RecordFix(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/fix-draft", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		runID := r.URL.Query().Get("run_id")
		result, err := workspaceops.SuggestFixDraft(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/guidance", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ListWorkspaceGuidanceRecords(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/repo-map", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadWorkspaceRepoMap(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/export", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ExportWorkspace(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/verification-profiles/{profile_id}/run", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		profileID := r.PathValue("profile_id")
		if workspaceID == "" || issueID == "" || profileID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "missing route parameters",
			})
			return
		}

		var request struct {
			RunID string `json:"run_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()

		result, err := workspaceops.RunIssueVerificationProfile(
			ctx,
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			profileID,
			request.RunID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runbooks", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ListRunbooks(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/runbooks", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.RunbookUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.SaveRunbook(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			request,
		)
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
	mux.HandleFunc("DELETE /api/workspaces/{workspace_id}/runbooks/{runbook_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		runbookID := r.PathValue("runbook_id")
		err := workspaceops.DeleteRunbook(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			runbookID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"runbook_id": runbookID,
		})
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/verification-profiles", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ListVerificationProfiles(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/verification-profile-history", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		profileID := r.URL.Query().Get("profile_id")
		issueID := r.URL.Query().Get("issue_id")
		result, err := workspaceops.ListVerificationProfileHistory(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			profileID,
			issueID,
		)
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/verification-profile-reports", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.URL.Query().Get("issue_id")
		result, err := workspaceops.ListVerificationProfileReports(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
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
	registerEvalRoutes(mux, envDefault("XMUSTARD_DATA_DIR", "../backend/data"))
	registerRepoConfigRoutes(mux, envDefault("XMUSTARD_DATA_DIR", "../backend/data"))
	registerIntegrationRoutes(mux, envDefault("XMUSTARD_DATA_DIR", "../backend/data"))
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/verification-profiles", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.VerificationProfileUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.SaveVerificationProfile(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			request,
		)
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
	mux.HandleFunc("DELETE /api/workspaces/{workspace_id}/verification-profiles/{profile_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		profileID := r.PathValue("profile_id")
		err := workspaceops.DeleteVerificationProfile(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			profileID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"profile_id": profileID,
		})
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/context", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		result, err := workspaceops.BuildIssueContextPacket(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/work", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		runbookID := r.URL.Query().Get("runbook_id")
		result, err := workspaceops.ReadIssueWork(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			runbookID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		result, err := workspaceops.ListTicketContexts(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		var request workspaceops.TicketContextUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.SaveTicketContext(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context/{context_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		contextID := r.PathValue("context_id")
		err := workspaceops.DeleteTicketContext(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			contextID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"context_id": contextID,
		})
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		result, err := workspaceops.ListBrowserDumps(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		var request workspaceops.BrowserDumpUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.SaveBrowserDump(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps/{dump_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		dumpID := r.PathValue("dump_id")
		err := workspaceops.DeleteBrowserDump(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			dumpID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"dump_id": dumpID,
		})
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		result, err := workspaceops.ListIssueContextReplays(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		var request workspaceops.IssueContextReplayRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.CaptureIssueContextReplay(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays/{replay_id}/compare", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		replayID := r.PathValue("replay_id")
		result, err := workspaceops.CompareIssueContextReplay(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			replayID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		result, err := workspaceops.ListThreatModels(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		var request workspaceops.ThreatModelUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.SaveThreatModel(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
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
	mux.HandleFunc("DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models/{threat_model_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		threatModelID := r.PathValue("threat_model_id")
		err := workspaceops.DeleteThreatModel(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			threatModelID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":              true,
			"threat_model_id": threatModelID,
		})
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/coverage/parse", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		reportPath := r.URL.Query().Get("report_path")
		runID := r.URL.Query().Get("run_id")
		issueID := r.URL.Query().Get("issue_id")
		if workspaceID == "" || reportPath == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "missing workspace_id or report_path",
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		result, err := workspaceops.ParseCoverageReport(
			ctx,
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			reportPath,
			runID,
			issueID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/coverage", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.URL.Query().Get("issue_id")
		runID := r.URL.Query().Get("run_id")
		result, err := workspaceops.GetCoverage(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
			runID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "No coverage data found",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/coverage-delta", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		issueID := r.PathValue("issue_id")
		result, err := workspaceops.GetCoverageDelta(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			issueID,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": err.Error(),
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
	mux.HandleFunc("POST /api/terminal/open", func(w http.ResponseWriter, r *http.Request) {
		var request workspaceops.TerminalOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.OpenTerminal(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			request,
		)
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
	mux.HandleFunc("POST /api/terminal/{terminal_id}/write", func(w http.ResponseWriter, r *http.Request) {
		terminalID := r.PathValue("terminal_id")
		var request workspaceops.TerminalWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		if err := workspaceops.WriteTerminal(terminalID, request.Data); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Terminal not found",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/terminal/{terminal_id}/resize", func(w http.ResponseWriter, r *http.Request) {
		terminalID := r.PathValue("terminal_id")
		var request workspaceops.TerminalResizeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		if err := workspaceops.ResizeTerminal(terminalID, request.Cols, request.Rows); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Terminal not found",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/terminal/{terminal_id}/read", func(w http.ResponseWriter, r *http.Request) {
		terminalID := r.PathValue("terminal_id")
		workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
		offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		result, err := workspaceops.ReadTerminal(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			terminalID,
			offset,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("DELETE /api/terminal/{terminal_id}", func(w http.ResponseWriter, r *http.Request) {
		terminalID := r.PathValue("terminal_id")
		if err := workspaceops.CloseTerminal(terminalID); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Terminal not found",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	addr := ":" + envDefault("XMUSTARD_API_PORT", "8080")
	log.Printf("xmustard api-go listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func envDefault(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

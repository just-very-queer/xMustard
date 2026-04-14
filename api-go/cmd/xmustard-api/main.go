package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"xmustard/api-go/internal/migration"
	"xmustard/api-go/internal/rustcore"
	"xmustard/api-go/internal/workspaceops"
)

type subsystemBoundary struct {
	Name         string `json:"name"`
	CurrentOwner string `json:"current_owner"`
	TargetOwner  string `json:"target_owner"`
	Notes        string `json:"notes"`
}

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
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "draft",
			"http_shell": "go",
			"core":       "rust",
			"boundaries": []subsystemBoundary{
				{
					Name:         "scanner",
					CurrentOwner: "python",
					TargetOwner:  "rust-core",
					Notes:        "Port repo scanning, ledger ingestion, and signal detection first.",
				},
				{
					Name:         "repo_map",
					CurrentOwner: "python",
					TargetOwner:  "rust-core",
					Notes:        "Port structural summaries and path ranking next.",
				},
				{
					Name:         "verification",
					CurrentOwner: "python",
					TargetOwner:  "rust-core",
					Notes:        "Move process-heavy verification orchestration into the compiled core.",
				},
				{
					Name:         "http_api",
					CurrentOwner: "python",
					TargetOwner:  "api-go",
					Notes:        "Replace FastAPI route groups only after contract parity is tested.",
				},
			},
		})
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

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

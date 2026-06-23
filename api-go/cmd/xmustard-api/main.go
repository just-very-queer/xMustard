package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"xmustard/api-go/internal/workspaceops"
)

func main() {
	// `xmustard-api mint-token <id> [role]` mints a bearer token (local file access,
	// no server needed) — the bootstrap path for the first admin token.
	if len(os.Args) > 1 && os.Args[1] == "mint-token" {
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: xmustard-api mint-token <id> [admin|agent|readonly]")
			os.Exit(2)
		}
		role := "agent"
		if len(os.Args) > 3 {
			role = os.Args[3]
		}
		raw, err := workspaceops.MintToken(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), os.Args[2], role)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(raw)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "api-go",
		})
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
		if !requireRole(w, r, "admin") {
			return
		}
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
	dataDir := func() string { return envDefault("XMUSTARD_DATA_DIR", "../backend/data") }
	// --- auth: token admin (admin-gated) + whoami ---
	mux.HandleFunc("GET /api/auth/whoami", func(w http.ResponseWriter, r *http.Request) {
		if p := principalFromContext(r.Context()); p != nil {
			writeJSON(w, http.StatusOK, p)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "", "role": "anonymous"})
	})
	mux.HandleFunc("GET /api/auth/principals", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "admin") {
			return
		}
		writeJSON(w, http.StatusOK, workspaceops.ListPrincipals(dataDir()))
	})
	auditActor := func(r *http.Request) string {
		if p := principalFromContext(r.Context()); p != nil {
			return p.ID
		}
		return "system"
	}
	mux.HandleFunc("POST /api/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "admin") {
			return
		}
		var req struct {
			ID         string   `json:"id"`
			Role       string   `json:"role"`
			TTLSeconds int      `json:"ttl_seconds"`
			Workspaces []string `json:"workspaces"` // optional: confine the token to these workspaces
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		raw, err := workspaceops.MintScopedToken(dataDir(), req.ID, req.Role, req.TTLSeconds, req.Workspaces)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
			Action: "mint", Actor: auditActor(r), TokenID: req.ID, Role: req.Role,
			Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
		})
		writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "token": raw, "note": "store this now; it is not recoverable"})
	})
	mux.HandleFunc("POST /api/auth/tokens/{id}/rotate", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "admin") {
			return
		}
		var req struct {
			TTLSeconds int `json:"ttl_seconds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // body optional
		id := r.PathValue("id")
		raw, err := workspaceops.RotateToken(dataDir(), id, req.TTLSeconds)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
			Action: "rotate", Actor: auditActor(r), TokenID: id,
			Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
		})
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "token": raw, "note": "old token invalidated; store this now"})
	})
	mux.HandleFunc("DELETE /api/auth/tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "admin") {
			return
		}
		id := r.PathValue("id")
		if err := workspaceops.RevokeToken(dataDir(), id); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
			Action: "revoke", Actor: auditActor(r), TokenID: id,
			Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
		})
		writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	})
	mux.HandleFunc("GET /api/auth/audit", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "admin") {
			return
		}
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		writeJSON(w, http.StatusOK, workspaceops.ListAuthAudit(dataDir(), limit))
	})
	// --- OpenAI-compatible providers (Ollama / vLLM / LM Studio / OpenAI / VLM) ---
	respond := func(w http.ResponseWriter, err error, result any) {
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
	mux.HandleFunc("GET /api/providers", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ListOpenAIProviders(dataDir())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /api/providers", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "admin") {
			return
		}
		var req workspaceops.OpenAIProvider
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		result, err := workspaceops.AddOpenAIProvider(dataDir(), req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("DELETE /api/providers/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "admin") {
			return
		}
		if err := workspaceops.RemoveOpenAIProvider(dataDir(), r.PathValue("name")); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": r.PathValue("name")})
	})
	mux.HandleFunc("GET /api/providers/{name}/models", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ListProviderModels(dataDir(), r.PathValue("name"))
		respond(w, err, result)
	})
	mux.HandleFunc("POST /api/providers/{name}/probe", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ProbeOpenAIProvider(dataDir(), r.PathValue("name"))
		respond(w, err, result)
	})
	mux.HandleFunc("POST /api/providers/{name}/chat", func(w http.ResponseWriter, r *http.Request) {
		var req workspaceops.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req) // body optional; query params support the MCP bridge
		q := r.URL.Query()
		if req.Prompt == "" {
			req.Prompt = q.Get("prompt")
		}
		if req.Model == "" {
			req.Model = q.Get("model")
		}
		result, err := workspaceops.OpenAIChat(dataDir(), r.PathValue("name"), req)
		respond(w, err, result)
	})
	// --- task-typed model routing over the providers ---
	mux.HandleFunc("POST /api/route", func(w http.ResponseWriter, r *http.Request) {
		var req workspaceops.RouteRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		q := r.URL.Query()
		if req.Prompt == "" {
			req.Prompt = q.Get("prompt")
		}
		if q.Has("has_image") {
			req.HasImage = q.Get("has_image") == "true"
		}
		if req.TaskHint == "" {
			req.TaskHint = q.Get("task_hint")
		}
		result, err := workspaceops.RouteModel(dataDir(), req)
		respond(w, err, result)
	})
	mux.HandleFunc("POST /api/route/chat", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			workspaceops.RouteRequest
			ImageURLs []string `json:"image_urls"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Prompt == "" {
			body.Prompt = r.URL.Query().Get("prompt")
		}
		result, err := workspaceops.RouteAndChat(dataDir(), body.RouteRequest, body.ImageURLs)
		respond(w, err, result)
	})
	mux.HandleFunc("GET /api/routes", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ListModelRoutes(dataDir())
		respond(w, err, result)
	})
	mux.HandleFunc("POST /api/routes", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "admin") {
			return
		}
		var rule workspaceops.RoutingRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		result, err := workspaceops.SetModelRoute(dataDir(), rule)
		respond(w, err, result)
	})
	mux.HandleFunc("GET /api/postgres/plan", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.GetPostgresSchemaPlan(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
		)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "postgres schema") {
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
	mux.HandleFunc("GET /api/postgres/render", func(w http.ResponseWriter, r *http.Request) {
		sqlText, err := workspaceops.RenderPostgresSchemaSQL(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			r.URL.Query().Get("schema"),
		)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "postgres schema") {
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
		writeJSON(w, http.StatusOK, map[string]any{"sql": sqlText})
	})
	mux.HandleFunc("POST /api/postgres/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		var request workspaceops.PostgresBootstrapRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.BootstrapPostgresSchema(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			request,
		)
		if err != nil {
			lowered := strings.ToLower(err.Error())
			if strings.Contains(lowered, "required") || strings.Contains(lowered, "must ") {
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/repo-state", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadRepoToolState(
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/ingestion-plan", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadIngestionPlan(
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/run-targets", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadRunTargets(
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/verify-targets", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadVerifyTargets(
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
		if !requireRole(w, r, "agent") { // launching a run is an agent action, not readonly
			return
		}
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
			respondError(w, err) // typed: no-metrics (NotFound) / os.ErrNotExist -> 404, else 500
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
			respondError(w, err) // typed: not-found -> 404, cannot-critique (Invalid) -> 400, else 500
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/impact", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadImpact(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("base_ref"),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/diagnostics/status", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadDiagnosticsStatus(
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadDiagnostics(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("diagnostic_run_id"),
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace not found",
				})
				return
			}
			if errors.Is(err, workspaceops.ErrInvalidDiagnosticsRequest) {
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/diagnostics/live", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadLiveDiagnostics(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("path"),
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace or path not found",
				})
				return
			}
			if errors.Is(err, workspaceops.ErrInvalidSemanticRequest) {
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/diagnostics/run", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.DiagnosticsRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.RunDiagnostics(
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
			if errors.Is(err, workspaceops.ErrInvalidDiagnosticsRequest) {
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/repo-context", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadRepoContext(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("base_ref"),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/project-info", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadProjectInfo(
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/verification-outcomes", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadVerificationOutcomes(
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/retrieval-search", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		result, err := workspaceops.SearchRetrieval(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("query"),
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/path-symbols", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadPathSymbols(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("path"),
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace or path not found",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/document-symbols", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ReadDocumentSymbols(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("path"),
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace or path not found",
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/go-to-definition", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		line, _ := strconv.Atoi(r.URL.Query().Get("line"))
		column, _ := strconv.Atoi(r.URL.Query().Get("column"))
		result, err := workspaceops.GoToDefinition(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("path"),
			line,
			column,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace or path not found",
				})
				return
			}
			if errors.Is(err, workspaceops.ErrInvalidSemanticRequest) {
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/references", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		line, _ := strconv.Atoi(r.URL.Query().Get("line"))
		column, _ := strconv.Atoi(r.URL.Query().Get("column"))
		includeDeclaration := true
		if raw := strings.TrimSpace(r.URL.Query().Get("include_declaration")); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "include_declaration must be a boolean",
				})
				return
			}
			includeDeclaration = parsed
		}
		result, err := workspaceops.FindReferences(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("path"),
			line,
			column,
			includeDeclaration,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace or path not found",
				})
				return
			}
			if errors.Is(err, workspaceops.ErrInvalidSemanticRequest) {
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/workspace-symbols", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		result, err := workspaceops.ReadWorkspaceSymbols(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("query"),
			limit,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace not found",
				})
				return
			}
			if errors.Is(err, workspaceops.ErrInvalidSemanticRequest) {
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/lsp/workspace-symbols", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		result, err := workspaceops.LSPWorkspaceSymbols(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("language"),
			r.URL.Query().Get("query"),
			limit,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace not found",
				})
				return
			}
			if errors.Is(err, workspaceops.ErrInvalidSemanticRequest) {
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/path-symbols/materialize", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.PostgresPathMaterializationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.MaterializePathSymbolsToPostgres(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace or path not found",
				})
				return
			}
			if errors.Is(err, workspaceops.ErrInvalidSemanticRequest) {
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/semantic-index/materialize", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.PostgresWorkspaceSemanticMaterializationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.MaterializeWorkspaceSymbolsToPostgres(
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
			if errors.Is(err, workspaceops.ErrInvalidSemanticRequest) {
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/explain-path", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ExplainPath(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("path"),
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace or path not found",
				})
				return
			}
			// A path that escapes the workspace (or other bad input) is a 400, not a 500.
			if workspaceops.IsInvalidInput(err) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		// enrich with the file's community cluster (its functional neighbourhood),
		// so explain answers "where does this file sit in the repo" — best-effort.
		path := r.URL.Query().Get("path")
		if cluster, cerr := workspaceops.PathCluster(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), workspaceID, path); cerr == nil && cluster != nil {
			writeJSON(w, http.StatusOK, map[string]any{"explanation": result, "cluster": cluster})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/clusters", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.WorkspaceClusters(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/semantic-search", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		result, err := workspaceops.SearchSemanticPattern(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			r.URL.Query().Get("pattern"),
			r.URL.Query().Get("language"),
			r.URL.Query().Get("path_glob"),
			limit,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Workspace not found",
				})
				return
			}
			if errors.Is(err, workspaceops.ErrInvalidSemanticRequest) {
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/semantic-search/materialize", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.PostgresSemanticSearchMaterializationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.MaterializeSemanticSearchToPostgres(
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
			if errors.Is(err, workspaceops.ErrInvalidSemanticRequest) {
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
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/goals", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		result, err := workspaceops.ListGoals(
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
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/goals", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		var request workspaceops.GoalCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.CreateGoal(
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
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/goals/{goal_id}", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		goalID := r.PathValue("goal_id")
		result, err := workspaceops.GetGoal(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			goalID,
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
	mux.HandleFunc("PATCH /api/workspaces/{workspace_id}/goals/{goal_id}/status", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		goalID := r.PathValue("goal_id")
		var request workspaceops.GoalStatusUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.UpdateGoalStatus(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			goalID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/goals/{goal_id}/iterations", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		goalID := r.PathValue("goal_id")
		var request workspaceops.GoalIterationAppendRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		result, err := workspaceops.AppendGoalIteration(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			goalID,
			request,
		)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Missing resource",
				})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/goals/{goal_id}/ledger", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		goalID := r.PathValue("goal_id")
		result, err := workspaceops.ReadGoalLedger(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			goalID,
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
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(result))
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/goals/{goal_id}/context", func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspace_id")
		goalID := r.PathValue("goal_id")
		result, err := workspaceops.BuildGoalContextPacket(
			envDefault("XMUSTARD_DATA_DIR", "../backend/data"),
			workspaceID,
			goalID,
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
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(result))
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
	// --- issue intelligence: quality, duplicates, triage, test suggestions ---
	issueIntel := func(w http.ResponseWriter, err error, result any) {
		if err != nil {
			// One typed mapper for every issueIntel handler: typed DomainError -> its
			// class; os.ErrNotExist -> 404; IsInvalidInput -> 400; else 500. Same
			// fallback as before, so no behavior change for un-migrated error sources.
			respondError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
	handleIssueQuality := func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ScoreIssueQuality(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("issue_id"))
		issueIntel(w, err, result)
	}
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/quality", handleIssueQuality)
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/quality", handleIssueQuality)
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/quality/score-all", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ScoreAllIssueQuality(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/duplicates", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.FindDuplicates(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("issue_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/triage", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.TriageIssue(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("issue_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/triage/all", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.TriageAllIssues(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/test-suggestions", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.GenerateTestSuggestions(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("issue_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/test-suggestions", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ListTestSuggestions(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("issue_id"))
		issueIntel(w, err, result)
	})
	// --- workspace policy / governance ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/policy", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.GetWorkspacePolicy(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("PUT /api/workspaces/{workspace_id}/policy", func(w http.ResponseWriter, r *http.Request) {
		var policy workspaceops.WorkspacePolicy
		if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		result, err := workspaceops.SetWorkspacePolicy(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), policy)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "Workspace not found"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/policy/evaluate", func(w http.ResponseWriter, r *http.Request) {
		var input workspaceops.RunPolicyInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		result, err := workspaceops.EvaluateRunAgainstPolicy(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), input)
		issueIntel(w, err, result)
	})
	// --- audit log ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/audit-log", func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		result, err := workspaceops.ListAuditEvents(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), limit)
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/audit-log", func(w http.ResponseWriter, r *http.Request) {
		var event workspaceops.AuditEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		result, err := workspaceops.RecordAuditEvent(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), event)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "Workspace not found"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	// --- PR-style review packet ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/review-packet", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.BuildReviewPacket(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("issue_id"))
		issueIntel(w, err, result)
	})
	// --- security review depth ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/security/dispositions", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ListSecurityDispositions(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/security/findings/{finding_id}/disposition", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.GetSecurityDisposition(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("finding_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("PUT /api/workspaces/{workspace_id}/security/findings/{finding_id}/disposition", func(w http.ResponseWriter, r *http.Request) {
		var in workspaceops.SecurityDisposition
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		result, err := workspaceops.SetSecurityDisposition(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("finding_id"), in)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "Workspace not found"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/security/review-packet", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.BuildSecurityReviewPacket(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	// --- operational dashboard ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/dashboard", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.BuildWorkspaceDashboard(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	// --- change state (gitnexus-style change tracking) ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/fingerprint", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.WorkspaceFingerprint(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/index", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.IndexWorkspace(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/changes/drift", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.WorkspaceDrift(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/changes/since-index", func(w http.ResponseWriter, r *http.Request) {
		dd := envDefault("XMUSTARD_DATA_DIR", "../backend/data")
		ws := r.PathValue("workspace_id")
		q := r.URL.Query()
		// impact enriched with the precomputed symbol graph: ?symbol= → blast radius;
		// ?from=&to= → shortest dependency path; otherwise the dirty-symbols view.
		switch {
		case q.Get("from") != "" && q.Get("to") != "":
			result, err := workspaceops.TraceSymbols(dd, ws, q.Get("from"), q.Get("to"))
			issueIntel(w, err, result)
		case q.Get("symbol") != "":
			depth, _ := strconv.Atoi(q.Get("depth"))
			result, err := workspaceops.SymbolImpact(dd, ws, q.Get("symbol"), depth)
			issueIntel(w, err, result)
		default:
			result, err := workspaceops.WorkspaceChangesSinceIndex(dd, ws)
			issueIntel(w, err, result)
		}
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/changes", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.WorkspaceWorkingChanges(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	// --- semantic symbol graph (intelligence) ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/symbol-graph", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.WorkspaceSymbolGraph(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issue-symbol-edges", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.IssueSymbolEdges(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/lsp/document-symbols", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "path query param required"})
			return
		}
		result, err := workspaceops.LiveDocumentSymbols(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), path)
		issueIntel(w, err, result)
	})
	// --- context governance: propose / verify / active shared context ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/context", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ListContextEntries(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.URL.Query().Get("filter"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/context/active", func(w http.ResponseWriter, r *http.Request) {
		dd := envDefault("XMUSTARD_DATA_DIR", "../backend/data")
		q := r.URL.Query()
		// ranked recall when a query/paths signal is present; full active set otherwise.
		if q.Get("query") != "" || q.Get("paths") != "" {
			var paths []string
			if q.Get("paths") != "" {
				paths = strings.Split(q.Get("paths"), ",")
			}
			limit := 8
			if v := q.Get("limit"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					limit = n
				}
			}
			result, err := workspaceops.RecallContext(dd, r.PathValue("workspace_id"), q.Get("query"), paths, limit)
			issueIntel(w, err, result)
			return
		}
		result, err := workspaceops.GetActiveContext(dd, r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/context", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "agent") { // proposing memory is an agent action, not readonly
			return
		}
		var req workspaceops.ProposeContextRequest
		_ = json.NewDecoder(r.Body).Decode(&req) // body optional; query params are the MCP-bridge path
		q := r.URL.Query()
		if req.Content == "" {
			req.Content = q.Get("content")
		}
		if req.Title == "" {
			req.Title = q.Get("title")
		}
		if req.Source == "" {
			req.Source = q.Get("source")
		}
		if req.Permission == "" {
			req.Permission = q.Get("permission")
		}
		if len(req.Paths) == 0 && q.Get("paths") != "" {
			req.Paths = strings.Split(q.Get("paths"), ",")
		}
		// attribute to the authenticated principal; open-mode callers collapse to one
		// "anonymous" identity (so the author can't also masquerade as a verifier).
		if p := principalFromContext(r.Context()); p != nil {
			req.Source = p.ID
		} else {
			req.Source = "anonymous"
		}
		result, err := workspaceops.ProposeContext(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), req)
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/context/{entry_id}/verify", func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, "agent") { // verifying/promoting memory is an agent action
			return
		}
		var req struct {
			Agent   string `json:"agent"`
			Approve bool   `json:"approve"`
			Note    string `json:"note"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // body optional; query params are the MCP-bridge path
		q := r.URL.Query()
		if req.Agent == "" {
			req.Agent = q.Get("agent")
		}
		if q.Has("approve") {
			req.Approve = q.Get("approve") == "true" // only a literal "true" approves; anything else is a reject
		}
		if req.Note == "" {
			req.Note = q.Get("note")
		}
		// The agent identity is the AUTHENTICATED principal, never a caller-asserted
		// string. In open mode (no auth configured) all unauthenticated callers
		// collapse to a single "anonymous" identity, so N fabricated agent names
		// cannot satisfy the multi-agent gate.
		if p := principalFromContext(r.Context()); p != nil {
			req.Agent = p.ID
		} else {
			req.Agent = "anonymous"
		}
		result, err := workspaceops.VerifyContext(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("entry_id"), req.Agent, req.Approve, req.Note)
		issueIntel(w, err, result)
	})
	mux.HandleFunc("PUT /api/workspaces/{workspace_id}/context/{entry_id}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		result, err := workspaceops.UpdateContextContent(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("entry_id"), req.Content)
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/hotspots", func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		result, err := workspaceops.WorkspaceHotspots(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), limit)
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/blast-radius", func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		if symbol == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "symbol query param required"})
			return
		}
		result, err := workspaceops.SymbolBlastRadius(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), symbol)
		issueIntel(w, err, result)
	})
	// --- knowledge layer: hybrid search + wiki ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/search", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "q query param required"})
			return
		}
		limit := 25
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		dd := envDefault("XMUSTARD_DATA_DIR", "../backend/data")
		var result json.RawMessage
		var err error
		switch {
		case r.URL.Query().Get("mode") == "pattern":
			// structural AST search: the query is an ast-grep pattern (e.g. `$A && $A()`).
			pat, perr := workspaceops.SearchSemanticPattern(dd, r.PathValue("workspace_id"), query, r.URL.Query().Get("lang"), r.URL.Query().Get("path"), limit)
			if perr != nil {
				issueIntel(w, perr, nil)
				return
			}
			result, err = json.Marshal(pat)
		case r.URL.Query().Get("rerank") != "":
			// optional neural lane: ?rerank=<provider>&embed_model=<model>
			result, err = workspaceops.WorkspaceSearchReranked(dd, r.PathValue("workspace_id"), query, r.URL.Query().Get("rerank"), r.URL.Query().Get("embed_model"), limit)
		default:
			// default search fuses the agent-feedback boost and records retrieval.
			// optional ?seed=<symbol> activates the graph-proximity lane.
			result, err = workspaceops.WorkspaceSearchWithFeedback(dd, r.PathValue("workspace_id"), query, r.URL.Query().Get("seed"), limit)
		}
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/wiki", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.WorkspaceWiki(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	// --- Postgres primary store: materialize + FTS hybrid search ---
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/pg/materialize", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.MaterializePostgresIndex(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/pg/search", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "q query param required"})
			return
		}
		limit := 25
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		result, err := workspaceops.SearchPostgres(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), query, limit)
		issueIntel(w, err, result)
	})
	// --- ops layer in Postgres: runs / activity / issues ---
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/pg/ops/materialize", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.MaterializeOpsPostgres(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/pg/runs", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		result, err := workspaceops.ListRunsPostgres(r.PathValue("workspace_id"), r.URL.Query().Get("status"), limit)
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/pg/run-plans", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		result, err := workspaceops.ListRunPlansPostgres(r.PathValue("workspace_id"), limit)
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/pg/issues/search", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "q query param required"})
			return
		}
		limit := 25
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		result, err := workspaceops.SearchIssuesPostgres(r.PathValue("workspace_id"), query, limit)
		issueIntel(w, err, result)
	})
	// --- verification outcomes: materialize to Postgres, then query/filter ---
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/pg/verifications/materialize", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.MaterializeVerificationsPostgres(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/pg/verifications", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		result, err := workspaceops.ListVerificationsPostgres(r.PathValue("workspace_id"), r.URL.Query().Get("status"), limit)
		issueIntel(w, err, result)
	})
	// --- ownership, incorporation lineage, session grounding ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/subsystems", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.WorkspaceSubsystems(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/owners", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "path query param required"})
			return
		}
		result, err := workspaceops.FileOwners(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), path)
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/incorporate", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.RecordIncorporation(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/lineage", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "path query param required"})
			return
		}
		result, err := workspaceops.FileLineage(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), path)
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/session-grounding", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.BuildSessionGrounding(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	// --- run confidence, owner suggestions, ownership, eval timeline, ticket ingest, guidance customization ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs/{run_id}/confidence", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ScoreRunConfidence(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("run_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs/{run_id}/why-failed", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ExplainRunFailure(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("run_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/owner-suggestions", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.SuggestIssueOwners(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("issue_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/ownership-history", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.BuildOwnershipHistory(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("issue_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/eval-timeline", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.BuildEvalTimeline(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/issues/{issue_id}/ingested-ticket", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.GetIngestedTicket(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("issue_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/issues/{issue_id}/ingest-ticket", func(w http.ResponseWriter, r *http.Request) {
		var req workspaceops.IngestTicketRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		result, err := workspaceops.IngestTicket(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("issue_id"), req)
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/guidance/customize", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Kind string `json:"kind"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		result, err := workspaceops.BuildGuidanceCustomization(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), body.Kind)
		issueIntel(w, err, result)
	})
	// --- per-run brief export ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/runs/{run_id}/brief", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.BuildRunBrief(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("run_id"))
		issueIntel(w, err, result)
	})
	// --- persistent agent identity registry ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/agents", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.ListAgentIdentities(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/agents/{agent_id}", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.GetAgentIdentity(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), r.PathValue("agent_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/workspaces/{workspace_id}/agents/sync", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.SyncAgentIdentitiesFromRuns(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	// --- explicit security acceptance criteria ---
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/security/acceptance-criteria", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.GetSecurityAcceptanceCriteria(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("PUT /api/workspaces/{workspace_id}/security/acceptance-criteria", func(w http.ResponseWriter, r *http.Request) {
		var in workspaceops.SecurityAcceptanceCriteria
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
		result, err := workspaceops.SetSecurityAcceptanceCriteria(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"), in)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "Workspace not found"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/workspaces/{workspace_id}/security/acceptance-evaluation", func(w http.ResponseWriter, r *http.Request) {
		result, err := workspaceops.EvaluateSecurityAcceptance(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), r.PathValue("workspace_id"))
		issueIntel(w, err, result)
	})
	mux.HandleFunc("POST /api/terminal/open", func(w http.ResponseWriter, r *http.Request) {
		var request workspaceops.TerminalOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid JSON body",
			})
			return
		}
		if !requireTerminalScope(w, r, dataDir(), request.WorkspaceID) {
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
		if !requireTerminalScope(w, r, dataDir(), request.WorkspaceID) {
			return
		}
		if err := workspaceops.WriteTerminal(request.WorkspaceID, terminalID, request.Data); err != nil {
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
		if !requireTerminalScope(w, r, dataDir(), request.WorkspaceID) {
			return
		}
		if err := workspaceops.ResizeTerminal(request.WorkspaceID, terminalID, request.Cols, request.Rows); err != nil {
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
		if !requireTerminalScope(w, r, dataDir(), workspaceID) {
			return
		}
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
		workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
		if !requireTerminalScope(w, r, dataDir(), workspaceID) {
			return
		}
		if err := workspaceops.CloseTerminal(workspaceID, terminalID); err != nil {
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

	// Bind to loopback by default. The API makes server-side requests (providers),
	// so it should not be exposed on all interfaces unless the operator opts in via
	// XMUSTARD_API_HOST=0.0.0.0 — and if they do, auth tokens must be configured.
	host := envDefault("XMUSTARD_API_HOST", "127.0.0.1")
	authMode := strings.ToLower(envDefault("XMUSTARD_AUTH", "auto"))
	isLoopback := host == "127.0.0.1" || host == "localhost" || host == "::1"
	tlsCert, tlsKey := os.Getenv("XMUSTARD_API_TLS_CERT"), os.Getenv("XMUSTARD_API_TLS_KEY")
	hasTLS := tlsCert != "" && tlsKey != ""
	allowInsecureBind := os.Getenv("XMUSTARD_ALLOW_INSECURE_BIND") == "1"
	// Fail-closed on a non-loopback bind: requires auth AND transport security.
	// XMUSTARD_AUTH=off does not satisfy this interlock; disabling auth and exposing
	// the interface are separate opt-ins.
	if !isLoopback {
		if authMode != "required" && !workspaceops.HasAuthConfigured(dataDir()) {
			log.Fatal("refusing non-loopback bind without auth; run `xmustard-api mint-token <id> admin` or set XMUSTARD_AUTH=required")
		}
		if !hasTLS && !allowInsecureBind {
			log.Fatal("refusing non-loopback bind without TLS; set XMUSTARD_API_TLS_CERT/KEY, or XMUSTARD_ALLOW_INSECURE_BIND=1 if TLS is terminated by a front proxy")
		}
	}
	var handler http.Handler = mux
	if os.Getenv("XMUSTARD_CORE_ONLY") == "1" {
		// Lean production surface: expose only the governed-memory + grounding +
		// search core (the paths the 9 MCP tools + auth use), 404 everything else.
		// The full platform surface stays available when this is unset (for the UI).
		handler = coreOnlyMiddleware(handler)
		log.Printf("surface: CORE_ONLY — platform routes disabled")
	}
	if authMode != "off" {
		handler = authMiddleware(dataDir(), authMode, handler)
		if authMode == "required" || workspaceops.HasAuthConfigured(dataDir()) {
			log.Printf("auth: ENFORCED (mode=%s, bearer token required)", authMode)
		} else {
			log.Printf("auth: open — no tokens configured (mode=%s); unauthenticated callers collapse to one identity. Mint a token to enforce.", authMode)
		}
	} else {
		log.Printf("auth: DISABLED (XMUSTARD_AUTH=off)")
	}
	// Cap every request body so a hostile/buggy client can't drive unbounded memory
	// by POSTing a huge payload — the HTTP analogue of the MCP stdio framing cap
	// (readBoundedLine). Outermost wrap so it applies before any handler reads the body.
	handler = bodyLimitMiddleware(handler)
	// Default to 8042 to match the MCP bridge's API target (XM-NEW-001) and
	// AGENTS.md ("an HTTP shell on :8042"); override with XMUSTARD_API_PORT.
	addr := host + ":" + envDefault("XMUSTARD_API_PORT", "8042")
	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
		// Bound slow/oversized clients so a few connections can't pin goroutines/FDs
		// (XM-NEW-019). Read/Write are generous because agent runs can be long, but
		// header + idle timeouts defeat slowloris and leaked keep-alives.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      300 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	// Graceful shutdown on SIGINT/SIGTERM so a restart doesn't orphan in-flight work.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		workspaceops.ClosePgPool()
	}()
	log.Printf("xmustard api-go listening on %s (tls=%v)", addr, hasTLS)
	var serveErr error
	if hasTLS {
		serveErr = srv.ListenAndServeTLS(tlsCert, tlsKey)
	} else {
		serveErr = srv.ListenAndServe()
	}
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatal(serveErr)
	}
}

// --- auth middleware + principal helpers ---

type ctxKey string

const principalCtxKey ctxKey = "principal"

func principalFromContext(ctx context.Context) *workspaceops.Principal {
	p, _ := ctx.Value(principalCtxKey).(*workspaceops.Principal)
	return p
}

// roleRank orders the role hierarchy: admin > agent > readonly. Used so a gate for
// "agent" is satisfied by admin too, and "readonly" by everyone authenticated.
func roleRank(role string) int {
	switch role {
	case "admin":
		return 3
	case "agent":
		return 2
	case "readonly":
		return 1
	default:
		return 0
	}
}

// requireRole enforces a minimum role for an endpoint (admin > agent > readonly).
// In open mode (no auth configured) it allows the operation locally; once auth is
// configured the middleware has already rejected unauthenticated requests. A denied
// authenticated request is recorded in the auth-audit log.
func requireRole(w http.ResponseWriter, r *http.Request, role string) bool {
	dd := envDefault("XMUSTARD_DATA_DIR", "../backend/data")
	p := principalFromContext(r.Context())
	if p == nil {
		if !workspaceops.HasAuthConfigured(dd) {
			return true
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "authentication required"})
		return false
	}
	if roleRank(p.Role) < roleRank(role) {
		workspaceops.RecordAuthAudit(dd, workspaceops.AuthAuditEvent{
			Action:     "denied",
			Actor:      p.ID,
			Detail:     role + " role required (have " + p.Role + ")",
			Method:     r.Method,
			Path:       r.URL.Path,
			RemoteAddr: r.RemoteAddr,
		})
		writeJSON(w, http.StatusForbidden, map[string]any{"error": role + " role required"})
		return false
	}
	return true
}

// coreWorkspaceSubpaths are the EXACT per-workspace subpaths the 9 MCP tools hit
// (the part after /api/workspaces/{id}/). Matching these exactly — rather than by
// substring — is what makes CORE_ONLY a real allowlist: a substring gate over
// "/context"/"/search"/"/diagnostics" would also admit /context-replays,
// /goals/{id}/context, /pg/search, /diagnostics/run, etc. (XM-PRO-011).
var coreWorkspaceSubpaths = map[string]bool{
	"session-grounding":   true, // ground
	"context":             true, // remember (POST)
	"context/active":      true, // recall
	"search":              true, // search
	"explain-path":        true, // explain
	"changes/since-index": true, // impact
	"diagnostics":         true, // diagnostics
}

// isCorePath reports whether p is one of the exact routes the lean 9-tool agent
// surface uses. CORE_ONLY 404s everything else, so the deployed attack surface
// matches the product claim, not a substring gate over ~200 handlers.
func isCorePath(p string) bool {
	if p == "/api/health" || p == "/api/workspaces" || strings.HasPrefix(p, "/api/auth/") {
		return true
	}
	const wsPrefix = "/api/workspaces/"
	if !strings.HasPrefix(p, wsPrefix) {
		return false
	}
	rest := p[len(wsPrefix):]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return false // /api/workspaces/{id} with no tool subpath
	}
	sub := rest[slash+1:] // subpath after the workspace id
	if coreWorkspaceSubpaths[sub] {
		return true
	}
	// the two parameterized core routes: context/{entry_id}/verify, runs/{run_id}/why-failed
	if seg := strings.Split(sub, "/"); len(seg) == 3 {
		if seg[0] == "context" && seg[2] == "verify" {
			return true // verify
		}
		if seg[0] == "runs" && seg[2] == "why-failed" {
			return true // why_failed
		}
	}
	return false
}

// maxRequestBodyBytes bounds any single HTTP request body. ReadTimeout +
// MaxHeaderBytes bound time and headers only; without this a single multi-GB POST
// (e.g. to /context or /settings) is decoded into memory unbounded — the same OOM
// class the MCP framing path explicitly caps. Generous enough for real diagnostics/
// eval payloads; a body past the cap is cut off and the handler's decode surfaces a
// clean error (the malformed-body -> 400 path) instead of the server allocating.
const maxRequestBodyBytes = 32 << 20 // 32 MiB

// maxRequestBodyBytesConfigured returns the per-request body cap, operator-tunable
// via XMUSTARD_MAX_BODY_BYTES (so a tight-RSS deployment can lower it without a
// rebuild). Defaults to maxRequestBodyBytes.
func maxRequestBodyBytesConfigured() int64 {
	if v := strings.TrimSpace(os.Getenv("XMUSTARD_MAX_BODY_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return maxRequestBodyBytes
}

// bodyInFlight bounds the number of body-bearing requests decoded concurrently, so
// the aggregate in-flight body memory is at most cap × maxInFlight rather than
// cap × (unbounded concurrent agents) — a single 32 MiB cap alone cannot hold the
// 50–100 MB RSS target under fan-in (XM-PRO-008). Tunable via XMUSTARD_MAX_INFLIGHT_BODIES.
// (Per-endpoint limits + decoded-object amplification remain follow-on work.)
var bodyInFlight = make(chan struct{}, inFlightBodyLimit())

func inFlightBodyLimit() int {
	if v := strings.TrimSpace(os.Getenv("XMUSTARD_MAX_INFLIGHT_BODIES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 12
}

// bodyBudgetThreshold: only requests declaring (or hiding, via chunked encoding) a
// body larger than this acquire an in-flight slot. Normal small requests — tool
// calls, memory proposals, run starts — are never throttled, and a long-running
// request with a tiny body never holds a slot. This targets the actual memory risk
// (large concurrent uploads) without affecting throughput.
const bodyBudgetThreshold = 1 << 20 // 1 MiB

func bodyLimitMiddleware(next http.Handler) http.Handler {
	limit := maxRequestBodyBytesConfigured()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if r.ContentLength > bodyBudgetThreshold || r.ContentLength < 0 {
			select {
			case bodyInFlight <- struct{}{}:
				defer func() { <-bodyInFlight }()
			case <-r.Context().Done():
				http.Error(w, "request cancelled while waiting for in-flight body budget", 499)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func coreOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isCorePath(r.URL.Path) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "endpoint disabled in CORE_ONLY mode"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authMiddleware(dataDir, mode string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" { // health stays public for liveness probes
			next.ServeHTTP(w, r)
			return
		}
		token := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		}
		// single read of the token store yields both the principal and whether auth
		// is configured (was two reads per request).
		principal, configured := workspaceops.ResolveAuth(dataDir, token)
		enforce := mode == "required" || configured
		if enforce && principal == nil {
			workspaceops.RecordAuthAudit(dataDir, workspaceops.AuthAuditEvent{
				Action:     "denied",
				Actor:      "anonymous",
				Detail:     "missing/invalid/expired bearer token",
				Method:     r.Method,
				Path:       r.URL.Path,
				RemoteAddr: r.RemoteAddr,
			})
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "authentication required: provide Authorization: Bearer <token>"})
			return
		}
		// readonly principals may only read.
		if principal != nil && principal.Role == "readonly" && r.Method != http.MethodGet {
			workspaceops.RecordAuthAudit(dataDir, workspaceops.AuthAuditEvent{
				Action:     "denied",
				Actor:      principal.ID,
				Detail:     "readonly principal cannot " + r.Method,
				Method:     r.Method,
				Path:       r.URL.Path,
				RemoteAddr: r.RemoteAddr,
			})
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "readonly principal cannot " + r.Method})
			return
		}
		// workspace scope: a scoped (per-worker) token may only touch its workspaces.
		// Unscoped tokens (the default) are unrestricted, so this is backward-compatible.
		if principal != nil {
			if wsID := workspaceIDFromPath(r.URL.Path); wsID != "" && !principal.AllowsWorkspace(wsID) {
				workspaceops.RecordAuthAudit(dataDir, workspaceops.AuthAuditEvent{
					Action: "denied", Actor: principal.ID,
					Detail: "workspace " + wsID + " not in token scope",
					Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
				})
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "token not scoped to workspace " + wsID})
				return
			}
		}
		if principal == nil {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalCtxKey, principal)))
	})
}

// requireTerminalScope enforces that the authenticated principal may touch
// workspaceID. Terminal routes live under /api/terminal (outside /api/workspaces/),
// so the path-based middleware scope check (workspaceIDFromPath) never fires for
// them and a workspace-scoped token could otherwise open/read/write another
// workspace's shell by passing its workspace_id in the body/query (XM-PRO-001).
// Writes 400 on an empty id, 403 on an out-of-scope token; returns false if handled.
func requireTerminalScope(w http.ResponseWriter, r *http.Request, dataDir, workspaceID string) bool {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "workspace_id is required"})
		return false
	}
	if p := principalFromContext(r.Context()); p != nil && !p.AllowsWorkspace(workspaceID) {
		workspaceops.RecordAuthAudit(dataDir, workspaceops.AuthAuditEvent{
			Action: "denied", Actor: p.ID,
			Detail: "workspace " + workspaceID + " not in token scope (terminal)",
			Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
		})
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "token not scoped to workspace " + workspaceID})
		return false
	}
	return true
}

// httpStatusForClass maps a typed domain-error class to an HTTP status. Status now
// follows the error TYPE, not its English wording (XM-PRO-013).
func httpStatusForClass(c workspaceops.ErrorClass) int {
	switch c {
	case workspaceops.ClassInvalidInput:
		return http.StatusBadRequest
	case workspaceops.ClassNotFound:
		return http.StatusNotFound
	case workspaceops.ClassConflict:
		return http.StatusConflict
	case workspaceops.ClassUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// respondError is the single error->HTTP mapper the per-handler substring classifiers
// converge on. A typed DomainError uses its class (and for the internal class the
// wrapped detail stays server-side); os.ErrNotExist -> 404; IsInvalidInput -> 400;
// anything else -> 500 with the raw message (the legacy fallback still being migrated).
func respondError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if de, ok := workspaceops.AsDomainError(err); ok {
		if de.Class == workspaceops.ClassInternal {
			log.Printf("internal error: %v", err) // detail logged, not disclosed
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
			return
		}
		writeJSON(w, httpStatusForClass(de.Class), map[string]any{"error": de.Public})
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Missing resource"})
		return
	}
	if workspaceops.IsInvalidInput(err) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
}

// workspaceIDFromPath extracts {id} from /api/workspaces/{id}/... ("" if not such a path).
func workspaceIDFromPath(p string) string {
	const prefix = "/api/workspaces/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	rest := p[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
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

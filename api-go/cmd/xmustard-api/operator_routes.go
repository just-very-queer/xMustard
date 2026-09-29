package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"xmustard/api-go/internal/workspaceops"
)

// Operator configuration (platform): agent runtimes, settings, OpenAI-compatible
// providers (Ollama, vLLM, LM Studio, OpenAI, VLMs) and task-typed model routing over
// them. Changing settings, providers or routing rules needs admin.
var operatorRoutes = []routeSpec{
	{"GET /api/runtimes", dirCall(workspaceops.DetectRuntimes, withStatus(http.StatusInternalServerError))},
	{"GET /api/settings", dirCall(workspaceops.GetSettings, withStatus(http.StatusInternalServerError))},
	{"POST /api/settings", updateSettings},
	{"GET /api/agent/capabilities", dirCall(workspaceops.GetLocalAgentCapabilities, withStatus(http.StatusInternalServerError))},
	{"GET /api/providers", dirCall(workspaceops.ListOpenAIProviders, withStatus(http.StatusInternalServerError))},
	{"POST /api/providers", addProvider},
	{"DELETE /api/providers/{name}", removeProvider},
	{"GET /api/providers/{name}/models", listProviderModels},
	{"POST /api/providers/{name}/probe", probeProvider},
	{"POST /api/providers/{name}/chat", chatWithProvider},
	{"POST /api/route", routeModel},
	{"POST /api/route/chat", routeAndChat},
	{"GET /api/routes", dirCall(workspaceops.ListModelRoutes, notFoundAsIs)},
	{"POST /api/routes", setModelRoute},
}

func updateSettings(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	var request workspaceops.AppSettings
	if !decodeJSONBody(w, r, &request) {
		return
	}
	result, err := workspaceops.UpdateSettings(dataDir(), request)
	if errors.Is(err, workspaceops.ErrInvalidInput) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	reply(w, result, err, withStatus(http.StatusInternalServerError))
}

func addProvider(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	var req workspaceops.OpenAIProvider
	if !decodeJSONBody(w, r, &req) {
		return
	}
	result, err := workspaceops.AddOpenAIProvider(dataDir(), req)
	reply(w, result, err, withStatus(http.StatusBadRequest))
}

func removeProvider(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	name := r.PathValue("name")
	err := workspaceops.RemoveOpenAIProvider(dataDir(), name)
	reply(w, map[string]any{"removed": name}, err, notFoundAsIs)
}

func listProviderModels(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ListProviderModels(dataDir(), r.PathValue("name"))
	reply(w, result, err, notFoundAsIs)
}

func probeProvider(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ProbeOpenAIProvider(dataDir(), r.PathValue("name"))
	reply(w, result, err, notFoundAsIs)
}

func chatWithProvider(w http.ResponseWriter, r *http.Request) {
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
	reply(w, result, err, notFoundAsIs)
}

func routeModel(w http.ResponseWriter, r *http.Request) {
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
	reply(w, result, err, notFoundAsIs)
}

func routeAndChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		workspaceops.RouteRequest
		ImageURLs []string `json:"image_urls"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Prompt == "" {
		body.Prompt = r.URL.Query().Get("prompt")
	}
	result, err := workspaceops.RouteAndChat(dataDir(), body.RouteRequest, body.ImageURLs)
	reply(w, result, err, notFoundAsIs)
}

func setModelRoute(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "admin") {
		return
	}
	var rule workspaceops.RoutingRule
	if !decodeJSONBody(w, r, &rule) {
		return
	}
	result, err := workspaceops.SetModelRoute(dataDir(), rule)
	reply(w, result, err, notFoundAsIs)
}

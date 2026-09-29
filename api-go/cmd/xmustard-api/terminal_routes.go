package main

import (
	"net/http"
	"strconv"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// Terminals (platform, admin): a remote shell per workspace. These routes live
// outside /api/workspaces/{id}, so the auth middleware's path scope check never sees
// the workspace: every handler runs requireTerminalScope on the workspace it names
// before it touches a terminal.
var terminalRoutes = []routeSpec{
	{"POST /api/terminal/open", openTerminal},
	{"POST /api/terminal/{terminal_id}/write", writeTerminal},
	{"POST /api/terminal/{terminal_id}/resize", resizeTerminal},
	{"GET /api/terminal/{terminal_id}/read", readTerminal},
	{"DELETE /api/terminal/{terminal_id}", closeTerminal},
}

var notFoundTerminal = notFound("Terminal not found")

func openTerminal(w http.ResponseWriter, r *http.Request) {
	var request workspaceops.TerminalOpenRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	if !requireTerminalScope(w, r, dataDir(), request.WorkspaceID) {
		return
	}
	result, err := workspaceops.OpenTerminal(dataDir(), request)
	reply(w, result, err, notFoundWorkspace)
}

func writeTerminal(w http.ResponseWriter, r *http.Request) {
	var request workspaceops.TerminalWriteRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	if !requireTerminalScope(w, r, dataDir(), request.WorkspaceID) {
		return
	}
	err := workspaceops.WriteTerminal(request.WorkspaceID, r.PathValue("terminal_id"), request.Data)
	reply(w, map[string]any{"ok": true}, err, notFoundTerminal)
}

func resizeTerminal(w http.ResponseWriter, r *http.Request) {
	var request workspaceops.TerminalResizeRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	if !requireTerminalScope(w, r, dataDir(), request.WorkspaceID) {
		return
	}
	err := workspaceops.ResizeTerminal(request.WorkspaceID, r.PathValue("terminal_id"), request.Cols, request.Rows)
	reply(w, map[string]any{"ok": true}, err, notFoundTerminal)
}

func readTerminal(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	if !requireTerminalScope(w, r, dataDir(), workspaceID) {
		return
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	result, err := workspaceops.ReadTerminal(dataDir(), workspaceID, r.PathValue("terminal_id"), offset)
	reply(w, result, err, withStatus(http.StatusInternalServerError))
}

func closeTerminal(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	if !requireTerminalScope(w, r, dataDir(), workspaceID) {
		return
	}
	err := workspaceops.CloseTerminal(workspaceID, r.PathValue("terminal_id"))
	reply(w, map[string]any{"ok": true}, err, notFoundTerminal)
}

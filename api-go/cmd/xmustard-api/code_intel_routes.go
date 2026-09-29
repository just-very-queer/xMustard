package main

import (
	"net/http"
	"strconv"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// Code intelligence (platform): impact against a base ref, retrieval, path and
// document symbols, definitions and references, workspace and LSP symbols, semantic
// pattern search and its Postgres materializations, the repo map, clusters, the
// symbol graph and its derived views (changes, hotspots, blast radius, subsystems,
// owners, lineage, wiki). The nine tools' own routes are in tool_routes.go.
var codeIntelRoutes = []routeSpec{
	{"GET /api/workspaces/{workspace_id}/impact", queryCall("base_ref", workspaceops.ReadImpact, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/retrieval-search", searchRetrieval},
	{"GET /api/workspaces/{workspace_id}/path-symbols", queryCall("path", workspaceops.ReadPathSymbols, notFound("Workspace or path not found"))},
	{"GET /api/workspaces/{workspace_id}/document-symbols", queryCall("path", workspaceops.ReadDocumentSymbols, notFound("Workspace or path not found"))},
	{"GET /api/workspaces/{workspace_id}/go-to-definition", goToDefinition},
	{"GET /api/workspaces/{workspace_id}/references", findReferences},
	{"GET /api/workspaces/{workspace_id}/workspace-symbols", readWorkspaceSymbols},
	{"GET /api/workspaces/{workspace_id}/lsp/workspace-symbols", lspWorkspaceSymbols},
	{"GET /api/workspaces/{workspace_id}/lsp/document-symbols", requiredQueryCall("path", workspaceops.LiveDocumentSymbols, respondError)},
	{"POST /api/workspaces/{workspace_id}/path-symbols/materialize", wsBodyCall(workspaceops.MaterializePathSymbolsToPostgres, semanticPathFailure)},
	{"POST /api/workspaces/{workspace_id}/semantic-index/materialize", wsBodyCall(workspaceops.MaterializeWorkspaceSymbolsToPostgres, semanticWorkspaceFailure)},
	{"GET /api/workspaces/{workspace_id}/semantic-search", searchSemanticPattern},
	{"POST /api/workspaces/{workspace_id}/semantic-search/materialize", wsBodyCall(workspaceops.MaterializeSemanticSearchToPostgres, semanticWorkspaceFailure)},
	{"GET /api/workspaces/{workspace_id}/repo-map", wsCall(workspaceops.ReadWorkspaceRepoMap, notFoundWorkspace)},
	{"GET /api/workspaces/{workspace_id}/clusters", workspaceClusters},
	{"GET /api/workspaces/{workspace_id}/symbol-graph", wsCall(workspaceops.WorkspaceSymbolGraph, respondError)},
	{"GET /api/workspaces/{workspace_id}/issue-symbol-edges", wsCall(workspaceops.IssueSymbolEdges, respondError)},
	{"GET /api/workspaces/{workspace_id}/changes", wsCall(workspaceops.WorkspaceWorkingChanges, respondError)},
	{"GET /api/workspaces/{workspace_id}/changes/drift", wsCall(workspaceops.WorkspaceDrift, respondError)},
	{"GET /api/workspaces/{workspace_id}/hotspots", workspaceHotspots},
	{"GET /api/workspaces/{workspace_id}/blast-radius", requiredQueryCall("symbol", workspaceops.SymbolBlastRadius, respondError)},
	{"GET /api/workspaces/{workspace_id}/subsystems", wsCall(workspaceops.WorkspaceSubsystems, respondError)},
	{"GET /api/workspaces/{workspace_id}/owners", requiredQueryCall("path", workspaceops.FileOwners, respondError)},
	{"GET /api/workspaces/{workspace_id}/lineage", requiredQueryCall("path", workspaceops.FileLineage, respondError)},
	{"POST /api/workspaces/{workspace_id}/incorporate", wsCall(workspaceops.RecordIncorporation, respondError)},
	{"GET /api/workspaces/{workspace_id}/wiki", wsCall(workspaceops.WorkspaceWiki, respondError)},
}

// The semantic operations' answers: 404 for a missing workspace (or path), 400 for
// workspaceops.ErrInvalidSemanticRequest, 500 otherwise.
var (
	semanticPathFailure      = notFound("Workspace or path not found", workspaceops.ErrInvalidSemanticRequest)
	semanticWorkspaceFailure = notFound("Workspace not found", workspaceops.ErrInvalidSemanticRequest)
)

func searchRetrieval(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	result, err := workspaceops.SearchRetrieval(dataDir(), r.PathValue("workspace_id"), q.Get("query"), limit)
	reply(w, result, err, notFoundWorkspace)
}

func goToDefinition(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	line, _ := strconv.Atoi(q.Get("line"))
	column, _ := strconv.Atoi(q.Get("column"))
	result, err := workspaceops.GoToDefinition(dataDir(), r.PathValue("workspace_id"), q.Get("path"), line, column)
	reply(w, result, err, semanticPathFailure)
}

func findReferences(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	line, _ := strconv.Atoi(q.Get("line"))
	column, _ := strconv.Atoi(q.Get("column"))
	includeDeclaration := true
	if raw := strings.TrimSpace(q.Get("include_declaration")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "include_declaration must be a boolean"})
			return
		}
		includeDeclaration = parsed
	}
	result, err := workspaceops.FindReferences(dataDir(), r.PathValue("workspace_id"), q.Get("path"), line, column, includeDeclaration)
	reply(w, result, err, semanticPathFailure)
}

func readWorkspaceSymbols(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	result, err := workspaceops.ReadWorkspaceSymbols(dataDir(), r.PathValue("workspace_id"), q.Get("query"), limit)
	reply(w, result, err, semanticWorkspaceFailure)
}

func lspWorkspaceSymbols(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	result, err := workspaceops.LSPWorkspaceSymbols(dataDir(), r.PathValue("workspace_id"), q.Get("language"), q.Get("query"), limit)
	reply(w, result, err, semanticWorkspaceFailure)
}

func searchSemanticPattern(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	result, err := workspaceops.SearchSemanticPattern(dataDir(), r.PathValue("workspace_id"), q.Get("pattern"), q.Get("language"), q.Get("path_glob"), limit)
	reply(w, result, err, semanticWorkspaceFailure)
}

func workspaceClusters(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.WorkspaceClustersCtx(r.Context(), dataDir(), r.PathValue("workspace_id"))
	reply(w, result, err, withStatus(http.StatusInternalServerError))
}

func workspaceHotspots(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.WorkspaceHotspots(dataDir(), r.PathValue("workspace_id"), queryInt(r, "limit", 20))
	reply(w, result, err, respondError)
}

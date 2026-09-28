package main

import (
	"net/http"

	"xmustard/api-go/internal/workspaceops"
)

// The optional Postgres store (platform): schema plan, rendering and bootstrap, the
// index, ops and verification materializations and the queries over them.
var postgresRoutes = []routeSpec{
	{"GET /api/postgres/plan", dirCall(workspaceops.GetPostgresSchemaPlan, respondError)},
	{"GET /api/postgres/render", renderPostgresSchema},
	{"POST /api/postgres/bootstrap", bootstrapPostgresSchema},
	{"POST /api/workspaces/{workspace_id}/pg/materialize", wsCall(workspaceops.MaterializePostgresIndex, respondError)},
	{"GET /api/workspaces/{workspace_id}/pg/search", searchPostgres},
	{"POST /api/workspaces/{workspace_id}/pg/ops/materialize", wsCall(workspaceops.MaterializeOpsPostgres, respondError)},
	{"GET /api/workspaces/{workspace_id}/pg/runs", listRunsPostgres},
	{"GET /api/workspaces/{workspace_id}/pg/run-plans", listRunPlansPostgres},
	{"GET /api/workspaces/{workspace_id}/pg/issues/search", searchIssuesPostgres},
	{"POST /api/workspaces/{workspace_id}/pg/verifications/materialize", wsCall(workspaceops.MaterializeVerificationsPostgres, respondError)},
	{"GET /api/workspaces/{workspace_id}/pg/verifications", listVerificationsPostgres},
}

// bootstrapFailure: the bootstrap reports bad input only in its error text.
var bootstrapFailure = invalidOnPhrase(withStatus(http.StatusInternalServerError), "required", "must ")

func renderPostgresSchema(w http.ResponseWriter, r *http.Request) {
	sqlText, err := workspaceops.RenderPostgresSchemaSQL(dataDir(), r.URL.Query().Get("schema"))
	reply(w, map[string]any{"sql": sqlText}, err, respondError)
}

func bootstrapPostgresSchema(w http.ResponseWriter, r *http.Request) {
	var request workspaceops.PostgresBootstrapRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	result, err := workspaceops.BootstrapPostgresSchema(dataDir(), request)
	reply(w, result, err, bootstrapFailure)
}

func searchPostgres(w http.ResponseWriter, r *http.Request) {
	query, ok := requireQuery(w, r, "q")
	if !ok {
		return
	}
	result, err := workspaceops.SearchPostgres(dataDir(), r.PathValue("workspace_id"), query, queryInt(r, "limit", 25))
	reply(w, result, err, respondError)
}

func listRunsPostgres(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ListRunsPostgres(r.PathValue("workspace_id"), r.URL.Query().Get("status"), queryInt(r, "limit", 50))
	reply(w, result, err, respondError)
}

func listRunPlansPostgres(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ListRunPlansPostgres(r.PathValue("workspace_id"), queryInt(r, "limit", 50))
	reply(w, result, err, respondError)
}

func searchIssuesPostgres(w http.ResponseWriter, r *http.Request) {
	query, ok := requireQuery(w, r, "q")
	if !ok {
		return
	}
	result, err := workspaceops.SearchIssuesPostgres(r.PathValue("workspace_id"), query, queryInt(r, "limit", 25))
	reply(w, result, err, respondError)
}

func listVerificationsPostgres(w http.ResponseWriter, r *http.Request) {
	result, err := workspaceops.ListVerificationsPostgres(r.PathValue("workspace_id"), r.URL.Query().Get("status"), queryInt(r, "limit", 50))
	reply(w, result, err, respondError)
}

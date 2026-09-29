package main

import (
	"net/http"

	"xmustard/api-go/internal/workspaceops"
)

// Goals (platform): the /goal runtime's records, status, iterations, and its ledger
// (Markdown) and context packet (plain text).
var goalRoutes = []routeSpec{
	{"GET /api/workspaces/{workspace_id}/goals", wsCall(workspaceops.ListGoals, notFoundWorkspace)},
	{"POST /api/workspaces/{workspace_id}/goals", wsBodyCall(workspaceops.CreateGoal, notFoundOrInvalid("Workspace not found"))},
	{"GET /api/workspaces/{workspace_id}/goals/{goal_id}", itemCall("goal_id", workspaceops.GetGoal, notFoundResource)},
	{"PATCH /api/workspaces/{workspace_id}/goals/{goal_id}/status", itemBodyCall("goal_id", workspaceops.UpdateGoalStatus, notFoundOrInvalid("Missing resource"))},
	{"POST /api/workspaces/{workspace_id}/goals/{goal_id}/iterations", itemBodyCall("goal_id", workspaceops.AppendGoalIteration, notFoundOrInvalid("Missing resource"))},
	{"GET /api/workspaces/{workspace_id}/goals/{goal_id}/ledger", goalText(workspaceops.ReadGoalLedger, "text/markdown; charset=utf-8")},
	{"GET /api/workspaces/{workspace_id}/goals/{goal_id}/context", goalText(workspaceops.BuildGoalContextPacket, "text/plain; charset=utf-8")},
}

// goalText answers one of a goal's text documents with its content type.
func goalText(op func(dataDir, workspaceID, goalID string) (string, error), contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		text, err := op(dataDir(), r.PathValue("workspace_id"), r.PathValue("goal_id"))
		if err != nil {
			notFoundResource(w, err)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(text))
	}
}

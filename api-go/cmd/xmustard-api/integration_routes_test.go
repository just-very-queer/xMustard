package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

func TestIntegrationRouteSpecsMatchFrontendContract(t *testing.T) {
	want := []integrationRouteSpec{
		configureIntegrationRoute,
		listIntegrationConfigsRoute,
		testIntegrationRoute,
		importGitHubIssuesRoute,
		createGitHubPRRoute,
		sendSlackNotificationRoute,
		syncIssueToLinearRoute,
		syncIssueToJiraRoute,
	}
	if got := listIntegrationRouteSpecs(); !slices.Equal(got, want) {
		t.Fatalf("unexpected integration route specs: %#v", got)
	}
}

func TestIntegrationRouteInventoryMatchesWorkspaceOps(t *testing.T) {
	wantInventory := workspaceops.IntegrationInventoryRoutes()
	if got := uniqueIntegrationRoutePaths(listIntegrationRouteSpecs()); !slices.Equal(got, wantInventory) {
		t.Fatalf("unexpected api integration inventory paths: %#v", got)
	}
}

func TestRegisterIntegrationRoutesMountsExpectedHandlers(t *testing.T) {
	mux := http.NewServeMux()
	registerIntegrationRoutes(mux, t.TempDir())

	testCases := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{
			name:   "configure integration",
			method: http.MethodPost,
			target: "/api/workspaces/workspace-1/integrations?provider=github",
			body:   `{}`,
		},
		{
			name:   "list integrations",
			method: http.MethodGet,
			target: "/api/workspaces/workspace-1/integrations",
		},
		{
			name:   "test integration",
			method: http.MethodPost,
			target: "/api/integrations/test",
			body:   `{}`,
		},
		{
			name:   "import github issues",
			method: http.MethodPost,
			target: "/api/workspaces/workspace-1/integrations/github/import?repo=acme/repo&state=open",
		},
		{
			name:   "create github pr",
			method: http.MethodPost,
			target: "/api/workspaces/workspace-1/integrations/github/pr",
			body:   `{"run_id":"run-1","issue_id":"ISSUE-1","head_branch":"fix/export"}`,
		},
		{
			name:   "send slack notification",
			method: http.MethodPost,
			target: "/api/workspaces/workspace-1/integrations/slack/notify?event=run.completed",
		},
		{
			name:   "sync linear issue",
			method: http.MethodPost,
			target: "/api/workspaces/workspace-1/integrations/linear/sync/ISSUE-1",
		},
		{
			name:   "sync jira issue",
			method: http.MethodPost,
			target: "/api/workspaces/workspace-1/integrations/jira/sync/ISSUE-1",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			if tc.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code == http.StatusNotFound && strings.TrimSpace(response.Body.String()) == "404 page not found" {
				t.Fatalf("expected integration handler for %s %s to be registered", tc.method, tc.target)
			}
		})
	}
}

func uniqueIntegrationRoutePaths(specs []integrationRouteSpec) []string {
	paths := make([]string, 0, len(specs))
	seen := map[string]struct{}{}
	for _, spec := range specs {
		if _, exists := seen[spec.Path]; exists {
			continue
		}
		seen[spec.Path] = struct{}{}
		paths = append(paths, spec.Path)
	}
	return paths
}


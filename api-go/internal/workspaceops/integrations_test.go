package workspaceops

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestIntegrationInventoryRoutesStayAlignedWithManifestCapabilities(t *testing.T) {
	wantCapabilityRoutes := []string{
		"/api/workspaces/{workspace_id}/integrations/github/import",
		"/api/workspaces/{workspace_id}/integrations/github/pr",
		"/api/workspaces/{workspace_id}/integrations/slack/notify",
		"/api/workspaces/{workspace_id}/integrations/linear/sync/{issue_id}",
		"/api/workspaces/{workspace_id}/integrations/jira/sync/{issue_id}",
	}
	if got := IntegrationCapabilityRoutes(); !slices.Equal(got, wantCapabilityRoutes) {
		t.Fatalf("unexpected capability routes: %#v", got)
	}

	wantInventoryRoutes := []string{
		"/api/workspaces/{workspace_id}/integrations",
		"/api/integrations/test",
		"/api/workspaces/{workspace_id}/integrations/github/import",
		"/api/workspaces/{workspace_id}/integrations/github/pr",
		"/api/workspaces/{workspace_id}/integrations/slack/notify",
		"/api/workspaces/{workspace_id}/integrations/linear/sync/{issue_id}",
		"/api/workspaces/{workspace_id}/integrations/jira/sync/{issue_id}",
	}
	if got := IntegrationInventoryRoutes(); !slices.Equal(got, wantInventoryRoutes) {
		t.Fatalf("unexpected integration inventory routes: %#v", got)
	}
}

func TestIntegrationRegistryPersistsManifestBackedConfigs(t *testing.T) {
	dataDir, workspaceID, _ := seedIntegrationWorkspace(t)

	manifests := ListIntegrationManifests()
	if len(manifests) < 4 {
		t.Fatalf("expected plugin manifests, got %#v", manifests)
	}

	record, err := ConfigureIntegration(dataDir, workspaceID, "github", map[string]any{
		"token":        "secret",
		"repo":         "acme/repo",
		"api_base_url": "http://example.test",
	})
	if err != nil {
		t.Fatalf("configure integration: %v", err)
	}
	if record.ManifestID != "github_bridge_v1" || record.SurfaceID != "works_with_agents" {
		t.Fatalf("expected manifest-backed github config, got %#v", record)
	}

	configs, err := GetIntegrationConfigs(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("list integrations: %v", err)
	}
	if len(configs) != 1 || configs[0].DisplayName != "GitHub Issue Bridge" {
		t.Fatalf("unexpected configs: %#v", configs)
	}

	activityPath := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	content, err := os.ReadFile(activityPath)
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsIssueAction(content, "integration.configured") {
		t.Fatalf("expected integration.configured activity, got %s", string(content))
	}
}

func TestIntegrationTestResolvesManifestID(t *testing.T) {
	withIntegrationTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "http://slack.example.test/hook" {
			t.Fatalf("unexpected request url: %s", req.URL.String())
		}
		return jsonHTTPResponse(http.StatusOK, ""), nil
	})

	result := TestIntegration(IntegrationTestRequest{
		ManifestID: optionalString("slack_sink_v1"),
		Settings: map[string]any{
			"webhook_url": "http://slack.example.test/hook",
		},
	})
	if !result.OK || result.Provider != "slack" || firstNonEmptyPtr(result.ManifestID) != "slack_sink_v1" {
		t.Fatalf("unexpected test result: %#v", result)
	}
}

func TestImportGitHubIssuesCreatesTrackerArtifacts(t *testing.T) {
	dataDir, workspaceID, _ := seedIntegrationWorkspace(t)

	withIntegrationTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/acme/repo/issues" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Fatalf("expected state=open, got %s", got)
		}
		return jsonHTTPResponse(http.StatusOK, `[
			{
				"number": 101,
				"title": "Export drops tenant ids",
				"body": "## Acceptance Criteria\n- preserve tenant ids\n- keep audit trail",
				"state": "open",
				"html_url": "https://github.example/acme/repo/issues/101",
				"labels": [{"name": "bug"}, {"name": "export"}]
			},
			{
				"number": 102,
				"title": "Ignore PR shadow",
				"pull_request": {"url": "https://github.example/acme/repo/pulls/102"}
			}
		]`), nil
	})

	if _, err := ConfigureIntegration(dataDir, workspaceID, "github", map[string]any{"api_base_url": "http://github.example.test"}); err != nil {
		t.Fatalf("configure github integration: %v", err)
	}

	imports, err := ImportGitHubIssues(dataDir, workspaceID, "acme/repo", "open")
	if err != nil {
		t.Fatalf("import github issues: %v", err)
	}
	if len(imports) != 1 || imports[0].IssueID != "GH-101" {
		t.Fatalf("unexpected imports: %#v", imports)
	}

	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	imported := findIssue(snapshot.Issues, "GH-101")
	if imported == nil || imported.Source != "tracker_issue" {
		t.Fatalf("expected imported tracker issue, got %#v", imported)
	}

	contexts, err := ListTicketContexts(dataDir, workspaceID, "GH-101")
	if err != nil {
		t.Fatalf("list ticket contexts: %v", err)
	}
	if len(contexts) != 1 || len(contexts[0].AcceptanceCriteria) != 2 {
		t.Fatalf("unexpected ticket contexts: %#v", contexts)
	}

	activityPath := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	content, err := os.ReadFile(activityPath)
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsIssueAction(content, "github.imported") || !containsIssueAction(content, "ticket_context.synced") {
		t.Fatalf("expected github import activity, got %s", string(content))
	}
}

func TestCreateGitHubPRPersistsArtifactAndActivity(t *testing.T) {
	dataDir, workspaceID, _ := seedIntegrationWorkspace(t)

	var receivedBody string
	withIntegrationTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/acme/repo/pulls" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		return jsonHTTPResponse(http.StatusOK, `{"number": 12, "html_url": "https://github.example/acme/repo/pull/12", "state": "open"}`), nil
	})

	if _, err := ConfigureIntegration(dataDir, workspaceID, "github", map[string]any{
		"token":        "secret",
		"repo":         "acme/repo",
		"api_base_url": "http://github.example.test",
	}); err != nil {
		t.Fatalf("configure github integration: %v", err)
	}

	result, err := CreateGitHubPR(dataDir, workspaceID, GitHubPRCreate{
		WorkspaceID: workspaceID,
		RunID:       "run_123",
		IssueID:     "P0_25M03_001",
		HeadBranch:  "fix/export",
	})
	if err != nil {
		t.Fatalf("create github pr: %v", err)
	}
	if result.PRNumber != 12 {
		t.Fatalf("unexpected pr result: %#v", result)
	}
	if !strings.Contains(receivedBody, `"head":"fix/export"`) || !strings.Contains(receivedBody, `"title":"Fix: Repair tenant export evidence"`) {
		t.Fatalf("unexpected pr payload: %s", receivedBody)
	}

	path := filepath.Join(dataDir, "pull_requests", result.PRID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected PR artifact at %s: %v", path, err)
	}
	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsIssueAction(content, "github.pr_created") {
		t.Fatalf("expected github.pr_created activity, got %s", string(content))
	}
}

func TestSendSlackNotificationPersistsArtifact(t *testing.T) {
	dataDir, workspaceID, _ := seedIntegrationWorkspace(t)

	var receivedBody string
	withIntegrationTransport(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		return jsonHTTPResponse(http.StatusOK, ""), nil
	})

	if _, err := ConfigureIntegration(dataDir, workspaceID, "slack", map[string]any{
		"webhook_url": "http://slack.example.test/hook",
		"channel":     "#ops",
	}); err != nil {
		t.Fatalf("configure slack integration: %v", err)
	}

	result, err := SendSlackNotification(dataDir, workspaceID, "run.completed", nil)
	if err != nil {
		t.Fatalf("send slack notification: %v", err)
	}
	if result.Status != "sent" || !strings.Contains(receivedBody, "Run Completed") {
		t.Fatalf("unexpected slack notification result: %#v body=%s", result, receivedBody)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "notifications", result.NotificationID+".json")); err != nil {
		t.Fatalf("expected notification artifact: %v", err)
	}
}

func TestSyncIssueToLinearPersistsTicketContextAndActivity(t *testing.T) {
	dataDir, workspaceID, _ := seedIntegrationWorkspace(t)

	withIntegrationTransport(t, func(r *http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{"data":{"issueCreate":{"issue":{"id":"lin_123","identifier":"ENG-42","title":"Repair tenant export evidence","url":"https://linear.example/ENG-42"}}}}`), nil
	})

	if _, err := ConfigureIntegration(dataDir, workspaceID, "linear", map[string]any{
		"api_key":      "secret",
		"team_id":      "team-1",
		"api_base_url": "http://linear.example.test/graphql",
	}); err != nil {
		t.Fatalf("configure linear integration: %v", err)
	}

	result, err := SyncIssueToLinear(dataDir, workspaceID, "P0_25M03_001")
	if err != nil {
		t.Fatalf("sync linear issue: %v", err)
	}
	if firstNonEmptyPtr(result.LinearStatus) != "ENG-42" {
		t.Fatalf("unexpected linear sync: %#v", result)
	}
	contexts, err := ListTicketContexts(dataDir, workspaceID, "P0_25M03_001")
	if err != nil {
		t.Fatalf("list ticket contexts: %v", err)
	}
	if !hasTicketContextProvider(contexts, "linear") {
		t.Fatalf("expected linear ticket context, got %#v", contexts)
	}
	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsIssueAction(content, "linear.synced") {
		t.Fatalf("expected linear.synced activity, got %s", string(content))
	}
}

func TestSyncIssueToJiraPersistsTicketContextAndActivity(t *testing.T) {
	dataDir, workspaceID, _ := seedIntegrationWorkspace(t)

	withIntegrationTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/rest/api/3/issue" {
			t.Fatalf("unexpected jira path: %s", r.URL.Path)
		}
		return jsonHTTPResponse(http.StatusOK, `{"key":"OPS-12"}`), nil
	})

	if _, err := ConfigureIntegration(dataDir, workspaceID, "jira", map[string]any{
		"base_url":    "http://jira.example.test",
		"email":       "ops@example.com",
		"api_token":   "token",
		"project_key": "OPS",
	}); err != nil {
		t.Fatalf("configure jira integration: %v", err)
	}

	result, err := SyncIssueToJira(dataDir, workspaceID, "P0_25M03_001")
	if err != nil {
		t.Fatalf("sync jira issue: %v", err)
	}
	if firstNonEmptyPtr(result.JiraKey) != "OPS-12" {
		t.Fatalf("unexpected jira sync: %#v", result)
	}
	contexts, err := ListTicketContexts(dataDir, workspaceID, "P0_25M03_001")
	if err != nil {
		t.Fatalf("list ticket contexts: %v", err)
	}
	if !hasTicketContextProvider(contexts, "jira") {
		t.Fatalf("expected jira ticket context, got %#v", contexts)
	}
	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsIssueAction(content, "jira.synced") {
		t.Fatalf("expected jira.synced activity, got %s", string(content))
	}
}

func seedIntegrationWorkspace(t *testing.T) (string, string, string) {
	t.Helper()
	rootDir := t.TempDir()
	dataDir := filepath.Join(rootDir, "data")
	workspaceID := "workspace-1"
	repoRoot := filepath.Join(rootDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("mkdir repo root: %v", err)
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces.json"), []workspaceRecord{
		{
			WorkspaceID: workspaceID,
			Name:        "repo",
			RootPath:    repoRoot,
		},
	}); err != nil {
		t.Fatalf("write workspaces: %v", err)
	}
	snapshot := workspaceSnapshot{
		Workspace: workspaceRecord{
			WorkspaceID: workspaceID,
			Name:        "repo",
			RootPath:    repoRoot,
		},
		Issues: []issueRecord{
			{
				BugID:                "P0_25M03_001",
				Title:                "Repair tenant export evidence",
				Severity:             "HIGH",
				IssueStatus:          "open",
				Source:               "ledger",
				DocStatus:            "open",
				CodeStatus:           "unknown",
				Evidence:             []evidenceRef{},
				VerificationEvidence: []evidenceRef{},
				TestsAdded:           []string{},
				TestsPassed:          []string{},
				DriftFlags:           []string{},
				Labels:               []string{"export", "tenant"},
				ReviewReadyRuns:      []string{},
				UpdatedAt:            nowUTC(),
			},
		},
		Summary: map[string]int{"issues": 1},
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json"), snapshot); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	run := runRecord{
		RunID:       "run_123",
		WorkspaceID: workspaceID,
		IssueID:     "P0_25M03_001",
		Title:       "Repair tenant export evidence",
		Summary: map[string]any{
			"text_excerpt": "Updated export serialization to preserve tenant ids.",
		},
		CreatedAt: nowUTC(),
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "run_123.json"), run); err != nil {
		t.Fatalf("write run: %v", err)
	}
	return dataDir, workspaceID, repoRoot
}

func findIssue(items []issueRecord, issueID string) *issueRecord {
	for idx := range items {
		if items[idx].BugID == issueID {
			return &items[idx]
		}
	}
	return nil
}

func hasTicketContextProvider(items []TicketContextRecord, provider string) bool {
	for _, item := range items {
		if item.Provider == provider {
			return true
		}
	}
	return false
}

func TestContainsIssueActionHelperParsesActivity(t *testing.T) {
	record := activityRecord{Action: "example.action"}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal activity: %v", err)
	}
	if !containsIssueAction(payload, "example.action") {
		t.Fatalf("expected helper to find example.action")
	}
}

func withIntegrationTransport(t *testing.T, fn roundTripFunc) {
	t.Helper()
	original := integrationHTTPClient
	integrationHTTPClient = &http.Client{Transport: fn}
	t.Cleanup(func() {
		integrationHTTPClient = original
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func jsonHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

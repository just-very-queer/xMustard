package workspaceops

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type AgentPluginManifest struct {
	ManifestID         string   `json:"manifest_id"`
	Provider           string   `json:"provider"`
	Title              string   `json:"title"`
	SurfaceID          string   `json:"surface_id"`
	ProtocolID         string   `json:"protocol_id"`
	Purpose            string   `json:"purpose"`
	AuthType           string   `json:"auth_type"`
	AuthFields         []string `json:"auth_fields"`
	SubscriptionTopics []string `json:"subscription_topics"`
	SupportedActions   []string `json:"supported_actions"`
	CapabilityRoutes   []string `json:"capability_routes"`
}

type IntegrationConfig struct {
	ConfigID           string         `json:"config_id"`
	WorkspaceID        string         `json:"workspace_id"`
	Provider           string         `json:"provider"`
	ManifestID         string         `json:"manifest_id"`
	SurfaceID          string         `json:"surface_id"`
	ProtocolID         string         `json:"protocol_id"`
	DisplayName        string         `json:"display_name"`
	Enabled            bool           `json:"enabled"`
	Settings           map[string]any `json:"settings"`
	SubscriptionTopics []string       `json:"subscription_topics"`
	SupportedActions   []string       `json:"supported_actions"`
	CreatedAt          string         `json:"created_at"`
	UpdatedAt          string         `json:"updated_at"`
}

type IntegrationTestRequest struct {
	Provider   string         `json:"provider"`
	ManifestID *string        `json:"manifest_id,omitempty"`
	Settings   map[string]any `json:"settings"`
}

type IntegrationTestResult struct {
	Provider   string         `json:"provider"`
	ManifestID *string        `json:"manifest_id,omitempty"`
	OK         bool           `json:"ok"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details"`
	TestedAt   string         `json:"tested_at"`
}

type GitHubIssueImport struct {
	ImportID    string   `json:"import_id"`
	WorkspaceID string   `json:"workspace_id"`
	GitHubRepo  string   `json:"github_repo"`
	IssueNumber int      `json:"issue_number"`
	IssueID     string   `json:"issue_id"`
	Title       string   `json:"title"`
	Body        *string  `json:"body,omitempty"`
	Labels      []string `json:"labels"`
	State       string   `json:"state"`
	HTMLURL     *string  `json:"html_url,omitempty"`
	ImportedAt  string   `json:"imported_at"`
}

type GitHubPRCreate struct {
	WorkspaceID string  `json:"workspace_id"`
	RunID       string  `json:"run_id"`
	IssueID     string  `json:"issue_id"`
	HeadBranch  string  `json:"head_branch"`
	BaseBranch  string  `json:"base_branch"`
	Title       *string `json:"title,omitempty"`
	Body        *string `json:"body,omitempty"`
	Draft       bool    `json:"draft"`
}

type GitHubPRResult struct {
	PRID        string `json:"pr_id"`
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id"`
	IssueID     string `json:"issue_id"`
	PRNumber    int    `json:"pr_number"`
	HTMLURL     string `json:"html_url"`
	State       string `json:"state"`
	CreatedAt   string `json:"created_at"`
}

type SlackNotification struct {
	NotificationID string  `json:"notification_id"`
	WorkspaceID    string  `json:"workspace_id"`
	Event          string  `json:"event"`
	Channel        *string `json:"channel,omitempty"`
	WebhookURL     *string `json:"webhook_url,omitempty"`
	Message        string  `json:"message"`
	Status         string  `json:"status"`
	Error          *string `json:"error,omitempty"`
	CreatedAt      string  `json:"created_at"`
	SentAt         *string `json:"sent_at,omitempty"`
}

type LinearIssueSync struct {
	SyncID        string   `json:"sync_id"`
	WorkspaceID   string   `json:"workspace_id"`
	IssueID       string   `json:"issue_id"`
	LinearID      *string  `json:"linear_id,omitempty"`
	LinearTeamID  *string  `json:"linear_team_id,omitempty"`
	LinearStatus  *string  `json:"linear_status,omitempty"`
	Title         string   `json:"title"`
	Description   *string  `json:"description,omitempty"`
	Labels        []string `json:"labels"`
	Priority      *string  `json:"priority,omitempty"`
	SyncDirection string   `json:"sync_direction"`
	SyncedAt      string   `json:"synced_at"`
}

type JiraIssueSync struct {
	SyncID        string   `json:"sync_id"`
	WorkspaceID   string   `json:"workspace_id"`
	IssueID       string   `json:"issue_id"`
	JiraKey       *string  `json:"jira_key,omitempty"`
	JiraProject   *string  `json:"jira_project,omitempty"`
	JiraStatus    *string  `json:"jira_status,omitempty"`
	Summary       string   `json:"summary"`
	Description   *string  `json:"description,omitempty"`
	Labels        []string `json:"labels"`
	Priority      *string  `json:"priority,omitempty"`
	IssueType     string   `json:"issue_type"`
	SyncDirection string   `json:"sync_direction"`
	SyncedAt      string   `json:"synced_at"`
}

type validationError struct {
	message string
}

func (err validationError) Error() string {
	return err.message
}

type missingResourceError struct {
	message string
}

func (err missingResourceError) Error() string {
	return err.message
}

func (err missingResourceError) Unwrap() error {
	return os.ErrNotExist
}

func IsIntegrationValidationError(err error) bool {
	var target validationError
	return errors.As(err, &target)
}

var integrationHTTPClient = &http.Client{Timeout: 15 * time.Second}

var integrationPluginManifests = []AgentPluginManifest{
	{
		ManifestID:         "github_bridge_v1",
		Provider:           "github",
		Title:              "GitHub Issue Bridge",
		SurfaceID:          "works_with_agents",
		ProtocolID:         "agent_plugin_manifest_v1",
		Purpose:            "Imports GitHub issues into durable tracker state and creates PRs from governed run artifacts.",
		AuthType:           "bearer",
		AuthFields:         []string{"token", "repo"},
		SubscriptionTopics: []string{"issue.imported", "run.pr_created"},
		SupportedActions:   []string{"import_issues", "create_pull_request"},
		CapabilityRoutes: []string{
			"/api/workspaces/{workspace_id}/integrations/github/import",
			"/api/workspaces/{workspace_id}/integrations/github/pr",
		},
	},
	{
		ManifestID:         "slack_sink_v1",
		Provider:           "slack",
		Title:              "Slack Event Sink",
		SurfaceID:          "works_with_agents",
		ProtocolID:         "agent_event_sink_v1",
		Purpose:            "Pushes durable run and verification events to Slack without relying on chat-thread state.",
		AuthType:           "webhook",
		AuthFields:         []string{"webhook_url", "channel"},
		SubscriptionTopics: []string{"run.completed", "run.failed", "run.cancelled", "verification.recorded", "fix.applied", "plan.approved", "plan.rejected"},
		SupportedActions:   []string{"send_notification"},
		CapabilityRoutes: []string{
			"/api/workspaces/{workspace_id}/integrations/slack/notify",
		},
	},
	{
		ManifestID:         "linear_sync_v1",
		Provider:           "linear",
		Title:              "Linear Issue Sync",
		SurfaceID:          "works_with_agents",
		ProtocolID:         "agent_event_sink_v1",
		Purpose:            "Syncs tracker issues into Linear while preserving imported ticket context and activity evidence.",
		AuthType:           "api_key",
		AuthFields:         []string{"api_key", "team_id"},
		SubscriptionTopics: []string{"issue.synced"},
		SupportedActions:   []string{"sync_issue"},
		CapabilityRoutes: []string{
			"/api/workspaces/{workspace_id}/integrations/linear/sync/{issue_id}",
		},
	},
	{
		ManifestID:         "jira_sync_v1",
		Provider:           "jira",
		Title:              "Jira Issue Sync",
		SurfaceID:          "works_with_agents",
		ProtocolID:         "agent_event_sink_v1",
		Purpose:            "Syncs tracker issues into Jira and records the durable ticket-context bridge back into xMustard.",
		AuthType:           "basic",
		AuthFields:         []string{"base_url", "email", "api_token", "project_key"},
		SubscriptionTopics: []string{"issue.synced"},
		SupportedActions:   []string{"sync_issue"},
		CapabilityRoutes: []string{
			"/api/workspaces/{workspace_id}/integrations/jira/sync/{issue_id}",
		},
	},
}

func ListIntegrationManifests() []AgentPluginManifest {
	return slices.Clone(integrationPluginManifests)
}

func ConfigureIntegration(dataDir string, workspaceID string, provider string, settings map[string]any) (*IntegrationConfig, error) {
	if _, err := getWorkspaceRecord(dataDir, workspaceID); err != nil {
		return nil, err
	}
	manifest, err := resolveIntegrationManifest(provider, nil)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	record := IntegrationConfig{
		ConfigID:           "int_" + hashID(workspaceID, manifest.Provider, now)[:12],
		WorkspaceID:        workspaceID,
		Provider:           manifest.Provider,
		ManifestID:         manifest.ManifestID,
		SurfaceID:          manifest.SurfaceID,
		ProtocolID:         manifest.ProtocolID,
		DisplayName:        manifest.Title,
		Enabled:            true,
		Settings:           cloneSettings(settings),
		SubscriptionTopics: append([]string{}, manifest.SubscriptionTopics...),
		SupportedActions:   append([]string{}, manifest.SupportedActions...),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := saveIntegrationConfig(dataDir, record); err != nil {
		return nil, err
	}
	if err := appendEntityActivity(
		dataDir,
		workspaceID,
		"settings",
		"integration:"+manifest.Provider,
		operatorActor(),
		"integration.configured",
		"Configured "+manifest.Provider+" integration",
		nil,
		nil,
		map[string]any{
			"provider":    manifest.Provider,
			"manifest_id": manifest.ManifestID,
			"surface_id":  manifest.SurfaceID,
		},
	); err != nil {
		return nil, err
	}
	return &record, nil
}

func GetIntegrationConfigs(dataDir string, workspaceID string) ([]IntegrationConfig, error) {
	if _, err := getWorkspaceRecord(dataDir, workspaceID); err != nil {
		return nil, err
	}
	return loadIntegrationConfigs(dataDir, workspaceID)
}

func TestIntegration(request IntegrationTestRequest) *IntegrationTestResult {
	manifest, err := resolveIntegrationManifest(request.Provider, request.ManifestID)
	if err != nil {
		return &IntegrationTestResult{
			Provider: fallbackString(strings.TrimSpace(request.Provider), "unknown"),
			OK:       false,
			Message:  err.Error(),
			Details:  map[string]any{},
			TestedAt: nowUTC(),
		}
	}
	settings := cloneSettings(request.Settings)
	switch manifest.Provider {
	case "github":
		return testGitHubIntegration(manifest, settings)
	case "slack":
		return testSlackIntegration(manifest, settings)
	case "linear":
		return testLinearIntegration(manifest, settings)
	case "jira":
		return testJiraIntegration(manifest, settings)
	default:
		return &IntegrationTestResult{
			Provider:   manifest.Provider,
			ManifestID: optionalString(manifest.ManifestID),
			OK:         false,
			Message:    "Unknown provider: " + manifest.Provider,
			Details:    map[string]any{},
			TestedAt:   nowUTC(),
		}
	}
}

func ImportGitHubIssues(dataDir string, workspaceID string, repo string, state string) ([]GitHubIssueImport, error) {
	if strings.TrimSpace(repo) == "" {
		return nil, validationError{message: "repo is required"}
	}
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	config, _ := loadIntegrationConfig(dataDir, workspaceID, "github")

	requestURL := strings.TrimRight(settingString(configSettings(config), "api_base_url"), "/")
	if requestURL == "" {
		requestURL = "https://api.github.com"
	}
	requestURL = requestURL + "/repos/" + githubRepoPath(repo) + "/issues?state=" + url.QueryEscape(fallbackString(strings.TrimSpace(state), "open")) + "&per_page=100"
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := settingString(configSettings(config), "token"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	var issuesData []map[string]any
	if err := doJSON(req, &issuesData); err != nil {
		return nil, validationError{message: "GitHub API error: " + err.Error()}
	}

	existingIssueIDs := map[string]struct{}{}
	for _, issue := range snapshot.Issues {
		existingIssueIDs[issue.BugID] = struct{}{}
	}

	imports := make([]GitHubIssueImport, 0, len(issuesData))
	for _, raw := range issuesData {
		if _, isPR := raw["pull_request"]; isPR {
			continue
		}
		number := asInt(raw["number"])
		title := strings.TrimSpace(asString(raw["title"]))
		body := trimOptional(optionalString(asString(raw["body"])))
		labels := githubLabels(raw["labels"])
		issueID := fmt.Sprintf("GH-%d", number)
		record := GitHubIssueImport{
			ImportID:    "ghi_" + hashID(workspaceID, repo, issueID, nowUTC())[:8],
			WorkspaceID: workspaceID,
			GitHubRepo:  strings.TrimSpace(repo),
			IssueNumber: number,
			IssueID:     issueID,
			Title:       title,
			Body:        body,
			Labels:      labels,
			State:       fallbackString(strings.TrimSpace(asString(raw["state"])), "open"),
			HTMLURL:     trimOptional(optionalString(asString(raw["html_url"]))),
			ImportedAt:  nowUTC(),
		}
		imports = append(imports, record)
		if _, exists := existingIssueIDs[issueID]; !exists {
			summary := trimmedSnippet(body, 500)
			source := "tracker_issue"
			if _, err := CreateIssue(dataDir, workspaceID, IssueCreateRequest{
				BugID:    &issueID,
				Title:    record.Title,
				Severity: "medium",
				Summary:  summary,
				Labels:   record.Labels,
				Source:   &source,
			}); err != nil && !isDuplicateIssueError(err) {
				return nil, err
			}
			existingIssueIDs[issueID] = struct{}{}
		}
		if _, err := saveImportedTicketContext(dataDir, workspaceID, record.IssueID, TicketContextUpsertRequest{
			ContextID:          optionalString(fmt.Sprintf("github-%d", record.IssueNumber)),
			Provider:           "github",
			ExternalID:         optionalString(fmt.Sprintf("%s#%d", repo, record.IssueNumber)),
			Title:              record.Title,
			Summary:            strings.TrimSpace(firstNonEmptyPtr(record.Body)),
			AcceptanceCriteria: parseAcceptanceCriteria(firstNonEmptyPtr(record.Body)),
			Labels:             record.Labels,
			Links:              trimStringList([]string{firstNonEmptyPtr(record.HTMLURL)}, 8),
			Status:             optionalString(record.State),
			SourceExcerpt:      trimmedSnippet(record.Body, 500),
		}); err != nil {
			return nil, err
		}
	}
	if err := appendEntityActivity(
		dataDir,
		workspaceID,
		"workspace",
		workspaceID,
		systemActor(),
		"github.imported",
		fmt.Sprintf("Imported %d issues from %s", len(imports), strings.TrimSpace(repo)),
		nil,
		nil,
		map[string]any{"repo": strings.TrimSpace(repo), "count": len(imports), "manifest_id": "github_bridge_v1"},
	); err != nil {
		return nil, err
	}
	return imports, nil
}

func CreateGitHubPR(dataDir string, workspaceID string, request GitHubPRCreate) (*GitHubPRResult, error) {
	config, err := loadRequiredIntegrationConfig(dataDir, workspaceID, "github", "GitHub integration not configured")
	if err != nil {
		return nil, err
	}
	token := settingString(config.Settings, "token")
	repo := settingString(config.Settings, "repo")
	if token == "" || repo == "" {
		return nil, validationError{message: "GitHub token and repo must be configured"}
	}
	run, err := loadRun(dataDir, workspaceID, request.RunID)
	if err != nil {
		return nil, err
	}

	prTitle := strings.TrimSpace(firstNonEmptyPtr(request.Title))
	if prTitle == "" {
		prTitle = "Fix for " + strings.TrimSpace(request.IssueID)
		if strings.TrimSpace(run.Title) != "" {
			prTitle = "Fix: " + strings.TrimSpace(run.Title)
		}
	}
	prBody := strings.TrimSpace(firstNonEmptyPtr(request.Body))
	if excerpt := trimmedSummaryExcerpt(run); excerpt != "" {
		if prBody != "" {
			prBody += "\n\n"
		}
		prBody += "**Run Summary:** " + excerpt
	}
	if prBody != "" {
		prBody += "\n\n"
	}
	prBody += "**Issue:** " + strings.TrimSpace(request.IssueID)

	payload := map[string]any{
		"title": prTitle,
		"body":  prBody,
		"head":  strings.TrimSpace(request.HeadBranch),
		"base":  fallbackString(strings.TrimSpace(request.BaseBranch), "main"),
		"draft": request.Draft,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	baseURL := strings.TrimRight(settingString(config.Settings, "api_base_url"), "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	reqURL := baseURL + "/repos/" + githubRepoPath(repo) + "/pulls"
	httpReq, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	var response struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
	}
	if err := doJSON(httpReq, &response); err != nil {
		return nil, validationError{message: "GitHub PR creation failed: " + err.Error()}
	}

	result := GitHubPRResult{
		PRID:        "pr_" + hashID(workspaceID, request.RunID, request.IssueID, nowUTC())[:8],
		WorkspaceID: workspaceID,
		RunID:       request.RunID,
		IssueID:     request.IssueID,
		PRNumber:    response.Number,
		HTMLURL:     response.HTMLURL,
		State:       fallbackString(strings.TrimSpace(response.State), "open"),
		CreatedAt:   nowUTC(),
	}
	if err := saveIntegrationArtifact(filepath.Join(dataDir, "pull_requests", result.PRID+".json"), result); err != nil {
		return nil, err
	}
	if err := appendEntityActivity(
		dataDir,
		workspaceID,
		"run",
		request.RunID,
		operatorActor(),
		"github.pr_created",
		fmt.Sprintf("Created PR #%d for %s", response.Number, strings.TrimSpace(request.IssueID)),
		optionalString(strings.TrimSpace(request.IssueID)),
		optionalString(strings.TrimSpace(request.RunID)),
		map[string]any{
			"pr_number":   response.Number,
			"html_url":    result.HTMLURL,
			"manifest_id": config.ManifestID,
		},
	); err != nil {
		return nil, err
	}
	return &result, nil
}

func SendSlackNotification(dataDir string, workspaceID string, event string, message *string) (*SlackNotification, error) {
	config, err := loadRequiredIntegrationConfig(dataDir, workspaceID, "slack", "Slack integration not configured")
	if err != nil {
		return nil, err
	}
	webhookURL := settingString(config.Settings, "webhook_url")
	channel := trimOptional(optionalString(settingString(config.Settings, "channel")))
	if webhookURL == "" {
		return nil, validationError{message: "Slack webhook_url must be configured"}
	}
	text := strings.TrimSpace(firstNonEmptyPtr(message))
	if text == "" {
		text = defaultSlackMessage(workspaceID, event)
	}
	payload := map[string]any{"text": text}
	if channel != nil {
		payload["channel"] = *channel
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	notification := SlackNotification{
		NotificationID: "sn_" + hashID(workspaceID, event, text, nowUTC())[:8],
		WorkspaceID:    workspaceID,
		Event:          strings.TrimSpace(event),
		Channel:        channel,
		WebhookURL:     optionalString(webhookURL),
		Message:        text,
		Status:         "pending",
		CreatedAt:      nowUTC(),
	}

	httpReq, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := integrationHTTPClient.Do(httpReq)
	if err != nil {
		notification.Status = "failed"
		notification.Error = optionalString(err.Error())
	} else {
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			notification.Status = "failed"
			notification.Error = optionalString(strings.TrimSpace(string(body)))
		} else {
			notification.Status = "sent"
			notification.SentAt = optionalString(nowUTC())
		}
	}
	if err := saveIntegrationArtifact(filepath.Join(dataDir, "notifications", notification.NotificationID+".json"), notification); err != nil {
		return nil, err
	}
	return &notification, nil
}

func SyncIssueToLinear(dataDir string, workspaceID string, issueID string) (*LinearIssueSync, error) {
	config, err := loadRequiredIntegrationConfig(dataDir, workspaceID, "linear", "Linear integration not configured")
	if err != nil {
		return nil, err
	}
	apiKey := settingString(config.Settings, "api_key")
	teamID := settingString(config.Settings, "team_id")
	if apiKey == "" || teamID == "" {
		return nil, validationError{message: "Linear api_key and team_id must be configured"}
	}
	issue, err := requireIssue(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}

	query := map[string]any{
		"query": `
            mutation CreateIssue($title: String!, $teamId: String!, $description: String, $priority: Float) {
                issueCreate(input: {title: $title, teamId: $teamId, description: $description, priority: $priority}) {
                    issue { id identifier title url }
                }
            }
        `,
		"variables": map[string]any{
			"title":       issue.Title,
			"teamId":      teamID,
			"description": firstNonEmptyPtr(issue.Summary),
			"priority":    severityToLinearPriority(issue.Severity),
		},
	}
	body, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(settingString(config.Settings, "api_base_url"), "/")
	if endpoint == "" {
		endpoint = "https://api.linear.app/graphql"
	}
	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", apiKey)

	var response struct {
		Data struct {
			IssueCreate struct {
				Issue struct {
					ID         string `json:"id"`
					Identifier string `json:"identifier"`
					Title      string `json:"title"`
					URL        string `json:"url"`
				} `json:"issue"`
			} `json:"issueCreate"`
		} `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	if err := doJSON(httpReq, &response); err != nil {
		return nil, validationError{message: "Linear API error: " + err.Error()}
	}
	if len(response.Errors) > 0 {
		return nil, validationError{message: "Linear API error: " + asString(response.Errors[0]["message"])}
	}
	created := response.Data.IssueCreate.Issue
	sync := LinearIssueSync{
		SyncID:        "lsync_" + hashID(workspaceID, issueID, nowUTC())[:8],
		WorkspaceID:   workspaceID,
		IssueID:       issueID,
		LinearID:      trimOptional(optionalString(created.ID)),
		LinearTeamID:  optionalString(teamID),
		LinearStatus:  trimOptional(optionalString(created.Identifier)),
		Title:         issue.Title,
		Description:   issue.Summary,
		Labels:        append([]string{}, issue.Labels...),
		Priority:      optionalString(issue.Severity),
		SyncDirection: "push",
		SyncedAt:      nowUTC(),
	}
	if err := saveIntegrationArtifact(filepath.Join(dataDir, "linear_syncs", sync.SyncID+".json"), sync); err != nil {
		return nil, err
	}
	if _, err := saveImportedTicketContext(dataDir, workspaceID, issueID, TicketContextUpsertRequest{
		ContextID:  optionalString("linear-" + fallbackString(created.ID, issueID)),
		Provider:   "linear",
		ExternalID: firstNonEmptyOptional(created.Identifier, created.ID),
		Title:      issue.Title,
		Summary:    firstNonEmptyPtr(issue.Summary),
		Labels:     append([]string{}, issue.Labels...),
		Links:      trimStringList([]string{created.URL}, 8),
		Status:     optionalString("synced"),
	}); err != nil {
		return nil, err
	}
	if err := appendEntityActivity(
		dataDir,
		workspaceID,
		"issue",
		issueID,
		systemActor(),
		"linear.synced",
		"Synced issue to Linear: "+fallbackString(created.Identifier, "unknown"),
		optionalString(issueID),
		nil,
		map[string]any{"linear_id": created.ID, "manifest_id": config.ManifestID},
	); err != nil {
		return nil, err
	}
	return &sync, nil
}

func SyncIssueToJira(dataDir string, workspaceID string, issueID string) (*JiraIssueSync, error) {
	config, err := loadRequiredIntegrationConfig(dataDir, workspaceID, "jira", "Jira integration not configured")
	if err != nil {
		return nil, err
	}
	baseURL := settingString(config.Settings, "base_url")
	email := settingString(config.Settings, "email")
	apiToken := settingString(config.Settings, "api_token")
	projectKey := settingString(config.Settings, "project_key")
	if baseURL == "" || email == "" || apiToken == "" || projectKey == "" {
		return nil, validationError{message: "Jira base_url, email, api_token, and project_key must be configured"}
	}
	issue, err := requireIssue(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}

	fields := map[string]any{
		"project": map[string]any{"key": projectKey},
		"summary": issue.Title,
		"description": map[string]any{
			"type":    "doc",
			"version": 1,
			"content": []map[string]any{
				{
					"type": "paragraph",
					"content": []map[string]any{
						{"type": "text", "text": firstNonEmptyPtr(issue.Summary)},
					},
				},
			},
		},
		"issuetype": map[string]any{"name": "Bug"},
		"labels":    issue.Labels,
		"priority":  map[string]any{"name": severityToJiraPriority(issue.Severity)},
	}
	body, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		return nil, err
	}
	creds := base64.StdEncoding.EncodeToString([]byte(email + ":" + apiToken))
	httpReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/rest/api/3/issue", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Basic "+creds)

	var response struct {
		Key string `json:"key"`
	}
	if err := doJSON(httpReq, &response); err != nil {
		return nil, validationError{message: "Jira API error: " + err.Error()}
	}
	sync := JiraIssueSync{
		SyncID:        "jsync_" + hashID(workspaceID, issueID, nowUTC())[:8],
		WorkspaceID:   workspaceID,
		IssueID:       issueID,
		JiraKey:       trimOptional(optionalString(response.Key)),
		JiraProject:   optionalString(projectKey),
		Summary:       issue.Title,
		Description:   issue.Summary,
		Labels:        append([]string{}, issue.Labels...),
		Priority:      optionalString(issue.Severity),
		IssueType:     "Bug",
		SyncDirection: "push",
		SyncedAt:      nowUTC(),
	}
	if err := saveIntegrationArtifact(filepath.Join(dataDir, "jira_syncs", sync.SyncID+".json"), sync); err != nil {
		return nil, err
	}
	jiraURL := ""
	if response.Key != "" {
		jiraURL = strings.TrimRight(baseURL, "/") + "/browse/" + response.Key
	}
	if _, err := saveImportedTicketContext(dataDir, workspaceID, issueID, TicketContextUpsertRequest{
		ContextID:  optionalString("jira-" + fallbackString(response.Key, issueID)),
		Provider:   "jira",
		ExternalID: trimOptional(optionalString(response.Key)),
		Title:      issue.Title,
		Summary:    firstNonEmptyPtr(issue.Summary),
		Labels:     append([]string{}, issue.Labels...),
		Links:      trimStringList([]string{jiraURL}, 8),
		Status:     optionalString("synced"),
	}); err != nil {
		return nil, err
	}
	if err := appendEntityActivity(
		dataDir,
		workspaceID,
		"issue",
		issueID,
		systemActor(),
		"jira.synced",
		"Synced issue to Jira: "+fallbackString(response.Key, "unknown"),
		optionalString(issueID),
		nil,
		map[string]any{"jira_key": response.Key, "manifest_id": config.ManifestID},
	); err != nil {
		return nil, err
	}
	return &sync, nil
}

func saveImportedTicketContext(dataDir string, workspaceID string, issueID string, request TicketContextUpsertRequest) (*TicketContextRecord, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	contexts, err := loadTicketContexts(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	contextID := slugProfileID(request.Provider + "-" + firstNonEmptyPtr(request.ExternalID) + firstNonEmptyString(request.Title))
	if request.ContextID != nil && strings.TrimSpace(*request.ContextID) != "" {
		contextID = strings.TrimSpace(*request.ContextID)
	}
	now := nowUTC()
	var previous *TicketContextRecord
	remaining := contexts[:0]
	for _, context := range contexts {
		if context.ContextID == contextID {
			copy := context
			previous = &copy
			continue
		}
		remaining = append(remaining, context)
	}
	record := TicketContextRecord{
		ContextID:          contextID,
		WorkspaceID:        workspaceID,
		IssueID:            issueID,
		Provider:           fallbackString(strings.TrimSpace(request.Provider), "other"),
		ExternalID:         trimOptional(request.ExternalID),
		Title:              strings.TrimSpace(request.Title),
		Summary:            strings.TrimSpace(request.Summary),
		AcceptanceCriteria: trimStringList(request.AcceptanceCriteria, 12),
		Links:              trimStringList(request.Links, 8),
		Labels:             trimStringList(request.Labels, 12),
		Status:             trimOptional(request.Status),
		SourceExcerpt:      trimOptional(request.SourceExcerpt),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if previous != nil {
		record.CreatedAt = previous.CreatedAt
	}
	remaining = append(remaining, record)
	if err := saveTicketContexts(dataDir, workspaceID, remaining); err != nil {
		return nil, err
	}
	if err := appendEntityActivity(
		dataDir,
		workspaceID,
		"issue",
		issueID,
		systemActor(),
		"ticket_context.synced",
		"Saved ticket context "+record.Title,
		optionalString(issueID),
		nil,
		map[string]any{
			"context_id":  record.ContextID,
			"provider":    record.Provider,
			"external_id": record.ExternalID,
		},
	); err != nil {
		return nil, err
	}
	return &record, nil
}

func resolveIntegrationManifest(provider string, manifestID *string) (AgentPluginManifest, error) {
	trimmedManifestID := strings.TrimSpace(firstNonEmptyPtr(manifestID))
	if trimmedManifestID != "" {
		for _, manifest := range integrationPluginManifests {
			if manifest.ManifestID == trimmedManifestID {
				return manifest, nil
			}
		}
		return AgentPluginManifest{}, validationError{message: "unknown manifest_id: " + trimmedManifestID}
	}
	trimmedProvider := strings.TrimSpace(provider)
	for _, manifest := range integrationPluginManifests {
		if manifest.Provider == trimmedProvider {
			return manifest, nil
		}
	}
	return AgentPluginManifest{}, validationError{message: "unknown provider: " + trimmedProvider}
}

func loadIntegrationConfigs(dataDir string, workspaceID string) ([]IntegrationConfig, error) {
	configDir := filepath.Join(dataDir, "integrations")
	entries, err := os.ReadDir(configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []IntegrationConfig{}, nil
		}
		return nil, err
	}
	items := make([]IntegrationConfig, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), workspaceID+"_") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var record IntegrationConfig
		if err := readJSON(filepath.Join(configDir, entry.Name()), &record); err != nil {
			continue
		}
		items = append(items, enrichIntegrationConfig(record))
	}
	slices.SortFunc(items, func(a, b IntegrationConfig) int {
		if a.Provider < b.Provider {
			return -1
		}
		if a.Provider > b.Provider {
			return 1
		}
		if a.UpdatedAt > b.UpdatedAt {
			return -1
		}
		if a.UpdatedAt < b.UpdatedAt {
			return 1
		}
		return 0
	})
	return items, nil
}

func loadIntegrationConfig(dataDir string, workspaceID string, provider string) (*IntegrationConfig, error) {
	var record IntegrationConfig
	path := filepath.Join(dataDir, "integrations", workspaceID+"_"+provider+".json")
	if err := readJSON(path, &record); err != nil {
		return nil, err
	}
	enriched := enrichIntegrationConfig(record)
	return &enriched, nil
}

func loadRequiredIntegrationConfig(dataDir string, workspaceID string, provider string, message string) (*IntegrationConfig, error) {
	config, err := loadIntegrationConfig(dataDir, workspaceID, provider)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, missingResourceError{message: message}
		}
		return nil, err
	}
	return config, nil
}

func saveIntegrationConfig(dataDir string, record IntegrationConfig) error {
	path := filepath.Join(dataDir, "integrations", record.WorkspaceID+"_"+record.Provider+".json")
	return saveIntegrationArtifact(path, record)
}

func saveIntegrationArtifact(path string, payload any) error {
	return writeJSON(path, payload)
}

func enrichIntegrationConfig(record IntegrationConfig) IntegrationConfig {
	manifest, err := resolveIntegrationManifest(record.Provider, optionalString(record.ManifestID))
	if err != nil {
		return record
	}
	record.ManifestID = manifest.ManifestID
	record.SurfaceID = manifest.SurfaceID
	record.ProtocolID = manifest.ProtocolID
	record.DisplayName = fallbackString(strings.TrimSpace(record.DisplayName), manifest.Title)
	if len(record.SubscriptionTopics) == 0 {
		record.SubscriptionTopics = append([]string{}, manifest.SubscriptionTopics...)
	}
	if len(record.SupportedActions) == 0 {
		record.SupportedActions = append([]string{}, manifest.SupportedActions...)
	}
	if record.Settings == nil {
		record.Settings = map[string]any{}
	}
	return record
}

func appendEntityActivity(
	dataDir string,
	workspaceID string,
	entityType string,
	entityID string,
	actor activityActor,
	action string,
	summary string,
	issueID *string,
	runID *string,
	details map[string]any,
) error {
	record := activityRecord{
		ActivityID:  hashID(workspaceID, entityType, entityID, action, nowUTC()),
		WorkspaceID: workspaceID,
		EntityType:  entityType,
		EntityID:    entityID,
		Action:      action,
		Summary:     summary,
		Actor:       actor,
		IssueID:     trimOptional(issueID),
		RunID:       trimOptional(runID),
		Details:     details,
		CreatedAt:   nowUTC(),
	}
	path := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer handle.Close()
	payload, err := jsonMarshal(record)
	if err != nil {
		return err
	}
	_, err = handle.Write(append(payload, '\n'))
	return err
}

func testGitHubIntegration(manifest AgentPluginManifest, settings map[string]any) *IntegrationTestResult {
	baseURL := strings.TrimRight(settingString(settings, "api_base_url"), "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/user", nil)
	if err != nil {
		return integrationTestFailure(manifest, err.Error())
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := settingString(settings, "token"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	var response map[string]any
	if err := doJSON(req, &response); err != nil {
		return integrationTestFailure(manifest, err.Error())
	}
	login := asString(response["login"])
	return &IntegrationTestResult{
		Provider:   manifest.Provider,
		ManifestID: optionalString(manifest.ManifestID),
		OK:         true,
		Message:    "Connected as " + fallbackString(login, "unknown"),
		Details: map[string]any{
			"login": login,
			"name":  asString(response["name"]),
		},
		TestedAt: nowUTC(),
	}
}

func testSlackIntegration(manifest AgentPluginManifest, settings map[string]any) *IntegrationTestResult {
	webhookURL := settingString(settings, "webhook_url")
	if webhookURL == "" {
		return integrationTestFailure(manifest, "webhook_url required")
	}
	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewBufferString(`{"text":"xMustard integration test"}`))
	if err != nil {
		return integrationTestFailure(manifest, err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := integrationHTTPClient.Do(req)
	if err != nil {
		return integrationTestFailure(manifest, err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return integrationTestFailure(manifest, strings.TrimSpace(string(body)))
	}
	return &IntegrationTestResult{
		Provider:   manifest.Provider,
		ManifestID: optionalString(manifest.ManifestID),
		OK:         true,
		Message:    "Webhook test sent successfully",
		Details:    map[string]any{},
		TestedAt:   nowUTC(),
	}
}

func testLinearIntegration(manifest AgentPluginManifest, settings map[string]any) *IntegrationTestResult {
	apiKey := settingString(settings, "api_key")
	if apiKey == "" {
		return integrationTestFailure(manifest, "api_key required")
	}
	endpoint := strings.TrimRight(settingString(settings, "api_base_url"), "/")
	if endpoint == "" {
		endpoint = "https://api.linear.app/graphql"
	}
	body := bytes.NewBufferString(`{"query":"{ viewer { id name } }"}`)
	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		return integrationTestFailure(manifest, err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)
	var response struct {
		Data struct {
			Viewer struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := doJSON(req, &response); err != nil {
		return integrationTestFailure(manifest, err.Error())
	}
	return &IntegrationTestResult{
		Provider:   manifest.Provider,
		ManifestID: optionalString(manifest.ManifestID),
		OK:         true,
		Message:    "Connected as " + fallbackString(response.Data.Viewer.Name, "unknown"),
		Details:    map[string]any{"name": response.Data.Viewer.Name},
		TestedAt:   nowUTC(),
	}
}

func testJiraIntegration(manifest AgentPluginManifest, settings map[string]any) *IntegrationTestResult {
	baseURL := settingString(settings, "base_url")
	email := settingString(settings, "email")
	apiToken := settingString(settings, "api_token")
	if baseURL == "" || email == "" || apiToken == "" {
		return integrationTestFailure(manifest, "base_url, email, and api_token required")
	}
	creds := base64.StdEncoding.EncodeToString([]byte(email + ":" + apiToken))
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/rest/api/3/myself", nil)
	if err != nil {
		return integrationTestFailure(manifest, err.Error())
	}
	req.Header.Set("Authorization", "Basic "+creds)
	var response map[string]any
	if err := doJSON(req, &response); err != nil {
		return integrationTestFailure(manifest, err.Error())
	}
	displayName := asString(response["displayName"])
	return &IntegrationTestResult{
		Provider:   manifest.Provider,
		ManifestID: optionalString(manifest.ManifestID),
		OK:         true,
		Message:    "Connected as " + fallbackString(displayName, "unknown"),
		Details:    map[string]any{"displayName": displayName},
		TestedAt:   nowUTC(),
	}
}

func integrationTestFailure(manifest AgentPluginManifest, message string) *IntegrationTestResult {
	return &IntegrationTestResult{
		Provider:   manifest.Provider,
		ManifestID: optionalString(manifest.ManifestID),
		OK:         false,
		Message:    strings.TrimSpace(message),
		Details:    map[string]any{},
		TestedAt:   nowUTC(),
	}
}

func doJSON(request *http.Request, target any) error {
	response, err := integrationHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 400 {
		trimmed := strings.TrimSpace(string(body))
		if trimmed == "" {
			trimmed = response.Status
		}
		return errors.New(trimmed)
	}
	if target == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	return json.Unmarshal(body, target)
}

func defaultSlackMessage(workspaceID string, event string) string {
	eventLabels := map[string]string{
		"run.completed":         "Run Completed",
		"run.failed":            "Run Failed",
		"run.cancelled":         "Run Cancelled",
		"verification.recorded": "Verification Recorded",
		"fix.applied":           "Fix Applied",
		"plan.approved":         "Plan Approved",
		"plan.rejected":         "Plan Rejected",
	}
	return "[xMustard] " + fallbackString(eventLabels[event], event) + " (workspace: " + workspaceID + ")"
}

func severityToLinearPriority(severity string) float64 {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return 1
	case "high":
		return 2
	case "low":
		return 4
	default:
		return 3
	}
}

func severityToJiraPriority(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return "Highest"
	case "high":
		return "High"
	case "low":
		return "Low"
	default:
		return "Medium"
	}
}

func trimmedSummaryExcerpt(run *runRecord) string {
	if run == nil || run.Summary == nil {
		return ""
	}
	value := strings.TrimSpace(asString(run.Summary["text_excerpt"]))
	if len(value) > 4000 {
		return value[:4000]
	}
	return value
}

func githubLabels(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return []string{}
	}
	labels := make([]string, 0, len(items))
	for _, item := range items {
		labelMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		label := strings.TrimSpace(asString(labelMap["name"]))
		if label == "" {
			continue
		}
		labels = append(labels, label)
	}
	return labels
}

func parseAcceptanceCriteria(text string) []string {
	if strings.TrimSpace(text) == "" {
		return []string{}
	}
	lines := strings.Split(text, "\n")
	criteria := []string{}
	collecting := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			lower := strings.ToLower(heading)
			if strings.Contains(lower, "acceptance") && strings.Contains(lower, "criteria") {
				collecting = true
				continue
			}
			if collecting && len(criteria) > 0 {
				break
			}
			collecting = false
		}
		if !collecting {
			continue
		}
		value := extractChecklistValue(line)
		if value == "" {
			if len(criteria) > 0 {
				break
			}
			continue
		}
		criteria = append(criteria, value)
		if len(criteria) >= 10 {
			return criteria
		}
	}
	if len(criteria) > 0 {
		return criteria
	}
	checklist := []string{}
	for _, raw := range lines {
		value := extractChecklistValue(strings.TrimSpace(raw))
		if value == "" {
			continue
		}
		checklist = append(checklist, value)
		if len(checklist) >= 10 {
			break
		}
	}
	return checklist
}

func extractChecklistValue(line string) string {
	switch {
	case strings.HasPrefix(line, "- [ ] "):
		return strings.TrimSpace(strings.TrimPrefix(line, "- [ ] "))
	case strings.HasPrefix(line, "- [x] "):
		return strings.TrimSpace(strings.TrimPrefix(line, "- [x] "))
	case strings.HasPrefix(line, "- [X] "):
		return strings.TrimSpace(strings.TrimPrefix(line, "- [X] "))
	case strings.HasPrefix(line, "- "):
		return strings.TrimSpace(strings.TrimPrefix(line, "- "))
	case strings.HasPrefix(line, "* "):
		return strings.TrimSpace(strings.TrimPrefix(line, "* "))
	default:
		for idx, char := range line {
			if char == '.' && idx > 0 {
				prefix := line[:idx]
				if isDigits(prefix) {
					return strings.TrimSpace(line[idx+1:])
				}
			}
		}
	}
	return ""
}

func cloneSettings(settings map[string]any) map[string]any {
	if settings == nil {
		return map[string]any{}
	}
	clone := make(map[string]any, len(settings))
	for key, value := range settings {
		clone[key] = value
	}
	return clone
}

func configSettings(config *IntegrationConfig) map[string]any {
	if config == nil {
		return map[string]any{}
	}
	return config.Settings
}

func settingString(settings map[string]any, key string) string {
	if settings == nil {
		return ""
	}
	return strings.TrimSpace(asString(settings[key]))
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%v", typed)
	case int:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	default:
		return ""
	}
}

func asInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		number, _ := typed.Int64()
		return int(number)
	default:
		return 0
	}
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func trimmedSnippet(value *string, limit int) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > limit {
		trimmed = trimmed[:limit]
	}
	return &trimmed
}

func firstNonEmptyOptional(values ...string) *string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return &trimmed
		}
	}
	return nil
}

func isDuplicateIssueError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "issue already exists")
}

func githubRepoPath(repo string) string {
	return strings.Trim(strings.TrimSpace(repo), "/")
}

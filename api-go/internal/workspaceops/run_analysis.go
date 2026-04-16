package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type RunSessionInsight struct {
	WorkspaceID      string                   `json:"workspace_id"`
	RunID            string                   `json:"run_id"`
	IssueID          string                   `json:"issue_id"`
	Status           string                   `json:"status"`
	Headline         string                   `json:"headline"`
	Summary          string                   `json:"summary"`
	GuidanceUsed     []string                 `json:"guidance_used"`
	Strengths        []string                 `json:"strengths"`
	Risks            []string                 `json:"risks"`
	Recommendations  []string                 `json:"recommendations"`
	AcceptanceReview AcceptanceCriteriaReview `json:"acceptance_review"`
	ScopeWarnings    []ScopeWarning           `json:"scope_warnings"`
	GeneratedAt      string                   `json:"generated_at"`
}

type PlanStep struct {
	StepID          string   `json:"step_id"`
	Description     string   `json:"description"`
	EstimatedImpact string   `json:"estimated_impact"`
	FilesAffected   []string `json:"files_affected"`
	Risks           []string `json:"risks"`
}

type RunPlan struct {
	PlanID          string     `json:"plan_id"`
	RunID           string     `json:"run_id"`
	Phase           string     `json:"phase"`
	Steps           []PlanStep `json:"steps"`
	Summary         string     `json:"summary"`
	Reasoning       *string    `json:"reasoning,omitempty"`
	CreatedAt       string     `json:"created_at"`
	ApprovedAt      *string    `json:"approved_at,omitempty"`
	Approver        *string    `json:"approver,omitempty"`
	Feedback        *string    `json:"feedback,omitempty"`
	ModifiedSummary *string    `json:"modified_summary,omitempty"`
}

type RunMetrics struct {
	RunID         string  `json:"run_id"`
	WorkspaceID   string  `json:"workspace_id"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	EstimatedCost float64 `json:"estimated_cost"`
	DurationMS    int     `json:"duration_ms"`
	Model         string  `json:"model"`
	Runtime       string  `json:"runtime"`
	CalculatedAt  string  `json:"calculated_at"`
}

type CostSummary struct {
	WorkspaceID        string             `json:"workspace_id"`
	TotalRuns          int                `json:"total_runs"`
	TotalInputTokens   int                `json:"total_input_tokens"`
	TotalOutputTokens  int                `json:"total_output_tokens"`
	TotalEstimatedCost float64            `json:"total_estimated_cost"`
	TotalDurationMS    int                `json:"total_duration_ms"`
	RunsByStatus       map[string]int     `json:"runs_by_status"`
	CostByRuntime      map[string]float64 `json:"cost_by_runtime"`
	CostByModel        map[string]float64 `json:"cost_by_model"`
	PeriodStart        *string            `json:"period_start,omitempty"`
	PeriodEnd          *string            `json:"period_end,omitempty"`
}

type ImprovementSuggestion struct {
	SuggestionID    string  `json:"suggestion_id"`
	FilePath        string  `json:"file_path"`
	LineStart       *int    `json:"line_start,omitempty"`
	LineEnd         *int    `json:"line_end,omitempty"`
	Category        string  `json:"category"`
	Severity        string  `json:"severity"`
	Description     string  `json:"description"`
	SuggestedFix    *string `json:"suggested_fix,omitempty"`
	Dismissed       bool    `json:"dismissed"`
	DismissedReason *string `json:"dismissed_reason,omitempty"`
}

type PatchCritique struct {
	CritiqueID       string                   `json:"critique_id"`
	WorkspaceID      string                   `json:"workspace_id"`
	RunID            string                   `json:"run_id"`
	IssueID          string                   `json:"issue_id"`
	OverallQuality   string                   `json:"overall_quality"`
	Correctness      float64                  `json:"correctness"`
	Completeness     float64                  `json:"completeness"`
	Style            float64                  `json:"style"`
	Safety           float64                  `json:"safety"`
	IssuesFound      []string                 `json:"issues_found"`
	Improvements     []ImprovementSuggestion  `json:"improvements"`
	AcceptanceReview AcceptanceCriteriaReview `json:"acceptance_review"`
	ScopeWarnings    []ScopeWarning           `json:"scope_warnings"`
	Summary          string                   `json:"summary"`
	GeneratedAt      string                   `json:"generated_at"`
}

type AcceptanceCriteriaReview struct {
	Status   string   `json:"status"`
	Criteria []string `json:"criteria"`
	Matched  []string `json:"matched,omitempty"`
	Missing  []string `json:"missing,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

type ScopeWarning struct {
	Kind     string   `json:"kind"`
	Message  string   `json:"message"`
	Paths    []string `json:"paths,omitempty"`
	Severity string   `json:"severity"`
}

type DismissImprovementRequest struct {
	Reason *string `json:"reason"`
}

func GetRunSessionInsight(dataDir string, workspaceID string, runID string) (*RunSessionInsight, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}

	critique, _ := loadPatchCritique(dataDir, runID)
	metrics, _ := loadRunMetrics(dataDir, runID)
	packet, _ := BuildIssueContextPacket(dataDir, workspaceID, run.IssueID)

	strengths := []string{}
	risks := []string{}
	recommendations := []string{}

	switch run.Status {
	case "completed":
		strengths = append(strengths, "Run completed successfully.")
	case "failed":
		risks = append(risks, "Run failed before completing successfully.")
	case "cancelled":
		risks = append(risks, "Run was cancelled before completion.")
	default:
		risks = append(risks, fmt.Sprintf("Run is still %s.", run.Status))
	}

	if len(run.GuidancePaths) > 0 {
		strengths = append(strengths, fmt.Sprintf("Run included repository guidance from %d file(s).", len(run.GuidancePaths)))
	} else {
		risks = append(risks, "No repository guidance was attached to this run.")
		recommendations = append(recommendations, "Add AGENTS.md, CONVENTIONS.md, or reusable skills so runs start with stable repository context.")
	}

	excerpt := runExcerpt(run)
	if run.Summary != nil {
		totalEvents, _ := intFromAny(run.Summary["event_count"])
		toolEvents, _ := intFromAny(run.Summary["tool_event_count"])
		if totalEvents > 0 && float64(toolEvents)/float64(maxIntLocal(totalEvents, 1)) > 0.8 {
			risks = append(risks, "The run spent a high share of events in tool usage.")
			recommendations = append(recommendations, "Move repeated workflow guidance into always-on repo instructions or reusable skills to reduce tool churn.")
		}
	}

	if metrics != nil && metrics.DurationMS > 0 {
		strengths = append(strengths, fmt.Sprintf("Captured runtime metrics for cost and duration (%d ms).", metrics.DurationMS))
	} else {
		recommendations = append(recommendations, "Persist run metrics consistently so cost and duration trends can be reviewed later.")
	}

	if critique != nil {
		if (critique.OverallQuality == "excellent" || critique.OverallQuality == "good") && len(critique.IssuesFound) == 0 {
			strengths = append(strengths, "Patch critique did not find correctness issues.")
		}
		risks = append(risks, critique.IssuesFound[:min(len(critique.IssuesFound), 3)]...)
		highSeverity := 0
		for _, item := range critique.Improvements {
			if !item.Dismissed && item.Severity == "high" {
				highSeverity++
			}
		}
		if highSeverity > 0 {
			risks = append(risks, fmt.Sprintf("%d high-severity improvement suggestion(s) remain open.", highSeverity))
			recommendations = append(recommendations, "Review the open high-severity improvement suggestions before treating the run as production-ready.")
		}
	} else if run.Status == "completed" {
		recommendations = append(recommendations, "Generate a critique for completed runs so review feedback is preserved as an artifact.")
	}

	if strings.TrimSpace(excerpt) == "" {
		recommendations = append(recommendations, "Capture a concise final summary in the run output so session review is easier.")
	}

	summary := excerpt
	if critique != nil && strings.TrimSpace(critique.Summary) != "" {
		summary = critique.Summary
	}
	if strings.TrimSpace(summary) == "" {
		summary = fmt.Sprintf("Run %s for %s is %s.", run.RunID, run.IssueID, run.Status)
	}

	acceptanceReview := buildAcceptanceReview(packet, run, excerpt)
	scopeWarnings := buildScopeWarnings(packet, run)
	switch acceptanceReview.Status {
	case "met":
		strengths = append(strengths, "Linked acceptance criteria appear to be covered by the run output and tracker evidence.")
	case "partial":
		risks = append(risks, "Only part of the linked acceptance criteria appears covered.")
		recommendations = append(recommendations, "Review the missing ticket acceptance criteria before marking the issue done.")
	case "not_met":
		risks = append(risks, "Linked acceptance criteria are not reflected in the run output yet.")
		recommendations = append(recommendations, "Bring the run output, tests, or verification evidence in line with the recorded ticket acceptance criteria.")
	}
	for _, warning := range scopeWarnings {
		risks = append(risks, warning.Message)
		if warning.Kind == "unrelated_change" {
			recommendations = append(recommendations, "Trim unrelated worktree changes or split them into a separate issue/fix record.")
		}
	}

	return &RunSessionInsight{
		WorkspaceID:      workspaceID,
		RunID:            runID,
		IssueID:          run.IssueID,
		Status:           run.Status,
		Headline:         draftSummaryFromExcerpt(excerpt, run.IssueID, run.RunID),
		Summary:          summary,
		GuidanceUsed:     append([]string{}, run.GuidancePaths...),
		Strengths:        dedupeText(strengths),
		Risks:            dedupeText(risks),
		Recommendations:  dedupeText(recommendations),
		AcceptanceReview: acceptanceReview,
		ScopeWarnings:    scopeWarnings,
		GeneratedAt:      nowUTC(),
	}, nil
}

func GetRunMetrics(dataDir string, workspaceID string, runID string) (*RunMetrics, error) {
	if _, err := ReadRun(dataDir, workspaceID, runID); err != nil {
		return nil, err
	}
	metrics, err := loadRunMetrics(dataDir, runID)
	if err != nil {
		return nil, err
	}
	if metrics == nil {
		return nil, fmt.Errorf("no metrics found for run %s", runID)
	}
	return metrics, nil
}

func ListWorkspaceMetrics(dataDir string, workspaceID string) ([]RunMetrics, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	metricsDir := filepath.Join(dataDir, "metrics")
	entries, err := os.ReadDir(metricsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []RunMetrics{}, nil
		}
		return nil, err
	}
	items := []RunMetrics{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := loadRunMetrics(dataDir, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil || record == nil || record.WorkspaceID != workspaceID {
			continue
		}
		items = append(items, *record)
	}
	slices.SortFunc(items, func(a, b RunMetrics) int {
		if a.CalculatedAt > b.CalculatedAt {
			return -1
		}
		if a.CalculatedAt < b.CalculatedAt {
			return 1
		}
		return 0
	})
	return items, nil
}

func GetWorkspaceCostSummary(dataDir string, workspaceID string) (*CostSummary, error) {
	metrics, err := ListWorkspaceMetrics(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	runs, err := ListRuns(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	runsByStatus := map[string]int{}
	costByRuntime := map[string]float64{}
	costByModel := map[string]float64{}
	totalInputTokens := 0
	totalOutputTokens := 0
	totalEstimatedCost := 0.0
	totalDurationMS := 0

	var periodStart *string
	var periodEnd *string
	for _, metric := range metrics {
		totalInputTokens += metric.InputTokens
		totalOutputTokens += metric.OutputTokens
		totalEstimatedCost += metric.EstimatedCost
		totalDurationMS += metric.DurationMS
		costByRuntime[metric.Runtime] += metric.EstimatedCost
		costByModel[metric.Model] += metric.EstimatedCost
		if periodStart == nil || metric.CalculatedAt < *periodStart {
			value := metric.CalculatedAt
			periodStart = &value
		}
		if periodEnd == nil || metric.CalculatedAt > *periodEnd {
			value := metric.CalculatedAt
			periodEnd = &value
		}
	}
	for _, run := range runs {
		runsByStatus[run.Status]++
	}

	return &CostSummary{
		WorkspaceID:        workspaceID,
		TotalRuns:          len(runs),
		TotalInputTokens:   totalInputTokens,
		TotalOutputTokens:  totalOutputTokens,
		TotalEstimatedCost: roundCost(totalEstimatedCost),
		TotalDurationMS:    totalDurationMS,
		RunsByStatus:       runsByStatus,
		CostByRuntime:      costByRuntime,
		CostByModel:        costByModel,
		PeriodStart:        periodStart,
		PeriodEnd:          periodEnd,
	}, nil
}

func GeneratePatchCritique(dataDir string, workspaceID string, runID string) (*PatchCritique, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != "completed" && run.Status != "failed" {
		return nil, fmt.Errorf("cannot critique run in status %s", run.Status)
	}
	outputText := ""
	if strings.TrimSpace(run.OutputPath) != "" {
		if content, err := os.ReadFile(run.OutputPath); err == nil {
			outputText = string(content)
		}
	}
	packet, _ := BuildIssueContextPacket(dataDir, workspaceID, run.IssueID)
	improvements := analyzeRunOutput(run, outputText)
	issuesFound := detectPatchIssues(run, outputText)
	correctness := scoreCorrectness(run, outputText, issuesFound)
	completeness := scoreCompleteness(run, outputText)
	style := scoreStyle(outputText)
	safety := scoreSafety(run, outputText, issuesFound)
	avg := (correctness + completeness + style + safety) / 4
	quality := "poor"
	switch {
	case avg >= 0.85:
		quality = "excellent"
	case avg >= 0.7:
		quality = "good"
	case avg >= 0.5:
		quality = "acceptable"
	case avg >= 0.3:
		quality = "needs_work"
	}
	critique := PatchCritique{
		CritiqueID:       "crit_" + hashID(workspaceID, runID, nowUTC())[:12],
		WorkspaceID:      workspaceID,
		RunID:            runID,
		IssueID:          run.IssueID,
		OverallQuality:   quality,
		Correctness:      roundPercent(correctness),
		Completeness:     roundPercent(completeness),
		Style:            roundPercent(style),
		Safety:           roundPercent(safety),
		IssuesFound:      issuesFound,
		Improvements:     improvements,
		AcceptanceReview: buildAcceptanceReview(packet, run, outputText),
		ScopeWarnings:    buildScopeWarnings(packet, run),
		Summary:          critiqueSummary(quality, issuesFound, improvements),
		GeneratedAt:      nowUTC(),
	}
	if err := savePatchCritique(dataDir, critique); err != nil {
		return nil, err
	}
	if err := appendRunSystemActivity(dataDir, workspaceID, run.IssueID, runID, "critique.generated", "Generated patch critique: "+quality, map[string]any{
		"quality":      quality,
		"issues":       len(issuesFound),
		"improvements": len(improvements),
	}); err != nil {
		return nil, err
	}
	return &critique, nil
}

func GetPatchCritique(dataDir string, workspaceID string, runID string) (*PatchCritique, error) {
	if _, err := ReadRun(dataDir, workspaceID, runID); err != nil {
		return nil, err
	}
	critique, err := loadPatchCritique(dataDir, runID)
	if err != nil {
		return nil, err
	}
	if critique == nil {
		return nil, os.ErrNotExist
	}
	return critique, nil
}

func GetRunImprovements(dataDir string, workspaceID string, runID string) ([]ImprovementSuggestion, error) {
	critique, err := GetPatchCritique(dataDir, workspaceID, runID)
	if err != nil {
		if os.IsNotExist(err) {
			return []ImprovementSuggestion{}, nil
		}
		return nil, err
	}
	items := []ImprovementSuggestion{}
	for _, item := range critique.Improvements {
		if !item.Dismissed {
			items = append(items, item)
		}
	}
	return items, nil
}

func DismissImprovement(dataDir string, workspaceID string, runID string, suggestionID string, request DismissImprovementRequest) (*PatchCritique, error) {
	critique, err := GetPatchCritique(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	found := false
	updatedItems := make([]ImprovementSuggestion, 0, len(critique.Improvements))
	for _, item := range critique.Improvements {
		if item.SuggestionID == suggestionID {
			item.Dismissed = true
			item.DismissedReason = trimOptional(request.Reason)
			found = true
		}
		updatedItems = append(updatedItems, item)
	}
	if !found {
		return nil, os.ErrNotExist
	}
	critique.Improvements = updatedItems
	if err := savePatchCritique(dataDir, *critique); err != nil {
		return nil, err
	}
	return critique, nil
}

func loadRunMetrics(dataDir string, runID string) (*RunMetrics, error) {
	path := filepath.Join(dataDir, "metrics", runID+".json")
	var metrics RunMetrics
	if err := readJSON(path, &metrics); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &metrics, nil
}

func loadPatchCritique(dataDir string, runID string) (*PatchCritique, error) {
	path := filepath.Join(dataDir, "critiques", runID+".json")
	var critique PatchCritique
	if err := readJSON(path, &critique); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &critique, nil
}

func savePatchCritique(dataDir string, critique PatchCritique) error {
	return writeJSON(filepath.Join(dataDir, "critiques", critique.RunID+".json"), critique)
}

func appendRunSystemActivity(dataDir string, workspaceID string, issueID string, runID string, action string, summary string, details map[string]any) error {
	createdAt := nowUTC()
	record := activityRecord{
		ActivityID:  hashID(workspaceID, "run", runID, action, createdAt),
		WorkspaceID: workspaceID,
		EntityType:  "run",
		EntityID:    runID,
		Action:      action,
		Summary:     summary,
		Actor:       systemActor(),
		Details:     details,
		CreatedAt:   createdAt,
	}
	record.IssueID = &issueID
	record.RunID = &runID
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
	if _, err := handle.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}

func analyzeRunOutput(run *runRecord, output string) []ImprovementSuggestion {
	improvements := []ImprovementSuggestion{}
	lines := strings.Split(output, "\n")
	for idx, line := range lines {
		stripped := strings.TrimSpace(line)
		if stripped == "" {
			continue
		}
		for _, pattern := range []string{"TODO", "FIXME", "HACK", "XXX"} {
			if strings.Contains(stripped, pattern) {
				lineStart := idx + 1
				improvements = append(improvements, ImprovementSuggestion{
					SuggestionID: "imp_" + hashID(run.RunID, fmt.Sprintf("%d", idx), pattern)[:8],
					FilePath:     fallbackString(extractFileFromLine(stripped), "unknown"),
					LineStart:    &lineStart,
					Category:     "maintainability",
					Severity:     "low",
					Description:  "Found " + pattern + " comment in generated output",
				})
			}
		}
		lowered := strings.ToLower(stripped)
		if strings.Contains(lowered, "password") || strings.Contains(lowered, "secret") || strings.Contains(lowered, "api_key") || strings.Contains(lowered, "token") {
			if !strings.Contains(lowered, "env(") && !strings.Contains(lowered, "os.environ") && !strings.Contains(lowered, "getenv") {
				lineStart := idx + 1
				improvements = append(improvements, ImprovementSuggestion{
					SuggestionID: "imp_" + hashID(run.RunID, fmt.Sprintf("%d", idx), "secret")[:8],
					FilePath:     fallbackString(extractFileFromLine(stripped), "unknown"),
					LineStart:    &lineStart,
					Category:     "security",
					Severity:     "high",
					Description:  "Possible hardcoded secret or credential",
				})
			}
		}
	}
	if run.Summary != nil {
		toolEvents, _ := intFromAny(run.Summary["tool_event_count"])
		totalEvents, _ := intFromAny(run.Summary["event_count"])
		if totalEvents > 0 && float64(toolEvents)/float64(totalEvents) > 0.8 {
			improvements = append(improvements, ImprovementSuggestion{
				SuggestionID: "imp_" + hashID(run.RunID, "tool_ratio")[:8],
				FilePath:     "general",
				Category:     "performance",
				Severity:     "low",
				Description:  "High ratio of tool events to total events - consider reducing tool calls",
			})
		}
	}
	if len(lines) > 2000 {
		improvements = append(improvements, ImprovementSuggestion{
			SuggestionID: "imp_" + hashID(run.RunID, "large_output")[:8],
			FilePath:     "general",
			Category:     "maintainability",
			Severity:     "medium",
			Description:  "Output is very large - consider breaking into smaller operations",
		})
	}
	if len(improvements) > 20 {
		improvements = improvements[:20]
	}
	return improvements
}

func detectPatchIssues(run *runRecord, output string) []string {
	issues := []string{}
	if run.ExitCode != nil && *run.ExitCode != 0 {
		issues = append(issues, fmt.Sprintf("Run exited with non-zero code: %d", *run.ExitCode))
	}
	if run.Error != nil && strings.TrimSpace(*run.Error) != "" {
		text := strings.TrimSpace(*run.Error)
		if len(text) > 200 {
			text = text[:200]
		}
		issues = append(issues, "Run had error: "+text)
	}
	lower := strings.ToLower(output)
	if strings.Contains(lower, "traceback") {
		issues = append(issues, "Python traceback found in output")
	}
	if strings.Contains(lower, "exception") && !strings.Contains(lower, "caught") {
		issues = append(issues, "Uncaught exception detected in output")
	}
	if strings.Contains(lower, "panic") {
		issues = append(issues, "Panic detected in output")
	}
	if strings.Contains(lower, "segmentation fault") || strings.Contains(lower, "segfault") {
		issues = append(issues, "Segmentation fault detected")
	}
	if strings.TrimSpace(output) == "" {
		issues = append(issues, "Empty output - no changes generated")
	}
	return issues
}

func scoreCorrectness(run *runRecord, output string, issues []string) float64 {
	score := 1.0
	if run.ExitCode != nil && *run.ExitCode != 0 {
		score -= 0.3
	}
	if run.Error != nil && strings.TrimSpace(*run.Error) != "" {
		score -= 0.2
	}
	for _, issue := range issues {
		lower := strings.ToLower(issue)
		if strings.Contains(lower, "traceback") || strings.Contains(lower, "exception") {
			score -= 0.15
		}
		if strings.Contains(lower, "empty output") {
			score -= 0.4
		}
	}
	return maxFloat(0.0, score)
}

func scoreCompleteness(run *runRecord, output string) float64 {
	score := 0.0
	if strings.TrimSpace(output) != "" {
		score += 0.3
	}
	if run.Summary != nil {
		if strings.TrimSpace(runExcerpt(run)) != "" {
			score += 0.3
		}
		toolEvents, _ := intFromAny(run.Summary["tool_event_count"])
		eventCount, _ := intFromAny(run.Summary["event_count"])
		if toolEvents > 0 {
			score += 0.2
		}
		if eventCount > 3 {
			score += 0.2
		}
	}
	return minFloat(1.0, score)
}

func scoreStyle(output string) float64 {
	if strings.TrimSpace(output) == "" {
		return 0.3
	}
	score := 0.7
	lines := strings.Split(output, "\n")
	if len(lines) > 0 {
		totalLen := 0
		for _, line := range lines {
			totalLen += len(line)
		}
		if float64(totalLen)/float64(len(lines)) > 200 {
			score -= 0.1
		}
	}
	veryLong := 0
	for _, line := range lines {
		if len(line) > 500 {
			veryLong++
		}
	}
	if veryLong > 5 {
		score -= 0.15
	}
	return maxFloat(0.0, minFloat(1.0, score))
}

func scoreSafety(run *runRecord, output string, issues []string) float64 {
	score := 1.0
	lower := strings.ToLower(output)
	for _, needle := range []string{"rm -rf", "del /s", "format c:", "drop table", "delete from"} {
		if strings.Contains(lower, needle) {
			score -= 0.4
			break
		}
	}
	for _, needle := range []string{"os.system", "subprocess.call", "exec(", "eval("} {
		if strings.Contains(lower, needle) {
			score -= 0.2
			break
		}
	}
	for _, issue := range issues {
		lowerIssue := strings.ToLower(issue)
		if strings.Contains(lowerIssue, "panic") || strings.Contains(lowerIssue, "segfault") {
			score -= 0.3
		}
	}
	return maxFloat(0.0, score)
}

func critiqueSummary(quality string, issues []string, improvements []ImprovementSuggestion) string {
	parts := []string{"Overall quality: " + quality}
	if len(issues) > 0 {
		parts = append(parts, fmt.Sprintf("Found %d issue(s)", len(issues)))
	}
	high := 0
	for _, item := range improvements {
		if !item.Dismissed && item.Severity == "high" {
			high++
		}
	}
	if high > 0 {
		parts = append(parts, fmt.Sprintf("%d high-severity improvement(s) suggested", high))
	}
	return strings.Join(parts, ". ")
}

func extractFileFromLine(line string) string {
	for _, pattern := range []string{`([\w/.-]+\.\w+):`, `file[:=]\s*([\w/.-]+\.\w+)`, `([\w/.-]+\.\w+)\s*\|`} {
		re := regexp.MustCompile(pattern)
		match := re.FindStringSubmatch(line)
		if len(match) > 1 {
			return match[1]
		}
	}
	return ""
}

func dedupeText(items []string) []string {
	result := []string{}
	seen := map[string]struct{}{}
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func buildAcceptanceReview(packet *IssueContextPacket, run *runRecord, text string) AcceptanceCriteriaReview {
	if packet == nil {
		return AcceptanceCriteriaReview{
			Status:   "unknown",
			Criteria: []string{},
			Notes:    []string{"No issue context packet was available for acceptance review."},
		}
	}
	criteria := []string{}
	for _, item := range packet.TicketContexts[:min(len(packet.TicketContexts), 4)] {
		criteria = append(criteria, item.AcceptanceCriteria[:min(len(item.AcceptanceCriteria), 4)]...)
	}
	criteria = dedupeText(criteria)
	if len(criteria) > 12 {
		criteria = criteria[:12]
	}
	if len(criteria) == 0 {
		return AcceptanceCriteriaReview{
			Status:   "unknown",
			Criteria: []string{},
			Notes:    []string{"No ticket acceptance criteria are linked to this issue."},
		}
	}

	corpusParts := []string{
		run.Title,
		run.Prompt,
		text,
		strings.Join(packet.Issue.TestsPassed, " "),
		firstNonEmptyPtr(packet.Issue.Summary),
		firstNonEmptyPtr(packet.Issue.Impact),
	}
	corpus := strings.ToLower(strings.Join(corpusParts, " "))
	tokenPattern := regexp.MustCompile(`[a-z0-9_./-]{3,}`)
	stopWords := map[string]struct{}{
		"the": {}, "and": {}, "for": {}, "with": {}, "that": {}, "this": {}, "from": {},
	}
	matched := []string{}
	missing := []string{}
	for _, criterion := range criteria {
		tokens := []string{}
		for _, token := range tokenPattern.FindAllString(strings.ToLower(criterion), -1) {
			if _, stop := stopWords[token]; stop {
				continue
			}
			tokens = append(tokens, token)
		}
		if len(tokens) > 6 {
			tokens = tokens[:6]
		}
		matchCount := 0
		for _, token := range tokens {
			if strings.Contains(corpus, token) {
				matchCount++
			}
		}
		threshold := 1
		if len(tokens) >= 2 {
			threshold = 2
		}
		if matchCount >= threshold {
			matched = append(matched, criterion)
		} else {
			missing = append(missing, criterion)
		}
	}
	status := "not_met"
	if len(matched) > 0 && len(missing) == 0 {
		status = "met"
	} else if len(matched) > 0 {
		status = "partial"
	}
	notes := []string{}
	if len(packet.Issue.TestsPassed) > 0 {
		notes = append(notes, fmt.Sprintf("Tracker already records %d passing test command(s).", len(packet.Issue.TestsPassed)))
	}
	if len(packet.Issue.VerificationEvidence) > 0 {
		notes = append(notes, fmt.Sprintf("Tracker already carries %d verification evidence reference(s).", len(packet.Issue.VerificationEvidence)))
	}
	return AcceptanceCriteriaReview{
		Status:   status,
		Criteria: criteria,
		Matched:  matched,
		Missing:  missing,
		Notes:    notes[:min(len(notes), 4)],
	}
}

func buildScopeWarnings(packet *IssueContextPacket, run *runRecord) []ScopeWarning {
	if packet == nil {
		return []ScopeWarning{}
	}
	relatedPaths := append([]string{}, packet.RelatedPaths...)
	if len(relatedPaths) == 0 {
		relatedPaths = append([]string{}, packet.TreeFocus...)
	}
	dirtyPaths := []string{}
	if run.Worktree != nil && len(run.Worktree.DirtyPaths) > 0 {
		dirtyPaths = append([]string{}, run.Worktree.DirtyPaths...)
	}
	warnings := []ScopeWarning{}
	unrelated := []string{}
	for _, path := range dirtyPaths {
		if !matchesRelatedPath(path, relatedPaths) {
			unrelated = append(unrelated, path)
		}
	}
	if len(unrelated) > 0 {
		severity := "medium"
		if len(unrelated) > 3 {
			severity = "high"
		}
		warnings = append(warnings, ScopeWarning{
			Kind:     "unrelated_change",
			Message:  fmt.Sprintf("%d worktree path(s) do not match the issue's ranked focus.", len(unrelated)),
			Paths:    unrelated[:min(len(unrelated), 8)],
			Severity: severity,
		})
	}
	driftFlags := []string{}
	for _, flag := range packet.Issue.DriftFlags {
		if !strings.Contains(strings.ToLower(flag), "review") {
			driftFlags = append(driftFlags, flag)
		}
	}
	if len(driftFlags) > 0 {
		warnings = append(warnings, ScopeWarning{
			Kind:     "scope_drift",
			Message:  "Issue drift flags remain open: " + strings.Join(driftFlags[:min(len(driftFlags), 3)], ", ") + ".",
			Paths:    relatedPaths[:min(len(relatedPaths), 6)],
			Severity: "medium",
		})
	}
	return warnings
}

func matchesRelatedPath(candidate string, relatedPaths []string) bool {
	normalized := strings.TrimSpace(strings.TrimPrefix(filepath.Clean(candidate), "./"))
	for _, item := range relatedPaths {
		focus := strings.TrimSpace(strings.TrimPrefix(filepath.Clean(item), "./"))
		if focus == "" {
			continue
		}
		if normalized == focus || strings.HasPrefix(normalized, focus+"/") || strings.HasPrefix(focus, normalized+"/") {
			return true
		}
	}
	return false
}

func intFromAny(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func roundPercent(value float64) float64 {
	return float64(int(value*1000+0.5)) / 10
}

func roundCost(value float64) float64 {
	return float64(int(value*1_000_000+0.5)) / 1_000_000
}

func maxIntLocal(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a float64, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a float64, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

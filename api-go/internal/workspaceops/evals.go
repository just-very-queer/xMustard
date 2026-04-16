package workspaceops

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type EvalScenarioUpsertRequest struct {
	ScenarioID             *string  `json:"scenario_id"`
	Name                   string   `json:"name"`
	IssueID                string   `json:"issue_id"`
	Description            *string  `json:"description"`
	BaselineReplayID       *string  `json:"baseline_replay_id"`
	GuidancePaths          []string `json:"guidance_paths"`
	TicketContextIDs       []string `json:"ticket_context_ids"`
	VerificationProfileIDs []string `json:"verification_profile_ids"`
	RunIDs                 []string `json:"run_ids"`
	BrowserDumpIDs         []string `json:"browser_dump_ids"`
	Notes                  *string  `json:"notes"`
}

type EvalScenarioRecord struct {
	ScenarioID             string   `json:"scenario_id"`
	WorkspaceID            string   `json:"workspace_id"`
	IssueID                string   `json:"issue_id"`
	Name                   string   `json:"name"`
	Description            *string  `json:"description,omitempty"`
	BaselineReplayID       *string  `json:"baseline_replay_id,omitempty"`
	GuidancePaths          []string `json:"guidance_paths"`
	TicketContextIDs       []string `json:"ticket_context_ids"`
	VerificationProfileIDs []string `json:"verification_profile_ids"`
	RunIDs                 []string `json:"run_ids"`
	BrowserDumpIDs         []string `json:"browser_dump_ids"`
	Notes                  *string  `json:"notes,omitempty"`
	CreatedAt              string   `json:"created_at"`
	UpdatedAt              string   `json:"updated_at"`
}

type EvalReplayBatchRecord struct {
	BatchID      string   `json:"batch_id"`
	WorkspaceID  string   `json:"workspace_id"`
	IssueID      string   `json:"issue_id"`
	Runtime      string   `json:"runtime"`
	Model        string   `json:"model"`
	ScenarioIDs  []string `json:"scenario_ids"`
	QueuedRunIDs []string `json:"queued_run_ids"`
	Instruction  *string  `json:"instruction,omitempty"`
	RunbookID    *string  `json:"runbook_id,omitempty"`
	Planning     bool     `json:"planning"`
	CreatedAt    string   `json:"created_at"`
}

type EvalScenarioVariantDiff struct {
	SelectedGuidancePaths    []string `json:"selected_guidance_paths"`
	CurrentGuidancePaths     []string `json:"current_guidance_paths"`
	AddedGuidancePaths       []string `json:"added_guidance_paths"`
	RemovedGuidancePaths     []string `json:"removed_guidance_paths"`
	SelectedTicketContextIDs []string `json:"selected_ticket_context_ids"`
	CurrentTicketContextIDs  []string `json:"current_ticket_context_ids"`
	AddedTicketContextIDs    []string `json:"added_ticket_context_ids"`
	RemovedTicketContextIDs  []string `json:"removed_ticket_context_ids"`
	Changed                  bool     `json:"changed"`
	Summary                  string   `json:"summary"`
}

type EvalScenarioReport struct {
	Scenario                   EvalScenarioRecord              `json:"scenario"`
	BaselineReplay             *IssueContextReplayRecord       `json:"baseline_replay,omitempty"`
	LatestReplayComparison     *IssueContextReplayComparison   `json:"latest_replay_comparison,omitempty"`
	VariantDiff                *EvalScenarioVariantDiff        `json:"variant_diff,omitempty"`
	ComparisonToBaseline       *EvalScenarioBaselineComparison `json:"comparison_to_baseline,omitempty"`
	LatestFreshRun             *EvalFreshRunSummary            `json:"latest_fresh_run,omitempty"`
	FreshComparisonToBaseline  *EvalFreshExecutionComparison   `json:"fresh_comparison_to_baseline,omitempty"`
	VerificationProfileReports []VerificationProfileReport     `json:"verification_profile_reports"`
	RunMetrics                 []RunMetrics                    `json:"run_metrics"`
	TotalEstimatedCost         float64                         `json:"total_estimated_cost"`
	AvgDurationMS              int                             `json:"avg_duration_ms"`
	SuccessRuns                int                             `json:"success_runs"`
	FailedRuns                 int                             `json:"failed_runs"`
	VerificationSuccessRate    float64                         `json:"verification_success_rate"`
	Summary                    string                          `json:"summary"`
}

type EvalScenarioBaselineComparison struct {
	ComparedToScenarioID              string                                 `json:"compared_to_scenario_id"`
	ComparedToName                    string                                 `json:"compared_to_name"`
	GuidanceOnlyInScenario            []string                               `json:"guidance_only_in_scenario"`
	GuidanceOnlyInBaseline            []string                               `json:"guidance_only_in_baseline"`
	TicketContextOnlyInScenario       []string                               `json:"ticket_context_only_in_scenario"`
	TicketContextOnlyInBaseline       []string                               `json:"ticket_context_only_in_baseline"`
	BrowserDumpOnlyInScenario         []string                               `json:"browser_dump_only_in_scenario"`
	BrowserDumpOnlyInBaseline         []string                               `json:"browser_dump_only_in_baseline"`
	VerificationProfileOnlyInScenario []string                               `json:"verification_profile_only_in_scenario"`
	VerificationProfileOnlyInBaseline []string                               `json:"verification_profile_only_in_baseline"`
	VerificationProfileDeltas         []EvalScenarioVerificationProfileDelta `json:"verification_profile_deltas"`
	SuccessRunsDelta                  int                                    `json:"success_runs_delta"`
	FailedRunsDelta                   int                                    `json:"failed_runs_delta"`
	VerificationSuccessRateDelta      float64                                `json:"verification_success_rate_delta"`
	AvgDurationMSDelta                int                                    `json:"avg_duration_ms_delta"`
	TotalEstimatedCostDelta           float64                                `json:"total_estimated_cost_delta"`
	Preferred                         string                                 `json:"preferred"`
	PreferredScenarioID               *string                                `json:"preferred_scenario_id,omitempty"`
	PreferredScenarioName             *string                                `json:"preferred_scenario_name,omitempty"`
	PreferenceReasons                 []string                               `json:"preference_reasons"`
	Summary                           string                                 `json:"summary"`
}

type EvalScenarioVerificationProfileDelta struct {
	ProfileID                 string         `json:"profile_id"`
	ProfileName               string         `json:"profile_name"`
	PresentInScenario         bool           `json:"present_in_scenario"`
	PresentInBaseline         bool           `json:"present_in_baseline"`
	ScenarioTotalRuns         int            `json:"scenario_total_runs"`
	BaselineTotalRuns         int            `json:"baseline_total_runs"`
	TotalRunsDelta            int            `json:"total_runs_delta"`
	ScenarioSuccessRate       float64        `json:"scenario_success_rate"`
	BaselineSuccessRate       float64        `json:"baseline_success_rate"`
	SuccessRateDelta          float64        `json:"success_rate_delta"`
	ScenarioChecklistPassRate float64        `json:"scenario_checklist_pass_rate"`
	BaselineChecklistPassRate float64        `json:"baseline_checklist_pass_rate"`
	ChecklistPassRateDelta    float64        `json:"checklist_pass_rate_delta"`
	ScenarioAvgAttemptCount   float64        `json:"scenario_avg_attempt_count"`
	BaselineAvgAttemptCount   float64        `json:"baseline_avg_attempt_count"`
	AvgAttemptCountDelta      float64        `json:"avg_attempt_count_delta"`
	ScenarioConfidenceCounts  map[string]int `json:"scenario_confidence_counts"`
	BaselineConfidenceCounts  map[string]int `json:"baseline_confidence_counts"`
	Preferred                 string         `json:"preferred"`
	Summary                   string         `json:"summary"`
}

type EvalFreshRunSummary struct {
	ScenarioID     string  `json:"scenario_id"`
	ScenarioName   string  `json:"scenario_name"`
	RunID          string  `json:"run_id"`
	Status         string  `json:"status"`
	Runtime        string  `json:"runtime"`
	Model          string  `json:"model"`
	CreatedAt      string  `json:"created_at"`
	EstimatedCost  float64 `json:"estimated_cost"`
	DurationMS     int     `json:"duration_ms"`
	CommandPreview *string `json:"command_preview,omitempty"`
	Planning       bool    `json:"planning"`
}

type EvalFreshExecutionComparison struct {
	ComparedToScenarioID  string   `json:"compared_to_scenario_id"`
	ComparedToName        string   `json:"compared_to_name"`
	ScenarioStatus        string   `json:"scenario_status"`
	BaselineStatus        string   `json:"baseline_status"`
	EstimatedCostDelta    float64  `json:"estimated_cost_delta"`
	DurationMSDelta       int      `json:"duration_ms_delta"`
	Preferred             string   `json:"preferred"`
	PreferredScenarioID   *string  `json:"preferred_scenario_id,omitempty"`
	PreferredScenarioName *string  `json:"preferred_scenario_name,omitempty"`
	PreferenceReasons     []string `json:"preference_reasons"`
	Summary               string   `json:"summary"`
}

type EvalFreshReplayRankingEntry struct {
	Rank              int                 `json:"rank"`
	ScenarioID        string              `json:"scenario_id"`
	ScenarioName      string              `json:"scenario_name"`
	LatestFreshRun    EvalFreshRunSummary `json:"latest_fresh_run"`
	PairwiseWins      int                 `json:"pairwise_wins"`
	PairwiseLosses    int                 `json:"pairwise_losses"`
	PairwiseTies      int                 `json:"pairwise_ties"`
	PreferenceReasons []string            `json:"preference_reasons"`
	Summary           string              `json:"summary"`
}

type EvalFreshReplayRanking struct {
	IssueID              string                        `json:"issue_id"`
	BaselineScenarioID   *string                       `json:"baseline_scenario_id,omitempty"`
	BaselineScenarioName *string                       `json:"baseline_scenario_name,omitempty"`
	RankedScenarios      []EvalFreshReplayRankingEntry `json:"ranked_scenarios"`
	Summary              string                        `json:"summary"`
}

type EvalFreshReplayTrendEntry struct {
	ScenarioID       string               `json:"scenario_id"`
	ScenarioName     string               `json:"scenario_name"`
	CurrentRank      int                  `json:"current_rank"`
	PreviousRank     *int                 `json:"previous_rank,omitempty"`
	Movement         string               `json:"movement"`
	LatestFreshRun   EvalFreshRunSummary  `json:"latest_fresh_run"`
	PreviousFreshRun *EvalFreshRunSummary `json:"previous_fresh_run,omitempty"`
	Summary          string               `json:"summary"`
}

type EvalFreshReplayTrend struct {
	IssueID         string                      `json:"issue_id"`
	LatestBatchID   *string                     `json:"latest_batch_id,omitempty"`
	PreviousBatchID *string                     `json:"previous_batch_id,omitempty"`
	Entries         []EvalFreshReplayTrendEntry `json:"entries"`
	Summary         string                      `json:"summary"`
}

type EvalWorkspaceReport struct {
	WorkspaceID                 string                   `json:"workspace_id"`
	ScenarioCount               int                      `json:"scenario_count"`
	RunCount                    int                      `json:"run_count"`
	SuccessRuns                 int                      `json:"success_runs"`
	FailedRuns                  int                      `json:"failed_runs"`
	TotalEstimatedCost          float64                  `json:"total_estimated_cost"`
	TotalDurationMS             int                      `json:"total_duration_ms"`
	VerificationSuccessRate     float64                  `json:"verification_success_rate"`
	CostSummary                 *CostSummary             `json:"cost_summary,omitempty"`
	ScenarioReports             []EvalScenarioReport     `json:"scenario_reports"`
	ReplayBatches               []EvalReplayBatchRecord  `json:"replay_batches"`
	FreshReplayRankings         []EvalFreshReplayRanking `json:"fresh_replay_rankings"`
	FreshReplayTrends           []EvalFreshReplayTrend   `json:"fresh_replay_trends"`
	GuidanceVariantRollups      []EvalVariantRollup      `json:"guidance_variant_rollups"`
	TicketContextVariantRollups []EvalVariantRollup      `json:"ticket_context_variant_rollups"`
	GeneratedAt                 string                   `json:"generated_at"`
}

type EvalVariantRollup struct {
	VariantKind             string             `json:"variant_kind"`
	VariantKey              string             `json:"variant_key"`
	Label                   string             `json:"label"`
	SelectedValues          []string           `json:"selected_values"`
	ScenarioIDs             []string           `json:"scenario_ids"`
	ScenarioNames           []string           `json:"scenario_names"`
	ScenarioCount           int                `json:"scenario_count"`
	RunCount                int                `json:"run_count"`
	SuccessRuns             int                `json:"success_runs"`
	FailedRuns              int                `json:"failed_runs"`
	TotalEstimatedCost      float64            `json:"total_estimated_cost"`
	AvgDurationMS           int                `json:"avg_duration_ms"`
	VerificationSuccessRate float64            `json:"verification_success_rate"`
	RuntimeBreakdown        []DimensionSummary `json:"runtime_breakdown"`
	ModelBreakdown          []DimensionSummary `json:"model_breakdown"`
	Summary                 string             `json:"summary"`
}

type EvalScenarioReplayRequest struct {
	Runtime     string   `json:"runtime"`
	Model       string   `json:"model"`
	ScenarioIDs []string `json:"scenario_ids"`
	Instruction *string  `json:"instruction"`
	RunbookID   *string  `json:"runbook_id"`
	Planning    bool     `json:"planning"`
}

type EvalScenarioReplayResult struct {
	WorkspaceID string      `json:"workspace_id"`
	IssueID     string      `json:"issue_id"`
	Runtime     string      `json:"runtime"`
	Model       string      `json:"model"`
	BatchID     *string     `json:"batch_id,omitempty"`
	ScenarioIDs []string    `json:"scenario_ids"`
	QueuedRuns  []runRecord `json:"queued_runs"`
}

func evalScenariosPath(dataDir string, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "eval_scenarios.json")
}

func evalReplayBatchesPath(dataDir string, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "eval_replay_batches.json")
}

func ListEvalScenarios(dataDir string, workspaceID string, issueID string) ([]EvalScenarioRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	items, err := loadEvalScenarios(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(issueID) == "" {
		return items, nil
	}
	filtered := make([]EvalScenarioRecord, 0, len(items))
	for _, item := range items {
		if item.IssueID == issueID {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func SaveEvalScenario(dataDir string, workspaceID string, request EvalScenarioUpsertRequest) (*EvalScenarioRecord, error) {
	issueID := strings.TrimSpace(request.IssueID)
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	items, err := loadEvalScenarios(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	scenarioID := firstNonEmptyPtr(request.ScenarioID)
	if scenarioID == "" {
		scenarioID = slugProfileID("eval-" + strings.TrimSpace(request.Name))
	}
	var previous *EvalScenarioRecord
	for idx := range items {
		if items[idx].ScenarioID == scenarioID {
			previous = &items[idx]
			break
		}
	}
	now := nowUTC()
	record := EvalScenarioRecord{
		ScenarioID:             scenarioID,
		WorkspaceID:            workspaceID,
		IssueID:                issueID,
		Name:                   strings.TrimSpace(request.Name),
		Description:            trimOptional(request.Description),
		BaselineReplayID:       trimOptional(request.BaselineReplayID),
		GuidancePaths:          trimStringList(request.GuidancePaths, 12),
		TicketContextIDs:       trimStringList(request.TicketContextIDs, 12),
		VerificationProfileIDs: trimStringList(request.VerificationProfileIDs, 12),
		RunIDs:                 trimStringList(request.RunIDs, 24),
		BrowserDumpIDs:         trimStringList(request.BrowserDumpIDs, 12),
		Notes:                  trimOptional(request.Notes),
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if previous != nil {
		record.CreatedAt = previous.CreatedAt
	}
	next := make([]EvalScenarioRecord, 0, len(items))
	for _, item := range items {
		if item.ScenarioID != scenarioID {
			next = append(next, item)
		}
	}
	next = append(next, record)
	if err := saveEvalScenarios(dataDir, workspaceID, next); err != nil {
		return nil, err
	}
	if err := appendIssueActivity(
		dataDir,
		workspaceID,
		issueID,
		"",
		"eval_scenario.saved",
		"Saved eval scenario "+record.Name,
		map[string]any{
			"scenario_id":        record.ScenarioID,
			"baseline_replay_id": record.BaselineReplayID,
		},
	); err != nil {
		return nil, err
	}
	return &record, nil
}

func DeleteEvalScenario(dataDir string, workspaceID string, scenarioID string) error {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return err
	}
	items, err := loadEvalScenarios(dataDir, workspaceID)
	if err != nil {
		return err
	}
	var current *EvalScenarioRecord
	next := make([]EvalScenarioRecord, 0, len(items))
	for idx := range items {
		if items[idx].ScenarioID == scenarioID {
			current = &items[idx]
			continue
		}
		next = append(next, items[idx])
	}
	if current == nil {
		return os.ErrNotExist
	}
	if err := saveEvalScenarios(dataDir, workspaceID, next); err != nil {
		return err
	}
	return appendIssueActivity(
		dataDir,
		workspaceID,
		current.IssueID,
		"",
		"eval_scenario.deleted",
		"Deleted eval scenario "+current.Name,
		map[string]any{"scenario_id": scenarioID},
	)
}

func ReplayEvalScenarios(
	dataDir string,
	workspaceID string,
	issueID string,
	request EvalScenarioReplayRequest,
) (*EvalScenarioReplayResult, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	scenarios, err := ListEvalScenarios(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	if len(request.ScenarioIDs) > 0 {
		allowed := map[string]struct{}{}
		for _, scenarioID := range request.ScenarioIDs {
			allowed[strings.TrimSpace(scenarioID)] = struct{}{}
		}
		filtered := make([]EvalScenarioRecord, 0, len(scenarios))
		for _, item := range scenarios {
			if _, ok := allowed[item.ScenarioID]; ok {
				filtered = append(filtered, item)
			}
		}
		scenarios = filtered
	}
	if len(scenarios) == 0 {
		return nil, os.ErrNotExist
	}
	batchID := "evalbatch_" + hashID(workspaceID, issueID, nowUTC())[:12]
	queuedRuns := make([]runRecord, 0, len(scenarios))
	for _, scenario := range scenarios {
		run, err := StartIssueRun(dataDir, workspaceID, issueID, RunRequest{
			Runtime:           request.Runtime,
			Model:             request.Model,
			Instruction:       request.Instruction,
			RunbookID:         request.RunbookID,
			EvalScenarioID:    &scenario.ScenarioID,
			EvalReplayBatchID: ptr(batchID),
			Planning:          request.Planning,
		})
		if err != nil {
			return nil, err
		}
		queuedRuns = append(queuedRuns, *run)
	}
	scenarioIDs := make([]string, 0, len(scenarios))
	for _, item := range scenarios {
		scenarioIDs = append(scenarioIDs, item.ScenarioID)
	}
	replayBatches, err := loadEvalReplayBatches(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	queuedRunIDs := make([]string, 0, len(queuedRuns))
	for _, item := range queuedRuns {
		queuedRunIDs = append(queuedRunIDs, item.RunID)
	}
	replayBatches = append(replayBatches, EvalReplayBatchRecord{
		BatchID:      batchID,
		WorkspaceID:  workspaceID,
		IssueID:      issueID,
		Runtime:      request.Runtime,
		Model:        request.Model,
		ScenarioIDs:  scenarioIDs,
		QueuedRunIDs: queuedRunIDs,
		Instruction:  trimOptional(request.Instruction),
		RunbookID:    trimOptional(request.RunbookID),
		Planning:     request.Planning,
		CreatedAt:    nowUTC(),
	})
	if err := saveEvalReplayBatches(dataDir, workspaceID, replayBatches); err != nil {
		return nil, err
	}
	return &EvalScenarioReplayResult{
		WorkspaceID: workspaceID,
		IssueID:     issueID,
		Runtime:     request.Runtime,
		Model:       request.Model,
		BatchID:     ptr(batchID),
		ScenarioIDs: scenarioIDs,
		QueuedRuns:  queuedRuns,
	}, nil
}

func GetEvalReport(dataDir string, workspaceID string, scenarioID string) (*EvalWorkspaceReport, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	scenarios, err := ListEvalScenarios(dataDir, workspaceID, "")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(scenarioID) != "" {
		filtered := make([]EvalScenarioRecord, 0, 1)
		for _, item := range scenarios {
			if item.ScenarioID == scenarioID {
				filtered = append(filtered, item)
				break
			}
		}
		if len(filtered) == 0 {
			return nil, os.ErrNotExist
		}
		scenarios = filtered
	}

	scenarioReports := make([]EvalScenarioReport, 0, len(scenarios))
	runCount := 0
	successRuns := 0
	failedRuns := 0
	totalEstimatedCost := 0.0
	totalDurationMS := 0
	profileRuns := 0
	successfulProfileRuns := 0
	for _, scenario := range scenarios {
		report, err := buildEvalScenarioReport(dataDir, workspaceID, scenario)
		if err != nil {
			return nil, err
		}
		scenarioReports = append(scenarioReports, *report)
		runCount += len(report.RunMetrics)
		successRuns += report.SuccessRuns
		failedRuns += report.FailedRuns
		totalEstimatedCost += report.TotalEstimatedCost
		for _, metric := range report.RunMetrics {
			totalDurationMS += metric.DurationMS
		}
		for _, profile := range report.VerificationProfileReports {
			profileRuns += profile.TotalRuns
			successfulProfileRuns += profile.SuccessRuns
		}
	}
	scenarioReports, err = attachEvalBaselineComparisons(dataDir, workspaceID, scenarioReports)
	if err != nil {
		return nil, err
	}
	scenarioReports = attachEvalFreshComparisons(scenarioReports)
	costSummary, err := GetWorkspaceCostSummary(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	guidanceVariantRollups, err := buildEvalVariantRollups(dataDir, workspaceID, scenarios, "guidance")
	if err != nil {
		return nil, err
	}
	ticketVariantRollups, err := buildEvalVariantRollups(dataDir, workspaceID, scenarios, "ticket_context")
	if err != nil {
		return nil, err
	}
	replayBatches, err := loadEvalReplayBatches(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	freshReplayRankings := buildEvalFreshReplayRankings(scenarioReports)
	return &EvalWorkspaceReport{
		WorkspaceID:                 workspaceID,
		ScenarioCount:               len(scenarioReports),
		RunCount:                    runCount,
		SuccessRuns:                 successRuns,
		FailedRuns:                  failedRuns,
		TotalEstimatedCost:          roundEvalCost(totalEstimatedCost),
		TotalDurationMS:             totalDurationMS,
		VerificationSuccessRate:     evalVerificationSuccessRate(successfulProfileRuns, profileRuns),
		CostSummary:                 costSummary,
		ScenarioReports:             scenarioReports,
		ReplayBatches:               replayBatches,
		FreshReplayRankings:         freshReplayRankings,
		FreshReplayTrends:           buildEvalFreshReplayTrends(dataDir, workspaceID, scenarios, scenarioReports, freshReplayRankings, replayBatches),
		GuidanceVariantRollups:      guidanceVariantRollups,
		TicketContextVariantRollups: ticketVariantRollups,
		GeneratedAt:                 nowUTC(),
	}, nil
}

func buildEvalScenarioReport(dataDir string, workspaceID string, scenario EvalScenarioRecord) (*EvalScenarioReport, error) {
	replays, err := ListIssueContextReplays(dataDir, workspaceID, scenario.IssueID)
	if err != nil {
		return nil, err
	}
	var baselineReplay *IssueContextReplayRecord
	baselineID := firstNonEmptyPtr(scenario.BaselineReplayID)
	for idx := range replays {
		if replays[idx].ReplayID == baselineID {
			baselineReplay = &replays[idx]
			break
		}
	}
	var latestReplayComparison *IssueContextReplayComparison
	if baselineReplay != nil {
		latestReplayComparison, err = CompareIssueContextReplay(dataDir, workspaceID, scenario.IssueID, baselineReplay.ReplayID)
		if err != nil {
			return nil, err
		}
	}
	packet, err := BuildIssueContextPacket(dataDir, workspaceID, scenario.IssueID)
	if err != nil {
		return nil, err
	}
	variantDiff := buildEvalVariantDiff(scenario, packet)

	verificationReports, err := ListVerificationProfileReports(dataDir, workspaceID, scenario.IssueID)
	if err != nil {
		return nil, err
	}
	if len(scenario.VerificationProfileIDs) > 0 {
		allowed := map[string]struct{}{}
		for _, profileID := range scenario.VerificationProfileIDs {
			allowed[profileID] = struct{}{}
		}
		filtered := make([]VerificationProfileReport, 0, len(verificationReports))
		for _, item := range verificationReports {
			if _, ok := allowed[item.ProfileID]; ok {
				filtered = append(filtered, item)
			}
		}
		verificationReports = filtered
	}

	selectedRuns := selectEvalRuns(dataDir, workspaceID, scenario)
	runMetrics := loadEvalRunMetrics(dataDir, selectedRuns)
	latestFreshRun := buildEvalFreshRunSummary(scenario, selectedRuns, runMetrics)
	successRuns := 0
	failedRuns := 0
	for _, run := range selectedRuns {
		if run.Status == "completed" {
			successRuns++
		} else if run.Status == "failed" {
			failedRuns++
		}
	}
	totalEstimatedCost := 0.0
	totalDuration := 0
	for _, metric := range runMetrics {
		totalEstimatedCost += metric.EstimatedCost
		totalDuration += metric.DurationMS
	}
	avgDuration := 0
	if len(runMetrics) > 0 {
		avgDuration = totalDuration / len(runMetrics)
	}
	verificationTotal := 0
	verificationSuccess := 0
	for _, item := range verificationReports {
		verificationTotal += item.TotalRuns
		verificationSuccess += item.SuccessRuns
	}
	summaryParts := []string{
		intString(successRuns) + " successful run(s)",
		intString(failedRuns) + " failed run(s)",
		intString(len(verificationReports)) + " verification profile report(s)",
	}
	if latestReplayComparison != nil && strings.TrimSpace(latestReplayComparison.Summary) != "" {
		summaryParts = append(summaryParts, latestReplayComparison.Summary)
	}
	if variantDiff != nil && strings.TrimSpace(variantDiff.Summary) != "" {
		summaryParts = append(summaryParts, variantDiff.Summary)
	}
	return &EvalScenarioReport{
		Scenario:                   scenario,
		BaselineReplay:             baselineReplay,
		LatestReplayComparison:     latestReplayComparison,
		VariantDiff:                variantDiff,
		VerificationProfileReports: verificationReports,
		LatestFreshRun:             latestFreshRun,
		RunMetrics:                 runMetrics,
		TotalEstimatedCost:         roundEvalCost(totalEstimatedCost),
		AvgDurationMS:              avgDuration,
		SuccessRuns:                successRuns,
		FailedRuns:                 failedRuns,
		VerificationSuccessRate:    evalVerificationSuccessRate(verificationSuccess, verificationTotal),
		Summary:                    strings.Join(summaryParts, "; "),
	}, nil
}

func attachEvalBaselineComparisons(dataDir string, workspaceID string, reports []EvalScenarioReport) ([]EvalScenarioReport, error) {
	baselines := map[string]EvalScenarioReport{}
	for _, report := range reports {
		current, ok := baselines[report.Scenario.IssueID]
		if !ok || evalBaselineSortKey(report) < evalBaselineSortKey(current) {
			baselines[report.Scenario.IssueID] = report
		}
	}
	result := make([]EvalScenarioReport, 0, len(reports))
	for _, report := range reports {
		comparison, err := buildEvalScenarioBaselineComparison(dataDir, workspaceID, report, baselines[report.Scenario.IssueID])
		if err != nil {
			return nil, err
		}
		report.ComparisonToBaseline = comparison
		result = append(result, report)
	}
	return result, nil
}

func attachEvalFreshComparisons(reports []EvalScenarioReport) []EvalScenarioReport {
	baselines := map[string]EvalScenarioReport{}
	for _, report := range reports {
		current, ok := baselines[report.Scenario.IssueID]
		if !ok || evalBaselineSortKey(report) < evalBaselineSortKey(current) {
			baselines[report.Scenario.IssueID] = report
		}
	}
	result := make([]EvalScenarioReport, 0, len(reports))
	for _, report := range reports {
		report.FreshComparisonToBaseline = buildEvalFreshExecutionComparison(report, baselines[report.Scenario.IssueID])
		result = append(result, report)
	}
	return result
}

func evalBaselineSortKey(report EvalScenarioReport) string {
	prefix := "1"
	if report.Scenario.BaselineReplayID != nil && strings.TrimSpace(*report.Scenario.BaselineReplayID) != "" {
		prefix = "0"
	}
	return prefix + "|" + report.Scenario.CreatedAt + "|" + report.Scenario.ScenarioID
}

func evalFreshStatusRank(status string) int {
	switch status {
	case "completed":
		return 4
	case "running":
		return 3
	case "queued":
		return 2
	case "planning":
		return 1
	case "failed":
		return 0
	case "cancelled":
		return -1
	default:
		return -1
	}
}

func buildEvalFreshRunSummary(
	scenario EvalScenarioRecord,
	runs []runRecord,
	metrics []RunMetrics,
) *EvalFreshRunSummary {
	summaries := buildEvalFreshRunSummaries(scenario, runs, metrics)
	if len(summaries) == 0 {
		return nil
	}
	return &summaries[0]
}

func buildEvalFreshRunSummaries(
	scenario EvalScenarioRecord,
	runs []runRecord,
	metrics []RunMetrics,
) []EvalFreshRunSummary {
	if len(runs) == 0 {
		return []EvalFreshRunSummary{}
	}
	metricLookup := map[string]RunMetrics{}
	for _, item := range metrics {
		metricLookup[item.RunID] = item
	}
	orderedRuns := slices.Clone(runs)
	slices.SortFunc(orderedRuns, func(left runRecord, right runRecord) int {
		if left.CreatedAt != right.CreatedAt {
			return strings.Compare(right.CreatedAt, left.CreatedAt)
		}
		return strings.Compare(right.RunID, left.RunID)
	})
	summaries := make([]EvalFreshRunSummary, 0, len(orderedRuns))
	for _, run := range orderedRuns {
		metric, hasMetric := metricLookup[run.RunID]
		var commandPreview *string
		if strings.TrimSpace(run.CommandPreview) != "" {
			commandPreview = optionalString(run.CommandPreview)
		}
		summaries = append(summaries, EvalFreshRunSummary{
			ScenarioID:     scenario.ScenarioID,
			ScenarioName:   scenario.Name,
			RunID:          run.RunID,
			Status:         run.Status,
			Runtime:        run.Runtime,
			Model:          run.Model,
			CreatedAt:      run.CreatedAt,
			EstimatedCost:  map[bool]float64{true: metric.EstimatedCost, false: 0.0}[hasMetric],
			DurationMS:     map[bool]int{true: metric.DurationMS, false: 0}[hasMetric],
			CommandPreview: commandPreview,
			Planning:       run.Status == "planning",
		})
	}
	return summaries
}

func buildEvalFreshExecutionComparison(
	report EvalScenarioReport,
	baseline EvalScenarioReport,
) *EvalFreshExecutionComparison {
	if baseline.Scenario.ScenarioID == "" || baseline.Scenario.ScenarioID == report.Scenario.ScenarioID {
		return nil
	}
	if report.LatestFreshRun == nil || baseline.LatestFreshRun == nil {
		return nil
	}
	scenarioRank := evalFreshStatusRank(report.LatestFreshRun.Status)
	baselineRank := evalFreshStatusRank(baseline.LatestFreshRun.Status)
	costDelta := roundEvalCost(report.LatestFreshRun.EstimatedCost - baseline.LatestFreshRun.EstimatedCost)
	durationDelta := report.LatestFreshRun.DurationMS - baseline.LatestFreshRun.DurationMS

	preferred := "tie"
	var preferredScenarioID *string
	var preferredScenarioName *string
	preferenceReasons := []string{}

	if scenarioRank > baselineRank {
		preferred = "scenario"
		preferredScenarioID = optionalString(report.Scenario.ScenarioID)
		preferredScenarioName = optionalString(report.Scenario.Name)
		preferenceReasons = append(preferenceReasons, "better fresh run status")
	} else if scenarioRank < baselineRank {
		preferred = "baseline"
		preferredScenarioID = optionalString(baseline.Scenario.ScenarioID)
		preferredScenarioName = optionalString(baseline.Scenario.Name)
		preferenceReasons = append(preferenceReasons, "better fresh run status")
	} else {
		if costDelta < 0 {
			preferred = "scenario"
			preferredScenarioID = optionalString(report.Scenario.ScenarioID)
			preferredScenarioName = optionalString(report.Scenario.Name)
			preferenceReasons = append(preferenceReasons, "lower fresh run cost")
		} else if costDelta > 0 {
			preferred = "baseline"
			preferredScenarioID = optionalString(baseline.Scenario.ScenarioID)
			preferredScenarioName = optionalString(baseline.Scenario.Name)
			preferenceReasons = append(preferenceReasons, "lower fresh run cost")
		}
		if durationDelta < 0 {
			if preferred == "tie" {
				preferred = "scenario"
				preferredScenarioID = optionalString(report.Scenario.ScenarioID)
				preferredScenarioName = optionalString(report.Scenario.Name)
			}
			preferenceReasons = append(preferenceReasons, "faster fresh run")
		} else if durationDelta > 0 {
			if preferred == "tie" {
				preferred = "baseline"
				preferredScenarioID = optionalString(baseline.Scenario.ScenarioID)
				preferredScenarioName = optionalString(baseline.Scenario.Name)
			}
			if preferred == "baseline" {
				preferenceReasons = append(preferenceReasons, "faster fresh run")
			}
		}
	}

	summaryParts := []string{
		"Latest fresh run vs " + baseline.Scenario.Name,
		"status " + report.LatestFreshRun.Status + " vs " + baseline.LatestFreshRun.Status,
	}
	if costDelta != 0 {
		summaryParts = append(summaryParts, signedFloatString(costDelta, 4)+" cost")
	}
	if durationDelta != 0 {
		summaryParts = append(summaryParts, signedIntString(durationDelta)+"ms duration")
	}
	if preferredScenarioName != nil {
		summaryParts = append(summaryParts, "preferred: "+*preferredScenarioName)
	}

	return &EvalFreshExecutionComparison{
		ComparedToScenarioID:  baseline.Scenario.ScenarioID,
		ComparedToName:        baseline.Scenario.Name,
		ScenarioStatus:        report.LatestFreshRun.Status,
		BaselineStatus:        baseline.LatestFreshRun.Status,
		EstimatedCostDelta:    costDelta,
		DurationMSDelta:       durationDelta,
		Preferred:             preferred,
		PreferredScenarioID:   preferredScenarioID,
		PreferredScenarioName: preferredScenarioName,
		PreferenceReasons:     preferenceReasons,
		Summary:               strings.Join(summaryParts, "; "),
	}
}

func buildEvalFreshReplayRankings(reports []EvalScenarioReport) []EvalFreshReplayRanking {
	grouped := map[string][]EvalScenarioReport{}
	for _, report := range reports {
		if report.LatestFreshRun == nil {
			continue
		}
		grouped[report.Scenario.IssueID] = append(grouped[report.Scenario.IssueID], report)
	}
	issueIDs := make([]string, 0, len(grouped))
	for issueID := range grouped {
		issueIDs = append(issueIDs, issueID)
	}
	slices.Sort(issueIDs)
	rankings := make([]EvalFreshReplayRanking, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		freshReports := grouped[issueID]
		if len(freshReports) < 2 {
			continue
		}
		baseline := freshReports[0]
		for _, report := range freshReports[1:] {
			if evalBaselineSortKey(report) < evalBaselineSortKey(baseline) {
				baseline = report
			}
		}
		type scoredEntry struct {
			report  EvalScenarioReport
			wins    int
			losses  int
			ties    int
			reasons []string
		}
		scored := make([]scoredEntry, 0, len(freshReports))
		for _, report := range freshReports {
			entry := scoredEntry{report: report}
			for _, other := range freshReports {
				if other.Scenario.ScenarioID == report.Scenario.ScenarioID {
					continue
				}
				comparison := buildEvalFreshExecutionComparison(report, other)
				if comparison == nil {
					continue
				}
				switch comparison.Preferred {
				case "scenario":
					entry.wins++
					entry.reasons = append(entry.reasons, comparison.PreferenceReasons...)
				case "baseline":
					entry.losses++
				default:
					entry.ties++
				}
			}
			entry.reasons = slices.Compact(entry.reasons)
			scored = append(scored, entry)
		}
		slices.SortFunc(scored, func(left scoredEntry, right scoredEntry) int {
			leftRun := left.report.LatestFreshRun
			rightRun := right.report.LatestFreshRun
			if left.wins != right.wins {
				return right.wins - left.wins
			}
			if left.losses != right.losses {
				return left.losses - right.losses
			}
			if evalFreshStatusRank(leftRun.Status) != evalFreshStatusRank(rightRun.Status) {
				return evalFreshStatusRank(rightRun.Status) - evalFreshStatusRank(leftRun.Status)
			}
			if leftRun.EstimatedCost != rightRun.EstimatedCost {
				if leftRun.EstimatedCost < rightRun.EstimatedCost {
					return -1
				}
				return 1
			}
			if leftRun.DurationMS != rightRun.DurationMS {
				return leftRun.DurationMS - rightRun.DurationMS
			}
			if left.report.Scenario.CreatedAt != right.report.Scenario.CreatedAt {
				if left.report.Scenario.CreatedAt < right.report.Scenario.CreatedAt {
					return -1
				}
				return 1
			}
			return strings.Compare(left.report.Scenario.ScenarioID, right.report.Scenario.ScenarioID)
		})
		rankedScenarios := make([]EvalFreshReplayRankingEntry, 0, len(scored))
		for index, entry := range scored {
			latest := entry.report.LatestFreshRun
			summaryParts := []string{
				intString(entry.wins) + " pairwise win(s)",
				intString(entry.losses) + " loss(es)",
				intString(entry.ties) + " tie(s)",
				"status " + latest.Status,
				"$" + strconv.FormatFloat(latest.EstimatedCost, 'f', 4, 64) + " estimated cost",
				intString(latest.DurationMS) + "ms duration",
			}
			if len(entry.reasons) > 0 {
				summaryParts = append(summaryParts, "reasons: "+strings.Join(entry.reasons[:min(3, len(entry.reasons))], ", "))
			}
			rankedScenarios = append(rankedScenarios, EvalFreshReplayRankingEntry{
				Rank:              index + 1,
				ScenarioID:        entry.report.Scenario.ScenarioID,
				ScenarioName:      entry.report.Scenario.Name,
				LatestFreshRun:    *latest,
				PairwiseWins:      entry.wins,
				PairwiseLosses:    entry.losses,
				PairwiseTies:      entry.ties,
				PreferenceReasons: entry.reasons,
				Summary:           strings.Join(summaryParts, "; "),
			})
		}
		top := rankedScenarios[0]
		summary := "Top fresh replay: " + top.ScenarioName + " ranked 1/" + intString(len(rankedScenarios)) +
			" with " + intString(top.PairwiseWins) + " pairwise win(s)"
		if baseline.Scenario.ScenarioID != top.ScenarioID {
			summary += "; baseline remains " + baseline.Scenario.Name
		}
		rankings = append(rankings, EvalFreshReplayRanking{
			IssueID:              issueID,
			BaselineScenarioID:   optionalString(baseline.Scenario.ScenarioID),
			BaselineScenarioName: optionalString(baseline.Scenario.Name),
			RankedScenarios:      rankedScenarios,
			Summary:              summary,
		})
	}
	return rankings
}

func buildEvalFreshReplayTrends(
	dataDir string,
	workspaceID string,
	scenarios []EvalScenarioRecord,
	reports []EvalScenarioReport,
	rankings []EvalFreshReplayRanking,
	replayBatches []EvalReplayBatchRecord,
) []EvalFreshReplayTrend {
	issueBatches := map[string][]EvalReplayBatchRecord{}
	for _, batch := range replayBatches {
		issueBatches[batch.IssueID] = append(issueBatches[batch.IssueID], batch)
	}
	trends := make([]EvalFreshReplayTrend, 0, len(rankings))
	batchDrivenIssues := map[string]struct{}{}
	for issueID, batches := range issueBatches {
		orderedBatches := slices.Clone(batches)
		slices.SortFunc(orderedBatches, func(left EvalReplayBatchRecord, right EvalReplayBatchRecord) int {
			if left.CreatedAt != right.CreatedAt {
				return strings.Compare(right.CreatedAt, left.CreatedAt)
			}
			return strings.Compare(right.BatchID, left.BatchID)
		})
		if len(orderedBatches) < 2 {
			continue
		}
		latestBatch := orderedBatches[0]
		previousBatch := orderedBatches[1]
		latestRanking := buildEvalBatchRanking(dataDir, workspaceID, issueID, latestBatch, reports)
		previousRanking := buildEvalBatchRanking(dataDir, workspaceID, issueID, previousBatch, reports)
		if latestRanking == nil || previousRanking == nil {
			continue
		}
		previousEntries := map[string]EvalFreshReplayRankingEntry{}
		for _, entry := range previousRanking.RankedScenarios {
			previousEntries[entry.ScenarioID] = entry
		}
		entries := make([]EvalFreshReplayTrendEntry, 0, len(latestRanking.RankedScenarios))
		movedCount := 0
		for _, entry := range latestRanking.RankedScenarios {
			previousEntry, hasPrevious := previousEntries[entry.ScenarioID]
			var previousRank *int
			movement := "new"
			if hasPrevious {
				previousRank = ptr(previousEntry.Rank)
				switch {
				case entry.Rank < previousEntry.Rank:
					movement = "up"
				case entry.Rank > previousEntry.Rank:
					movement = "down"
				default:
					movement = "same"
				}
			}
			if movement == "up" || movement == "down" {
				movedCount++
			}
			var previousFreshRun *EvalFreshRunSummary
			if hasPrevious {
				previousFreshRun = &previousEntry.LatestFreshRun
			}
			summaryParts := []string{"current rank " + intString(entry.Rank)}
			if previousRank != nil {
				summaryParts = append(summaryParts, "previous rank "+intString(*previousRank))
			}
			summaryParts = append(summaryParts, "movement "+movement)
			summaryParts = append(summaryParts, "latest batch "+latestBatch.BatchID)
			summaryParts = append(summaryParts, "previous batch "+previousBatch.BatchID)
			entries = append(entries, EvalFreshReplayTrendEntry{
				ScenarioID:       entry.ScenarioID,
				ScenarioName:     entry.ScenarioName,
				CurrentRank:      entry.Rank,
				PreviousRank:     previousRank,
				Movement:         movement,
				LatestFreshRun:   entry.LatestFreshRun,
				PreviousFreshRun: previousFreshRun,
				Summary:          strings.Join(summaryParts, "; "),
			})
		}
		if len(entries) == 0 {
			continue
		}
		batchDrivenIssues[issueID] = struct{}{}
		summary := "Replay batch ranks are unchanged between " + previousBatch.BatchID + " and " + latestBatch.BatchID
		if movedCount > 0 {
			summary = intString(movedCount) + " scenario(s) changed rank between replay batches " + previousBatch.BatchID + " and " + latestBatch.BatchID
		}
		trends = append(trends, EvalFreshReplayTrend{
			IssueID:         issueID,
			LatestBatchID:   ptr(latestBatch.BatchID),
			PreviousBatchID: ptr(previousBatch.BatchID),
			Entries:         entries,
			Summary:         summary,
		})
	}

	reportLookup := map[string]EvalScenarioReport{}
	for _, report := range reports {
		reportLookup[report.Scenario.ScenarioID] = report
	}
	previousReports := make([]EvalScenarioReport, 0, len(scenarios))
	previousRunLookup := map[string]EvalFreshRunSummary{}
	for _, scenario := range scenarios {
		report, ok := reportLookup[scenario.ScenarioID]
		if !ok {
			continue
		}
		runs := selectEvalRuns(dataDir, workspaceID, scenario)
		metrics := loadEvalRunMetrics(dataDir, runs)
		freshRuns := buildEvalFreshRunSummaries(scenario, runs, metrics)
		if len(freshRuns) < 2 {
			continue
		}
		previousRunLookup[scenario.ScenarioID] = freshRuns[1]
		report.LatestFreshRun = &freshRuns[1]
		previousReports = append(previousReports, report)
	}
	previousRankings := buildEvalFreshReplayRankings(previousReports)
	previousLookup := map[string]map[string]EvalFreshReplayRankingEntry{}
	for _, ranking := range previousRankings {
		entryLookup := map[string]EvalFreshReplayRankingEntry{}
		for _, entry := range ranking.RankedScenarios {
			entryLookup[entry.ScenarioID] = entry
		}
		previousLookup[ranking.IssueID] = entryLookup
	}
	for _, ranking := range rankings {
		if _, ok := batchDrivenIssues[ranking.IssueID]; ok {
			continue
		}
		previousEntries := previousLookup[ranking.IssueID]
		if len(previousEntries) == 0 {
			continue
		}
		entries := make([]EvalFreshReplayTrendEntry, 0, len(ranking.RankedScenarios))
		movedCount := 0
		for _, entry := range ranking.RankedScenarios {
			previousEntry, hasPrevious := previousEntries[entry.ScenarioID]
			var previousRank *int
			movement := "new"
			if hasPrevious {
				previousRank = &previousEntry.Rank
				switch {
				case entry.Rank < previousEntry.Rank:
					movement = "up"
				case entry.Rank > previousEntry.Rank:
					movement = "down"
				default:
					movement = "same"
				}
			}
			if movement == "up" || movement == "down" {
				movedCount++
			}
			summaryParts := []string{"current rank " + intString(entry.Rank)}
			if previousRank != nil {
				summaryParts = append(summaryParts, "previous rank "+intString(*previousRank))
			}
			summaryParts = append(summaryParts, "movement "+movement)
			var previousFreshRun *EvalFreshRunSummary
			if previous, ok := previousRunLookup[entry.ScenarioID]; ok {
				previousFreshRun = &previous
			}
			entries = append(entries, EvalFreshReplayTrendEntry{
				ScenarioID:       entry.ScenarioID,
				ScenarioName:     entry.ScenarioName,
				CurrentRank:      entry.Rank,
				PreviousRank:     previousRank,
				Movement:         movement,
				LatestFreshRun:   entry.LatestFreshRun,
				PreviousFreshRun: previousFreshRun,
				Summary:          strings.Join(summaryParts, "; "),
			})
		}
		summary := "Fresh replay ranks are unchanged from the previous snapshot"
		if movedCount > 0 {
			summary = intString(movedCount) + " scenario(s) changed rank since the previous fresh replay snapshot"
		}
		trends = append(trends, EvalFreshReplayTrend{
			IssueID:         ranking.IssueID,
			LatestBatchID:   nil,
			PreviousBatchID: nil,
			Entries:         entries,
			Summary:         summary,
		})
	}
	return trends
}

func buildEvalBatchRanking(
	dataDir string,
	workspaceID string,
	issueID string,
	batch EvalReplayBatchRecord,
	reports []EvalScenarioReport,
) *EvalFreshReplayRanking {
	reportLookup := map[string]EvalScenarioReport{}
	for _, report := range reports {
		if report.Scenario.IssueID == issueID {
			reportLookup[report.Scenario.ScenarioID] = report
		}
	}
	runs, err := ListRuns(dataDir, workspaceID)
	if err != nil {
		return nil
	}
	runLookup := map[string]runRecord{}
	for _, run := range runs {
		runLookup[run.RunID] = run
	}
	batchReports := make([]EvalScenarioReport, 0, len(batch.ScenarioIDs))
	for _, scenarioID := range batch.ScenarioIDs {
		report, ok := reportLookup[scenarioID]
		if !ok {
			continue
		}
		var run *runRecord
		for _, runID := range batch.QueuedRunIDs {
			current, ok := runLookup[runID]
			if ok && current.EvalScenarioID != nil && *current.EvalScenarioID == scenarioID {
				run = &current
				break
			}
		}
		if run == nil {
			continue
		}
		metric, _ := loadRunMetrics(dataDir, run.RunID)
		summary := EvalFreshRunSummary{
			ScenarioID:     scenarioID,
			ScenarioName:   report.Scenario.Name,
			RunID:          run.RunID,
			Status:         run.Status,
			Runtime:        run.Runtime,
			Model:          run.Model,
			CreatedAt:      run.CreatedAt,
			CommandPreview: optionalString(run.CommandPreview),
			Planning:       run.Status == "planning",
		}
		if metric != nil {
			summary.EstimatedCost = metric.EstimatedCost
			summary.DurationMS = metric.DurationMS
		}
		report.LatestFreshRun = &summary
		batchReports = append(batchReports, report)
	}
	if len(batchReports) < 2 {
		return nil
	}
	rankings := buildEvalFreshReplayRankings(batchReports)
	for _, ranking := range rankings {
		if ranking.IssueID == issueID {
			return &ranking
		}
	}
	return nil
}

func buildEvalScenarioBaselineComparison(
	dataDir string,
	workspaceID string,
	report EvalScenarioReport,
	baseline EvalScenarioReport,
) (*EvalScenarioBaselineComparison, error) {
	if baseline.Scenario.ScenarioID == "" || baseline.Scenario.ScenarioID == report.Scenario.ScenarioID {
		return nil, nil
	}
	issue, err := requireIssue(dataDir, workspaceID, report.Scenario.IssueID)
	if err != nil {
		return nil, err
	}
	guidanceOnlyInScenario := sortedDifference(report.Scenario.GuidancePaths, baseline.Scenario.GuidancePaths)
	guidanceOnlyInBaseline := sortedDifference(baseline.Scenario.GuidancePaths, report.Scenario.GuidancePaths)
	ticketOnlyInScenario := sortedDifference(report.Scenario.TicketContextIDs, baseline.Scenario.TicketContextIDs)
	ticketOnlyInBaseline := sortedDifference(baseline.Scenario.TicketContextIDs, report.Scenario.TicketContextIDs)
	browserOnlyInScenario := sortedDifference(report.Scenario.BrowserDumpIDs, baseline.Scenario.BrowserDumpIDs)
	browserOnlyInBaseline := sortedDifference(baseline.Scenario.BrowserDumpIDs, report.Scenario.BrowserDumpIDs)
	profileOnlyInScenario := sortedDifference(report.Scenario.VerificationProfileIDs, baseline.Scenario.VerificationProfileIDs)
	profileOnlyInBaseline := sortedDifference(baseline.Scenario.VerificationProfileIDs, report.Scenario.VerificationProfileIDs)
	verificationProfileDeltas := buildEvalVerificationProfileDeltas(report, baseline)
	successDelta := report.SuccessRuns - baseline.SuccessRuns
	failedDelta := report.FailedRuns - baseline.FailedRuns
	verificationDelta := roundOneDecimal(report.VerificationSuccessRate - baseline.VerificationSuccessRate)
	durationDelta := report.AvgDurationMS - baseline.AvgDurationMS
	costDelta := roundEvalCost(report.TotalEstimatedCost - baseline.TotalEstimatedCost)
	scenarioScore := 0
	baselineScore := 0
	scenarioReasons := []string{}
	baselineReasons := []string{}
	for _, candidate := range []struct {
		reason       string
		weight       int
		scenarioWins bool
		baselineWins bool
	}{
		{reason: "more successful runs", weight: 2, scenarioWins: successDelta > 0, baselineWins: successDelta < 0},
		{reason: "fewer failed runs", weight: 2, scenarioWins: failedDelta < 0, baselineWins: failedDelta > 0},
		{reason: "higher verification confidence", weight: 2, scenarioWins: verificationDelta > 0, baselineWins: verificationDelta < 0},
		{reason: "lower estimated cost", weight: 1, scenarioWins: costDelta < 0, baselineWins: costDelta > 0},
		{reason: "faster average duration", weight: 1, scenarioWins: durationDelta < 0, baselineWins: durationDelta > 0},
	} {
		if candidate.scenarioWins {
			scenarioScore += candidate.weight
			scenarioReasons = append(scenarioReasons, candidate.reason)
		} else if candidate.baselineWins {
			baselineScore += candidate.weight
			baselineReasons = append(baselineReasons, candidate.reason)
		}
	}
	preferred := "tie"
	var preferredScenarioID *string
	var preferredScenarioName *string
	preferenceReasons := []string{}
	if scenarioScore > baselineScore {
		preferred = "scenario"
		preferredScenarioID = optionalString(report.Scenario.ScenarioID)
		preferredScenarioName = optionalString(report.Scenario.Name)
		preferenceReasons = scenarioReasons
	} else if baselineScore > scenarioScore {
		preferred = "baseline"
		preferredScenarioID = optionalString(baseline.Scenario.ScenarioID)
		preferredScenarioName = optionalString(baseline.Scenario.Name)
		preferenceReasons = baselineReasons
	}
	summaryParts := []string{"Compared to baseline " + baseline.Scenario.Name}
	if successDelta != 0 {
		summaryParts = append(summaryParts, signedIntString(successDelta)+" successful run(s)")
	}
	if failedDelta != 0 {
		summaryParts = append(summaryParts, signedIntString(failedDelta)+" failed run(s)")
	}
	if verificationDelta != 0 {
		summaryParts = append(summaryParts, signedFloatString(verificationDelta, 1)+"% verification")
	}
	if costDelta != 0 {
		summaryParts = append(summaryParts, signedFloatString(costDelta, 4)+" cost")
	}
	if durationDelta != 0 {
		summaryParts = append(summaryParts, signedIntString(durationDelta)+"ms duration")
	}
	if len(verificationProfileDeltas) > 0 {
		summaryParts = append(summaryParts, intString(len(verificationProfileDeltas))+" verification profile comparison(s)")
	}
	if preferredScenarioName != nil {
		summaryParts = append(summaryParts, "preferred: "+*preferredScenarioName)
	}
	if strings.TrimSpace(issue.Title) != "" {
		summaryParts = append(summaryParts, "issue: "+issue.Title)
	}
	return &EvalScenarioBaselineComparison{
		ComparedToScenarioID:              baseline.Scenario.ScenarioID,
		ComparedToName:                    baseline.Scenario.Name,
		GuidanceOnlyInScenario:            guidanceOnlyInScenario,
		GuidanceOnlyInBaseline:            guidanceOnlyInBaseline,
		TicketContextOnlyInScenario:       ticketOnlyInScenario,
		TicketContextOnlyInBaseline:       ticketOnlyInBaseline,
		BrowserDumpOnlyInScenario:         browserOnlyInScenario,
		BrowserDumpOnlyInBaseline:         browserOnlyInBaseline,
		VerificationProfileOnlyInScenario: profileOnlyInScenario,
		VerificationProfileOnlyInBaseline: profileOnlyInBaseline,
		VerificationProfileDeltas:         verificationProfileDeltas,
		SuccessRunsDelta:                  successDelta,
		FailedRunsDelta:                   failedDelta,
		VerificationSuccessRateDelta:      verificationDelta,
		AvgDurationMSDelta:                durationDelta,
		TotalEstimatedCostDelta:           costDelta,
		Preferred:                         preferred,
		PreferredScenarioID:               preferredScenarioID,
		PreferredScenarioName:             preferredScenarioName,
		PreferenceReasons:                 preferenceReasons,
		Summary:                           strings.Join(summaryParts, "; "),
	}, nil
}

func buildEvalVerificationProfileDeltas(
	report EvalScenarioReport,
	baseline EvalScenarioReport,
) []EvalScenarioVerificationProfileDelta {
	scenarioReports := map[string]VerificationProfileReport{}
	for _, item := range report.VerificationProfileReports {
		scenarioReports[item.ProfileID] = item
	}
	baselineReports := map[string]VerificationProfileReport{}
	for _, item := range baseline.VerificationProfileReports {
		baselineReports[item.ProfileID] = item
	}
	profileIDs := make([]string, 0, len(scenarioReports)+len(baselineReports))
	seen := map[string]struct{}{}
	for profileID := range scenarioReports {
		if _, ok := seen[profileID]; !ok {
			seen[profileID] = struct{}{}
			profileIDs = append(profileIDs, profileID)
		}
	}
	for profileID := range baselineReports {
		if _, ok := seen[profileID]; !ok {
			seen[profileID] = struct{}{}
			profileIDs = append(profileIDs, profileID)
		}
	}
	slices.Sort(profileIDs)
	deltas := make([]EvalScenarioVerificationProfileDelta, 0, len(profileIDs))
	for _, profileID := range profileIDs {
		scenarioItem, scenarioPresent := scenarioReports[profileID]
		baselineItem, baselinePresent := baselineReports[profileID]
		profileName := profileID
		if scenarioPresent {
			profileName = scenarioItem.ProfileName
		} else if baselinePresent {
			profileName = baselineItem.ProfileName
		}
		scenarioTotalRuns := 0
		baselineTotalRuns := 0
		scenarioSuccessRate := 0.0
		baselineSuccessRate := 0.0
		scenarioChecklistPassRate := 0.0
		baselineChecklistPassRate := 0.0
		scenarioAvgAttemptCount := 0.0
		baselineAvgAttemptCount := 0.0
		scenarioConfidenceCounts := map[string]int{}
		baselineConfidenceCounts := map[string]int{}
		if scenarioPresent {
			scenarioTotalRuns = scenarioItem.TotalRuns
			scenarioSuccessRate = scenarioItem.SuccessRate
			scenarioChecklistPassRate = scenarioItem.ChecklistPassRate
			scenarioAvgAttemptCount = scenarioItem.AvgAttemptCount
			scenarioConfidenceCounts = cloneIntMap(scenarioItem.ConfidenceCounts)
		}
		if baselinePresent {
			baselineTotalRuns = baselineItem.TotalRuns
			baselineSuccessRate = baselineItem.SuccessRate
			baselineChecklistPassRate = baselineItem.ChecklistPassRate
			baselineAvgAttemptCount = baselineItem.AvgAttemptCount
			baselineConfidenceCounts = cloneIntMap(baselineItem.ConfidenceCounts)
		}
		successRateDelta := roundOneDecimal(scenarioSuccessRate - baselineSuccessRate)
		checklistPassRateDelta := roundOneDecimal(scenarioChecklistPassRate - baselineChecklistPassRate)
		avgAttemptCountDelta := roundTwoDecimals(scenarioAvgAttemptCount - baselineAvgAttemptCount)
		preferred := "tie"
		if successRateDelta > 0 || checklistPassRateDelta > 0 || avgAttemptCountDelta < 0 {
			preferred = "scenario"
		} else if successRateDelta < 0 || checklistPassRateDelta < 0 || avgAttemptCountDelta > 0 {
			preferred = "baseline"
		}
		summaryParts := []string{profileName}
		if scenarioTotalRuns-baselineTotalRuns != 0 {
			summaryParts = append(summaryParts, signedIntString(scenarioTotalRuns-baselineTotalRuns)+" run(s)")
		}
		if successRateDelta != 0 {
			summaryParts = append(summaryParts, signedFloatString(successRateDelta, 1)+"% success")
		}
		if checklistPassRateDelta != 0 {
			summaryParts = append(summaryParts, signedFloatString(checklistPassRateDelta, 1)+"% checklist")
		}
		if avgAttemptCountDelta != 0 {
			summaryParts = append(summaryParts, signedFloatString(avgAttemptCountDelta, 2)+" attempts")
		}
		deltas = append(deltas, EvalScenarioVerificationProfileDelta{
			ProfileID:                 profileID,
			ProfileName:               profileName,
			PresentInScenario:         scenarioPresent,
			PresentInBaseline:         baselinePresent,
			ScenarioTotalRuns:         scenarioTotalRuns,
			BaselineTotalRuns:         baselineTotalRuns,
			TotalRunsDelta:            scenarioTotalRuns - baselineTotalRuns,
			ScenarioSuccessRate:       scenarioSuccessRate,
			BaselineSuccessRate:       baselineSuccessRate,
			SuccessRateDelta:          successRateDelta,
			ScenarioChecklistPassRate: scenarioChecklistPassRate,
			BaselineChecklistPassRate: baselineChecklistPassRate,
			ChecklistPassRateDelta:    checklistPassRateDelta,
			ScenarioAvgAttemptCount:   scenarioAvgAttemptCount,
			BaselineAvgAttemptCount:   baselineAvgAttemptCount,
			AvgAttemptCountDelta:      avgAttemptCountDelta,
			ScenarioConfidenceCounts:  scenarioConfidenceCounts,
			BaselineConfidenceCounts:  baselineConfidenceCounts,
			Preferred:                 preferred,
			Summary:                   strings.Join(summaryParts, "; "),
		})
	}
	return deltas
}

func buildEvalVariantRollups(dataDir string, workspaceID string, scenarios []EvalScenarioRecord, variantKind string) ([]EvalVariantRollup, error) {
	type variantCounts struct {
		total   int
		success int
	}
	buckets := map[string]*EvalVariantRollup{}
	bucketRuns := map[string][]runRecord{}
	bucketMetricCount := map[string]int{}
	bucketDuration := map[string]int{}
	bucketVerification := map[string]variantCounts{}
	bucketVerificationKeys := map[string]map[string]struct{}{}
	for _, scenario := range scenarios {
		selectedValues := scenario.GuidancePaths
		if variantKind == "ticket_context" {
			selectedValues = scenario.TicketContextIDs
		}
		variantKey := "__default__"
		if len(selectedValues) > 0 {
			variantKey = strings.Join(selectedValues, "|")
		}
		current, ok := buckets[variantKey]
		if !ok {
			current = &EvalVariantRollup{
				VariantKind:    variantKind,
				VariantKey:     variantKey,
				Label:          formatEvalVariantLabel(variantKind, selectedValues),
				SelectedValues: append([]string{}, selectedValues...),
			}
			buckets[variantKey] = current
			bucketRuns[variantKey] = []runRecord{}
			bucketMetricCount[variantKey] = 0
			bucketVerificationKeys[variantKey] = map[string]struct{}{}
		}
		current.ScenarioIDs = append(current.ScenarioIDs, scenario.ScenarioID)
		current.ScenarioNames = append(current.ScenarioNames, scenario.Name)
		current.ScenarioCount++
		selectedRuns := dedupeEvalRuns(selectEvalRuns(dataDir, workspaceID, scenario))
		runMetrics := loadEvalRunMetrics(dataDir, selectedRuns)
		bucketRuns[variantKey] = dedupeEvalRuns(append(bucketRuns[variantKey], selectedRuns...))
		current.RunCount = len(bucketRuns[variantKey])
		current.SuccessRuns = 0
		current.FailedRuns = 0
		for _, run := range bucketRuns[variantKey] {
			if run.Status == "completed" {
				current.SuccessRuns++
			} else if run.Status == "failed" {
				current.FailedRuns++
			}
		}
		for _, metric := range runMetrics {
			current.TotalEstimatedCost += metric.EstimatedCost
			bucketDuration[variantKey] += metric.DurationMS
		}
		bucketMetricCount[variantKey] += len(runMetrics)
		current.TotalEstimatedCost = roundEvalCost(current.TotalEstimatedCost)
		if bucketMetricCount[variantKey] > 0 {
			current.AvgDurationMS = bucketDuration[variantKey] / bucketMetricCount[variantKey]
		}
		verificationReports, err := ListVerificationProfileReports(dataDir, workspaceID, scenario.IssueID)
		if err != nil {
			return nil, err
		}
		if len(scenario.VerificationProfileIDs) > 0 {
			allowed := map[string]struct{}{}
			for _, profileID := range scenario.VerificationProfileIDs {
				allowed[profileID] = struct{}{}
			}
			filtered := make([]VerificationProfileReport, 0, len(verificationReports))
			for _, item := range verificationReports {
				if _, ok := allowed[item.ProfileID]; ok {
					filtered = append(filtered, item)
				}
			}
			verificationReports = filtered
		}
		counts := bucketVerification[variantKey]
		for _, item := range verificationReports {
			verificationKey := scenario.IssueID + "|" + item.ProfileID
			if _, ok := bucketVerificationKeys[variantKey][verificationKey]; ok {
				continue
			}
			bucketVerificationKeys[variantKey][verificationKey] = struct{}{}
			counts.total += item.TotalRuns
			counts.success += item.SuccessRuns
		}
		bucketVerification[variantKey] = counts
		current.VerificationSuccessRate = evalVerificationSuccessRate(counts.success, counts.total)
		current.RuntimeBreakdown = buildEvalRunDimensionBreakdown(
			bucketRuns[variantKey],
			func(run runRecord) (string, string) {
				if strings.TrimSpace(run.Runtime) == "" {
					return "unknown", "unknown"
				}
				return run.Runtime, run.Runtime
			},
		)
		current.ModelBreakdown = buildEvalRunDimensionBreakdown(
			bucketRuns[variantKey],
			func(run runRecord) (string, string) {
				if strings.TrimSpace(run.Model) == "" {
					return "unknown", "unknown"
				}
				return run.Model, run.Model
			},
		)
		current.Summary = strings.Join([]string{
			intString(current.ScenarioCount) + " scenario(s)",
			intString(current.SuccessRuns) + " successful run(s)",
			intString(current.FailedRuns) + " failed run(s)",
			"verification " + strconv.FormatFloat(current.VerificationSuccessRate, 'f', 1, 64) + "%",
		}, "; ")
	}
	items := make([]EvalVariantRollup, 0, len(buckets))
	for _, item := range buckets {
		items = append(items, *item)
	}
	slices.SortFunc(items, func(a, b EvalVariantRollup) int {
		if a.ScenarioCount != b.ScenarioCount {
			if a.ScenarioCount > b.ScenarioCount {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Label)
		right := strings.ToLower(b.Label)
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
		if a.VariantKey < b.VariantKey {
			return -1
		}
		if a.VariantKey > b.VariantKey {
			return 1
		}
		return 0
	})
	return items, nil
}

func loadEvalScenarios(dataDir string, workspaceID string) ([]EvalScenarioRecord, error) {
	path := evalScenariosPath(dataDir, workspaceID)
	var items []EvalScenarioRecord
	if err := readJSON(path, &items); err != nil {
		if os.IsNotExist(err) {
			return []EvalScenarioRecord{}, nil
		}
		return nil, err
	}
	sortEvalScenariosForRead(items)
	return items, nil
}

func loadEvalReplayBatches(dataDir string, workspaceID string) ([]EvalReplayBatchRecord, error) {
	path := evalReplayBatchesPath(dataDir, workspaceID)
	var items []EvalReplayBatchRecord
	if err := readJSON(path, &items); err != nil {
		if os.IsNotExist(err) {
			return []EvalReplayBatchRecord{}, nil
		}
		return nil, err
	}
	slices.SortFunc(items, func(left EvalReplayBatchRecord, right EvalReplayBatchRecord) int {
		if left.CreatedAt != right.CreatedAt {
			return strings.Compare(right.CreatedAt, left.CreatedAt)
		}
		return strings.Compare(right.BatchID, left.BatchID)
	})
	return items, nil
}

func selectEvalRuns(dataDir string, workspaceID string, scenario EvalScenarioRecord) []runRecord {
	runs, err := ListRuns(dataDir, workspaceID)
	if err != nil {
		return []runRecord{}
	}
	runIDFilter := map[string]struct{}{}
	for _, runID := range scenario.RunIDs {
		runIDFilter[runID] = struct{}{}
	}
	selectedRuns := make([]runRecord, 0, len(runs))
	for _, run := range runs {
		if run.IssueID != scenario.IssueID {
			continue
		}
		if len(runIDFilter) > 0 {
			if _, ok := runIDFilter[run.RunID]; !ok {
				continue
			}
		}
		selectedRuns = append(selectedRuns, run)
	}
	return selectedRuns
}

func dedupeEvalRuns(runs []runRecord) []runRecord {
	ordered := make([]runRecord, 0, len(runs))
	seen := map[string]int{}
	for _, run := range runs {
		if index, ok := seen[run.RunID]; ok {
			ordered[index] = run
			continue
		}
		seen[run.RunID] = len(ordered)
		ordered = append(ordered, run)
	}
	return ordered
}

func loadEvalRunMetrics(dataDir string, runs []runRecord) []RunMetrics {
	runMetrics := make([]RunMetrics, 0, len(runs))
	for _, run := range runs {
		metrics, err := loadRunMetrics(dataDir, run.RunID)
		if err != nil || metrics == nil {
			continue
		}
		runMetrics = append(runMetrics, *metrics)
	}
	return runMetrics
}

func saveEvalScenarios(dataDir string, workspaceID string, items []EvalScenarioRecord) error {
	sortEvalScenariosForSave(items)
	return writeJSON(evalScenariosPath(dataDir, workspaceID), items)
}

func saveEvalReplayBatches(dataDir string, workspaceID string, items []EvalReplayBatchRecord) error {
	slices.SortFunc(items, func(left EvalReplayBatchRecord, right EvalReplayBatchRecord) int {
		if left.CreatedAt != right.CreatedAt {
			return strings.Compare(right.CreatedAt, left.CreatedAt)
		}
		return strings.Compare(right.BatchID, left.BatchID)
	})
	return writeJSON(evalReplayBatchesPath(dataDir, workspaceID), items)
}

func buildEvalRunDimensionBreakdown(
	runs []runRecord,
	resolver func(run runRecord) (string, string),
) []DimensionSummary {
	buckets := map[string]*DimensionSummary{}
	for _, run := range runs {
		key, label := resolver(run)
		if strings.TrimSpace(key) == "" {
			key = "unknown"
		}
		if strings.TrimSpace(label) == "" {
			label = key
		}
		current, ok := buckets[key]
		if !ok {
			current = &DimensionSummary{Key: key, Label: label}
			buckets[key] = current
		}
		current.TotalRuns++
		if run.Status == "completed" {
			current.SuccessRuns++
		} else if run.Status == "failed" {
			current.FailedRuns++
		}
		current.SuccessRate = roundOneDecimal((float64(current.SuccessRuns) / float64(current.TotalRuns)) * 100)
		if current.LastRunAt == nil || run.CreatedAt > *current.LastRunAt {
			value := run.CreatedAt
			current.LastRunAt = &value
		}
	}
	items := make([]DimensionSummary, 0, len(buckets))
	for _, item := range buckets {
		items = append(items, *item)
	}
	slices.SortFunc(items, func(a, b DimensionSummary) int {
		if a.TotalRuns != b.TotalRuns {
			if a.TotalRuns > b.TotalRuns {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Label)
		right := strings.ToLower(b.Label)
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
		return 0
	})
	return items
}

func sortEvalScenariosForRead(items []EvalScenarioRecord) {
	slices.SortFunc(items, func(a, b EvalScenarioRecord) int {
		if a.IssueID != b.IssueID {
			if a.IssueID < b.IssueID {
				return -1
			}
			return 1
		}
		if a.UpdatedAt != b.UpdatedAt {
			if a.UpdatedAt < b.UpdatedAt {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Name)
		right := strings.ToLower(b.Name)
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
		return 0
	})
}

func sortEvalScenariosForSave(items []EvalScenarioRecord) {
	slices.SortFunc(items, func(a, b EvalScenarioRecord) int {
		if a.IssueID != b.IssueID {
			if a.IssueID < b.IssueID {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Name)
		right := strings.ToLower(b.Name)
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
		if a.CreatedAt < b.CreatedAt {
			return -1
		}
		if a.CreatedAt > b.CreatedAt {
			return 1
		}
		return 0
	})
}

func evalVerificationSuccessRate(success int, total int) float64 {
	if total == 0 {
		return 0.0
	}
	return roundOneDecimal((float64(success) / float64(total)) * 100)
}

func roundEvalCost(value float64) float64 {
	return float64(int(value*10_000+0.5)) / 10_000
}

func formatEvalVariantLabel(variantKind string, selectedValues []string) string {
	if len(selectedValues) == 0 {
		if variantKind == "guidance" {
			return "Current defaults"
		}
		return "Current ticket context"
	}
	label := strings.Join(selectedValues[:min(len(selectedValues), 3)], ", ")
	if len(selectedValues) > 3 {
		label += " +" + strconv.Itoa(len(selectedValues)-3) + " more"
	}
	return label
}

func buildEvalVariantDiff(scenario EvalScenarioRecord, packet *IssueContextPacket) *EvalScenarioVariantDiff {
	if packet == nil {
		return nil
	}
	currentGuidancePaths := guidancePaths(packet.Guidance)
	currentTicketContextIDs := ticketContextIDs(packet.TicketContexts)
	addedGuidancePaths, removedGuidancePaths := diffOrderedStrings(scenario.GuidancePaths, currentGuidancePaths)
	addedTicketContextIDs, removedTicketContextIDs := diffOrderedStrings(scenario.TicketContextIDs, currentTicketContextIDs)
	changed := len(addedGuidancePaths) > 0 || len(removedGuidancePaths) > 0 || len(addedTicketContextIDs) > 0 || len(removedTicketContextIDs) > 0

	summaryParts := []string{}
	if len(addedGuidancePaths) > 0 || len(removedGuidancePaths) > 0 {
		summaryParts = append(summaryParts, "guidance +"+strconv.Itoa(len(addedGuidancePaths))+" / -"+strconv.Itoa(len(removedGuidancePaths)))
	}
	if len(addedTicketContextIDs) > 0 || len(removedTicketContextIDs) > 0 {
		summaryParts = append(summaryParts, "ticket context +"+strconv.Itoa(len(addedTicketContextIDs))+" / -"+strconv.Itoa(len(removedTicketContextIDs)))
	}
	if len(summaryParts) == 0 {
		summaryParts = append(summaryParts, "saved guidance and ticket-context variants still match the current issue packet")
	}

	return &EvalScenarioVariantDiff{
		SelectedGuidancePaths:    append([]string{}, scenario.GuidancePaths...),
		CurrentGuidancePaths:     currentGuidancePaths,
		AddedGuidancePaths:       addedGuidancePaths,
		RemovedGuidancePaths:     removedGuidancePaths,
		SelectedTicketContextIDs: append([]string{}, scenario.TicketContextIDs...),
		CurrentTicketContextIDs:  currentTicketContextIDs,
		AddedTicketContextIDs:    addedTicketContextIDs,
		RemovedTicketContextIDs:  removedTicketContextIDs,
		Changed:                  changed,
		Summary:                  strings.Join(summaryParts, "; "),
	}
}

func intString(value int) string {
	return strconv.Itoa(value)
}

func signedIntString(value int) string {
	if value >= 0 {
		return "+" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

func signedFloatString(value float64, decimals int) string {
	if value >= 0 {
		return "+" + strconv.FormatFloat(value, 'f', decimals, 64)
	}
	return strconv.FormatFloat(value, 'f', decimals, 64)
}

func sortedDifference(left []string, right []string) []string {
	rightSet := map[string]struct{}{}
	for _, item := range right {
		rightSet[item] = struct{}{}
	}
	result := make([]string, 0, len(left))
	for _, item := range left {
		if _, ok := rightSet[item]; ok {
			continue
		}
		result = append(result, item)
	}
	slices.Sort(result)
	return result
}

func cloneIntMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return map[string]int{}
	}
	cloned := make(map[string]int, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

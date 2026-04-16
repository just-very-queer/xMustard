package workspaceops

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"xmustard/api-go/internal/rustcore"
)

func TestSaveListDeleteEvalScenarios(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	saved, err := SaveEvalScenario(dataDir, workspaceID, EvalScenarioUpsertRequest{
		Name:             "Eval baseline",
		IssueID:          issueID,
		BaselineReplayID: stringPtr("replay-baseline"),
		GuidancePaths:    []string{"AGENTS.md"},
		TicketContextIDs: []string{"ticket-1"},
		RunIDs:           []string{"run-1"},
	})
	if err != nil {
		t.Fatalf("save eval scenario: %v", err)
	}
	if saved.ScenarioID == "" || saved.Name != "Eval baseline" {
		t.Fatalf("unexpected saved scenario: %#v", saved)
	}
	if len(saved.GuidancePaths) != 1 || len(saved.TicketContextIDs) != 1 {
		t.Fatalf("expected saved variant selections, got %#v", saved)
	}

	listed, err := ListEvalScenarios(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list eval scenarios: %v", err)
	}
	if len(listed) != 1 || listed[0].ScenarioID != saved.ScenarioID {
		t.Fatalf("unexpected listed scenarios: %#v", listed)
	}

	if err := DeleteEvalScenario(dataDir, workspaceID, saved.ScenarioID); err != nil {
		t.Fatalf("delete eval scenario: %v", err)
	}

	remaining, err := ListEvalScenarios(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list remaining eval scenarios: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected scenario deletion, got %#v", remaining)
	}

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl"))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !containsAction(content, "eval_scenario.saved") || !containsAction(content, "eval_scenario.deleted") {
		t.Fatalf("missing eval scenario activity entries: %s", string(content))
	}
}

func TestGetEvalReportAggregatesReplayVerificationAndMetrics(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)

	replay, err := CaptureIssueContextReplay(dataDir, workspaceID, issueID, IssueContextReplayRequest{
		Label: stringPtr("baseline replay"),
	})
	if err != nil {
		t.Fatalf("capture issue context replay: %v", err)
	}

	if _, err := SaveVerificationProfile(dataDir, workspaceID, VerificationProfileUpsertRequest{
		ProfileID:   stringPtr("backend-pytest"),
		Name:        "Backend pytest",
		Description: "Backend verification",
		TestCommand: "pytest -q",
	}); err != nil {
		t.Fatalf("save verification profile: %v", err)
	}
	if _, err := SaveVerificationProfile(dataDir, workspaceID, VerificationProfileUpsertRequest{
		ProfileID:      stringPtr("frontend-smoke"),
		Name:           "Frontend smoke",
		Description:    "Frontend verification",
		TestCommand:    "npm run test:smoke",
		ChecklistItems: []string{"banner renders"},
	}); err != nil {
		t.Fatalf("save second verification profile: %v", err)
	}

	run := runRecord{
		RunID:          "eval-run-001",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "codex",
		Model:          "gpt-5.4-mini",
		Status:         "completed",
		Title:          "eval run",
		Prompt:         "prompt",
		Command:        []string{"codex", "exec"},
		CommandPreview: "codex exec",
		LogPath:        filepath.Join(repoRoot, "eval-run-001.log"),
		OutputPath:     filepath.Join(repoRoot, "eval-run-001.out.json"),
		CreatedAt:      "2026-04-14T10:06:00Z",
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "eval-run-001.json"), run); err != nil {
		t.Fatalf("write run: %v", err)
	}
	secondRun := runRecord{
		RunID:          "eval-run-002",
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        "opencode",
		Model:          "gpt-5.4",
		Status:         "failed",
		Title:          "eval run 2",
		Prompt:         "prompt",
		Command:        []string{"opencode", "run"},
		CommandPreview: "opencode run",
		LogPath:        filepath.Join(repoRoot, "eval-run-002.log"),
		OutputPath:     filepath.Join(repoRoot, "eval-run-002.out.json"),
		CreatedAt:      "2026-04-14T10:09:00Z",
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", "eval-run-002.json"), secondRun); err != nil {
		t.Fatalf("write second run: %v", err)
	}
	if err := writeJSON(filepath.Join(dataDir, "metrics", "eval-run-001.json"), RunMetrics{
		RunID:         "eval-run-001",
		WorkspaceID:   workspaceID,
		InputTokens:   120,
		OutputTokens:  80,
		EstimatedCost: 0.123456,
		DurationMS:    3456,
		Model:         "gpt-5.4-mini",
		Runtime:       "codex",
		CalculatedAt:  "2026-04-14T10:07:00Z",
	}); err != nil {
		t.Fatalf("write metrics: %v", err)
	}
	if err := writeJSON(filepath.Join(dataDir, "metrics", "eval-run-002.json"), RunMetrics{
		RunID:         "eval-run-002",
		WorkspaceID:   workspaceID,
		InputTokens:   90,
		OutputTokens:  60,
		EstimatedCost: 0.075,
		DurationMS:    2100,
		Model:         "gpt-5.4",
		Runtime:       "opencode",
		CalculatedAt:  "2026-04-14T10:10:00Z",
	}); err != nil {
		t.Fatalf("write second metrics: %v", err)
	}

	if err := saveVerificationProfileHistory(dataDir, workspaceID, []rustcore.VerificationProfileResult{
		{
			ProfileID:    "backend-pytest",
			WorkspaceID:  workspaceID,
			ProfileName:  "Backend pytest",
			IssueID:      stringPtr(issueID),
			RunID:        stringPtr("eval-run-001"),
			AttemptCount: 1,
			Success:      true,
			Confidence:   "high",
			CreatedAt:    "2026-04-14T10:08:00Z",
		},
		{
			ProfileID:    "frontend-smoke",
			WorkspaceID:  workspaceID,
			ProfileName:  "Frontend smoke",
			IssueID:      stringPtr(issueID),
			RunID:        stringPtr("eval-run-002"),
			AttemptCount: 2,
			Success:      false,
			Confidence:   "medium",
			ChecklistResults: []rustcore.VerificationChecklistResult{
				{
					ItemID:  "banner-renders",
					Title:   "banner renders",
					Kind:    "custom",
					Passed:  false,
					Details: stringPtr("Banner stayed hidden"),
				},
			},
			CreatedAt: "2026-04-14T10:11:00Z",
		},
	}); err != nil {
		t.Fatalf("save verification history: %v", err)
	}

	scenario, err := SaveEvalScenario(dataDir, workspaceID, EvalScenarioUpsertRequest{
		Name:                   "Eval baseline",
		IssueID:                issueID,
		BaselineReplayID:       &replay.ReplayID,
		GuidancePaths:          []string{"AGENTS.md", "missing.md"},
		TicketContextIDs:       []string{"ticket-1", "ticket-missing"},
		VerificationProfileIDs: []string{"backend-pytest"},
		RunIDs:                 []string{"eval-run-001"},
	})
	if err != nil {
		t.Fatalf("save eval scenario: %v", err)
	}
	if _, err := SaveTicketContext(dataDir, workspaceID, issueID, TicketContextUpsertRequest{
		ContextID: stringPtr("ticket-2"),
		Title:     "Alternate ticket context",
		Summary:   "Secondary acceptance criteria",
	}); err != nil {
		t.Fatalf("save secondary ticket context: %v", err)
	}
	if _, err := SaveEvalScenario(dataDir, workspaceID, EvalScenarioUpsertRequest{
		Name:                   "Eval alternate",
		IssueID:                issueID,
		GuidancePaths:          []string{"CONVENTIONS.md"},
		TicketContextIDs:       []string{"ticket-2"},
		VerificationProfileIDs: []string{"frontend-smoke"},
		RunIDs:                 []string{"eval-run-002"},
	}); err != nil {
		t.Fatalf("save alternate eval scenario: %v", err)
	}
	if _, err := SaveEvalScenario(dataDir, workspaceID, EvalScenarioUpsertRequest{
		Name:                   "Eval duplicate baseline",
		IssueID:                issueID,
		GuidancePaths:          []string{"AGENTS.md", "missing.md"},
		TicketContextIDs:       []string{"ticket-1", "ticket-missing"},
		VerificationProfileIDs: []string{"backend-pytest"},
		RunIDs:                 []string{"eval-run-001"},
	}); err != nil {
		t.Fatalf("save duplicate baseline eval scenario: %v", err)
	}

	report, err := GetEvalReport(dataDir, workspaceID, scenario.ScenarioID)
	if err != nil {
		t.Fatalf("get eval report: %v", err)
	}
	if report.ScenarioCount != 1 || len(report.ScenarioReports) != 1 {
		t.Fatalf("unexpected report scenario count: %#v", report)
	}
	item := report.ScenarioReports[0]
	if item.Scenario.ScenarioID != scenario.ScenarioID {
		t.Fatalf("unexpected scenario in report: %#v", item)
	}
	if item.SuccessRuns != 1 || item.FailedRuns != 0 {
		t.Fatalf("unexpected run rollup: %#v", item)
	}
	if item.VerificationSuccessRate != 100.0 {
		t.Fatalf("unexpected verification success rate: %#v", item)
	}
	if item.BaselineReplay == nil || item.BaselineReplay.ReplayID != replay.ReplayID {
		t.Fatalf("missing baseline replay: %#v", item)
	}
	if item.LatestReplayComparison == nil {
		t.Fatalf("expected replay comparison in eval report: %#v", item)
	}
	if item.VariantDiff == nil || !item.VariantDiff.Changed {
		t.Fatalf("expected variant diff in eval report: %#v", item)
	}
	if len(item.VariantDiff.RemovedGuidancePaths) == 0 || item.VariantDiff.RemovedGuidancePaths[0] != "missing.md" {
		t.Fatalf("expected removed guidance path in variant diff: %#v", item.VariantDiff)
	}
	if len(item.VariantDiff.RemovedTicketContextIDs) == 0 || item.VariantDiff.RemovedTicketContextIDs[0] != "ticket-missing" {
		t.Fatalf("expected removed ticket context in variant diff: %#v", item.VariantDiff)
	}
	if len(item.RunMetrics) != 1 || item.RunMetrics[0].RunID != "eval-run-001" {
		t.Fatalf("unexpected run metrics: %#v", item.RunMetrics)
	}
	if len(item.VerificationProfileReports) != 1 || item.VerificationProfileReports[0].ProfileID != "backend-pytest" {
		t.Fatalf("unexpected verification profile reports: %#v", item.VerificationProfileReports)
	}
	if report.RunCount != 1 || report.SuccessRuns != 1 || report.VerificationSuccessRate != 100.0 {
		t.Fatalf("unexpected workspace eval rollup: %#v", report)
	}
	fullReport, err := GetEvalReport(dataDir, workspaceID, "")
	if err != nil {
		t.Fatalf("get full eval report: %v", err)
	}
	if fullReport.ScenarioCount != 3 {
		t.Fatalf("expected three scenarios in full report, got %#v", fullReport)
	}
	if len(fullReport.FreshReplayRankings) != 1 {
		t.Fatalf("expected fresh replay rankings, got %#v", fullReport.FreshReplayRankings)
	}
	freshRanking := fullReport.FreshReplayRankings[0]
	if freshRanking.IssueID != issueID || freshRanking.BaselineScenarioName == nil || *freshRanking.BaselineScenarioName != "Eval baseline" {
		t.Fatalf("unexpected fresh replay ranking header: %#v", freshRanking)
	}
	if len(freshRanking.RankedScenarios) != 3 || freshRanking.RankedScenarios[0].ScenarioName != "Eval baseline" || freshRanking.RankedScenarios[len(freshRanking.RankedScenarios)-1].ScenarioName != "Eval alternate" {
		t.Fatalf("unexpected fresh replay ranking order: %#v", freshRanking.RankedScenarios)
	}
	if freshRanking.RankedScenarios[len(freshRanking.RankedScenarios)-1].PairwiseLosses != 2 {
		t.Fatalf("expected alternate scenario to lose both pairwise fresh comparisons: %#v", freshRanking.RankedScenarios)
	}
	var alternateReport *EvalScenarioReport
	for index := range fullReport.ScenarioReports {
		if fullReport.ScenarioReports[index].Scenario.Name == "Eval alternate" {
			alternateReport = &fullReport.ScenarioReports[index]
			break
		}
	}
	if alternateReport == nil || alternateReport.ComparisonToBaseline == nil {
		t.Fatalf("expected alternate scenario comparison to baseline: %#v", fullReport.ScenarioReports)
	}
	if alternateReport.ComparisonToBaseline.ComparedToName != "Eval baseline" || alternateReport.ComparisonToBaseline.Preferred != "baseline" {
		t.Fatalf("unexpected baseline comparison: %#v", alternateReport.ComparisonToBaseline)
	}
	if len(alternateReport.ComparisonToBaseline.GuidanceOnlyInScenario) == 0 || alternateReport.ComparisonToBaseline.GuidanceOnlyInScenario[0] != "CONVENTIONS.md" {
		t.Fatalf("unexpected guidance comparison: %#v", alternateReport.ComparisonToBaseline)
	}
	if len(alternateReport.ComparisonToBaseline.VerificationProfileDeltas) != 2 {
		t.Fatalf("expected verification profile deltas: %#v", alternateReport.ComparisonToBaseline)
	}
	var frontendDelta *EvalScenarioVerificationProfileDelta
	for index := range alternateReport.ComparisonToBaseline.VerificationProfileDeltas {
		if alternateReport.ComparisonToBaseline.VerificationProfileDeltas[index].ProfileID == "frontend-smoke" {
			frontendDelta = &alternateReport.ComparisonToBaseline.VerificationProfileDeltas[index]
			break
		}
	}
	if frontendDelta == nil || frontendDelta.Preferred != "baseline" || frontendDelta.ScenarioTotalRuns != 1 || frontendDelta.BaselineTotalRuns != 0 {
		t.Fatalf("unexpected frontend verification delta: %#v", alternateReport.ComparisonToBaseline.VerificationProfileDeltas)
	}
	if len(fullReport.GuidanceVariantRollups) < 2 || len(fullReport.TicketContextVariantRollups) < 2 {
		t.Fatalf("expected multiple variant rollups, got %#v", fullReport)
	}
	var guidanceRollup *EvalVariantRollup
	for index := range fullReport.GuidanceVariantRollups {
		if len(fullReport.GuidanceVariantRollups[index].SelectedValues) == 2 {
			guidanceRollup = &fullReport.GuidanceVariantRollups[index]
			break
		}
	}
	if guidanceRollup == nil || guidanceRollup.ScenarioCount != 2 || guidanceRollup.RunCount != 1 || guidanceRollup.SuccessRuns != 1 {
		t.Fatalf("missing guidance rollup: %#v", fullReport.GuidanceVariantRollups)
	}
	if len(guidanceRollup.RuntimeBreakdown) == 0 || guidanceRollup.RuntimeBreakdown[0].Label != "codex" {
		t.Fatalf("unexpected guidance runtime breakdown: %#v", guidanceRollup.RuntimeBreakdown)
	}
	if guidanceRollup.VerificationSuccessRate != 100.0 {
		t.Fatalf("unexpected guidance verification rate: %#v", guidanceRollup)
	}
	var ticketRollup *EvalVariantRollup
	for index := range fullReport.TicketContextVariantRollups {
		if len(fullReport.TicketContextVariantRollups[index].SelectedValues) == 1 && fullReport.TicketContextVariantRollups[index].SelectedValues[0] == "ticket-2" {
			ticketRollup = &fullReport.TicketContextVariantRollups[index]
			break
		}
	}
	if ticketRollup == nil || ticketRollup.FailedRuns != 1 {
		t.Fatalf("missing ticket rollup: %#v", fullReport.TicketContextVariantRollups)
	}
	if len(ticketRollup.ModelBreakdown) == 0 || ticketRollup.ModelBreakdown[0].Label != "gpt-5.4" {
		t.Fatalf("unexpected ticket model breakdown: %#v", ticketRollup.ModelBreakdown)
	}
}

func TestGetEvalReportReturnsErrorForBrokenVerificationHistory(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	if _, err := SaveEvalScenario(dataDir, workspaceID, EvalScenarioUpsertRequest{
		Name:    "Broken verification history",
		IssueID: issueID,
	}); err != nil {
		t.Fatalf("save eval scenario: %v", err)
	}
	if err := os.WriteFile(verificationProfileHistoryPath(dataDir, workspaceID), []byte("{broken"), 0o644); err != nil {
		t.Fatalf("corrupt verification history: %v", err)
	}

	if _, err := GetEvalReport(dataDir, workspaceID, ""); err == nil {
		t.Fatal("expected eval report to fail on broken verification history")
	}
}

func TestGetEvalReportTracksFreshReplayRankMovement(t *testing.T) {
	dataDir, workspaceID, issueID, repoRoot := writeIssueContextFixture(t, false)
	for _, run := range []runRecord{
		{
			RunID:          "baseline-prev",
			WorkspaceID:    workspaceID,
			IssueID:        issueID,
			Runtime:        "codex",
			Model:          "gpt-5.4-mini",
			Status:         "completed",
			EvalScenarioID: stringPtr("eval-eval-baseline"),
			Title:          "eval trend",
			Prompt:         "prompt",
			Command:        []string{"codex", "exec"},
			CommandPreview: "codex exec",
			LogPath:        filepath.Join(repoRoot, "baseline-prev.log"),
			OutputPath:     filepath.Join(repoRoot, "baseline-prev.out.json"),
			CreatedAt:      "2026-04-14T10:00:00Z",
		},
		{
			RunID:          "baseline-latest",
			WorkspaceID:    workspaceID,
			IssueID:        issueID,
			Runtime:        "codex",
			Model:          "gpt-5.4-mini",
			Status:         "completed",
			EvalScenarioID: stringPtr("eval-eval-baseline"),
			Title:          "eval trend",
			Prompt:         "prompt",
			Command:        []string{"codex", "exec"},
			CommandPreview: "codex exec",
			LogPath:        filepath.Join(repoRoot, "baseline-latest.log"),
			OutputPath:     filepath.Join(repoRoot, "baseline-latest.out.json"),
			CreatedAt:      "2026-04-14T10:10:00Z",
		},
		{
			RunID:          "alternate-prev",
			WorkspaceID:    workspaceID,
			IssueID:        issueID,
			Runtime:        "opencode",
			Model:          "gpt-5.4",
			Status:         "failed",
			EvalScenarioID: stringPtr("eval-eval-alternate"),
			Title:          "eval trend",
			Prompt:         "prompt",
			Command:        []string{"opencode", "run"},
			CommandPreview: "opencode run",
			LogPath:        filepath.Join(repoRoot, "alternate-prev.log"),
			OutputPath:     filepath.Join(repoRoot, "alternate-prev.out.json"),
			CreatedAt:      "2026-04-14T10:01:00Z",
		},
		{
			RunID:          "alternate-latest",
			WorkspaceID:    workspaceID,
			IssueID:        issueID,
			Runtime:        "opencode",
			Model:          "gpt-5.4",
			Status:         "completed",
			EvalScenarioID: stringPtr("eval-eval-alternate"),
			Title:          "eval trend",
			Prompt:         "prompt",
			Command:        []string{"opencode", "run"},
			CommandPreview: "opencode run",
			LogPath:        filepath.Join(repoRoot, "alternate-latest.log"),
			OutputPath:     filepath.Join(repoRoot, "alternate-latest.out.json"),
			CreatedAt:      "2026-04-14T10:11:00Z",
		},
	} {
		if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "runs", run.RunID+".json"), run); err != nil {
			t.Fatalf("write run %s: %v", run.RunID, err)
		}
	}
	for _, metric := range []RunMetrics{
		{RunID: "baseline-prev", WorkspaceID: workspaceID, EstimatedCost: 0.0100, DurationMS: 900, Model: "gpt-5.4-mini", Runtime: "codex", CalculatedAt: "2026-04-14T10:00:00Z"},
		{RunID: "baseline-latest", WorkspaceID: workspaceID, EstimatedCost: 0.0500, DurationMS: 3000, Model: "gpt-5.4-mini", Runtime: "codex", CalculatedAt: "2026-04-14T10:10:00Z"},
		{RunID: "alternate-prev", WorkspaceID: workspaceID, EstimatedCost: 0.0400, DurationMS: 2400, Model: "gpt-5.4", Runtime: "opencode", CalculatedAt: "2026-04-14T10:01:00Z"},
		{RunID: "alternate-latest", WorkspaceID: workspaceID, EstimatedCost: 0.0100, DurationMS: 1200, Model: "gpt-5.4", Runtime: "opencode", CalculatedAt: "2026-04-14T10:11:00Z"},
	} {
		if err := writeJSON(filepath.Join(dataDir, "metrics", metric.RunID+".json"), metric); err != nil {
			t.Fatalf("write metrics %s: %v", metric.RunID, err)
		}
	}
	baselineScenario, err := SaveEvalScenario(dataDir, workspaceID, EvalScenarioUpsertRequest{
		Name:    "Eval baseline",
		IssueID: issueID,
		RunIDs:  []string{"baseline-prev", "baseline-latest"},
	})
	if err != nil {
		t.Fatalf("save baseline scenario: %v", err)
	}
	alternateScenario, err := SaveEvalScenario(dataDir, workspaceID, EvalScenarioUpsertRequest{
		Name:    "Eval alternate",
		IssueID: issueID,
		RunIDs:  []string{"alternate-prev", "alternate-latest"},
	})
	if err != nil {
		t.Fatalf("save alternate scenario: %v", err)
	}
	if err := saveEvalReplayBatches(dataDir, workspaceID, []EvalReplayBatchRecord{
		{
			BatchID:      "evalbatch_prev",
			WorkspaceID:  workspaceID,
			IssueID:      issueID,
			Runtime:      "codex",
			Model:        "gpt-5.4-mini",
			ScenarioIDs:  []string{baselineScenario.ScenarioID, alternateScenario.ScenarioID},
			QueuedRunIDs: []string{"baseline-prev", "alternate-prev"},
			CreatedAt:    "2026-04-14T10:02:00Z",
		},
		{
			BatchID:      "evalbatch_latest",
			WorkspaceID:  workspaceID,
			IssueID:      issueID,
			Runtime:      "codex",
			Model:        "gpt-5.4-mini",
			ScenarioIDs:  []string{baselineScenario.ScenarioID, alternateScenario.ScenarioID},
			QueuedRunIDs: []string{"baseline-latest", "alternate-latest"},
			CreatedAt:    "2026-04-14T10:12:00Z",
		},
	}); err != nil {
		t.Fatalf("save replay batches: %v", err)
	}

	report, err := GetEvalReport(dataDir, workspaceID, "")
	if err != nil {
		t.Fatalf("get eval report: %v", err)
	}
	if len(report.ReplayBatches) != 2 {
		t.Fatalf("expected replay batches in report, got %#v", report.ReplayBatches)
	}
	if len(report.FreshReplayTrends) != 1 {
		t.Fatalf("expected fresh replay trend, got %#v", report.FreshReplayTrends)
	}
	trend := report.FreshReplayTrends[0]
	if trend.IssueID != issueID {
		t.Fatalf("unexpected trend issue: %#v", trend)
	}
	if trend.LatestBatchID == nil || *trend.LatestBatchID != "evalbatch_latest" || trend.PreviousBatchID == nil || *trend.PreviousBatchID != "evalbatch_prev" {
		t.Fatalf("unexpected batch-backed trend ids: %#v", trend)
	}
	var baselineEntry *EvalFreshReplayTrendEntry
	var alternateEntry *EvalFreshReplayTrendEntry
	for index := range trend.Entries {
		switch trend.Entries[index].ScenarioName {
		case "Eval baseline":
			baselineEntry = &trend.Entries[index]
		case "Eval alternate":
			alternateEntry = &trend.Entries[index]
		}
	}
	if baselineEntry == nil || baselineEntry.Movement != "down" || baselineEntry.PreviousRank == nil || *baselineEntry.PreviousRank != 1 {
		t.Fatalf("unexpected baseline trend: %#v", baselineEntry)
	}
	if alternateEntry == nil || alternateEntry.Movement != "up" || alternateEntry.PreviousRank == nil || *alternateEntry.PreviousRank != 2 {
		t.Fatalf("unexpected alternate trend: %#v", alternateEntry)
	}
}

func TestReplayEvalScenariosQueuesFreshRunsAndUpdatesReport(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)
	opencodeBin := writeFakeOpencode(t)
	if _, err := UpdateSettings(dataDir, AppSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &opencodeBin,
		OpencodeModel:  stringPtr("fake/test-model"),
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	baselineScenario, err := SaveEvalScenario(dataDir, workspaceID, EvalScenarioUpsertRequest{
		Name:          "Eval baseline",
		IssueID:       issueID,
		GuidancePaths: []string{"AGENTS.md"},
	})
	if err != nil {
		t.Fatalf("save baseline scenario: %v", err)
	}
	if _, err := SaveTicketContext(dataDir, workspaceID, issueID, TicketContextUpsertRequest{
		ContextID: stringPtr("ticket-2"),
		Title:     "Alternate ticket context",
		Summary:   "Secondary acceptance criteria",
	}); err != nil {
		t.Fatalf("save ticket context: %v", err)
	}
	alternateScenario, err := SaveEvalScenario(dataDir, workspaceID, EvalScenarioUpsertRequest{
		Name:             "Eval alternate",
		IssueID:          issueID,
		GuidancePaths:    []string{"CONVENTIONS.md"},
		TicketContextIDs: []string{"ticket-2"},
	})
	if err != nil {
		t.Fatalf("save alternate scenario: %v", err)
	}

	replayResult, err := ReplayEvalScenarios(dataDir, workspaceID, issueID, EvalScenarioReplayRequest{
		Runtime: "opencode",
		Model:   "fake/test-model",
	})
	if err != nil {
		t.Fatalf("replay eval scenarios: %v", err)
	}
	if len(replayResult.QueuedRuns) != 2 {
		t.Fatalf("expected two queued runs, got %#v", replayResult)
	}
	if !slices.Equal(replayResult.ScenarioIDs, []string{baselineScenario.ScenarioID, alternateScenario.ScenarioID}) {
		t.Fatalf("unexpected replay scenario ids: %#v", replayResult.ScenarioIDs)
	}
	for _, run := range replayResult.QueuedRuns {
		waitForRunStatus(t, dataDir, workspaceID, run.RunID, "completed")
		waitForRunCleanup(t, run.RunID)
	}

	scenarios, err := ListEvalScenarios(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list eval scenarios: %v", err)
	}
	for _, scenario := range scenarios {
		if len(scenario.RunIDs) == 0 {
			t.Fatalf("expected replayed scenario to record fresh runs: %#v", scenario)
		}
	}

	report, err := GetEvalReport(dataDir, workspaceID, "")
	if err != nil {
		t.Fatalf("get eval report: %v", err)
	}
	var baselineReport *EvalScenarioReport
	var alternateReport *EvalScenarioReport
	for index := range report.ScenarioReports {
		item := &report.ScenarioReports[index]
		switch item.Scenario.ScenarioID {
		case baselineScenario.ScenarioID:
			baselineReport = item
		case alternateScenario.ScenarioID:
			alternateReport = item
		}
	}
	if baselineReport == nil || baselineReport.LatestFreshRun == nil {
		t.Fatalf("expected baseline fresh run summary: %#v", report.ScenarioReports)
	}
	if alternateReport == nil || alternateReport.LatestFreshRun == nil {
		t.Fatalf("expected alternate fresh run summary: %#v", report.ScenarioReports)
	}
	if alternateReport.FreshComparisonToBaseline == nil {
		t.Fatalf("expected alternate fresh comparison to baseline: %#v", alternateReport)
	}
	if alternateReport.FreshComparisonToBaseline.ComparedToScenarioID != baselineScenario.ScenarioID {
		t.Fatalf("unexpected fresh comparison baseline: %#v", alternateReport.FreshComparisonToBaseline)
	}
	if alternateReport.LatestFreshRun.RunID == baselineReport.LatestFreshRun.RunID {
		t.Fatalf("expected distinct fresh runs per scenario, got %#v and %#v", alternateReport.LatestFreshRun, baselineReport.LatestFreshRun)
	}
}

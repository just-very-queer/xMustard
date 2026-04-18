package workspaceops

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSettingsAndCapabilitiesUsePersistedConfig(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
	opencodeBin := writeFakeOpencode(t)

	settings, err := GetSettings(dataDir)
	if err != nil {
		t.Fatalf("get default settings: %v", err)
	}
	if settings.LocalAgentType != "codex" {
		t.Fatalf("unexpected default settings: %#v", settings)
	}

	updated, err := UpdateSettings(dataDir, AppSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &opencodeBin,
		OpencodeModel:  stringPtr("fake/test-model"),
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if updated.LocalAgentType != "opencode" || updated.OpencodeBin == nil || *updated.OpencodeBin != opencodeBin {
		t.Fatalf("unexpected updated settings: %#v", updated)
	}

	runtimes, err := DetectRuntimes(dataDir)
	if err != nil {
		t.Fatalf("detect runtimes: %v", err)
	}
	if len(runtimes) != 2 {
		t.Fatalf("unexpected runtimes: %#v", runtimes)
	}

	capabilities, err := GetLocalAgentCapabilities(dataDir)
	if err != nil {
		t.Fatalf("get capabilities: %v", err)
	}
	if capabilities.SelectedRuntime != "opencode" || capabilities.SupportsLiveSubscribe {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}

	probe, err := ProbeRuntime(dataDir, workspaceID, "opencode", "fake/test-model")
	if err != nil {
		t.Fatalf("probe runtime: %v", err)
	}
	if !probe.OK || probe.OutputExcerpt == nil {
		t.Fatalf("unexpected probe result: %#v", probe)
	}
}

func TestProbeRuntimeReturnsExitCodeAndFallbackOutputOnFailure(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
	opencodeBin := writeExecutableScript(t, "opencode", `#!/bin/sh
if [ "$1" = "models" ]; then
  echo "fake/test-model"
  exit 0
fi
if [ "$1" = "run" ]; then
  echo "probe failed on stderr" >&2
  exit 7
fi
echo "unknown command" >&2
exit 1
`)
	if _, err := UpdateSettings(dataDir, AppSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &opencodeBin,
		OpencodeModel:  stringPtr("fake/test-model"),
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	probe, err := ProbeRuntime(dataDir, workspaceID, "opencode", "fake/test-model")
	if err != nil {
		t.Fatalf("probe runtime failure path: %v", err)
	}
	if probe.OK {
		t.Fatalf("expected failed probe: %#v", probe)
	}
	if probe.ExitCode == nil || *probe.ExitCode != 7 {
		t.Fatalf("expected exit code 7, got %#v", probe)
	}
	if probe.Error == nil || !strings.Contains(*probe.Error, "probe failed on stderr") {
		t.Fatalf("expected probe error summary, got %#v", probe)
	}
	if probe.OutputExcerpt == nil || !strings.Contains(*probe.OutputExcerpt, "probe failed on stderr") {
		t.Fatalf("expected probe output excerpt, got %#v", probe)
	}
}

func TestStartIssueRunAndWorkspaceQueryPersistRunArtifacts(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)
	opencodeBin := writeFakeOpencode(t)
	if _, err := UpdateSettings(dataDir, AppSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &opencodeBin,
		OpencodeModel:  stringPtr("fake/test-model"),
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	run, err := StartIssueRun(dataDir, workspaceID, issueID, RunRequest{
		Runtime:     "opencode",
		Model:       "fake/test-model",
		Instruction: stringPtr("Please be concise"),
		RunbookID:   stringPtr("fix"),
		Planning:    false,
	})
	if err != nil {
		t.Fatalf("start issue run: %v", err)
	}
	if run.Status != "queued" || run.RunbookID == nil || *run.RunbookID != "fix" {
		t.Fatalf("unexpected run: %#v", run)
	}
	final := waitForRunStatus(t, dataDir, workspaceID, run.RunID, "completed")
	waitForRunCleanup(t, run.RunID)
	if final.Summary == nil {
		t.Fatalf("expected completed run summary: %#v", final)
	}

	planningRun, err := StartIssueRun(dataDir, workspaceID, issueID, RunRequest{
		Runtime:  "opencode",
		Model:    "fake/test-model",
		Planning: true,
	})
	if err != nil {
		t.Fatalf("start planning run: %v", err)
	}
	if planningRun.Status != "planning" {
		t.Fatalf("expected planning status, got %#v", planningRun)
	}

	queryRun, err := StartAgentQuery(dataDir, workspaceID, AgentQueryRequest{
		Runtime: "opencode",
		Model:   "fake/test-model",
		Prompt:  "Summarize the repository state",
	})
	if err != nil {
		t.Fatalf("start agent query: %v", err)
	}
	if queryRun.IssueID != "workspace-query" || !strings.Contains(queryRun.Prompt, "Repository guidance to respect:") {
		t.Fatalf("unexpected query run: %#v", queryRun)
	}
	waitForRunStatus(t, dataDir, workspaceID, queryRun.RunID, "completed")
	waitForRunCleanup(t, queryRun.RunID)
}

func TestStartIssueRunWithEvalScenarioPersistsScenarioRun(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)
	opencodeBin := writeFakeOpencode(t)
	if _, err := UpdateSettings(dataDir, AppSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &opencodeBin,
		OpencodeModel:  stringPtr("fake/test-model"),
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if _, err := SaveVerificationProfile(dataDir, workspaceID, VerificationProfileUpsertRequest{
		ProfileID:   stringPtr("backend-pytest"),
		Name:        "Backend pytest",
		Description: "Backend verification",
		TestCommand: "pytest -q",
	}); err != nil {
		t.Fatalf("save verification profile: %v", err)
	}
	scenario, err := SaveEvalScenario(dataDir, workspaceID, EvalScenarioUpsertRequest{
		Name:                   "Eval fresh execution",
		IssueID:                issueID,
		GuidancePaths:          []string{"AGENTS.md"},
		VerificationProfileIDs: []string{"backend-pytest"},
	})
	if err != nil {
		t.Fatalf("save eval scenario: %v", err)
	}

	run, err := StartIssueRun(dataDir, workspaceID, issueID, RunRequest{
		Runtime:        "opencode",
		Model:          "fake/test-model",
		Instruction:    stringPtr("Focus on export regression"),
		EvalScenarioID: &scenario.ScenarioID,
	})
	if err != nil {
		t.Fatalf("start eval scenario run: %v", err)
	}
	if run.EvalScenarioID == nil || *run.EvalScenarioID != scenario.ScenarioID {
		t.Fatalf("expected eval scenario on run: %#v", run)
	}
	if !strings.Contains(run.Prompt, "Evaluation scenario: Eval fresh execution") {
		t.Fatalf("expected eval scenario prompt overlay: %#v", run.Prompt)
	}
	waitForRunStatus(t, dataDir, workspaceID, run.RunID, "completed")
	waitForRunCleanup(t, run.RunID)
	scenarios, err := ListEvalScenarios(dataDir, workspaceID, issueID)
	if err != nil {
		t.Fatalf("list eval scenarios: %v", err)
	}
	updated := scenarios[0]
	if !slices.Contains(updated.RunIDs, run.RunID) {
		t.Fatalf("expected run to be recorded on eval scenario: %#v", updated)
	}
}

func TestStartIssueRunRejectsUnavailableRuntimeModel(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)
	brokenPath := filepath.Join(t.TempDir(), "missing-opencode")
	if _, err := UpdateSettings(dataDir, AppSettings{
		LocalAgentType: "opencode",
		OpencodeBin:    &brokenPath,
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	_, err := StartIssueRun(dataDir, workspaceID, issueID, RunRequest{
		Runtime: "opencode",
		Model:   "fake/test-model",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "runtime") {
		t.Fatalf("expected runtime error, got %v", err)
	}
}

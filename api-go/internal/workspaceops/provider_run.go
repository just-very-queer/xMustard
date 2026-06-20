package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Provider-backed run execution makes the OpenAI-compatible provider layer a
// first-class run-execution runtime: instead of shelling out to the codex/opencode
// CLIs, a run executes by calling a model directly (via OpenAIChat) and records the
// same runRecord the CLI path produces — so it shows up in run history, can be
// explained by ExplainRunFailure, etc.
//
// A run uses this path when its runtime is "provider:<name>" (an explicit provider)
// or "route" (task-typed routing picks the provider+model). Everything else falls
// through to the existing CLI runtimes.

// isProviderRuntime reports whether a runtime string selects the provider path and
// returns the provider name ("" means route by task type).
func isProviderRuntime(runtime string) (provider string, ok bool) {
	runtime = strings.TrimSpace(runtime)
	if runtime == "route" {
		return "", true
	}
	if name, found := strings.CutPrefix(runtime, "provider:"); found {
		return strings.TrimSpace(name), true
	}
	return "", false
}

// StartProviderRun executes a prompt against an OpenAI-compatible provider and
// records a completed runRecord. The verified shared context is injected into the
// prompt, just like the CLI run paths.
func StartProviderRun(dataDir, workspaceID, providerName, model, prompt string) (*runRecord, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	grounded := applyActiveContextToPrompt(dataDir, workspaceID, prompt)

	// Resolve provider + model: explicit name, or task-typed routing.
	chosenProvider, chosenModel := providerName, model
	var routeReason string
	if chosenProvider == "" {
		route, err := RouteModel(dataDir, RouteRequest{Prompt: prompt})
		if err != nil {
			return nil, err
		}
		if route.Provider == "" {
			return nil, fmt.Errorf("no provider available to route this run")
		}
		chosenProvider, chosenModel, routeReason = route.Provider, route.Model, route.Reason
	}

	runID := "run_" + hashID(workspaceID, "provider", nowUTC())[:12]
	runtimeLabel := "provider:" + chosenProvider
	run := runRecord{
		RunID:          runID,
		WorkspaceID:    workspaceID,
		IssueID:        "provider-run",
		Runtime:        runtimeLabel,
		Model:          chosenModel,
		Status:         "running",
		Title:          runtimeLabel + ":" + chosenModel,
		Prompt:         grounded,
		Command:        []string{runtimeLabel, chosenModel},
		CommandPreview: runtimeLabel + " " + chosenModel,
		OutputPath:     filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".out.json"),
		CreatedAt:      nowUTC(),
		GuidancePaths:  []string{},
	}
	if err := saveRunRecord(dataDir, run); err != nil {
		return nil, err
	}

	// Execute the model call and capture its output as the run output.
	chat, chatErr := OpenAIChat(dataDir, chosenProvider, ChatRequest{Model: chosenModel, Prompt: grounded})
	completedAt := nowUTC()
	run.CompletedAt = &completedAt
	if chatErr != nil {
		run.Status = "failed"
		errText := chatErr.Error()
		run.Error = &errText
		code := 1
		run.ExitCode = &code
		_ = os.WriteFile(run.OutputPath, []byte(errText), 0o644)
	} else {
		run.Status = "succeeded"
		code := 0
		run.ExitCode = &code
		content := ""
		if c, ok := chat["content"].(string); ok {
			content = c
		}
		_ = os.WriteFile(run.OutputPath, []byte(content), 0o644)
		run.Summary = map[string]any{"served_by": chat["served_by"], "vision": chat["vision"]}
	}
	if routeReason != "" {
		if run.Summary == nil {
			run.Summary = map[string]any{}
		}
		run.Summary["route_reason"] = routeReason
	}
	_ = saveRunRecord(dataDir, run)

	_ = appendRunActivityWithActor(
		dataDir, workspaceID, run.IssueID, runID, "run.provider",
		fmt.Sprintf("Provider run on %s (%s) finished: %s", chosenProvider, chosenModel, run.Status),
		activityActor{
			Kind:    "agent",
			Name:    chosenProvider,
			Runtime: ptr(runtimeLabel),
			Model:   ptr(chosenModel),
			Key:     "agent:" + runtimeLabel + ":" + chosenModel,
			Label:   runtimeLabel + ":" + chosenModel,
		},
		map[string]any{"provider": chosenProvider, "model": chosenModel, "status": run.Status},
	)
	_ = snapshot
	return &run, nil
}

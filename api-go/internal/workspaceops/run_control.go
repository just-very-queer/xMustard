package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

var activeRunProcesses sync.Map
var cancelledRunIDs sync.Map

var defaultCodexModels = []string{
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex",
	"gpt-5.3-codex-spark",
	"gpt-5.2-codex",
}

type appSettings struct {
	LocalAgentType string  `json:"local_agent_type"`
	CodexBin       *string `json:"codex_bin"`
	OpencodeBin    *string `json:"opencode_bin"`
	CodexArgs      *string `json:"codex_args"`
	CodexModel     *string `json:"codex_model"`
	OpencodeModel  *string `json:"opencode_model"`
}

type PlanApproveRequest struct {
	Feedback *string `json:"feedback"`
}

type PlanRejectRequest struct {
	Reason string `json:"reason"`
}

func CancelRun(dataDir string, workspaceID string, runID string) (*runRecord, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	cancelledRunIDs.Store(runID, struct{}{})
	if processValue, ok := activeRunProcesses.Load(runID); ok {
		if cmd, ok := processValue.(*exec.Cmd); ok && cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	} else if run.PID != nil && *run.PID > 0 {
		process, findErr := os.FindProcess(*run.PID)
		if findErr == nil && process != nil {
			signalErr := process.Signal(syscall.SIGTERM)
			if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
				return nil, signalErr
			}
		}
	}
	completedAt := nowUTC()
	exitCode := -15
	run.Status = "cancelled"
	run.CompletedAt = &completedAt
	if run.ExitCode == nil {
		run.ExitCode = &exitCode
	}
	if err := saveRunRecord(dataDir, *run); err != nil {
		return nil, err
	}
	if err := appendRunActivityWithActor(
		dataDir,
		workspaceID,
		run.IssueID,
		runID,
		"run.cancelled",
		"Cancelled run "+runID,
		operatorActor(),
		map[string]any{"runtime": run.Runtime, "model": run.Model},
	); err != nil {
		return nil, err
	}
	return run, nil
}

func RetryRun(dataDir string, workspaceID string, runID string) (*runRecord, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := validateRuntimeModel(dataDir, run.Runtime, run.Model); err != nil {
		return nil, err
	}
	command, err := buildRuntimeCommand(dataDir, run.Runtime, run.Model, snapshot.Workspace.RootPath, run.Prompt)
	if err != nil {
		return nil, err
	}
	newRunID := "run_" + hashID(workspaceID, run.IssueID, nowUTC())[:12]
	worktree := run.Worktree
	if worktree == nil {
		worktree = readWorktreeStatus(snapshot.Workspace.RootPath)
	}
	retried := runRecord{
		RunID:          newRunID,
		WorkspaceID:    workspaceID,
		IssueID:        run.IssueID,
		Runtime:        run.Runtime,
		Model:          run.Model,
		Status:         "queued",
		Title:          fmt.Sprintf("%s:%s", run.Runtime, run.IssueID),
		Prompt:         run.Prompt,
		Command:        command,
		CommandPreview: shellPreview(command),
		LogPath:        filepath.Join(dataDir, "workspaces", workspaceID, "runs", newRunID+".log"),
		OutputPath:     filepath.Join(dataDir, "workspaces", workspaceID, "runs", newRunID+".out.json"),
		CreatedAt:      nowUTC(),
		RunbookID:      run.RunbookID,
		Worktree:       worktree,
		GuidancePaths:  append([]string{}, run.GuidancePaths...),
	}
	if err := saveRunRecord(dataDir, retried); err != nil {
		return nil, err
	}
	startManagedRun(dataDir, retried, snapshot.Workspace.RootPath)
	if err := appendRunActivityWithActor(
		dataDir,
		workspaceID,
		retried.IssueID,
		retried.RunID,
		"run.retried",
		fmt.Sprintf("Retried %s run for %s", run.Runtime, run.IssueID),
		activityActor{
			Kind:    "agent",
			Name:    run.Runtime,
			Runtime: ptr(run.Runtime),
			Model:   ptr(run.Model),
			Key:     "agent:" + run.Runtime + ":" + run.Model,
			Label:   run.Runtime + ":" + run.Model,
		},
		map[string]any{"previous_run_id": run.RunID, "runtime": run.Runtime, "model": run.Model},
	); err != nil {
		return nil, err
	}
	return &retried, nil
}

func GenerateRunPlan(dataDir string, workspaceID string, runID string) (*RunPlan, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != "queued" && run.Status != "planning" {
		return nil, fmt.Errorf("cannot generate plan for run in status %s", run.Status)
	}
	runbookID := firstNonEmptyPtr(run.RunbookID)
	packet, err := BuildIssueWorkPacket(dataDir, workspaceID, run.IssueID, runbookID)
	if err != nil {
		return nil, err
	}
	result, err := callAgentForPlan(dataDir, run.Runtime, run.Model, packet.Workspace.RootPath, buildPlanningPrompt(packet))
	if err != nil {
		return nil, err
	}
	plan := &RunPlan{
		PlanID:    "plan_" + hashID(workspaceID, runID, nowUTC())[:12],
		RunID:     runID,
		Phase:     "awaiting_approval",
		Steps:     result.Steps,
		Summary:   result.Summary,
		Reasoning: result.Reasoning,
		CreatedAt: nowUTC(),
	}
	run.Status = "planning"
	run.Plan = plan
	if err := saveRunRecord(dataDir, *run); err != nil {
		return nil, err
	}
	if err := appendRunActivityWithActor(
		dataDir,
		workspaceID,
		run.IssueID,
		runID,
		"run.plan_generated",
		"Generated plan for "+run.IssueID,
		activityActor{
			Kind:    "agent",
			Name:    run.Runtime,
			Runtime: ptr(run.Runtime),
			Model:   ptr(run.Model),
			Key:     "agent:" + run.Runtime + ":" + run.Model,
			Label:   run.Runtime + ":" + run.Model,
		},
		map[string]any{"plan_id": plan.PlanID, "step_count": len(plan.Steps)},
	); err != nil {
		return nil, err
	}
	return plan, nil
}

func GetRunPlan(dataDir string, workspaceID string, runID string) (*RunPlan, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	if run.Plan == nil {
		return nil, fmt.Errorf("no plan found for run %s", runID)
	}
	return run.Plan, nil
}

func ApproveRunPlan(dataDir string, workspaceID string, runID string, request PlanApproveRequest) (*RunPlan, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	if run.Plan == nil {
		return nil, os.ErrNotExist
	}
	if run.Plan.Phase != "awaiting_approval" && run.Plan.Phase != "modified" {
		return nil, fmt.Errorf("plan is not awaiting approval (phase: %s)", run.Plan.Phase)
	}
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	approvedAt := nowUTC()
	approver := "operator"
	feedback := trimOptional(request.Feedback)
	modifiedSummary := (*string)(nil)
	if feedback != nil && run.Plan.Phase == "modified" {
		modifiedSummary = feedback
	}
	run.Plan.Phase = "approved"
	run.Plan.ApprovedAt = &approvedAt
	run.Plan.Approver = &approver
	run.Plan.Feedback = feedback
	run.Plan.ModifiedSummary = modifiedSummary
	run.Status = "queued"
	if err := saveRunRecord(dataDir, *run); err != nil {
		return nil, err
	}
	startManagedRun(dataDir, *run, snapshot.Workspace.RootPath)
	if err := appendRunActivityWithActor(
		dataDir,
		workspaceID,
		run.IssueID,
		runID,
		"run.plan_approved",
		"Approved plan for "+run.IssueID,
		operatorActor(),
		map[string]any{"feedback": feedback},
	); err != nil {
		return nil, err
	}
	return run.Plan, nil
}

func RejectRunPlan(dataDir string, workspaceID string, runID string, request PlanRejectRequest) (*RunPlan, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	if run.Plan == nil {
		return nil, os.ErrNotExist
	}
	run.Plan.Phase = "rejected"
	reason := strings.TrimSpace(request.Reason)
	if reason != "" {
		run.Plan.Feedback = &reason
	}
	run.Status = "cancelled"
	if err := saveRunRecord(dataDir, *run); err != nil {
		return nil, err
	}
	if err := appendRunActivityWithActor(
		dataDir,
		workspaceID,
		run.IssueID,
		runID,
		"run.plan_rejected",
		fmt.Sprintf("Rejected plan for %s: %s", run.IssueID, reason),
		operatorActor(),
		map[string]any{"reason": reason},
	); err != nil {
		return nil, err
	}
	return run.Plan, nil
}

func startManagedRun(dataDir string, run runRecord, workspaceRoot string) {
	go runManagedProcess(dataDir, run, workspaceRoot)
}

func runManagedProcess(dataDir string, run runRecord, workspaceRoot string) {
	persisted, err := loadRun(dataDir, run.WorkspaceID, run.RunID)
	if err != nil || persisted == nil {
		return
	}
	if persisted.Status == "planning" || persisted.Status == "cancelled" {
		return
	}

	if err := os.MkdirAll(filepath.Dir(run.LogPath), 0o755); err != nil {
		saveRunFailure(dataDir, run, err)
		return
	}
	logHandle, err := os.Create(run.LogPath)
	if err != nil {
		saveRunFailure(dataDir, run, err)
		return
	}
	defer logHandle.Close()

	command := exec.Command(run.Command[0], run.Command[1:]...)
	command.Dir = workspaceRoot
	command.Stdin = nil
	var output strings.Builder
	multi := io.MultiWriter(logHandle, &output)
	command.Stdout = multi
	command.Stderr = multi
	if err := command.Start(); err != nil {
		saveRunFailure(dataDir, run, err)
		return
	}
	activeRunProcesses.Store(run.RunID, command)
	startedAt := nowUTC()
	pid := command.Process.Pid
	current := run
	current.Status = "running"
	current.StartedAt = &startedAt
	current.PID = &pid
	_ = saveRunRecord(dataDir, current)

	waitErr := command.Wait()
	combinedOutput := output.String()
	_ = os.WriteFile(run.OutputPath, []byte(combinedOutput), 0o644)
	summary := summarizeRunOutput(run.Runtime, combinedOutput)
	persisted, _ = loadRun(dataDir, run.WorkspaceID, run.RunID)
	finalStatus := "completed"
	if _, cancelled := cancelledRunIDs.Load(run.RunID); cancelled || (persisted != nil && persisted.Status == "cancelled") {
		finalStatus = "cancelled"
	} else if waitErr != nil {
		finalStatus = "failed"
	}
	completedAt := nowUTC()
	exitCode := 0
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	final := current
	final.Status = finalStatus
	final.CompletedAt = &completedAt
	final.ExitCode = &exitCode
	final.Summary = summary
	if waitErr != nil && finalStatus == "failed" {
		errText := waitErr.Error()
		final.Error = &errText
	}
	_ = saveRunRecord(dataDir, final)
	metrics := calculateRunMetrics(final, len(combinedOutput))
	_ = saveRunMetricsRecord(dataDir, metrics)
	action := "run.completed"
	if finalStatus != "completed" {
		action = "run." + finalStatus
	}
	_ = appendRunActivityWithActor(
		dataDir,
		run.WorkspaceID,
		run.IssueID,
		run.RunID,
		action,
		strings.ReplaceAll(action, ".", " ")+" for "+run.IssueID,
		activityActor{
			Kind:    "agent",
			Name:    run.Runtime,
			Runtime: ptr(run.Runtime),
			Model:   ptr(run.Model),
			Key:     "agent:" + run.Runtime + ":" + run.Model,
			Label:   run.Runtime + ":" + run.Model,
		},
		map[string]any{"status": final.Status, "exit_code": final.ExitCode, "runtime": final.Runtime, "model": final.Model},
	)
	activeRunProcesses.Delete(run.RunID)
	cancelledRunIDs.Delete(run.RunID)
}

func saveRunFailure(dataDir string, run runRecord, err error) {
	completedAt := nowUTC()
	failed := run
	failed.Status = "failed"
	failed.CompletedAt = &completedAt
	errText := err.Error()
	failed.Error = &errText
	failed.Summary = map[string]any{"event_count": 0, "tool_event_count": 0, "text_excerpt": nil, "last_event_type": nil}
	_ = saveRunRecord(dataDir, failed)
	metrics := calculateRunMetrics(failed, 0)
	_ = saveRunMetricsRecord(dataDir, metrics)
	_ = os.WriteFile(run.LogPath, []byte(errText), 0o644)
	_ = appendRunActivityWithActor(
		dataDir,
		run.WorkspaceID,
		run.IssueID,
		run.RunID,
		"run.failed",
		"run failed for "+run.IssueID,
		activityActor{
			Kind:    "agent",
			Name:    run.Runtime,
			Runtime: ptr(run.Runtime),
			Model:   ptr(run.Model),
			Key:     "agent:" + run.Runtime + ":" + run.Model,
			Label:   run.Runtime + ":" + run.Model,
		},
		map[string]any{"status": failed.Status, "runtime": failed.Runtime, "model": failed.Model, "error": errText},
	)
}

func loadSettings(dataDir string) (*appSettings, error) {
	path := filepath.Join(dataDir, "settings.json")
	settings := &appSettings{LocalAgentType: "codex"}
	if err := readJSON(path, settings); err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return nil, err
	}
	if strings.TrimSpace(settings.LocalAgentType) == "" {
		settings.LocalAgentType = "codex"
	}
	return settings, nil
}

func validateRuntimeModel(dataDir string, runtime string, model string) error {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	switch runtime {
	case "codex":
		binary := resolveBinary(settings.CodexBin, "codex")
		if binary == "" {
			return fmt.Errorf("runtime %s is not available", runtime)
		}
		if !slices.Contains(defaultCodexModels, model) {
			return fmt.Errorf("model %s is not available for runtime %s", model, runtime)
		}
	case "opencode":
		binary := resolveBinary(settings.OpencodeBin, "opencode")
		if binary == "" {
			return fmt.Errorf("runtime %s is not available", runtime)
		}
		models := detectOpencodeModels(binary)
		if len(models) > 0 && !slices.Contains(models, model) {
			return fmt.Errorf("model %s is not available for runtime %s", model, runtime)
		}
	default:
		return fmt.Errorf("runtime %s is not available", runtime)
	}
	return nil
}

func buildRuntimeCommand(dataDir string, runtime string, model string, workspacePath string, prompt string) ([]string, error) {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	if runtime == "codex" {
		codexBin := resolveBinary(settings.CodexBin, "codex")
		if codexBin == "" {
			return nil, fmt.Errorf("runtime %s is not available", runtime)
		}
		args := sanitizeCodexArgs(firstNonEmptyPtr(settings.CodexArgs))
		return append([]string{
			codexBin,
			"exec",
			"--json",
			"--skip-git-repo-check",
			"-s",
			"workspace-write",
			"-C",
			workspacePath,
			"-m",
			model,
		}, append(args, prompt)...), nil
	}
	opencodeBin := resolveBinary(settings.OpencodeBin, "opencode")
	if opencodeBin == "" {
		return nil, fmt.Errorf("runtime %s is not available", runtime)
	}
	return []string{
		opencodeBin,
		"run",
		"--format",
		"json",
		"--dir",
		workspacePath,
		"-m",
		model,
		prompt,
	}, nil
}

func detectOpencodeModels(binary string) []string {
	command := exec.Command(binary, "models")
	output, err := command.Output()
	if err != nil {
		return nil
	}
	items := []string{}
	seen := map[string]struct{}{}
	for _, rawLine := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line := strings.TrimSpace(strings.TrimLeft(rawLine, "-* "))
		if line == "" {
			continue
		}
		token := strings.Fields(line)[0]
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		items = append(items, token)
	}
	return items
}

func resolveBinary(configuredValue *string, defaultName string) string {
	candidate := firstNonEmptyPtr(configuredValue)
	if candidate != "" {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
		return ""
	}
	resolved, err := exec.LookPath(defaultName)
	if err != nil {
		return ""
	}
	return resolved
}

func sanitizeCodexArgs(raw string) []string {
	fields := strings.Fields(raw)
	blockedWithValue := map[string]struct{}{
		"-m": {}, "--model": {}, "-C": {}, "--cd": {}, "--cwd": {}, "-s": {}, "--sandbox": {}, "--sandbox-mode": {},
		"-a": {}, "--ask-for-approval": {}, "--approval-mode": {},
	}
	blockedExact := map[string]struct{}{"exec": {}, "--json": {}, "--skip-git-repo-check": {}}
	result := []string{}
	skipNext := false
	for _, field := range fields {
		if skipNext {
			skipNext = false
			continue
		}
		if _, blocked := blockedWithValue[field]; blocked {
			skipNext = true
			continue
		}
		if _, blocked := blockedExact[field]; blocked {
			continue
		}
		result = append(result, field)
	}
	return result
}

type planCommandResult struct {
	Summary   string
	Reasoning *string
	Steps     []PlanStep
}

func callAgentForPlan(dataDir string, runtime string, model string, workspacePath string, prompt string) (*planCommandResult, error) {
	commandArgs, err := buildRuntimeCommand(dataDir, runtime, model, workspacePath, prompt)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, commandArgs[0], commandArgs[1:]...)
	command.Dir = workspacePath
	output, _ := command.CombinedOutput()
	text := strings.TrimSpace(string(output))
	for _, line := range strings.Split(text, "\n") {
		if parsed, ok := parsePlanJSON(strings.TrimSpace(line)); ok {
			return parsed, nil
		}
	}
	if parsed, ok := parsePlanJSON(text); ok {
		return parsed, nil
	}
	summary := text
	if summary == "" {
		if ctx.Err() == context.DeadlineExceeded {
			summary = "Planning timed out"
		} else {
			summary = "Planning produced no structured output"
		}
	}
	return &planCommandResult{Summary: summary, Steps: []PlanStep{}}, nil
}

func parsePlanJSON(text string) (*planCommandResult, bool) {
	if !strings.HasPrefix(strings.TrimSpace(text), "{") {
		return nil, false
	}
	var payload struct {
		Summary   string     `json:"summary"`
		Reasoning *string    `json:"reasoning"`
		Steps     []PlanStep `json:"steps"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return nil, false
	}
	if strings.TrimSpace(payload.Summary) == "" {
		return nil, false
	}
	return &planCommandResult{Summary: payload.Summary, Reasoning: payload.Reasoning, Steps: payload.Steps}, true
}

func buildPlanningPrompt(packet *IssueContextPacket) string {
	evidenceLines := []string{}
	for _, evidence := range packet.EvidenceBundle[:min(len(packet.EvidenceBundle), 5)] {
		line := "  - " + evidence.Path
		if evidence.Line != nil {
			line += fmt.Sprintf(":%d", *evidence.Line)
		}
		if evidence.Excerpt != nil && strings.TrimSpace(*evidence.Excerpt) != "" {
			line += " - " + strings.TrimSpace(*evidence.Excerpt)
		}
		evidenceLines = append(evidenceLines, line)
	}
	fixLines := []string{}
	for _, fix := range packet.RecentFixes[:min(len(packet.RecentFixes), 3)] {
		fixLines = append(fixLines, fmt.Sprintf("  - %s (%s)", fix.Summary, fix.Status))
	}
	return fmt.Sprintf(`You are a bug fixing assistant. Generate a structured plan to address the following issue.

ISSUE: %s
Title: %s
Severity: %s
Summary: %s
Impact: %s

Evidence:
%s

Recent fixes for context:
%s

Your task is to generate a concise fix plan. Consider:
1. What files need to be modified
2. What the actual fix should be
3. What tests should be added or updated
4. What risks this fix might introduce

Respond with a JSON object containing:
{
  "summary": "Brief one-line summary of the fix approach",
  "reasoning": "Brief explanation of why this approach was chosen",
  "steps": [
    {
      "step_id": "step_1",
      "description": "What to do in this step",
      "estimated_impact": "low|medium|high",
      "files_affected": ["file1.py", "file2.py"],
      "risks": ["risk1", "risk2"]
    }
  ]
}`, packet.Issue.BugID, packet.Issue.Title, packet.Issue.Severity,
		fallbackString(firstNonEmptyPtr(packet.Issue.Summary), "No summary provided"),
		fallbackString(firstNonEmptyPtr(packet.Issue.Impact), "No impact provided"),
		strings.Join(evidenceLines, "\n"),
		strings.Join(fixLines, "\n"))
}

func saveRunRecord(dataDir string, run runRecord) error {
	return writeJSON(filepath.Join(dataDir, "workspaces", run.WorkspaceID, "runs", run.RunID+".json"), run)
}

func appendRunActivityWithActor(dataDir string, workspaceID string, issueID string, runID string, action string, summary string, actor activityActor, details map[string]any) error {
	createdAt := nowUTC()
	record := activityRecord{
		ActivityID:  hashID(workspaceID, "run", runID, action, createdAt),
		WorkspaceID: workspaceID,
		EntityType:  "run",
		EntityID:    runID,
		Action:      action,
		Summary:     summary,
		Actor:       actor,
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

func summarizeRunOutput(runtime string, output string) map[string]any {
	eventCount := 0
	toolEventCount := 0
	var sessionID any
	var lastEventType any
	textChunks := []string{}
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			continue
		}
		eventCount++
		if eventType, ok := payload["type"].(string); ok {
			lastEventType = eventType
			if eventType == "tool_use" {
				toolEventCount++
			}
		}
		if sessionID == nil {
			sessionID = payload["sessionID"]
			if sessionID == nil {
				sessionID = payload["session_id"]
			}
		}
		if text := extractRunText(payload); strings.TrimSpace(text) != "" {
			textChunks = append(textChunks, strings.TrimSpace(text))
		}
	}
	excerpt := strings.TrimSpace(strings.Join(textChunks, "\n"))
	if len(excerpt) > 1400 {
		excerpt = strings.TrimSpace(excerpt[:1400]) + "..."
	}
	var excerptValue any
	if excerpt != "" {
		excerptValue = excerpt
	}
	return map[string]any{
		"runtime":          runtime,
		"session_id":       sessionID,
		"event_count":      eventCount,
		"tool_event_count": toolEventCount,
		"last_event_type":  lastEventType,
		"text_excerpt":     excerptValue,
	}
}

func extractRunText(payload map[string]any) string {
	if direct, ok := payload["text"].(string); ok && strings.TrimSpace(direct) != "" {
		return direct
	}
	if part, ok := payload["part"].(map[string]any); ok {
		if text, ok := part["text"].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	if message, ok := payload["message"].(map[string]any); ok {
		if text, ok := message["text"].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func calculateRunMetrics(run runRecord, outputLength int) RunMetrics {
	inputTokens := estimateTokens(run.Prompt)
	outputTokens := estimateTokens(outputLength)
	durationMS := 0
	if run.StartedAt != nil && run.CompletedAt != nil {
		if start, err := time.Parse(time.RFC3339Nano, strings.ReplaceAll(*run.StartedAt, "Z", "+00:00")); err == nil {
			if end, err := time.Parse(time.RFC3339Nano, strings.ReplaceAll(*run.CompletedAt, "Z", "+00:00")); err == nil {
				durationMS = int(end.Sub(start).Milliseconds())
			}
		}
	}
	return RunMetrics{
		RunID:         run.RunID,
		WorkspaceID:   run.WorkspaceID,
		InputTokens:   inputTokens,
		OutputTokens:  outputTokens,
		EstimatedCost: calculateCost(run.Model, inputTokens, outputTokens),
		DurationMS:    durationMS,
		Model:         run.Model,
		Runtime:       run.Runtime,
		CalculatedAt:  nowUTC(),
	}
}

func saveRunMetricsRecord(dataDir string, metrics RunMetrics) error {
	return writeJSON(filepath.Join(dataDir, "metrics", metrics.RunID+".json"), metrics)
}

func estimateTokens(value any) int {
	switch typed := value.(type) {
	case int:
		if typed < 0 {
			return 1
		}
		if typed/4 < 1 {
			return 1
		}
		return typed / 4
	case string:
		if len(typed)/4 < 1 {
			return 1
		}
		return len(typed) / 4
	default:
		return 1
	}
}

func calculateCost(model string, inputTokens int, outputTokens int) float64 {
	inputPer1K := 0.01
	outputPer1K := 0.03
	switch model {
	case "gpt-5.4-mini":
		inputPer1K = 0.005
		outputPer1K = 0.015
	case "gpt-5.3-codex":
		inputPer1K = 0.02
		outputPer1K = 0.06
	case "gpt-5.3-codex-spark":
		inputPer1K = 0.015
		outputPer1K = 0.045
	case "gpt-5.2-codex":
		inputPer1K = 0.025
		outputPer1K = 0.075
	}
	return roundCost((float64(inputTokens)/1000)*inputPer1K + (float64(outputTokens)/1000)*outputPer1K)
}

func shellPreview(command []string) string {
	parts := make([]string, 0, len(command))
	for _, part := range command {
		if strings.ContainsAny(part, " \t\n\"'") {
			parts = append(parts, strconvQuote(part))
		} else {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

func strconvQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

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
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"xmustard/api-go/internal/rustcore"
)

var activeRunProcesses sync.Map
var cancelledRunIDs sync.Map

// maxRunOutputBytes caps the in-RAM tail of a managed run's combined output (the
// full stream still goes to the run's .log file on disk).
const maxRunOutputBytes = 1 << 20 // 1 MiB

// boundedTail is an io.Writer that retains only the last `max` bytes written while
// counting the total, so capturing a run's output can't grow the heap unbounded.
type boundedTail struct {
	buf       []byte
	max       int
	total     int64
	truncated bool
}

func (b *boundedTail) Write(p []byte) (int, error) {
	b.total += int64(len(p))
	if b.max <= 0 {
		return len(p), nil
	}
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		b.truncated = true
		return len(p), nil
	}
	if len(b.buf)+len(p) > b.max {
		b.buf = b.buf[len(b.buf)+len(p)-b.max:]
		b.truncated = true
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *boundedTail) String() string { return string(b.buf) }

var defaultCodexModels = []string{
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex",
	"gpt-5.3-codex-spark",
	"gpt-5.2-codex",
}

var opencodeModelTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*(/[A-Za-z0-9][A-Za-z0-9._:-]*)+$`)

var runManagedCommand = rustcore.RunManagedCommand
var planningCommandTimeout = 60 * time.Second
var planningRustCoreBuffer = 5 * time.Second

type appSettings struct {
	LocalAgentType string  `json:"local_agent_type"`
	CodexBin       *string `json:"codex_bin"`
	OpencodeBin    *string `json:"opencode_bin"`
	CodexArgs      *string `json:"codex_args"`
	CodexModel     *string `json:"codex_model"`
	OpencodeModel  *string `json:"opencode_model"`
	PostgresDSN    *string `json:"postgres_dsn"`
	PostgresSchema string  `json:"postgres_schema"`
	// Context-governance: whether shared-context entries must be verified by
	// multiple agents before promotion, and how many distinct approvals are needed.
	RequireMultiAgentVerification *bool `json:"require_multi_agent_verification,omitempty"`
	ContextVerificationThreshold  int   `json:"context_verification_threshold,omitempty"`
}

type PlanApproveRequest struct {
	Feedback *string `json:"feedback"`
}

type PlanRejectRequest struct {
	Reason string `json:"reason"`
}

// terminalRunStatuses are end states a run cannot transition out of.
func isTerminalRunStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "cancelled", "completed", "failed", "error":
		return true
	}
	return false
}

func CancelRun(dataDir string, workspaceID string, runID string) (*runRecord, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	// Idempotent: a run already in a terminal state cannot be cancelled, and must
	// NEVER be signalled — its persisted PID may have been reused by an unrelated
	// host process group since it completed (XM-NEW-006, host-safety).
	if isTerminalRunStatus(run.Status) {
		return run, nil
	}
	_, active := activeRunProcesses.Load(runID)
	cancelledRunIDs.Store(runID, struct{}{})
	if processValue, ok := activeRunProcesses.Load(runID); ok {
		// signal only the LIVE supervised process handle — never a bare persisted
		// PID (which the OS may have reused).
		if cmd, ok := processValue.(*exec.Cmd); ok && cmd.Process != nil {
			terminateManagedRunCommand(cmd)
		}
	}
	completedAt := nowUTC()
	exitCode := -15
	run.Status = "cancelled"
	run.CompletedAt = &completedAt
	run.PID = nil // clear the PID so nothing can signal it after reap
	if run.ExitCode == nil {
		run.ExitCode = &exitCode
	}
	if err := saveRunRecord(dataDir, *run); err != nil {
		return nil, err
	}
	// A run that was never active is not reaped by runManagedProcess; the durable
	// cancelled status (saved above) is the authoritative signal, so drop the
	// in-memory marker to keep cancelledRunIDs from accumulating.
	if !active {
		cancelledRunIDs.Delete(runID)
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
	// Only retry a run that has reached a terminal state. Retrying a queued/
	// planning/running run would start a second worker against the same worktree,
	// spend tokens twice, and race on files/verification records (XM-NEW-007).
	if !isTerminalRunStatus(run.Status) {
		return nil, fmt.Errorf("cannot retry a run in status %q; cancel it first", run.Status)
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
		return nil, Conflict(fmt.Sprintf("cannot generate plan for run in status %s", run.Status))
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
		return nil, NotFoundErr(fmt.Sprintf("no plan found for run %s", runID))
	}
	return run.Plan, nil
}

// runLockKey is the path-keyed transaction-lock key for a single run's record, so
// approval / launch / finalization of the same run serialize through lockStore.
func runLockKey(dataDir, workspaceID, runID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".json")
}

func ApproveRunPlan(dataDir string, workspaceID string, runID string, request PlanApproveRequest) (*RunPlan, error) {
	// Serialize the read-check-transition-launch for this run so two concurrent
	// approvals can't both pass the awaiting_approval check and both start a worker
	// against the same worktree (only one process handle is then cancellable).
	// The loser, once it holds the lock, re-reads phase=approved and bails (XM-PRO-002).
	unlock := lockStore(runLockKey(dataDir, workspaceID, runID))
	defer unlock()

	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	if run.Plan == nil {
		return nil, os.ErrNotExist
	}
	if run.Plan.Phase != "awaiting_approval" && run.Plan.Phase != "modified" {
		return nil, Conflict(fmt.Sprintf("plan is not awaiting approval (phase: %s)", run.Plan.Phase))
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
	configureManagedRunCommand(command)
	// The full stream is persisted to the .log file on disk (logHandle); the in-RAM
	// capture used for the output snapshot + summary is a bounded tail, so a chatty
	// long-running agent can't grow the API's heap without limit (XM-POST-005).
	output := &boundedTail{max: maxRunOutputBytes}
	multi := io.MultiWriter(logHandle, output)
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
	// surface the true byte count + whether the in-RAM snapshot was truncated (the
	// full stream remains in the .log file).
	summary["output_bytes"] = output.total
	summary["output_truncated"] = output.truncated
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
	final.PID = nil // reaped: clear the PID so a later cancel can't signal a reused PID
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

func configureManagedRunCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateManagedRunCommand(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	if err := signalManagedRunPID(command.Process.Pid); err == nil || errors.Is(err, os.ErrProcessDone) {
		return
	}
	_ = command.Process.Signal(syscall.SIGTERM)
}

func signalManagedRunPID(pid int) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	return syscall.Kill(-pid, syscall.SIGTERM)
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
	settings := &appSettings{LocalAgentType: "codex", PostgresSchema: "xmustard"}
	if err := readJSON(path, settings); err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return nil, err
	}
	if strings.TrimSpace(settings.LocalAgentType) == "" {
		settings.LocalAgentType = "codex"
	}
	if strings.TrimSpace(settings.PostgresSchema) == "" {
		settings.PostgresSchema = "xmustard"
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
			return Invalid(fmt.Sprintf("runtime %s is not available", runtime))
		}
		if !slices.Contains(defaultCodexModels, model) {
			return Invalid(fmt.Sprintf("model %s is not available for runtime %s", model, runtime))
		}
	case "opencode":
		binary := resolveBinary(settings.OpencodeBin, "opencode")
		if binary == "" {
			return Invalid(fmt.Sprintf("runtime %s is not available", runtime))
		}
		models := detectOpencodeModels(binary)
		if len(models) > 0 && !slices.Contains(models, model) {
			return Invalid(fmt.Sprintf("model %s is not available for runtime %s", model, runtime))
		}
	default:
		return Invalid(fmt.Sprintf("runtime %s is not available", runtime))
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
			return nil, Invalid(fmt.Sprintf("runtime %s is not available", runtime))
		}
		args, err := sanitizeCodexArgs(firstNonEmptyPtr(settings.CodexArgs))
		if err != nil {
			return nil, err
		}
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
		return nil, Invalid(fmt.Sprintf("runtime %s is not available", runtime))
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
	return parseOpencodeModelsOutput(string(output))
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

func parseOpencodeModelsOutput(output string) []string {
	normalized := []string{}
	seen := map[string]struct{}{}
	push := func(candidate string) {
		token := strings.TrimSpace(strings.Trim(candidate, ","))
		if token == "" {
			return
		}
		if !opencodeModelTokenPattern.MatchString(token) {
			return
		}
		if _, exists := seen[token]; exists {
			return
		}
		seen[token] = struct{}{}
		normalized = append(normalized, token)
	}

	stripped := strings.TrimSpace(output)
	if stripped == "" {
		return []string{}
	}
	if strings.HasPrefix(stripped, "[") {
		var payload any
		if err := json.Unmarshal([]byte(stripped), &payload); err == nil {
			if items, ok := payload.([]any); ok {
				for _, item := range items {
					if text, ok := item.(string); ok {
						push(text)
					}
				}
				return normalized
			}
		}
	}

	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "*") {
			line = strings.TrimSpace(line[1:])
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		push(fields[0])
	}
	return normalized
}

func sanitizeCodexArgs(raw string) ([]string, error) {
	fields, err := splitShellArgs(raw)
	if err != nil {
		return nil, err
	}
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
		if hasBlockedFlagValue(field, blockedWithValue) {
			continue
		}
		result = append(result, field)
	}
	return result, nil
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
	execution := runPlanningCommand(workspacePath, planningCommandTimeout, commandArgs)
	text := execution.Output
	for _, line := range strings.Split(text, "\n") {
		if parsed, ok := parsePlanJSON(strings.TrimSpace(line)); ok {
			return parsed, nil
		}
	}
	if parsed, ok := parsePlanJSON(text); ok {
		return parsed, nil
	}
	summary := fallbackCommandSummary("Planning", execution, 500)
	return &planCommandResult{Summary: summary, Steps: []PlanStep{}}, nil
}

func runPlanningCommand(workspacePath string, timeout time.Duration, commandArgs []string) *commandRunResult {
	return runManagedCommandWithFallback(workspacePath, timeout, commandArgs)
}

func runManagedCommandWithFallback(workspacePath string, timeout time.Duration, commandArgs []string) *commandRunResult {
	ctxTimeout := timeout + planningRustCoreBuffer
	if ctxTimeout < timeout {
		ctxTimeout = timeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
	defer cancel()

	result, err := runManagedCommand(ctx, workspacePath, durationSeconds(timeout), commandArgs)
	if err != nil {
		return runCommandWithTimeout(workspacePath, timeout, commandArgs)
	}
	return managedCommandResultToRunResult(result)
}

type commandRunResult struct {
	Output   string
	ExitCode *int
	TimedOut bool
	Err      error
}

func runCommandWithTimeout(dir string, timeout time.Duration, commandArgs []string) *commandRunResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, commandArgs[0], commandArgs[1:]...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	result := &commandRunResult{
		Output:   strings.TrimSpace(string(output)),
		TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded),
		Err:      err,
	}
	if !result.TimedOut && command.ProcessState != nil {
		exitCode := command.ProcessState.ExitCode()
		result.ExitCode = &exitCode
	}
	if !result.TimedOut && result.ExitCode == nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			result.ExitCode = &exitCode
		} else if err == nil {
			exitCode := 0
			result.ExitCode = &exitCode
		}
	}
	return result
}

func managedCommandResultToRunResult(result *rustcore.ManagedCommandResult) *commandRunResult {
	var err error
	if !result.Success && !result.TimedOut {
		err = fmt.Errorf("managed command failed")
	}
	return &commandRunResult{
		Output:   combineCommandOutput(result.StdoutExcerpt, result.StderrExcerpt),
		ExitCode: result.ExitCode,
		TimedOut: result.TimedOut,
		Err:      err,
	}
}

func combineCommandOutput(stdout string, stderr string) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}

func durationSeconds(timeout time.Duration) int {
	if timeout <= 0 {
		return 1
	}
	seconds := int(timeout / time.Second)
	if timeout%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}

func fallbackCommandSummary(action string, result *commandRunResult, limit int) string {
	text := strings.TrimSpace(result.Output)
	if text != "" {
		if limit > 0 && len(text) > limit {
			text = strings.TrimSpace(text[:limit])
		}
		return text
	}
	if result.TimedOut {
		return action + " timed out"
	}
	if result.ExitCode != nil {
		return fmt.Sprintf("%s failed with exit code %d", action, *result.ExitCode)
	}
	if result.Err != nil {
		return fmt.Sprintf("%s failed: %v", action, result.Err)
	}
	return action + " produced no structured output"
}

func splitShellArgs(raw string) ([]string, error) {
	args := []string{}
	var current strings.Builder
	tokenStarted := false
	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if tokenStarted {
			args = append(args, current.String())
			current.Reset()
			tokenStarted = false
		}
	}

	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if escaped {
			current.WriteByte(ch)
			tokenStarted = true
			escaped = false
			continue
		}
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
				continue
			}
			current.WriteByte(ch)
			tokenStarted = true
		case inDouble:
			switch ch {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
			default:
				current.WriteByte(ch)
				tokenStarted = true
			}
		default:
			switch ch {
			case ' ', '\t', '\n', '\r':
				flush()
			case '\'':
				inSingle = true
				tokenStarted = true
			case '"':
				inDouble = true
				tokenStarted = true
			case '\\':
				escaped = true
				tokenStarted = true
			default:
				current.WriteByte(ch)
				tokenStarted = true
			}
		}
	}
	if escaped {
		return nil, fmt.Errorf("unterminated escape sequence in codex args")
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unclosed quote in codex args")
	}
	flush()
	return args, nil
}

func hasBlockedFlagValue(arg string, blockedWithValue map[string]struct{}) bool {
	for flag := range blockedWithValue {
		if strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
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
	// Durable, restart-safe mirror revision: bump past whatever is already persisted
	// (and past the caller's in-memory value), so the PG mirror's monotonic guard
	// survives a process restart instead of resetting to a process-local zero and
	// suppressing updates for existing runs (XM-PRO-004).
	prev := run.MirrorRevision
	if existing, err := loadRun(dataDir, run.WorkspaceID, run.RunID); err == nil && existing != nil && existing.MirrorRevision > prev {
		prev = existing.MirrorRevision
	}
	run.MirrorRevision = prev + 1
	if err := writeJSON(filepath.Join(dataDir, "workspaces", run.WorkspaceID, "runs", run.RunID+".json"), run); err != nil {
		return err
	}
	// JSON is the source of truth (written above). Mirror into the queryable PG
	// index inline so run plans/status are queryable immediately — best-effort,
	// never fails the mutation (no-op unless XMUSTARD_PG_DSN is set).
	pgInlineUpsertRun(run)
	return nil
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
	return writeActivityRecord(dataDir, workspaceID, record)
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

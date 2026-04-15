package workspaceops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type RuntimeModel struct {
	Runtime string `json:"runtime"`
	ID      string `json:"id"`
}

type RuntimeCapabilities struct {
	Runtime    string         `json:"runtime"`
	Available  bool           `json:"available"`
	BinaryPath *string        `json:"binary_path,omitempty"`
	Models     []RuntimeModel `json:"models"`
	Notes      *string        `json:"notes,omitempty"`
}

type AppSettings struct {
	LocalAgentType string  `json:"local_agent_type"`
	CodexBin       *string `json:"codex_bin,omitempty"`
	OpencodeBin    *string `json:"opencode_bin,omitempty"`
	CodexArgs      *string `json:"codex_args,omitempty"`
	CodexModel     *string `json:"codex_model,omitempty"`
	OpencodeModel  *string `json:"opencode_model,omitempty"`
}

type LocalAgentCapabilities struct {
	SelectedRuntime       string                `json:"selected_runtime"`
	SupportsLiveSubscribe bool                  `json:"supports_live_subscribe"`
	SupportsTerminal      bool                  `json:"supports_terminal"`
	Runtimes              []RuntimeCapabilities `json:"runtimes"`
}

type RuntimeProbeResult struct {
	Runtime        string  `json:"runtime"`
	Model          string  `json:"model"`
	OK             bool    `json:"ok"`
	Available      bool    `json:"available"`
	CheckedAt      string  `json:"checked_at"`
	DurationMS     int     `json:"duration_ms"`
	ExitCode       *int    `json:"exit_code,omitempty"`
	BinaryPath     *string `json:"binary_path,omitempty"`
	CommandPreview *string `json:"command_preview,omitempty"`
	OutputExcerpt  *string `json:"output_excerpt,omitempty"`
	Error          *string `json:"error,omitempty"`
}

type RunRequest struct {
	Runtime     string  `json:"runtime"`
	Model       string  `json:"model"`
	Instruction *string `json:"instruction"`
	RunbookID   *string `json:"runbook_id"`
	Planning    bool    `json:"planning"`
}

type AgentQueryRequest struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
}

type RuntimeProbeRequest struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
}

const runtimeGuidanceLimit = 6

func GetSettings(dataDir string) (*AppSettings, error) {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	return &AppSettings{
		LocalAgentType: settings.LocalAgentType,
		CodexBin:       settings.CodexBin,
		OpencodeBin:    settings.OpencodeBin,
		CodexArgs:      settings.CodexArgs,
		CodexModel:     settings.CodexModel,
		OpencodeModel:  settings.OpencodeModel,
	}, nil
}

func UpdateSettings(dataDir string, settings AppSettings) (*AppSettings, error) {
	next := appSettings{
		LocalAgentType: fallbackString(strings.TrimSpace(settings.LocalAgentType), "codex"),
		CodexBin:       trimOptional(settings.CodexBin),
		OpencodeBin:    trimOptional(settings.OpencodeBin),
		CodexArgs:      trimOptional(settings.CodexArgs),
		CodexModel:     trimOptional(settings.CodexModel),
		OpencodeModel:  trimOptional(settings.OpencodeModel),
	}
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), next); err != nil {
		return nil, err
	}
	return GetSettings(dataDir)
}

func DetectRuntimes(dataDir string) ([]RuntimeCapabilities, error) {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	codexBin := resolveBinary(settings.CodexBin, "codex")
	opencodeBin := resolveBinary(settings.OpencodeBin, "opencode")
	codexModels := make([]RuntimeModel, 0, len(defaultCodexModels))
	for _, model := range defaultCodexModels {
		codexModels = append(codexModels, RuntimeModel{Runtime: "codex", ID: model})
	}
	opencodeModels := []RuntimeModel{}
	if opencodeBin != "" {
		for _, model := range detectOpencodeModels(opencodeBin) {
			opencodeModels = append(opencodeModels, RuntimeModel{Runtime: "opencode", ID: model})
		}
	}
	return []RuntimeCapabilities{
		{
			Runtime:    "codex",
			Available:  codexBin != "",
			BinaryPath: optionalString(codexBin),
			Models:     codexModels,
			Notes:      optionalString("Uses codex exec JSON streaming for issue runs."),
		},
		{
			Runtime:    "opencode",
			Available:  opencodeBin != "",
			BinaryPath: optionalString(opencodeBin),
			Models:     opencodeModels,
			Notes:      optionalString("Uses opencode run JSON streaming and supports local OpenCode providers."),
		},
	}, nil
}

func GetLocalAgentCapabilities(dataDir string) (*LocalAgentCapabilities, error) {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	runtimes, err := DetectRuntimes(dataDir)
	if err != nil {
		return nil, err
	}
	return &LocalAgentCapabilities{
		SelectedRuntime:       settings.LocalAgentType,
		SupportsLiveSubscribe: settings.LocalAgentType == "codex",
		SupportsTerminal:      true,
		Runtimes:              runtimes,
	}, nil
}

func ProbeRuntime(dataDir string, workspaceID string, runtime string, model string) (*RuntimeProbeResult, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := validateRuntimeModel(dataDir, runtime, model); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "runtime") {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	command, err := buildRuntimeCommand(
		dataDir,
		runtime,
		model,
		snapshot.Workspace.RootPath,
		"Reply with JSON only: "+`{"status":"ok","runtime":"`+runtime+`","model":"`+model+`"}`,
	)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = snapshot.Workspace.RootPath
	output, err := cmd.CombinedOutput()
	durationMS := int(time.Since(started).Milliseconds())
	commandPreview := shellPreview(command)
	runtimes, _ := DetectRuntimes(dataDir)
	var runtimeEntry *RuntimeCapabilities
	for idx := range runtimes {
		if runtimes[idx].Runtime == runtime {
			runtimeEntry = &runtimes[idx]
			break
		}
	}
	result := &RuntimeProbeResult{
		Runtime:        runtime,
		Model:          model,
		Available:      runtimeEntry != nil && runtimeEntry.Available,
		CheckedAt:      nowUTC(),
		DurationMS:     durationMS,
		CommandPreview: &commandPreview,
	}
	if runtimeEntry != nil {
		result.BinaryPath = runtimeEntry.BinaryPath
	}
	combinedOutput := strings.TrimSpace(string(output))
	if ctx.Err() == context.DeadlineExceeded {
		result.OK = false
		result.OutputExcerpt = optionalExcerpt(combinedOutput)
		result.Error = optionalString("Probe timed out after 45 seconds")
		return result, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode := exitErr.ExitCode()
		result.ExitCode = &exitCode
	} else if err == nil {
		exitCode := 0
		result.ExitCode = &exitCode
	}
	summary := summarizeRunOutput(runtime, combinedOutput)
	excerpt := summaryString(summary, "text_excerpt")
	if strings.TrimSpace(excerpt) == "" {
		excerpt = combinedOutput
	}
	result.OutputExcerpt = optionalExcerpt(excerpt)
	result.OK = err == nil
	if err != nil {
		errText := excerpt
		if strings.TrimSpace(errText) == "" {
			errText = "Runtime probe failed"
		}
		result.Error = &errText
	}
	if err == nil && result.ExitCode == nil {
		exitCode := 0
		result.ExitCode = &exitCode
	}
	return result, nil
}

func StartIssueRun(dataDir string, workspaceID string, issueID string, request RunRequest) (*runRecord, error) {
	packet, err := BuildIssueWorkPacket(dataDir, workspaceID, issueID, firstNonEmptyPtr(request.RunbookID))
	if err != nil {
		return nil, err
	}
	if err := validateRuntimeModel(dataDir, request.Runtime, request.Model); err != nil {
		return nil, err
	}
	prompt := packet.Prompt
	if request.Instruction != nil && strings.TrimSpace(*request.Instruction) != "" {
		prompt = prompt + "\n\nAdditional operator instruction:\n" + strings.TrimSpace(*request.Instruction)
	}
	command, err := buildRuntimeCommand(dataDir, request.Runtime, request.Model, packet.Workspace.RootPath, prompt)
	if err != nil {
		return nil, err
	}
	runID := "run_" + hashID(workspaceID, issueID, nowUTC())[:12]
	run := &runRecord{
		RunID:          runID,
		WorkspaceID:    workspaceID,
		IssueID:        issueID,
		Runtime:        request.Runtime,
		Model:          request.Model,
		Status:         map[bool]string{true: "planning", false: "queued"}[request.Planning],
		Title:          request.Runtime + ":" + issueID,
		Prompt:         prompt,
		Command:        command,
		CommandPreview: shellPreview(command),
		LogPath:        filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".log"),
		OutputPath:     filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".out.json"),
		CreatedAt:      nowUTC(),
		RunbookID:      trimOptional(request.RunbookID),
		Worktree:       packet.Worktree,
		GuidancePaths:  guidancePaths(packet.Guidance),
	}
	if err := saveRunRecord(dataDir, *run); err != nil {
		return nil, err
	}
	if !request.Planning {
		startManagedRun(dataDir, *run, packet.Workspace.RootPath)
	}
	action := map[bool]string{true: "run.planning", false: "run.queued"}[request.Planning]
	summary := map[bool]string{
		true:  fmt.Sprintf("Started %s run with planning for %s", request.Runtime, issueID),
		false: fmt.Sprintf("Queued %s run for %s", request.Runtime, issueID),
	}[request.Planning]
	if err := appendRunActivityWithActor(
		dataDir,
		workspaceID,
		issueID,
		runID,
		action,
		summary,
		activityActor{
			Kind:    "agent",
			Name:    request.Runtime,
			Runtime: ptr(request.Runtime),
			Model:   ptr(request.Model),
			Key:     "agent:" + request.Runtime + ":" + request.Model,
			Label:   request.Runtime + ":" + request.Model,
		},
		map[string]any{"runtime": request.Runtime, "model": request.Model, "runbook_id": request.RunbookID, "planning": request.Planning},
	); err != nil {
		return nil, err
	}
	return run, nil
}

func StartAgentQuery(dataDir string, workspaceID string, request AgentQueryRequest) (*runRecord, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	trimmedPrompt := strings.TrimSpace(request.Prompt)
	if trimmedPrompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	guidance, err := ListWorkspaceGuidanceRecords(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if len(guidance) > runtimeGuidanceLimit {
		guidance = guidance[:runtimeGuidanceLimit]
	}
	if err := validateRuntimeModel(dataDir, request.Runtime, request.Model); err != nil {
		return nil, err
	}
	prompt := applyGuidanceToPrompt(trimmedPrompt, guidance)
	command, err := buildRuntimeCommand(dataDir, request.Runtime, request.Model, snapshot.Workspace.RootPath, prompt)
	if err != nil {
		return nil, err
	}
	runID := "run_" + hashID(workspaceID, "workspace-query", nowUTC())[:12]
	worktree := readWorktreeStatus(snapshot.Workspace.RootPath)
	run := &runRecord{
		RunID:          runID,
		WorkspaceID:    workspaceID,
		IssueID:        "workspace-query",
		Runtime:        request.Runtime,
		Model:          request.Model,
		Status:         "queued",
		Title:          request.Runtime + ":workspace-query",
		Prompt:         prompt,
		Command:        command,
		CommandPreview: shellPreview(command),
		LogPath:        filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".log"),
		OutputPath:     filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".out.json"),
		CreatedAt:      nowUTC(),
		Worktree:       worktree,
		GuidancePaths:  guidancePathList(guidance),
	}
	if err := saveRunRecord(dataDir, *run); err != nil {
		return nil, err
	}
	startManagedRun(dataDir, *run, snapshot.Workspace.RootPath)
	if err := appendRunActivityWithActor(
		dataDir,
		workspaceID,
		run.IssueID,
		runID,
		"run.query",
		fmt.Sprintf("Queued %s workspace query", request.Runtime),
		activityActor{
			Kind:    "agent",
			Name:    request.Runtime,
			Runtime: ptr(request.Runtime),
			Model:   ptr(request.Model),
			Key:     "agent:" + request.Runtime + ":" + request.Model,
			Label:   request.Runtime + ":" + request.Model,
		},
		map[string]any{"runtime": request.Runtime, "model": request.Model, "prompt_preview": trimmedPrompt[:min(len(trimmedPrompt), 160)]},
	); err != nil {
		return nil, err
	}
	return run, nil
}

func applyGuidanceToPrompt(prompt string, guidance []RepoGuidanceRecord) string {
	if len(guidance) == 0 {
		return prompt
	}
	lines := []string{}
	for _, item := range guidance[:min(len(guidance), runtimeGuidanceLimit)] {
		lines = append(lines, "- "+item.Path+": "+fallbackString(item.Summary, item.Title))
	}
	return "Repository guidance to respect:\n" + strings.Join(lines, "\n") + "\n\n" + prompt
}

func guidancePathList(guidance []RepoGuidanceRecord) []string {
	items := make([]string, 0, len(guidance))
	for _, item := range guidance {
		items = append(items, item.Path)
	}
	return items
}

func optionalExcerpt(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > 1400 {
		trimmed = strings.TrimSpace(trimmed[:1400]) + "..."
	}
	return &trimmed
}

func summaryString(summary map[string]any, key string) string {
	value, ok := summary[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

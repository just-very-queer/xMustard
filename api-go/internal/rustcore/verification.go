package rustcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

type VerificationCommandResult struct {
	Command       string `json:"command"`
	Cwd           string `json:"cwd"`
	ExitCode      *int   `json:"exit_code"`
	Success       bool   `json:"success"`
	TimedOut      bool   `json:"timed_out"`
	DurationMS    int64  `json:"duration_ms"`
	StdoutExcerpt string `json:"stdout_excerpt"`
	StderrExcerpt string `json:"stderr_excerpt"`
	CreatedAt     string `json:"created_at"`
}

type VerificationProfileInput struct {
	ProfileID          string   `json:"profile_id"`
	WorkspaceID        string   `json:"workspace_id"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	TestCommand        string   `json:"test_command"`
	CoverageCommand    *string  `json:"coverage_command"`
	CoverageReportPath *string  `json:"coverage_report_path"`
	CoverageFormat     string   `json:"coverage_format"`
	MaxRuntimeSeconds  int64    `json:"max_runtime_seconds"`
	RetryCount         int64    `json:"retry_count"`
	SourcePaths        []string `json:"source_paths"`
	BuiltIn            bool     `json:"built_in"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

type VerificationProfileResult struct {
	ProfileID             string                      `json:"profile_id"`
	WorkspaceID           string                      `json:"workspace_id"`
	Attempts              []VerificationCommandResult `json:"attempts"`
	AttemptCount          int                         `json:"attempt_count"`
	Success               bool                        `json:"success"`
	CoverageCommandResult *VerificationCommandResult  `json:"coverage_command_result"`
	CoverageResult        *CoverageResult             `json:"coverage_result"`
	CoverageReportPath    *string                     `json:"coverage_report_path"`
	CreatedAt             string                      `json:"created_at"`
}

func RunVerificationCommand(ctx context.Context, workspaceRoot string, timeoutSeconds int, command string) (*VerificationCommandResult, error) {
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}

	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"run-verification-command",
		workspaceRoot,
		fmt.Sprintf("%d", timeoutSeconds),
		command,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core run-verification-command failed: %w: %s", err, stderr.String())
	}

	var result VerificationCommandResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core verification result: %w", err)
	}
	return &result, nil
}

func RunVerificationProfile(
	ctx context.Context,
	workspaceRoot string,
	profile VerificationProfileInput,
	runID string,
	issueID string,
) (*VerificationProfileResult, error) {
	profileBytes, err := json.Marshal(profile)
	if err != nil {
		return nil, fmt.Errorf("encode verification profile: %w", err)
	}

	profileFile, err := os.CreateTemp("", "xmustard-verification-profile-*.json")
	if err != nil {
		return nil, fmt.Errorf("create temp verification profile: %w", err)
	}
	defer os.Remove(profileFile.Name())

	if _, err := profileFile.Write(profileBytes); err != nil {
		_ = profileFile.Close()
		return nil, fmt.Errorf("write temp verification profile: %w", err)
	}
	if err := profileFile.Close(); err != nil {
		return nil, fmt.Errorf("close temp verification profile: %w", err)
	}

	args := []string{
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"run-verification-profile",
		workspaceRoot,
		profileFile.Name(),
	}
	if runID != "" {
		args = append(args, runID)
	}
	if issueID != "" {
		args = append(args, issueID)
	}

	cmd := exec.CommandContext(ctx, "cargo", args...)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core run-verification-profile failed: %w: %s", err, stderr.String())
	}

	var result VerificationProfileResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core verification profile result: %w", err)
	}
	return &result, nil
}

package rustcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

type ManagedCommandResult = VerificationCommandResult

func RunManagedCommand(ctx context.Context, workspaceRoot string, timeoutSeconds int, commandArgs []string) (*ManagedCommandResult, error) {
	if len(commandArgs) == 0 {
		return nil, fmt.Errorf("managed command args cannot be empty")
	}
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}

	args := []string{workspaceRoot, fmt.Sprintf("%d", timeoutSeconds)}
	args = append(args, commandArgs...)

	cmd := coreCommandContext(ctx, "run-managed-command", args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core run-managed-command failed: %w: %s", err, stderr.String())
	}

	var result ManagedCommandResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core managed command result: %w", err)
	}
	return &result, nil
}

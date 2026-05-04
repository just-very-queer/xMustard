package rustcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

type NormalizedDiagnostic struct {
	WorkspaceID      string  `json:"workspace_id"`
	Path             string  `json:"path"`
	RangeStartLine   int     `json:"range_start_line"`
	RangeStartColumn int     `json:"range_start_column"`
	RangeEndLine     int     `json:"range_end_line"`
	RangeEndColumn   int     `json:"range_end_column"`
	Severity         string  `json:"severity"`
	Message          string  `json:"message"`
	SourceKind       string  `json:"source_kind"`
	SourceName       string  `json:"source_name"`
	RuleCode         *string `json:"rule_code,omitempty"`
	Fingerprint      string  `json:"fingerprint"`
	GeneratedAt      string  `json:"generated_at"`
}

type DiagnosticsBatch struct {
	WorkspaceID     string                 `json:"workspace_id"`
	SourceKind      string                 `json:"source_kind"`
	SourceName      string                 `json:"source_name"`
	DiagnosticCount int                    `json:"diagnostic_count"`
	Diagnostics     []NormalizedDiagnostic `json:"diagnostics"`
	SeverityCounts  map[string]int         `json:"severity_counts"`
	Warnings        []string               `json:"warnings"`
	GeneratedAt     string                 `json:"generated_at"`
}

func NormalizeDiagnostics(ctx context.Context, workspaceID string, repoRoot string, inputJSONPath string, sourceKind string, sourceName string) (*DiagnosticsBatch, error) {
	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"normalize-diagnostics",
		workspaceID,
		repoRoot,
		inputJSONPath,
		sourceKind,
		sourceName,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core normalize-diagnostics failed: %w: %s", err, stderr.String())
	}

	var result DiagnosticsBatch
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core diagnostics: %w", err)
	}
	return &result, nil
}

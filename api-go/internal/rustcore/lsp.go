package rustcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

type DefinitionLocation struct {
	Path                 string `json:"path"`
	TargetLineStart      int    `json:"target_line_start"`
	TargetColumnStart    int    `json:"target_column_start"`
	TargetLineEnd        int    `json:"target_line_end"`
	TargetColumnEnd      int    `json:"target_column_end"`
	SelectionLineStart   *int   `json:"selection_line_start,omitempty"`
	SelectionColumnStart *int   `json:"selection_column_start,omitempty"`
	SelectionLineEnd     *int   `json:"selection_line_end,omitempty"`
	SelectionColumnEnd   *int   `json:"selection_column_end,omitempty"`
}

type DefinitionResult struct {
	WorkspaceID     string               `json:"workspace_id"`
	Path            string               `json:"path"`
	Line            int                  `json:"line"`
	Column          int                  `json:"column"`
	SourceName      string               `json:"source_name"`
	EvidenceSource  string               `json:"evidence_source"`
	SelectionReason string               `json:"selection_reason"`
	DefinitionCount int                  `json:"definition_count"`
	Definitions     []DefinitionLocation `json:"definitions"`
	Warnings        []string             `json:"warnings"`
	GeneratedAt     string               `json:"generated_at"`
}

func NormalizeLSPDefinition(
	ctx context.Context,
	workspaceID string,
	repoRoot string,
	relativePath string,
	line int,
	column int,
	sourceName string,
	payload []byte,
) (*DefinitionResult, error) {
	inputFile, err := os.CreateTemp("", "xmustard-lsp-definition-*.json")
	if err != nil {
		return nil, fmt.Errorf("create LSP definition temp file: %w", err)
	}
	inputPath := inputFile.Name()
	defer os.Remove(inputPath)
	if _, err := inputFile.Write(payload); err != nil {
		inputFile.Close()
		return nil, fmt.Errorf("write LSP definition payload: %w", err)
	}
	if err := inputFile.Close(); err != nil {
		return nil, fmt.Errorf("close LSP definition payload: %w", err)
	}

	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"normalize-lsp-definition",
		workspaceID,
		repoRoot,
		relativePath,
		fmt.Sprintf("%d", line),
		fmt.Sprintf("%d", column),
		sourceName,
		inputPath,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core normalize-lsp-definition failed: %w: %s", err, stderr.String())
	}

	var result DefinitionResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core LSP definition result: %w", err)
	}
	return &result, nil
}

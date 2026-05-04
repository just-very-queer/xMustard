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

type ReferenceLocation struct {
	Path        string `json:"path"`
	LineStart   int    `json:"line_start"`
	ColumnStart int    `json:"column_start"`
	LineEnd     int    `json:"line_end"`
	ColumnEnd   int    `json:"column_end"`
}

type ReferencesResult struct {
	WorkspaceID     string              `json:"workspace_id"`
	Path            string              `json:"path"`
	Line            int                 `json:"line"`
	Column          int                 `json:"column"`
	SourceName      string              `json:"source_name"`
	EvidenceSource  string              `json:"evidence_source"`
	SelectionReason string              `json:"selection_reason"`
	ReferenceCount  int                 `json:"reference_count"`
	References      []ReferenceLocation `json:"references"`
	Warnings        []string            `json:"warnings"`
	GeneratedAt     string              `json:"generated_at"`
}

type DocumentSymbolRecord struct {
	Path           string  `json:"path"`
	Symbol         string  `json:"symbol"`
	Kind           string  `json:"kind"`
	LineStart      *int    `json:"line_start,omitempty"`
	LineEnd        *int    `json:"line_end,omitempty"`
	EnclosingScope *string `json:"enclosing_scope,omitempty"`
	EvidenceSource string  `json:"evidence_source"`
	Reason         *string `json:"reason,omitempty"`
	Score          int     `json:"score"`
}

type DocumentSymbolsResult struct {
	WorkspaceID     string                 `json:"workspace_id"`
	Path            string                 `json:"path"`
	SymbolSource    string                 `json:"symbol_source"`
	ParserLanguage  *string                `json:"parser_language,omitempty"`
	EvidenceSource  string                 `json:"evidence_source"`
	SelectionReason string                 `json:"selection_reason"`
	Symbols         []DocumentSymbolRecord `json:"symbols"`
	Warnings        []string               `json:"warnings"`
	GeneratedAt     string                 `json:"generated_at"`
}

type WorkspaceSymbolRecord struct {
	Path           string  `json:"path"`
	Symbol         string  `json:"symbol"`
	Kind           string  `json:"kind"`
	LineStart      *int    `json:"line_start,omitempty"`
	LineEnd        *int    `json:"line_end,omitempty"`
	EnclosingScope *string `json:"enclosing_scope,omitempty"`
	EvidenceSource string  `json:"evidence_source"`
	Reason         *string `json:"reason,omitempty"`
	Score          int     `json:"score"`
}

type LSPWorkspaceSymbolsResult struct {
	WorkspaceID     string                  `json:"workspace_id"`
	Query           string                  `json:"query"`
	Limit           int                     `json:"limit"`
	SymbolSource    string                  `json:"symbol_source"`
	SourceName      string                  `json:"source_name"`
	EvidenceSource  string                  `json:"evidence_source"`
	SelectionReason string                  `json:"selection_reason"`
	Symbols         []WorkspaceSymbolRecord `json:"symbols"`
	Warnings        []string                `json:"warnings"`
	GeneratedAt     string                  `json:"generated_at"`
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

func NormalizeLSPReferences(
	ctx context.Context,
	workspaceID string,
	repoRoot string,
	relativePath string,
	line int,
	column int,
	sourceName string,
	payload []byte,
) (*ReferencesResult, error) {
	inputFile, err := os.CreateTemp("", "xmustard-lsp-references-*.json")
	if err != nil {
		return nil, fmt.Errorf("create LSP references temp file: %w", err)
	}
	inputPath := inputFile.Name()
	defer os.Remove(inputPath)
	if _, err := inputFile.Write(payload); err != nil {
		inputFile.Close()
		return nil, fmt.Errorf("write LSP references payload: %w", err)
	}
	if err := inputFile.Close(); err != nil {
		return nil, fmt.Errorf("close LSP references payload: %w", err)
	}

	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"normalize-lsp-references",
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
		return nil, fmt.Errorf("rust-core normalize-lsp-references failed: %w: %s", err, stderr.String())
	}

	var result ReferencesResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core LSP references result: %w", err)
	}
	return &result, nil
}

func NormalizeLSPDocumentSymbols(
	ctx context.Context,
	workspaceID string,
	repoRoot string,
	relativePath string,
	sourceName string,
	payload []byte,
) (*DocumentSymbolsResult, error) {
	inputFile, err := os.CreateTemp("", "xmustard-lsp-document-symbols-*.json")
	if err != nil {
		return nil, fmt.Errorf("create LSP document-symbols temp file: %w", err)
	}
	inputPath := inputFile.Name()
	defer os.Remove(inputPath)
	if _, err := inputFile.Write(payload); err != nil {
		inputFile.Close()
		return nil, fmt.Errorf("write LSP document-symbols payload: %w", err)
	}
	if err := inputFile.Close(); err != nil {
		return nil, fmt.Errorf("close LSP document-symbols payload: %w", err)
	}

	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"normalize-lsp-document-symbols",
		workspaceID,
		repoRoot,
		relativePath,
		sourceName,
		inputPath,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core normalize-lsp-document-symbols failed: %w: %s", err, stderr.String())
	}

	var result DocumentSymbolsResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core LSP document-symbols result: %w", err)
	}
	return &result, nil
}

func NormalizeLSPWorkspaceSymbols(
	ctx context.Context,
	workspaceID string,
	repoRoot string,
	query string,
	limit int,
	sourceName string,
	payload []byte,
) (*LSPWorkspaceSymbolsResult, error) {
	inputFile, err := os.CreateTemp("", "xmustard-lsp-workspace-symbols-*.json")
	if err != nil {
		return nil, fmt.Errorf("create LSP workspace-symbols temp file: %w", err)
	}
	inputPath := inputFile.Name()
	defer os.Remove(inputPath)
	if _, err := inputFile.Write(payload); err != nil {
		inputFile.Close()
		return nil, fmt.Errorf("write LSP workspace-symbols payload: %w", err)
	}
	if err := inputFile.Close(); err != nil {
		return nil, fmt.Errorf("close LSP workspace-symbols payload: %w", err)
	}

	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"normalize-lsp-workspace-symbols",
		workspaceID,
		repoRoot,
		query,
		fmt.Sprintf("%d", limit),
		sourceName,
		inputPath,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core normalize-lsp-workspace-symbols failed: %w: %s", err, stderr.String())
	}

	var result LSPWorkspaceSymbolsResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core LSP workspace-symbols result: %w", err)
	}
	return &result, nil
}

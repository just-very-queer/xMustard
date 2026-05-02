package rustcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

type RepoMapDirectoryRecord struct {
	Path            string `json:"path"`
	FileCount       int    `json:"file_count"`
	SourceFileCount int    `json:"source_file_count"`
	TestFileCount   int    `json:"test_file_count"`
}

type RepoMapFileRecord struct {
	Path      string `json:"path"`
	Role      string `json:"role"`
	SizeBytes *int64 `json:"size_bytes"`
}

type RepoMapSummary struct {
	WorkspaceID    string                   `json:"workspace_id"`
	RootPath       string                   `json:"root_path"`
	TotalFiles     int                      `json:"total_files"`
	SourceFiles    int                      `json:"source_files"`
	TestFiles      int                      `json:"test_files"`
	TopExtensions  map[string]int           `json:"top_extensions"`
	TopDirectories []RepoMapDirectoryRecord `json:"top_directories"`
	KeyFiles       []RepoMapFileRecord      `json:"key_files"`
	GeneratedAt    string                   `json:"generated_at"`
}

type RepoChangeRecord struct {
	Path         string  `json:"path"`
	Status       string  `json:"status"`
	Scope        string  `json:"scope"`
	PreviousPath *string `json:"previous_path,omitempty"`
	Staged       bool    `json:"staged"`
	Unstaged     bool    `json:"unstaged"`
}

type ChangedSymbolRecord struct {
	Path            string   `json:"path"`
	Symbol          string   `json:"symbol"`
	Kind            string   `json:"kind"`
	LineStart       *int     `json:"line_start,omitempty"`
	LineEnd         *int     `json:"line_end,omitempty"`
	EvidenceSource  string   `json:"evidence_source"`
	SemanticStatus  *string  `json:"semantic_status,omitempty"`
	SelectionReason string   `json:"selection_reason"`
	ChangeScopes    []string `json:"change_scopes"`
	ChangeStatuses  []string `json:"change_statuses"`
}

type ImpactPathRecord struct {
	Path             string `json:"path"`
	Reason           string `json:"reason"`
	DerivationSource string `json:"derivation_source"`
	Score            int    `json:"score"`
}

type SemanticImpactReport struct {
	WorkspaceID         string                `json:"workspace_id"`
	ChangedSymbols      []ChangedSymbolRecord `json:"changed_symbols"`
	LikelyAffectedFiles []ImpactPathRecord    `json:"likely_affected_files"`
	LikelyAffectedTests []ImpactPathRecord    `json:"likely_affected_tests"`
	DerivationSource    string                `json:"derivation_source"`
	Warnings            []string              `json:"warnings"`
	GeneratedAt         string                `json:"generated_at"`
}

type PathSymbolRecord struct {
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

type PathSymbolsResult struct {
	WorkspaceID     string             `json:"workspace_id"`
	Path            string             `json:"path"`
	SymbolSource    string             `json:"symbol_source"`
	ParserLanguage  *string            `json:"parser_language,omitempty"`
	EvidenceSource  string             `json:"evidence_source"`
	SelectionReason string             `json:"selection_reason"`
	Symbols         []PathSymbolRecord `json:"symbols"`
	Warnings        []string           `json:"warnings"`
	GeneratedAt     string             `json:"generated_at"`
}

type CodeExplainerResult struct {
	WorkspaceID     string   `json:"workspace_id"`
	Path            string   `json:"path"`
	Role            string   `json:"role"`
	LineCount       int      `json:"line_count"`
	ImportCount     int      `json:"import_count"`
	DetectedSymbols []string `json:"detected_symbols"`
	SymbolSource    string   `json:"symbol_source"`
	ParserLanguage  *string  `json:"parser_language,omitempty"`
	EvidenceSource  string   `json:"evidence_source"`
	SelectionReason string   `json:"selection_reason"`
	Summary         string   `json:"summary"`
	Hints           []string `json:"hints"`
	Warnings        []string `json:"warnings"`
	GeneratedAt     string   `json:"generated_at"`
}

func BuildRepoMap(ctx context.Context, workspaceID string, repoRoot string) (*RepoMapSummary, error) {
	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"build-repo-map",
		workspaceID,
		repoRoot,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core build-repo-map failed: %w: %s", err, stderr.String())
	}

	var summary RepoMapSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		return nil, fmt.Errorf("decode rust-core repo-map: %w", err)
	}
	return &summary, nil
}

func BuildSemanticImpact(ctx context.Context, workspaceID string, repoRoot string, changes []RepoChangeRecord) (*SemanticImpactReport, error) {
	changesFile, err := os.CreateTemp("", "xmustard-semantic-impact-*.json")
	if err != nil {
		return nil, fmt.Errorf("create semantic impact change file: %w", err)
	}
	changesPath := changesFile.Name()
	defer os.Remove(changesPath)
	if err := json.NewEncoder(changesFile).Encode(changes); err != nil {
		changesFile.Close()
		return nil, fmt.Errorf("write semantic impact changes: %w", err)
	}
	if err := changesFile.Close(); err != nil {
		return nil, fmt.Errorf("close semantic impact changes: %w", err)
	}

	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"semantic-impact",
		workspaceID,
		repoRoot,
		changesPath,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core semantic-impact failed: %w: %s", err, stderr.String())
	}

	var report SemanticImpactReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		return nil, fmt.Errorf("decode rust-core semantic impact: %w", err)
	}
	return &report, nil
}

func ExtractPathSymbols(ctx context.Context, workspaceID string, repoRoot string, relativePath string) (*PathSymbolsResult, error) {
	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"path-symbols",
		workspaceID,
		repoRoot,
		relativePath,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core path-symbols failed: %w: %s", err, stderr.String())
	}

	var result PathSymbolsResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core path symbols: %w", err)
	}
	return &result, nil
}

func ExplainPath(ctx context.Context, workspaceID string, repoRoot string, relativePath string) (*CodeExplainerResult, error) {
	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"explain-path",
		workspaceID,
		repoRoot,
		relativePath,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core explain-path failed: %w: %s", err, stderr.String())
	}

	var result CodeExplainerResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core path explainer: %w", err)
	}
	return &result, nil
}

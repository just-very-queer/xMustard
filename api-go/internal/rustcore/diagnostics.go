package rustcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
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

type DiagnosticsReplayArchive struct {
	WorkspaceID      string         `json:"workspace_id"`
	SourceKind       string         `json:"source_kind"`
	SourceName       string         `json:"source_name"`
	RawPayload       any            `json:"raw_payload"`
	RawPayloadSHA256 string         `json:"raw_payload_sha256"`
	RawPayloadBytes  int            `json:"raw_payload_bytes"`
	ServerProvenance map[string]any `json:"server_provenance"`
	ReplayReadiness  string         `json:"replay_readiness"`
	Warnings         []string       `json:"warnings"`
	GeneratedAt      string         `json:"generated_at"`
}

type DiagnosticSymbolCandidate struct {
	SymbolID       int64   `json:"symbol_id"`
	Path           string  `json:"path"`
	Symbol         string  `json:"symbol"`
	Kind           string  `json:"kind"`
	Language       *string `json:"language,omitempty"`
	LineStart      *int    `json:"line_start,omitempty"`
	LineEnd        *int    `json:"line_end,omitempty"`
	EnclosingScope *string `json:"enclosing_scope,omitempty"`
	SignatureText  *string `json:"signature_text,omitempty"`
}

type DiagnosticLinkedSymbol struct {
	SymbolID        int64   `json:"symbol_id"`
	Path            string  `json:"path"`
	Symbol          string  `json:"symbol"`
	Kind            string  `json:"kind"`
	Language        *string `json:"language,omitempty"`
	LineStart       *int    `json:"line_start,omitempty"`
	LineEnd         *int    `json:"line_end,omitempty"`
	EnclosingScope  *string `json:"enclosing_scope,omitempty"`
	SignatureText   *string `json:"signature_text,omitempty"`
	LinkStrategy    string  `json:"link_strategy"`
	EvidenceSource  string  `json:"evidence_source"`
	SelectionReason string  `json:"selection_reason"`
}

type DiagnosticSymbolLinkResult struct {
	WorkspaceID           string                  `json:"workspace_id"`
	Path                  string                  `json:"path"`
	DiagnosticFingerprint string                  `json:"diagnostic_fingerprint"`
	LinkedSymbol          *DiagnosticLinkedSymbol `json:"linked_symbol,omitempty"`
	CandidateCount        int                     `json:"candidate_count"`
	EvidenceSource        string                  `json:"evidence_source"`
	SelectionReason       string                  `json:"selection_reason"`
	Warnings              []string                `json:"warnings"`
	GeneratedAt           string                  `json:"generated_at"`
}

func NormalizeDiagnostics(ctx context.Context, workspaceID string, repoRoot string, inputJSONPath string, sourceKind string, sourceName string) (*DiagnosticsBatch, error) {
	stdout, err := runCoreCtx(ctx, "normalize-diagnostics", workspaceID, repoRoot, inputJSONPath, sourceKind, sourceName)
	if err != nil {
		return nil, err
	}
	var result DiagnosticsBatch
	if err := json.Unmarshal(stdout, &result); err != nil {
		return nil, fmt.Errorf("decode rust-core diagnostics: %w", err)
	}
	return &result, nil
}

func NormalizeDiagnosticsPayload(ctx context.Context, workspaceID string, repoRoot string, payload []byte, sourceKind string, sourceName string) (*DiagnosticsBatch, error) {
	inputFile, err := os.CreateTemp("", "xmustard-live-diagnostics-*.json")
	if err != nil {
		return nil, fmt.Errorf("create live diagnostics temp file: %w", err)
	}
	inputPath := inputFile.Name()
	defer os.Remove(inputPath)
	if _, err := inputFile.Write(payload); err != nil {
		inputFile.Close()
		return nil, fmt.Errorf("write live diagnostics payload: %w", err)
	}
	if err := inputFile.Close(); err != nil {
		return nil, fmt.Errorf("close live diagnostics payload: %w", err)
	}
	return NormalizeDiagnostics(ctx, workspaceID, repoRoot, inputPath, sourceKind, sourceName)
}

func ArchiveDiagnosticsPayload(ctx context.Context, workspaceID string, inputJSONPath string, sourceKind string, sourceName string, serverProvenance map[string]any) (*DiagnosticsReplayArchive, error) {
	provenancePayload, err := json.Marshal(serverProvenance)
	if err != nil {
		return nil, fmt.Errorf("encode diagnostics server provenance: %w", err)
	}
	provenanceFile, err := os.CreateTemp("", "xmustard-diagnostics-server-provenance-*.json")
	if err != nil {
		return nil, fmt.Errorf("create diagnostics server provenance temp file: %w", err)
	}
	provenancePath := provenanceFile.Name()
	defer os.Remove(provenancePath)
	if _, err := provenanceFile.Write(provenancePayload); err != nil {
		provenanceFile.Close()
		return nil, fmt.Errorf("write diagnostics server provenance: %w", err)
	}
	if err := provenanceFile.Close(); err != nil {
		return nil, fmt.Errorf("close diagnostics server provenance: %w", err)
	}

	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"archive-diagnostics-payload",
		workspaceID,
		inputJSONPath,
		sourceKind,
		sourceName,
		provenancePath,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core archive-diagnostics-payload failed: %w: %s", err, stderr.String())
	}

	var result DiagnosticsReplayArchive
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core diagnostics replay archive: %w", err)
	}
	return &result, nil
}

func LinkDiagnosticSymbol(
	ctx context.Context,
	workspaceID string,
	diagnosticPath string,
	startLine int,
	endLine int,
	diagnosticFingerprint string,
	candidates []DiagnosticSymbolCandidate,
) (*DiagnosticSymbolLinkResult, error) {
	payload, err := json.Marshal(candidates)
	if err != nil {
		return nil, fmt.Errorf("encode diagnostic symbol candidates: %w", err)
	}
	inputFile, err := os.CreateTemp("", "xmustard-diagnostic-symbol-candidates-*.json")
	if err != nil {
		return nil, fmt.Errorf("create diagnostic symbol candidates temp file: %w", err)
	}
	inputPath := inputFile.Name()
	defer os.Remove(inputPath)
	if _, err := inputFile.Write(payload); err != nil {
		inputFile.Close()
		return nil, fmt.Errorf("write diagnostic symbol candidates: %w", err)
	}
	if err := inputFile.Close(); err != nil {
		return nil, fmt.Errorf("close diagnostic symbol candidates: %w", err)
	}

	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"link-diagnostic-symbol",
		workspaceID,
		diagnosticPath,
		fmt.Sprintf("%d", startLine),
		fmt.Sprintf("%d", endLine),
		diagnosticFingerprint,
		inputPath,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core link-diagnostic-symbol failed: %w: %s", err, stderr.String())
	}

	var result DiagnosticSymbolLinkResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode rust-core diagnostic symbol link: %w", err)
	}
	return &result, nil
}

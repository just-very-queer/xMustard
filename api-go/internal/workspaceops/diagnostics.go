package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"xmustard/api-go/internal/rustcore"
)

var ErrInvalidDiagnosticsRequest = errors.New("invalid diagnostics request")

const (
	diagnosticLinkStatusUnevaluated        = "unevaluated"
	diagnosticLinkStatusSymbolsUnavailable = "symbols_unavailable"
	diagnosticLinkStatusEvaluatedUnlinked  = "evaluated_unlinked"
	diagnosticLinkStatusLinked             = "linked"
)

type DiagnosticsRequest struct {
	InputPath  string  `json:"input_path"`
	SourceKind string  `json:"source_kind"`
	SourceName string  `json:"source_name"`
	DSN        *string `json:"dsn,omitempty"`
	SchemaName *string `json:"schema_name,omitempty"`
	DryRun     bool    `json:"dry_run"`
}

type DiagnosticsPlan struct {
	WorkspaceID        string                   `json:"workspace_id"`
	RootPath           string                   `json:"root_path"`
	InputPath          string                   `json:"input_path"`
	SourceKind         string                   `json:"source_kind"`
	SourceName         string                   `json:"source_name"`
	DiagnosticCount    int                      `json:"diagnostic_count"`
	SeverityCounts     map[string]int           `json:"severity_counts"`
	HeadSHA            *string                  `json:"head_sha,omitempty"`
	DirtyFiles         int                      `json:"dirty_files"`
	WorktreeDirty      bool                     `json:"worktree_dirty"`
	BatchFingerprint   *string                  `json:"batch_fingerprint,omitempty"`
	ReplayArchive      *DiagnosticReplayArchive `json:"replay_archive,omitempty"`
	PostgresConfigured bool                     `json:"postgres_configured"`
	PostgresSchema     string                   `json:"postgres_schema"`
	Blockers           []string                 `json:"blockers"`
	Warnings           []string                 `json:"warnings"`
	NextActions        []string                 `json:"next_actions"`
	CanRun             bool                     `json:"can_run"`
	GeneratedAt        string                   `json:"generated_at"`
	NormalizedBatch    *DiagnosticsBatch        `json:"normalized_batch,omitempty"`
}

type DiagnosticsRunResult struct {
	WorkspaceID    string           `json:"workspace_id"`
	DryRun         bool             `json:"dry_run"`
	Plan           *DiagnosticsPlan `json:"plan"`
	Baseline       *DiagnosticRun   `json:"baseline,omitempty"`
	DiagnosticRows int              `json:"diagnostic_rows"`
	Message        string           `json:"message"`
	GeneratedAt    string           `json:"generated_at"`
}

type DiagnosticsStatus struct {
	WorkspaceID        string         `json:"workspace_id"`
	Status             string         `json:"status"`
	PostgresConfigured bool           `json:"postgres_configured"`
	PostgresSchema     string         `json:"postgres_schema"`
	CurrentHeadSHA     *string        `json:"current_head_sha,omitempty"`
	CurrentDirtyFiles  int            `json:"current_dirty_files"`
	Baseline           *DiagnosticRun `json:"baseline,omitempty"`
	StaleReasons       []string       `json:"stale_reasons"`
	Warnings           []string       `json:"warnings"`
	GeneratedAt        string         `json:"generated_at"`
}

type DiagnosticRun struct {
	DiagnosticRunID  string                      `json:"diagnostic_run_id"`
	WorkspaceID      string                      `json:"workspace_id"`
	SourceKind       string                      `json:"source_kind"`
	SourceName       string                      `json:"source_name"`
	BatchFingerprint string                      `json:"batch_fingerprint"`
	ReplayArchive    *DiagnosticReplayArchive    `json:"replay_archive,omitempty"`
	SemanticBaseline *DiagnosticSemanticBaseline `json:"semantic_baseline,omitempty"`
	HeadSHA          *string                     `json:"head_sha,omitempty"`
	DirtyFiles       int                         `json:"dirty_files"`
	WorktreeDirty    bool                        `json:"worktree_dirty"`
	DiagnosticCount  int                         `json:"diagnostic_count"`
	SeverityCounts   map[string]int              `json:"severity_counts"`
	InputPath        string                      `json:"input_path"`
	PostgresSchema   string                      `json:"postgres_schema"`
	CreatedAt        string                      `json:"created_at"`
}

type DiagnosticReplayArchive struct {
	RawPayload            any            `json:"raw_payload,omitempty"`
	RawPayloadSHA256      string         `json:"raw_payload_sha256"`
	RawPayloadBytes       int            `json:"raw_payload_bytes"`
	ServerProvenance      map[string]any `json:"server_provenance"`
	NormalizationContract string         `json:"normalization_contract"`
	ReplayReadiness       string         `json:"replay_readiness"`
	Warnings              []string       `json:"warnings"`
	GeneratedAt           string         `json:"generated_at"`
}

type DiagnosticSemanticBaseline struct {
	IndexRunID       string   `json:"index_run_id"`
	IndexFingerprint string   `json:"index_fingerprint"`
	Surface          string   `json:"surface"`
	Strategy         string   `json:"strategy"`
	CoveredPaths     []string `json:"covered_paths"`
}

type DiagnosticLinkCandidate struct {
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

type DiagnosticLinkContext struct {
	CandidateCount  int                       `json:"candidate_count"`
	Candidates      []DiagnosticLinkCandidate `json:"candidates"`
	EvidenceSource  string                    `json:"evidence_source"`
	SelectionReason string                    `json:"selection_reason"`
	Warnings        []string                  `json:"warnings"`
	GeneratedAt     string                    `json:"generated_at"`
}

type DiagnosticRecord struct {
	WorkspaceID      string                  `json:"workspace_id"`
	DiagnosticRunID  string                  `json:"diagnostic_run_id"`
	Path             string                  `json:"path"`
	RangeStartLine   int                     `json:"range_start_line"`
	RangeStartColumn int                     `json:"range_start_column"`
	RangeEndLine     int                     `json:"range_end_line"`
	RangeEndColumn   int                     `json:"range_end_column"`
	Severity         string                  `json:"severity"`
	Message          string                  `json:"message"`
	SourceKind       string                  `json:"source_kind"`
	SourceName       string                  `json:"source_name"`
	RuleCode         *string                 `json:"rule_code,omitempty"`
	Fingerprint      string                  `json:"fingerprint"`
	HeadSHA          *string                 `json:"head_sha,omitempty"`
	ContentHash      *string                 `json:"content_hash,omitempty"`
	LinkStatus       string                  `json:"link_status"`
	LinkedSymbol     *DiagnosticLinkedSymbol `json:"linked_symbol,omitempty"`
	LinkContext      *DiagnosticLinkContext  `json:"link_context,omitempty"`
	GeneratedAt      string                  `json:"generated_at"`
}

type DiagnosticsReadResult struct {
	WorkspaceID string             `json:"workspace_id"`
	Baseline    *DiagnosticRun     `json:"baseline,omitempty"`
	Diagnostics []DiagnosticRecord `json:"diagnostics"`
	Warnings    []string           `json:"warnings"`
	GeneratedAt string             `json:"generated_at"`
}

type DiagnosticsBatch = rustcore.DiagnosticsBatch

func ReadLiveDiagnostics(dataDir string, workspaceID string, relativePath string) (*DiagnosticsBatch, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeWorkspaceFile(workspace.RootPath, relativePath)
	if err != nil {
		return nil, err
	}
	config, err := resolveLSPServerForPath(workspace.RootPath, normalized)
	if err != nil {
		return nil, err
	}
	session, err := acquireLSPSession(dataDir, workspaceID, workspace.RootPath, config)
	if err != nil {
		return nil, err
	}
	absolutePath := filepath.Join(workspace.RootPath, filepath.FromSlash(normalized))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	payload, err := session.liveDiagnostics(ctx, absolutePath)
	if err != nil {
		releaseLSPSession(workspaceID, config.ServerID, session)
		return nil, err
	}
	return rustcore.NormalizeDiagnosticsPayload(ctx, workspaceID, workspace.RootPath, payload, "lsp", config.ServerID)
}

func PlanDiagnostics(dataDir string, workspaceID string, request DiagnosticsRequest) (*DiagnosticsPlan, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := strings.TrimSpace(firstConfiguredString(request.DSN, settings.PostgresDSN))
	targetSchema := strings.TrimSpace(firstConfiguredString(request.SchemaName, &settings.PostgresSchema))
	if targetSchema == "" {
		targetSchema = "xmustard"
	}
	blockers := []string{}
	warnings := []string{}
	sourceKind := normalizeDiagnosticSourceKind(request.SourceKind)
	sourceName := strings.TrimSpace(request.SourceName)
	if sourceName == "" {
		sourceName = "unknown"
	}
	if targetDSN == "" {
		blockers = append(blockers, "Postgres DSN is not configured; diagnostics need durable baseline storage.")
	}
	inputPath, inputErr := resolveDiagnosticsInputPath(workspace.RootPath, request.InputPath)
	if inputErr != nil {
		blockers = append(blockers, inputErr.Error())
	}
	worktree := readWorktreeStatus(workspace.RootPath)
	var batch *DiagnosticsBatch
	var fingerprint *string
	var replayArchive *DiagnosticReplayArchive
	if inputErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		batch, err = rustcore.NormalizeDiagnostics(ctx, workspaceID, workspace.RootPath, inputPath, sourceKind, sourceName)
		if err != nil {
			blockers = append(blockers, err.Error())
		} else {
			warnings = append(warnings, batch.Warnings...)
			value := diagnosticsBatchFingerprint(workspaceID, sourceKind, sourceName, batch.Diagnostics, worktree.HeadSHA)
			fingerprint = &value
			archive, err := rustcore.ArchiveDiagnosticsPayload(ctx, workspaceID, inputPath, sourceKind, sourceName, diagnosticServerProvenance(workspace.RootPath, sourceKind, sourceName, inputPath, batch))
			if err != nil {
				blockers = append(blockers, err.Error())
			} else {
				replayArchive = diagnosticsReplayArchiveFromRust(archive)
				warnings = append(warnings, archive.Warnings...)
			}
		}
	}
	if worktree.DirtyFiles > 0 {
		warnings = append(warnings, "Worktree has dirty files; this diagnostics baseline should be treated as provisional.")
	}
	nextActions := []string{
		"Run diagnostics run after reviewing the normalized diagnostics count and source provenance.",
		"Feed LSP publishDiagnostics JSON into --input-path for the first bounded Phase 3 ingestion path.",
	}
	if targetDSN == "" {
		nextActions = append([]string{"Configure Postgres or pass --dsn for this diagnostics run."}, nextActions...)
	}
	counts := map[string]int{}
	if batch != nil {
		counts = batch.SeverityCounts
	}
	return &DiagnosticsPlan{
		WorkspaceID:        workspaceID,
		RootPath:           workspace.RootPath,
		InputPath:          inputPath,
		SourceKind:         sourceKind,
		SourceName:         sourceName,
		DiagnosticCount:    diagnosticsBatchCount(batch),
		SeverityCounts:     counts,
		HeadSHA:            worktree.HeadSHA,
		DirtyFiles:         worktree.DirtyFiles,
		WorktreeDirty:      worktree.DirtyFiles > 0,
		BatchFingerprint:   fingerprint,
		ReplayArchive:      replayArchive,
		PostgresConfigured: targetDSN != "",
		PostgresSchema:     targetSchema,
		Blockers:           dedupeSemanticStrings(blockers),
		Warnings:           dedupeSemanticStrings(warnings),
		NextActions:        nextActions,
		CanRun:             len(blockers) == 0,
		GeneratedAt:        nowUTC(),
		NormalizedBatch:    batch,
	}, nil
}

func RunDiagnostics(dataDir string, workspaceID string, request DiagnosticsRequest) (*DiagnosticsRunResult, error) {
	plan, err := PlanDiagnostics(dataDir, workspaceID, request)
	if err != nil {
		return nil, err
	}
	if request.DryRun {
		return &DiagnosticsRunResult{
			WorkspaceID: workspaceID,
			DryRun:      true,
			Plan:        plan,
			Message:     "Diagnostics dry run completed; no rows were written.",
			GeneratedAt: nowUTC(),
		}, nil
	}
	if !plan.CanRun {
		return &DiagnosticsRunResult{
			WorkspaceID: workspaceID,
			DryRun:      false,
			Plan:        plan,
			Message:     "Diagnostics run is blocked; inspect plan.blockers.",
			GeneratedAt: nowUTC(),
		}, nil
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := strings.TrimSpace(firstConfiguredString(request.DSN, settings.PostgresDSN))
	baseline, rows, err := persistDiagnosticsBaseline(targetDSN, plan.PostgresSchema, plan)
	if err != nil {
		return nil, err
	}
	if err := appendWorkspaceSemanticActivity(
		dataDir,
		workspaceID,
		"postgres.materialize.diagnostics",
		fmt.Sprintf("Materialized %d normalized diagnostic row(s)", rows),
		map[string]any{
			"source_kind":       plan.SourceKind,
			"source_name":       plan.SourceName,
			"schema_name":       plan.PostgresSchema,
			"diagnostic_rows":   rows,
			"batch_fingerprint": firstNonEmptyPtr(plan.BatchFingerprint),
		},
	); err != nil {
		return nil, err
	}
	return &DiagnosticsRunResult{
		WorkspaceID:    workspaceID,
		DryRun:         false,
		Plan:           plan,
		Baseline:       baseline,
		DiagnosticRows: rows,
		Message:        fmt.Sprintf("Materialized %d normalized diagnostic row(s) into Postgres schema '%s'.", rows, plan.PostgresSchema),
		GeneratedAt:    nowUTC(),
	}, nil
}

func ReadDiagnosticsStatus(dataDir string, workspaceID string) (*DiagnosticsStatus, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := strings.TrimSpace(firstConfiguredString(nil, settings.PostgresDSN))
	schema := settings.PostgresSchema
	if schema == "" {
		schema = "xmustard"
	}
	worktree := readWorktreeStatus(workspace.RootPath)
	warnings := []string{}
	if targetDSN == "" {
		return &DiagnosticsStatus{
			WorkspaceID:        workspaceID,
			Status:             "blocked",
			PostgresConfigured: false,
			PostgresSchema:     schema,
			CurrentHeadSHA:     worktree.HeadSHA,
			CurrentDirtyFiles:  worktree.DirtyFiles,
			StaleReasons:       []string{"Postgres DSN is not configured, so no diagnostics baseline can be read."},
			GeneratedAt:        nowUTC(),
		}, nil
	}
	baseline, err := readLatestDiagnosticRun(targetDSN, schema, workspaceID)
	if err != nil {
		return nil, err
	}
	status := "fresh"
	staleReasons := []string{}
	if baseline == nil {
		status = "no_baseline"
		staleReasons = append(staleReasons, "No diagnostics baseline has been materialized for this workspace.")
	} else if worktree.HeadSHA != nil && baseline.HeadSHA != nil && *worktree.HeadSHA != *baseline.HeadSHA {
		status = "stale"
		staleReasons = append(staleReasons, "Current HEAD does not match the latest diagnostics baseline.")
	}
	if worktree.DirtyFiles > 0 {
		if status == "fresh" {
			status = "dirty_provisional"
		}
		staleReasons = append(staleReasons, "Worktree has dirty files, so diagnostics freshness is provisional.")
	}
	return &DiagnosticsStatus{
		WorkspaceID:        workspaceID,
		Status:             status,
		PostgresConfigured: true,
		PostgresSchema:     schema,
		CurrentHeadSHA:     worktree.HeadSHA,
		CurrentDirtyFiles:  worktree.DirtyFiles,
		Baseline:           baseline,
		StaleReasons:       dedupeSemanticStrings(staleReasons),
		Warnings:           warnings,
		GeneratedAt:        nowUTC(),
	}, nil
}

func ReadDiagnostics(dataDir string, workspaceID string, diagnosticRunID string) (*DiagnosticsReadResult, error) {
	if _, err := getWorkspaceRecord(dataDir, workspaceID); err != nil {
		return nil, err
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := strings.TrimSpace(firstConfiguredString(nil, settings.PostgresDSN))
	schema := settings.PostgresSchema
	if schema == "" {
		schema = "xmustard"
	}
	if targetDSN == "" {
		return nil, fmt.Errorf("%w: Postgres DSN is required to read diagnostics", ErrInvalidDiagnosticsRequest)
	}
	var baseline *DiagnosticRun
	if strings.TrimSpace(diagnosticRunID) != "" {
		baseline, err = readDiagnosticRunByID(targetDSN, schema, workspaceID, diagnosticRunID)
		if err != nil {
			return nil, err
		}
		if baseline == nil {
			return nil, fmt.Errorf("%w: diagnostics run not found: %s", ErrInvalidDiagnosticsRequest, strings.TrimSpace(diagnosticRunID))
		}
	} else {
		baseline, err = readLatestDiagnosticRun(targetDSN, schema, workspaceID)
		if err != nil {
			return nil, err
		}
	}
	if baseline == nil {
		return &DiagnosticsReadResult{WorkspaceID: workspaceID, Diagnostics: []DiagnosticRecord{}, Warnings: []string{"No diagnostics baseline has been materialized."}, GeneratedAt: nowUTC()}, nil
	}
	diagnostics, err := readDiagnosticRows(targetDSN, schema, workspaceID, baseline.DiagnosticRunID)
	if err != nil {
		return nil, err
	}
	return &DiagnosticsReadResult{
		WorkspaceID: workspaceID,
		Baseline:    baseline,
		Diagnostics: diagnostics,
		Warnings:    diagnosticReplayWarnings(baseline, diagnostics),
		GeneratedAt: nowUTC(),
	}, nil
}

func persistDiagnosticsBaseline(dsn string, schema string, plan *DiagnosticsPlan) (*DiagnosticRun, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	connection, err := connectSemanticPostgres(ctx, dsn)
	if err != nil {
		return nil, 0, fmt.Errorf("connect Postgres: %w", err)
	}
	defer connection.Close(context.Background())
	if err := upsertSemanticWorkspace(ctx, connection, schema, plan.WorkspaceID, filepath.Base(plan.RootPath), plan.RootPath); err != nil {
		return nil, 0, err
	}
	runID := "diag_" + hashID(plan.WorkspaceID, firstNonEmptyPtr(plan.BatchFingerprint), nowUTC())[:12]
	countsJSON, err := json.Marshal(plan.SeverityCounts)
	if err != nil {
		return nil, 0, fmt.Errorf("encode diagnostic severity counts: %w", err)
	}
	semanticBaseline, err := resolveDiagnosticSemanticBaseline(ctx, connection, schema, plan.WorkspaceID, plan.HeadSHA, diagnosticPaths(plan.NormalizedBatch.Diagnostics))
	if err != nil {
		return nil, 0, err
	}
	var semanticBaselineJSON *string
	if semanticBaseline != nil {
		payload, err := json.Marshal(semanticBaseline)
		if err != nil {
			return nil, 0, fmt.Errorf("encode diagnostic semantic baseline: %w", err)
		}
		value := string(payload)
		semanticBaselineJSON = &value
	}
	var (
		rawPayloadJSON        *string
		serverProvenanceJSON  *string
		replayWarningsJSON    *string
		rawPayloadSHA256      string
		rawPayloadBytes       int
		normalizationContract string
		replayReadiness       string
	)
	if plan.ReplayArchive != nil {
		rawPayloadSHA256 = plan.ReplayArchive.RawPayloadSHA256
		rawPayloadBytes = plan.ReplayArchive.RawPayloadBytes
		normalizationContract = plan.ReplayArchive.NormalizationContract
		replayReadiness = plan.ReplayArchive.ReplayReadiness
		if payload, err := json.Marshal(plan.ReplayArchive.RawPayload); err != nil {
			return nil, 0, fmt.Errorf("encode diagnostic raw replay payload: %w", err)
		} else {
			value := string(payload)
			rawPayloadJSON = &value
		}
		if payload, err := json.Marshal(plan.ReplayArchive.ServerProvenance); err != nil {
			return nil, 0, fmt.Errorf("encode diagnostic server provenance: %w", err)
		} else {
			value := string(payload)
			serverProvenanceJSON = &value
		}
		if payload, err := json.Marshal(plan.ReplayArchive.Warnings); err != nil {
			return nil, 0, fmt.Errorf("encode diagnostic replay warnings: %w", err)
		} else {
			value := string(payload)
			replayWarningsJSON = &value
		}
	}
	if _, err := connection.Exec(
		ctx,
		fmt.Sprintf("insert into %s.diagnostic_runs (diagnostic_run_id, workspace_id, source_kind, source_name, batch_fingerprint, raw_payload_json, raw_payload_sha256, raw_payload_bytes, server_provenance_json, normalization_contract, replay_readiness, replay_warnings_json, semantic_baseline_json, head_sha, dirty_files, worktree_dirty, diagnostic_count, severity_counts_json, input_path, postgres_schema) values ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9::jsonb, $10, $11, $12::jsonb, $13::jsonb, $14, $15, $16, $17, $18::jsonb, $19, $20)", schema),
		runID,
		plan.WorkspaceID,
		plan.SourceKind,
		plan.SourceName,
		firstNonEmptyPtr(plan.BatchFingerprint),
		rawPayloadJSON,
		rawPayloadSHA256,
		rawPayloadBytes,
		serverProvenanceJSON,
		normalizationContract,
		replayReadiness,
		replayWarningsJSON,
		semanticBaselineJSON,
		plan.HeadSHA,
		plan.DirtyFiles,
		plan.WorktreeDirty,
		plan.DiagnosticCount,
		string(countsJSON),
		plan.InputPath,
		plan.PostgresSchema,
	); err != nil {
		return nil, 0, fmt.Errorf("insert diagnostic run: %w", err)
	}
	inserted := 0
	for _, row := range plan.NormalizedBatch.Diagnostics {
		fileID, err := upsertSemanticFile(ctx, connection, schema, plan.WorkspaceID, plan.RootPath, row.Path, nil, "source")
		if err != nil {
			return nil, 0, err
		}
		_, contentHash, err := semanticFileMetadata(plan.RootPath, row.Path)
		if err != nil {
			return nil, 0, err
		}
		linkState, err := resolveDiagnosticLinkPersistence(ctx, connection, schema, plan.WorkspaceID, row.Path, row.RangeStartLine, row.RangeEndLine, row.Fingerprint)
		if err != nil {
			return nil, 0, err
		}
		var linkedSymbolJSON *string
		if linkState.LinkedSymbol != nil {
			payload, err := json.Marshal(linkState.LinkedSymbol)
			if err != nil {
				return nil, 0, fmt.Errorf("encode linked diagnostic symbol: %w", err)
			}
			value := string(payload)
			linkedSymbolJSON = &value
		}
		var linkContextJSON *string
		if linkState.LinkContext != nil {
			payload, err := json.Marshal(linkState.LinkContext)
			if err != nil {
				return nil, 0, fmt.Errorf("encode diagnostic link context: %w", err)
			}
			value := string(payload)
			linkContextJSON = &value
		}
		if _, err := connection.Exec(
			ctx,
			fmt.Sprintf("insert into %s.diagnostics (diagnostic_run_id, workspace_id, file_id, path, range_start_line, range_start_column, range_end_line, range_end_column, severity, message, source_kind, source_name, rule_code, fingerprint, head_sha, content_hash, symbol_id, link_status, linked_symbol_json, link_context_json, generated_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19::jsonb, $20::jsonb, $21::timestamptz) on conflict (workspace_id, diagnostic_run_id, fingerprint) do update set message = excluded.message, severity = excluded.severity, symbol_id = excluded.symbol_id, link_status = excluded.link_status, linked_symbol_json = excluded.linked_symbol_json, link_context_json = excluded.link_context_json, generated_at = excluded.generated_at", schema),
			runID,
			row.WorkspaceID,
			fileID,
			row.Path,
			row.RangeStartLine,
			row.RangeStartColumn,
			row.RangeEndLine,
			row.RangeEndColumn,
			row.Severity,
			row.Message,
			row.SourceKind,
			row.SourceName,
			row.RuleCode,
			row.Fingerprint,
			plan.HeadSHA,
			contentHash,
			linkState.SymbolID,
			linkState.Status,
			linkedSymbolJSON,
			linkContextJSON,
			row.GeneratedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("insert diagnostic row: %w", err)
		}
		inserted++
	}
	return &DiagnosticRun{
		DiagnosticRunID:  runID,
		WorkspaceID:      plan.WorkspaceID,
		SourceKind:       plan.SourceKind,
		SourceName:       plan.SourceName,
		BatchFingerprint: firstNonEmptyPtr(plan.BatchFingerprint),
		ReplayArchive:    plan.ReplayArchive,
		SemanticBaseline: semanticBaseline,
		HeadSHA:          plan.HeadSHA,
		DirtyFiles:       plan.DirtyFiles,
		WorktreeDirty:    plan.WorktreeDirty,
		DiagnosticCount:  plan.DiagnosticCount,
		SeverityCounts:   plan.SeverityCounts,
		InputPath:        plan.InputPath,
		PostgresSchema:   plan.PostgresSchema,
		CreatedAt:        nowUTC(),
	}, inserted, nil
}

func readLatestDiagnosticRun(dsn string, schema string, workspaceID string) (*DiagnosticRun, error) {
	return readDiagnosticRun(dsn, schema, workspaceID, "")
}

func readDiagnosticRunByID(dsn string, schema string, workspaceID string, runID string) (*DiagnosticRun, error) {
	return readDiagnosticRun(dsn, schema, workspaceID, strings.TrimSpace(runID))
}

func readDiagnosticRun(dsn string, schema string, workspaceID string, targetRunID string) (*DiagnosticRun, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := connectSemanticPostgres(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect Postgres: %w", err)
	}
	defer connection.Close(context.Background())
	var (
		runID                 string
		sourceKind            string
		sourceName            string
		fingerprint           string
		rawPayloadJSON        []byte
		rawPayloadSHA256      *string
		rawPayloadBytes       int
		serverProvenanceJSON  []byte
		normalizationContract string
		replayReadiness       *string
		replayWarningsJSON    []byte
		semanticBaselineJSON  []byte
		headSHA               *string
		dirtyFiles            int
		dirty                 bool
		count                 int
		countsJSON            []byte
		inputPath             string
		pgSchema              string
		createdAt             string
	)
	query := fmt.Sprintf("select diagnostic_run_id, source_kind, source_name, batch_fingerprint, coalesce(raw_payload_json, 'null'::jsonb), raw_payload_sha256, coalesce(raw_payload_bytes, 0), coalesce(server_provenance_json, '{}'::jsonb), coalesce(normalization_contract, 'diagnostics.normalized.v1'), replay_readiness, coalesce(replay_warnings_json, '[]'::jsonb), coalesce(semantic_baseline_json, '{}'::jsonb), head_sha, dirty_files, worktree_dirty, diagnostic_count, severity_counts_json, input_path, postgres_schema, created_at::text from %s.diagnostic_runs where workspace_id = $1", schema)
	args := []any{workspaceID}
	if targetRunID != "" {
		query += " and diagnostic_run_id = $2"
		args = append(args, targetRunID)
	}
	query += " order by created_at desc limit 1"
	err = connection.QueryRow(ctx, query, args...).Scan(&runID, &sourceKind, &sourceName, &fingerprint, &rawPayloadJSON, &rawPayloadSHA256, &rawPayloadBytes, &serverProvenanceJSON, &normalizationContract, &replayReadiness, &replayWarningsJSON, &semanticBaselineJSON, &headSHA, &dirtyFiles, &dirty, &count, &countsJSON, &inputPath, &pgSchema, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read latest diagnostic run: %w", err)
	}
	counts := map[string]int{}
	_ = json.Unmarshal(countsJSON, &counts)
	semanticBaseline, err := decodeDiagnosticSemanticBaseline(semanticBaselineJSON)
	if err != nil {
		return nil, fmt.Errorf("decode diagnostic semantic baseline: %w", err)
	}
	replayArchive, err := decodeDiagnosticReplayArchive(sourceKind, rawPayloadJSON, rawPayloadSHA256, rawPayloadBytes, serverProvenanceJSON, normalizationContract, replayReadiness, replayWarningsJSON)
	if err != nil {
		return nil, fmt.Errorf("decode diagnostic replay archive: %w", err)
	}
	return &DiagnosticRun{
		DiagnosticRunID:  runID,
		WorkspaceID:      workspaceID,
		SourceKind:       sourceKind,
		SourceName:       sourceName,
		BatchFingerprint: fingerprint,
		ReplayArchive:    replayArchive,
		SemanticBaseline: semanticBaseline,
		HeadSHA:          headSHA,
		DirtyFiles:       dirtyFiles,
		WorktreeDirty:    dirty,
		DiagnosticCount:  count,
		SeverityCounts:   counts,
		InputPath:        inputPath,
		PostgresSchema:   pgSchema,
		CreatedAt:        createdAt,
	}, nil
}

func readDiagnosticRows(dsn string, schema string, workspaceID string, runID string) ([]DiagnosticRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := connectSemanticPostgres(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect Postgres: %w", err)
	}
	defer connection.Close(context.Background())
	var payload []byte
	err = connection.QueryRow(
		ctx,
		fmt.Sprintf("select coalesce(jsonb_agg(jsonb_build_object('workspace_id', workspace_id, 'diagnostic_run_id', diagnostic_run_id, 'path', path, 'range_start_line', range_start_line, 'range_start_column', range_start_column, 'range_end_line', range_end_line, 'range_end_column', range_end_column, 'severity', severity, 'message', message, 'source_kind', source_kind, 'source_name', source_name, 'rule_code', rule_code, 'fingerprint', fingerprint, 'head_sha', head_sha, 'content_hash', content_hash, 'link_status', coalesce(link_status, 'unevaluated'), 'linked_symbol', linked_symbol_json, 'link_context', link_context_json, 'generated_at', generated_at::text) order by severity, path), '[]'::jsonb) from %s.diagnostics where workspace_id = $1 and diagnostic_run_id = $2", schema),
		workspaceID,
		runID,
	).Scan(&payload)
	if err != nil {
		return nil, fmt.Errorf("read diagnostic rows: %w", err)
	}
	rows := []DiagnosticRecord{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &rows); err != nil {
			return nil, fmt.Errorf("decode diagnostic rows: %w", err)
		}
	}
	for idx := range rows {
		if strings.TrimSpace(rows[idx].LinkStatus) == "" {
			rows[idx].LinkStatus = diagnosticLinkStatusUnevaluated
		}
	}
	return rows, nil
}

func resolveDiagnosticsInputPath(rootPath string, inputPath string) (string, error) {
	trimmed := strings.TrimSpace(inputPath)
	if trimmed == "" {
		return "", fmt.Errorf("%w: input_path is required", ErrInvalidDiagnosticsRequest)
	}
	path := trimmed
	if !filepath.IsAbs(path) {
		path = filepath.Join(rootPath, filepath.FromSlash(path))
	}
	info, err := os.Stat(path)
	if err != nil {
		return path, fmt.Errorf("%w: diagnostics input not found: %s", ErrInvalidDiagnosticsRequest, trimmed)
	}
	if info.IsDir() {
		return path, fmt.Errorf("%w: diagnostics input must be a JSON file: %s", ErrInvalidDiagnosticsRequest, trimmed)
	}
	return path, nil
}

func normalizeDiagnosticSourceKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "lsp", "compiler", "test", "scanner", "manual":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "lsp"
	}
}

func diagnosticsBatchCount(batch *DiagnosticsBatch) int {
	if batch == nil {
		return 0
	}
	return batch.DiagnosticCount
}

func diagnosticsBatchFingerprint(workspaceID string, sourceKind string, sourceName string, diagnostics []rustcore.NormalizedDiagnostic, headSHA *string) string {
	hasher := sha256.New()
	hasher.Write([]byte(workspaceID))
	hasher.Write([]byte{0})
	hasher.Write([]byte(sourceKind))
	hasher.Write([]byte{0})
	hasher.Write([]byte(sourceName))
	hasher.Write([]byte{0})
	if headSHA != nil {
		hasher.Write([]byte(*headSHA))
	}
	for _, item := range diagnostics {
		hasher.Write([]byte{0})
		hasher.Write([]byte(item.Fingerprint))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

type diagnosticLinkPersistence struct {
	Status       string
	SymbolID     *int64
	LinkedSymbol *DiagnosticLinkedSymbol
	LinkContext  *DiagnosticLinkContext
}

func resolveDiagnosticLinkPersistence(ctx context.Context, connection semanticMaterializationConn, schema string, workspaceID string, relativePath string, startLine int, endLine int, diagnosticFingerprint string) (*diagnosticLinkPersistence, error) {
	ready, err := hasMaterializedSymbolSummary(ctx, connection, schema, workspaceID, relativePath)
	if err != nil {
		return nil, err
	}
	if !ready {
		return &diagnosticLinkPersistence{Status: diagnosticLinkStatusSymbolsUnavailable}, nil
	}
	linkContext, link, err := findBestDiagnosticSymbolLink(ctx, connection, schema, workspaceID, relativePath, startLine, endLine, diagnosticFingerprint)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return &diagnosticLinkPersistence{
			Status:      diagnosticLinkStatusEvaluatedUnlinked,
			LinkContext: linkContext,
		}, nil
	}
	symbolID := link.SymbolID
	return &diagnosticLinkPersistence{
		Status:       diagnosticLinkStatusLinked,
		SymbolID:     &symbolID,
		LinkedSymbol: link,
		LinkContext:  linkContext,
	}, nil
}

func diagnosticReplayWarnings(baseline *DiagnosticRun, rows []DiagnosticRecord) []string {
	legacy := 0
	unavailable := 0
	archivedLinkContext := 0
	for _, row := range rows {
		if row.LinkContext != nil {
			archivedLinkContext++
		}
		switch row.LinkStatus {
		case diagnosticLinkStatusUnevaluated:
			legacy++
		case diagnosticLinkStatusSymbolsUnavailable:
			unavailable++
		}
	}
	warnings := []string{}
	if baseline != nil && baseline.ReplayArchive == nil {
		warnings = append(warnings, "Diagnostics run predates raw payload archive and cannot fully replay the original diagnostic source payload.")
	}
	if baseline != nil && baseline.ReplayArchive != nil {
		warnings = append(warnings, baseline.ReplayArchive.Warnings...)
	}
	if legacy > 0 {
		warnings = append(warnings, fmt.Sprintf("%d diagnostic row(s) predate durable link replay and remain link_status=unevaluated.", legacy))
	}
	if unavailable > 0 {
		warnings = append(warnings, fmt.Sprintf("%d diagnostic row(s) were stored before materialized symbols were available, so durable link replay is unavailable.", unavailable))
	}
	if archivedLinkContext > 0 && baseline != nil && baseline.SemanticBaseline == nil {
		warnings = append(warnings, "Diagnostic rows include archived link context, but this run was not anchored to a stored semantic baseline.")
	}
	return dedupeSemanticStrings(warnings)
}

type diagnosticSemanticBaselineCandidate struct {
	IndexRunID        string   `json:"index_run_id"`
	IndexFingerprint  string   `json:"index_fingerprint"`
	Surface           string   `json:"surface"`
	Strategy          string   `json:"strategy"`
	MaterializedPaths []string `json:"materialized_paths"`
}

func resolveDiagnosticSemanticBaseline(ctx context.Context, connection semanticMaterializationConn, schema string, workspaceID string, headSHA *string, paths []string) (*DiagnosticSemanticBaseline, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var payload []byte
	if err := connection.QueryRow(
		ctx,
		fmt.Sprintf(`
			select coalesce(jsonb_agg(jsonb_build_object(
				'index_run_id', candidate.index_run_id,
				'index_fingerprint', candidate.index_fingerprint,
				'surface', candidate.surface,
				'strategy', candidate.strategy,
				'materialized_paths', candidate.materialized_paths_json
			) order by candidate.created_at desc), '[]'::jsonb)
			from (
				select index_run_id, index_fingerprint, surface, strategy, materialized_paths_json, created_at
				from %s.semantic_index_runs
				where workspace_id = $1 and head_sha is not distinct from $2
				order by created_at desc
				limit 16
			) candidate
		`, schema),
		workspaceID,
		headSHA,
	).Scan(&payload); err != nil {
		return nil, fmt.Errorf("read diagnostic semantic baseline candidates: %w", err)
	}
	candidates := []diagnosticSemanticBaselineCandidate{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &candidates); err != nil {
			return nil, fmt.Errorf("decode diagnostic semantic baseline candidates: %w", err)
		}
	}
	needed := map[string]struct{}{}
	for _, item := range paths {
		needed[item] = struct{}{}
	}
	for _, candidate := range candidates {
		covered := []string{}
		seen := map[string]struct{}{}
		for _, path := range candidate.MaterializedPaths {
			if _, ok := needed[path]; !ok {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			covered = append(covered, path)
		}
		if len(covered) != len(needed) {
			continue
		}
		return &DiagnosticSemanticBaseline{
			IndexRunID:       candidate.IndexRunID,
			IndexFingerprint: candidate.IndexFingerprint,
			Surface:          candidate.Surface,
			Strategy:         candidate.Strategy,
			CoveredPaths:     covered,
		}, nil
	}
	return nil, nil
}

func diagnosticPaths(rows []rustcore.NormalizedDiagnostic) []string {
	seen := map[string]struct{}{}
	paths := []string{}
	for _, row := range rows {
		if row.Path == "" {
			continue
		}
		if _, ok := seen[row.Path]; ok {
			continue
		}
		seen[row.Path] = struct{}{}
		paths = append(paths, row.Path)
	}
	return paths
}

func diagnosticServerProvenance(rootPath string, sourceKind string, sourceName string, inputPath string, batch *DiagnosticsBatch) map[string]any {
	provenance := map[string]any{
		"source_mode":            "input_file",
		"source_kind":            normalizeDiagnosticSourceKind(sourceKind),
		"server_id":              strings.TrimSpace(sourceName),
		"input_path":             inputPath,
		"normalization_contract": "diagnostics.normalized.v1",
	}
	if normalizeDiagnosticSourceKind(sourceKind) != "lsp" {
		return provenance
	}
	primaryPath := diagnosticPrimaryPath(batch)
	if primaryPath == "" {
		provenance["provenance_level"] = "declared_server_id_only"
		return provenance
	}
	config, err := resolveLSPServerForPath(rootPath, primaryPath)
	if err != nil {
		provenance["provenance_level"] = "declared_server_id_only"
		return provenance
	}
	provenance["provenance_level"] = "resolved_lsp_command"
	provenance["language_id"] = config.LanguageID
	provenance["server_command"] = append([]string{}, config.Command...)
	provenance["resolved_from_path"] = primaryPath
	return provenance
}

func diagnosticsReplayArchiveFromRust(archive *rustcore.DiagnosticsReplayArchive) *DiagnosticReplayArchive {
	if archive == nil {
		return nil
	}
	return &DiagnosticReplayArchive{
		RawPayload:            archive.RawPayload,
		RawPayloadSHA256:      archive.RawPayloadSHA256,
		RawPayloadBytes:       archive.RawPayloadBytes,
		ServerProvenance:      archive.ServerProvenance,
		NormalizationContract: "diagnostics.normalized.v1",
		ReplayReadiness:       archive.ReplayReadiness,
		Warnings:              archive.Warnings,
		GeneratedAt:           archive.GeneratedAt,
	}
}

func decodeDiagnosticReplayArchive(sourceKind string, rawPayloadJSON []byte, rawPayloadSHA256 *string, rawPayloadBytes int, serverProvenanceJSON []byte, normalizationContract string, replayReadiness *string, replayWarningsJSON []byte) (*DiagnosticReplayArchive, error) {
	if rawPayloadSHA256 == nil || strings.TrimSpace(*rawPayloadSHA256) == "" {
		return nil, nil
	}
	var rawPayload any
	if len(rawPayloadJSON) > 0 {
		if err := json.Unmarshal(rawPayloadJSON, &rawPayload); err != nil {
			return nil, err
		}
	}
	serverProvenance := map[string]any{}
	if len(serverProvenanceJSON) > 0 {
		if err := json.Unmarshal(serverProvenanceJSON, &serverProvenance); err != nil {
			return nil, err
		}
	}
	warnings := []string{}
	if len(replayWarningsJSON) > 0 {
		if err := json.Unmarshal(replayWarningsJSON, &warnings); err != nil {
			return nil, err
		}
	}
	readiness := ""
	if replayReadiness != nil {
		readiness = strings.TrimSpace(*replayReadiness)
	}
	if readiness == "" {
		if diagnosticArchiveHasSufficientServerProvenance(sourceKind, serverProvenance) {
			readiness = "raw_payload_and_server_provenance_archived"
		} else {
			readiness = "raw_payload_archived_with_provenance_warnings"
			warnings = append(warnings, "Diagnostics replay archive predates persisted replay_readiness and does not prove complete server provenance.")
		}
	}
	if strings.TrimSpace(normalizationContract) == "" {
		normalizationContract = "diagnostics.normalized.v1"
	}
	return &DiagnosticReplayArchive{
		RawPayload:            rawPayload,
		RawPayloadSHA256:      strings.TrimSpace(*rawPayloadSHA256),
		RawPayloadBytes:       rawPayloadBytes,
		ServerProvenance:      serverProvenance,
		NormalizationContract: normalizationContract,
		ReplayReadiness:       readiness,
		Warnings:              warnings,
	}, nil
}

func diagnosticArchiveHasSufficientServerProvenance(sourceKind string, provenance map[string]any) bool {
	if len(provenance) == 0 {
		return false
	}
	serverID, _ := provenance["server_id"].(string)
	if strings.TrimSpace(serverID) == "" {
		return false
	}
	if normalizeDiagnosticSourceKind(sourceKind) != "lsp" {
		return true
	}
	switch typed := provenance["server_command"].(type) {
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
		}
	case []string:
		for _, item := range typed {
			if strings.TrimSpace(item) != "" {
				return true
			}
		}
	case string:
		return strings.TrimSpace(typed) != ""
	}
	return false
}

func diagnosticPrimaryPath(batch *DiagnosticsBatch) string {
	if batch == nil {
		return ""
	}
	for _, item := range batch.Diagnostics {
		if strings.TrimSpace(item.Path) != "" {
			return item.Path
		}
	}
	return ""
}

func decodeDiagnosticSemanticBaseline(payload []byte) (*DiagnosticSemanticBaseline, error) {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return nil, nil
	}
	var baseline DiagnosticSemanticBaseline
	if err := json.Unmarshal(payload, &baseline); err != nil {
		return nil, err
	}
	if baseline.IndexRunID == "" {
		return nil, nil
	}
	return &baseline, nil
}

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

type DiagnosticsRequest struct {
	InputPath  string  `json:"input_path"`
	SourceKind string  `json:"source_kind"`
	SourceName string  `json:"source_name"`
	DSN        *string `json:"dsn,omitempty"`
	SchemaName *string `json:"schema_name,omitempty"`
	DryRun     bool    `json:"dry_run"`
}

type DiagnosticsPlan struct {
	WorkspaceID        string            `json:"workspace_id"`
	RootPath           string            `json:"root_path"`
	InputPath          string            `json:"input_path"`
	SourceKind         string            `json:"source_kind"`
	SourceName         string            `json:"source_name"`
	DiagnosticCount    int               `json:"diagnostic_count"`
	SeverityCounts     map[string]int    `json:"severity_counts"`
	HeadSHA            *string           `json:"head_sha,omitempty"`
	DirtyFiles         int               `json:"dirty_files"`
	WorktreeDirty      bool              `json:"worktree_dirty"`
	BatchFingerprint   *string           `json:"batch_fingerprint,omitempty"`
	PostgresConfigured bool              `json:"postgres_configured"`
	PostgresSchema     string            `json:"postgres_schema"`
	Blockers           []string          `json:"blockers"`
	Warnings           []string          `json:"warnings"`
	NextActions        []string          `json:"next_actions"`
	CanRun             bool              `json:"can_run"`
	GeneratedAt        string            `json:"generated_at"`
	NormalizedBatch    *DiagnosticsBatch `json:"normalized_batch,omitempty"`
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
	DiagnosticRunID  string         `json:"diagnostic_run_id"`
	WorkspaceID      string         `json:"workspace_id"`
	SourceKind       string         `json:"source_kind"`
	SourceName       string         `json:"source_name"`
	BatchFingerprint string         `json:"batch_fingerprint"`
	HeadSHA          *string        `json:"head_sha,omitempty"`
	DirtyFiles       int            `json:"dirty_files"`
	WorktreeDirty    bool           `json:"worktree_dirty"`
	DiagnosticCount  int            `json:"diagnostic_count"`
	SeverityCounts   map[string]int `json:"severity_counts"`
	InputPath        string         `json:"input_path"`
	PostgresSchema   string         `json:"postgres_schema"`
	CreatedAt        string         `json:"created_at"`
}

type DiagnosticRecord struct {
	WorkspaceID      string  `json:"workspace_id"`
	DiagnosticRunID  string  `json:"diagnostic_run_id"`
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
	HeadSHA          *string `json:"head_sha,omitempty"`
	ContentHash      *string `json:"content_hash,omitempty"`
	GeneratedAt      string  `json:"generated_at"`
}

type DiagnosticsReadResult struct {
	WorkspaceID string             `json:"workspace_id"`
	Baseline    *DiagnosticRun     `json:"baseline,omitempty"`
	Diagnostics []DiagnosticRecord `json:"diagnostics"`
	Warnings    []string           `json:"warnings"`
	GeneratedAt string             `json:"generated_at"`
}

type DiagnosticsBatch = rustcore.DiagnosticsBatch

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

func ReadDiagnostics(dataDir string, workspaceID string) (*DiagnosticsReadResult, error) {
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
	baseline, err := readLatestDiagnosticRun(targetDSN, schema, workspaceID)
	if err != nil {
		return nil, err
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
	if _, err := connection.Exec(
		ctx,
		fmt.Sprintf("insert into %s.diagnostic_runs (diagnostic_run_id, workspace_id, source_kind, source_name, batch_fingerprint, head_sha, dirty_files, worktree_dirty, diagnostic_count, severity_counts_json, input_path, postgres_schema) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12)", schema),
		runID,
		plan.WorkspaceID,
		plan.SourceKind,
		plan.SourceName,
		firstNonEmptyPtr(plan.BatchFingerprint),
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
		if _, err := connection.Exec(
			ctx,
			fmt.Sprintf("insert into %s.diagnostics (diagnostic_run_id, workspace_id, file_id, path, range_start_line, range_start_column, range_end_line, range_end_column, severity, message, source_kind, source_name, rule_code, fingerprint, head_sha, content_hash, generated_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17::timestamptz) on conflict (workspace_id, diagnostic_run_id, fingerprint) do update set message = excluded.message, severity = excluded.severity, generated_at = excluded.generated_at", schema),
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := connectSemanticPostgres(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect Postgres: %w", err)
	}
	defer connection.Close(context.Background())
	var (
		runID       string
		sourceKind  string
		sourceName  string
		fingerprint string
		headSHA     *string
		dirtyFiles  int
		dirty       bool
		count       int
		countsJSON  []byte
		inputPath   string
		pgSchema    string
		createdAt   string
	)
	err = connection.QueryRow(
		ctx,
		fmt.Sprintf("select diagnostic_run_id, source_kind, source_name, batch_fingerprint, head_sha, dirty_files, worktree_dirty, diagnostic_count, severity_counts_json, input_path, postgres_schema, created_at::text from %s.diagnostic_runs where workspace_id = $1 order by created_at desc limit 1", schema),
		workspaceID,
	).Scan(&runID, &sourceKind, &sourceName, &fingerprint, &headSHA, &dirtyFiles, &dirty, &count, &countsJSON, &inputPath, &pgSchema, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read latest diagnostic run: %w", err)
	}
	counts := map[string]int{}
	_ = json.Unmarshal(countsJSON, &counts)
	return &DiagnosticRun{
		DiagnosticRunID:  runID,
		WorkspaceID:      workspaceID,
		SourceKind:       sourceKind,
		SourceName:       sourceName,
		BatchFingerprint: fingerprint,
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
		fmt.Sprintf("select coalesce(jsonb_agg(jsonb_build_object('workspace_id', workspace_id, 'diagnostic_run_id', diagnostic_run_id, 'path', path, 'range_start_line', range_start_line, 'range_start_column', range_start_column, 'range_end_line', range_end_line, 'range_end_column', range_end_column, 'severity', severity, 'message', message, 'source_kind', source_kind, 'source_name', source_name, 'rule_code', rule_code, 'fingerprint', fingerprint, 'head_sha', head_sha, 'content_hash', content_hash, 'generated_at', generated_at::text) order by severity, path), '[]'::jsonb) from %s.diagnostics where workspace_id = $1 and diagnostic_run_id = $2", schema),
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

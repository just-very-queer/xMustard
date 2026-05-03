package workspaceops

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrInvalidSemanticRequest = errors.New("invalid semantic request")

type PostgresPathMaterializationRequest struct {
	Path       string  `json:"path"`
	DSN        *string `json:"dsn,omitempty"`
	SchemaName *string `json:"schema_name,omitempty"`
}

type PostgresSemanticSearchMaterializationRequest struct {
	Pattern    string  `json:"pattern"`
	Language   *string `json:"language,omitempty"`
	PathGlob   *string `json:"path_glob,omitempty"`
	Limit      int     `json:"limit"`
	DSN        *string `json:"dsn,omitempty"`
	SchemaName *string `json:"schema_name,omitempty"`
}

type PostgresWorkspaceSemanticMaterializationRequest struct {
	Strategy   string   `json:"strategy"`
	Paths      []string `json:"paths"`
	Limit      int      `json:"limit"`
	DSN        *string  `json:"dsn,omitempty"`
	SchemaName *string  `json:"schema_name,omitempty"`
}

type PostgresSemanticMaterializationResult struct {
	Applied           bool     `json:"applied"`
	DSNRedacted       *string  `json:"dsn_redacted,omitempty"`
	SchemaName        string   `json:"schema_name"`
	WorkspaceID       string   `json:"workspace_id"`
	Source            string   `json:"source"`
	Target            string   `json:"target"`
	MaterializedPaths []string `json:"materialized_paths"`
	FileRows          int      `json:"file_rows"`
	SymbolRows        int      `json:"symbol_rows"`
	SummaryRows       int      `json:"summary_rows"`
	QueryRows         int      `json:"query_rows"`
	MatchRows         int      `json:"match_rows"`
	Message           string   `json:"message"`
	GeneratedAt       string   `json:"generated_at"`
}

type PostgresWorkspaceSemanticMaterializationResult struct {
	Applied           bool     `json:"applied"`
	DSNRedacted       *string  `json:"dsn_redacted,omitempty"`
	SchemaName        string   `json:"schema_name"`
	WorkspaceID       string   `json:"workspace_id"`
	Strategy          string   `json:"strategy"`
	RequestedPaths    []string `json:"requested_paths"`
	MaterializedPaths []string `json:"materialized_paths"`
	SkippedPaths      []string `json:"skipped_paths"`
	FileRows          int      `json:"file_rows"`
	SymbolRows        int      `json:"symbol_rows"`
	SummaryRows       int      `json:"summary_rows"`
	Message           string   `json:"message"`
	GeneratedAt       string   `json:"generated_at"`
}

type SemanticPatternMatchRecord struct {
	Path          string   `json:"path"`
	Language      *string  `json:"language,omitempty"`
	LineStart     *int     `json:"line_start,omitempty"`
	LineEnd       *int     `json:"line_end,omitempty"`
	ColumnStart   *int     `json:"column_start,omitempty"`
	ColumnEnd     *int     `json:"column_end,omitempty"`
	MatchedText   string   `json:"matched_text"`
	ContextLines  *string  `json:"context_lines,omitempty"`
	MetaVariables []string `json:"meta_variables"`
	Reason        *string  `json:"reason,omitempty"`
	Score         int      `json:"score"`
}

type SemanticQueryMaterializationRecord struct {
	QueryRef    string  `json:"query_ref"`
	WorkspaceID string  `json:"workspace_id"`
	IssueID     *string `json:"issue_id,omitempty"`
	RunID       *string `json:"run_id,omitempty"`
	Source      string  `json:"source"`
	Reason      *string `json:"reason,omitempty"`
	Pattern     string  `json:"pattern"`
	Language    *string `json:"language,omitempty"`
	PathGlob    *string `json:"path_glob,omitempty"`
	Engine      string  `json:"engine"`
	MatchCount  int     `json:"match_count"`
	Truncated   bool    `json:"truncated"`
	Error       *string `json:"error,omitempty"`
}

type SemanticMatchMaterializationRecord struct {
	QueryRef      string   `json:"query_ref"`
	WorkspaceID   string   `json:"workspace_id"`
	Path          string   `json:"path"`
	Language      *string  `json:"language,omitempty"`
	LineStart     *int     `json:"line_start,omitempty"`
	LineEnd       *int     `json:"line_end,omitempty"`
	ColumnStart   *int     `json:"column_start,omitempty"`
	ColumnEnd     *int     `json:"column_end,omitempty"`
	MatchedText   string   `json:"matched_text"`
	ContextLines  *string  `json:"context_lines,omitempty"`
	MetaVariables []string `json:"meta_variables"`
	Reason        *string  `json:"reason,omitempty"`
	Score         int      `json:"score"`
}

type SemanticPatternQueryResult struct {
	WorkspaceID string                               `json:"workspace_id"`
	Pattern     string                               `json:"pattern"`
	Language    *string                              `json:"language,omitempty"`
	PathGlob    *string                              `json:"path_glob,omitempty"`
	Engine      string                               `json:"engine"`
	BinaryPath  *string                              `json:"binary_path,omitempty"`
	MatchCount  int                                  `json:"match_count"`
	Truncated   bool                                 `json:"truncated"`
	Matches     []SemanticPatternMatchRecord         `json:"matches"`
	QueryRow    *SemanticQueryMaterializationRecord  `json:"query_row,omitempty"`
	MatchRows   []SemanticMatchMaterializationRecord `json:"match_rows"`
	Error       *string                              `json:"error,omitempty"`
	GeneratedAt string                               `json:"generated_at"`
}

type semanticMaterializationConn interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Close(context.Context) error
}

var connectSemanticPostgres = func(ctx context.Context, dsn string) (semanticMaterializationConn, error) {
	return pgx.Connect(ctx, dsn)
}

func SearchSemanticPattern(dataDir string, workspaceID string, pattern string, language string, pathGlob string, limit int) (*SemanticPatternQueryResult, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("%w: pattern is required", ErrInvalidSemanticRequest)
	}
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	normalizedLanguage := trimOptional(optionalString(language))
	normalizedPathGlob := trimOptional(optionalString(pathGlob))
	matches, binaryPath, queryError, truncated := runAstGrepSemanticQuery(
		snapshot.Workspace.RootPath,
		pattern,
		normalizedLanguage,
		normalizedPathGlob,
		max(1, min(limit, 200)),
	)
	engine := "none"
	if binaryPath != nil {
		engine = "ast_grep"
	}
	queryRow := buildSemanticQueryRow(
		workspaceID,
		pattern,
		normalizedLanguage,
		normalizedPathGlob,
		engine,
		len(matches),
		truncated,
		queryError,
		"adhoc_tool",
		nil,
		nil,
		nil,
	)
	matchRows := buildSemanticMatchRows(workspaceID, queryRow.QueryRef, matches)
	return &SemanticPatternQueryResult{
		WorkspaceID: workspaceID,
		Pattern:     pattern,
		Language:    normalizedLanguage,
		PathGlob:    normalizedPathGlob,
		Engine:      engine,
		BinaryPath:  binaryPath,
		MatchCount:  len(matches),
		Truncated:   truncated,
		Matches:     matches,
		QueryRow:    queryRow,
		MatchRows:   matchRows,
		Error:       queryError,
		GeneratedAt: nowUTC(),
	}, nil
}

func MaterializePathSymbolsToPostgres(dataDir string, workspaceID string, request PostgresPathMaterializationRequest) (*PostgresSemanticMaterializationResult, error) {
	return materializePathSymbolsToPostgres(dataDir, workspaceID, request, true)
}

func materializePathSymbolsToPostgres(dataDir string, workspaceID string, request PostgresPathMaterializationRequest, recordActivity bool) (*PostgresSemanticMaterializationResult, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := firstConfiguredString(request.DSN, settings.PostgresDSN)
	if strings.TrimSpace(targetDSN) == "" {
		return nil, fmt.Errorf("%w: Postgres DSN is required for semantic materialization", ErrInvalidSemanticRequest)
	}
	targetSchema := strings.TrimSpace(firstConfiguredString(request.SchemaName, &settings.PostgresSchema))
	if targetSchema == "" {
		targetSchema = "xmustard"
	}
	normalizedSchema, err := validatePostgresSchemaName(targetSchema)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSemanticRequest, err)
	}
	if strings.TrimSpace(request.Path) == "" {
		return nil, fmt.Errorf("%w: path is required", ErrInvalidSemanticRequest)
	}
	pathSymbols, err := ReadPathSymbols(dataDir, workspaceID, request.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidSemanticRequest, err)
	}
	if pathSymbols.FileSummaryRow == nil {
		return nil, fmt.Errorf("%w: no symbol summary is available for %s", ErrInvalidSemanticRequest, pathSymbols.Path)
	}
	result, err := applyPathSymbolMaterialization(
		targetDSN,
		normalizedSchema,
		snapshot.Workspace.WorkspaceID,
		snapshot.Workspace.Name,
		snapshot.Workspace.RootPath,
		pathSymbols.Path,
		pathSymbols.FileSummaryRow,
		pathSymbols.SymbolRows,
	)
	if err != nil {
		return nil, err
	}
	if recordActivity {
		if err := appendWorkspaceSemanticActivity(
			dataDir,
			workspaceID,
			"postgres.materialize.path_symbols",
			"Materialized parser-backed symbol rows for "+pathSymbols.Path,
			map[string]any{
				"path":         pathSymbols.Path,
				"schema_name":  result.SchemaName,
				"symbol_rows":  result.SymbolRows,
				"summary_rows": result.SummaryRows,
			},
		); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func MaterializeWorkspaceSymbolsToPostgres(dataDir string, workspaceID string, request PostgresWorkspaceSemanticMaterializationRequest) (*PostgresWorkspaceSemanticMaterializationResult, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := firstConfiguredString(request.DSN, settings.PostgresDSN)
	if strings.TrimSpace(targetDSN) == "" {
		return nil, fmt.Errorf("%w: Postgres DSN is required for semantic materialization", ErrInvalidSemanticRequest)
	}
	targetSchema := strings.TrimSpace(firstConfiguredString(request.SchemaName, &settings.PostgresSchema))
	if targetSchema == "" {
		targetSchema = "xmustard"
	}
	normalizedSchema, err := validatePostgresSchemaName(targetSchema)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSemanticRequest, err)
	}
	requestedPaths, err := semanticMaterializationPaths(dataDir, workspaceID, snapshot.Workspace.RootPath, request.Strategy, request.Paths, request.Limit)
	if err != nil {
		return nil, err
	}
	materializedPaths := []string{}
	skippedPaths := []string{}
	fileRows := 0
	symbolRows := 0
	summaryRows := 0
	var redactedDSN *string
	for _, relativePath := range requestedPaths {
		result, materializeErr := materializePathSymbolsToPostgres(
			dataDir,
			workspaceID,
			PostgresPathMaterializationRequest{
				Path:       relativePath,
				DSN:        optionalString(targetDSN),
				SchemaName: optionalString(normalizedSchema),
			},
			false,
		)
		if materializeErr != nil {
			if errors.Is(materializeErr, os.ErrNotExist) || errors.Is(materializeErr, ErrInvalidSemanticRequest) {
				skippedPaths = append(skippedPaths, relativePath)
				continue
			}
			return nil, materializeErr
		}
		materializedPaths = append(materializedPaths, result.MaterializedPaths...)
		fileRows += result.FileRows
		symbolRows += result.SymbolRows
		summaryRows += result.SummaryRows
		redactedDSN = result.DSNRedacted
	}
	batchResult := &PostgresWorkspaceSemanticMaterializationResult{
		Applied:           len(materializedPaths) > 0,
		DSNRedacted:       redactedDSN,
		SchemaName:        normalizedSchema,
		WorkspaceID:       workspaceID,
		Strategy:          semanticStrategyOrDefault(request.Strategy),
		RequestedPaths:    requestedPaths,
		MaterializedPaths: dedupeStrings(materializedPaths, 500),
		SkippedPaths:      dedupeStrings(skippedPaths, 500),
		FileRows:          fileRows,
		SymbolRows:        symbolRows,
		SummaryRows:       summaryRows,
		Message:           fmt.Sprintf("Materialized parser-backed symbol rows for %d paths using strategy '%s' into Postgres schema '%s'.", len(materializedPaths), semanticStrategyOrDefault(request.Strategy), normalizedSchema),
		GeneratedAt:       nowUTC(),
	}
	if err := appendWorkspaceSemanticActivity(
		dataDir,
		workspaceID,
		"postgres.materialize.workspace_symbols",
		fmt.Sprintf("Materialized workspace symbol batch for %d paths", len(batchResult.MaterializedPaths)),
		map[string]any{
			"strategy":           batchResult.Strategy,
			"surface":            "cli",
			"schema_name":        batchResult.SchemaName,
			"requested_paths":    batchResult.RequestedPaths,
			"materialized_paths": batchResult.MaterializedPaths,
			"skipped_paths":      batchResult.SkippedPaths,
			"symbol_rows":        batchResult.SymbolRows,
			"summary_rows":       batchResult.SummaryRows,
		},
	); err != nil {
		return nil, err
	}
	return batchResult, nil
}

func MaterializeSemanticSearchToPostgres(dataDir string, workspaceID string, request PostgresSemanticSearchMaterializationRequest) (*PostgresSemanticMaterializationResult, error) {
	if strings.TrimSpace(request.Pattern) == "" {
		return nil, fmt.Errorf("%w: pattern is required", ErrInvalidSemanticRequest)
	}
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := firstConfiguredString(request.DSN, settings.PostgresDSN)
	if strings.TrimSpace(targetDSN) == "" {
		return nil, fmt.Errorf("%w: Postgres DSN is required for semantic materialization", ErrInvalidSemanticRequest)
	}
	targetSchema := strings.TrimSpace(firstConfiguredString(request.SchemaName, &settings.PostgresSchema))
	if targetSchema == "" {
		targetSchema = "xmustard"
	}
	normalizedSchema, err := validatePostgresSchemaName(targetSchema)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSemanticRequest, err)
	}
	searchResult, err := SearchSemanticPattern(
		dataDir,
		workspaceID,
		request.Pattern,
		firstNonEmptyPtr(request.Language),
		firstNonEmptyPtr(request.PathGlob),
		request.Limit,
	)
	if err != nil {
		return nil, err
	}
	if searchResult.QueryRow == nil {
		return nil, fmt.Errorf("%w: semantic search did not produce a storage-ready query row", ErrInvalidSemanticRequest)
	}
	result, err := applySemanticSearchMaterialization(
		targetDSN,
		normalizedSchema,
		snapshot.Workspace.WorkspaceID,
		snapshot.Workspace.Name,
		snapshot.Workspace.RootPath,
		searchResult.QueryRow,
		searchResult.MatchRows,
	)
	if err != nil {
		return nil, err
	}
	if err := appendWorkspaceSemanticActivity(
		dataDir,
		workspaceID,
		"postgres.materialize.semantic_search",
		"Materialized semantic search query for pattern "+request.Pattern,
		map[string]any{
			"pattern":     request.Pattern,
			"schema_name": result.SchemaName,
			"query_rows":  result.QueryRows,
			"match_rows":  result.MatchRows,
			"engine":      searchResult.Engine,
		},
	); err != nil {
		return nil, err
	}
	return result, nil
}

func applyPathSymbolMaterialization(
	dsn string,
	schema string,
	workspaceID string,
	workspaceName string,
	workspaceRoot string,
	relativePath string,
	fileSummaryRow *FileSymbolSummaryMaterializationRecord,
	symbolRows []SymbolMaterializationRecord,
) (*PostgresSemanticMaterializationResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	connection, err := connectSemanticPostgres(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect Postgres: %w", err)
	}
	defer connection.Close(context.Background())
	if err := upsertSemanticWorkspace(ctx, connection, schema, workspaceID, workspaceName, workspaceRoot); err != nil {
		return nil, err
	}
	fileID, err := upsertSemanticFile(ctx, connection, schema, workspaceID, workspaceRoot, relativePath, fileSummaryRow.Language, "source")
	if err != nil {
		return nil, err
	}
	if _, err := connection.Exec(ctx, fmt.Sprintf("delete from %s.symbols where workspace_id = $1 and path = $2", schema), workspaceID, relativePath); err != nil {
		return nil, fmt.Errorf("delete existing semantic symbols: %w", err)
	}
	insertedSymbols := 0
	for _, row := range symbolRows {
		if _, err := connection.Exec(
			ctx,
			fmt.Sprintf(
				"insert into %s.symbols (workspace_id, file_id, path, symbol, kind, language, line_start, line_end, enclosing_scope, signature_text, symbol_text) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)",
				schema,
			),
			row.WorkspaceID,
			fileID,
			row.Path,
			row.Symbol,
			row.Kind,
			row.Language,
			row.LineStart,
			row.LineEnd,
			row.EnclosingScope,
			row.SignatureText,
			row.SymbolText,
		); err != nil {
			return nil, fmt.Errorf("insert semantic symbol row: %w", err)
		}
		insertedSymbols++
	}
	summaryJSON, err := json.Marshal(fileSummaryRow.SummaryJSON)
	if err != nil {
		return nil, fmt.Errorf("encode file summary row: %w", err)
	}
	if _, err := connection.Exec(
		ctx,
		fmt.Sprintf(
			"insert into %s.file_symbol_summaries (workspace_id, file_id, path, language, parser_language, symbol_source, symbol_count, summary_json) values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb) on conflict (workspace_id, path) do update set file_id = excluded.file_id, language = excluded.language, parser_language = excluded.parser_language, symbol_source = excluded.symbol_source, symbol_count = excluded.symbol_count, summary_json = excluded.summary_json, indexed_at = now()",
			schema,
		),
		workspaceID,
		fileID,
		relativePath,
		fileSummaryRow.Language,
		fileSummaryRow.ParserLanguage,
		fileSummaryRow.SymbolSource,
		fileSummaryRow.SymbolCount,
		string(summaryJSON),
	); err != nil {
		return nil, fmt.Errorf("upsert file symbol summary row: %w", err)
	}
	return &PostgresSemanticMaterializationResult{
		Applied:           true,
		DSNRedacted:       redactPostgresDSN(dsn),
		SchemaName:        schema,
		WorkspaceID:       workspaceID,
		Source:            "path_symbols",
		Target:            relativePath,
		MaterializedPaths: []string{relativePath},
		FileRows:          1,
		SymbolRows:        insertedSymbols,
		SummaryRows:       1,
		Message:           fmt.Sprintf("Materialized parser-backed symbol rows for %s into Postgres schema '%s'.", relativePath, schema),
		GeneratedAt:       nowUTC(),
	}, nil
}

func applySemanticSearchMaterialization(
	dsn string,
	schema string,
	workspaceID string,
	workspaceName string,
	workspaceRoot string,
	queryRow *SemanticQueryMaterializationRecord,
	matchRows []SemanticMatchMaterializationRecord,
) (*PostgresSemanticMaterializationResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	connection, err := connectSemanticPostgres(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect Postgres: %w", err)
	}
	defer connection.Close(context.Background())
	if err := upsertSemanticWorkspace(ctx, connection, schema, workspaceID, workspaceName, workspaceRoot); err != nil {
		return nil, err
	}
	var queryID int64
	if err := connection.QueryRow(
		ctx,
		fmt.Sprintf(
			"insert into %s.semantic_queries (workspace_id, issue_id, run_id, source, reason, pattern, language, path_glob, engine, match_count, truncated, error) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) returning query_id",
			schema,
		),
		queryRow.WorkspaceID,
		queryRow.IssueID,
		queryRow.RunID,
		queryRow.Source,
		queryRow.Reason,
		queryRow.Pattern,
		queryRow.Language,
		queryRow.PathGlob,
		queryRow.Engine,
		queryRow.MatchCount,
		queryRow.Truncated,
		queryRow.Error,
	).Scan(&queryID); err != nil {
		return nil, fmt.Errorf("insert semantic query row: %w", err)
	}
	materializedPaths := uniqueSemanticPaths(matchRows)
	fileIDs := map[string]*int64{}
	for _, path := range materializedPaths {
		fileLanguage := firstSemanticMatchLanguage(matchRows, path)
		fileID, upsertErr := upsertSemanticFile(ctx, connection, schema, workspaceID, workspaceRoot, path, fileLanguage, "source")
		if upsertErr != nil {
			return nil, upsertErr
		}
		fileIDs[path] = fileID
	}
	insertedMatches := 0
	for _, row := range matchRows {
		metaVariables, marshalErr := json.Marshal(row.MetaVariables)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode semantic match variables: %w", marshalErr)
		}
		if _, execErr := connection.Exec(
			ctx,
			fmt.Sprintf(
				"insert into %s.semantic_matches (workspace_id, query_id, file_id, symbol_id, path, language, line_start, line_end, column_start, column_end, matched_text, context_lines, meta_variables_json, reason, score) values ($1, $2, $3, null, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13, $14)",
				schema,
			),
			row.WorkspaceID,
			queryID,
			fileIDs[row.Path],
			row.Path,
			row.Language,
			row.LineStart,
			row.LineEnd,
			row.ColumnStart,
			row.ColumnEnd,
			row.MatchedText,
			row.ContextLines,
			string(metaVariables),
			row.Reason,
			row.Score,
		); execErr != nil {
			return nil, fmt.Errorf("insert semantic match row: %w", execErr)
		}
		insertedMatches++
	}
	return &PostgresSemanticMaterializationResult{
		Applied:           true,
		DSNRedacted:       redactPostgresDSN(dsn),
		SchemaName:        schema,
		WorkspaceID:       workspaceID,
		Source:            "semantic_search",
		Target:            queryRow.Pattern,
		MaterializedPaths: materializedPaths,
		FileRows:          len(materializedPaths),
		QueryRows:         1,
		MatchRows:         insertedMatches,
		Message:           fmt.Sprintf("Materialized semantic query '%s' and %d match rows into Postgres schema '%s'.", queryRow.Pattern, insertedMatches, schema),
		GeneratedAt:       nowUTC(),
	}, nil
}

func upsertSemanticWorkspace(ctx context.Context, connection semanticMaterializationConn, schema string, workspaceID string, workspaceName string, workspaceRoot string) error {
	if _, err := connection.Exec(
		ctx,
		fmt.Sprintf(
			"insert into %s.workspaces (workspace_id, name, root_path, latest_scan_at) values ($1, $2, $3, now()) on conflict (workspace_id) do update set name = excluded.name, root_path = excluded.root_path, latest_scan_at = now(), updated_at = now()",
			schema,
		),
		workspaceID,
		workspaceName,
		workspaceRoot,
	); err != nil {
		return fmt.Errorf("upsert semantic workspace: %w", err)
	}
	return nil
}

func upsertSemanticFile(ctx context.Context, connection semanticMaterializationConn, schema string, workspaceID string, workspaceRoot string, relativePath string, language *string, role string) (*int64, error) {
	sizeBytes, contentHash, err := semanticFileMetadata(workspaceRoot, relativePath)
	if err != nil {
		return nil, err
	}
	var fileID int64
	if err := connection.QueryRow(
		ctx,
		fmt.Sprintf(
			"insert into %s.files (workspace_id, path, role, language, size_bytes, content_hash) values ($1, $2, $3, $4, $5, $6) on conflict (workspace_id, path) do update set role = excluded.role, language = coalesce(excluded.language, files.language), size_bytes = excluded.size_bytes, content_hash = excluded.content_hash, last_indexed_at = now() returning file_id",
			schema,
		),
		workspaceID,
		relativePath,
		role,
		language,
		sizeBytes,
		contentHash,
	).Scan(&fileID); err != nil {
		return nil, fmt.Errorf("upsert semantic file row: %w", err)
	}
	return &fileID, nil
}

func semanticFileMetadata(workspaceRoot string, relativePath string) (*int64, *string, error) {
	path := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read semantic file metadata: %w", err)
	}
	if info.IsDir() {
		return nil, nil, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read semantic file content: %w", err)
	}
	size := int64(len(content))
	sum := sha1.Sum(content)
	hash := hex.EncodeToString(sum[:])
	return &size, &hash, nil
}

func appendWorkspaceSemanticActivity(dataDir string, workspaceID string, action string, summary string, details map[string]any) error {
	createdAt := nowUTC()
	record := activityRecord{
		ActivityID:  hashID(workspaceID, "workspace", workspaceID, action, createdAt),
		WorkspaceID: workspaceID,
		EntityType:  "workspace",
		EntityID:    workspaceID,
		Action:      action,
		Summary:     summary,
		Actor:       systemActor(),
		Details:     details,
		CreatedAt:   createdAt,
	}
	path := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer handle.Close()
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := handle.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}

func semanticMaterializationPaths(dataDir string, workspaceID string, repoRoot string, strategy string, paths []string, limit int) ([]string, error) {
	normalizedLimit := max(1, min(limit, 100))
	switch semanticStrategyOrDefault(strategy) {
	case "paths":
		return dedupeOrderedSemanticPaths(paths, normalizedLimit), nil
	case "key_files":
		repoMap, err := loadOrBuildRepoMap(dataDir, workspaceID, repoRoot)
		if err != nil {
			return nil, err
		}
		prioritized := []string{}
		for _, item := range repoMap.KeyFiles {
			if item.Role == "test" {
				continue
			}
			if semanticMaterializationIsIndexableSource(item.Path) {
				prioritized = append(prioritized, item.Path)
			}
		}
		prioritized = append(prioritized, semanticMaterializationSourceCandidates(repoRoot, max(normalizedLimit*6, 40))...)
		ranked := dedupeOrderedSemanticPaths(prioritized, 500)
		sort.SliceStable(ranked, func(i, j int) bool {
			leftScore := semanticMaterializationPathScore(ranked[i])
			rightScore := semanticMaterializationPathScore(ranked[j])
			if leftScore != rightScore {
				return leftScore > rightScore
			}
			return ranked[i] < ranked[j]
		})
		selected := []string{}
		for _, relativePath := range ranked {
			if !semanticMaterializationIsIndexableSource(relativePath) {
				continue
			}
			info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(relativePath)))
			if err != nil || info.IsDir() {
				continue
			}
			selected = append(selected, relativePath)
			if len(selected) >= normalizedLimit {
				break
			}
		}
		return selected, nil
	default:
		return nil, fmt.Errorf("%w: unknown semantic materialization strategy: %s", ErrInvalidSemanticRequest, strategy)
	}
}

func semanticStrategyOrDefault(strategy string) string {
	if strings.TrimSpace(strategy) == "paths" {
		return "paths"
	}
	return "key_files"
}

func dedupeOrderedSemanticPaths(paths []string, limit int) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range paths {
		trimmed := strings.TrimPrefix(strings.TrimSpace(value), "./")
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, filepath.ToSlash(trimmed))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func semanticMaterializationSourceCandidates(repoRoot string, limit int) []string {
	candidates := []string{}
	_ = filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			name := strings.ToLower(entry.Name())
			switch name {
			case ".git", "node_modules", "dist", "build", "target", "vendor", "coverage", ".venv", "venv", "__pycache__", "research":
				return filepath.SkipDir
			}
			return nil
		}
		relative, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}
		normalized := filepath.ToSlash(relative)
		role := inferGoFileRole(normalized)
		if (role == "source" || role == "config" || role == "guide" || role == "entry") && semanticMaterializationIsIndexableSource(normalized) {
			candidates = append(candidates, normalized)
		}
		if len(candidates) >= limit {
			return fs.SkipAll
		}
		return nil
	})
	return candidates
}

func semanticMaterializationIsIndexableSource(relativePath string) bool {
	switch strings.ToLower(filepath.Ext(relativePath)) {
	case ".py", ".ts", ".tsx", ".js", ".jsx", ".go", ".rs", ".java", ".kt", ".swift", ".c", ".cc", ".cpp", ".h", ".hpp", ".cs", ".rb", ".php":
		return true
	default:
		return false
	}
}

func semanticMaterializationPathScore(relativePath string) int {
	lowered := strings.ToLower(relativePath)
	name := filepath.Base(lowered)
	score := 0
	switch inferGoFileRole(relativePath) {
	case "entry":
		score += 80
	case "source":
		score += 60
	case "config":
		score += 30
	case "guide":
		score += 20
	case "test":
		score -= 80
	}
	if strings.HasPrefix(lowered, "backend/") || strings.HasPrefix(lowered, "api/") || strings.HasPrefix(lowered, "cmd/") || strings.HasPrefix(lowered, "cli/") || strings.HasPrefix(lowered, "scripts/") || strings.HasPrefix(lowered, "src/") {
		score += 35
	}
	if strings.Contains(lowered, "cli.py") || strings.Contains(lowered, "__main__.py") || strings.Contains(lowered, "/main.py") || strings.Contains(lowered, "/service.py") || strings.Contains(lowered, "/commands/") || strings.Contains(lowered, "/command/") || strings.Contains(lowered, "/bin/") {
		score += 25
	}
	if strings.Contains(lowered, "cli") || strings.Contains(lowered, "terminal") || strings.Contains(lowered, "runtime") || strings.Contains(lowered, "server") || strings.Contains(lowered, "agent") || strings.Contains(lowered, "command") {
		score += 12
	}
	if name == "pyproject.toml" || name == "package.json" || name == "cargo.toml" || name == "makefile" || name == "justfile" || name == "readme.md" || name == "agents.md" {
		score += 18
	}
	if strings.Contains("/"+lowered+"/", "/tests/") || strings.HasPrefix(name, "test_") || strings.Contains(name, ".test.") || strings.Contains(name, ".spec.") {
		score -= 60
	}
	if strings.HasSuffix(lowered, ".md") || strings.HasSuffix(lowered, ".json") || strings.HasSuffix(lowered, ".toml") || strings.HasSuffix(lowered, ".yaml") || strings.HasSuffix(lowered, ".yml") {
		score += 5
	}
	return score
}

func runAstGrepSemanticQuery(repoRoot string, pattern string, language *string, pathGlob *string, limit int) ([]SemanticPatternMatchRecord, *string, *string, bool) {
	binary := astGrepBinary()
	if binary == "" {
		return []SemanticPatternMatchRecord{}, nil, optionalString("ast-grep binary is not installed on this machine."), false
	}
	args := []string{"run", "--pattern", pattern, "--json=stream"}
	if language != nil {
		args = append(args, "--lang", *language)
	}
	if pathGlob != nil {
		args = append(args, "--globs", *pathGlob)
	}
	args = append(args, repoRoot)
	cmd := exec.Command(binary, args...)
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			matches, truncated := parseAstGrepMatches(repoRoot, stdout.String(), limit)
			return matches, optionalString(binary), nil, truncated
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			message = err.Error()
		}
		return []SemanticPatternMatchRecord{}, optionalString(binary), &message, false
	}
	matches, truncated := parseAstGrepMatches(repoRoot, stdout.String(), limit)
	return matches, optionalString(binary), nil, truncated
}

func parseAstGrepMatches(repoRoot string, output string, limit int) ([]SemanticPatternMatchRecord, bool) {
	matches := []SemanticPatternMatchRecord{}
	truncated := false
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			continue
		}
		filePath, ok := payload["file"].(string)
		if !ok || strings.TrimSpace(filePath) == "" {
			continue
		}
		matchedText, ok := payload["text"].(string)
		if !ok {
			continue
		}
		rangePayload, _ := payload["range"].(map[string]any)
		startPayload, _ := rangePayload["start"].(map[string]any)
		endPayload, _ := rangePayload["end"].(map[string]any)
		var contextLines *string
		if value, ok := payload["lines"].(string); ok && strings.TrimSpace(value) != "" {
			contextLines = &value
		}
		metaVariables := []string{}
		if metaPayload, ok := payload["metaVariables"].(map[string]any); ok {
			if singlePayload, ok := metaPayload["single"].(map[string]any); ok {
				for key := range singlePayload {
					metaVariables = append(metaVariables, key)
				}
				sort.Strings(metaVariables)
			}
		}
		matches = append(matches, SemanticPatternMatchRecord{
			Path:          relativeSemanticMatchPath(repoRoot, filePath),
			Language:      normalizeAstGrepLanguage(payload["language"]),
			LineStart:     oneBasedSemanticIndex(startPayload, "line"),
			LineEnd:       oneBasedSemanticIndex(endPayload, "line"),
			ColumnStart:   oneBasedSemanticIndex(startPayload, "column"),
			ColumnEnd:     oneBasedSemanticIndex(endPayload, "column"),
			MatchedText:   matchedText,
			ContextLines:  contextLines,
			MetaVariables: metaVariables,
			Score:         0,
		})
		if len(matches) >= limit {
			truncated = true
			break
		}
	}
	return matches, truncated
}

func relativeSemanticMatchPath(repoRoot string, filePath string) string {
	target := filePath
	if filepath.IsAbs(filePath) {
		if relative, err := filepath.Rel(repoRoot, filePath); err == nil {
			target = relative
		}
	}
	return filepath.ToSlash(strings.TrimPrefix(target, "./"))
}

func oneBasedSemanticIndex(payload map[string]any, key string) *int {
	value, ok := payload[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case float64:
		result := int(typed) + 1
		return &result
	case int:
		result := typed + 1
		return &result
	default:
		return nil
	}
}

func normalizeAstGrepLanguage(value any) *string {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return nil
	}
	return &text
}

func astGrepBinary() string {
	if binary, err := exec.LookPath("sg"); err == nil {
		return binary
	}
	if binary, err := exec.LookPath("ast-grep"); err == nil {
		return binary
	}
	return ""
}

func buildSemanticQueryRow(
	workspaceID string,
	pattern string,
	language *string,
	pathGlob *string,
	engine string,
	matchCount int,
	truncated bool,
	queryError *string,
	source string,
	reason *string,
	issueID *string,
	runID *string,
) *SemanticQueryMaterializationRecord {
	normalizedEngine := "none"
	if engine == "ast_grep" {
		normalizedEngine = "ast_grep"
	}
	normalizedSource := "adhoc_tool"
	if source == "issue_context" {
		normalizedSource = "issue_context"
	}
	return &SemanticQueryMaterializationRecord{
		QueryRef:    semanticQueryRef(workspaceID, normalizedSource, pattern, firstNonEmptyPtr(language), firstNonEmptyPtr(pathGlob), firstNonEmptyPtr(issueID), firstNonEmptyPtr(runID), firstNonEmptyPtr(reason)),
		WorkspaceID: workspaceID,
		IssueID:     issueID,
		RunID:       runID,
		Source:      normalizedSource,
		Reason:      reason,
		Pattern:     pattern,
		Language:    language,
		PathGlob:    pathGlob,
		Engine:      normalizedEngine,
		MatchCount:  matchCount,
		Truncated:   truncated,
		Error:       queryError,
	}
}

func semanticQueryRef(workspaceID string, source string, pattern string, language string, pathGlob string, issueID string, runID string, reason string) string {
	digest := sha1.Sum([]byte(strings.Join([]string{
		workspaceID,
		source,
		issueID,
		runID,
		language,
		pathGlob,
		pattern,
		reason,
	}, "|")))
	return "semanticq_" + hex.EncodeToString(digest[:])[:16]
}

func buildSemanticMatchRows(workspaceID string, queryRef string, matches []SemanticPatternMatchRecord) []SemanticMatchMaterializationRecord {
	out := make([]SemanticMatchMaterializationRecord, 0, len(matches))
	for _, item := range matches {
		out = append(out, SemanticMatchMaterializationRecord{
			QueryRef:      queryRef,
			WorkspaceID:   workspaceID,
			Path:          item.Path,
			Language:      item.Language,
			LineStart:     item.LineStart,
			LineEnd:       item.LineEnd,
			ColumnStart:   item.ColumnStart,
			ColumnEnd:     item.ColumnEnd,
			MatchedText:   item.MatchedText,
			ContextLines:  item.ContextLines,
			MetaVariables: item.MetaVariables,
			Reason:        item.Reason,
			Score:         item.Score,
		})
	}
	return out
}

func uniqueSemanticPaths(matchRows []SemanticMatchMaterializationRecord) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, row := range matchRows {
		if strings.TrimSpace(row.Path) == "" {
			continue
		}
		if _, exists := seen[row.Path]; exists {
			continue
		}
		seen[row.Path] = struct{}{}
		out = append(out, row.Path)
	}
	sort.Strings(out)
	return out
}

func firstSemanticMatchLanguage(matchRows []SemanticMatchMaterializationRecord, path string) *string {
	for _, row := range matchRows {
		if row.Path == path && row.Language != nil {
			return row.Language
		}
	}
	return nil
}

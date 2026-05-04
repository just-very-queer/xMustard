package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xmustard/api-go/internal/rustcore"
)

type MaterializedSymbolRecord struct {
	SymbolID       int64   `json:"symbol_id"`
	Path           string  `json:"path"`
	Symbol         string  `json:"symbol"`
	Kind           string  `json:"kind"`
	Language       *string `json:"language,omitempty"`
	LineStart      *int    `json:"line_start,omitempty"`
	LineEnd        *int    `json:"line_end,omitempty"`
	EnclosingScope *string `json:"enclosing_scope,omitempty"`
	SignatureText  *string `json:"signature_text,omitempty"`
	SymbolText     *string `json:"symbol_text,omitempty"`
	SymbolSource   *string `json:"symbol_source,omitempty"`
	ParserLanguage *string `json:"parser_language,omitempty"`
}

type WorkspaceSymbolsResult struct {
	WorkspaceID string                     `json:"workspace_id"`
	Query       string                     `json:"query"`
	Limit       int                        `json:"limit"`
	Symbols     []MaterializedSymbolRecord `json:"symbols"`
	Warnings    []string                   `json:"warnings"`
	GeneratedAt string                     `json:"generated_at"`
}

type DiagnosticLinkedSymbol struct {
	SymbolID       int64   `json:"symbol_id"`
	Path           string  `json:"path"`
	Symbol         string  `json:"symbol"`
	Kind           string  `json:"kind"`
	Language       *string `json:"language,omitempty"`
	LineStart      *int    `json:"line_start,omitempty"`
	LineEnd        *int    `json:"line_end,omitempty"`
	EnclosingScope *string `json:"enclosing_scope,omitempty"`
	SignatureText  *string `json:"signature_text,omitempty"`
	LinkStrategy   string  `json:"link_strategy"`
}

func ReadWorkspaceSymbols(dataDir string, workspaceID string, query string, limit int) (*WorkspaceSymbolsResult, error) {
	if _, err := getWorkspaceRecord(dataDir, workspaceID); err != nil {
		return nil, err
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := strings.TrimSpace(firstConfiguredString(nil, settings.PostgresDSN))
	if targetDSN == "" {
		return nil, fmt.Errorf("%w: Postgres DSN is required to read workspace symbols", ErrInvalidSemanticRequest)
	}
	schema := strings.TrimSpace(settings.PostgresSchema)
	if schema == "" {
		schema = "xmustard"
	}
	rows, err := readWorkspaceSymbolRows(targetDSN, schema, workspaceID, query, limit)
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	if len(rows) == 0 {
		warnings = append(warnings, "No materialized workspace symbols matched this query.")
	}
	return &WorkspaceSymbolsResult{
		WorkspaceID: workspaceID,
		Query:       strings.TrimSpace(query),
		Limit:       normalizeWorkspaceSymbolLimit(limit),
		Symbols:     rows,
		Warnings:    warnings,
		GeneratedAt: nowUTC(),
	}, nil
}

func ReadDocumentSymbols(dataDir string, workspaceID string, relativePath string) (*PathSymbolsResult, error) {
	result, err := LSPDocumentSymbols(dataDir, workspaceID, relativePath)
	if err != nil {
		return nil, err
	}
	return &PathSymbolsResult{
		WorkspaceID:     result.WorkspaceID,
		Path:            result.Path,
		SymbolSource:    result.SymbolSource,
		ParserLanguage:  result.ParserLanguage,
		EvidenceSource:  result.EvidenceSource,
		SelectionReason: result.SelectionReason,
		Symbols:         convertRustDocumentSymbols(result.Symbols),
		Warnings:        result.Warnings,
		GeneratedAt:     result.GeneratedAt,
	}, nil
}

func readWorkspaceSymbolRows(dsn string, schema string, workspaceID string, query string, limit int) ([]MaterializedSymbolRecord, error) {
	normalizedSchema, err := validatePostgresSchemaName(schema)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := connectSemanticPostgres(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect Postgres: %w", err)
	}
	defer connection.Close(context.Background())
	normalizedQuery := strings.ToLower(strings.TrimSpace(query))
	pattern := "%"
	if normalizedQuery != "" {
		pattern = "%" + normalizedQuery + "%"
	}
	var payload []byte
	err = connection.QueryRow(
		ctx,
		fmt.Sprintf(`
			select coalesce(jsonb_agg(item order by match_rank, path, line_start, symbol), '[]'::jsonb)
			from (
				select jsonb_build_object(
					'symbol_id', s.symbol_id,
					'path', s.path,
					'symbol', s.symbol,
					'kind', s.kind,
					'language', s.language,
					'line_start', s.line_start,
					'line_end', s.line_end,
					'enclosing_scope', s.enclosing_scope,
					'signature_text', s.signature_text,
					'symbol_text', s.symbol_text,
					'symbol_source', fss.symbol_source,
					'parser_language', fss.parser_language
				) as item,
				case
					when $2 = '' then 4
					when lower(s.symbol) = $2 then 0
					when lower(s.symbol) like ($2 || '%%') then 1
					when lower(s.symbol) like $3 then 2
					else 3
				end as match_rank,
				s.path,
				coalesce(s.line_start, 2147483647) as line_start,
				s.symbol
				from %s.symbols s
				left join %s.file_symbol_summaries fss on fss.workspace_id = s.workspace_id and fss.path = s.path
				where s.workspace_id = $1
				  and ($2 = '' or lower(s.symbol) like $3 or lower(s.path) like $3 or lower(coalesce(s.enclosing_scope, '')) like $3)
				order by match_rank, s.path, coalesce(s.line_start, 2147483647), s.symbol
				limit $4
			) rows
		`, normalizedSchema, normalizedSchema),
		workspaceID,
		normalizedQuery,
		pattern,
		normalizeWorkspaceSymbolLimit(limit),
	).Scan(&payload)
	if err != nil {
		return nil, fmt.Errorf("read workspace symbols: %w", err)
	}
	rows := []MaterializedSymbolRecord{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &rows); err != nil {
			return nil, fmt.Errorf("decode workspace symbols: %w", err)
		}
	}
	return rows, nil
}

func findBestDiagnosticSymbolLink(ctx context.Context, connection semanticMaterializationConn, schema string, workspaceID string, relativePath string, startLine int, endLine int) (*DiagnosticLinkedSymbol, error) {
	_ = endLine
	var payload []byte
	err := connection.QueryRow(
		ctx,
		fmt.Sprintf(`
			select coalesce((
				select case
					when jsonb_array_length(matches) = 1
						then jsonb_set(matches->0, '{link_strategy}', '"diagnostic_start_line_exact_symbol_anchor"'::jsonb, true)
					else 'null'::jsonb
				end
				from (
					select coalesce(jsonb_agg(jsonb_build_object(
						'symbol_id', s.symbol_id,
						'path', s.path,
						'symbol', s.symbol,
						'kind', s.kind,
						'language', s.language,
						'line_start', s.line_start,
						'line_end', s.line_end,
						'enclosing_scope', s.enclosing_scope,
						'signature_text', s.signature_text
					) order by s.symbol), '[]'::jsonb) as matches
					from (
						select s.symbol_id, s.path, s.symbol, s.kind, s.language, s.line_start, s.line_end, s.enclosing_scope, s.signature_text
						from %s.symbols s
						where s.workspace_id = $1
						  and s.path = $2
						  and s.line_start is not null
						  and s.line_start = $3
						order by s.symbol asc
						limit 2
					) s
				) candidates
			), 'null'::jsonb)
		`, schema),
		workspaceID,
		relativePath,
		startLine,
	).Scan(&payload)
	if err != nil {
		return nil, fmt.Errorf("read diagnostic symbol link: %w", err)
	}
	if strings.TrimSpace(string(payload)) == "null" || len(payload) == 0 {
		return nil, nil
	}
	var link DiagnosticLinkedSymbol
	if err := json.Unmarshal(payload, &link); err != nil {
		return nil, fmt.Errorf("decode diagnostic symbol link: %w", err)
	}
	return &link, nil
}

func normalizeWorkspaceSymbolLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func convertRustDocumentSymbols(items []rustcore.DocumentSymbolRecord) []PathSymbolRecord {
	out := make([]PathSymbolRecord, 0, len(items))
	for _, item := range items {
		out = append(out, PathSymbolRecord{
			Path:           item.Path,
			Symbol:         item.Symbol,
			Kind:           item.Kind,
			LineStart:      item.LineStart,
			LineEnd:        item.LineEnd,
			EnclosingScope: item.EnclosingScope,
			EvidenceSource: item.EvidenceSource,
			Reason:         item.Reason,
			Score:          item.Score,
		})
	}
	return out
}

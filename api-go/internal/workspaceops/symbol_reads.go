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

func findBestDiagnosticSymbolLink(ctx context.Context, connection semanticMaterializationConn, schema string, workspaceID string, relativePath string, startLine int, endLine int, diagnosticFingerprint string) (*DiagnosticLinkedSymbol, error) {
	var payload []byte
	err := connection.QueryRow(
		ctx,
		fmt.Sprintf(`
			select coalesce(jsonb_agg(jsonb_build_object(
				'symbol_id', candidate.symbol_id,
				'path', candidate.path,
				'symbol', candidate.symbol,
				'kind', candidate.kind,
				'language', candidate.language,
				'line_start', candidate.line_start,
				'line_end', candidate.line_end,
				'enclosing_scope', candidate.enclosing_scope,
				'signature_text', candidate.signature_text
			) order by coalesce(candidate.line_start, 2147483647), coalesce(candidate.line_end, 2147483647), candidate.symbol), '[]'::jsonb)
			from (
				select s.symbol_id, s.path, s.symbol, s.kind, s.language, s.line_start, s.line_end, s.enclosing_scope, s.signature_text
				from %s.symbols s
				where s.workspace_id = $1
				  and s.path = $2
				  and s.line_start is not null
				  and (
					s.line_start = $3
					or (s.line_end is not null and s.line_start <= $3 and s.line_end >= $4)
				  )
				order by coalesce(s.line_start, 2147483647), coalesce(s.line_end, 2147483647), s.symbol
				limit 50
			) candidate
		`, schema),
		workspaceID,
		relativePath,
		startLine,
		endLine,
	).Scan(&payload)
	if err != nil {
		return nil, fmt.Errorf("read diagnostic symbol candidates: %w", err)
	}
	var candidates []rustcore.DiagnosticSymbolCandidate
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &candidates); err != nil {
			return nil, fmt.Errorf("decode diagnostic symbol candidates: %w", err)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	linkResult, err := rustcore.LinkDiagnosticSymbol(ctx, workspaceID, relativePath, startLine, endLine, diagnosticFingerprint, candidates)
	if err != nil {
		return nil, err
	}
	if linkResult.LinkedSymbol == nil {
		return nil, nil
	}
	return convertRustDiagnosticLinkedSymbol(linkResult.LinkedSymbol), nil
}

func hasMaterializedSymbolSummary(ctx context.Context, connection semanticMaterializationConn, schema string, workspaceID string, relativePath string) (bool, error) {
	var ready bool
	err := connection.QueryRow(
		ctx,
		fmt.Sprintf("select exists(select 1 from %s.file_symbol_summaries where workspace_id = $1 and path = $2)", schema),
		workspaceID,
		relativePath,
	).Scan(&ready)
	if err != nil {
		return false, fmt.Errorf("read diagnostic symbol readiness: %w", err)
	}
	return ready, nil
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

func convertRustDiagnosticLinkedSymbol(item *rustcore.DiagnosticLinkedSymbol) *DiagnosticLinkedSymbol {
	if item == nil {
		return nil
	}
	return &DiagnosticLinkedSymbol{
		SymbolID:        item.SymbolID,
		Path:            item.Path,
		Symbol:          item.Symbol,
		Kind:            item.Kind,
		Language:        item.Language,
		LineStart:       item.LineStart,
		LineEnd:         item.LineEnd,
		EnclosingScope:  item.EnclosingScope,
		SignatureText:   item.SignatureText,
		LinkStrategy:    item.LinkStrategy,
		EvidenceSource:  item.EvidenceSource,
		SelectionReason: item.SelectionReason,
	}
}

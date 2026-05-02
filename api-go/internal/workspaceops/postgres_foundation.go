package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	errInvalidPostgresRequest = errors.New("invalid postgres request")
	postgresSchemaNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	postgresTableNamePattern  = regexp.MustCompile(`(?i)create table if not exists\s+(?:[a-zA-Z_][a-zA-Z0-9_]*\.)?([a-zA-Z_][a-zA-Z0-9_]*)`)
	postgresSearchDocPattern  = regexp.MustCompile(`(?i)alter table\s+(?:[a-zA-Z_][a-zA-Z0-9_]*\.)?([a-zA-Z_][a-zA-Z0-9_]*)\s+add column if not exists\s+search_document\b`)
)

var semanticTableNames = map[string]struct{}{
	"files":                 {},
	"symbols":               {},
	"symbol_edges":          {},
	"file_symbol_summaries": {},
	"semantic_queries":      {},
	"semantic_matches":      {},
	"semantic_index_runs":   {},
	"diagnostics":           {},
}

var opsMemoryTableNames = map[string]struct{}{
	"activity_events":       {},
	"run_records":           {},
	"run_plans":             {},
	"run_plan_revisions":    {},
	"verification_profiles": {},
	"verification_runs":     {},
	"issue_artifacts":       {},
}

type PostgresSchemaPlan struct {
	Configured           bool     `json:"configured"`
	DSNRedacted          *string  `json:"dsn_redacted,omitempty"`
	SchemaName           string   `json:"schema_name"`
	SQLPath              string   `json:"sql_path"`
	StatementCount       int      `json:"statement_count"`
	TableNames           []string `json:"table_names"`
	SemanticTableNames   []string `json:"semantic_table_names"`
	OpsMemoryTableNames  []string `json:"ops_memory_table_names"`
	SearchDocumentTables []string `json:"search_document_tables"`
	GeneratedAt          string   `json:"generated_at"`
}

type PostgresBootstrapRequest struct {
	DSN        *string `json:"dsn,omitempty"`
	SchemaName *string `json:"schema_name,omitempty"`
}

type PostgresBootstrapResult struct {
	Applied              bool     `json:"applied"`
	DSNRedacted          *string  `json:"dsn_redacted,omitempty"`
	SchemaName           string   `json:"schema_name"`
	SQLPath              string   `json:"sql_path"`
	StatementCount       int      `json:"statement_count"`
	TableNames           []string `json:"table_names"`
	SemanticTableNames   []string `json:"semantic_table_names"`
	SearchDocumentTables []string `json:"search_document_tables"`
	Message              string   `json:"message"`
	GeneratedAt          string   `json:"generated_at"`
}

type postgresBootstrapConn interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Close(context.Context) error
}

var connectPostgresFoundation = func(ctx context.Context, dsn string) (postgresBootstrapConn, error) {
	return pgx.Connect(ctx, dsn)
}

func GetPostgresSchemaPlan(dataDir string) (*PostgresSchemaPlan, error) {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	return buildPostgresSchemaPlan(dataDir, settings.PostgresDSN, settings.PostgresSchema)
}

func RenderPostgresSchemaSQL(dataDir string, schema string) (string, error) {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return "", err
	}
	targetSchema := strings.TrimSpace(schema)
	if targetSchema == "" {
		targetSchema = settings.PostgresSchema
	}
	rendered, _, err := renderPostgresSchemaSQL(dataDir, targetSchema)
	return rendered, err
}

func BootstrapPostgresSchema(dataDir string, request PostgresBootstrapRequest) (*PostgresBootstrapResult, error) {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := firstConfiguredString(request.DSN, settings.PostgresDSN)
	if strings.TrimSpace(targetDSN) == "" {
		return nil, fmt.Errorf("%w: Postgres DSN is required for bootstrap", errInvalidPostgresRequest)
	}
	targetSchema := strings.TrimSpace(firstConfiguredString(request.SchemaName, &settings.PostgresSchema))
	if targetSchema == "" {
		targetSchema = "xmustard"
	}
	normalizedSchema, err := validatePostgresSchemaName(targetSchema)
	if err != nil {
		return nil, err
	}
	renderedSQL, sqlPath, err := renderPostgresSchemaSQL(dataDir, normalizedSchema)
	if err != nil {
		return nil, err
	}
	statements := splitPostgresStatements(renderedSQL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	connection, err := connectPostgresFoundation(ctx, targetDSN)
	if err != nil {
		return nil, fmt.Errorf("connect Postgres: %w", err)
	}
	defer connection.Close(context.Background())
	for _, statement := range statements {
		if _, err := connection.Exec(ctx, statement); err != nil {
			return nil, fmt.Errorf("apply Postgres bootstrap statement: %w", err)
		}
	}
	tableNames := extractPostgresTableNames(renderedSQL)
	result := &PostgresBootstrapResult{
		Applied:              true,
		DSNRedacted:          redactPostgresDSN(targetDSN),
		SchemaName:           normalizedSchema,
		SQLPath:              sqlPath,
		StatementCount:       len(statements),
		TableNames:           tableNames,
		SemanticTableNames:   filterNamedTables(tableNames, semanticTableNames),
		SearchDocumentTables: extractPostgresSearchDocumentTables(renderedSQL),
		Message:              fmt.Sprintf("Applied repo cockpit foundation schema to Postgres schema '%s'.", normalizedSchema),
		GeneratedAt:          nowUTC(),
	}
	if err := appendSettingsActivity(
		dataDir,
		"global",
		"postgres",
		"postgres.bootstrap",
		"Applied Postgres schema bootstrap for schema "+result.SchemaName,
		map[string]any{
			"schema_name":     result.SchemaName,
			"statement_count": result.StatementCount,
			"dsn_redacted":    firstNonEmptyPtr(result.DSNRedacted),
		},
	); err != nil {
		return nil, err
	}
	return result, nil
}

func buildPostgresSchemaPlan(dataDir string, dsn *string, schema string) (*PostgresSchemaPlan, error) {
	targetSchema := strings.TrimSpace(schema)
	if targetSchema == "" {
		targetSchema = "xmustard"
	}
	normalizedSchema, err := validatePostgresSchemaName(targetSchema)
	if err != nil {
		return nil, err
	}
	renderedSQL, sqlPath, err := renderPostgresSchemaSQL(dataDir, normalizedSchema)
	if err != nil {
		return nil, err
	}
	tableNames := extractPostgresTableNames(renderedSQL)
	return &PostgresSchemaPlan{
		Configured:           strings.TrimSpace(firstNonEmptyPtr(dsn)) != "",
		DSNRedacted:          redactPostgresDSN(firstNonEmptyPtr(dsn)),
		SchemaName:           normalizedSchema,
		SQLPath:              sqlPath,
		StatementCount:       len(splitPostgresStatements(renderedSQL)),
		TableNames:           tableNames,
		SemanticTableNames:   filterNamedTables(tableNames, semanticTableNames),
		OpsMemoryTableNames:  filterNamedTables(tableNames, opsMemoryTableNames),
		SearchDocumentTables: extractPostgresSearchDocumentTables(renderedSQL),
		GeneratedAt:          nowUTC(),
	}, nil
}

func renderPostgresSchemaSQL(dataDir string, schema string) (string, string, error) {
	normalizedSchema, err := validatePostgresSchemaName(schema)
	if err != nil {
		return "", "", err
	}
	sqlPath := filepath.Join(filepath.Dir(dataDir), "sql", "001_repo_cockpit_postgres.sql")
	template, err := os.ReadFile(sqlPath)
	if err != nil {
		return "", "", err
	}
	rendered := strings.ReplaceAll(string(template), "{{schema}}", normalizedSchema)
	return rendered, sqlPath, nil
}

func validatePostgresSchemaName(schema string) (string, error) {
	normalized := strings.TrimSpace(schema)
	if normalized == "" {
		return "", fmt.Errorf("%w: Postgres schema must not be empty", errInvalidPostgresRequest)
	}
	if !postgresSchemaNamePattern.MatchString(normalized) {
		return "", fmt.Errorf("%w: Postgres schema must start with a letter or underscore and contain only letters, digits, or underscores", errInvalidPostgresRequest)
	}
	return normalized, nil
}

func redactPostgresDSN(dsn string) *string {
	normalized := strings.TrimSpace(dsn)
	if normalized == "" {
		return nil
	}
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return optionalString("<configured>")
	}
	username := "user"
	if parsed.User != nil && parsed.User.Username() != "" {
		username = parsed.User.Username()
	}
	parsed.User = url.UserPassword(username, "***")
	redacted := parsed.String()
	return optionalString(redacted)
}

func splitPostgresStatements(sqlText string) []string {
	chunks := strings.Split(sqlText, ";\n")
	statements := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		statement := strings.TrimSpace(chunk)
		if statement == "" {
			continue
		}
		statements = append(statements, statement+";")
	}
	return statements
}

func extractPostgresTableNames(sqlText string) []string {
	return uniqueSortedMatches(postgresTableNamePattern, sqlText)
}

func extractPostgresSearchDocumentTables(sqlText string) []string {
	return uniqueSortedMatches(postgresSearchDocPattern, sqlText)
}

func uniqueSortedMatches(pattern *regexp.Regexp, sqlText string) []string {
	seen := map[string]struct{}{}
	for _, match := range pattern.FindAllStringSubmatch(sqlText, -1) {
		if len(match) < 2 {
			continue
		}
		seen[match[1]] = struct{}{}
	}
	items := make([]string, 0, len(seen))
	for item := range seen {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

func filterNamedTables(tableNames []string, allowed map[string]struct{}) []string {
	items := make([]string, 0, len(tableNames))
	for _, name := range tableNames {
		if _, ok := allowed[name]; ok {
			items = append(items, name)
		}
	}
	return items
}

func firstConfiguredString(values ...*string) string {
	for _, value := range values {
		if trimmed := firstNonEmptyPtr(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

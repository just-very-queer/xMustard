package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fatalUsage("usage: xmustard-ops <semantic-index|postgres|workspace> ...")
	}
	switch args[0] {
	case "semantic-index":
		runSemanticIndex(args[1:])
	case "postgres":
		runPostgres(args[1:])
	case "workspace":
		runWorkspace(args[1:])
	default:
		fatalUsage("usage: xmustard-ops <semantic-index|postgres|workspace> ...")
	}
}

func runSemanticIndex(args []string) {
	if len(args) < 2 {
		fatalUsage("usage: xmustard-ops semantic-index <plan|run|status> <workspace_id> [flags]")
	}
	action := args[0]
	workspaceID := strings.TrimSpace(args[1])
	if workspaceID == "" {
		fatalUsage("workspace_id is required")
	}
	fs := flag.NewFlagSet("xmustard-ops", flag.ExitOnError)
	dataDir := fs.String("data-dir", envDefault("XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
	surface := fs.String("surface", "cli", "cli | web | all")
	strategy := fs.String("strategy", "key_files", "key_files | paths")
	limit := fs.Int("limit", 12, "path selection limit")
	dsn := fs.String("dsn", "", "Postgres DSN override")
	schema := fs.String("schema", "", "Postgres schema override")
	dryRun := fs.Bool("dry-run", false, "plan without applying")
	var paths stringSliceFlag
	fs.Var(&paths, "path", "exact relative path to include; may be repeated")
	if err := fs.Parse(args[2:]); err != nil {
		fatal(err.Error())
	}
	request := workspaceops.SemanticIndexRequest{
		Surface:  *surface,
		Strategy: *strategy,
		Paths:    []string(paths),
		Limit:    *limit,
		DryRun:   *dryRun,
	}
	if strings.TrimSpace(*dsn) != "" {
		value := strings.TrimSpace(*dsn)
		request.DSN = &value
	}
	if strings.TrimSpace(*schema) != "" {
		value := strings.TrimSpace(*schema)
		request.Schema = &value
	}

	var (
		payload any
		err     error
	)
	switch action {
	case "plan":
		payload, err = workspaceops.PlanSemanticIndex(*dataDir, workspaceID, request)
	case "run":
		payload, err = workspaceops.RunSemanticIndex(*dataDir, workspaceID, request)
	case "status":
		payload, err = workspaceops.ReadSemanticIndexStatus(*dataDir, workspaceID, request)
	default:
		fatalUsage("usage: xmustard-ops semantic-index <plan|run|status> <workspace_id> [flags]")
	}
	writeJSON(payload, err)
}

func runPostgres(args []string) {
	if len(args) < 1 {
		fatalUsage("usage: xmustard-ops postgres <plan|render|bootstrap> [flags]")
	}
	action := args[0]
	fs := flag.NewFlagSet("xmustard-ops postgres", flag.ExitOnError)
	dataDir := fs.String("data-dir", envDefault("XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
	dsn := fs.String("dsn", "", "Postgres DSN override")
	schema := fs.String("schema", "", "Postgres schema override")
	if err := fs.Parse(args[1:]); err != nil {
		fatal(err.Error())
	}

	var (
		payload any
		err     error
	)
	switch action {
	case "plan":
		payload, err = workspaceops.GetPostgresSchemaPlan(*dataDir)
	case "render":
		rendered, renderErr := workspaceops.RenderPostgresSchemaSQL(*dataDir, *schema)
		if renderErr != nil {
			fatal(renderErr.Error())
		}
		fmt.Print(rendered)
		return
	case "bootstrap":
		payload, err = workspaceops.BootstrapPostgresSchema(*dataDir, workspaceops.PostgresBootstrapRequest{
			DSN:        optionalFlagString(*dsn),
			SchemaName: optionalFlagString(*schema),
		})
	default:
		fatalUsage("usage: xmustard-ops postgres <plan|render|bootstrap> [flags]")
	}
	writeJSON(payload, err)
}

func runWorkspace(args []string) {
	if len(args) < 2 {
		fatalUsage("usage: xmustard-ops workspace <action> <workspace_id> [flags]")
	}
	action := args[0]
	workspaceID := strings.TrimSpace(args[1])
	if workspaceID == "" {
		fatalUsage("workspace_id is required")
	}
	fs := flag.NewFlagSet("xmustard-ops workspace", flag.ExitOnError)
	dataDir := fs.String("data-dir", envDefault("XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
	baseRef := fs.String("base-ref", "HEAD", "git base ref")
	query := fs.String("query", "", "retrieval query")
	pattern := fs.String("pattern", "", "semantic pattern")
	language := fs.String("language", "", "semantic language")
	pathGlob := fs.String("path-glob", "", "path glob")
	path := fs.String("path", "", "relative workspace path")
	strategy := fs.String("strategy", "key_files", "key_files | paths")
	limit := fs.Int("limit", 12, "result or path selection limit")
	dsn := fs.String("dsn", "", "Postgres DSN override")
	schema := fs.String("schema", "", "Postgres schema override")
	var paths stringSliceFlag
	fs.Var(&paths, "select-path", "exact relative path to include in a workspace materialization batch; may be repeated")
	if err := fs.Parse(args[2:]); err != nil {
		fatal(err.Error())
	}

	var (
		payload any
		err     error
	)
	switch action {
	case "scan":
		payload, err = workspaceops.ScanWorkspace(*dataDir, workspaceID)
	case "changed-symbols":
		payload, err = workspaceops.ReadChangedSymbols(*dataDir, workspaceID, *baseRef)
	case "impact":
		payload, err = workspaceops.ReadImpact(*dataDir, workspaceID, *baseRef)
	case "repo-context":
		payload, err = workspaceops.ReadRepoContext(*dataDir, workspaceID, *baseRef)
	case "repo-map":
		payload, err = workspaceops.ReadWorkspaceRepoMap(*dataDir, workspaceID)
	case "retrieval-search":
		payload, err = workspaceops.SearchRetrieval(*dataDir, workspaceID, *query, *limit)
	case "path-symbols":
		payload, err = workspaceops.ReadPathSymbols(*dataDir, workspaceID, *path)
	case "explain-path":
		payload, err = workspaceops.ExplainPath(*dataDir, workspaceID, *path)
	case "semantic-search":
		payload, err = workspaceops.SearchSemanticPattern(*dataDir, workspaceID, *pattern, *language, *pathGlob, *limit)
	case "postgres-materialize-path":
		payload, err = workspaceops.MaterializePathSymbolsToPostgres(*dataDir, workspaceID, workspaceops.PostgresPathMaterializationRequest{
			Path:       *path,
			DSN:        optionalFlagString(*dsn),
			SchemaName: optionalFlagString(*schema),
		})
	case "postgres-materialize-workspace-symbols":
		payload, err = workspaceops.MaterializeWorkspaceSymbolsToPostgres(*dataDir, workspaceID, workspaceops.PostgresWorkspaceSemanticMaterializationRequest{
			Strategy:   *strategy,
			Paths:      []string(paths),
			Limit:      *limit,
			DSN:        optionalFlagString(*dsn),
			SchemaName: optionalFlagString(*schema),
		})
	case "postgres-materialize-semantic-search":
		payload, err = workspaceops.MaterializeSemanticSearchToPostgres(*dataDir, workspaceID, workspaceops.PostgresSemanticSearchMaterializationRequest{
			Pattern:    *pattern,
			Language:   optionalFlagString(*language),
			PathGlob:   optionalFlagString(*pathGlob),
			Limit:      *limit,
			DSN:        optionalFlagString(*dsn),
			SchemaName: optionalFlagString(*schema),
		})
	case "semantic-index-materialize":
		payload, err = workspaceops.MaterializeWorkspaceSymbolsToPostgres(*dataDir, workspaceID, workspaceops.PostgresWorkspaceSemanticMaterializationRequest{
			Strategy:   *strategy,
			Paths:      []string(paths),
			Limit:      *limit,
			DSN:        optionalFlagString(*dsn),
			SchemaName: optionalFlagString(*schema),
		})
	default:
		fatalUsage("usage: xmustard-ops workspace <scan|repo-map|changed-symbols|impact|repo-context|retrieval-search|path-symbols|explain-path|semantic-search|postgres-materialize-path|postgres-materialize-workspace-symbols|postgres-materialize-semantic-search|semantic-index-materialize> <workspace_id> [flags]")
	}
	writeJSON(payload, err)
}

func writeJSON(payload any, err error) {
	if err != nil {
		fatal(err.Error())
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		fatal(err.Error())
	}
	_, _ = os.Stdout.Write(append(encoded, '\n'))
}

func optionalFlagString(value string) *string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func fatalUsage(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(2)
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

func envDefault(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

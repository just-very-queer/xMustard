package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

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
		fatalUsage("usage: xmustard-ops <diagnostics|semantic-index|postgres|runtime|workspace> ...")
	}
	switch args[0] {
	case "diagnostics":
		runDiagnostics(args[1:])
	case "semantic-index":
		runSemanticIndex(args[1:])
	case "postgres":
		runPostgres(args[1:])
	case "runtime":
		runRuntime(args[1:])
	case "workspace":
		runWorkspace(args[1:])
	default:
		fatalUsage("usage: xmustard-ops <diagnostics|semantic-index|postgres|runtime|workspace> ...")
	}
}

func runDiagnostics(args []string) {
	if len(args) < 2 {
		fatalUsage("usage: xmustard-ops diagnostics <plan|run|status|read|live> <workspace_id> [flags]")
	}
	action := args[0]
	workspaceID := strings.TrimSpace(args[1])
	if workspaceID == "" {
		fatalUsage("workspace_id is required")
	}
	fs := flag.NewFlagSet("xmustard-ops diagnostics", flag.ExitOnError)
	dataDir := fs.String("data-dir", envDefault("XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
	inputPath := fs.String("input-path", "", "LSP publishDiagnostics JSON file")
	sourceKind := fs.String("source-kind", "lsp", "lsp | compiler | test | scanner | manual")
	sourceName := fs.String("source-name", "", "diagnostic source name, e.g. pyright or typescript-language-server")
	issueID := fs.String("issue-id", "", "optional issue id to anchor the diagnostics baseline")
	runID := fs.String("run-id", "", "optional run id to anchor the diagnostics baseline")
	path := fs.String("path", "", "relative workspace path for live LSP diagnostics")
	diagnosticRunID := fs.String("diagnostic-run-id", "", "historical diagnostics run id for durable reads")
	dsn := fs.String("dsn", "", "Postgres DSN override")
	schema := fs.String("schema", "", "Postgres schema override")
	dryRun := fs.Bool("dry-run", false, "plan without applying")
	if err := fs.Parse(args[2:]); err != nil {
		fatal(err.Error())
	}
	request := workspaceops.DiagnosticsRequest{
		InputPath:  *inputPath,
		SourceKind: *sourceKind,
		SourceName: *sourceName,
		DryRun:     *dryRun,
	}
	if strings.TrimSpace(*issueID) != "" {
		value := strings.TrimSpace(*issueID)
		request.IssueID = &value
	}
	if strings.TrimSpace(*runID) != "" {
		value := strings.TrimSpace(*runID)
		request.RunID = &value
	}
	if strings.TrimSpace(*dsn) != "" {
		value := strings.TrimSpace(*dsn)
		request.DSN = &value
	}
	if strings.TrimSpace(*schema) != "" {
		value := strings.TrimSpace(*schema)
		request.SchemaName = &value
	}

	var (
		payload any
		err     error
	)
	switch action {
	case "plan":
		payload, err = workspaceops.PlanDiagnostics(*dataDir, workspaceID, request)
	case "run":
		payload, err = workspaceops.RunDiagnostics(*dataDir, workspaceID, request)
	case "status":
		payload, err = workspaceops.ReadDiagnosticsStatus(*dataDir, workspaceID)
	case "read":
		payload, err = workspaceops.ReadDiagnostics(*dataDir, workspaceID, *diagnosticRunID)
	case "live":
		payload, err = workspaceops.ReadLiveDiagnostics(*dataDir, workspaceID, *path)
	default:
		fatalUsage("usage: xmustard-ops diagnostics <plan|run|status|read|live> <workspace_id> [flags]")
	}
	writeJSON(payload, err)
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

func runRuntime(args []string) {
	if len(args) < 1 {
		fatalUsage("usage: xmustard-ops runtime <capabilities|runtimes|models|probe> [args] [flags]")
	}
	action := args[0]
	switch action {
	case "capabilities", "runtimes":
		fs := flag.NewFlagSet("xmustard-ops runtime "+action, flag.ExitOnError)
		dataDir := fs.String("data-dir", envDefault("XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
		if err := fs.Parse(args[1:]); err != nil {
			fatal(err.Error())
		}
		if action == "capabilities" {
			writeJSON(workspaceops.GetLocalAgentCapabilities(*dataDir))
			return
		}
		writeJSON(workspaceops.DetectRuntimes(*dataDir))
	case "models":
		if len(args) < 2 {
			fatalUsage("usage: xmustard-ops runtime models <runtime> [flags]")
		}
		runtimeName := strings.TrimSpace(args[1])
		if runtimeName == "" {
			fatalUsage("runtime is required")
		}
		fs := flag.NewFlagSet("xmustard-ops runtime models", flag.ExitOnError)
		dataDir := fs.String("data-dir", envDefault("XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
		if err := fs.Parse(args[2:]); err != nil {
			fatal(err.Error())
		}
		runtimes, err := workspaceops.DetectRuntimes(*dataDir)
		if err != nil {
			writeJSON(nil, err)
			return
		}
		for _, entry := range runtimes {
			if entry.Runtime == runtimeName {
				writeJSON(entry.Models, nil)
				return
			}
		}
		fatal("unknown runtime: " + runtimeName)
	case "probe":
		if len(args) < 2 {
			fatalUsage("usage: xmustard-ops runtime probe <workspace_id> --runtime <runtime> --model <model> [flags]")
		}
		workspaceID := strings.TrimSpace(args[1])
		if workspaceID == "" {
			fatalUsage("workspace_id is required")
		}
		fs := flag.NewFlagSet("xmustard-ops runtime probe", flag.ExitOnError)
		dataDir := fs.String("data-dir", envDefault("XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
		runtimeName := fs.String("runtime", "", "runtime id")
		model := fs.String("model", "", "runtime model id")
		if err := fs.Parse(args[2:]); err != nil {
			fatal(err.Error())
		}
		if strings.TrimSpace(*runtimeName) == "" || strings.TrimSpace(*model) == "" {
			fatalUsage("usage: xmustard-ops runtime probe <workspace_id> --runtime <runtime> --model <model> [flags]")
		}
		writeJSON(workspaceops.ProbeRuntime(*dataDir, workspaceID, *runtimeName, *model))
	default:
		fatalUsage("usage: xmustard-ops runtime <capabilities|runtimes|models|probe> [args] [flags]")
	}
}

func runWorkspace(args []string) {
	if len(args) < 1 {
		fatalUsage("usage: xmustard-ops workspace <list|load|scan|repo-state|ingestion-plan|run-targets|verify-targets|project-info|verification-outcomes|verification-profiles|verification-profile-save|verification-profile-run|repo-map|changed-symbols|impact|repo-context|issue-context|retrieval-search|path-symbols|document-symbols|go-to-definition|references|workspace-symbols|live-workspace-symbols|explain-path|semantic-search|postgres-materialize-path|postgres-materialize-workspace-symbols|postgres-materialize-semantic-search|semantic-index-materialize> [workspace_id] [flags]")
	}
	action := args[0]
	switch action {
	case "list":
		fs := flag.NewFlagSet("xmustard-ops workspace list", flag.ExitOnError)
		dataDir := fs.String("data-dir", envDefault("XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
		if err := fs.Parse(args[1:]); err != nil {
			fatal(err.Error())
		}
		payload, err := workspaceops.ListWorkspaces(*dataDir)
		writeJSON(payload, err)
		return
	case "load":
		fs := flag.NewFlagSet("xmustard-ops workspace load", flag.ExitOnError)
		dataDir := fs.String("data-dir", envDefault("XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
		rootPath := fs.String("root-path", "", "workspace root path")
		name := fs.String("name", "", "workspace name override")
		autoScan := fs.Bool("auto-scan", true, "scan the workspace after loading it")
		preferCachedSnapshot := fs.Bool("prefer-cached-snapshot", true, "reuse a current cached snapshot when possible")
		if err := fs.Parse(args[1:]); err != nil {
			fatal(err.Error())
		}
		if strings.TrimSpace(*rootPath) == "" {
			fatalUsage("usage: xmustard-ops workspace load --root-path <path> [flags]")
		}
		request := workspaceops.WorkspaceLoadRequest{
			RootPath:             *rootPath,
			AutoScan:             *autoScan,
			PreferCachedSnapshot: *preferCachedSnapshot,
		}
		if trimmedName := strings.TrimSpace(*name); trimmedName != "" {
			request.Name = &trimmedName
		}
		payload, err := workspaceops.LoadWorkspace(*dataDir, request)
		writeJSON(payload, err)
		return
	}
	if len(args) < 2 {
		fatalUsage("usage: xmustard-ops workspace <scan|repo-state|ingestion-plan|run-targets|verify-targets|project-info|verification-outcomes|verification-profiles|verification-profile-save|verification-profile-run|repo-map|changed-symbols|impact|repo-context|issue-context|retrieval-search|path-symbols|document-symbols|go-to-definition|references|workspace-symbols|live-workspace-symbols|explain-path|semantic-search|postgres-materialize-path|postgres-materialize-workspace-symbols|postgres-materialize-semantic-search|semantic-index-materialize> <workspace_id> [flags]")
	}
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
	issueID := fs.String("issue-id", "", "issue id for issue-context reads")
	profileID := fs.String("profile-id", "", "verification profile id")
	name := fs.String("name", "", "verification profile name")
	description := fs.String("description", "", "verification profile description")
	testCommand := fs.String("test-command", "", "verification profile test command")
	coverageCommand := fs.String("coverage-command", "", "verification profile coverage command")
	coverageReportPath := fs.String("coverage-report-path", "", "verification profile coverage report path")
	coverageFormat := fs.String("coverage-format", "unknown", "verification profile coverage format")
	maxRuntimeSeconds := fs.Int64("max-runtime-seconds", 30, "verification profile max runtime seconds")
	retryCount := fs.Int64("retry-count", 1, "verification profile retry count")
	runID := fs.String("run-id", "", "run id for verification profile execution")
	timeoutSeconds := fs.Int("timeout-seconds", 125, "verification profile execution timeout in seconds")
	line := fs.Int("line", 1, "1-based file line")
	column := fs.Int("column", 1, "1-based file column")
	includeDeclaration := fs.Bool("include-declaration", true, "include declaration location in LSP references")
	strategy := fs.String("strategy", "key_files", "key_files | paths")
	limit := fs.Int("limit", 12, "result or path selection limit")
	dsn := fs.String("dsn", "", "Postgres DSN override")
	schema := fs.String("schema", "", "Postgres schema override")
	var paths stringSliceFlag
	var sourcePaths stringSliceFlag
	var checklistItems stringSliceFlag
	fs.Var(&paths, "select-path", "exact relative path to include in a workspace materialization batch; may be repeated")
	fs.Var(&sourcePaths, "source-path", "verification profile source path; may be repeated")
	fs.Var(&checklistItems, "checklist-item", "verification profile checklist item; may be repeated")
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
	case "repo-state":
		payload, err = workspaceops.ReadRepoToolState(*dataDir, workspaceID)
	case "ingestion-plan":
		payload, err = workspaceops.ReadIngestionPlan(*dataDir, workspaceID)
	case "run-targets":
		payload, err = workspaceops.ReadRunTargets(*dataDir, workspaceID)
	case "verify-targets":
		payload, err = workspaceops.ReadVerifyTargets(*dataDir, workspaceID)
	case "project-info":
		payload, err = workspaceops.ReadProjectInfo(*dataDir, workspaceID)
	case "verification-outcomes":
		payload, err = workspaceops.ReadVerificationOutcomes(*dataDir, workspaceID)
	case "verification-profiles":
		payload, err = workspaceops.ListVerificationProfiles(*dataDir, workspaceID)
	case "verification-profile-save":
		profileName := strings.TrimSpace(*name)
		command := strings.TrimSpace(*testCommand)
		if profileName == "" || command == "" {
			fatalUsage("usage: xmustard-ops workspace verification-profile-save <workspace_id> --name <name> --test-command <command> [flags]")
		}
		request := workspaceops.VerificationProfileUpsertRequest{
			ProfileID:          optionalFlagString(*profileID),
			Name:               profileName,
			Description:        strings.TrimSpace(*description),
			TestCommand:        command,
			CoverageCommand:    optionalFlagString(*coverageCommand),
			CoverageReportPath: optionalFlagString(*coverageReportPath),
			CoverageFormat:     strings.TrimSpace(*coverageFormat),
			MaxRuntimeSeconds:  *maxRuntimeSeconds,
			RetryCount:         *retryCount,
			SourcePaths:        []string(sourcePaths),
			ChecklistItems:     []string(checklistItems),
		}
		payload, err = workspaceops.SaveVerificationProfile(*dataDir, workspaceID, request)
	case "verification-profile-run":
		targetIssueID := strings.TrimSpace(*issueID)
		targetProfileID := strings.TrimSpace(*profileID)
		if targetIssueID == "" || targetProfileID == "" {
			fatalUsage("usage: xmustard-ops workspace verification-profile-run <workspace_id> --issue-id <issue_id> --profile-id <profile_id> [flags]")
		}
		if *timeoutSeconds < 1 {
			*timeoutSeconds = 125
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSeconds)*time.Second)
		defer cancel()
		payload, err = workspaceops.RunIssueVerificationProfile(
			ctx,
			*dataDir,
			workspaceID,
			targetIssueID,
			targetProfileID,
			strings.TrimSpace(*runID),
		)
	case "changed-symbols":
		payload, err = workspaceops.ReadChangedSymbols(*dataDir, workspaceID, *baseRef)
	case "impact":
		payload, err = workspaceops.ReadImpact(*dataDir, workspaceID, *baseRef)
	case "repo-context":
		payload, err = workspaceops.ReadRepoContext(*dataDir, workspaceID, *baseRef)
	case "issue-context":
		targetIssueID := strings.TrimSpace(*issueID)
		if targetIssueID == "" {
			fatalUsage("usage: xmustard-ops workspace issue-context <workspace_id> --issue-id <issue_id> [flags]")
		}
		payload, err = workspaceops.BuildIssueContextPacket(*dataDir, workspaceID, targetIssueID)
	case "repo-map":
		payload, err = workspaceops.ReadWorkspaceRepoMap(*dataDir, workspaceID)
	case "retrieval-search":
		payload, err = workspaceops.SearchRetrieval(*dataDir, workspaceID, *query, *limit)
	case "path-symbols":
		payload, err = workspaceops.ReadPathSymbols(*dataDir, workspaceID, *path)
	case "document-symbols":
		payload, err = workspaceops.ReadDocumentSymbols(*dataDir, workspaceID, *path)
	case "go-to-definition":
		payload, err = workspaceops.GoToDefinition(*dataDir, workspaceID, *path, *line, *column)
	case "references":
		payload, err = workspaceops.FindReferences(*dataDir, workspaceID, *path, *line, *column, *includeDeclaration)
	case "workspace-symbols":
		payload, err = workspaceops.ReadWorkspaceSymbols(*dataDir, workspaceID, *query, *limit)
	case "live-workspace-symbols":
		payload, err = workspaceops.LSPWorkspaceSymbols(*dataDir, workspaceID, *language, *query, *limit)
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
		fatalUsage("usage: xmustard-ops workspace <list|load|scan|repo-state|ingestion-plan|run-targets|verify-targets|project-info|verification-outcomes|verification-profiles|verification-profile-save|verification-profile-run|repo-map|changed-symbols|impact|repo-context|issue-context|retrieval-search|path-symbols|document-symbols|go-to-definition|references|workspace-symbols|live-workspace-symbols|explain-path|semantic-search|postgres-materialize-path|postgres-materialize-workspace-symbols|postgres-materialize-semantic-search|semantic-index-materialize> [workspace_id] [flags]")
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

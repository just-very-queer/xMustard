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
	if len(args) < 3 || args[0] != "semantic-index" {
		fatalUsage("usage: xmustard-ops semantic-index <plan|run|status> <workspace_id> [flags]")
	}
	action := args[1]
	workspaceID := strings.TrimSpace(args[2])
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
	if err := fs.Parse(args[3:]); err != nil {
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
	if err != nil {
		fatal(err.Error())
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		fatal(err.Error())
	}
	_, _ = os.Stdout.Write(append(encoded, '\n'))
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

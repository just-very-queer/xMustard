package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"xmustard/api-go/internal/sessionimport"
	"xmustard/api-go/internal/workspaceops"
)

// `import sessions` (WS-34; PAR-ADP-12) streams the session stores of Claude Code,
// Codex, OpenCode and Pi for one project into the workspace's transcript store. The text
// is redacted on the way in and stays a raw observation: searchable with
// recall(source=sessions), never memory. It runs as the token in XMUSTARD_API_TOKEN
// when auth is configured (proposer role), else as the open-mode identity.

func init() {
	governanceCommands["import"] = func(e opsEnv, args []string) int {
		if len(args) == 0 || args[0] != "sessions" {
			return e.usage(importUsage)
		}
		return runImportSessions(e, args[1:])
	}
}

const importUsage = "usage: xmustard-ops import sessions [--source claude|codex|opencode|pi|all] [--project DIR] [--workspace ID] [--dir STORE] [--since RFC3339] [--data-dir DIR]"

// importResult is the command's output: what each source wrote and what the process cost.
type importResult struct {
	Reports []workspaceops.ImportReport `json:"reports"`
	// PeakRSSBytes is the process's peak resident set: the transient cost of the import.
	PeakRSSBytes int64  `json:"peak_rss_bytes"`
	ElapsedMS    int64  `json:"elapsed_ms"`
	Note         string `json:"note"`
}

func runImportSessions(e opsEnv, args []string) int {
	fs, dataDir := e.dataDirFlagSet("import sessions")
	source := fs.String("source", "all", "claude | codex | opencode | pi | all")
	project := fs.String("project", ".", "project directory whose sessions to import")
	workspace := fs.String("workspace", "", "workspace id (default: the workspace registered at --project)")
	dir := fs.String("dir", "", "store root to read (needs a single --source)")
	since := fs.String("since", "", "only messages at or after this RFC 3339 time")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return e.usage(importUsage)
	}
	names := sessionimport.Names()
	if *source != "all" {
		names = []string{*source}
	}
	if _, ok := sessionimport.Sources[names[0]]; !ok || *dir != "" && len(names) != 1 {
		return e.usage(importUsage)
	}
	root, err := filepath.Abs(*project)
	if err != nil {
		return e.fail(err)
	}
	opts := sessionimport.Options{Dir: *dir, Project: root}
	if *since != "" {
		if opts.Since, err = time.Parse(time.RFC3339, *since); err != nil {
			return e.fail(err)
		}
	}
	ws := strings.TrimSpace(*workspace)
	if ws == "" {
		found, err := workspaceops.LookupWorkspaceRoot(*dataDir, root)
		if err != nil || !found.Registered {
			return e.usage("no workspace is registered at " + root + "; pass --workspace")
		}
		ws = found.ID
	}
	principal, err := workspaceops.ImportActor(*dataDir, ws, e.getenv("XMUSTARD_API_TOKEN"))
	if err != nil {
		return e.fail(err)
	}
	start := time.Now()
	res := importResult{Note: "imported text is raw, redacted observation: searchable with recall source=sessions, never verified memory"}
	for _, name := range names {
		rep, err := workspaceops.ImportSessions(context.Background(), *dataDir, ws, principal, sessionimport.Sources[name], opts)
		res.Reports = append(res.Reports, rep)
		if err != nil {
			e.emit(res)
			return e.fail(err)
		}
	}
	res.ElapsedMS, res.PeakRSSBytes = time.Since(start).Milliseconds(), peakRSS()
	return e.emit(res)
}

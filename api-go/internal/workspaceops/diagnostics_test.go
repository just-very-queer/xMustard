package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRunDiagnosticsNormalizesWithRustAndPersistsRows(t *testing.T) {
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	runID := "run-123"
	runPath := filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".json")
	if err := writeJSON(runPath, runRecord{
		RunID:       runID,
		WorkspaceID: workspaceID,
		IssueID:     "P0_25M03_001",
		Runtime:     "codex",
		Model:       "gpt-5.4",
		Status:      "completed",
		Title:       "codex:P0_25M03_001",
		Prompt:      "fix it",
		Command:     []string{"codex", "exec"},
		CreatedAt:   "2026-05-04T00:00:00Z",
	}); err != nil {
		t.Fatalf("write run fixture: %v", err)
	}
	inputPath := filepath.Join(repoRoot, "diagnostics.json")
	payload := `{
		"path": "src/app.py",
		"diagnostics": [{
			"range": {
				"start": {"line": 0, "character": 6},
				"end": {"line": 0, "character": 19}
			},
			"severity": 1,
			"code": "PY100",
			"source": "pyright",
			"message": "Example LSP diagnostic."
		}]
	}`
	if err := os.WriteFile(inputPath, []byte(payload), 0o644); err != nil {
		t.Fatalf("write diagnostics input: %v", err)
	}

	candidatesJSON, err := json.Marshal([]map[string]any{{
		"symbol_id":      21,
		"path":           "src/app.py",
		"symbol":         "ExportService",
		"kind":           "class",
		"line_start":     1,
		"line_end":       1,
		"signature_text": "class ExportService:",
	}})
	if err != nil {
		t.Fatalf("marshal symbol candidates: %v", err)
	}
	semanticBaselineJSON, err := json.Marshal([]map[string]any{{
		"index_run_id":       "semidx_fixture",
		"index_fingerprint":  "semfp",
		"surface":            "cli",
		"strategy":           "paths",
		"materialized_paths": []string{"src/app.py"},
	}})
	if err != nil {
		t.Fatalf("marshal semantic baseline candidates: %v", err)
	}
	fakeConn := &fakeSemanticConn{
		queryRows: []pgx.Row{
			fakeSemanticJSONRow(semanticBaselineJSON),
			fakeSemanticBaselineRowValues(int64(21)),
			fakeSemanticBaselineRowValues(true),
			fakeSemanticJSONRow(candidatesJSON),
		},
	}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()
	originalResolve := resolveLSPServerForPath
	resolveLSPServerForPath = func(rootPath string, relativePath string) (*lspServerConfig, error) {
		return &lspServerConfig{
			ServerID:   "pyright",
			LanguageID: "python",
			Command:    []string{"/usr/local/bin/pyright-langserver", "--stdio"},
		}, nil
	}
	defer func() {
		resolveLSPServerForPath = originalResolve
	}()

	result, err := RunDiagnostics(dataDir, workspaceID, DiagnosticsRequest{
		InputPath:  "diagnostics.json",
		SourceKind: "lsp",
		SourceName: "pyright",
		RunID:      optionalString(runID),
		DSN:        optionalString("postgres://user:secret@example.com/xmustard"),
		SchemaName: optionalString("xmustard"),
	})
	if err != nil {
		t.Fatalf("run diagnostics: %v", err)
	}
	if result.Baseline == nil || result.DiagnosticRows != 1 || result.Plan.DiagnosticCount != 1 {
		t.Fatalf("unexpected diagnostics run result: %#v", result)
	}
	if result.Plan.NormalizedBatch.Diagnostics[0].SourceKind != "lsp" {
		t.Fatalf("expected LSP diagnostic provenance, got %#v", result.Plan.NormalizedBatch.Diagnostics[0])
	}
	if !containsSubstring(fakeConn.execSQL, "insert into xmustard.diagnostic_runs") || !containsSubstring(fakeConn.execSQL, "insert into xmustard.diagnostics") {
		t.Fatalf("expected diagnostic run and row writes, got %#v", fakeConn.execSQL)
	}
	if !containsSubstring(fakeConn.execSQL, "insert into xmustard.run_records") {
		t.Fatalf("expected linked run record upsert before diagnostics linkage, got %#v", fakeConn.execSQL)
	}
	if !containsSubstring(fakeConn.execSQL, "issue_id") || !containsSubstring(fakeConn.execSQL, "run_id") {
		t.Fatalf("expected durable issue/run linkage columns, got %#v", fakeConn.execSQL)
	}
	if !containsSubstring(fakeConn.execSQL, "semantic_baseline_json") || !containsSubstring(fakeConn.execSQL, "link_context_json") {
		t.Fatalf("expected historical semantic replay columns, got %#v", fakeConn.execSQL)
	}
	if !containsSubstring(fakeConn.execSQL, "raw_payload_json") || !containsSubstring(fakeConn.execSQL, "raw_payload_sha256") || !containsSubstring(fakeConn.execSQL, "server_provenance_json") {
		t.Fatalf("expected raw payload archive and provenance columns, got %#v", fakeConn.execSQL)
	}
	if !containsSubstring(fakeConn.execSQL, "link_status") || !containsSubstring(fakeConn.execSQL, "linked_symbol_json") || !containsSubstring(fakeConn.execSQL, "symbol_id") {
		t.Fatalf("expected durable diagnostic link replay columns, got %#v", fakeConn.execSQL)
	}
	runArgs := diagnosticRunInsertArgs(t, fakeConn)
	if issueArg, ok := runArgs[2].(*string); !ok || issueArg == nil || *issueArg != "P0_25M03_001" {
		t.Fatalf("expected derived issue linkage, got %#v", runArgs[2])
	}
	if linkedRunArg, ok := runArgs[3].(*string); !ok || linkedRunArg == nil || *linkedRunArg != runID {
		t.Fatalf("expected run linkage, got %#v", runArgs[3])
	}
	if rawPayload, ok := runArgs[7].(*string); !ok || rawPayload == nil || !strings.Contains(*rawPayload, "Example LSP diagnostic.") {
		t.Fatalf("expected archived raw diagnostic payload, got %#v", runArgs[7])
	}
	if sha, ok := runArgs[8].(string); !ok || len(sha) != 64 {
		t.Fatalf("expected raw payload sha256, got %#v", runArgs[8])
	}
	if bytes, ok := runArgs[9].(int); !ok || bytes <= 0 {
		t.Fatalf("expected raw payload byte count, got %#v", runArgs[9])
	}
	if provenance, ok := runArgs[10].(*string); !ok || provenance == nil || !strings.Contains(*provenance, `"server_id":"pyright"`) || !strings.Contains(*provenance, `"source_mode":"input_file"`) || !strings.Contains(*provenance, `"server_command":["/usr/local/bin/pyright-langserver","--stdio"]`) || !strings.Contains(*provenance, `"provenance_level":"resolved_lsp_command"`) {
		t.Fatalf("expected archived server provenance, got %#v", runArgs[10])
	}
	if contract, ok := runArgs[11].(string); !ok || contract != "diagnostics.normalized.v1" {
		t.Fatalf("expected normalization contract, got %#v", runArgs[11])
	}
	if readiness, ok := runArgs[12].(string); !ok || readiness != "raw_payload_and_server_provenance_archived" {
		t.Fatalf("expected archive replay readiness, got %#v", runArgs[12])
	}
	if result.Baseline.RunID == nil || *result.Baseline.RunID != runID || result.Baseline.IssueID == nil || *result.Baseline.IssueID != "P0_25M03_001" {
		t.Fatalf("expected persisted run and issue linkage, got %#v", result.Baseline)
	}
	if result.Baseline.SemanticBaseline == nil || result.Baseline.SemanticBaseline.IndexRunID != "semidx_fixture" {
		t.Fatalf("expected persisted semantic baseline anchor, got %#v", result.Baseline)
	}
	activityPath := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	activityContent, err := os.ReadFile(activityPath)
	if err != nil {
		t.Fatalf("read activity log: %v", err)
	}
	if !strings.Contains(string(activityContent), "postgres.materialize.diagnostics") {
		t.Fatalf("expected diagnostics activity, got %s", activityContent)
	}
}

func TestPlanDiagnosticsRejectsIssueMismatchForLinkedRun(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
	runID := "run-123"
	runPath := filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".json")
	if err := writeJSON(runPath, runRecord{
		RunID:       runID,
		WorkspaceID: workspaceID,
		IssueID:     "P0_25M03_001",
		Runtime:     "codex",
		Model:       "gpt-5.4",
		Status:      "completed",
		Title:       "codex:P0_25M03_001",
		Prompt:      "fix it",
		Command:     []string{"codex", "exec"},
		CreatedAt:   "2026-05-04T00:00:00Z",
	}); err != nil {
		t.Fatalf("write run fixture: %v", err)
	}

	plan, err := PlanDiagnostics(dataDir, workspaceID, DiagnosticsRequest{
		InputPath:  "missing.json",
		SourceKind: "lsp",
		SourceName: "pyright",
		IssueID:    optionalString("OTHER_ISSUE"),
		RunID:      optionalString(runID),
	})
	if err != nil {
		t.Fatalf("plan diagnostics: %v", err)
	}
	if plan.CanRun {
		t.Fatalf("expected mismatch blocker, got %#v", plan)
	}
	if !strings.Contains(strings.Join(plan.Blockers, "\n"), "does not match run") {
		t.Fatalf("expected mismatch blocker, got %#v", plan.Blockers)
	}
}

func diagnosticRunInsertArgs(t *testing.T, fakeConn *fakeSemanticConn) []any {
	t.Helper()
	for idx, sql := range fakeConn.execSQL {
		if strings.Contains(sql, "insert into xmustard.diagnostic_runs") {
			return fakeConn.execArgs[idx]
		}
	}
	t.Fatalf("missing diagnostic run insert: %#v", fakeConn.execSQL)
	return nil
}

func TestDiagnosticsStatusBlocksWithoutPostgres(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	status, err := ReadDiagnosticsStatus(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read diagnostics status: %v", err)
	}
	if status.Status != "blocked" || status.PostgresConfigured {
		t.Fatalf("expected blocked status without Postgres, got %#v", status)
	}
}

func TestReadDiagnosticsWarnsWhenLegacyReplayArchiveLacksResolvedServerProvenance(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
	dsn := "postgresql://xmustard:secret@localhost:5432/xmustard"
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), appSettings{
		LocalAgentType: "codex",
		PostgresDSN:    &dsn,
		PostgresSchema: "xmustard",
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	countsJSON, _ := json.Marshal(map[string]int{"error": 1})
	diagnosticsJSON, _ := json.Marshal([]map[string]any{
		{
			"workspace_id":       workspaceID,
			"diagnostic_run_id":  "diag_fixture",
			"path":               "src/app.py",
			"range_start_line":   1,
			"range_start_column": 1,
			"range_end_line":     1,
			"range_end_column":   5,
			"severity":           "error",
			"message":            "Legacy diagnostic.",
			"source_kind":        "lsp",
			"source_name":        "pyright",
			"fingerprint":        "diagfp4",
			"link_status":        "evaluated_unlinked",
			"generated_at":       "2026-05-04T00:00:00Z",
		},
	})
	rawPayloadJSON, _ := json.Marshal(map[string]any{
		"diagnostics": []map[string]any{{
			"message": "Legacy diagnostic.",
		}},
	})
	serverProvenanceJSON, _ := json.Marshal(map[string]any{
		"source_mode": "input_file",
		"server_id":   "pyright",
		"input_path":  "diagnostics.json",
	})
	replayWarningsJSON, _ := json.Marshal([]string{})
	sha := "fixturepayloadsha256"
	fakeConn := &fakeSemanticConn{
		queryRows: []pgx.Row{
			fakeSemanticBaselineRowValues(
				"diag_fixture",
				(*string)(nil),
				(*string)(nil),
				"lsp",
				"pyright",
				"batchfp",
				rawPayloadJSON,
				&sha,
				123,
				serverProvenanceJSON,
				"diagnostics.normalized.v1",
				(*string)(nil),
				replayWarningsJSON,
				[]byte("{}"),
				(*string)(nil),
				0,
				false,
				1,
				countsJSON,
				"diagnostics.json",
				"xmustard",
				"2026-05-04T00:00:00Z",
			),
			fakeSemanticJSONRow(diagnosticsJSON),
		},
	}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()

	result, err := ReadDiagnostics(dataDir, workspaceID, "diag_fixture")
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	if result.Baseline == nil || result.Baseline.ReplayArchive == nil {
		t.Fatalf("expected replay archive, got %#v", result)
	}
	if result.Baseline.ReplayArchive.ReplayReadiness != "raw_payload_archived_with_provenance_warnings" {
		t.Fatalf("expected downgraded legacy replay readiness, got %#v", result.Baseline.ReplayArchive)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "does not prove complete server provenance") {
		t.Fatalf("expected surfaced replay provenance warning, got %#v", result.Warnings)
	}
}

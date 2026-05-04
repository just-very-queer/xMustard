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

	result, err := RunDiagnostics(dataDir, workspaceID, DiagnosticsRequest{
		InputPath:  "diagnostics.json",
		SourceKind: "lsp",
		SourceName: "pyright",
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
	if !containsSubstring(fakeConn.execSQL, "semantic_baseline_json") || !containsSubstring(fakeConn.execSQL, "link_context_json") {
		t.Fatalf("expected historical semantic replay columns, got %#v", fakeConn.execSQL)
	}
	if !containsSubstring(fakeConn.execSQL, "link_status") || !containsSubstring(fakeConn.execSQL, "linked_symbol_json") || !containsSubstring(fakeConn.execSQL, "symbol_id") {
		t.Fatalf("expected durable diagnostic link replay columns, got %#v", fakeConn.execSQL)
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

package workspaceops

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestReadWorkspaceSymbolsReadsMaterializedRows(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
	dsn := "postgresql://xmustard:secret@localhost:5432/xmustard"
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), appSettings{
		LocalAgentType: "codex",
		PostgresDSN:    &dsn,
		PostgresSchema: "xmustard",
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	payload, err := json.Marshal([]map[string]any{
		{
			"symbol_id":       21,
			"path":            "src/app.py",
			"symbol":          "ExportService",
			"kind":            "class",
			"line_start":      1,
			"line_end":        1,
			"symbol_source":   "regex",
			"parser_language": "python",
		},
	})
	if err != nil {
		t.Fatalf("marshal workspace symbols: %v", err)
	}

	fakeConn := &fakeSemanticConn{
		queryRows: []pgx.Row{
			fakeSemanticJSONRow(payload),
		},
	}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()

	result, err := ReadWorkspaceSymbols(dataDir, workspaceID, "Export", 10)
	if err != nil {
		t.Fatalf("read workspace symbols: %v", err)
	}
	if len(result.Symbols) != 1 || result.Symbols[0].Symbol != "ExportService" {
		t.Fatalf("unexpected workspace symbols: %#v", result)
	}
	if result.Query != "Export" || result.Limit != 10 {
		t.Fatalf("unexpected query metadata: %#v", result)
	}
}

func TestReadDocumentSymbolsAliasesPathSymbols(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	result, err := ReadDocumentSymbols(dataDir, workspaceID, "src/app.py")
	if err != nil {
		t.Fatalf("read document symbols: %v", err)
	}
	if result.EvidenceSource != "rust_semantic_core" || len(result.Symbols) == 0 {
		t.Fatalf("expected Rust-backed document symbols, got %#v", result)
	}
}

func TestReadDiagnosticsDecoratesRowsWithConservativeSymbolLinks(t *testing.T) {
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
			"range_start_column": 7,
			"range_end_line":     1,
			"range_end_column":   20,
			"severity":           "error",
			"message":            "Example LSP diagnostic.",
			"source_kind":        "lsp",
			"source_name":        "pyright",
			"fingerprint":        "diagfp",
			"generated_at":       "2026-05-04T00:00:00Z",
		},
	})
	linkJSON, _ := json.Marshal(map[string]any{
		"symbol_id":     21,
		"path":          "src/app.py",
		"symbol":        "ExportService",
		"kind":          "class",
		"line_start":    1,
		"line_end":      1,
		"link_strategy": "diagnostic_start_line_exact_symbol_anchor",
	})

	fakeConn := &fakeSemanticConn{
		queryRows: []pgx.Row{
			fakeSemanticBaselineRowValues(
				"diag_fixture",
				"lsp",
				"pyright",
				"batchfp",
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
			fakeSemanticJSONRow(linkJSON),
		},
	}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()

	result, err := ReadDiagnostics(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("expected one diagnostic row, got %#v", result)
	}
	if result.Diagnostics[0].LinkedSymbol == nil || result.Diagnostics[0].LinkedSymbol.Symbol != "ExportService" {
		t.Fatalf("expected conservative symbol link, got %#v", result.Diagnostics[0])
	}
}

func TestReadDiagnosticsLeavesUnmatchedRowsUnlinked(t *testing.T) {
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
			"range_start_line":   9,
			"range_start_column": 1,
			"range_end_line":     9,
			"range_end_column":   5,
			"severity":           "error",
			"message":            "Out-of-range diagnostic.",
			"source_kind":        "lsp",
			"source_name":        "pyright",
			"fingerprint":        "diagfp2",
			"generated_at":       "2026-05-04T00:00:00Z",
		},
	})

	fakeConn := &fakeSemanticConn{
		queryRows: []pgx.Row{
			fakeSemanticBaselineRowValues(
				"diag_fixture",
				"lsp",
				"pyright",
				"batchfp",
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
			fakeSemanticJSONRow([]byte("null")),
		},
	}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()

	result, err := ReadDiagnostics(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("expected one diagnostic row, got %#v", result)
	}
	if result.Diagnostics[0].LinkedSymbol != nil {
		t.Fatalf("expected unmatched diagnostic to stay unlinked, got %#v", result.Diagnostics[0])
	}
}

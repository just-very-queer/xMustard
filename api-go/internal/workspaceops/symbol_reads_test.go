package workspaceops

import (
	"context"
	"encoding/json"
	"os"
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

func TestLspDocumentSymbolsSmokeWithFakeServer(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	result, err := ReadDocumentSymbols(dataDir, workspaceID, "src/app.py")
	if err != nil {
		t.Fatalf("read document symbols: %v", err)
	}
	if result.EvidenceSource != "rust_lsp_document_symbols" || result.SymbolSource != "lsp" {
		t.Fatalf("expected Rust-normalized LSP document symbols, got %#v", result)
	}
	if len(result.Symbols) != 2 || result.Symbols[0].Symbol != "OnlyFromLSP" || result.Symbols[1].EnclosingScope == nil {
		t.Fatalf("unexpected document symbols: %#v", result)
	}
	methods := readFakeLSPMethods(t, logPath)
	if countMethod(methods, "initialize") != 1 || countMethod(methods, "textDocument/documentSymbol") != 1 {
		t.Fatalf("expected initialize + documentSymbol traffic, got %#v", methods)
	}
}

func TestLspDocumentSymbolsDoesNotRequirePostgresOrMaterializedSymbols(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	originalConnect := connectSemanticPostgres
	connectSemanticPostgres = func(ctx context.Context, dsn string) (semanticMaterializationConn, error) {
		t.Fatalf("unexpected Postgres connect in live LSP document-symbols path")
		return nil, nil
	}
	defer func() {
		connectSemanticPostgres = originalConnect
	}()

	result, err := ReadDocumentSymbols(dataDir, workspaceID, "src/app.py")
	if err != nil {
		t.Fatalf("read document symbols: %v", err)
	}
	if len(result.Symbols) == 0 || result.Symbols[0].Symbol != "OnlyFromLSP" {
		t.Fatalf("expected fake LSP symbols, got %#v", result)
	}
}

func TestReadDocumentSymbolsUsesLSPResponseNotStaticParsing(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	if err := os.WriteFile(filepath.Join(repoRoot, "src", "app.py"), []byte("value = 1\n"), 0o644); err != nil {
		t.Fatalf("rewrite fixture file: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	result, err := ReadDocumentSymbols(dataDir, workspaceID, "src/app.py")
	if err != nil {
		t.Fatalf("read document symbols: %v", err)
	}
	if len(result.Symbols) != 2 || result.Symbols[0].Symbol != "OnlyFromLSP" {
		t.Fatalf("expected symbol only available from fake LSP response, got %#v", result)
	}
	for _, symbol := range result.Symbols {
		if symbol.EvidenceSource != "rust_lsp_document_symbol" {
			t.Fatalf("expected LSP symbol evidence, got %#v", result.Symbols)
		}
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

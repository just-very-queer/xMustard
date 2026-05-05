package workspaceops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	semanticBaselineJSON, _ := json.Marshal(map[string]any{
		"index_run_id":      "semidx_fixture",
		"index_fingerprint": "semfp",
		"surface":           "cli",
		"strategy":          "paths",
		"covered_paths":     []string{"src/app.py"},
	})
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
			"link_status":        "linked",
			"linked_symbol": map[string]any{
				"symbol_id":        21,
				"path":             "src/app.py",
				"symbol":           "ExportService",
				"kind":             "class",
				"line_start":       1,
				"line_end":         1,
				"signature_text":   "class ExportService:",
				"link_strategy":    "diagnostic_start_line_exact_symbol_anchor",
				"evidence_source":  "rust_diagnostic_symbol_link",
				"selection_reason": "The diagnostic starts on exactly one durable symbol anchor line.",
			},
			"link_context": map[string]any{
				"candidate_count":  1,
				"evidence_source":  "rust_diagnostic_symbol_link",
				"selection_reason": "The diagnostic starts on exactly one durable symbol anchor line.",
				"candidates": []map[string]any{{
					"symbol_id":      21,
					"path":           "src/app.py",
					"symbol":         "ExportService",
					"kind":           "class",
					"line_start":     1,
					"line_end":       1,
					"signature_text": "class ExportService:",
				}},
				"generated_at": "2026-05-04T00:00:00Z",
			},
			"generated_at": "2026-05-04T00:00:00Z",
		},
	})

	fakeConn := &fakeSemanticConn{
		queryRows: []pgx.Row{
			fakeDiagnosticBaselineReadRow(semanticBaselineJSON, countsJSON),
			fakeSemanticJSONRow(diagnosticsJSON),
		},
	}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()

	result, err := ReadDiagnostics(dataDir, workspaceID, "diag_fixture")
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("expected one diagnostic row, got %#v", result)
	}
	if result.Baseline == nil || result.Baseline.SemanticBaseline == nil || result.Baseline.SemanticBaseline.IndexRunID != "semidx_fixture" {
		t.Fatalf("expected historical semantic baseline anchor, got %#v", result.Baseline)
	}
	if result.Baseline.ReplayArchive == nil || result.Baseline.ReplayArchive.RawPayloadSHA256 != "fixturepayloadsha256" {
		t.Fatalf("expected archived raw diagnostic replay payload, got %#v", result.Baseline)
	}
	if result.Baseline.ReplayArchive.ServerProvenance["server_id"] != "pyright" {
		t.Fatalf("expected archived server provenance, got %#v", result.Baseline.ReplayArchive)
	}
	if result.Diagnostics[0].LinkedSymbol == nil || result.Diagnostics[0].LinkedSymbol.Symbol != "ExportService" {
		t.Fatalf("expected conservative symbol link, got %#v", result.Diagnostics[0])
	}
	if result.Diagnostics[0].LinkContext == nil || result.Diagnostics[0].LinkContext.CandidateCount != 1 {
		t.Fatalf("expected archived link context, got %#v", result.Diagnostics[0])
	}
	if result.Diagnostics[0].LinkedSymbol.LinkStrategy != "diagnostic_start_line_exact_symbol_anchor" || result.Diagnostics[0].LinkedSymbol.EvidenceSource != "rust_diagnostic_symbol_link" {
		t.Fatalf("expected Rust-owned link provenance, got %#v", result.Diagnostics[0].LinkedSymbol)
	}
	if len(fakeConn.querySQL) != 2 {
		t.Fatalf("expected durable replay read without candidate relinking, got %#v", fakeConn.querySQL)
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
			"link_status":        "evaluated_unlinked",
			"generated_at":       "2026-05-04T00:00:00Z",
		},
	})

	fakeConn := &fakeSemanticConn{
		queryRows: []pgx.Row{
			fakeDiagnosticBaselineReadRow([]byte("{}"), countsJSON),
			fakeSemanticJSONRow(diagnosticsJSON),
		},
	}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()

	result, err := ReadDiagnostics(dataDir, workspaceID, "diag_fixture")
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("expected one diagnostic row, got %#v", result)
	}
	if result.Diagnostics[0].LinkedSymbol != nil {
		t.Fatalf("expected unmatched diagnostic to stay unlinked, got %#v", result.Diagnostics[0])
	}
	if result.Diagnostics[0].LinkStatus != "evaluated_unlinked" {
		t.Fatalf("expected persisted evaluated_unlinked status, got %#v", result.Diagnostics[0])
	}
	if result.Diagnostics[0].LinkContext != nil {
		t.Fatalf("expected no archived link context in this fixture, got %#v", result.Diagnostics[0].LinkContext)
	}
}

func TestReadDiagnosticsLeavesAmbiguousSymbolMatchesUnlinked(t *testing.T) {
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
			"message":            "Ambiguous diagnostic.",
			"source_kind":        "lsp",
			"source_name":        "pyright",
			"fingerprint":        "diagfp3",
			"link_status":        "symbols_unavailable",
			"generated_at":       "2026-05-04T00:00:00Z",
		},
	})

	fakeConn := &fakeSemanticConn{
		queryRows: []pgx.Row{
			fakeDiagnosticBaselineReadRow([]byte("{}"), countsJSON),
			fakeSemanticJSONRow(diagnosticsJSON),
		},
	}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()

	result, err := ReadDiagnostics(dataDir, workspaceID, "diag_fixture")
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("expected one diagnostic row, got %#v", result)
	}
	if result.Diagnostics[0].LinkedSymbol != nil {
		t.Fatalf("expected ambiguous diagnostic to stay unlinked, got %#v", result.Diagnostics[0])
	}
	if result.Diagnostics[0].LinkStatus != "symbols_unavailable" {
		t.Fatalf("expected persisted symbols_unavailable status, got %#v", result.Diagnostics[0])
	}
	if result.Diagnostics[0].LinkContext != nil {
		t.Fatalf("expected missing archived link context when symbols were unavailable, got %#v", result.Diagnostics[0].LinkContext)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "durable link replay is unavailable") {
		t.Fatalf("expected readiness warning, got %#v", result.Warnings)
	}
}

func fakeDiagnosticBaselineReadRow(semanticBaselineJSON []byte, countsJSON []byte) fakeSemanticRow {
	rawPayloadJSON, _ := json.Marshal(map[string]any{
		"diagnostics": []map[string]any{{
			"message": "Example LSP diagnostic.",
		}},
	})
	serverProvenanceJSON, _ := json.Marshal(map[string]any{
		"source_mode": "input_file",
		"server_id":   "pyright",
		"input_path":  "diagnostics.json",
	})
	replayWarningsJSON, _ := json.Marshal([]string{})
	sha := "fixturepayloadsha256"
	readiness := "raw_payload_and_server_provenance_archived"
	return fakeSemanticBaselineRowValues(
		"diag_fixture",
		"lsp",
		"pyright",
		"batchfp",
		rawPayloadJSON,
		&sha,
		123,
		serverProvenanceJSON,
		"diagnostics.normalized.v1",
		&readiness,
		replayWarningsJSON,
		semanticBaselineJSON,
		(*string)(nil),
		0,
		false,
		1,
		countsJSON,
		"diagnostics.json",
		"xmustard",
		"2026-05-04T00:00:00Z",
	)
}

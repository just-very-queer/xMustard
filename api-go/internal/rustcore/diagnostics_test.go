package rustcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeDiagnosticsPayload(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("create src dir: %v", err)
	}
	appPath := filepath.Join(srcDir, "app.py")
	if err := os.WriteFile(appPath, []byte("value = missing\n"), 0o644); err != nil {
		t.Fatalf("write app fixture: %v", err)
	}

	payload := []byte(`{
		"uri": "file://` + filepath.ToSlash(appPath) + `",
		"diagnostics": [{
			"range": {
				"start": {"line": 0, "character": 8},
				"end": {"line": 0, "character": 15}
			},
			"severity": 1,
			"code": "XMUSTARD_FAKE",
			"message": "Fake live diagnostic."
		}]
	}`)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := NormalizeDiagnosticsPayload(ctx, "workspace-1", root, payload, "lsp", "fake-pyright")
	if err != nil {
		t.Fatalf("normalize diagnostics payload: %v", err)
	}
	if result.SourceKind != "lsp" || result.SourceName != "fake-pyright" {
		t.Fatalf("unexpected diagnostics provenance: %#v", result)
	}
	if result.DiagnosticCount != 1 || result.Diagnostics[0].Path != "src/app.py" {
		t.Fatalf("unexpected diagnostics result: %#v", result)
	}
	if result.Diagnostics[0].RangeStartLine != 1 || result.Diagnostics[0].RangeStartColumn != 9 || result.Diagnostics[0].Fingerprint == "" {
		t.Fatalf("expected linkable one-based diagnostic coordinates, got %#v", result.Diagnostics[0])
	}
}

func TestArchiveDiagnosticsPayloadUsesRustReplayContract(t *testing.T) {
	root := t.TempDir()
	inputPath := filepath.Join(root, "diagnostics.json")
	payload := []byte(`{
		"path": "src/app.py",
		"diagnostics": [{
			"range": {
				"start": {"line": 0, "character": 8},
				"end": {"line": 0, "character": 15}
			},
			"severity": 1,
			"message": "Fake live diagnostic."
		}]
	}`)
	if err := os.WriteFile(inputPath, payload, 0o644); err != nil {
		t.Fatalf("write diagnostics fixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := ArchiveDiagnosticsPayload(ctx, "workspace-1", inputPath, "lsp", "fake-pyright", map[string]any{
		"source_mode":    "input_file",
		"server_id":      "fake-pyright",
		"server_command": []string{"/usr/local/bin/fake-pyright", "--stdio"},
	})
	if err != nil {
		t.Fatalf("archive diagnostics payload: %v", err)
	}
	if result.RawPayloadSHA256 == "" || len(result.RawPayloadSHA256) != 64 {
		t.Fatalf("expected replay payload sha256, got %#v", result)
	}
	if result.RawPayloadBytes != len(payload) {
		t.Fatalf("expected raw payload byte count, got %#v", result)
	}
	if result.ServerProvenance["server_id"] != "fake-pyright" || result.ReplayReadiness != "raw_payload_and_server_provenance_archived" {
		t.Fatalf("expected server provenance archive, got %#v", result)
	}
}

func TestArchiveDiagnosticsPayloadWarnsWithoutResolvedServerCommand(t *testing.T) {
	root := t.TempDir()
	inputPath := filepath.Join(root, "diagnostics.json")
	payload := []byte(`{
		"path": "src/app.py",
		"diagnostics": [{
			"range": {
				"start": {"line": 0, "character": 8},
				"end": {"line": 0, "character": 15}
			},
			"severity": 1,
			"message": "Fake live diagnostic."
		}]
	}`)
	if err := os.WriteFile(inputPath, payload, 0o644); err != nil {
		t.Fatalf("write diagnostics fixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := ArchiveDiagnosticsPayload(ctx, "workspace-1", inputPath, "lsp", "fake-pyright", map[string]any{
		"source_mode": "input_file",
		"server_id":   "fake-pyright",
	})
	if err != nil {
		t.Fatalf("archive diagnostics payload: %v", err)
	}
	if result.ReplayReadiness != "raw_payload_archived_with_provenance_warnings" {
		t.Fatalf("expected provenance warning readiness, got %#v", result)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("expected archive warnings, got %#v", result)
	}
}

func TestLinkDiagnosticSymbolUsesRustContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	lineStart := 1
	lineEnd := 10
	signature := "class ExportService:"
	result, err := LinkDiagnosticSymbol(ctx, "workspace-1", "src/app.py", 1, 1, "diagfp", []DiagnosticSymbolCandidate{
		{
			SymbolID:      21,
			Path:          "src/app.py",
			Symbol:        "ExportService",
			Kind:          "class",
			LineStart:     &lineStart,
			LineEnd:       &lineEnd,
			SignatureText: &signature,
		},
	})
	if err != nil {
		t.Fatalf("link diagnostic symbol: %v", err)
	}
	if result.LinkedSymbol == nil || result.LinkedSymbol.Symbol != "ExportService" {
		t.Fatalf("expected Rust-linked symbol, got %#v", result)
	}
	if result.EvidenceSource != "rust_diagnostic_symbol_link" || result.LinkedSymbol.LinkStrategy != "diagnostic_start_line_exact_symbol_anchor" {
		t.Fatalf("expected Rust link provenance, got %#v", result)
	}
}

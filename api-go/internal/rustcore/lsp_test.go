package rustcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeLSPDocumentSymbols(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("create src dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "app.py"), []byte("value = 1\n"), 0o644); err != nil {
		t.Fatalf("write app fixture: %v", err)
	}

	payload := []byte(`[
		{
			"name": "OnlyFromBridgeLSP",
			"kind": 12,
			"location": {
				"uri": "file://` + filepath.ToSlash(filepath.Join(srcDir, "app.py")) + `",
				"range": {
					"start": {"line": 0, "character": 0},
					"end": {"line": 1, "character": 0}
				}
			}
		}
	]`)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := NormalizeLSPDocumentSymbols(ctx, "workspace-1", root, "src/app.py", "fake-pyright", payload)
	if err != nil {
		t.Fatalf("normalize LSP document symbols: %v", err)
	}
	if result.EvidenceSource != "rust_lsp_document_symbols" || result.SymbolSource != "lsp" {
		t.Fatalf("unexpected document-symbols provenance: %#v", result)
	}
	if len(result.Symbols) != 1 || result.Symbols[0].Symbol != "OnlyFromBridgeLSP" {
		t.Fatalf("unexpected document-symbols result: %#v", result)
	}
}

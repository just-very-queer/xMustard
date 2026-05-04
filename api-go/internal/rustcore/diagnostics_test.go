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
}

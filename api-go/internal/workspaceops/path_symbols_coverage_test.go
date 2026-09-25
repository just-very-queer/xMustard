package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Stage 3 integration: Rust path-symbols reports total_symbols / symbols_truncated so
// a display limit never implies the file's symbols were all indexed. Go decoded the
// result into a struct without those fields and silently dropped them.
func TestPathSymbolsCarriesRustTruncationFields(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, "wsSyms", root)
	if err := os.WriteFile(filepath.Join(root, "many.rs"), []byte("fn a() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(t.TempDir(), "xmustard-core")
	script := `#!/bin/sh
printf '{"workspace_id":"wsSyms","path":"many.rs","symbol_source":"tree_sitter","evidence_source":"x","selection_reason":"x","symbols":[],"warnings":["Showing 64 of 70 extracted symbols."],"generated_at":"t","total_symbols":70,"symbols_truncated":true}'
`
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	res, err := ReadPathSymbols(dir, "wsSyms", "many.rs")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"total_symbols":70`) || !strings.Contains(string(b), `"symbols_truncated":true`) {
		t.Fatalf("truncation fields dropped: %s", b)
	}
}

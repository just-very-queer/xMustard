package workspaceops

import (
	"encoding/json"
	"strings"
	"testing"

	"xmustard/api-go/internal/rustcore"
)

// Cross-language audit: the LSP document-symbols path reused the extended
// PathSymbolsResult and reported total_symbols 0 beside a non-empty symbol list.
// Extraction-completeness metadata belongs to the Rust extractor path only.
func TestDocumentSymbolsOmitExtractorCompletenessFields(t *testing.T) {
	res := documentSymbolsResult(&rustcore.DocumentSymbolsResult{
		WorkspaceID: "ws", Path: "a.go", SymbolSource: "lsp",
		Symbols: []rustcore.DocumentSymbolRecord{{Path: "a.go", Symbol: "A", Kind: "function"}, {Path: "a.go", Symbol: "B", Kind: "function"}},
	})
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "total_symbols") || strings.Contains(string(b), "symbols_truncated") {
		t.Fatalf("LSP result must not carry extractor completeness fields: %s", b)
	}
}

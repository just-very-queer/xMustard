package workspaceops

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLspDefinitionUsesWorkspaceScopedSession(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	first, err := GoToDefinition(dataDir, workspaceID, "src/app.py", 1, 7)
	if err != nil {
		t.Fatalf("first go-to-definition: %v", err)
	}
	second, err := GoToDefinition(dataDir, workspaceID, "src/app.py", 2, 10)
	if err != nil {
		t.Fatalf("second go-to-definition: %v", err)
	}
	if first.EvidenceSource != "rust_lsp_definition" || second.EvidenceSource != "rust_lsp_definition" {
		t.Fatalf("expected LSP evidence source, got first=%#v second=%#v", first, second)
	}

	methods := readFakeLSPMethods(t, logPath)
	if countMethod(methods, "initialize") != 1 {
		t.Fatalf("expected one initialize for reused session, got %#v", methods)
	}
	if countMethod(methods, "textDocument/definition") != 2 {
		t.Fatalf("expected two definition requests, got %#v", methods)
	}
}

func TestLspDefinitionAndDocumentSymbolsReuseWorkspaceScopedSession(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	if _, err := GoToDefinition(dataDir, workspaceID, "src/app.py", 1, 7); err != nil {
		t.Fatalf("go-to-definition: %v", err)
	}
	symbols, err := ReadDocumentSymbols(dataDir, workspaceID, "src/app.py")
	if err != nil {
		t.Fatalf("document-symbols: %v", err)
	}
	if symbols.EvidenceSource != "rust_lsp_document_symbols" {
		t.Fatalf("expected LSP document-symbols evidence, got %#v", symbols)
	}

	methods := readFakeLSPMethods(t, logPath)
	if countMethod(methods, "initialize") != 1 {
		t.Fatalf("expected one initialize for reused session, got %#v", methods)
	}
	if countMethod(methods, "textDocument/definition") != 1 || countMethod(methods, "textDocument/documentSymbol") != 1 {
		t.Fatalf("expected definition + documentSymbol traffic, got %#v", methods)
	}
}

func TestLspDefinitionAndReferencesReuseWorkspaceScopedSession(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	if _, err := GoToDefinition(dataDir, workspaceID, "src/app.py", 1, 7); err != nil {
		t.Fatalf("go-to-definition: %v", err)
	}
	refs, err := FindReferences(dataDir, workspaceID, "src/app.py", 1, 7, true)
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	if refs.EvidenceSource != "rust_lsp_references" {
		t.Fatalf("expected LSP references evidence, got %#v", refs)
	}

	methods := readFakeLSPMethods(t, logPath)
	if countMethod(methods, "initialize") != 1 {
		t.Fatalf("expected one initialize for reused session, got %#v", methods)
	}
	if countMethod(methods, "textDocument/definition") != 1 || countMethod(methods, "textDocument/references") != 1 {
		t.Fatalf("expected definition + references traffic, got %#v", methods)
	}
}

func TestLspLiveDiagnosticsUseWorkspaceScopedSession(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	result, err := ReadLiveDiagnostics(dataDir, workspaceID, "src/app.py")
	if err != nil {
		t.Fatalf("live diagnostics: %v", err)
	}
	if result.SourceKind != "lsp" || result.SourceName != "fake-pyright" {
		t.Fatalf("expected live LSP diagnostics source, got %#v", result)
	}
	if result.DiagnosticCount != 1 {
		t.Fatalf("expected one live diagnostic, got %#v", result)
	}
	diagnostic := result.Diagnostics[0]
	if diagnostic.Path != "src/app.py" || diagnostic.Severity != "error" || diagnostic.RuleCode == nil || *diagnostic.RuleCode != "XMUSTARD_FAKE" {
		t.Fatalf("unexpected live diagnostic: %#v", diagnostic)
	}

	methods := readFakeLSPMethods(t, logPath)
	if countMethod(methods, "initialize") != 1 || countMethod(methods, "textDocument/didOpen") != 1 {
		t.Fatalf("expected initialize + didOpen session traffic, got %#v", methods)
	}
	if countMethod(methods, "textDocument/definition") != 0 || countMethod(methods, "textDocument/documentSymbol") != 0 || countMethod(methods, "textDocument/references") != 0 {
		t.Fatalf("live diagnostics should not masquerade as request/response symbol traffic, got %#v", methods)
	}
}

func TestLspLiveDiagnosticsDoNotRequirePostgresOrMaterializedSymbols(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	originalConnect := connectSemanticPostgres
	connectSemanticPostgres = func(ctx context.Context, dsn string) (semanticMaterializationConn, error) {
		t.Fatalf("unexpected Postgres connect in live LSP diagnostics path")
		return nil, nil
	}
	defer func() {
		connectSemanticPostgres = originalConnect
	}()

	result, err := ReadLiveDiagnostics(dataDir, workspaceID, "src/app.py")
	if err != nil {
		t.Fatalf("live diagnostics: %v", err)
	}
	if result.DiagnosticCount != 1 || result.Diagnostics[0].SourceName != "fake-pyright" {
		t.Fatalf("expected fake LSP diagnostic, got %#v", result)
	}
}

func TestLspDefinitionDoesNotRequirePostgresOrMaterializedSymbols(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	originalConnect := connectSemanticPostgres
	connectSemanticPostgres = func(ctx context.Context, dsn string) (semanticMaterializationConn, error) {
		t.Fatalf("unexpected Postgres connect in live LSP definition path")
		return nil, nil
	}
	defer func() {
		connectSemanticPostgres = originalConnect
	}()

	result, err := GoToDefinition(dataDir, workspaceID, "src/app.py", 1, 7)
	if err != nil {
		t.Fatalf("go-to-definition: %v", err)
	}
	if result.DefinitionCount != 1 || result.Definitions[0].Path != "src/definition.py" {
		t.Fatalf("unexpected definition result: %#v", result)
	}
}

func TestLspReferencesDoesNotRequirePostgresOrMaterializedSymbols(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	originalConnect := connectSemanticPostgres
	connectSemanticPostgres = func(ctx context.Context, dsn string) (semanticMaterializationConn, error) {
		t.Fatalf("unexpected Postgres connect in live LSP references path")
		return nil, nil
	}
	defer func() {
		connectSemanticPostgres = originalConnect
	}()

	result, err := FindReferences(dataDir, workspaceID, "src/app.py", 1, 7, true)
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	if result.ReferenceCount != 2 || result.References[0].Path != "src/app.py" {
		t.Fatalf("unexpected references result: %#v", result)
	}
}

func TestLspDefinitionReturnsExplicitUnavailableWhenServerBootstrapFails(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
	originalResolver := resolveLSPServerForPath
	resolveLSPServerForPath = func(rootPath string, relativePath string) (*lspServerConfig, error) {
		return &lspServerConfig{
			ServerID:   "missing-lsp",
			LanguageID: "python",
			Command:    []string{"/definitely/missing-lsp-binary"},
		}, nil
	}
	defer func() {
		resolveLSPServerForPath = originalResolver
	}()

	_, err := GoToDefinition(dataDir, workspaceID, "src/app.py", 1, 7)
	if err == nil {
		t.Fatal("expected LSP unavailable error")
	}
	if !strings.Contains(err.Error(), "LSP unavailable") {
		t.Fatalf("expected explicit LSP unavailable error, got %v", err)
	}
}

func TestLspReferencesSmokeWithFakeServer(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	result, err := FindReferences(dataDir, workspaceID, "src/app.py", 1, 7, true)
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	if result.EvidenceSource != "rust_lsp_references" || result.SourceName != "fake-pyright" {
		t.Fatalf("expected Rust-normalized LSP references, got %#v", result)
	}
	if result.ReferenceCount != 2 {
		t.Fatalf("expected two references, got %#v", result)
	}
	if result.References[1].LineStart != 6 || result.References[1].ColumnStart != 3 {
		t.Fatalf("unexpected reference location: %#v", result.References[1])
	}
	methods := readFakeLSPMethods(t, logPath)
	if countMethod(methods, "initialize") != 1 || countMethod(methods, "textDocument/references") != 1 {
		t.Fatalf("expected initialize + references traffic, got %#v", methods)
	}
}

func TestLspDefinitionSmokeWithFakeServer(t *testing.T) {
	defer closeAllLSPSessions()
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "fake-lsp.log")
	restore := stubLSPServerResolver(t, repoRoot, logPath)
	defer restore()

	result, err := GoToDefinition(dataDir, workspaceID, "src/app.py", 1, 7)
	if err != nil {
		t.Fatalf("go-to-definition: %v", err)
	}
	if result.EvidenceSource != "rust_lsp_definition" || result.SourceName != "fake-pyright" {
		t.Fatalf("expected Rust-normalized LSP result, got %#v", result)
	}
	if result.DefinitionCount != 1 {
		t.Fatalf("expected one definition, got %#v", result)
	}
	if result.Definitions[0].TargetLineStart != 3 || result.Definitions[0].TargetColumnStart != 1 {
		t.Fatalf("unexpected definition location: %#v", result.Definitions[0])
	}
	methods := readFakeLSPMethods(t, logPath)
	if countMethod(methods, "initialize") != 1 || countMethod(methods, "textDocument/definition") != 1 {
		t.Fatalf("expected initialize + definition traffic, got %#v", methods)
	}
}

func TestWorkspaceOpsFakeLSPServer(t *testing.T) {
	if os.Getenv("XMUSTARD_FAKE_LSP") != "1" {
		return
	}
	logPath := os.Getenv("XMUSTARD_FAKE_LSP_LOG")
	if strings.TrimSpace(logPath) == "" {
		os.Exit(2)
	}
	if err := runFakeLSPServer(logPath); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}

func stubLSPServerResolver(t *testing.T, repoRoot string, logPath string) func() {
	t.Helper()
	originalResolver := resolveLSPServerForPath
	resolveLSPServerForPath = func(rootPath string, relativePath string) (*lspServerConfig, error) {
		if filepath.Clean(rootPath) != filepath.Clean(repoRoot) {
			t.Fatalf("unexpected rootPath %s", rootPath)
		}
		return &lspServerConfig{
			ServerID:   "fake-pyright",
			LanguageID: "python",
			Command: []string{
				os.Args[0],
				"-test.run=TestWorkspaceOpsFakeLSPServer",
			},
		}, nil
	}
	originalTTL := lspSessionIdleTTL
	lspSessionIdleTTL = time.Hour
	originalCommandEnv := os.Getenv("XMUSTARD_FAKE_LSP_LOG")
	originalMarker := os.Getenv("XMUSTARD_FAKE_LSP")
	originalRepoRoot := os.Getenv("XMUSTARD_FAKE_LSP_REPO_ROOT")
	_ = os.Setenv("XMUSTARD_FAKE_LSP", "1")
	_ = os.Setenv("XMUSTARD_FAKE_LSP_LOG", logPath)
	_ = os.Setenv("XMUSTARD_FAKE_LSP_REPO_ROOT", repoRoot)
	return func() {
		resolveLSPServerForPath = originalResolver
		lspSessionIdleTTL = originalTTL
		if originalCommandEnv == "" {
			_ = os.Unsetenv("XMUSTARD_FAKE_LSP_LOG")
		} else {
			_ = os.Setenv("XMUSTARD_FAKE_LSP_LOG", originalCommandEnv)
		}
		if originalMarker == "" {
			_ = os.Unsetenv("XMUSTARD_FAKE_LSP")
		} else {
			_ = os.Setenv("XMUSTARD_FAKE_LSP", originalMarker)
		}
		if originalRepoRoot == "" {
			_ = os.Unsetenv("XMUSTARD_FAKE_LSP_REPO_ROOT")
		} else {
			_ = os.Setenv("XMUSTARD_FAKE_LSP_REPO_ROOT", originalRepoRoot)
		}
		closeAllLSPSessions()
	}
}

func readFakeLSPMethods(t *testing.T, logPath string) []string {
	t.Helper()
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake LSP log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	methods := []string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			methods = append(methods, trimmed)
		}
	}
	return methods
}

func countMethod(items []string, target string) int {
	count := 0
	for _, item := range items {
		if item == target {
			count++
		}
	}
	return count
}

func runFakeLSPServer(logPath string) error {
	handle, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer handle.Close()
	repoRoot := strings.TrimSpace(os.Getenv("XMUSTARD_FAKE_LSP_REPO_ROOT"))
	if repoRoot == "" {
		return fmt.Errorf("XMUSTARD_FAKE_LSP_REPO_ROOT is required")
	}

	reader := bufio.NewReader(os.Stdin)
	writer := os.Stdout
	for {
		payload, err := readLSPFrame(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var message map[string]any
		if err := json.Unmarshal(payload, &message); err != nil {
			return err
		}
		method, _ := message["method"].(string)
		if method != "" {
			if _, err := handle.WriteString(method + "\n"); err != nil {
				return err
			}
		}
		switch method {
		case "initialize":
			if err := writeFakeLSPResponse(writer, message["id"], map[string]any{
				"capabilities": map[string]any{
					"definitionProvider":     true,
					"documentSymbolProvider": true,
					"referencesProvider":     true,
				},
			}); err != nil {
				return err
			}
		case "initialized":
			continue
		case "textDocument/didOpen", "textDocument/didChange":
			if err := writeFakeLSPNotification(writer, "textDocument/publishDiagnostics", map[string]any{
				"uri": fileURL(filepath.Join(repoRoot, "src", "app.py")),
				"diagnostics": []map[string]any{
					{
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 6},
							"end":   map[string]any{"line": 0, "character": 19},
						},
						"severity": 1,
						"code":     "XMUSTARD_FAKE",
						"source":   "fake-pyright",
						"message":  "Fake live diagnostic from publishDiagnostics.",
					},
				},
			}); err != nil {
				return err
			}
			continue
		case "textDocument/definition":
			response := []map[string]any{
				{
					"uri": fileURL(filepath.Join(repoRoot, "src", "definition.py")),
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 0},
						"end":   map[string]any{"line": 4, "character": 12},
					},
				},
			}
			if err := writeFakeLSPResponse(writer, message["id"], response); err != nil {
				return err
			}
		case "textDocument/references":
			response := []map[string]any{
				{
					"uri": fileURL(filepath.Join(repoRoot, "src", "app.py")),
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 6},
						"end":   map[string]any{"line": 0, "character": 19},
					},
				},
				{
					"uri": fileURL(filepath.Join(repoRoot, "src", "references.py")),
					"range": map[string]any{
						"start": map[string]any{"line": 5, "character": 2},
						"end":   map[string]any{"line": 5, "character": 15},
					},
				},
			}
			if err := writeFakeLSPResponse(writer, message["id"], response); err != nil {
				return err
			}
		case "textDocument/documentSymbol":
			response := []map[string]any{
				{
					"name": "OnlyFromLSP",
					"kind": 5,
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 0},
						"end":   map[string]any{"line": 3, "character": 0},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 0, "character": 6},
						"end":   map[string]any{"line": 0, "character": 17},
					},
					"children": []map[string]any{
						{
							"name": "run",
							"kind": 6,
							"range": map[string]any{
								"start": map[string]any{"line": 1, "character": 4},
								"end":   map[string]any{"line": 2, "character": 8},
							},
							"selectionRange": map[string]any{
								"start": map[string]any{"line": 1, "character": 8},
								"end":   map[string]any{"line": 1, "character": 11},
							},
						},
					},
				},
			}
			if err := writeFakeLSPResponse(writer, message["id"], response); err != nil {
				return err
			}
		case "shutdown":
			if err := writeFakeLSPResponse(writer, message["id"], nil); err != nil {
				return err
			}
		case "exit":
			return nil
		default:
			if message["id"] != nil {
				if err := writeFakeLSPResponse(writer, message["id"], nil); err != nil {
					return err
				}
			}
		}
	}
}

func writeFakeLSPResponse(writer io.Writer, id any, result any) error {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err = writer.Write(payload)
	return err
}

func writeFakeLSPNotification(writer io.Writer, method string, params any) error {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err = writer.Write(payload)
	return err
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.Clean(path)}).String()
}

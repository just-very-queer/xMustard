package workspaceops

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xmustard/api-go/internal/rustcore"
)

type lspServerConfig struct {
	ServerID   string
	LanguageID string
	Command    []string
}

type lspSessionKey struct {
	WorkspaceID string
	ServerID    string
}

type lspSession struct {
	workspaceID   string
	rootPath      string
	config        lspServerConfig
	command       *exec.Cmd
	stdin         io.WriteCloser
	stdout        *bufio.Reader
	stderrPath    string
	initialized   bool
	lastUsedAt    time.Time
	nextRequestID int64

	writeMu sync.Mutex
	stateMu sync.Mutex
	initMu  sync.Mutex
	docMu   sync.Mutex
	diagMu  sync.Mutex

	openDocuments map[string]int
	diagnostics   map[string]json.RawMessage
	diagWaiters   map[string][]chan json.RawMessage

	pendingMu sync.Mutex
	pending   map[int64]chan lspResponseEnvelope

	loopErrMu sync.Mutex
	loopErr   error
	done      chan struct{}
	closeOnce sync.Once
}

type lspResponseEnvelope struct {
	result json.RawMessage
	err    error
}

type lspIncomingMessage struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      json.RawMessage        `json:"id,omitempty"`
	Method  string                 `json:"method,omitempty"`
	Params  json.RawMessage        `json:"params,omitempty"`
	Result  json.RawMessage        `json:"result,omitempty"`
	Error   *lspJSONRPCErrorRecord `json:"error,omitempty"`
}

type lspJSONRPCErrorRecord struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var resolveLSPServerForPath = defaultResolveLSPServerForPath
var lspSessions sync.Map
var lspSessionsMu sync.Mutex
var lspSessionIdleTTL = 2 * time.Minute

func GoToDefinition(dataDir string, workspaceID string, relativePath string, line int, column int) (*rustcore.DefinitionResult, error) {
	if line < 1 || column < 1 {
		return nil, fmt.Errorf("%w: line and column must be >= 1", ErrInvalidSemanticRequest)
	}
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeWorkspaceFile(workspace.RootPath, relativePath)
	if err != nil {
		return nil, err
	}
	config, err := resolveLSPServerForPath(workspace.RootPath, normalized)
	if err != nil {
		return nil, err
	}
	session, err := acquireLSPSession(dataDir, workspaceID, workspace.RootPath, config)
	if err != nil {
		return nil, err
	}
	absolutePath := filepath.Join(workspace.RootPath, filepath.FromSlash(normalized))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	payload, err := session.goToDefinition(ctx, absolutePath, line, column)
	if err != nil {
		releaseLSPSession(workspaceID, config.ServerID, session)
		return nil, err
	}
	return rustcore.NormalizeLSPDefinition(
		ctx,
		workspaceID,
		workspace.RootPath,
		normalized,
		line,
		column,
		config.ServerID,
		payload,
	)
}

func FindReferences(dataDir string, workspaceID string, relativePath string, line int, column int, includeDeclaration bool) (*rustcore.ReferencesResult, error) {
	if line < 1 || column < 1 {
		return nil, fmt.Errorf("%w: line and column must be >= 1", ErrInvalidSemanticRequest)
	}
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeWorkspaceFile(workspace.RootPath, relativePath)
	if err != nil {
		return nil, err
	}
	config, err := resolveLSPServerForPath(workspace.RootPath, normalized)
	if err != nil {
		return nil, err
	}
	session, err := acquireLSPSession(dataDir, workspaceID, workspace.RootPath, config)
	if err != nil {
		return nil, err
	}
	absolutePath := filepath.Join(workspace.RootPath, filepath.FromSlash(normalized))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	payload, err := session.references(ctx, absolutePath, line, column, includeDeclaration)
	if err != nil {
		releaseLSPSession(workspaceID, config.ServerID, session)
		return nil, err
	}
	return rustcore.NormalizeLSPReferences(
		ctx,
		workspaceID,
		workspace.RootPath,
		normalized,
		line,
		column,
		config.ServerID,
		payload,
	)
}

func LSPDocumentSymbols(dataDir string, workspaceID string, relativePath string) (*rustcore.DocumentSymbolsResult, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeWorkspaceFile(workspace.RootPath, relativePath)
	if err != nil {
		return nil, err
	}
	config, err := resolveLSPServerForPath(workspace.RootPath, normalized)
	if err != nil {
		return nil, err
	}
	session, err := acquireLSPSession(dataDir, workspaceID, workspace.RootPath, config)
	if err != nil {
		return nil, err
	}
	absolutePath := filepath.Join(workspace.RootPath, filepath.FromSlash(normalized))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	payload, err := session.documentSymbols(ctx, absolutePath)
	if err != nil {
		releaseLSPSession(workspaceID, config.ServerID, session)
		return nil, err
	}
	return rustcore.NormalizeLSPDocumentSymbols(
		ctx,
		workspaceID,
		workspace.RootPath,
		normalized,
		config.ServerID,
		payload,
	)
}

func LSPWorkspaceSymbols(dataDir string, workspaceID string, language string, query string, limit int) (*rustcore.LSPWorkspaceSymbolsResult, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	config, err := resolveLSPServerForLanguage(workspace.RootPath, language)
	if err != nil {
		return nil, err
	}
	session, err := acquireLSPSession(dataDir, workspaceID, workspace.RootPath, config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	payload, err := session.workspaceSymbols(ctx, query)
	if err != nil {
		releaseLSPSession(workspaceID, config.ServerID, session)
		return nil, err
	}
	return rustcore.NormalizeLSPWorkspaceSymbols(
		ctx,
		workspaceID,
		workspace.RootPath,
		query,
		normalizeWorkspaceSymbolLimit(limit),
		config.ServerID,
		payload,
	)
}

func acquireLSPSession(dataDir string, workspaceID string, rootPath string, config *lspServerConfig) (*lspSession, error) {
	key := lspSessionKey{WorkspaceID: workspaceID, ServerID: config.ServerID}
	lspSessionsMu.Lock()
	defer lspSessionsMu.Unlock()

	if existing, ok := loadLSPSession(key); ok {
		if existing.rootPath == rootPath && !existing.isIdle() {
			existing.touch()
			return existing, nil
		}
		existing.close()
		lspSessions.Delete(key)
	}

	session, err := startLSPSession(dataDir, workspaceID, rootPath, *config)
	if err != nil {
		return nil, err
	}
	lspSessions.Store(key, session)
	return session, nil
}

func releaseLSPSession(workspaceID string, serverID string, session *lspSession) {
	key := lspSessionKey{WorkspaceID: workspaceID, ServerID: serverID}
	lspSessionsMu.Lock()
	defer lspSessionsMu.Unlock()
	if current, ok := loadLSPSession(key); ok && current == session {
		current.close()
		lspSessions.Delete(key)
	}
}

func loadLSPSession(key lspSessionKey) (*lspSession, bool) {
	value, ok := lspSessions.Load(key)
	if !ok {
		return nil, false
	}
	session, ok := value.(*lspSession)
	if !ok {
		return nil, false
	}
	return session, true
}

func startLSPSession(dataDir string, workspaceID string, rootPath string, config lspServerConfig) (*lspSession, error) {
	if len(config.Command) == 0 {
		return nil, fmt.Errorf("%w: LSP command is required", ErrInvalidSemanticRequest)
	}
	logDir := filepath.Join(dataDir, "workspaces", workspaceID, "lsp")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	stderrPath := filepath.Join(logDir, sanitizeLSPLogName(config.ServerID)+".stderr.log")
	stderrHandle, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	command := exec.Command(config.Command[0], config.Command[1:]...)
	command.Dir = rootPath
	command.Stderr = stderrHandle

	stdin, err := command.StdinPipe()
	if err != nil {
		stderrHandle.Close()
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		stderrHandle.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		stderrHandle.Close()
		return nil, wrapLSPUnavailable(config.ServerID, fmt.Errorf("start failed: %w", err))
	}
	_ = stderrHandle.Close()

	session := &lspSession{
		workspaceID:   workspaceID,
		rootPath:      rootPath,
		config:        config,
		command:       command,
		stdin:         stdin,
		stdout:        bufio.NewReader(stdout),
		stderrPath:    stderrPath,
		lastUsedAt:    time.Now().UTC(),
		nextRequestID: 1,
		openDocuments: map[string]int{},
		diagnostics:   map[string]json.RawMessage{},
		diagWaiters:   map[string][]chan json.RawMessage{},
		pending:       map[int64]chan lspResponseEnvelope{},
		done:          make(chan struct{}),
	}
	go session.readLoop()
	return session, nil
}

func (session *lspSession) goToDefinition(ctx context.Context, absolutePath string, line int, column int) (json.RawMessage, error) {
	session.touch()
	if err := session.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	if err := session.syncDocument(absolutePath); err != nil {
		return nil, err
	}
	uri := fileURIForPath(absolutePath)
	return session.request(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position": map[string]any{
			"line":      line - 1,
			"character": column - 1,
		},
	})
}

func (session *lspSession) documentSymbols(ctx context.Context, absolutePath string) (json.RawMessage, error) {
	session.touch()
	if err := session.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	if err := session.syncDocument(absolutePath); err != nil {
		return nil, err
	}
	uri := fileURIForPath(absolutePath)
	return session.request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

func (session *lspSession) workspaceSymbols(ctx context.Context, query string) (json.RawMessage, error) {
	session.touch()
	if err := session.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	return session.request(ctx, "workspace/symbol", map[string]any{
		"query": strings.TrimSpace(query),
	})
}

func (session *lspSession) references(ctx context.Context, absolutePath string, line int, column int, includeDeclaration bool) (json.RawMessage, error) {
	session.touch()
	if err := session.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	if err := session.syncDocument(absolutePath); err != nil {
		return nil, err
	}
	uri := fileURIForPath(absolutePath)
	return session.request(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position": map[string]any{
			"line":      line - 1,
			"character": column - 1,
		},
		"context": map[string]any{
			"includeDeclaration": includeDeclaration,
		},
	})
}

func (session *lspSession) liveDiagnostics(ctx context.Context, absolutePath string) (json.RawMessage, error) {
	session.touch()
	if err := session.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	uri := fileURIForPath(absolutePath)
	session.clearDiagnostics(uri)
	if err := session.syncDocument(absolutePath); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	payload, err := session.waitForDiagnostics(waitCtx, uri)
	if err != nil {
		return nil, fmt.Errorf("%w: no live diagnostics published by %s for %s", ErrInvalidSemanticRequest, session.config.ServerID, filepath.Base(absolutePath))
	}
	return payload, nil
}

func (session *lspSession) ensureInitialized(ctx context.Context) error {
	session.stateMu.Lock()
	if session.initialized {
		session.stateMu.Unlock()
		return nil
	}
	session.stateMu.Unlock()

	session.initMu.Lock()
	defer session.initMu.Unlock()

	session.stateMu.Lock()
	if session.initialized {
		session.stateMu.Unlock()
		return nil
	}
	session.stateMu.Unlock()

	rootURI := fileURIForPath(session.rootPath)
	if _, err := session.request(ctx, "initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"definition": map[string]any{
					"linkSupport": true,
				},
				"references": map[string]any{},
				"documentSymbol": map[string]any{
					"hierarchicalDocumentSymbolSupport": true,
					"symbolKind": map[string]any{
						"valueSet": []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26},
					},
				},
			},
			"workspace": map[string]any{
				"symbol": map[string]any{},
			},
		},
		"clientInfo": map[string]any{
			"name":    "xmustard-api-go",
			"version": "phase3-lsp",
		},
		"workspaceFolders": []map[string]any{
			{
				"uri":  rootURI,
				"name": filepath.Base(session.rootPath),
			},
		},
	}); err != nil {
		return wrapLSPUnavailable(session.config.ServerID, err)
	}
	if err := session.notify("initialized", map[string]any{}); err != nil {
		return wrapLSPUnavailable(session.config.ServerID, err)
	}

	session.stateMu.Lock()
	session.initialized = true
	session.stateMu.Unlock()
	return nil
}

func (session *lspSession) syncDocument(absolutePath string) error {
	content, err := os.ReadFile(absolutePath)
	if err != nil {
		return err
	}
	uri := fileURIForPath(absolutePath)

	session.docMu.Lock()
	version := session.openDocuments[uri]
	if version == 0 {
		session.openDocuments[uri] = 1
		session.docMu.Unlock()
		return session.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": session.config.LanguageID,
				"version":    1,
				"text":       string(content),
			},
		})
	}
	version++
	session.openDocuments[uri] = version
	session.docMu.Unlock()

	return session.notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{
			"uri":     uri,
			"version": version,
		},
		"contentChanges": []map[string]any{
			{"text": string(content)},
		},
	})
}

func (session *lspSession) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := session.nextID()
	responseCh := make(chan lspResponseEnvelope, 1)

	session.pendingMu.Lock()
	session.pending[id] = responseCh
	session.pendingMu.Unlock()

	if err := session.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		session.removePending(id)
		return nil, err
	}

	select {
	case response, ok := <-responseCh:
		if !ok {
			return nil, session.currentLoopErr()
		}
		return response.result, response.err
	case <-ctx.Done():
		session.removePending(id)
		return nil, ctx.Err()
	case <-session.done:
		return nil, session.currentLoopErr()
	}
}

func (session *lspSession) notify(method string, params any) error {
	return session.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (session *lspSession) writeMessage(payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if _, err := fmt.Fprintf(session.stdin, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = session.stdin.Write(body)
	return err
}

func (session *lspSession) readLoop() {
	for {
		payload, err := readLSPFrame(session.stdout)
		if err != nil {
			session.failPending(err)
			return
		}
		var incoming lspIncomingMessage
		if err := json.Unmarshal(payload, &incoming); err != nil {
			session.failPending(err)
			return
		}
		if incoming.Method != "" && len(incoming.ID) > 0 {
			var responseID any
			if err := json.Unmarshal(incoming.ID, &responseID); err == nil {
				_ = session.writeMessage(map[string]any{
					"jsonrpc": "2.0",
					"id":      responseID,
					"result":  nil,
				})
			}
			continue
		}
		if incoming.Method == "textDocument/publishDiagnostics" && len(incoming.ID) == 0 {
			session.recordDiagnostics(incoming.Params)
			continue
		}
		if len(incoming.ID) == 0 {
			continue
		}
		id, ok := parseJSONRPCID(incoming.ID)
		if !ok {
			continue
		}

		session.pendingMu.Lock()
		responseCh := session.pending[id]
		delete(session.pending, id)
		session.pendingMu.Unlock()
		if responseCh == nil {
			continue
		}
		if incoming.Error != nil {
			responseCh <- lspResponseEnvelope{
				err: fmt.Errorf("LSP %s request failed (%d): %s", session.config.ServerID, incoming.Error.Code, incoming.Error.Message),
			}
		} else {
			responseCh <- lspResponseEnvelope{result: incoming.Result}
		}
		close(responseCh)
	}
}

func (session *lspSession) failPending(err error) {
	session.loopErrMu.Lock()
	session.loopErr = err
	session.loopErrMu.Unlock()

	session.pendingMu.Lock()
	defer session.pendingMu.Unlock()
	for id, responseCh := range session.pending {
		responseCh <- lspResponseEnvelope{err: err}
		close(responseCh)
		delete(session.pending, id)
	}
	close(session.done)
}

func (session *lspSession) currentLoopErr() error {
	session.loopErrMu.Lock()
	defer session.loopErrMu.Unlock()
	if session.loopErr != nil {
		return wrapLSPUnavailable(session.config.ServerID, session.loopErr)
	}
	return fmt.Errorf("%w: %s session closed unexpectedly", ErrInvalidSemanticRequest, session.config.ServerID)
}

func (session *lspSession) nextID() int64 {
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	id := session.nextRequestID
	session.nextRequestID++
	session.lastUsedAt = time.Now().UTC()
	return id
}

func (session *lspSession) recordDiagnostics(payload json.RawMessage) {
	var body struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || strings.TrimSpace(body.URI) == "" {
		return
	}
	uri := strings.TrimSpace(body.URI)
	copied := append(json.RawMessage(nil), payload...)

	session.diagMu.Lock()
	session.diagnostics[uri] = copied
	waiters := session.diagWaiters[uri]
	delete(session.diagWaiters, uri)
	session.diagMu.Unlock()

	for _, waiter := range waiters {
		waiter <- copied
		close(waiter)
	}
}

func (session *lspSession) clearDiagnostics(uri string) {
	session.diagMu.Lock()
	delete(session.diagnostics, uri)
	session.diagMu.Unlock()
}

func (session *lspSession) waitForDiagnostics(ctx context.Context, uri string) (json.RawMessage, error) {
	session.diagMu.Lock()
	if payload, ok := session.diagnostics[uri]; ok {
		copied := append(json.RawMessage(nil), payload...)
		session.diagMu.Unlock()
		return copied, nil
	}
	waiter := make(chan json.RawMessage, 1)
	session.diagWaiters[uri] = append(session.diagWaiters[uri], waiter)
	session.diagMu.Unlock()

	select {
	case payload := <-waiter:
		return append(json.RawMessage(nil), payload...), nil
	case <-ctx.Done():
		session.diagMu.Lock()
		waiters := session.diagWaiters[uri]
		for idx, item := range waiters {
			if item == waiter {
				waiters = append(waiters[:idx], waiters[idx+1:]...)
				break
			}
		}
		if len(waiters) == 0 {
			delete(session.diagWaiters, uri)
		} else {
			session.diagWaiters[uri] = waiters
		}
		session.diagMu.Unlock()
		return nil, ctx.Err()
	case <-session.done:
		return nil, session.currentLoopErr()
	}
}

func (session *lspSession) touch() {
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	session.lastUsedAt = time.Now().UTC()
}

func (session *lspSession) isIdle() bool {
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	return time.Since(session.lastUsedAt) > lspSessionIdleTTL
}

func (session *lspSession) removePending(id int64) {
	session.pendingMu.Lock()
	defer session.pendingMu.Unlock()
	delete(session.pending, id)
}

func (session *lspSession) close() {
	session.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if session.initialized {
			_, _ = session.request(ctx, "shutdown", nil)
			_ = session.notify("exit", nil)
		}
		if session.command != nil && session.command.Process != nil {
			_ = session.command.Process.Kill()
			_, _ = session.command.Process.Wait()
		}
		_ = session.stdin.Close()
	})
}

func closeAllLSPSessions() {
	lspSessionsMu.Lock()
	defer lspSessionsMu.Unlock()
	lspSessions.Range(func(key any, value any) bool {
		session, ok := value.(*lspSession)
		if ok {
			session.close()
		}
		lspSessions.Delete(key)
		return true
	})
}

func readLSPFrame(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(parts[0]), "Content-Length") {
			var value int
			if _, err := fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &value); err == nil {
				contentLength = value
			}
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header in LSP response")
	}
	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func parseJSONRPCID(raw json.RawMessage) (int64, bool) {
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		var value int64
		if _, err := fmt.Sscanf(text, "%d", &value); err == nil {
			return value, true
		}
	}
	return 0, false
}

func fileURIForPath(path string) string {
	absolute := filepath.Clean(path)
	return (&url.URL{Scheme: "file", Path: absolute}).String()
}

func sanitizeLSPLogName(value string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
	return replacer.Replace(strings.ToLower(strings.TrimSpace(value)))
}

func wrapLSPUnavailable(serverID string, err error) error {
	return fmt.Errorf("%w: LSP unavailable for %s: %v", ErrInvalidSemanticRequest, serverID, err)
}

func defaultResolveLSPServerForPath(rootPath string, relativePath string) (*lspServerConfig, error) {
	_ = rootPath
	extension := strings.ToLower(filepath.Ext(relativePath))
	candidates := []struct {
		extensions []string
		serverID   string
		languageID string
		command    []string
	}{
		{
			extensions: []string{".py"},
			serverID:   "pyright",
			languageID: "python",
			command:    []string{"pyright-langserver", "--stdio"},
		},
		{
			extensions: []string{".ts", ".tsx", ".js", ".jsx"},
			serverID:   "typescript-language-server",
			languageID: "typescript",
			command:    []string{"typescript-language-server", "--stdio"},
		},
		{
			extensions: []string{".go"},
			serverID:   "gopls",
			languageID: "go",
			command:    []string{"gopls"},
		},
		{
			extensions: []string{".rs"},
			serverID:   "rust-analyzer",
			languageID: "rust",
			command:    []string{"rust-analyzer"},
		},
	}
	for _, candidate := range candidates {
		if !containsString(candidate.extensions, extension) {
			continue
		}
		binaryPath, err := exec.LookPath(candidate.command[0])
		if err != nil {
			return nil, fmt.Errorf("%w: no supported LSP server available for %s; looked for %s", ErrInvalidSemanticRequest, extension, candidate.command[0])
		}
		command := append([]string{binaryPath}, candidate.command[1:]...)
		return &lspServerConfig{
			ServerID:   candidate.serverID,
			LanguageID: candidate.languageID,
			Command:    command,
		}, nil
	}
	return nil, fmt.Errorf("%w: no LSP server mapping for %s", ErrInvalidSemanticRequest, extension)
}

func resolveLSPServerForLanguage(rootPath string, language string) (*lspServerConfig, error) {
	normalized := strings.ToLower(strings.TrimSpace(language))
	if normalized == "" {
		return nil, fmt.Errorf("%w: language is required for live LSP workspace symbols", ErrInvalidSemanticRequest)
	}
	hints := map[string]string{
		"python":     ".py",
		"py":         ".py",
		"typescript": ".ts",
		"ts":         ".ts",
		"javascript": ".js",
		"js":         ".js",
		"go":         ".go",
		"golang":     ".go",
		"rust":       ".rs",
		"rs":         ".rs",
	}
	extension, ok := hints[normalized]
	if !ok {
		return nil, fmt.Errorf("%w: no LSP server mapping for language %s", ErrInvalidSemanticRequest, language)
	}
	return resolveLSPServerForPath(rootPath, "workspace"+extension)
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

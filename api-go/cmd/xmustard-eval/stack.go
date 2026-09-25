package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// An xMustard stack is what the xMustard arms give the client: an API process with the
// run's worktree loaded as a workspace, and the MCP server (or Pi adapter settings)
// that reach it. Every run gets a fresh data directory, so memory never leaks between
// runs; the memory arm seeds its fixture through the real propose/verify routes with
// distinct principals, so a seeded memory is peer-verified exactly as it would be in
// use. The stub stack is for dry runs only and is labelled as such in every record.

// Stack kinds.
const (
	StackReal = "real"
	StackStub = "stub"
	StackNone = "none"
)

// StackConfig selects and locates the stack.
type StackConfig struct {
	Kind     string `yaml:"kind" json:"kind"`
	APIBin   string `yaml:"api_bin" json:"api_bin,omitempty"`
	MCPBin   string `yaml:"mcp_bin" json:"mcp_bin,omitempty"`
	CoreBin  string `yaml:"core_bin" json:"core_bin,omitempty"`
	CoreOnly *bool  `yaml:"core_only" json:"core_only,omitempty"`
}

func (s StackConfig) coreOnly() bool { return s.CoreOnly == nil || *s.CoreOnly }

// Principals minted for each real-stack run. The agent under test is one principal;
// seeding uses an author and two verifiers so seeded memories reach the default
// two-approval quorum without the author approving its own proposal.
const (
	principalAdmin     = "eval-admin"
	principalAgent     = "eval-agent"
	principalAuthor    = "eval-seed-author"
	principalVerifierA = "eval-seed-verifier-a"
	principalVerifierB = "eval-seed-verifier-b"
)

// StackInfo is recorded per run.
type StackInfo struct {
	Kind        string         `json:"kind"`
	WorkspaceID string         `json:"workspace_id,omitempty"`
	CoreOnly    bool           `json:"core_only,omitempty"`
	Seeds       []SeedResult   `json:"seeds,omitempty"`
	Health      map[string]any `json:"health,omitempty"` // /api/health after the agent (counters)
	Error       string         `json:"error,omitempty"`
}

// stackRun is one started stack.
type stackRun interface {
	workspaceID() string
	mcpServer() MCPServer
	piEnv() map[string]string
	seeds() []SeedResult
	// agentPhase switches to the configuration the agent uses and reports the xMustard
	// root pids to sample.
	agentPhase(ctx context.Context) ([]int, error)
	// finish records post-run state (health counters, promotion errors).
	finish(ctx context.Context) (health map[string]any, promotionErrors *int)
	stop()
}

type stackStart struct {
	runDir   string // private stack directory (hidden from the agent)
	worktree string
	taskID   string
	memory   *MemorySpec // nil unless the arm seeds memory
	sampler  *rssSampler
	self     string // this executable (stub MCP bridge)
}

func startStack(ctx context.Context, cfg StackConfig, st stackStart) (stackRun, error) {
	switch cfg.Kind {
	case StackReal:
		return startRealStack(ctx, cfg, st)
	case StackStub:
		return startStubStack(st)
	}
	return nil, fmt.Errorf("stack kind %q cannot be started", cfg.Kind)
}

// ---- real stack ----

type realStack struct {
	cfg       StackConfig
	st        stackStart
	dataDir   string
	tokens    map[string]string
	api       *exec.Cmd
	apiExited chan struct{}
	base      string
	ws        string
	seeded    []SeedResult
	logIndex  int
}

func startRealStack(ctx context.Context, cfg StackConfig, st stackStart) (*realStack, error) {
	s := &realStack{cfg: cfg, st: st, dataDir: filepath.Join(st.runDir, "data"), tokens: map[string]string{}}
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return nil, err
	}
	for _, p := range []struct{ id, role string }{
		{principalAdmin, "admin"}, {principalAgent, "agent"}, {principalAuthor, "agent"},
		{principalVerifierA, "agent"}, {principalVerifierB, "agent"},
	} {
		cmd := exec.CommandContext(ctx, cfg.APIBin, "mint-token", p.id, p.role)
		cmd.Env = append(scrubbedEnv(), "XMUSTARD_DATA_DIR="+s.dataDir)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("mint-token %s: %w", p.id, err)
		}
		s.tokens[p.id] = strings.TrimSpace(string(out))
	}
	if err := s.startAPI(ctx, false); err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			s.stop()
		}
	}()
	ws, err := s.loadWorkspace(ctx, st.worktree)
	if err != nil {
		return nil, err
	}
	s.ws = ws
	if code, body, err := s.call(ctx, "POST", "/api/workspaces/"+url.PathEscape(ws)+"/index", principalAdmin, nil); err != nil || code != http.StatusOK {
		return nil, fmt.Errorf("index baseline: status %d: %v %s", code, err, truncate(body, 300))
	}
	if st.memory != nil {
		if err := s.seed(ctx, st.memory); err != nil {
			return nil, err
		}
	}
	ok = true
	return s, nil
}

func (s *realStack) startAPI(ctx context.Context, coreOnly bool) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	s.logIndex++
	logf, err := os.Create(filepath.Join(s.st.runDir, fmt.Sprintf("api-%d.log", s.logIndex)))
	if err != nil {
		return err
	}
	cmd := exec.Command(s.cfg.APIBin)
	cmd.Env = append(scrubbedEnv(),
		"XMUSTARD_DATA_DIR="+s.dataDir,
		"XMUSTARD_API_HOST=127.0.0.1",
		"XMUSTARD_API_PORT="+strconv.Itoa(port),
		"XMUSTARD_AUTH=required",
		"XMUSTARD_CORE_BIN="+s.cfg.CoreBin,
	)
	if coreOnly {
		cmd.Env = append(cmd.Env, "XMUSTARD_CORE_ONLY=1")
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := startGroup(cmd); err != nil {
		logf.Close()
		return fmt.Errorf("start xmustard-api: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); logf.Close(); close(exited) }()
	s.api, s.apiExited = cmd, exited
	s.base = "http://127.0.0.1:" + strconv.Itoa(port)
	if s.st.sampler != nil {
		s.st.sampler.setXmRoots(cmd.Process.Pid)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if code, _, err := s.call(ctx, "GET", "/api/health", "", nil); err == nil && code == http.StatusOK {
			return nil
		}
		select {
		case <-exited:
			s.stopAPI()
			return fmt.Errorf("xmustard-api exited during startup; see %s", logf.Name())
		case <-time.After(50 * time.Millisecond):
		}
	}
	s.stopAPI() // never leave an unhealthy API behind
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("xmustard-api did not become healthy; see %s", logf.Name())
}

func (s *realStack) stopAPI() {
	if s.api != nil && s.api.Process != nil {
		killGroup(s.api.Process.Pid, 10*time.Second)
		<-s.apiExited
	}
	s.api = nil
}

func (s *realStack) call(ctx context.Context, method, path, principal string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if principal != "" {
		req.Header.Set("Authorization", "Bearer "+s.tokens[principal])
	}
	resp, err := stackHTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return resp.StatusCode, b, err
}

var stackHTTP = &http.Client{Timeout: 10 * time.Minute}

func (s *realStack) loadWorkspace(ctx context.Context, root string) (string, error) {
	code, body, err := s.call(ctx, "POST", "/api/workspaces/load", principalAdmin, map[string]any{"root_path": root, "auto_scan": true})
	if err != nil || code != http.StatusOK {
		return "", fmt.Errorf("load workspace %s: status %d: %v %s", root, code, err, truncate(body, 300))
	}
	var snap struct {
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(body, &snap); err != nil || snap.Workspace.ID == "" {
		return "", fmt.Errorf("load workspace: no workspace_id in response")
	}
	return snap.Workspace.ID, nil
}

// seed proposes every memory as the author and, unless it is labelled pending, has
// both verifiers approve it. A seed that does not end peer-verified (or, for pending,
// that is not left pending) fails the run rather than mislabel the fixture.
func (s *realStack) seed(ctx context.Context, m *MemorySpec) error {
	foreignWS := ""
	for _, sm := range m.Seed {
		ws, where := s.ws, "task"
		if sm.Label == LabelForeignScope {
			if foreignWS == "" {
				root := filepath.Join(s.st.runDir, "foreign-repo")
				if err := makeForeignRepo(root); err != nil {
					return err
				}
				id, err := s.loadWorkspace(ctx, root)
				if err != nil {
					return err
				}
				foreignWS = id
			}
			ws, where = foreignWS, "foreign"
		}
		payload := map[string]any{"content": seededContent(s.st.taskID, sm)}
		if sm.Title != "" {
			payload["title"] = sm.Title
		}
		if len(sm.Paths) > 0 {
			payload["paths"] = sm.Paths
		}
		res := SeedResult{Key: sm.Key, Label: sm.Label, Workspace: where}
		entry, err := s.contextCall(ctx, "POST", "/api/workspaces/"+url.PathEscape(ws)+"/context", principalAuthor, payload)
		if err != nil {
			return fmt.Errorf("seed %s: propose: %w", sm.Key, err)
		}
		res.EntryID = entry.ID
		if sm.Label != LabelPending {
			for _, v := range []string{principalVerifierA, principalVerifierB} {
				p := "/api/workspaces/" + url.PathEscape(ws) + "/context/" + url.PathEscape(entry.ID) + "/verify?approve=true"
				if entry, err = s.contextCall(ctx, "POST", p, v, nil); err != nil {
					return fmt.Errorf("seed %s: verify: %w", sm.Key, err)
				}
			}
		}
		res.Status, res.VerificationMode = entry.Status, entry.VerificationMode
		switch {
		case sm.Label == LabelPending && entry.Status != "pending":
			return fmt.Errorf("seed %s: expected a pending memory, got status %q", sm.Key, entry.Status)
		case sm.Label != LabelPending && (entry.Status != "verified" || entry.VerificationMode != "peer_verified"):
			return fmt.Errorf("seed %s: expected peer_verified, got status %q mode %q", sm.Key, entry.Status, entry.VerificationMode)
		}
		s.seeded = append(s.seeded, res)
	}
	return nil
}

type contextEntry struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	VerificationMode string `json:"verification_mode"`
}

func (s *realStack) contextCall(ctx context.Context, method, path, principal string, body any) (contextEntry, error) {
	var e contextEntry
	code, b, err := s.call(ctx, method, path, principal, body)
	if err != nil {
		return e, err
	}
	if code != http.StatusOK {
		return e, fmt.Errorf("status %d: %s", code, truncate(b, 300))
	}
	if err := json.Unmarshal(b, &e); err != nil || e.ID == "" {
		return e, fmt.Errorf("unexpected response: %s", truncate(b, 300))
	}
	return e, nil
}

func makeForeignRepo(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("A different repository. Memories here must never reach another workspace.\n"), 0o644); err != nil {
		return err
	}
	if _, err := git(root, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	if _, err := git(root, "add", "-A"); err != nil {
		return err
	}
	_, err := git(root, "-c", "user.name=xmustard-eval", "-c", "user.email=eval@xmustard.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-m", "foreign")
	return err
}

func (s *realStack) workspaceID() string { return s.ws }
func (s *realStack) seeds() []SeedResult { return s.seeded }

func (s *realStack) mcpServer() MCPServer {
	return MCPServer{Name: "xmustard", Command: s.cfg.MCPBin,
		Env: map[string]string{"XMUSTARD_API_BASE": s.base, "XMUSTARD_API_TOKEN": s.tokens[principalAgent]}}
}

func (s *realStack) piEnv() map[string]string {
	return map[string]string{"XMUSTARD_API_BASE": s.base, "XMUSTARD_TOKEN": s.tokens[principalAgent]}
}

// agentPhase restarts the API with only the nine-tool surface (CORE_ONLY), the
// deployed shape agents use; setup routes (load, index) are then unreachable.
func (s *realStack) agentPhase(ctx context.Context) ([]int, error) {
	if s.cfg.coreOnly() {
		s.stopAPI()
		if err := s.startAPI(ctx, true); err != nil {
			return nil, err
		}
	}
	return []int{s.api.Process.Pid}, nil
}

func (s *realStack) finish(ctx context.Context) (map[string]any, *int) {
	var health map[string]any
	if code, b, err := s.call(ctx, "GET", "/api/health", "", nil); err == nil && code == http.StatusOK {
		_ = json.Unmarshal(b, &health)
	}
	// Promotion errors: with one agent principal and a two-approval quorum, nothing
	// may become verified during the run except what seeding verified.
	code, b, err := s.call(ctx, "GET", "/api/workspaces/"+url.PathEscape(s.ws)+"/context?filter=all", principalAdmin, nil)
	if err != nil || code != http.StatusOK {
		return health, nil
	}
	var entries []contextEntry
	if json.Unmarshal(b, &entries) != nil {
		return health, nil
	}
	allowed := map[string]bool{}
	for _, sd := range s.seeded {
		if sd.Status == "verified" {
			allowed[sd.EntryID] = true
		}
	}
	n := 0
	for _, e := range entries {
		if e.Status == "verified" && !allowed[e.ID] {
			n++
		}
	}
	return health, &n
}

func (s *realStack) stop() { s.stopAPI() }

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// ---- stub stack (dry runs only) ----

// stubStack serves the few API routes the fake agent and the stub MCP bridge use,
// from an in-memory store. It is not xMustard: it serves every verified memory of the
// task workspace and flags a memory stale when a drift edit touched its paths. It
// exists so the harness pipeline can be exercised without building the product.
type stubStack struct {
	srv     *http.Server
	base    string
	token   string
	ws      string
	self    string
	mu      sync.Mutex
	entries []stubEntry
	seeded  []SeedResult
}

type stubEntry struct {
	ID, Title, Content, Mode string
	Paths                    []string
	Foreign, Pending         bool
	Stale                    bool
}

func startStubStack(st stackStart) (*stubStack, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &stubStack{token: "stub-agent-token", ws: "stub-" + st.taskID, self: st.self}
	if st.memory != nil {
		drifted := map[string]bool{}
		for _, d := range st.memory.Drift {
			drifted[d.Path] = true
		}
		for _, sm := range st.memory.Seed {
			e := stubEntry{ID: "stub-" + sm.Key, Title: sm.Title, Content: seededContent(st.taskID, sm), Paths: sm.Paths,
				Foreign: sm.Label == LabelForeignScope, Pending: sm.Label == LabelPending, Mode: "peer_verified"}
			for _, p := range sm.Paths {
				e.Stale = e.Stale || drifted[p]
			}
			res := SeedResult{Key: sm.Key, Label: sm.Label, EntryID: e.ID, Workspace: "task", Status: "verified", VerificationMode: "peer_verified"}
			if e.Foreign {
				res.Workspace = "foreign"
			}
			if e.Pending {
				e.Mode, res.Status, res.VerificationMode = "", "pending", ""
			}
			s.entries = append(s.entries, e)
			s.seeded = append(s.seeded, res)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, http.StatusOK, map[string]any{"status": "ok", "service": "eval-stub"})
	})
	mux.HandleFunc("GET /api/workspaces/{ws}/context/active", s.auth(s.recall))
	mux.HandleFunc("GET /api/workspaces/{ws}/session-grounding", s.auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, http.StatusOK, map[string]any{"workspace_id": r.PathValue("ws"), "stub": true, "changes": []any{}})
	}))
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(l) }()
	s.base = "http://" + l.Addr().String()
	return s, nil
}

func (s *stubStack) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			writeJSONResp(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if r.PathValue("ws") != s.ws {
			writeJSONResp(w, http.StatusNotFound, map[string]any{"error": "workspace not found"})
			return
		}
		h(w, r)
	}
}

func (s *stubStack) recall(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	byPath := map[string][]string{}
	for _, e := range s.entries {
		if e.Foreign || e.Pending {
			continue
		}
		out = append(out, map[string]any{"id": e.ID, "title": e.Title, "content": e.Content, "paths": e.Paths,
			"status": "verified", "verification_mode": e.Mode, "stale": e.Stale})
		for _, p := range e.Paths {
			byPath[p] = append(byPath[p], e.ID)
		}
	}
	conflicts := []map[string]any{}
	for _, p := range sortedKeys(byPath) {
		if ids := byPath[p]; len(ids) > 1 {
			conflicts = append(conflicts, map[string]any{"path": p, "entry_ids": ids})
		}
	}
	writeJSONResp(w, http.StatusOK, map[string]any{"workspace_id": s.ws, "stub": true, "returned": len(out), "entries": out, "conflicts": conflicts})
}

func writeJSONResp(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *stubStack) workspaceID() string { return s.ws }
func (s *stubStack) seeds() []SeedResult { return s.seeded }

func (s *stubStack) mcpServer() MCPServer {
	return MCPServer{Name: "xmustard", Command: s.self, Args: []string{"stub-mcp"},
		Env: map[string]string{"XMUSTARD_API_BASE": s.base, "XMUSTARD_API_TOKEN": s.token, "XMUSTARD_EVAL_AS_MAIN": "1"}}
}

func (s *stubStack) piEnv() map[string]string {
	return map[string]string{"XMUSTARD_API_BASE": s.base, "XMUSTARD_TOKEN": s.token}
}

func (s *stubStack) agentPhase(context.Context) ([]int, error) { return nil, nil }

func (s *stubStack) finish(context.Context) (map[string]any, *int) {
	zero := 0
	return map[string]any{"status": "ok", "service": "eval-stub"}, &zero
}

func (s *stubStack) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

// ---- stub MCP bridge (`xmustard-eval stub-mcp`) ----

// stubMCPTools mirrors the nine tool names; only ground and recall are routed.
var stubMCPTools = []string{"ground", "recall", "remember", "verify", "search", "explain", "impact", "diagnostics", "why_failed"}

// runStubMCP is a minimal newline-delimited JSON-RPC MCP server that forwards ground
// and recall to the stub API. Dry runs only.
func runStubMCP(in io.Reader, out io.Writer) error {
	base := strings.TrimRight(os.Getenv("XMUSTARD_API_BASE"), "/")
	token := os.Getenv("XMUSTARD_API_TOKEN")
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	for {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if len(msg.ID) == 0 {
			continue // notification
		}
		var result any
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "xmustard-eval-stub", "version": "0"}}
		case "tools/list":
			var tools []map[string]any
			for _, n := range stubMCPTools {
				tools = append(tools, map[string]any{"name": n, "description": "stub " + n, "inputSchema": map[string]any{"type": "object"}})
			}
			result = map[string]any{"tools": tools}
		case "tools/call":
			var p struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			result = stubToolCall(base, token, p.Name, p.Arguments)
		default:
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			continue
		}
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result}); err != nil {
			return err
		}
	}
}

func stubToolCall(base, token, name string, args map[string]string) map[string]any {
	ws := url.PathEscape(args["workspace_id"])
	var path string
	switch name {
	case "recall":
		path = "/api/workspaces/" + ws + "/context/active?query=" + url.QueryEscape(args["query"])
	case "ground":
		path = "/api/workspaces/" + ws + "/session-grounding"
	default:
		return toolText("stub stack: "+name+" is not served in dry runs", true)
	}
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return toolText(err.Error(), true)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return toolText(string(b), resp.StatusCode != http.StatusOK)
}

func toolText(text string, isErr bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isErr}
}

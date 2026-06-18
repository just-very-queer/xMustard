// xmustard-mcp is an MCP (Model Context Protocol) stdio server that exposes
// xMustard's repo intelligence + runtime memory to agents as typed tools — the
// "context engine" surface. It bridges newline-delimited JSON-RPC 2.0 over
// stdin/stdout to the running xMustard HTTP API (XMUSTARD_API_BASE, default
// http://127.0.0.1:8042), so agents share durable state across agents/modules
// instead of markdown, and a reconnecting agent re-checks current state.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const protocolVersion = "2024-11-05"

func apiBase() string {
	if v := os.Getenv("XMUSTARD_API_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:8042"
}

// tool describes one MCP tool and how to turn its arguments into an API call.
type tool struct {
	Name        string
	Description string
	Required    []string
	// build returns (method, path-with-query) for the API call.
	Build func(args map[string]string) (string, string)
}

func wsPath(args map[string]string, suffix string) string {
	return "/api/workspaces/" + url.PathEscape(args["workspace_id"]) + suffix
}

func tools() []tool {
	return []tool{
		{"repo_state", "Current repo state: what repo this is, branch, dirty state, and health.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/repo-state") }},
		{"repo_summary", "Structural repo map summary (files, directories, key files).", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/snapshot") }},
		{"changed_since", "Uncommitted working-tree changes plus dirty SYMBOLS (not just files).", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/changes") }},
		{"drift", "Stale-index / sibling-clone drift vs the indexed baseline — is the index trustworthy.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/changes/drift") }},
		{"definitions", "Symbols defined in a file (path-relative).", []string{"workspace_id", "path"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/path-symbols") + "?path=" + url.QueryEscape(a["path"])
			}},
		{"diagnostics", "Normalized diagnostics (errors/warnings) for the workspace.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/diagnostics") }},
		{"impact", "Likely impact of current changes: changed symbols + affected files/tests.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/changes/since-index") }},
		{"run_targets", "Detected run/build/test/lint targets for the repo.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/run-targets") }},
		{"verify_targets", "Detected verification targets for the repo.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/verify-targets") }},
		{"issue_context_packet", "Full grounded context packet for an issue.", []string{"workspace_id", "issue_id"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/issues/"+url.PathEscape(a["issue_id"])+"/context")
			}},
		{"recent_failures", "Recent runs (inspect for failures) in the workspace.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/runs") }},
		{"code_explainer", "Explain a file: purpose, role, key symbols, how to run/verify.", []string{"workspace_id", "path"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/explain-path") + "?path=" + url.QueryEscape(a["path"])
			}},
		{"subsystem_explainer", "Explain a subsystem/directory's purpose and structure.", []string{"workspace_id", "path"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/explain-path") + "?path=" + url.QueryEscape(a["path"])
			}},
		{"hotspots", "Most-depended-on files (risky to touch) from the symbol graph.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/hotspots") }},
		{"blast_radius", "What files reference a symbol — the blast radius of changing it.", []string{"workspace_id", "symbol"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/blast-radius") + "?symbol=" + url.QueryEscape(a["symbol"])
			}},
		{"symbol_graph", "The full semantic symbol graph: files, symbols, and typed edges (imports/calls/inherits/tests/references).", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/symbol-graph") }},
		{"issue_symbol_edges", "Typed issue↔symbol edges: which issues mention or have evidence pointing at which defined symbols.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/issue-symbol-edges") }},
		{"lsp_document_symbols", "Live LSP document symbols for a file (rust-core spawns the real language server). Degrades gracefully if the server isn't installed.", []string{"workspace_id", "path"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/lsp/document-symbols") + "?path=" + url.QueryEscape(a["path"])
			}},
		{"context_active", "The trusted shared context: entries promoted after multi-agent verification (read-only view an agent should ground on).", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/context/active") }},
		{"context_propose", "Propose a context entry for the shared context (pending until verified by enough agents). Pass content; optional title/source.", []string{"workspace_id", "content"},
			func(a map[string]string) (string, string) {
				p := wsPath(a, "/context") + "?content=" + url.QueryEscape(a["content"])
				if a["title"] != "" {
					p += "&title=" + url.QueryEscape(a["title"])
				}
				if a["source"] != "" {
					p += "&source=" + url.QueryEscape(a["source"])
				}
				return "POST", p
			}},
		{"context_verify", "Verify (approve/reject) a proposed context entry as an agent; promotes it once the multi-agent threshold is met.", []string{"workspace_id", "entry_id", "agent"},
			func(a map[string]string) (string, string) {
				approve := "true"
				if a["approve"] == "false" {
					approve = "false"
				}
				return "POST", wsPath(a, "/context/"+url.PathEscape(a["entry_id"])+"/verify") + "?agent=" + url.QueryEscape(a["agent"]) + "&approve=" + approve
			}},
		{"provider_chat", "Call an OpenAI-compatible provider (Ollama/vLLM/LM Studio/OpenAI) by name with a prompt; optional model. For local/private model access.", []string{"provider", "prompt"},
			func(a map[string]string) (string, string) {
				p := "/api/providers/" + url.PathEscape(a["provider"]) + "/chat?prompt=" + url.QueryEscape(a["prompt"])
				if a["model"] != "" {
					p += "&model=" + url.QueryEscape(a["model"])
				}
				return "POST", p
			}},
		{"route_model", "Classify a coding request into a task type and pick the best provider+model (task-typed routing). Returns the routing decision without executing.", []string{"prompt"},
			func(a map[string]string) (string, string) {
				p := "/api/route?prompt=" + url.QueryEscape(a["prompt"])
				if a["task_hint"] != "" {
					p += "&task_hint=" + url.QueryEscape(a["task_hint"])
				}
				return "POST", p
			}},
		{"search_repo", "Hybrid lexical+structural search over the repo's symbols and files.", []string{"workspace_id", "query"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/search") + "?q=" + url.QueryEscape(a["query"])
			}},
		{"wiki", "Generated repo wiki: overview + per-subsystem pages from the symbol graph.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/wiki") }},
		{"session_grounding", "What changed / what's broken / what's blocked since the indexed baseline.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/session-grounding") }},
		{"subsystems", "Subsystem/ownership model: clusters, cohesion, file/symbol counts.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/subsystems") }},
		{"owners", "Likely owners of a file or directory (from git history).", []string{"workspace_id", "path"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/owners") + "?path=" + url.QueryEscape(a["path"])
			}},
		{"lineage", "Incorporation lineage of a file: when indexed and each change, with hashes.", []string{"workspace_id", "path"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/lineage") + "?path=" + url.QueryEscape(a["path"])
			}},
		{"pg_search", "Postgres FTS hybrid search (RRF of ts_rank + structural lanes) over the symbol index.", []string{"workspace_id", "query"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/pg/search") + "?q=" + url.QueryEscape(a["query"])
			}},
		{"pg_runs", "Recent runs from the Postgres ops index (optionally filter by status).", []string{"workspace_id"},
			func(a map[string]string) (string, string) {
				p := wsPath(a, "/pg/runs")
				if s := a["status"]; s != "" {
					p += "?status=" + url.QueryEscape(s)
				}
				return "GET", p
			}},
		{"pg_issue_search", "Postgres FTS over the workspace's issues (title/summary/impact/notes).", []string{"workspace_id", "query"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/pg/issues/search") + "?q=" + url.QueryEscape(a["query"])
			}},
	}
}

func toolByName(name string) (tool, bool) {
	for _, t := range tools() {
		if t.Name == name {
			return t, true
		}
	}
	return tool{}, false
}

// --- JSON-RPC ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func httpClient() *http.Client { return &http.Client{Timeout: 60 * time.Second} }

func callAPI(method, path string) (string, error) {
	req, err := http.NewRequest(method, apiBase()+path, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("xmustard API unreachable at %s (%w)", apiBase(), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("API %s %s -> %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

// toolsListResult builds the MCP tools/list payload.
func toolsListResult() map[string]any {
	list := []map[string]any{}
	for _, t := range tools() {
		props := map[string]any{}
		for _, r := range t.Required {
			desc := "the workspace id"
			switch r {
			case "issue_id":
				desc = "the issue/bug id"
			case "path":
				desc = "a repo-relative file or directory path"
			case "query":
				desc = "the search query"
			case "symbol":
				desc = "a symbol name"
			}
			props[r] = map[string]any{"type": "string", "description": desc}
		}
		list = append(list, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": map[string]any{"type": "object", "properties": props, "required": t.Required},
		})
	}
	return map[string]any{"tools": list}
}

// callTool runs one tool and returns the MCP tools/call result object.
func callTool(name string, args map[string]string) map[string]any {
	t, ok := toolByName(name)
	if !ok {
		return mcpText(fmt.Sprintf("unknown tool %q", name), true)
	}
	for _, r := range t.Required {
		if strings.TrimSpace(args[r]) == "" {
			return mcpText(fmt.Sprintf("missing required argument %q for %s", r, name), true)
		}
	}
	method, path := t.Build(args)
	body, err := callAPI(method, path)
	if err != nil {
		return mcpText(err.Error(), true)
	}
	return mcpText(body, false)
}

func mcpText(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

// dispatch handles one JSON-RPC method, returning a result (or nil for notifications).
func dispatch(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "xmustard", "version": "0.1.0"},
		}, nil
	case "tools/list":
		return toolsListResult(), nil
	case "tools/call":
		var p struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		_ = json.Unmarshal(params, &p)
		args := map[string]string{}
		for k, v := range p.Arguments {
			args[k] = fmt.Sprintf("%v", v)
		}
		return callTool(p.Name, args), nil
	case "ping":
		return map[string]any{}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + method}
	}
}

func main() {
	reader := bufio.NewReaderSize(os.Stdin, 1<<20)
	writer := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(writer)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = []byte(strings.TrimSpace(string(line)))
		}
		if len(line) > 0 {
			var req rpcRequest
			if jsonErr := json.Unmarshal(line, &req); jsonErr == nil {
				// notifications have no id and expect no response
				if len(req.ID) == 0 && strings.HasPrefix(req.Method, "notifications/") {
					// no-op
				} else {
					result, rerr := dispatch(req.Method, req.Params)
					resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
					if rerr != nil {
						resp.Error = rerr
					} else {
						resp.Result = result
					}
					_ = enc.Encode(resp)
					_ = writer.Flush()
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			break
		}
	}
}

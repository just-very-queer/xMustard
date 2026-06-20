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

// tools returns the agent-facing MCP tool set: a small, fixed list of governed
// runtime-memory and grounding tools. Each entry maps a tool name to the HTTP
// method and path it proxies to on the xMustard API.
func tools() []tool {
	return []tool{
		{"ground", "Orient before acting: what changed / what's stale / what's broken / what's blocked since the indexed baseline, with index-trust (drift) included.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/session-grounding") }},
		{"recall", "The VERIFIED shared context to trust, RANKED to your task: pass a query and/or paths to get the few relevant facts (multi-signal: lexical + path overlap + verification strength), not a dump. No query → recency-ranked top-N.", []string{"workspace_id"},
			func(a map[string]string) (string, string) {
				p := wsPath(a, "/context/active")
				sep := "?"
				if a["query"] != "" {
					p += sep + "query=" + url.QueryEscape(a["query"])
					sep = "&"
				}
				if a["paths"] != "" {
					p += sep + "paths=" + url.QueryEscape(a["paths"])
				}
				return "GET", p
			}},
		{"remember", "Propose a durable memory (fact/decision/gotcha) for the shared context; pending until verified by enough agents. Pass content; optional title and paths (comma-separated files the memory is about, so recall can flag it stale when they change).", []string{"workspace_id", "content"},
			func(a map[string]string) (string, string) {
				p := wsPath(a, "/context") + "?content=" + url.QueryEscape(a["content"])
				if a["title"] != "" {
					p += "&title=" + url.QueryEscape(a["title"])
				}
				if a["paths"] != "" {
					p += "&paths=" + url.QueryEscape(a["paths"])
				}
				return "POST", p
			}},
		{"verify", "Verify (approve/reject) a peer's proposed memory; it promotes once enough DISTINCT agents approve. Your identity is your auth token; approve defaults true.", []string{"workspace_id", "entry_id"},
			func(a map[string]string) (string, string) {
				approve := "true"
				if a["approve"] == "false" {
					approve = "false"
				}
				return "POST", wsPath(a, "/context/"+url.PathEscape(a["entry_id"])+"/verify") + "?approve=" + approve
			}},
		{"search", "Narrow code search over the repo, returning relevant slices (path:line), not a dump. Default mode is hybrid (lexical+semantic+structural+proximity). Pass seed=<symbol> to anchor a graph-PROXIMITY lane that pulls symbols structurally near that symbol up the ranking (auto-seeds from an exact query→symbol match otherwise). Pass mode=pattern to run an ast-grep STRUCTURAL query (query is the pattern, e.g. `$A && $A()`; optional lang).", []string{"workspace_id", "query"},
			func(a map[string]string) (string, string) {
				p := wsPath(a, "/search") + "?q=" + url.QueryEscape(a["query"])
				if a["mode"] != "" {
					p += "&mode=" + url.QueryEscape(a["mode"])
				}
				if a["lang"] != "" {
					p += "&lang=" + url.QueryEscape(a["lang"])
				}
				if a["seed"] != "" {
					p += "&seed=" + url.QueryEscape(a["seed"])
				}
				return "GET", p
			}},
		{"explain", "Explain a file or directory: purpose, role, key symbols, and how to run/verify it.", []string{"workspace_id", "path"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/explain-path") + "?path=" + url.QueryEscape(a["path"])
			}},
		{"impact", "Blast radius. No args → impact of the current changes (dirty symbols). symbol= → every file that transitively references that symbol (graph BFS). from= & to= → the shortest dependency path between two symbols.", []string{"workspace_id"},
			func(a map[string]string) (string, string) {
				p := wsPath(a, "/changes/since-index")
				q := ""
				if a["from"] != "" && a["to"] != "" {
					q = "?from=" + url.QueryEscape(a["from"]) + "&to=" + url.QueryEscape(a["to"])
				} else if a["symbol"] != "" {
					q = "?symbol=" + url.QueryEscape(a["symbol"])
				}
				return "GET", p + q
			}},
		{"diagnostics", "Current normalized diagnostics (errors/warnings) for the workspace.", []string{"workspace_id"},
			func(a map[string]string) (string, string) { return "GET", wsPath(a, "/diagnostics") }},
		{"why_failed", "Explain why a run failed: failure signals, salient error lines, and which changed files are implicated.", []string{"workspace_id", "run_id"},
			func(a map[string]string) (string, string) {
				return "GET", wsPath(a, "/runs/"+url.PathEscape(a["run_id"])+"/why-failed")
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
	// Each agent runs its own xmustard-mcp; XMUSTARD_API_TOKEN is that agent's
	// bearer token, so the API resolves a real per-agent identity (and the
	// multi-agent verification gate counts distinct authenticated principals).
	if tok := strings.TrimSpace(os.Getenv("XMUSTARD_API_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
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

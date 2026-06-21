// xmustard-mcp is an MCP (Model Context Protocol) stdio server that exposes
// xMustard's repo intelligence + runtime memory to agents as typed tools — the
// "context engine" surface. It bridges newline-delimited JSON-RPC 2.0 over
// stdin/stdout to the running xMustard HTTP API (XMUSTARD_API_BASE, default
// http://127.0.0.1:8042), so agents share durable state across agents/modules
// instead of markdown, and a reconnecting agent re-checks current state.
package main

import (
	"bufio"
	"bytes"
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

// argSpec describes one optional tool argument (required args are always strings).
type argSpec struct {
	Name string
	Type string // "string" | "boolean"
	Enum []string
	Desc string
}

// tool describes one MCP tool and how to turn its arguments into an API call.
type tool struct {
	Name        string
	Description string
	Required    []string  // required args (all string-typed)
	Optional    []argSpec // optional args with types/enums for the input schema + validation
	// Build returns (method, path-with-query, body). body is "" for no body; a
	// non-empty body is sent as application/json (used so `remember` ships memory
	// content in the POST body, not the URL query — XM-NEW-018).
	Build func(args map[string]string) (string, string, string)
}

func wsPath(args map[string]string, suffix string) string {
	return "/api/workspaces/" + url.PathEscape(args["workspace_id"]) + suffix
}

// splitCSV turns a comma-separated arg ("a.go, b.go") into a trimmed, non-empty
// slice for JSON-body fields like `paths`.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// tools returns the agent-facing MCP tool set: a small, fixed list of governed
// runtime-memory and grounding tools. Each entry maps a tool name to the HTTP
// method and path it proxies to on the xMustard API.
func tools() []tool {
	return []tool{
		{"ground", "Orient before acting: what changed / what's stale / what's broken / what's blocked since the indexed baseline, with index-trust (drift) and any contract breaks (changed function signatures vs the baseline) included.", []string{"workspace_id"}, nil,
			func(a map[string]string) (string, string, string) { return "GET", wsPath(a, "/session-grounding"), "" }},
		{"recall", "The VERIFIED shared context to trust, RANKED to your task: pass a query and/or paths to get the few relevant facts (multi-signal: lexical + path overlap + verification strength), not a dump. No query → recency-ranked top-N.", []string{"workspace_id"},
			[]argSpec{{"query", "string", nil, "task query to rank memories by"}, {"paths", "string", nil, "comma-separated repo-relative files to focus on"}},
			func(a map[string]string) (string, string, string) {
				p := wsPath(a, "/context/active")
				sep := "?"
				if a["query"] != "" {
					p += sep + "query=" + url.QueryEscape(a["query"])
					sep = "&"
				}
				if a["paths"] != "" {
					p += sep + "paths=" + url.QueryEscape(a["paths"])
				}
				return "GET", p, ""
			}},
		{"remember", "Propose a durable memory (fact/decision/gotcha) for the shared context; pending until verified by enough agents. Pass content; optional title and paths (comma-separated files the memory is about, so recall can flag it stale when they change).", []string{"workspace_id", "content"},
			[]argSpec{{"title", "string", nil, "short title"}, {"paths", "string", nil, "comma-separated repo-relative files the memory is about"}},
			func(a map[string]string) (string, string, string) {
				// content goes in the JSON BODY, not the URL, so durable memory text is
				// not exposed in access logs / error strings (XM-NEW-018).
				payload := map[string]any{"content": a["content"]}
				if a["title"] != "" {
					payload["title"] = a["title"]
				}
				if a["paths"] != "" {
					payload["paths"] = splitCSV(a["paths"])
				}
				b, _ := json.Marshal(payload)
				return "POST", wsPath(a, "/context"), string(b)
			}},
		{"verify", "Verify (approve/reject) a peer's proposed memory; it promotes once enough DISTINCT agents approve. Your identity is your auth token; approve defaults true.", []string{"workspace_id", "entry_id"},
			[]argSpec{{"approve", "boolean", nil, "approve (default true) or reject"}},
			func(a map[string]string) (string, string, string) {
				approve := "true"
				if a["approve"] == "false" {
					approve = "false"
				}
				return "POST", wsPath(a, "/context/"+url.PathEscape(a["entry_id"])+"/verify") + "?approve=" + approve, ""
			}},
		{"search", "Narrow code search over the repo, returning relevant slices (path:line), not a dump. Default mode is hybrid (lexical+semantic+structural+proximity). Pass seed=<symbol> to anchor a graph-PROXIMITY lane that pulls symbols structurally near that symbol up the ranking (auto-seeds from an exact query→symbol match otherwise). Pass mode=pattern to run an ast-grep STRUCTURAL query (query is the pattern, e.g. `$A && $A()`; optional lang).", []string{"workspace_id", "query"},
			[]argSpec{{"mode", "string", []string{"hybrid", "pattern"}, "hybrid (default) or pattern (ast-grep)"}, {"lang", "string", nil, "language hint for pattern mode"}, {"seed", "string", nil, "symbol to anchor the graph-proximity lane"}},
			func(a map[string]string) (string, string, string) {
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
				return "GET", p, ""
			}},
		{"explain", "Explain a file or directory: purpose, role, key symbols, and how to run/verify it.", []string{"workspace_id", "path"}, nil,
			func(a map[string]string) (string, string, string) {
				return "GET", wsPath(a, "/explain-path") + "?path=" + url.QueryEscape(a["path"]), ""
			}},
		{"impact", "Blast radius. No args → impact of the current changes (dirty symbols, with contract_break flags where a signature changed vs the baseline). symbol= → every file that transitively references that symbol (graph BFS). from= & to= → the shortest dependency path between two symbols.", []string{"workspace_id"},
			[]argSpec{{"symbol", "string", nil, "symbol to compute blast radius for"}, {"from", "string", nil, "trace path from this symbol"}, {"to", "string", nil, "trace path to this symbol"}},
			func(a map[string]string) (string, string, string) {
				p := wsPath(a, "/changes/since-index")
				q := ""
				if a["from"] != "" && a["to"] != "" {
					q = "?from=" + url.QueryEscape(a["from"]) + "&to=" + url.QueryEscape(a["to"])
				} else if a["symbol"] != "" {
					q = "?symbol=" + url.QueryEscape(a["symbol"])
				}
				return "GET", p + q, ""
			}},
		{"diagnostics", "Current normalized diagnostics (errors/warnings) for the workspace.", []string{"workspace_id"}, nil,
			func(a map[string]string) (string, string, string) { return "GET", wsPath(a, "/diagnostics"), "" }},
		{"why_failed", "Explain why a run failed: failure signals, salient error lines, and which changed files are implicated.", []string{"workspace_id", "run_id"}, nil,
			func(a map[string]string) (string, string, string) {
				return "GET", wsPath(a, "/runs/"+url.PathEscape(a["run_id"])+"/why-failed"), ""
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

func callAPI(method, path, body string) (string, error) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, apiBase()+path, bodyReader)
	if err != nil {
		return "", err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
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
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("API %s %s -> %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return string(respBody), nil
}

// requiredDesc returns a human description for a required (always-string) arg.
func requiredDesc(name string) string {
	switch name {
	case "issue_id":
		return "the issue/bug id"
	case "entry_id":
		return "the id of the memory entry"
	case "run_id":
		return "the id of the run"
	case "path":
		return "a repo-relative file or directory path"
	case "query":
		return "the search query"
	case "content":
		return "the memory text to propose"
	case "symbol":
		return "a symbol name"
	default:
		return "the workspace id"
	}
}

// toolsListResult builds the MCP tools/list payload. The inputSchema merges the
// required (string) args and the typed optional args, and sets
// additionalProperties:false so a client schema-validates the same surface the
// server enforces in dispatch.
func toolsListResult() map[string]any {
	list := []map[string]any{}
	for _, t := range tools() {
		props := map[string]any{}
		for _, r := range t.Required {
			props[r] = map[string]any{"type": "string", "description": requiredDesc(r)}
		}
		for _, o := range t.Optional {
			typ := o.Type
			if typ == "" {
				typ = "string"
			}
			prop := map[string]any{"type": typ, "description": o.Desc}
			if len(o.Enum) > 0 {
				prop["enum"] = o.Enum
			}
			props[o.Name] = prop
		}
		list = append(list, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           props,
				"required":             t.Required,
				"additionalProperties": false,
			},
		})
	}
	return map[string]any{"tools": list}
}

// callTool runs one tool and returns the MCP tools/call result object. Required
// args and value types are validated up front in dispatch (buildArgs); the
// missing-required check here is a defensive backstop for direct callers/tests.
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
	method, path, reqBody := t.Build(args)
	respBody, err := callAPI(method, path, reqBody)
	if err != nil {
		return mcpText(err.Error(), true)
	}
	return mcpText(respBody, false)
}

// buildArgs strictly validates the raw tools/call arguments against a tool's
// declared required/optional surface and coerces them into a string map. It
// rejects (with a JSON-RPC -32602 invalid-params error) unknown arguments,
// wrong-typed values, non-scalar values (objects/arrays), and out-of-enum
// values — never silently string-coercing whatever was passed.
func buildArgs(t tool, raw map[string]any) (map[string]string, *rpcError) {
	known := map[string]argSpec{}
	for _, r := range t.Required {
		known[r] = argSpec{Name: r, Type: "string"}
	}
	for _, o := range t.Optional {
		known[o.Name] = o
	}
	invalid := func(format string, a ...any) *rpcError {
		return &rpcError{Code: -32602, Message: fmt.Sprintf(format, a...)}
	}
	args := map[string]string{}
	for k, v := range raw {
		spec, ok := known[k]
		if !ok {
			return nil, invalid("unknown argument %q for tool %s", k, t.Name)
		}
		switch spec.Type {
		case "boolean":
			b, ok := v.(bool)
			if !ok {
				return nil, invalid("argument %q for tool %s must be a boolean", k, t.Name)
			}
			if b {
				args[k] = "true"
			} else {
				args[k] = "false"
			}
		default: // string-typed (required args and string optionals)
			s, ok := v.(string)
			if !ok {
				return nil, invalid("argument %q for tool %s must be a string", k, t.Name)
			}
			if len(spec.Enum) > 0 {
				match := false
				for _, e := range spec.Enum {
					if s == e {
						match = true
						break
					}
				}
				if !match {
					return nil, invalid("argument %q for tool %s must be one of: %s", k, t.Name, strings.Join(spec.Enum, ", "))
				}
			}
			args[k] = s
		}
	}
	return args, nil
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
		dec := json.NewDecoder(bytes.NewReader(params))
		dec.DisallowUnknownFields() // reject stray top-level fields instead of ignoring them
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := dec.Decode(&p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
		}
		if strings.TrimSpace(p.Name) == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params: missing tool name"}
		}
		t, ok := toolByName(p.Name)
		if !ok {
			// Unknown tool is reported as a tool result (isError) so the agent can
			// self-correct, matching MCP's tool-error convention.
			return mcpText(fmt.Sprintf("unknown tool %q", p.Name), true), nil
		}
		args, rerr := buildArgs(t, p.Arguments)
		if rerr != nil {
			return nil, rerr
		}
		return callTool(p.Name, args), nil
	case "ping":
		return map[string]any{}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + method}
	}
}

// maxMessageBytes bounds a single newline-delimited JSON-RPC message. Without it,
// ReadBytes('\n') accumulates a newline-less stream unboundedly and OOM-kills the
// server (XM-NEW-020). 8 MiB comfortably fits any legitimate tool call.
const maxMessageBytes = 8 << 20

// readBoundedLine reads one '\n'-terminated message, capped at maxMessageBytes. If
// the line exceeds the cap it is drained to the newline and reported truncated, so a
// hostile huge frame stays bounded instead of allocating without limit.
func readBoundedLine(r *bufio.Reader) (line []byte, truncated bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		if len(chunk) > 0 {
			if len(line)+len(chunk) <= maxMessageBytes {
				line = append(line, chunk...)
			} else {
				truncated = true // keep draining to the newline, discard the overflow
			}
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		return line, truncated, e
	}
}

func main() {
	reader := bufio.NewReaderSize(os.Stdin, 64<<10)
	writer := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(writer)
	send := func(resp rpcResponse) {
		_ = enc.Encode(resp)
		_ = writer.Flush()
	}
	for {
		raw, truncated, err := readBoundedLine(reader)
		line := []byte(strings.TrimSpace(string(raw)))
		if truncated {
			// can't trust the (partial) body to parse an id; reply with a null-id error.
			send(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32600, Message: "request exceeds max message size"}})
		} else if len(line) > 0 {
			var req rpcRequest
			if jsonErr := json.Unmarshal(line, &req); jsonErr != nil {
				// malformed JSON → structured parse error rather than a silent drop.
				send(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			} else if len(req.ID) == 0 && strings.HasPrefix(req.Method, "notifications/") {
				// notifications have no id and expect no response
			} else {
				result, rerr := dispatch(req.Method, req.Params)
				resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
				if rerr != nil {
					resp.Error = rerr
				} else {
					resp.Result = result
				}
				send(resp)
			}
		}
		if err != nil { // io.EOF or a read error: stop
			break
		}
	}
}

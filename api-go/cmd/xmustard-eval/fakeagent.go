package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The fake agent stands in for claude, codex or pi in dry runs. The harness builds
// the exact invocation it would give the real client and runs this executable
// instead (`xmustard-eval fake-agent --emulate <client> -- <client args>`), so arm
// wiring, containment, process-group handling, transcript parsing and accounting
// are exercised end to end. It speaks the client's output protocol with canned,
// deterministic numbers, calls the xMustard tools the arm configured (over MCP for
// claude/codex, over HTTP like the Pi adapter for pi), and applies the task's
// reference patch unless told to fail. It never contacts a model provider.

const (
	envFakePatch = "XMUSTARD_EVAL_FAKE_PATCH" // reference patch to apply
	envFakeSolve = "XMUSTARD_EVAL_FAKE_SOLVE" // "0" leaves the task unsolved
	envFakeProbe = "XMUSTARD_EVAL_FAKE_PROBE" // test only: paths to try reading, "|"-separated
	envFakeSleep = "XMUSTARD_EVAL_FAKE_SLEEP_MS"
	envFakeExit  = "XMUSTARD_EVAL_FAKE_EXIT" // exit status after a complete transcript
	envAsMain    = "XMUSTARD_EVAL_AS_MAIN"   // lets a test binary act as this command
)

var workspaceLine = regexp.MustCompile(`workspace_id "([^"]+)"`)

type fakeCall struct {
	tool   string
	args   map[string]string
	text   string
	isErr  bool
	server string
}

type fakeState struct {
	kind    string
	args    []string
	prompt  string
	model   string
	servers []MCPServer // claude/codex MCP servers
	piTools bool        // pi with the xMustard extension
	calls   []fakeCall
	notes   []string
	solved  bool
	usage   Usage
	cost    float64
}

func runFakeAgent(argv []string, stdin io.Reader, stdout io.Writer) int {
	if len(argv) < 3 || argv[0] != "--emulate" || argv[2] != "--" {
		fmt.Fprintln(os.Stderr, "usage: xmustard-eval fake-agent --emulate claude|codex|pi -- <client args>")
		return 2
	}
	st := &fakeState{kind: argv[1], args: argv[3:]}
	st.model = flagValue(st.args, "--model", "-m")
	enc := json.NewEncoder(stdout)
	emit := func(v any) { _ = enc.Encode(v) }
	var err error
	switch st.kind {
	case "claude":
		if cfg := flagValue(st.args, "--mcp-config"); cfg != "" {
			st.servers, err = parseClaudeMCP(cfg)
		}
		if err == nil {
			st.prompt, err = readAll(stdin)
		}
		if err == nil {
			st.work()
			st.emitClaude(emit)
		}
	case "codex":
		st.servers, err = parseCodexMCP(st.args)
		if err == nil {
			st.prompt, err = readAll(stdin)
		}
		if err == nil {
			st.work()
			st.emitCodex(emit)
		}
	case "pi":
		st.piTools = flagValue(st.args, "-e") != "" && os.Getenv("XMUSTARD_API_BASE") != ""
		err = st.piRPC(stdin, emit)
	default:
		err = fmt.Errorf("unknown client %q", st.kind)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake-agent:", err)
		return 1
	}
	if code, _ := strconv.Atoi(os.Getenv(envFakeExit)); code != 0 {
		return code
	}
	return 0
}

// work performs the canned session: orient with xMustard, probe, then patch.
func (st *fakeState) work() {
	ws := ""
	if m := workspaceLine.FindStringSubmatch(st.prompt); m != nil {
		ws = m[1]
	}
	query := strings.TrimSpace(strings.SplitN(st.prompt, "\n", 2)[0])
	if ws != "" {
		for _, s := range st.servers {
			if s.Name != "xmustard" {
				continue
			}
			st.calls = append(st.calls, mcpSession(s, []fakeCall{
				{tool: "ground", args: map[string]string{"workspace_id": ws}},
				{tool: "recall", args: map[string]string{"workspace_id": ws, "query": query}},
			})...)
		}
		if st.piTools {
			for _, c := range []fakeCall{
				{tool: "ground", args: map[string]string{"workspace_id": ws}},
				{tool: "recall", args: map[string]string{"workspace_id": ws, "query": query}},
			} {
				c.text, c.isErr = piHTTPTool(c.tool, c.args)
				st.calls = append(st.calls, c)
			}
		}
	}
	for _, s := range st.servers {
		if s.Name != "xmustard" {
			st.calls = append(st.calls, mcpSession(s, nil)...)
		}
	}
	for _, p := range strings.Split(os.Getenv(envFakeProbe), "|") {
		if p == "" {
			continue
		}
		if _, err := os.ReadFile(p); err == nil {
			st.notes = append(st.notes, "probe "+p+": readable")
		} else {
			st.notes = append(st.notes, "probe "+p+": denied")
		}
	}
	if ms, _ := strconv.Atoi(os.Getenv(envFakeSleep)); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if patch := os.Getenv(envFakePatch); patch != "" && os.Getenv(envFakeSolve) != "0" {
		if err := applyPatch(".", patch); err != nil {
			st.notes = append(st.notes, "patch failed: "+err.Error())
		} else {
			st.solved = true
		}
	}
	// Deterministic usage: fixed base plus the volume the session actually moved.
	toolBytes := 0
	for _, c := range st.calls {
		toolBytes += len(c.text)
	}
	st.usage = Usage{
		Input:      int64(1000 + len(st.prompt)/4 + toolBytes/4),
		CacheRead:  4000,
		CacheWrite: 500,
		Output:     int64(200 + 20*len(st.calls)),
	}
	st.usage.Total = st.usage.Input + st.usage.CacheRead + st.usage.CacheWrite + st.usage.Output
	st.cost = (float64(st.usage.Input)*3 + float64(st.usage.CacheRead)*0.3 + float64(st.usage.CacheWrite)*3.75 + float64(st.usage.Output)*15) / 1e6
}

func (st *fakeState) summary() string {
	s := "fake agent done; solved=" + strconv.FormatBool(st.solved)
	if len(st.notes) > 0 {
		s += "\n" + strings.Join(st.notes, "\n")
	}
	return s
}

func (st *fakeState) serverNames() []string {
	var out []string
	for _, s := range st.servers {
		out = append(out, s.Name)
	}
	return out
}

func (st *fakeState) emitClaude(emit func(any)) {
	var servers []any
	for _, s := range st.servers {
		servers = append(servers, map[string]any{"name": s.Name, "status": "connected"})
	}
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": "fake", "model": st.model, "mcp_servers": servers})
	emit(map[string]any{"type": "xm.fake.config", "args": st.args, "mcp_servers": st.serverNames()})
	for i, c := range st.calls {
		id := "toolu_fake_" + strconv.Itoa(i)
		name := "mcp__" + c.server + "__" + c.tool
		emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "model": st.model,
			"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": c.args}}}})
		emit(map[string]any{"type": "user", "message": map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": c.isErr,
				"content": []any{map[string]any{"type": "text", "text": c.text}}}}}})
	}
	if st.solved {
		emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
			"content": []any{map[string]any{"type": "tool_use", "id": "toolu_fake_edit", "name": "Edit", "input": map[string]any{}}}}})
	}
	emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": st.summary()}}}})
	u := st.usage
	emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": len(st.calls) + 1,
		"result": st.summary(), "session_id": "fake", "total_cost_usd": st.cost,
		"usage": map[string]any{"input_tokens": u.Input, "cache_read_input_tokens": u.CacheRead, "cache_creation_input_tokens": u.CacheWrite, "output_tokens": u.Output},
		"modelUsage": map[string]any{firstNonEmpty(st.model, "fake-model"): map[string]any{"inputTokens": u.Input, "outputTokens": u.Output,
			"cacheReadInputTokens": u.CacheRead, "cacheCreationInputTokens": u.CacheWrite, "costUSD": st.cost}}})
}

func (st *fakeState) emitCodex(emit func(any)) {
	emit(map[string]any{"type": "thread.started", "thread_id": "fake"})
	emit(map[string]any{"type": "xm.fake.config", "args": st.args, "mcp_servers": st.serverNames()})
	emit(map[string]any{"type": "turn.started"})
	for i, c := range st.calls {
		item := map[string]any{"id": "item_" + strconv.Itoa(i), "type": "mcp_tool_call", "server": c.server, "tool": c.tool, "arguments": c.args,
			"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": c.text}}}, "status": "completed"}
		if c.isErr {
			item["status"] = "failed"
		}
		emit(map[string]any{"type": "item.completed", "item": item})
	}
	if st.solved {
		emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_edit", "type": "file_change", "changes": []any{}, "status": "completed"}})
	}
	emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_msg", "type": "agent_message", "text": st.summary()}})
	u := st.usage
	emit(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": u.Input + u.CacheRead, "cached_input_tokens": u.CacheRead,
		"output_tokens": u.Output, "reasoning_output_tokens": 40}})
}

// piRPC answers Pi's RPC protocol: one prompt, then get_session_stats, until EOF.
func (st *fakeState) piRPC(stdin io.Reader, emit func(any)) error {
	emit(map[string]any{"type": "xm.fake.config", "args": st.args, "xmustard_tools": st.piTools})
	br := bufio.NewReader(stdin)
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var cmd map[string]any
			if json.Unmarshal(line, &cmd) != nil {
				emit(map[string]any{"type": "response", "command": "parse", "success": false, "error": "bad json"})
				continue
			}
			id := str(cmd["id"])
			switch str(cmd["type"]) {
			case "prompt":
				st.prompt = str(cmd["message"])
				emit(map[string]any{"id": id, "type": "response", "command": "prompt", "success": true})
				emit(map[string]any{"type": "agent_start"})
				emit(map[string]any{"type": "turn_start"})
				st.work()
				for i, c := range st.calls {
					cid := "call_" + strconv.Itoa(i)
					emit(map[string]any{"type": "tool_execution_start", "toolCallId": cid, "toolName": c.tool, "args": c.args})
					emit(map[string]any{"type": "tool_execution_end", "toolCallId": cid, "toolName": c.tool, "isError": c.isErr,
						"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": c.text}}}})
				}
				if st.solved {
					emit(map[string]any{"type": "tool_execution_end", "toolCallId": "call_edit", "toolName": "edit", "isError": false,
						"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "edited"}}}})
				}
				u := st.usage
				emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "model": st.model, "stopReason": "stop",
					"content": []any{map[string]any{"type": "text", "text": st.summary()}},
					"usage": map[string]any{"input": u.Input, "output": u.Output, "cacheRead": u.CacheRead, "cacheWrite": u.CacheWrite, "reasoning": 40,
						"totalTokens": u.Total, "cost": map[string]any{"total": st.cost}}}})
				emit(map[string]any{"type": "turn_end"})
				emit(map[string]any{"type": "agent_end", "messages": []any{}})
				emit(map[string]any{"type": "agent_settled"})
			case "get_session_stats":
				u := st.usage
				emit(map[string]any{"id": id, "type": "response", "command": "get_session_stats", "success": true,
					"data": map[string]any{"sessionId": "fake", "toolCalls": len(st.calls),
						"tokens": map[string]any{"input": u.Input, "output": u.Output, "cacheRead": u.CacheRead, "cacheWrite": u.CacheWrite, "total": u.Total},
						"cost":   st.cost}})
			default:
				emit(map[string]any{"id": id, "type": "response", "command": str(cmd["type"]), "success": false, "error": "unsupported in fake agent"})
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// mcpSession runs one stdio MCP server: initialize, tools/list, then the calls.
// A server that fails is reported as an error result, as a client would.
func mcpSession(s MCPServer, calls []fakeCall) []fakeCall {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = os.Environ()
	for _, k := range sortedKeys(s.Env) {
		cmd.Env = append(cmd.Env, k+"="+s.Env[k])
	}
	fail := func(err error) []fakeCall {
		var out []fakeCall
		for _, c := range calls {
			c.server, c.text, c.isErr = s.Name, err.Error(), true
			out = append(out, c)
		}
		return out
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	defer func() {
		_ = in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	dec := json.NewDecoder(bufio.NewReader(outPipe))
	enc := json.NewEncoder(in)
	id := 0
	rpc := func(method string, params any) (map[string]any, error) {
		id++
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		for {
			var msg map[string]any
			if err := dec.Decode(&msg); err != nil {
				return nil, err
			}
			if int(num(msg["id"])) != id {
				continue // notifications or stray replies
			}
			if e := obj(msg["error"]); e != nil {
				return nil, errors.New(str(e["message"]))
			}
			return obj(msg["result"]), nil
		}
	}
	if _, err := rpc("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "xmustard-eval-fake", "version": "0"}}); err != nil {
		return fail(err)
	}
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if _, err := rpc("tools/list", map[string]any{}); err != nil {
		return fail(err)
	}
	var out []fakeCall
	for _, c := range calls {
		c.server = s.Name
		res, err := rpc("tools/call", map[string]any{"name": c.tool, "arguments": c.args})
		if err != nil {
			c.text, c.isErr = err.Error(), true
		} else {
			c.text, c.isErr = contentText(res["content"]), boolean(res["isError"])
		}
		out = append(out, c)
	}
	return out
}

// piHTTPTool calls a tool route directly, as the Pi adapter does.
func piHTTPTool(tool string, args map[string]string) (string, bool) {
	base := strings.TrimRight(os.Getenv("XMUSTARD_API_BASE"), "/")
	ws := url.PathEscape(args["workspace_id"])
	path := "/api/workspaces/" + ws + "/session-grounding"
	if tool == "recall" {
		path = "/api/workspaces/" + ws + "/context/active?query=" + url.QueryEscape(args["query"])
	}
	req, err := http.NewRequest("GET", base+path, nil)
	if err != nil {
		return err.Error(), true
	}
	if tok := os.Getenv("XMUSTARD_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return err.Error(), true
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode != http.StatusOK
}

func readAll(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	return string(b), err
}

// flagValue returns the value following the first matching flag.
func flagValue(args []string, names ...string) string {
	for i := 0; i+1 < len(args); i++ {
		for _, n := range names {
			if args[i] == n {
				return args[i+1]
			}
		}
	}
	return ""
}

func parseClaudeMCP(raw string) ([]MCPServer, error) {
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("--mcp-config: %w", err)
	}
	var out []MCPServer
	for _, name := range sortedKeys(cfg.MCPServers) {
		s := cfg.MCPServers[name]
		out = append(out, MCPServer{Name: name, Command: s.Command, Args: s.Args, Env: s.Env})
	}
	return out, nil
}

// parseCodexMCP reads the `-c mcp_servers.<name>.<field>=<toml>` overrides the codex
// driver emits.
func parseCodexMCP(args []string) ([]MCPServer, error) {
	byName := map[string]*MCPServer{}
	disabled := map[string]bool{}
	var order []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-c" {
			continue
		}
		kv := args[i+1]
		rest, ok := strings.CutPrefix(kv, "mcp_servers.")
		if !ok {
			continue
		}
		key, val, ok := strings.Cut(rest, "=")
		if !ok {
			return nil, fmt.Errorf("bad override %q", kv)
		}
		parts := strings.Split(key, ".")
		name := parts[0]
		s := byName[name]
		if s == nil {
			s = &MCPServer{Name: name, Env: map[string]string{}}
			byName[name] = s
			order = append(order, name)
		}
		var err error
		switch {
		case len(parts) == 2 && parts[1] == "command":
			s.Command, err = parseTOMLString(val)
		case len(parts) == 2 && parts[1] == "args":
			s.Args, err = parseTOMLStringArray(val)
		case len(parts) == 3 && parts[1] == "env":
			s.Env[parts[2]], err = parseTOMLString(val)
		case len(parts) == 2 && parts[1] == "enabled":
			disabled[name] = val == "false"
		default:
			err = fmt.Errorf("unsupported override %q", kv)
		}
		if err != nil {
			return nil, err
		}
	}
	var out []MCPServer
	for _, n := range order {
		if !disabled[n] && byName[n].Command != "" {
			out = append(out, *byName[n])
		}
	}
	return out, nil
}

func parseTOMLString(v string) (string, error) {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return "", fmt.Errorf("not a TOML basic string: %s", v)
	}
	s, err := strconv.Unquote(v)
	if err != nil {
		return "", fmt.Errorf("TOML string %s: %w", v, err)
	}
	return s, nil
}

func parseTOMLStringArray(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, fmt.Errorf("not a TOML array: %s", v)
	}
	inner := strings.TrimSpace(v[1 : len(v)-1])
	var out []string
	for inner != "" {
		if inner[0] != '"' {
			return nil, fmt.Errorf("TOML array element: %s", inner)
		}
		end := 1
		for end < len(inner) && inner[end] != '"' {
			if inner[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(inner) {
			return nil, fmt.Errorf("unterminated TOML string in %s", v)
		}
		s, err := parseTOMLString(inner[:end+1])
		if err != nil {
			return nil, err
		}
		out = append(out, s)
		inner = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(inner[end+1:]), ","))
	}
	return out, nil
}

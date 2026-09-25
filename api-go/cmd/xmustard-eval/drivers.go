package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Drivers launch one headless client per run and parse its event stream:
//
//	claude  claude -p --output-format stream-json; final event {"type":"result"}
//	codex   codex exec --json; final events {"type":"turn.completed"|"turn.failed"}
//	pi      pi --mode rpc; the harness sends a prompt, waits for agent_settled, then
//	        asks get_session_stats, whose response is the final accounting event
//
// Token and cost numbers come only from those final events (plus a price table for
// codex, which reports no cost). Nothing is estimated from text length.
var driverNames = []string{"claude", "codex", "pi"}

// xmustardTools are the nine agent tools plus the Pi adapter's expansion tool.
var xmustardTools = []string{"ground", "recall", "remember", "verify", "search", "explain", "impact", "diagnostics", "why_failed", "xmustard_expand"}

// Usage is token usage as the client reported it.
type Usage struct {
	Input      int64 `json:"input"` // uncached input tokens
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning,omitempty"` // subset of Output, where the client reports it
	Total      int64 `json:"total"`
}

// ToolResult is one xMustard (or peer) tool result exactly as the model received it.
type ToolResult struct {
	Tool    string `json:"tool"`
	Text    string `json:"-"`
	Bytes   int    `json:"bytes"`
	IsError bool   `json:"is_error,omitempty"`
}

// Transcript is what the harness extracts from one client event stream.
type Transcript struct {
	FinalEvent    bool           `json:"final_event"`
	FinalKind     string         `json:"final_kind,omitempty"`
	Usage         Usage          `json:"usage"`
	UsageSource   string         `json:"usage_source,omitempty"`
	CostUSD       *float64       `json:"cost_usd,omitempty"`
	CostSource    string         `json:"cost_source"`
	Model         string         `json:"model,omitempty"`
	NumTurns      int            `json:"num_turns"`
	IsError       bool           `json:"is_error,omitempty"`
	ErrorText     string         `json:"error_text,omitempty"`
	ToolCalls     map[string]int `json:"tool_calls"`
	MCPServers    []string       `json:"mcp_servers,omitempty"`
	XmResults     []ToolResult   `json:"xmustard_results,omitempty"`
	PeerResults   []ToolResult   `json:"peer_results,omitempty"`
	Events        int            `json:"events"`
	UnparsedLines int            `json:"unparsed_lines,omitempty"`
}

// Cost sources.
const (
	CostClientFinal   = "client_final_event"
	CostClientMessage = "client_message_events" // pi without a stats response
	CostPriceTable    = "price_table"
	CostUnpriced      = "unpriced"
)

// DriverRequest is everything a driver needs to build one client invocation.
type DriverRequest struct {
	Arm          Arm
	Model        string
	WorkDir      string
	Servers      []MCPServer       // MCP servers this arm adds (xMustard or a peer)
	PiEnv        map[string]string // Pi adapter environment for xMustard arms
	HookArgs     []string
	Client       ClientConfig
	UserServers  []string // codex: operator-configured MCP servers to disable
	XmServerName string   // MCP server name the xMustard tools are registered under
	// ConfigDir is a private per-run directory for client config files, so server
	// credentials never appear in a command line (other local users can read argv).
	ConfigDir string
	// ExternalSandbox is set when the harness runs the client under sandbox-exec. A
	// macOS seatbelt cannot be applied inside another one (`codex sandbox macos` fails
	// with "sandbox_apply: Operation not permitted" under sandbox-exec), so codex is
	// told the sandbox is external instead of failing every shell command.
	ExternalSandbox bool
}

// Invocation is a concrete client command.
type Invocation struct {
	Bin   string
	Args  []string
	Env   []string // additions to the scrubbed environment
	Stdin string   // one-shot prompt (claude, codex)
}

// Driver builds invocations and parses transcripts for one client.
type Driver interface {
	Build(req DriverRequest, prompt string) (Invocation, error)
	Parse(r io.Reader, xmServer string) Transcript
	// Interactive reports whether the prompt is delivered over an RPC session.
	Interactive() bool
}

func driverByName(name string) (Driver, error) {
	switch name {
	case "claude":
		return claudeDriver{}, nil
	case "codex":
		return codexDriver{}, nil
	case "pi":
		return piDriver{}, nil
	}
	return nil, fmt.Errorf("unknown driver %q (want one of %v)", name, driverNames)
}

func binOr(c ClientConfig, def string) string {
	if c.Bin != "" {
		return c.Bin
	}
	return def
}

// ---- claude ----

type claudeDriver struct{}

func (claudeDriver) Interactive() bool { return false }

func (claudeDriver) Build(req DriverRequest, prompt string) (Invocation, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--no-session-persistence",
		"--permission-mode", "bypassPermissions"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Client.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(req.Client.MaxBudgetUSD, 'f', -1, 64))
	}
	if req.Arm.IsolateMCP {
		servers := map[string]any{}
		for _, s := range req.Servers {
			servers[s.Name] = map[string]any{"type": "stdio", "command": s.Command, "args": nonNil(s.Args), "env": nonNilMap(s.Env)}
		}
		cfg, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil {
			return Invocation{}, err
		}
		path := filepath.Join(req.ConfigDir, "mcp.json")
		if err := os.WriteFile(path, cfg, 0o600); err != nil {
			return Invocation{}, err
		}
		// --mcp-config is variadic; --strict-mcp-config after it ends the list.
		args = append(args, "--mcp-config", path, "--strict-mcp-config")
	}
	args = append(args, req.HookArgs...)
	args = append(args, req.Client.Args...)
	return Invocation{Bin: binOr(req.Client, "claude"), Args: args, Stdin: prompt}, nil
}

func (claudeDriver) Parse(r io.Reader, xmServer string) Transcript {
	t := Transcript{ToolCalls: map[string]int{}, CostSource: CostUnpriced}
	idName := map[string]string{}
	xmPrefix := "mcp__" + xmServer + "__"
	var fallback Usage
	eachJSONLine(r, &t, func(ev map[string]any) {
		switch str(ev["type"]) {
		case "system":
			if str(ev["subtype"]) == "init" {
				t.Model = str(ev["model"])
				for _, s := range arr(ev["mcp_servers"]) {
					m := obj(s)
					t.MCPServers = append(t.MCPServers, str(m["name"])+":"+str(m["status"]))
				}
			}
		case "assistant":
			for _, b := range arr(obj(ev["message"])["content"]) {
				blk := obj(b)
				if str(blk["type"]) == "tool_use" {
					name := str(blk["name"])
					idName[str(blk["id"])] = name
					t.ToolCalls[name]++
				}
			}
		case "user":
			for _, b := range arr(obj(ev["message"])["content"]) {
				blk := obj(b)
				if str(blk["type"]) != "tool_result" {
					continue
				}
				name := idName[str(blk["tool_use_id"])]
				text := contentText(blk["content"])
				res := ToolResult{Tool: name, Text: text, Bytes: len(text), IsError: boolean(blk["is_error"])}
				switch {
				case xmServer != "" && strings.HasPrefix(name, xmPrefix):
					res.Tool = strings.TrimPrefix(name, xmPrefix)
					t.XmResults = append(t.XmResults, res)
				case strings.HasPrefix(name, "mcp__"):
					t.PeerResults = append(t.PeerResults, res)
				}
			}
		case "result":
			t.FinalEvent, t.FinalKind = true, "result"
			t.IsError = boolean(ev["is_error"]) || str(ev["subtype"]) != "success"
			if t.IsError {
				t.ErrorText = firstNonEmpty(str(ev["result"]), str(ev["subtype"]))
			}
			t.NumTurns = int(num(ev["num_turns"]))
			if c, ok := ev["total_cost_usd"].(float64); ok {
				t.CostUSD, t.CostSource = &c, CostClientFinal
			}
			u := obj(ev["usage"])
			fallback = Usage{
				Input:      int64(num(u["input_tokens"])),
				CacheWrite: int64(num(u["cache_creation_input_tokens"])),
				CacheRead:  int64(num(u["cache_read_input_tokens"])),
				Output:     int64(num(u["output_tokens"])),
			}
			// modelUsage covers every model the session billed (the cost figure does
			// too), so it is preferred over the main-model usage block when present.
			var mu Usage
			mm := obj(ev["modelUsage"])
			for _, k := range sortedKeys(mm) {
				m := obj(mm[k])
				mu.Input += int64(num(m["inputTokens"]))
				mu.Output += int64(num(m["outputTokens"]))
				mu.CacheRead += int64(num(m["cacheReadInputTokens"]))
				mu.CacheWrite += int64(num(m["cacheCreationInputTokens"]))
			}
			if len(mm) > 0 {
				t.Usage, t.UsageSource = mu, "result.modelUsage"
			} else {
				t.Usage, t.UsageSource = fallback, "result.usage"
			}
		}
	})
	t.Usage.Total = t.Usage.Input + t.Usage.CacheRead + t.Usage.CacheWrite + t.Usage.Output
	return t
}

// ---- codex ----

type codexDriver struct{}

func (codexDriver) Interactive() bool { return false }

func (codexDriver) Build(req DriverRequest, prompt string) (Invocation, error) {
	sandbox := "workspace-write"
	if req.ExternalSandbox {
		sandbox = "danger-full-access"
	}
	args := []string{"exec", "--json", "--color", "never", "-C", req.WorkDir, "--sandbox", sandbox}
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	if req.Arm.IsolateMCP {
		// `-c mcp_servers={}` merges instead of replacing (checked against codex-cli
		// 0.156.1 `codex mcp list`), so each operator server is disabled by name.
		for _, n := range req.UserServers {
			if !tomlBareKey(n) {
				return Invocation{}, fmt.Errorf("codex MCP server %q cannot be disabled by a dotted -c key", n)
			}
			args = append(args, "-c", "mcp_servers."+n+".enabled=false")
		}
	}
	var env []string
	for _, s := range req.Servers {
		if !tomlBareKey(s.Name) {
			return Invocation{}, fmt.Errorf("MCP server name %q is not a bare TOML key", s.Name)
		}
		p := "mcp_servers." + s.Name + "."
		args = append(args, "-c", p+"command="+tomlString(s.Command), "-c", p+"args="+tomlStringArray(s.Args))
		// Values travel in codex's environment and are forwarded by name (env_vars),
		// so credentials stay out of the command line.
		keys := sortedKeys(s.Env)
		if len(keys) > 0 {
			args = append(args, "-c", p+"env_vars="+tomlStringArray(keys))
		}
		for _, k := range keys {
			env = append(env, k+"="+s.Env[k])
		}
	}
	args = append(args, req.HookArgs...)
	args = append(args, req.Client.Args...)
	args = append(args, "-") // prompt from stdin
	return Invocation{Bin: binOr(req.Client, "codex"), Args: args, Env: env, Stdin: prompt}, nil
}

func (codexDriver) Parse(r io.Reader, xmServer string) Transcript {
	t := Transcript{ToolCalls: map[string]int{}, CostSource: CostUnpriced, UsageSource: "turn.completed"}
	var input, cached, output, reasoning int64
	eachJSONLine(r, &t, func(ev map[string]any) {
		switch str(ev["type"]) {
		case "item.completed":
			it := obj(ev["item"])
			switch str(it["type"]) {
			case "mcp_tool_call":
				server, tool := str(it["server"]), str(it["tool"])
				t.ToolCalls["mcp__"+server+"__"+tool]++
				text := ""
				for _, c := range arr(obj(it["result"])["content"]) {
					if s := str(obj(c)["text"]); s != "" {
						text += s
					}
				}
				isErr := str(it["status"]) == "failed" || it["error"] != nil
				if text == "" && isErr {
					text = str(obj(it["error"])["message"])
				}
				res := ToolResult{Tool: tool, Text: text, Bytes: len(text), IsError: isErr}
				if xmServer != "" && server == xmServer {
					t.XmResults = append(t.XmResults, res)
				} else {
					res.Tool = server + "." + tool
					t.PeerResults = append(t.PeerResults, res)
				}
			case "command_execution":
				t.ToolCalls["shell"]++
			case "file_change":
				t.ToolCalls["apply_patch"]++
			case "web_search":
				t.ToolCalls["web_search"]++
			}
		case "turn.completed":
			t.FinalEvent, t.FinalKind = true, "turn.completed"
			t.NumTurns++
			u := obj(ev["usage"])
			input += int64(num(u["input_tokens"]))
			cached += int64(num(u["cached_input_tokens"]))
			output += int64(num(u["output_tokens"]))
			reasoning += int64(num(u["reasoning_output_tokens"]))
		case "turn.failed":
			t.FinalEvent, t.FinalKind = true, "turn.failed"
			t.NumTurns++
			t.IsError = true
			t.ErrorText = str(obj(ev["error"])["message"])
		case "error":
			t.IsError = true
			t.ErrorText = firstNonEmpty(t.ErrorText, str(ev["message"]))
		}
	})
	// OpenAI usage counts cached tokens inside input_tokens and reasoning inside
	// output_tokens; split the cache out so Input is the uncached share.
	t.Usage = Usage{Input: max(input-cached, 0), CacheRead: cached, Output: output, Reasoning: reasoning, Total: input + output}
	return t
}

// ---- pi ----

type piDriver struct{}

func (piDriver) Interactive() bool { return true }

func (piDriver) Build(req DriverRequest, _ string) (Invocation, error) {
	args := []string{"--mode", "rpc", "--no-session"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Arm.IsolateMCP {
		args = append(args, "--no-extensions")
	}
	var env []string
	if req.Arm.UsesStack {
		if req.Client.Extension == "" {
			return Invocation{}, errors.New("pi: clients.pi.extension (the xMustard Pi adapter) is required for xMustard arms")
		}
		args = append(args, "-e", req.Client.Extension)
		for _, k := range sortedKeys(req.PiEnv) {
			env = append(env, k+"="+req.PiEnv[k])
		}
	}
	args = append(args, req.HookArgs...)
	args = append(args, req.Client.Args...)
	return Invocation{Bin: binOr(req.Client, "pi"), Args: args, Env: env}, nil
}

const (
	piPromptID = "xm-eval-prompt"
	piStatsID  = "xm-eval-stats"
)

func (piDriver) Parse(r io.Reader, _ string) Transcript {
	t := Transcript{ToolCalls: map[string]int{}, CostSource: CostUnpriced}
	var msgUsage Usage
	var msgCost float64
	msgCostSeen := false
	eachJSONLine(r, &t, func(ev map[string]any) {
		switch str(ev["type"]) {
		case "response":
			switch str(ev["command"]) {
			case "prompt":
				if success, ok := ev["success"].(bool); ok && !success {
					t.IsError = true
					t.ErrorText = str(ev["error"])
				}
			case "get_session_stats":
				if boolean(ev["success"]) {
					d := obj(ev["data"])
					tok := obj(d["tokens"])
					t.FinalEvent, t.FinalKind = true, "get_session_stats"
					t.Usage = Usage{
						Input:      int64(num(tok["input"])),
						Output:     int64(num(tok["output"])),
						CacheRead:  int64(num(tok["cacheRead"])),
						CacheWrite: int64(num(tok["cacheWrite"])),
						Total:      int64(num(tok["total"])),
					}
					t.UsageSource = "get_session_stats"
					if c, ok := d["cost"].(float64); ok {
						t.CostUSD, t.CostSource = &c, CostClientFinal
					}
				}
			}
		case "message_end":
			m := obj(ev["message"])
			if str(m["role"]) != "assistant" {
				return
			}
			if s := str(m["model"]); s != "" {
				t.Model = s
			}
			if str(m["stopReason"]) == "error" {
				t.IsError = true
				t.ErrorText = firstNonEmpty(str(m["errorMessage"]), t.ErrorText)
			}
			u := obj(m["usage"])
			msgUsage.Input += int64(num(u["input"]))
			msgUsage.Output += int64(num(u["output"]))
			msgUsage.CacheRead += int64(num(u["cacheRead"]))
			msgUsage.CacheWrite += int64(num(u["cacheWrite"]))
			msgUsage.Reasoning += int64(num(u["reasoning"]))
			msgUsage.Total += int64(num(u["totalTokens"]))
			if c, ok := obj(u["cost"])["total"].(float64); ok {
				msgCost += c
				msgCostSeen = true
			}
		case "turn_end":
			t.NumTurns++
		case "tool_execution_end":
			name := str(ev["toolName"])
			t.ToolCalls[name]++
			if slices.Contains(xmustardTools, name) {
				text := contentText(obj(ev["result"])["content"])
				t.XmResults = append(t.XmResults, ToolResult{Tool: name, Text: text, Bytes: len(text), IsError: boolean(ev["isError"])})
			}
		}
	})
	if !t.FinalEvent {
		// No stats response: report the per-message sums, labelled as such.
		t.Usage, t.UsageSource = msgUsage, "message_end"
		if msgCostSeen {
			c := math.Round(msgCost*1e9) / 1e9 // summing float costs; drop the ulp noise
			t.CostUSD, t.CostSource = &c, CostClientMessage
		}
	} else {
		t.Usage.Reasoning = msgUsage.Reasoning
	}
	return t
}

// ---- running a client ----

// ClientExit is how a client process ended.
type ClientExit struct {
	PID      int   `json:"pid"` // process-group leader; the group is dead when the record is written
	ExitCode int   `json:"exit_code"`
	TimedOut bool  `json:"timed_out,omitempty"`
	Canceled bool  `json:"canceled,omitempty"`
	WallMS   int64 `json:"wall_ms"`
}

// runClient starts the client in its own process group, feeds it the prompt (one-shot
// on stdin, or over Pi's RPC session), tees stdout to transcriptPath and stderr to
// stderrPath, and kills the whole group on exit, timeout or cancellation. onStart
// receives the leader pid (for RSS attribution).
func runClient(ctx context.Context, d Driver, inv Invocation, dir, prompt, transcriptPath, stderrPath string, timeout time.Duration, onStart func(pid int)) (ClientExit, error) {
	var ex ClientExit
	tf, err := os.Create(transcriptPath)
	if err != nil {
		return ex, err
	}
	defer tf.Close()
	ef, err := os.Create(stderrPath)
	if err != nil {
		return ex, err
	}
	defer ef.Close()

	cmd := exec.Command(inv.Bin, inv.Args...)
	cmd.Dir = dir
	cmd.Env = append(scrubbedEnv(), inv.Env...)
	cmd.Stderr = ef
	// stdout goes through a pipe the harness reads line by line (Pi answers events);
	// WaitDelay force-closes it if a straggler still holds it after the client exits.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.WaitDelay = 2 * time.Second
	var stdin io.WriteCloser
	if d.Interactive() {
		if stdin, err = cmd.StdinPipe(); err != nil {
			return ex, err
		}
	} else {
		cmd.Stdin = strings.NewReader(inv.Stdin)
	}
	start := time.Now()
	if err := startGroup(cmd); err != nil {
		return ex, fmt.Errorf("start %s: %w", inv.Bin, err)
	}
	pid := cmd.Process.Pid
	ex.PID = pid
	if onStart != nil {
		onStart(pid)
	}

	pumpDone := make(chan error, 1)
	go func() {
		var err error
		if d.Interactive() {
			err = piSession(pr, tf, stdin, prompt)
		} else {
			_, err = io.Copy(tf, pr)
		}
		_, _ = io.Copy(io.Discard, pr) // keep draining so the writer never blocks
		pumpDone <- err
	}()
	waitDone := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		waitDone <- err
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-timer.C:
		ex.TimedOut = true
		killGroup(pid, 3*time.Second)
		waitErr = <-waitDone
	case <-ctx.Done():
		ex.Canceled = true
		killGroup(pid, 3*time.Second)
		waitErr = <-waitDone
	}
	ex.WallMS = time.Since(start).Milliseconds()
	// Stragglers (MCP servers, background shells) die with the run.
	killGroup(pid, 2*time.Second)
	pumpErr := <-pumpDone
	ex.ExitCode = -1
	if cmd.ProcessState != nil {
		ex.ExitCode = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, exec.ErrWaitDelay) && !ex.TimedOut && !ex.Canceled {
		return ex, waitErr
	}
	if pumpErr != nil && !ex.TimedOut && !ex.Canceled {
		return ex, fmt.Errorf("read client output: %w", pumpErr)
	}
	return ex, nil
}

// piSession drives one Pi RPC exchange: send the prompt; when the agent settles (or
// the prompt is refused) ask for session stats; close stdin once they arrive so Pi
// exits. Every stdout line is copied to the transcript.
func piSession(stdout io.Reader, transcript io.Writer, stdin io.WriteCloser, prompt string) error {
	var once sync.Once
	closeIn := func() { once.Do(func() { _ = stdin.Close() }) }
	defer closeIn()
	write := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = stdin.Write(append(b, '\n'))
		return err
	}
	if err := write(map[string]any{"id": piPromptID, "type": "prompt", "message": prompt}); err != nil {
		return err
	}
	statsAsked := false
	askStats := func() {
		if !statsAsked {
			statsAsked = true
			if write(map[string]any{"id": piStatsID, "type": "get_session_stats"}) != nil {
				closeIn()
			}
		}
	}
	br := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := transcript.Write(line); werr != nil {
				return werr
			}
			var ev map[string]any
			if json.Unmarshal(bytes.TrimSpace(line), &ev) == nil {
				switch str(ev["type"]) {
				case "agent_settled":
					askStats()
				case "response":
					switch {
					case str(ev["id"]) == piStatsID:
						closeIn()
					case str(ev["id"]) == piPromptID && !boolean(ev["success"]):
						askStats()
					}
				}
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

// ---- small JSON helpers ----

func eachJSONLine(r io.Reader, t *Transcript, fn func(map[string]any)) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if s := bytes.TrimSpace(line); len(s) > 0 {
			var ev map[string]any
			if json.Unmarshal(s, &ev) == nil {
				t.Events++
				fn(ev)
			} else {
				t.UnparsedLines++
			}
		}
		if err != nil {
			return
		}
	}
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func arr(v any) []any {
	a, _ := v.([]any)
	return a
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func boolean(v any) bool {
	b, _ := v.(bool)
	return b
}

// contentText flattens an MCP/Anthropic content value (a string or a list of text
// blocks) into the text the model saw.
func contentText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var b strings.Builder
	for _, c := range arr(v) {
		if s := str(obj(c)["text"]); s != "" {
			b.WriteString(s)
		}
	}
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func tomlBareKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// tomlString encodes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlStringArray(list []string) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = tomlString(s)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// codexUserServers lists the MCP servers the operator configured for codex, so the
// isolated arms can disable each one. It runs `codex mcp list --json`, which makes no
// model call.
func codexUserServers(bin string) ([]string, error) {
	cmd := exec.Command(bin, "mcp", "list", "--json")
	cmd.Env = scrubbedEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("codex mcp list --json: %w", err)
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("codex mcp list --json: %w", err)
	}
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names, nil
}

// clientVersion records `<bin> --version` for the run manifest (no model call).
func clientVersion(bin string) string {
	cmd := exec.Command(bin, "--version")
	cmd.Env = scrubbedEnv()
	out, err := cmd.Output()
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	return strings.TrimSpace(string(out))
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Canned final-event shapes, one per client. Numbers are chosen so every field is
// distinguishable in the assertions.

const claudeStream = `{"type":"system","subtype":"init","model":"claude-sonnet-4-5","mcp_servers":[{"name":"xmustard","status":"connected"}]}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"mcp__xmustard__recall","input":{"workspace_id":"w"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"{\"entries\":[]}"}]}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{}},{"type":"tool_use","id":"t3","name":"mcp__serena__find_symbol","input":{}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"},{"type":"tool_result","tool_use_id":"t3","content":"sym","is_error":true}]}}
not json at all
{"type":"result","subtype":"success","is_error":false,"num_turns":4,"total_cost_usd":0.1234,"usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":40},"modelUsage":{"claude-sonnet-4-5":{"inputTokens":100,"outputTokens":200,"cacheReadInputTokens":300,"cacheCreationInputTokens":400,"costUSD":0.12},"claude-haiku-4-5":{"inputTokens":1,"outputTokens":2,"cacheReadInputTokens":3,"cacheCreationInputTokens":4,"costUSD":0.0034}}}
`

func TestParseClaudeFinalEvent(t *testing.T) {
	tr := claudeDriver{}.Parse(strings.NewReader(claudeStream), "xmustard")
	if !tr.FinalEvent || tr.FinalKind != "result" || tr.IsError {
		t.Fatalf("final event: %+v", tr)
	}
	want := Usage{Input: 101, Output: 202, CacheRead: 303, CacheWrite: 404, Total: 1010}
	if tr.Usage != want || tr.UsageSource != "result.modelUsage" || !tr.UsageReported {
		t.Fatalf("usage %+v (%s, reported %v), want %+v", tr.Usage, tr.UsageSource, tr.UsageReported, want)
	}
	if tr.CostUSD == nil || *tr.CostUSD != 0.1234 || tr.CostSource != CostClientFinal {
		t.Fatalf("cost %v %s", tr.CostUSD, tr.CostSource)
	}
	if tr.NumTurns != 4 || tr.Model != "claude-sonnet-4-5" || tr.UnparsedLines != 1 {
		t.Fatalf("turns %d model %q unparsed %d", tr.NumTurns, tr.Model, tr.UnparsedLines)
	}
	if tr.ToolCalls["mcp__xmustard__recall"] != 1 || tr.ToolCalls["Bash"] != 1 || tr.ToolCalls["mcp__serena__find_symbol"] != 1 {
		t.Fatalf("tool calls %v", tr.ToolCalls)
	}
	if len(tr.XmResults) != 1 || tr.XmResults[0].Tool != "recall" || tr.XmResults[0].Text != `{"entries":[]}` {
		t.Fatalf("xmustard results %+v", tr.XmResults)
	}
	if len(tr.PeerResults) != 1 || !tr.PeerResults[0].IsError {
		t.Fatalf("peer results %+v", tr.PeerResults)
	}
	if !slices.Equal(tr.MCPServers, []string{"xmustard:connected"}) {
		t.Fatalf("servers %v", tr.MCPServers)
	}

	// without modelUsage the result.usage block is used
	noModel := `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.5,"usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":40}}`
	tr = claudeDriver{}.Parse(strings.NewReader(noModel), "xmustard")
	if tr.Usage != (Usage{Input: 10, CacheWrite: 20, CacheRead: 30, Output: 40, Total: 100}) || tr.UsageSource != "result.usage" {
		t.Fatalf("fallback usage %+v", tr.Usage)
	}
	// an error result is final but flagged
	errRes := `{"type":"result","subtype":"error_max_budget_usd","is_error":true,"num_turns":9,"total_cost_usd":5}`
	tr = claudeDriver{}.Parse(strings.NewReader(errRes), "xmustard")
	if !tr.FinalEvent || !tr.IsError || tr.ErrorText != "error_max_budget_usd" || tr.UsageReported {
		t.Fatalf("error result without usage %+v", tr)
	}
	// no result event (killed at the timeout): not final, no cost, usage not reported
	tr = claudeDriver{}.Parse(strings.NewReader(`{"type":"system","subtype":"init"}`), "xmustard")
	if tr.FinalEvent || tr.CostUSD != nil || tr.CostSource != CostUnpriced || tr.UsageReported {
		t.Fatalf("missing final %+v", tr)
	}
}

const codexStream = `{"type":"thread.started","thread_id":"abc"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"i1","type":"mcp_tool_call","server":"xmustard","tool":"recall","arguments":{"workspace_id":"w"},"result":{"content":[{"type":"text","text":"memories"}]},"status":"completed"}}
{"type":"item.completed","item":{"id":"i2","type":"mcp_tool_call","server":"other","tool":"x","arguments":{},"result":null,"error":{"message":"boom"},"status":"failed"}}
{"type":"item.completed","item":{"id":"i3","type":"command_execution","command":"go test","aggregated_output":"ok","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"i4","type":"file_change","changes":[],"status":"completed"}}
{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":300,"reasoning_output_tokens":120}}
{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":50,"reasoning_output_tokens":0}}
`

func TestParseCodexFinalEvent(t *testing.T) {
	tr := codexDriver{}.Parse(strings.NewReader(codexStream), "xmustard")
	want := Usage{Input: 800, CacheRead: 700, Output: 350, Reasoning: 120, Total: 1850}
	if !tr.FinalEvent || tr.FinalKind != "turn.completed" || tr.Usage != want || tr.NumTurns != 2 || !tr.UsageReported {
		t.Fatalf("usage %+v final %v turns %d reported %v, want %+v", tr.Usage, tr.FinalEvent, tr.NumTurns, tr.UsageReported, want)
	}
	if tr.CostUSD != nil || tr.CostSource != CostUnpriced {
		t.Fatal("codex reports no cost; it must stay unpriced until a price table is applied")
	}
	if tr.ToolCalls["mcp__xmustard__recall"] != 1 || tr.ToolCalls["shell"] != 1 || tr.ToolCalls["apply_patch"] != 1 {
		t.Fatalf("tool calls %v", tr.ToolCalls)
	}
	if len(tr.XmResults) != 1 || tr.XmResults[0].Text != "memories" || len(tr.PeerResults) != 1 || tr.PeerResults[0].Text != "boom" {
		t.Fatalf("results %+v %+v", tr.XmResults, tr.PeerResults)
	}
	priceUsage(&tr, "gpt-x", map[string]Price{"gpt-x": {InputPerMTok: 1, CachedInputPerMTok: 0.1, OutputPerMTok: 10}})
	// (800*1 + 700*0.1 + 350*10) / 1e6
	if tr.CostUSD == nil || *tr.CostUSD != 0.00437 || tr.CostSource != CostPriceTable {
		t.Fatalf("priced cost %v %s", tr.CostUSD, tr.CostSource)
	}
	tr2 := codexDriver{}.Parse(strings.NewReader(codexStream), "xmustard")
	priceUsage(&tr2, "unknown-model", map[string]Price{"gpt-x": {}})
	if tr2.CostUSD != nil || tr2.CostSource != CostUnpriced {
		t.Fatal("an unpriced model must not get a cost")
	}
	failed := `{"type":"turn.failed","error":{"message":"rate limited"}}`
	tr = codexDriver{}.Parse(strings.NewReader(failed), "xmustard")
	if !tr.FinalEvent || !tr.IsError || tr.ErrorText != "rate limited" || tr.UsageReported {
		t.Fatalf("turn.failed %+v", tr)
	}
	// a failed turn carries no usage: the session total is unknown, and it is not
	// priced from the partial sum of the earlier turns
	tr = codexDriver{}.Parse(strings.NewReader(codexStream+failed+"\n"), "xmustard")
	priceUsage(&tr, "gpt-x", map[string]Price{"gpt-x": {InputPerMTok: 1}})
	if tr.UsageReported || tr.CostUSD != nil || tr.CostSource != CostUnpriced {
		t.Fatalf("completed then failed: reported %v cost %v %s", tr.UsageReported, tr.CostUSD, tr.CostSource)
	}
}

const piStream = `{"id":"xm-eval-prompt","type":"response","command":"prompt","success":true}
{"type":"agent_start"}
{"type":"tool_execution_end","toolCallId":"c1","toolName":"recall","isError":false,"result":{"content":[{"type":"text","text":"mem"}]}}
{"type":"tool_execution_end","toolCallId":"c2","toolName":"bash","isError":false,"result":{"content":[{"type":"text","text":"ok"}]}}
{"type":"message_end","message":{"role":"assistant","model":"gpt-5","stopReason":"stop","usage":{"input":10,"output":20,"cacheRead":30,"cacheWrite":40,"reasoning":5,"totalTokens":100,"cost":{"total":0.25}}}}
{"type":"turn_end"}
{"type":"message_end","message":{"role":"assistant","model":"gpt-5","stopReason":"stop","usage":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"reasoning":1,"totalTokens":10,"cost":{"total":0.05}}}}
{"type":"turn_end"}
{"type":"agent_settled"}
`

func TestParsePiFinalEvent(t *testing.T) {
	stats := `{"id":"xm-eval-stats","type":"response","command":"get_session_stats","success":true,"data":{"tokens":{"input":11,"output":22,"cacheRead":33,"cacheWrite":44,"total":110},"cost":0.3}}` + "\n"
	tr := piDriver{}.Parse(strings.NewReader(piStream+stats), "")
	want := Usage{Input: 11, Output: 22, CacheRead: 33, CacheWrite: 44, Reasoning: 6, Total: 110}
	if !tr.FinalEvent || tr.FinalKind != "get_session_stats" || tr.Usage != want || !tr.UsageReported {
		t.Fatalf("usage %+v (reported %v), want %+v", tr.Usage, tr.UsageReported, want)
	}
	if tr.CostUSD == nil || *tr.CostUSD != 0.3 || tr.CostSource != CostClientFinal || tr.NumTurns != 2 || tr.Model != "gpt-5" {
		t.Fatalf("cost %v %s turns %d", tr.CostUSD, tr.CostSource, tr.NumTurns)
	}
	if len(tr.XmResults) != 1 || tr.XmResults[0].Tool != "recall" || tr.ToolCalls["bash"] != 1 {
		t.Fatalf("tools %v %+v", tr.ToolCalls, tr.XmResults)
	}
	// no stats response: per-message sums, labelled as such, and not final
	tr = piDriver{}.Parse(strings.NewReader(piStream), "")
	if tr.FinalEvent || tr.Usage != (Usage{Input: 11, Output: 22, CacheRead: 33, CacheWrite: 44, Reasoning: 6, Total: 110}) || tr.UsageSource != "message_end" || tr.UsageReported {
		t.Fatalf("fallback %+v", tr)
	}
	if tr.CostUSD == nil || *tr.CostUSD != 0.3 || tr.CostSource != CostClientMessage {
		t.Fatalf("fallback cost %v %s", tr.CostUSD, tr.CostSource)
	}
	refused := `{"id":"xm-eval-prompt","type":"response","command":"prompt","success":false,"error":"no model"}`
	tr = piDriver{}.Parse(strings.NewReader(refused), "")
	if !tr.IsError || tr.ErrorText != "no model" {
		t.Fatalf("refused prompt %+v", tr)
	}
}

func TestDriverInvocationsPerArm(t *testing.T) {
	arm := func(n string) Arm { a, _ := parseArm(n); return a }
	xm := MCPServer{Name: "xmustard", Command: "/bin/xmustard-mcp", Env: map[string]string{"XMUSTARD_API_BASE": "http://127.0.0.1:1", "XMUSTARD_API_TOKEN": `t"k`}}

	// claude: baseline keeps the operator's MCP config; isolated arms are strict
	cfgDir := t.TempDir()
	readCfg := func(inv Invocation) string {
		b, _ := os.ReadFile(flagValue(inv.Args, "--mcp-config"))
		return string(b)
	}
	inv, _ := claudeDriver{}.Build(DriverRequest{Arm: arm(ArmBaseline), Model: "m", ConfigDir: cfgDir}, "p")
	if slices.Contains(inv.Args, "--strict-mcp-config") || inv.Stdin != "p" || !slices.Contains(inv.Args, "stream-json") {
		t.Fatalf("claude baseline %v", inv.Args)
	}
	inv, _ = claudeDriver{}.Build(DriverRequest{Arm: arm(ArmBaselineNoMCP), Model: "m", ConfigDir: cfgDir}, "p")
	cfg := readCfg(inv)
	if !slices.Contains(inv.Args, "--strict-mcp-config") || cfg != `{"mcpServers":{}}` {
		t.Fatalf("claude nomcp %v", inv.Args)
	}
	inv, _ = claudeDriver{}.Build(DriverRequest{Arm: arm(ArmXmustardMCPHooks), Model: "m", ConfigDir: cfgDir, Servers: []MCPServer{xm}, HookArgs: []string{"--settings", "/h.json"}}, "p")
	if strings.Contains(strings.Join(inv.Args, " "), `t"k`) {
		t.Fatal("a server credential reached the command line")
	}
	if fi, err := os.Stat(flagValue(inv.Args, "--mcp-config")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mcp config file must be private: %v %v", fi, err)
	}
	servers, err := parseClaudeMCP(readCfg(inv))
	if err != nil || len(servers) != 1 || servers[0].Env["XMUSTARD_API_TOKEN"] != `t"k` || flagValue(inv.Args, "--settings") != "/h.json" {
		t.Fatalf("claude xmustard %v %v", servers, err)
	}

	// codex: each operator server is disabled by name; ours is added by -c keys. Under
	// sandbox-exec containment codex must not apply its own (nested) seatbelt.
	ext, _ := codexDriver{}.Build(DriverRequest{Arm: arm(ArmBaselineNoMCP), WorkDir: "/wt", ExternalSandbox: true}, "p")
	if flagValue(ext.Args, "--sandbox") != "danger-full-access" {
		t.Fatalf("codex under external containment: %v", ext.Args)
	}
	inv, err = codexDriver{}.Build(DriverRequest{Arm: arm(ArmXmustardMCP), Model: "m", WorkDir: "/wt", Servers: []MCPServer{xm}, UserServers: []string{"chrome-devtools", "node_repl"}}, "p")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(inv.Args, " ")
	for _, want := range []string{"mcp_servers.chrome-devtools.enabled=false", "mcp_servers.node_repl.enabled=false", `mcp_servers.xmustard.command="/bin/xmustard-mcp"`,
		`mcp_servers.xmustard.env_vars=["XMUSTARD_API_BASE","XMUSTARD_API_TOKEN"]`, "-C /wt", "--sandbox workspace-write"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("codex args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, `t"k`) || !slices.Contains(inv.Env, `XMUSTARD_API_TOKEN=t"k`) {
		t.Fatalf("codex credentials must travel in the environment: %v %v", inv.Args, inv.Env)
	}
	if inv.Args[len(inv.Args)-1] != "-" {
		t.Fatal("codex prompt must come from stdin")
	}
	t.Setenv("XMUSTARD_API_TOKEN", `t"k`)
	parsed, err := parseCodexMCP(inv.Args)
	if err != nil || len(parsed) != 1 || parsed[0].Env["XMUSTARD_API_TOKEN"] != `t"k` {
		t.Fatalf("codex overrides round trip: %+v %v", parsed, err)
	}
	inv, _ = codexDriver{}.Build(DriverRequest{Arm: arm(ArmBaseline), Model: "m", WorkDir: "/wt", UserServers: []string{"chrome-devtools"}}, "p")
	if strings.Contains(strings.Join(inv.Args, " "), "enabled=false") {
		t.Fatal("codex baseline must keep operator servers")
	}
	if _, err := (codexDriver{}).Build(DriverRequest{Arm: arm(ArmBaselineNoMCP), UserServers: []string{"a.b"}}, "p"); err == nil {
		t.Fatal("a dotted server name must be refused, not silently left enabled")
	}

	// pi: isolated arms drop extensions; xMustard arms load the adapter with its env
	inv, _ = piDriver{}.Build(DriverRequest{Arm: arm(ArmXmustardMemory), Model: "m", Client: ClientConfig{Extension: "/pi/index.ts"}, PiEnv: map[string]string{"XMUSTARD_API_BASE": "b", "XMUSTARD_TOKEN": "t"}}, "p")
	if !slices.Contains(inv.Args, "--no-extensions") || flagValue(inv.Args, "-e") != "/pi/index.ts" || !slices.Contains(inv.Env, "XMUSTARD_TOKEN=t") {
		t.Fatalf("pi xmustard %v %v", inv.Args, inv.Env)
	}
	if _, err := (piDriver{}).Build(DriverRequest{Arm: arm(ArmXmustardMCP)}, "p"); err == nil {
		t.Fatal("pi xMustard arm without the adapter must fail")
	}
}

func TestTOMLStringRoundTrip(t *testing.T) {
	for _, s := range []string{"plain", `quo"te`, `back\slash`, "new\nline", "tab\t", "ctl\x01", "ünï"} {
		got, err := parseTOMLString(tomlString(s))
		if err != nil || got != s {
			t.Fatalf("%q -> %s -> %q (%v)", s, tomlString(s), got, err)
		}
	}
	arr, err := parseTOMLStringArray(tomlStringArray([]string{"a b", `c"d`, ""}))
	if err != nil || !slices.Equal(arr, []string{"a b", `c"d`, ""}) {
		t.Fatalf("array %v %v", arr, err)
	}
}

func TestContainmentWrappers(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/secret.txt"
	mustWrite(t, file, "x")
	own := dir + "/runs/r0/stack"
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	inv := Invocation{Bin: "claude", Args: []string{"-p"}}
	ro := t.TempDir()
	missing := ro + "/not-yet/.gitconfig"
	sb, err := contain(&sandbox{mode: ContainSandbox, hidden: []string{dir, file, dir + "/missing"}, readOnly: []string{ro, missing}}, inv)
	if err != nil || sb.Bin != "sandbox-exec" || sb.Args[0] != "-p" || sb.Args[2] != "claude" || sb.Args[3] != "-p" {
		t.Fatalf("sandbox wrapper %v %v", sb, err)
	}
	if !strings.Contains(sb.Args[1], "(deny file-read* file-write* (subpath ") || !strings.Contains(sb.Args[1], "(literal ") || strings.Contains(sb.Args[1], "missing") ||
		!strings.Contains(sb.Args[1], "(deny file-write* (subpath ") {
		t.Fatalf("sandbox profile %s", sb.Args[1])
	}
	// the process's own directory inside a hidden path is re-allowed, after the denies
	_, withOwn, _ := (&sandbox{mode: ContainSandbox, hidden: []string{dir}}).with(own).wrap("xmustard-api", nil)
	realOwn, _ := filepathEval(own)
	realDir, _ := filepathEval(dir)
	prof := withOwn[1]
	if i, j := strings.Index(prof, "(deny file-read* file-write* (subpath "+sbplString(realDir)), strings.Index(prof, "(allow file-read* file-write* (subpath "+sbplString(realOwn)); i < 0 || j < i ||
		!strings.Contains(prof, "(allow file-read-metadata (literal "+sbplString(realDir)+"))") {
		t.Fatalf("allow rule missing or before the deny: %s", prof)
	}
	bw, _ := contain(&sandbox{mode: ContainBwrap, hidden: []string{dir, file}, readOnly: []string{ro}, allow: []string{own}}, inv)
	j := strings.Join(bw.Args, " ")
	if bw.Bin != "bwrap" || !strings.Contains(j, "--tmpfs ") || !strings.Contains(j, "--ro-bind /dev/null ") || !strings.HasSuffix(j, "-- claude -p") ||
		!strings.Contains(j, "--unshare-pid") || !strings.Contains(j, "--bind "+realOwn+" "+realOwn) {
		t.Fatalf("bwrap wrapper %s", j)
	}
	if realRO, _ := filepathEval(ro); !strings.Contains(j, "--ro-bind "+realRO+" "+realRO) {
		t.Fatalf("bwrap read-only bind missing: %s", j)
	}
	none, _ := contain(&sandbox{mode: ContainNone, hidden: []string{dir}}, inv)
	if none.Bin != "claude" {
		t.Fatal("none must not wrap")
	}
	if nilSB, _ := contain(nil, inv); nilSB.Bin != "claude" {
		t.Fatal("a nil sandbox must not wrap")
	}
	if _, err := resolveContainment("jail"); err == nil {
		t.Fatal("unknown containment accepted")
	}
}

func TestClaudeMCPConfigIsValidJSON(t *testing.T) {
	inv, _ := claudeDriver{}.Build(DriverRequest{Arm: Arm{Name: ArmXmustardMCP, IsolateMCP: true, UsesStack: true}, ConfigDir: t.TempDir(),
		Servers: []MCPServer{{Name: "xmustard", Command: "/x"}}}, "p")
	var v map[string]map[string]map[string]any
	raw, _ := os.ReadFile(flagValue(inv.Args, "--mcp-config"))
	if err := json.Unmarshal(raw, &v); err != nil || v["mcpServers"]["xmustard"]["type"] != "stdio" {
		t.Fatalf("mcp config %v %v", v, err)
	}
}

func filepathEval(p string) (string, error) { return filepath.EvalSymlinks(p) }

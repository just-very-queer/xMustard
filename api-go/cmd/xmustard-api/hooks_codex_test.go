package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/hooks"
)

// Codex hook service (WS-40a). integrations/codex/testdata/*.in.json are hook bodies
// derived from codex-rs hooks/src/schema.rs at 44fe510 (the *CommandInput structs, as
// Codex serializes them), not recordings; 06's shape matches the PostToolUse bodies
// recorded from codex-cli 0.156.1 under internal/evidence/testdata/codex. golden/ holds
// the daemon's answers in one scripted session (XMUSTARD_UPDATE_GOLDEN=1 rewrites
// them), and every answer must pass Codex's output contract.

func codexTestdata() string {
	return filepath.Join("..", "..", "..", "integrations", "codex", "testdata")
}

func TestCodexHookGoldenSession(t *testing.T) {
	f := newHookFixture(t)
	dir := codexTestdata()
	inputs, _ := filepath.Glob(filepath.Join(dir, "*.in.json"))
	sort.Strings(inputs)
	covered := map[string]bool{}
	answers := map[string][]byte{}
	spawns := budget.Counters().SpawnsTotal
	update := os.Getenv("XMUSTARD_UPDATE_GOLDEN") == "1"
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".in.json")
		event := strings.SplitN(name, "-", 3)[1]
		covered[event] = true
		code, body := f.postTo(t, f.agent, "codex", event, f.fixtureBody(t, in))
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		if err := hooks.ValidateCodexAnswer(event, body); err != nil {
			t.Errorf("%s: Codex would refuse the answer: %v\n%s", name, err, body)
		}
		answers[name] = body
		got := f.normalize(body)
		golden := filepath.Join(dir, "golden", name+".out.json")
		if update {
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%s: %v (XMUSTARD_UPDATE_GOLDEN=1 writes it)", name, err)
		}
		if got != string(want) {
			t.Errorf("%s: answer differs from %s:\n%s", name, golden, firstDiff(got, string(want)))
		}
	}
	for _, e := range hooks.CodexEvents {
		if !covered[e.Name] {
			t.Errorf("no fixture for %s", e.Name)
		}
	}
	if n := budget.Counters().SpawnsTotal - spawns; n != 0 {
		t.Fatalf("the hook session started %d processes", n)
	}
	// the large Bash and MCP outputs are blocked with a reason Codex passes unspilled,
	// the recovery line last; xMustard's own tool is left alone
	for _, name := range []string{"06-PostToolUse-Bash", "08-PostToolUse-MCP"} {
		var out struct{ Decision, Reason string }
		_ = json.Unmarshal(answers[name], &out)
		lines := strings.Split(out.Reason, "\n")
		if out.Decision != "block" || len(out.Reason) > hooks.CodexHookTextBytes || !strings.HasPrefix(lines[len(lines)-1], "[xmustard evidence] ") ||
			!strings.Contains(out.Reason, "--- FAIL: TestParse") {
			t.Errorf("%s: decision %q, %d-byte reason ending %q", name, out.Decision, len(out.Reason), lines[len(lines)-1])
		}
	}
	if !strings.HasPrefix(string(answers["06-PostToolUse-Bash"]), `{"decision":"block","reason":"[xmustard] exit status unknown`) {
		t.Errorf("the Bash reason does not state the exit status first: %.160s", answers["06-PostToolUse-Bash"])
	}
	if len(answers["09-PostToolUse-xmustard"]) != 0 {
		t.Errorf("xMustard's own tool result was replaced: %.200s", answers["09-PostToolUse-xmustard"])
	}
	obs := capturedObservations(t, f.dir, f.ws)
	if len(obs) < 2 {
		t.Fatalf("%d observations, want the Bash and the MCP captures", len(obs))
	}
	for _, o := range obs {
		if o.Actor != "hook-agent" || o.SessionID != "01a0e76d-8f1c-7612-a5ba-bf9093182dc5" {
			t.Errorf("attribution %+v", o)
		}
	}
}

// The source-derived fixtures carry exactly the fields codex-cli 0.156.1 sends for the
// events a no-model turn reaches (testdata/recorded/0.156.1, from the Codex recorder:
// all but SubagentStart and SubagentStop), and those recorded bodies are answered
// within Codex's output contract.
func TestCodexFixturesMatchTheRecordedPayloads(t *testing.T) {
	f := newHookFixture(t)
	recorded, _ := filepath.Glob(filepath.Join(codexTestdata(), "recorded", "0.156.1", "*.json"))
	derived, _ := filepath.Glob(filepath.Join(codexTestdata(), "*.in.json"))
	sort.Strings(derived)
	keys := func(raw []byte) []string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return slices.Sorted(maps.Keys(m))
	}
	seen := map[string]bool{}
	for _, rec := range recorded {
		raw, _ := os.ReadFile(rec)
		event := strings.SplitN(strings.TrimSuffix(filepath.Base(rec), ".json"), "-", 2)[0] // SessionStart-compact
		seen[event] = true
		for _, d := range derived {
			body := []byte(f.fixtureBody(t, d))
			if strings.SplitN(filepath.Base(d), "-", 3)[1] != event || bytes.Contains(body, []byte(`"agent_id"`)) {
				continue
			}
			if got, want := keys(body), keys(raw); !slices.Equal(got, want) {
				t.Errorf("%s has fields %v; codex-cli 0.156.1 sends %v", filepath.Base(d), got, want)
			}
			break
		}
		if _, routed := hooks.Codex.Event(event); !routed {
			continue
		}
		body := strings.ReplaceAll(string(raw), "/Users/dev/src/app", f.root)
		code, answer := f.postTo(t, f.agent, "codex", event, body)
		if err := hooks.ValidateCodexAnswer(event, answer); code != http.StatusOK || err != nil {
			t.Errorf("recorded %s: %d %v %s", event, code, err, answer)
		}
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostCompact", "SessionEnd"} {
		if !seen[event] {
			t.Errorf("no recorded %s payload", event)
		}
	}
}

// Every Codex event has exactly one way to be answered and a gate row, the context cap
// is the reason cap (both are Codex's spill limit), and the shipped hooks.json is what
// `xmustard-ops codex-hooks config` writes by default.
func TestCodexHookTablesAndConfigAgree(t *testing.T) {
	if hooks.CodexHookTextBytes != evidence.PolicyFor("codex").MaxChars {
		t.Fatalf("the hook context cap (%d) and the codex reason cap (%d) are both Codex's spill limit", hooks.CodexHookTextBytes,
			evidence.PolicyFor("codex").MaxChars)
	}
	for _, e := range hooks.CodexEvents {
		_, answers := hookHandlers[e.Name]
		_, queued := hookQueueEffects[e.Name]
		if answers == queued || queued != (e.Mode == hooks.Enqueue) {
			t.Errorf("%s: handler %v, queue effect %v, mode %v", e.Name, answers, queued, e.Mode)
		}
		if _, ok := routeGateTable["POST /api/hooks/codex/"+e.Name]; !ok {
			t.Errorf("%s has no gate row", e.Name)
		}
	}
	path := filepath.Join(codexTestdata(), "..", "hooks.json")
	want := hooks.CodexHooksJSON("xmustard-hook", "")
	if os.Getenv("XMUSTARD_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	shipped, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shipped, want) {
		t.Fatalf("integrations/codex/hooks.json differs from CodexHooksJSON:\n%s", firstDiff(string(shipped), string(want)))
	}
}

// A Codex body of another event, or one Codex never sends for this route, fails open;
// the scope and role checks are the Claude routes'.
func TestCodexHookScopeAndRoles(t *testing.T) {
	f := newHookFixture(t)
	body := f.fixtureBody(t, filepath.Join(codexTestdata(), "06-PostToolUse-Bash.in.json"))
	if code, _ := f.postTo(t, f.reader, "codex", "PostToolUse", body); code != http.StatusForbidden {
		t.Fatalf("a reader-only token captured: %d", code)
	}
	if code, out := f.postTo(t, f.agent, "codex", "SessionStart", body); code != 200 || len(out) != 0 {
		t.Fatalf("a PostToolUse body at SessionStart got %d %s", code, out)
	}
	for _, event := range []string{"Stop", "PreCompact", "PostToolUseFailure", "CwdChanged"} {
		if code, _ := f.postTo(t, f.agent, "codex", event, `{}`); code == http.StatusOK {
			t.Errorf("unrouted Codex event %s was answered", event)
		}
	}
	// the workspace header pins the event whatever its cwd (Codex sends no roots)
	pinned := strings.ReplaceAll(body, f.root, "/elsewhere")
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/hooks/codex/PostToolUse", strings.NewReader(pinned))
	req.Header.Set("Authorization", "Bearer "+f.agent)
	req.Header.Set("X-Xmustard-Workspace", f.ws)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Decision string }
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Decision != "block" {
		t.Fatalf("a pinned event outside the root was not answered: %+v", out)
	}
}

// TestCodexHooksEndToEnd runs the installed codex-cli with hooks.json from
// CodexHooksJSON, a freshly built xmustard-hook and this API, against the mock
// Responses API of the Codex recorder (no model call). It checks what the model is sent:
// the SessionStart and UserPromptSubmit context, and the Bash output replaced by the
// block reason whole (not spilled), handle last. Set XMUSTARD_CODEX_BIN to a codex
// binary and have node on PATH to run it.
func TestCodexHooksEndToEnd(t *testing.T) {
	codex := os.Getenv("XMUSTARD_CODEX_BIN")
	if codex == "" {
		t.Skip("XMUSTARD_CODEX_BIN is not set")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	f := newHookFixture(t)
	t.Setenv("XMUSTARD_HOOK_BUDGET_MS", "3000")
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "xmustard-hook")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join("..", "xmustard-hook")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build xmustard-hook: %v\n%s", err, out)
	}
	port := freePort(t)
	home, log := filepath.Join(tmp, "home"), filepath.Join(tmp, "model-input.jsonl")
	_ = os.MkdirAll(home, 0o700)
	config := fmt.Sprintf("model = \"gpt-5.5\"\nmodel_provider = \"mock\"\napproval_policy = \"never\"\n\n[model_providers.mock]\nname = \"mock\"\n"+
		"base_url = \"http://127.0.0.1:%d/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = false\nsupports_websockets = false\n", port)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), hooks.CodexHooksJSON(bin, f.ws), 0o600); err != nil {
		t.Fatal(err)
	}
	// Codex hands a hook its own preview of a long output, the middle cut out, so the
	// failure sits near the end, where the preview keeps it
	shell := `for i in $(seq 1 1500); do echo "=== RUN   TestCase$i"; echo "--- PASS: TestCase$i (0.00s)"; ` +
		`if [ $i = 1490 ]; then echo "=== RUN   TestParseValue"; echo "    parse_test.go:17: got 3, want 4"; echo "--- FAIL: TestParseValue (0.00s)"; fi; done; ` +
		`echo FAIL; printf "FAIL\texample.com/pkg\t0.412s\n"; exit 1`
	mock := exec.Command(node, filepath.Join("..", "..", "internal", "evidence", "testdata", "codex", "record", "mock-responses.mjs"), fmt.Sprint(port))
	mock.Env = append(os.Environ(), "XM_SHELL="+shell, "XM_LOG="+log)
	if err := mock.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mock.Process.Kill(); _ = mock.Wait() })
	waitListening(t, port)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, codex, "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-hook-trust", "-s", "workspace-write",
		"deploy check: run the SHELL step")
	run.Dir = f.root
	run.Env = append(os.Environ(), "CODEX_HOME="+home, "CODEX_SQLITE_HOME="+home, "XMUSTARD_API_BASE="+f.srv.URL,
		"XMUSTARD_API_TOKEN="+f.agent, "XMUSTARD_HOOK_SOCKET=off", "XMUSTARD_HOOK_TIMEOUT_MS=4000")
	events, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("codex exec: %v\n%s", err, events)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the mock saw no request: %v\n%s", err, events)
	}
	var sent [][]map[string]any
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var input []map[string]any
		if json.Unmarshal(sc.Bytes(), &input) == nil {
			sent = append(sent, input)
		}
	}
	if len(sent) < 2 {
		t.Fatalf("%d model requests:\n%s", len(sent), events)
	}
	first, _ := json.Marshal(sent[0])
	if !bytes.Contains(first, []byte("[xmustard ground] workspace "+f.ws)) || !bytes.Contains(first, []byte("Deploys go through the canary stage")) {
		t.Errorf("the first request lacks the SessionStart or UserPromptSubmit context:\n%.2000s", first)
	}
	var reason string
	for _, item := range sent[len(sent)-1] {
		if item["type"] == "function_call_output" {
			reason, _ = item["output"].(string)
		}
	}
	lines := strings.Split(reason, "\n")
	t.Logf("the model got a %d-byte reason in place of the Bash output", len(reason))
	if !strings.HasPrefix(reason, "[xmustard] exit status unknown") || len(reason) > hooks.CodexHookTextBytes ||
		strings.Contains(reason, "Full hook output saved to") || !strings.HasPrefix(lines[len(lines)-1], "[xmustard evidence] ") ||
		!strings.Contains(reason, "--- FAIL: TestParseValue") {
		t.Fatalf("the model did not get the whole block reason (%d bytes):\n%s\ncodex events:\n%s", len(reason), reason, events)
	}
	if len(capturedObservations(t, f.dir, f.ws)) != 1 {
		t.Fatal("the Bash output was not captured once")
	}
	// what each hook costs Codex beyond its login shell: the client's start, one post
	// and the daemon's answer (a PreToolUse search with memory and index hits)
	pre := fmt.Sprintf(`{"session_id":"t","turn_id":"u","transcript_path":null,"cwd":%q,"hook_event_name":"PreToolUse","model":"m",`+
		`"permission_mode":"default","tool_name":"Bash","tool_input":{"command":"rg -n roundHalfEven src"},"tool_use_id":"c"}`, f.root)
	var took []time.Duration
	for range 20 {
		hook := exec.Command(bin, "codex", "PreToolUse", "--workspace", f.ws)
		hook.Stdin = strings.NewReader(pre)
		hook.Env = []string{"XMUSTARD_API_BASE=" + f.srv.URL, "XMUSTARD_API_TOKEN=" + f.agent, "XMUSTARD_HOOK_SOCKET=off"}
		start := time.Now()
		out, err := hook.Output()
		took = append(took, time.Since(start))
		if err != nil || !bytes.Contains(out, []byte("roundHalfEven")) {
			t.Fatalf("client run: %v %s", err, out)
		}
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	t.Logf("xmustard-hook codex PreToolUse over 20 runs: p50 %v, max %v", took[len(took)/2], took[len(took)-1])
}

func waitListening(t *testing.T, port int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			return
		}
	}
	t.Fatalf("nothing listens on %d", port)
}

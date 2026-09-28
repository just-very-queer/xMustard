package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/hooks"
	"xmustard/api-go/internal/workspaceops"
)

// hookFixture is a registered workspace with human-approved and peer-verified memory,
// an API with auth enforced, and fake resident-worker reads.
type hookFixture struct {
	dir, ws, root string
	agent, reader string // bearer tokens
	srv           *httptest.Server
	coreMarker    string // created if anything runs the core binary
	searches      atomic.Int64
	memories      map[string]string // name -> memory id
}

const hookRepoWS = "wsHooks"

func newHookFixture(t testing.TB) *hookFixture {
	t.Helper()
	f := &hookFixture{ws: hookRepoWS, memories: map[string]string{}}
	f.dir = t.TempDir()
	// A fixed-width root: its length reaches the goldens through captured sizes and
	// reduction cuts, and t.TempDir's names vary in length.
	root := fmt.Sprintf("/tmp/xmhook-%08x", rand.Uint32())
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	f.root = root
	t.Setenv("XMUSTARD_DATA_DIR", f.dir)
	t.Setenv("XMUSTARD_HOOK_BUDGET_MS", "3000")
	for rel, text := range map[string]string{
		"src/billing.go": "package billing\n\nfunc total(x int) int {\n\treturn x\n}\n",
		"src/ledger.go":  "package billing\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": f.ws, "root_path": root}})
	_ = os.MkdirAll(filepath.Join(f.dir, "workspaces", f.ws), 0o755)
	_ = os.WriteFile(filepath.Join(f.dir, "workspaces", f.ws, "snapshot.json"), snap, 0o644)
	recs, _ := json.Marshal([]map[string]any{{"workspace_id": f.ws, "name": "hooks", "root_path": root}})
	_ = os.WriteFile(filepath.Join(f.dir, "workspaces.json"), recs, 0o644)
	// any process start is visible: the core binary is a script that leaves a marker
	f.coreMarker = filepath.Join(t.TempDir(), "core-ran")
	core := filepath.Join(t.TempDir(), "xmustard-core")
	if err := os.WriteFile(core, []byte("#!/bin/sh\ntouch "+f.coreMarker+"\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	var err2 error
	if f.agent, err2 = workspaceops.MintToken(f.dir, "hook-agent", "proposer"); err2 != nil {
		t.Fatal(err2)
	}
	f.reader, _ = workspaceops.MintToken(f.dir, "hook-reader", "reader")
	if _, err := workspaceops.MintIdentityToken(f.dir, "human-1", workspaceops.RoleHumanApprover, 0, []string{f.ws},
		workspaceops.TokenIdentity{Kind: workspaceops.PrincipalHuman}); err != nil {
		t.Fatal(err)
	}
	f.memory(t, "billing", "billing rounding", "Invoices round half-even (banker's rounding); totals are kept in cents.",
		[]string{"src/billing.go"}, nil, true)
	f.memory(t, "billing-peer", "billing currency", "Currency conversion happens before rounding.", []string{"src/billing.go"}, nil, false)
	f.memory(t, "deploy", "deploy canary", "Deploys go through the canary stage for 30 minutes before full rollout.", nil, []string{"trigger-deploy"}, true)
	f.srv = httptest.NewServer(bodyLimitMiddleware(authMiddleware(f.dir, "auto", newAPIHandler())))
	t.Cleanup(f.srv.Close)
	f.fakeWorker(t)
	return f
}

// memory writes a promoted memory: two peer approvals, the second a human approver's
// when human is set.
func (f *hookFixture) memory(t testing.TB, name, title, content string, paths, tags []string, human bool) {
	t.Helper()
	e, err := workspaceops.Remember(f.dir, f.ws, workspaceops.RememberRequest{ProposeContextRequest: workspaceops.ProposeContextRequest{
		Title: title, Content: content, Permission: "readwrite", Paths: paths, Tags: tags}}, workspaceops.ContextActor{ID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceops.VerifyContext(f.dir, f.ws, e.ID, "peer-1", true, ""); err != nil {
		t.Fatal(err)
	}
	second := workspaceops.ContextActor{ID: "peer-2", Verifier: true}
	if human {
		second = workspaceops.ContextActor{ID: "human-1", Kind: workspaceops.PrincipalHuman, Verifier: true}
	}
	e, err = workspaceops.VerifyContextOutcome(f.dir, f.ws, e.ID, second, workspaceops.VerifyRequest{})
	if err != nil || !e.Promoted {
		t.Fatalf("memory %s not promoted: %v %+v", name, err, e)
	}
	f.memories[name] = e.ID
}

// fakeWorker stands in for the resident worker's search and syntax check.
func (f *hookFixture) fakeWorker(t testing.TB) {
	prevSearch, prevSyntax := hookSearch, hookSyntax
	t.Cleanup(func() { hookSearch, hookSyntax = prevSearch, prevSyntax })
	hookSearch = func(_ context.Context, ws workspaceops.ResolvedWorkspace, query string, limit int) (json.RawMessage, error) {
		f.searches.Add(1)
		first := strings.Fields(query)[0]
		return json.RawMessage(fmt.Sprintf(`{"workspace_id":%q,"query":%q,"total":2,"hits":[`+
			`{"kind":"symbol","name":%q,"path":"src/billing.go","line":3,"lines":[3,5],"lanes_matched":["bm25","name"],"score":1.2},`+
			`{"kind":"chunk","name":"total","path":"src/ledger.go","line":1,"lanes_matched":["bm25"],"score":0.4}],`+
			`"freshness":{"source":"resident_index","indexed_commit":"4b1f0c2d9e8a7f6b5a4c","head":"4b1f0c2d9e8a7f6b5a4c","status":"current","dirty_paths_touching_result":[]}}`,
			ws.WorkspaceID, query, first)), nil
	}
	var checks sync.Map // path -> calls
	hookSyntax = func(_ context.Context, ws workspaceops.ResolvedWorkspace, rels []string) (json.RawMessage, error) {
		n, _ := checks.LoadOrStore(rels[0], new(atomic.Int64))
		errs := `[]`
		if n.(*atomic.Int64).Add(1) > 1 { // after the edit: one missing brace
			errs = `[{"line":4,"column":24,"end_line":4,"kind":"missing","node":")","line_hash":"9f2c"}]`
		}
		return json.RawMessage(fmt.Sprintf(`{"files":[{"path":%q,"language":"go","status":"checked","errors":%s,"truncated":false}]}`, rels[0], errs)), nil
	}
}

func (f *hookFixture) post(t *testing.T, token, event, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/hooks/claude/"+event, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// fixtureBody reads an event fixture and fills its placeholders.
func (f *hookFixture) fixtureBody(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(raw), "${ROOT}", f.root)
	s = strings.ReplaceAll(s, `"${BIG_SOURCE}"`, jsonString(bigSource(1200)))
	return strings.ReplaceAll(s, `"${BIG_TEST_LOG}"`, jsonString(goTestLog(1500)))
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func bigSource(lines int) string {
	var b strings.Builder
	b.WriteString("package billing\n\n")
	for i := 0; i < lines-2; i++ {
		fmt.Fprintf(&b, "func ledgerEntry%04d(x int) int { return x + %d } // keep\n", i, i)
	}
	return b.String()
}

var (
	handlePattern  = regexp.MustCompile(`xm1\.[A-Za-z0-9_-]{43}`)
	timePattern    = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z`)
	memoryPattern  = regexp.MustCompile(`ctx_[0-9a-f]{12}`)
	outcomePattern = regexp.MustCompile(`oc_[0-9a-f]{16}`)
)

// normalize makes an answer comparable across runs: the root, handles, times and
// memory ids are replaced by stable names.
func (f *hookFixture) normalize(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") != nil {
		return "NOT JSON: " + string(body)
	}
	s := strings.ReplaceAll(pretty.String(), f.root, "${ROOT}")
	s = handlePattern.ReplaceAllString(s, "<handle>")
	s = timePattern.ReplaceAllString(s, "<time>")
	s = outcomePattern.ReplaceAllString(s, "<outcome>")
	names := map[string]string{}
	for name, id := range f.memories {
		names[id] = "<memory:" + name + ">"
	}
	s = memoryPattern.ReplaceAllStringFunc(s, func(id string) string { return names[id] })
	return s + "\n"
}

// Every recorded Claude Code event, in session order, against its golden answer; the
// whole session starts no process.
func TestHookGoldenSession(t *testing.T) {
	f := newHookFixture(t)
	dir := filepath.Join("..", "..", "..", "integrations", "claude-code", "testdata")
	inputs, _ := filepath.Glob(filepath.Join(dir, "*.in.json"))
	if len(inputs) < len(hooks.Events) {
		t.Fatalf("%d fixtures for %d events", len(inputs), len(hooks.Events))
	}
	sort.Strings(inputs)
	covered := map[string]bool{}
	spawns := budget.Counters().SpawnsTotal
	update := os.Getenv("XMUSTARD_UPDATE_GOLDEN") == "1"
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".in.json")
		event := strings.SplitN(name, "-", 3)[1]
		covered[event] = true
		code, body := f.post(t, f.agent, event, f.fixtureBody(t, in))
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, code, body)
		}
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
	for _, e := range hooks.Events {
		if !covered[e.Name] {
			t.Errorf("no fixture for %s", e.Name)
		}
	}
	if n := budget.Counters().SpawnsTotal - spawns; n != 0 {
		t.Fatalf("the hook session started %d processes", n)
	}
	if _, err := os.Stat(f.coreMarker); err == nil {
		t.Fatal("a hook ran the Rust core")
	}
	if f.searches.Load() < 2 {
		t.Fatalf("search hooks reached the resident index %d times", f.searches.Load())
	}
	admin, _ := workspaceops.MintToken(f.dir, "ops", "admin")
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/health", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var health struct {
		HookUsage map[string]int64 `json:"hook_usage"`
	}
	if json.NewDecoder(resp.Body).Decode(&health) != nil || health.HookUsage["answered"] < 10 || health.HookUsage["enqueued"] < 5 {
		t.Fatalf("health hook_usage %v", health.HookUsage)
	}
}

// firstDiff shows where two texts first differ.
func firstDiff(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	from := max(0, i-120)
	return fmt.Sprintf("at byte %d:\n got …%s\nwant …%s", i, got[from:min(len(got), i+120)], want[from:min(len(want), i+120)])
}

// capturedObservation reads the one observation record under the workspace.
func capturedObservations(t *testing.T, dataDir, ws string) []evidence.Observation {
	t.Helper()
	metas, _ := filepath.Glob(filepath.Join(dataDir, "evidence", ws, "*", "meta.json"))
	var out []evidence.Observation
	for _, m := range metas {
		var obs evidence.Observation
		raw, _ := os.ReadFile(m)
		if json.Unmarshal(raw, &obs) == nil {
			out = append(out, obs)
		}
	}
	return out
}

func postToolUseBash(root, session, agent, stdout string) string {
	b, _ := json.Marshal(map[string]any{"session_id": session, "cwd": root, "hook_event_name": "PostToolUse", "agent_id": agent, "agent_type": "Explore",
		"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}, "tool_use_id": "toolu_x",
		"tool_response": map[string]any{"stdout": stdout, "stderr": "", "interrupted": false, "isImage": false}})
	return string(b)
}

// Captured native output is redacted before it is retained or projected, and the
// record names the session and the subagent that ran the tool (attribution only).
func TestHookCaptureIsRedactedAndAttributed(t *testing.T) {
	f := newHookFixture(t)
	secret := "gh" + "p_" + strings.Repeat("Z9x", 12) // a GitHub token shape
	stdout := goTestLog(1500) + "export GITHUB_TOKEN=" + secret + "\n"
	code, body := f.post(t, f.agent, "PostToolUse", postToolUseBash(f.root, "sess-r", "agent-7", stdout))
	if code != 200 || strings.Contains(string(body), secret) {
		t.Fatalf("answer %d leaks the secret or failed: %.300s", code, body)
	}
	var out hooks.Output
	if err := json.Unmarshal(body, &out); err != nil || out.HookSpecificOutput == nil || len(out.HookSpecificOutput.UpdatedToolOutput) == 0 {
		t.Fatalf("no updatedToolOutput: %s", body)
	}
	if err := evidence.ValidateShape("claude", "Bash", out.HookSpecificOutput.UpdatedToolOutput); err != nil {
		t.Fatalf("replacement does not match the Bash shape: %v", err)
	}
	obs := capturedObservations(t, f.dir, f.ws)
	if len(obs) != 1 {
		t.Fatalf("%d observations", len(obs))
	}
	o := obs[0]
	if o.SessionID != "sess-r" || o.AgentID != "agent-7" || o.Actor != "hook-agent" || o.CapturedKey != "" || o.Tool != "Bash" {
		t.Fatalf("attribution %+v", o)
	}
	raws, _ := filepath.Glob(filepath.Join(f.dir, "evidence", f.ws, "*", "raw.bin"))
	for _, r := range raws {
		b, _ := os.ReadFile(r)
		if bytes.Contains(b, []byte(secret)) || !bytes.Contains(b, []byte("[REDACTED:")) {
			t.Fatalf("retained original is not redacted")
		}
	}
}

// A capture the redaction refuses fails open: the output of a secret path (WS-72
// denylist, evidence.ErrSecretPath) and a redactor that fails (redaction_failed) leave
// the client its native output, retain nothing, and are not counted as the daemon
// being busy.
func TestHookRefusedCaptureFailsOpenWithoutBusyNote(t *testing.T) {
	f := newHookFixture(t)
	content := strings.Repeat("API_URL=https://internal.example.test\nFEATURE=on\n", 1000)
	read := func(path string) string {
		return hookEvent(map[string]any{"session_id": "refused", "cwd": f.root, "hook_event_name": "PostToolUse", "tool_name": "Read",
			"tool_input":    map[string]any{"file_path": path},
			"tool_response": map[string]any{"type": "text", "file": map[string]any{"filePath": path, "content": content}}, "tool_use_id": "toolu_read"})
	}
	check := func(name string, code int, body []byte, retained int) {
		t.Helper()
		if code != 200 || bytes.Contains(body, []byte("daemon was busy")) {
			t.Fatalf("%s: %d %.300s", name, code, body)
		}
		if obs := capturedObservations(t, f.dir, f.ws); len(obs) != retained {
			t.Fatalf("%s: %d observations retained, want %d", name, len(obs), retained)
		}
	}
	code, body := f.post(t, f.agent, "PostToolUse", read(filepath.Join(f.root, ".env")))
	if bytes.Contains(body, []byte("updatedToolOutput")) {
		t.Fatalf("secret path: the output was replaced: %.300s", body)
	}
	check("secret path", code, body, 0)
	code, body = f.post(t, f.agent, "PostToolUse", read(filepath.Join(f.root, "config.txt"))) // the same output elsewhere is captured
	check("control", code, body, 1)
	withCaptureRedactor(t, func(w io.Writer) evidence.StreamRedactor { return panicRedactor{w} })
	code, body = f.post(t, f.agent, "PostToolUse", postToolUseBash(f.root, "refused", "", goTestLog(1500)))
	if bytes.Contains(body, []byte("updatedToolOutput")) {
		t.Fatalf("redactor failure: the output was replaced: %.300s", body)
	}
	check("redactor failure", code, body, 1)
}

// A payload the Bash schema rejects falls back to the original output (with a notice
// as context), within the budget.
func TestHookShapeMismatchFallsBackWithinBudget(t *testing.T) {
	f := newHookFixture(t)
	t.Setenv("XMUSTARD_HOOK_BUDGET_MS", "1500")
	b, _ := json.Marshal(map[string]any{"session_id": "s", "cwd": f.root, "hook_event_name": "PostToolUse", "tool_name": "Bash",
		"tool_input":    map[string]any{"command": "go test ./..."},
		"tool_response": map[string]any{"stdout": goTestLog(400), "stderr": ""}}) // no "interrupted": not the Bash output schema
	start := time.Now()
	code, body := f.post(t, f.agent, "PostToolUse", string(b))
	took := time.Since(start)
	var out hooks.Output
	_ = json.Unmarshal(body, &out)
	if code != 200 || out.HookSpecificOutput == nil || len(out.HookSpecificOutput.UpdatedToolOutput) != 0 ||
		!strings.Contains(out.HookSpecificOutput.AdditionalContext, "could not be shaped") || took > 1500*time.Millisecond {
		t.Fatalf("shape mismatch: %d after %v: %s", code, took, body)
	}
}

// Past the budget the answer is an empty 200 at once, and the session hears about
// it (throttled) on a later hook.
func TestHookTimeoutFailsOpenWithEmptyOutput(t *testing.T) {
	f := newHookFixture(t)
	t.Setenv("XMUSTARD_HOOK_BUDGET_MS", "60")
	release := make(chan struct{})
	defer close(release)
	hookSearch = func(ctx context.Context, _ workspaceops.ResolvedWorkspace, _ string, _ int) (json.RawMessage, error) {
		select { // a worker that ignores cancellation for a while
		case <-release:
		case <-time.After(400 * time.Millisecond):
		}
		return nil, ctx.Err()
	}
	grep := func(session string) string {
		b, _ := json.Marshal(map[string]any{"session_id": session, "cwd": f.root, "hook_event_name": "PreToolUse", "tool_name": "Grep",
			"tool_input": map[string]any{"pattern": "roundHalfEven"}})
		return string(b)
	}
	before := hookStats.timedOut.Load()
	start := time.Now()
	code, body := f.post(t, f.agent, "PreToolUse", grep("slow"))
	if took := time.Since(start); code != 200 || len(body) != 0 || took > 350*time.Millisecond {
		t.Fatalf("timed-out hook: %d %q after %v", code, body, took)
	}
	if hookStats.timedOut.Load() == before {
		t.Fatal("the timeout was not counted")
	}
	t.Setenv("XMUSTARD_HOOK_BUDGET_MS", "3000")
	hookSearch = func(context.Context, workspaceops.ResolvedWorkspace, string, int) (json.RawMessage, error) {
		return nil, workspaceops.ErrIndexNotResident
	}
	_, body = f.post(t, f.agent, "PreToolUse", grep("slow"))
	if !strings.Contains(string(body), "returned no context because the daemon was busy") {
		t.Fatalf("no busy note after a skipped hook: %s", body)
	}
	_, body = f.post(t, f.agent, "PreToolUse", grep("slow"))
	if strings.Contains(string(body), "daemon was busy") {
		t.Fatal("the busy note is not throttled")
	}
}

// hookEvent marshals a hook body.
func hookEvent(fields map[string]any) string {
	b, _ := json.Marshal(fields)
	return string(b)
}

// hookContext is an answer's additionalContext ("" for an empty answer).
func hookContext(t *testing.T, body []byte) string {
	t.Helper()
	if len(body) == 0 {
		return ""
	}
	var out hooks.Output
	if err := json.Unmarshal(body, &out); err != nil || out.HookSpecificOutput == nil {
		t.Fatalf("answer %s", body)
	}
	return out.HookSpecificOutput.AdditionalContext
}

// A subagent starts with a fresh context: a memory pushed into the main thread is
// pushed again into a subagent of the same session, and one pushed into a subagent
// (whose context is then discarded) still reaches the main thread.
func TestHookSubagentHasItsOwnContext(t *testing.T) {
	f := newHookFixture(t)
	read := func(session, agent string) string {
		fields := map[string]any{"session_id": session, "cwd": f.root, "hook_event_name": "PreToolUse", "tool_name": "Read",
			"tool_input": map[string]any{"file_path": filepath.Join(f.root, "src", "billing.go")}}
		if agent != "" {
			fields["agent_id"], fields["agent_type"] = agent, "Explore"
		}
		return hookEvent(fields)
	}
	for _, step := range []struct {
		session, agent string
		pushed         bool
	}{
		{"main-first", "", true}, {"main-first", "a1", true}, {"main-first", "a1", false}, {"main-first", "", false},
		{"sub-first", "a2", true}, {"sub-first", "", true}, {"sub-first", "a3", true},
	} {
		_, body := f.post(t, f.agent, "PreToolUse", read(step.session, step.agent))
		if got := strings.Contains(hookContext(t, body), "billing rounding"); got != step.pushed {
			t.Fatalf("%s/%q: pushed %v, want %v: %s", step.session, step.agent, got, step.pushed, body)
		}
	}
}

// An admitted memory that does not fit in the context left is not lost silently: it is
// counted too_large in the withheld note with the recall call, and the ones that fit
// are pushed. A memory is marked pushed only once the answer carrying it is written:
// when the budget runs out after admission, the next hook pushes it.
func TestHookPushBudgetsFramesAndMarksOnlyWhatIsDelivered(t *testing.T) {
	f := newHookFixture(t)
	big := strings.Repeat("Ledger entries post in cents, rounded half-even at close. ", 200) // ~11,600 chars
	f.memory(t, "ledger-big", "ledger posting", big, []string{"src/ledger.go"}, nil, true)
	f.memory(t, "ledger-small", "ledger close", "Ledger close runs after the billing batch.", []string{"src/ledger.go"}, nil, true)
	read := hookEvent(map[string]any{"session_id": "budget", "cwd": f.root, "hook_event_name": "PreToolUse", "tool_name": "Read",
		"tool_input": map[string]any{"file_path": filepath.Join(f.root, "src", "ledger.go")}})
	_, body := f.post(t, f.agent, "PreToolUse", read)
	ctx := hookContext(t, body)
	if !strings.Contains(ctx, "Ledger close runs after") || strings.Contains(ctx, "Ledger entries post") ||
		!strings.Contains(ctx, "too_large: 1") || !strings.Contains(ctx, `recall(paths=["src/ledger.go"])`) {
		t.Fatalf("budgeted push: %.600s", ctx)
	}
	if len(ctx) > hooks.MaxContextChars {
		t.Fatalf("context of %d characters", len(ctx))
	}
	if _, body := f.post(t, f.agent, "PreToolUse", read); len(body) != 0 {
		t.Fatalf("a delivered memory or note was given twice: %s", body)
	}

	// the budget runs out after admission (the pre-edit syntax baseline hangs)
	t.Setenv("XMUSTARD_HOOK_BUDGET_MS", "800")
	hookSyntax = func(ctx context.Context, _ workspaceops.ResolvedWorkspace, _ []string) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	edit := hookEvent(map[string]any{"session_id": "late", "cwd": f.root, "hook_event_name": "PreToolUse", "tool_name": "Edit",
		"tool_input": map[string]any{"file_path": filepath.Join(f.root, "src", "billing.go"), "old_string": "x", "new_string": "y"}})
	if _, body := f.post(t, f.agent, "PreToolUse", edit); len(body) != 0 {
		t.Fatalf("a timed-out hook answered %s", body)
	}
	readBilling := hookEvent(map[string]any{"session_id": "late", "cwd": f.root, "hook_event_name": "PreToolUse", "tool_name": "Read",
		"tool_input": map[string]any{"file_path": filepath.Join(f.root, "src", "billing.go")}})
	if _, body := f.post(t, f.agent, "PreToolUse", readBilling); !strings.Contains(hookContext(t, body), "billing rounding") {
		t.Fatalf("a memory admitted into a timed-out answer was lost: %s", body)
	}
}

// CwdChanged always sends watchPaths: into a directory of no workspace, [] clears the
// previous workspace's list.
func TestHookCwdChangedClearsTheWatchList(t *testing.T) {
	f := newHookFixture(t)
	elsewhere := t.TempDir()
	_, body := f.post(t, f.agent, "CwdChanged", hookEvent(map[string]any{"session_id": "cwd", "cwd": elsewhere,
		"hook_event_name": "CwdChanged", "old_cwd": f.root, "new_cwd": elsewhere}))
	if string(body) != `{"hookSpecificOutput":{"hookEventName":"CwdChanged","watchPaths":[]}}` {
		t.Fatalf("CwdChanged out of any workspace: %s", body)
	}
}

// WorktreeRemove forgets a worktree's cached identity only inside the workspace that
// passed the scope checks: the root or a path under it, never a relative path, a path
// that climbs out, another directory, or any path from a cwd in no workspace.
func TestHookWorktreeRemoveStaysInTheWorkspace(t *testing.T) {
	f := newHookFixture(t)
	var mu sync.Mutex
	var forgot []string
	prev := hookForgetRoot
	t.Cleanup(func() { hookForgetRoot = prev })
	hookForgetRoot = func(root string) {
		mu.Lock()
		defer mu.Unlock()
		forgot = append(forgot, root)
	}
	under := filepath.Join(f.root, ".claude", "worktrees", "feature")
	elsewhere := t.TempDir()
	for _, c := range []struct {
		cwd, path string
		forgets   bool
	}{
		{f.root, under, true},
		{f.root, f.root, true},
		{f.root, elsewhere, false},
		{f.root, f.root + "/../other", false},
		{f.root, ".claude/worktrees/feature", false},
		{elsewhere, under, false}, // unbound: the cwd is in no workspace
	} {
		mu.Lock()
		forgot = nil
		mu.Unlock()
		if code, body := f.post(t, f.agent, "WorktreeRemove", hookEvent(map[string]any{"session_id": "wt", "cwd": c.cwd,
			"hook_event_name": "WorktreeRemove", "worktree_path": c.path})); code != 200 || len(body) != 0 {
			t.Fatalf("%s: %d %s", c.path, code, body)
		}
		mu.Lock()
		got := slices.Clone(forgot)
		mu.Unlock()
		var want []string
		if c.forgets {
			want = []string{filepath.Clean(c.path)}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("cwd %s, worktree %s: forgot %v, want %v", c.cwd, c.path, got, want)
		}
	}
}

// The withheld note of a keyword push names the trigger tag recall filters on.
func TestHookTriggerNoteNamesTheTag(t *testing.T) {
	f := newHookFixture(t)
	f.memory(t, "deploy-peer", "deploy freeze", "No deploys on Fridays.", nil, []string{"trigger-deploy"}, false)
	_, body := f.post(t, f.agent, "UserPromptSubmit", hookEvent(map[string]any{"session_id": "kw", "cwd": f.root,
		"hook_event_name": "UserPromptSubmit", "prompt": "Please deploy the fix."}))
	ctx := hookContext(t, body)
	if !strings.Contains(ctx, "deploy canary") || !strings.Contains(ctx, "needs_human_approved: 1") ||
		!strings.Contains(ctx, `recall(tags=["trigger-deploy"])`) {
		t.Fatalf("trigger push: %s", ctx)
	}
}

// barrierRedactor holds every capture's first write until n captures are in flight at
// once, so a server that handled them one at a time would never release them.
type barrierRedactor struct {
	io.Writer
	once *sync.Once
	wg   *sync.WaitGroup
}

func (b barrierRedactor) Write(p []byte) (int, error) {
	b.once.Do(func() {
		b.wg.Done()
		done := make(chan struct{})
		go func() { b.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	return b.Writer.Write(p)
}

func (barrierRedactor) Flush() error { return nil }

func TestHookParallelPostToolUseCallsRunConcurrently(t *testing.T) {
	f := newHookFixture(t)
	const n = 4
	var wg sync.WaitGroup
	wg.Add(n)
	withCaptureRedactor(t, func(w io.Writer) evidence.StreamRedactor {
		return barrierRedactor{Writer: w, once: new(sync.Once), wg: &wg}
	})
	bodies := make([][]byte, n)
	var calls sync.WaitGroup
	start := time.Now()
	for i := range n {
		calls.Go(func() {
			_, bodies[i] = f.post(t, f.agent, "PostToolUse", postToolUseBash(f.root, fmt.Sprintf("par-%d", i), "", goTestLog(1200)))
		})
	}
	calls.Wait()
	for i, b := range bodies {
		if !bytes.Contains(b, []byte("updatedToolOutput")) {
			t.Fatalf("call %d was not answered with a reduction (serialized?): %.200s", i, b)
		}
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("parallel captures took %v", took)
	}
}

// The workspace a hook acts on passes the same checks as a workspace path: a token
// scoped elsewhere gets nothing (audited), a reader-only token drives no hook, an
// unregistered cwd gets nothing.
func TestHookWorkspaceScopeAndRoles(t *testing.T) {
	f := newHookFixture(t)
	read := func(cwd string) string {
		b, _ := json.Marshal(map[string]any{"session_id": "scope", "cwd": cwd, "hook_event_name": "PreToolUse", "tool_name": "Read",
			"tool_input": map[string]any{"file_path": filepath.Join(f.root, "src", "billing.go")}})
		return string(b)
	}
	if _, body := f.post(t, f.agent, "PreToolUse", read(f.root)); !strings.Contains(string(body), "billing rounding") {
		t.Fatalf("an agent in scope gets the path-bound memory: %s", body)
	}
	other, _ := workspaceops.MintScopedToken(f.dir, "elsewhere", "proposer", 0, []string{"otherWS"})
	// denied events are persisted at most once a second, process-wide: wait out any
	// other test's denial
	audited := false
	for try := 0; try < 3 && !audited; try++ {
		time.Sleep(1100 * time.Millisecond)
		if code, body := f.post(t, other, "PreToolUse", read(f.root)); code != 200 || len(body) != 0 {
			t.Fatalf("an out-of-scope token got %d %s", code, body)
		}
		audit, _ := os.ReadFile(filepath.Join(f.dir, "auth_audit.json"))
		audited = bytes.Contains(audit, []byte("not in token scope (hook PreToolUse)"))
	}
	if !audited {
		t.Fatal("the refusal was not audited")
	}
	if code, _ := f.post(t, f.reader, "PreToolUse", read(f.root)); code != http.StatusForbidden {
		t.Fatalf("a reader-only token drove a hook: %d", code)
	}
	if code, _ := f.post(t, f.reader, "PostToolUse", postToolUseBash(f.root, "s", "", goTestLog(1200))); code != http.StatusForbidden {
		t.Fatalf("a reader captured: %d", code)
	}
	if code, body := f.post(t, f.agent, "PreToolUse", read(t.TempDir())); code != 200 || len(body) != 0 {
		t.Fatalf("an unregistered cwd got %d %s", code, body)
	}
	if code, _ := f.post(t, "", "PreToolUse", read(f.root)); code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated hook got %d", code)
	}
	// a body for another event fails open
	if code, body := f.post(t, f.agent, "PostToolUse", read(f.root)); code != 200 || len(body) != 0 {
		t.Fatalf("a mismatched event got %d %s", code, body)
	}
}

// Every event has exactly one way to be answered, and the shipped hooks.json matches
// the routes and the Claude Code rules it depends on.
func TestHookTablesAndPluginConfigAgree(t *testing.T) {
	for _, e := range hooks.Events {
		_, answers := hookHandlers[e.Name]
		_, queued := hookQueueEffects[e.Name]
		if answers == queued || queued != (e.Mode == hooks.Enqueue) {
			t.Errorf("%s: handler %v, queue effect %v, mode %v", e.Name, answers, queued, e.Mode)
		}
		if _, ok := routeGateTable["POST /api/hooks/claude/"+e.Name]; !ok {
			t.Errorf("%s has no gate row", e.Name)
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "integrations", "claude-code", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type           string            `json:"type"`
				URL            string            `json:"url"`
				Command        string            `json:"command"`
				Args           []string          `json:"args"`
				Timeout        float64           `json:"timeout"`
				Headers        map[string]string `json:"headers"`
				AllowedEnvVars []string          `json:"allowedEnvVars"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Hooks["WorktreeCreate"]; ok {
		t.Fatal("WorktreeCreate would replace git worktree creation")
	}
	for event, groups := range cfg.Hooks {
		ev, ok := hooks.EventByName(event)
		if !ok {
			t.Errorf("hooks.json registers %s, which has no route", event)
			continue
		}
		for _, g := range groups {
			for _, h := range g.Hooks {
				switch {
				case h.Timeout <= 0 || h.Timeout > 5:
					t.Errorf("%s: timeout %v (explicit, a few seconds)", event, h.Timeout)
				case event == "SessionEnd" && h.Timeout > 1.5:
					t.Errorf("SessionEnd timeout %v exceeds the 1.5 s budget", h.Timeout)
				case h.Type == "http" && !strings.HasSuffix(h.URL, "/api/hooks/claude/"+ev.Name):
					t.Errorf("%s: url %s", event, h.URL)
				case h.Type == "http" && (!slices.Contains(h.AllowedEnvVars, "XMUSTARD_API_TOKEN") || h.Headers["Authorization"] != "Bearer ${XMUSTARD_API_TOKEN}"):
					t.Errorf("%s: http hook does not authenticate", event)
				case h.Type == "command" && (!strings.HasSuffix(h.Command, "/xmustard-hook") || !slices.Equal(h.Args, []string{"claude", ev.Name})):
					t.Errorf("%s: command %s %v", event, h.Command, h.Args)
				case h.Type != "http" && h.Type != "command":
					t.Errorf("%s: hook type %s", event, h.Type)
				case event == "SessionStart" && h.Type != "command":
					t.Errorf("SessionStart takes command or mcp_tool hooks only, and mcp_tool is skipped at launch")
				}
			}
		}
	}
	for _, e := range hooks.Events {
		if _, ok := cfg.Hooks[e.Name]; !ok {
			t.Errorf("hooks.json does not register %s", e.Name)
		}
	}
}

// The static client's socket serves the hook routes and nothing else, from a
// directory only its owner can enter.
func TestHookSocketServesOnlyHookRoutes(t *testing.T) {
	f := newHookFixture(t)
	dir, err := os.MkdirTemp("", "xmhs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "run", "hook.sock")
	ln, err := listenHookSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: onlyHookRoutes(bodyLimitMiddleware(authMiddleware(f.dir, "auto", newAPIHandler())))}
	go srv.Serve(ln)
	defer srv.Close()
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", fi.Mode().Perm())
	}
	if _, err := listenHookSocket(path); err == nil {
		t.Fatal("a second daemon took over a live socket")
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	get := func(method, p, body string) int {
		req, _ := http.NewRequest(method, "http://localhost"+p, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.agent)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("GET", "/api/auth/whoami", ""); code != http.StatusNotFound {
		t.Fatalf("non-hook route over the socket: %d", code)
	}
	if code := get("POST", "/api/hooks/claude/SessionEnd", `{"session_id":"s","hook_event_name":"SessionEnd"}`); code != 200 {
		t.Fatalf("hook route over the socket: %d", code)
	}
	open := filepath.Join(dir, "open")
	_ = os.Mkdir(open, 0o755)
	if _, err := listenHookSocket(filepath.Join(open, "hook.sock")); err == nil {
		t.Fatal("listened in a directory others can enter")
	}
	// a file that is not a socket is never removed to make room
	file := filepath.Join(dir, "run", "not-a-socket")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenHookSocket(file); err == nil {
		t.Fatal("listened over a regular file")
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "keep" {
		t.Fatalf("the regular file was removed or changed: %v", err)
	}
	// a stale socket of this user is replaced
	stale := filepath.Join(dir, "run", "stale.sock")
	sl, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	sl.(*net.UnixListener).SetUnlinkOnClose(false)
	sl.Close()
	ln2, err := listenHookSocket(stale)
	if err != nil {
		t.Fatalf("a stale socket was not replaced: %v", err)
	}
	ln2.Close()
}

// BenchmarkHookAnswers times representative hooks through the full handler stack (auth,
// body limit, route gate), with the resident worker faked: a PreToolUse that pushes a
// path-bound memory, a PreToolUse search, a PostToolUse capture of a 30,000-character
// Bash output (Claude Code's clamp), and a SessionStart.
func BenchmarkHookAnswers(b *testing.B) {
	f := newHookFixture(b)
	read := fmt.Sprintf(`{"session_id":"%%d","cwd":%q,"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":%q}}`,
		f.root, filepath.Join(f.root, "src", "billing.go"))
	grep := fmt.Sprintf(`{"session_id":"%%d","cwd":%q,"hook_event_name":"PreToolUse","tool_name":"Grep","tool_input":{"pattern":"roundHalfEven"}}`, f.root)
	start := fmt.Sprintf(`{"session_id":"%%d","cwd":%q,"hook_event_name":"SessionStart","source":"startup"}`, f.root)
	stdout := goTestLog(1500)[:30000]
	cases := []struct {
		name, event string
		body        func(i int) string
	}{
		{"PreToolUse-Read", "PreToolUse", func(i int) string { return fmt.Sprintf(read, i) }},
		{"PreToolUse-Grep", "PreToolUse", func(i int) string { return fmt.Sprintf(grep, i) }},
		{"PostToolUse-Bash-30k", "PostToolUse", func(i int) string { return postToolUseBash(f.root, fmt.Sprint(i), "", stdout) }},
		{"SessionStart", "SessionStart", func(i int) string { return fmt.Sprintf(start, i) }},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for i := 0; b.Loop(); i++ {
				req, _ := http.NewRequest("POST", f.srv.URL+"/api/hooks/claude/"+c.event, strings.NewReader(c.body(i)))
				req.Header.Set("Authorization", "Bearer "+f.agent)
				resp, err := http.DefaultClient.Do(req)
				if err != nil || resp.StatusCode != 200 {
					b.Fatalf("%v %v", err, resp)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		})
	}
}

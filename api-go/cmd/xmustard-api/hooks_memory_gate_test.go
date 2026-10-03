package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/hooks"
	"xmustard/api-go/internal/workspaceops"
)

// hookMemoryFixture is newHookFixture behind an exposure posture, so a hook answer can
// be read under XMUSTARD_DISABLED_TOOLS. It is the only way the hook routes see a
// deployment's disabled tools: postureFrom reads what the route gate middleware puts
// in the request context, and only buildHandler wires that middleware.
func hookMemoryFixture(t *testing.T, p exposurePosture) *hookFixture {
	t.Helper()
	f := newHookFixture(t)
	p.Loopback = true
	f.srv.Close()
	f.srv = httptest.NewServer(buildHandler(serverConfig{host: "127.0.0.1", authMode: "auto", dataDir: f.dir, posture: p}, newAPIHandlerFor(p)))
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() { waitRegistrationBaselines(t) })
	return f
}

// hookMemoryAnswers posts the events that inject memory and returns each answer's
// additionalContext. key is the memory the event pushes when recall is served, asserted
// when require is set, so a fixture that injects nothing cannot make the refusal tests
// pass vacuously.
func hookMemoryAnswers(t *testing.T, f *hookFixture, require bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, step := range []struct{ session, event, key string }{
		{"s1", "SessionStart", "billing rounding"},
		{"s2", "SubagentStart", "billing rounding"},
		{"s3", "UserPromptSubmit", "deploy canary"},
		{"s4", "PreToolUse", "billing rounding"},
	} {
		fields := map[string]any{"session_id": step.session, "cwd": f.root, "hook_event_name": step.event}
		switch step.event {
		case "UserPromptSubmit":
			fields["prompt"] = "how do we deploy this safely"
		case "PreToolUse":
			fields["tool_name"] = "Read"
			fields["tool_input"] = map[string]any{"file_path": filepath.Join(f.root, "src", "billing.go")}
		case "SubagentStart":
			fields["agent_id"], fields["agent_type"] = "a1", "Explore"
		}
		_, body := f.post(t, f.agent, step.event, hookEvent(fields))
		out[step.event] = hookContext(t, body)
		if require && !strings.Contains(out[step.event], step.key) {
			t.Fatalf("%s: the served answer does not carry %q, so this test cannot prove a refusal:\n%s", step.event, step.key, out[step.event])
		}
	}
	return out
}

// hookMemoryTexts are the words that would appear in any memory the fixture's three
// promoted memories push: their titles and one memory's content. A refusal test asserts
// none of them reaches an answer.
var hookMemoryTexts = []string{
	"billing rounding",         // the human-approved core tier, pushed by the projection
	"billing currency",         // a peer-verified memory listed by the deferred index
	"deploy canary",            // the keyword-triggered memory of UserPromptSubmit
	"Invoices round half-even", // the core tier memory's content
}

// A deployment that disables recall serves no memory to an agent, however the memory
// would reach it. The hook routes name no tool, so security_middleware.go's tool_disabled
// branch never fires for them and a hook injects the same core tier and recalled memories
// the recall routes refuse. The positive control runs first on the same fixture with
// recall served, so the refusal cannot pass because the fixture had no memory to push.
func TestDisabledRecallInjectsNoMemoryThroughHooks(t *testing.T) {
	hookMemoryAnswers(t, hookMemoryFixture(t, exposurePosture{}), true)

	refused := hookMemoryAnswers(t, hookMemoryFixture(t, exposurePosture{DisabledTools: map[string]bool{"recall": true}}), false)
	for event, context := range refused {
		for _, memory := range hookMemoryTexts {
			if strings.Contains(context, memory) {
				t.Errorf("%s injected %q with recall disabled:\n%s", event, memory, context)
			}
		}
	}
}

// A hook that serves no memory must not lose the rest of its answer with it. SessionStart
// also carries watchPaths and the ground line, and CwdChanged carries the watch list and
// nothing else, so disabling recall withdraws memory and not the hook: an operator who
// disables recall on a deployment keeps watchPaths, the restored snapshot and the handoff.
func TestDisabledRecallKeepsTheNonMemoryPartsOfAHookAnswer(t *testing.T) {
	f := hookMemoryFixture(t, exposurePosture{DisabledTools: map[string]bool{"recall": true}})
	_, body := f.post(t, f.agent, "SessionStart", hookEvent(map[string]any{
		"session_id": "watch", "cwd": f.root, "hook_event_name": "SessionStart", "source": "startup"}))
	var out hooks.Output
	if err := json.Unmarshal(body, &out); err != nil || out.HookSpecificOutput == nil {
		t.Fatalf("SessionStart answer: %s", body)
	}
	if out.HookSpecificOutput.WatchPaths == nil {
		t.Errorf("SessionStart dropped watchPaths with recall disabled: %s", body)
	}
	if !strings.Contains(out.HookSpecificOutput.AdditionalContext, f.ws) {
		t.Errorf("SessionStart dropped the ground line with recall disabled: %s", out.HookSpecificOutput.AdditionalContext)
	}
}

// The hook routes carry no Tool today, on purpose: naming one makes the middleware refuse
// the whole route with 403 tool_disabled, which would take watchPaths, the restored snapshot
// and the handoff away with the memories. So the gate stays empty and the refusal is the
// handler's, consulting the same tool gate a recall call passes. This asserts the table
// shape that decision depends on: if a future row names a tool on a hook route, the
// middleware refuses that route outright and the handler's guard becomes redundant, so the
// row must come back and re-decide.
func TestHookGateRowsNameNoToolSoMemoryIsWithdrawnPerSection(t *testing.T) {
	for _, pattern := range sortedGatePatterns() {
		if !strings.HasPrefix(pattern, "POST /api/hooks/") {
			continue
		}
		if tool := routeGateTable[pattern].Tool; tool != "" {
			t.Errorf("%s names tool %q: the middleware would refuse the whole hook with 403 tool_disabled", pattern, tool)
		}
	}
}

// Withholding memory must withhold only memory. A search-shaped PreToolUse still gets its
// index hits, which come from the resident index and not from the memory store, so a
// deployment that disables recall keeps them. This is the observable half of round 4's
// r4-2 fix, which moved the refusal ahead of the store reads: it says the answer does not
// depend on the memory store being readable.
func TestDisabledRecallKeepsTheNonMemoryContextOfAPreToolUse(t *testing.T) {
	f := hookMemoryFixture(t, exposurePosture{DisabledTools: map[string]bool{"recall": true}})
	_, body := f.post(t, f.agent, "PreToolUse", hookEvent(map[string]any{
		"session_id": "s", "cwd": f.root, "hook_event_name": "PreToolUse", "tool_name": "Grep",
		"tool_input": map[string]any{"pattern": "total"}}))
	context := hookContext(t, body)
	if !strings.Contains(context, "src/billing.go") {
		t.Errorf("a search PreToolUse lost its index hits with recall disabled:\n%s", context)
	}
	if strings.Contains(context, "billing rounding") {
		t.Errorf("a search PreToolUse injected memory with recall disabled:\n%s", context)
	}
	if f.searches.Load() == 0 {
		t.Error("the index search did not run, so the answer did not come from it")
	}
}

// The refusal has to come before the store read, not after the ranking is discarded. A guard
// on push's output alone produces a byte-identical answer, so this row first shipped that
// guard alone and the mutation that removed the pre-read guard SURVIVED (M6 in S5): nothing
// could tell the two placements apart. The counter is what tells them apart — it is the same
// shape stalenessChecks already uses, and the same reason.
func TestDisabledRecallRanksNoMemoryForAHook(t *testing.T) {
	// The counter is process-wide, so compare deltas: another test in this binary ranking
	// memory is not this test's failure, and asserting an absolute zero would be.
	before := workspaceops.HookMemoryRanks()
	f := hookMemoryFixture(t, exposurePosture{DisabledTools: map[string]bool{"recall": true}})
	for _, step := range []struct{ session, event string }{{"a", "PreToolUse"}, {"b", "PreToolUse"}} {
		fields := map[string]any{"session_id": step.session, "cwd": f.root, "hook_event_name": step.event,
			"tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(f.root, "src", "billing.go")}}
		if step.session != "a" {
			fields["agent_id"], fields["agent_type"] = "a1", "Explore" // a second context, so the "already seen" path cannot answer
		}
		f.post(t, f.agent, step.event, hookEvent(fields))
	}
	if n := workspaceops.HookMemoryRanks() - before; n != 0 {
		t.Errorf("a refused hook ranked the memory store %d times; the refusal must come before the read", n)
	}

	after := workspaceops.HookMemoryRanks()
	served := hookMemoryFixture(t, exposurePosture{})
	_, body := served.post(t, served.agent, "PreToolUse", hookEvent(map[string]any{
		"session_id": "c", "cwd": served.root, "hook_event_name": "PreToolUse", "tool_name": "Grep",
		"tool_input": map[string]any{"pattern": "total"}}))
	if !strings.Contains(hookContext(t, body), "billing rounding") {
		t.Fatalf("with recall served the hook pushed no memory, so the counter cannot see the refused case either: %s", body)
	}
	if workspaceops.HookMemoryRanks() == after {
		t.Error("with recall served the hook ranked nothing, so the counter cannot see the refused case either")
	}
}

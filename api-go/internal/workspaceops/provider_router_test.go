package workspaceops

import (
	"strings"
	"testing"
)

func TestClassifyTask(t *testing.T) {
	cases := []struct {
		prompt   string
		hasImage bool
		hint     string
		wantType string
	}{
		{"here is a screenshot", true, "", "vision_ui_diagnose"},
		{"write unit tests for the parser", false, "", "test_gen_validate"},
		{"refactor the auth handler", false, "", "code_edit_patch"},
		{"debug why the index goes stale", false, "", "multi_step_debug_reason"},
		{"where is the symbol graph built", false, "", "locate"},
		{"explain how RRF fusion works", false, "", "repo_qa_explain"},
		{"anything", false, "code_edit_patch", "code_edit_patch"}, // explicit hint override
		{"just chatting", false, "", "repo_qa_explain"},           // default
	}
	for _, c := range cases {
		got, _ := ClassifyTask(c.prompt, c.hasImage, c.hint)
		if got != c.wantType {
			t.Errorf("ClassifyTask(%q, img=%v, hint=%q) = %q, want %q", c.prompt, c.hasImage, c.hint, got, c.wantType)
		}
	}
}

func TestRouteModelCapabilityAndRules(t *testing.T) {
	dir := t.TempDir()
	// no providers → classification only
	r, err := RouteModel(dir, RouteRequest{Prompt: "explain this"})
	if err != nil || r.Provider != "" || r.TaskType != "repo_qa_explain" {
		t.Fatalf("no-provider route: %v %+v", err, r)
	}

	// configure a text provider (coder default) and a vision provider
	if _, err := AddOpenAIProvider(dir, OpenAIProvider{Name: "ollama-coder", Kind: "ollama", BaseURL: "http://x:1", DefaultModel: "qwen2.5-coder"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddOpenAIProvider(dir, OpenAIProvider{Name: "ollama-vision", Kind: "ollama", BaseURL: "http://x:2", DefaultModel: "llama3.2-vision", SupportsVision: true}); err != nil {
		t.Fatal(err)
	}

	// a code task should match the coder provider by model name
	r, _ = RouteModel(dir, RouteRequest{Prompt: "implement the cache layer"})
	if r.Provider != "ollama-coder" || r.ModelClass != "code-specialized" {
		t.Fatalf("code task routed to %+v", r)
	}

	// a vision task must pick the supports_vision provider
	r, _ = RouteModel(dir, RouteRequest{Prompt: "what is in this ui", HasImage: true})
	if r.Provider != "ollama-vision" || r.ModelClass != "vision" {
		t.Fatalf("vision task routed to %+v", r)
	}

	// an explicit rule overrides heuristics
	if _, err := SetModelRoute(dir, RoutingRule{TaskType: "locate", Provider: "ollama-coder", Model: "tiny"}); err != nil {
		t.Fatal(err)
	}
	r, _ = RouteModel(dir, RouteRequest{Prompt: "where is the parser"})
	if r.Provider != "ollama-coder" || r.Model != "tiny" || !strings.Contains(r.Reason, "explicit") {
		t.Fatalf("rule override failed: %+v", r)
	}
}

func TestActiveContextInjectedIntoPrompt(t *testing.T) {
	dir := t.TempDir()
	ws := "wsP"
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})

	// no promoted entries → prompt unchanged
	if got := applyActiveContextToPrompt(dir, ws, "BASE"); got != "BASE" {
		t.Fatalf("empty context should pass through, got %q", got)
	}

	// single-agent mode promotes immediately
	if _, err := ProposeContext(dir, ws, ProposeContextRequest{
		Title: "API base", Content: "the api base path is /api", Source: "solo",
	}); err != nil {
		t.Fatal(err)
	}
	got := applyActiveContextToPrompt(dir, ws, "BASE PROMPT")
	if !strings.Contains(got, "Verified shared context") || !strings.Contains(got, "/api") || !strings.HasSuffix(got, "BASE PROMPT") {
		t.Fatalf("verified context not injected: %q", got)
	}
}

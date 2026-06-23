package workspaceops

import (
	"strings"
	"testing"
)

// Feature-depth: structured, operator-declared provider capabilities take precedence over
// the model-name substring heuristic — so routing reflects intent, not a lucky name match.
func TestRouteModelPrefersDeclaredCapability(t *testing.T) {
	dir := t.TempDir()

	// Provider A's model name looks like a coder ("...-coder"), but A declares only
	// small-fast. Provider B has a generic model name (no coder/code/reason hint) yet
	// DECLARES code-specialized. A code task must route to B (declared), not A (name).
	if _, err := AddOpenAIProvider(dir, OpenAIProvider{
		Name: "a-namecoder", Kind: "ollama", BaseURL: "http://x:1",
		DefaultModel: "fancy-coder-7b", Capabilities: []string{"small-fast"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddOpenAIProvider(dir, OpenAIProvider{
		Name: "b-declared", Kind: "ollama", BaseURL: "http://x:2",
		DefaultModel: "glm-9", Capabilities: []string{"code-specialized"},
	}); err != nil {
		t.Fatal(err)
	}

	r, err := RouteModel(dir, RouteRequest{Prompt: "implement the cache layer"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ModelClass != "code-specialized" {
		t.Fatalf("expected code-specialized class, got %q", r.ModelClass)
	}
	if r.Provider != "b-declared" {
		t.Fatalf("declared capability must win over the name heuristic; routed to %+v", r)
	}
	if !strings.Contains(r.Reason, "declared capability") {
		t.Fatalf("reason should cite the structured signal, got %q", r.Reason)
	}

	// With no declared capability anywhere, the name heuristic still applies (fallback).
	dir2 := t.TempDir()
	if _, err := AddOpenAIProvider(dir2, OpenAIProvider{
		Name: "namecoder", Kind: "ollama", BaseURL: "http://x:1", DefaultModel: "qwen2.5-coder",
	}); err != nil {
		t.Fatal(err)
	}
	r2, _ := RouteModel(dir2, RouteRequest{Prompt: "implement the cache layer"})
	if r2.Provider != "namecoder" || !strings.Contains(r2.Reason, "by model name") {
		t.Fatalf("name-heuristic fallback failed: %+v", r2)
	}
}

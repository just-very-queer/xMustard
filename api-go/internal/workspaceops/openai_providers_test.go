package workspaceops

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIProviderCRUDAndClient(t *testing.T) {
	dir := t.TempDir()

	// mock OpenAI-compatible server (Ollama/vLLM-style)
	var lastChatBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "llama3.2-vision"}, {"id": "qwen2.5-coder"}},
			})
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &lastChatBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":   "llama3.2-vision",
				"choices": []map[string]any{{"message": map[string]any{"content": "hello from mock"}}},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// add a provider
	if _, err := AddOpenAIProvider(dir, OpenAIProvider{
		Name: "local-ollama", Kind: "ollama", BaseURL: srv.URL,
		DefaultModel: "qwen2.5-coder", SupportsVision: true,
	}); err != nil {
		t.Fatalf("add provider: %v", err)
	}

	providers, err := ListOpenAIProviders(dir)
	if err != nil || len(providers) != 1 {
		t.Fatalf("list providers: %v len=%d", err, len(providers))
	}
	if providers[0].BaseURL != srv.URL {
		t.Fatalf("base url not persisted: %q", providers[0].BaseURL)
	}

	// list models
	models, err := ListProviderModels(dir, "local-ollama")
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if models["count"].(int) != 2 {
		t.Fatalf("expected 2 models, got %v", models["count"])
	}

	// probe
	probe, err := ProbeOpenAIProvider(dir, "local-ollama")
	if err != nil || probe["ok"] != true {
		t.Fatalf("probe failed: %v %v", err, probe)
	}

	// text chat
	chat, err := OpenAIChat(dir, "local-ollama", ChatRequest{Prompt: "hi"})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if chat["content"] != "hello from mock" {
		t.Fatalf("unexpected chat content: %v", chat["content"])
	}
	if chat["vision"] != false {
		t.Fatalf("expected non-vision chat")
	}

	// vision chat: the user message must carry an image_url part
	visionChat, err := OpenAIChat(dir, "local-ollama", ChatRequest{
		Prompt: "describe", ImageURLs: []string{"data:image/png;base64,AAAA"},
	})
	if err != nil {
		t.Fatalf("vision chat: %v", err)
	}
	if visionChat["vision"] != true {
		t.Fatalf("expected vision=true")
	}
	// verify the last request actually sent multimodal content parts
	msgs, _ := lastChatBody["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	parts, ok := last["content"].([]any)
	if !ok || len(parts) < 2 {
		t.Fatalf("vision request did not send content parts: %v", last["content"])
	}

	// removing
	if err := RemoveOpenAIProvider(dir, "local-ollama"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if providers, _ := ListOpenAIProviders(dir); len(providers) != 0 {
		t.Fatalf("provider not removed")
	}
}

func TestVisionRequiresSupportsVision(t *testing.T) {
	dir := t.TempDir()
	if _, err := AddOpenAIProvider(dir, OpenAIProvider{
		Name: "text-only", Kind: "openai", BaseURL: "http://127.0.0.1:9", SupportsVision: false,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	_, err := OpenAIChat(dir, "text-only", ChatRequest{
		Prompt: "x", Model: "gpt", ImageURLs: []string{"data:image/png;base64,AAAA"},
	})
	if err == nil || !strings.Contains(err.Error(), "supports_vision") {
		t.Fatalf("expected supports_vision rejection, got %v", err)
	}
}

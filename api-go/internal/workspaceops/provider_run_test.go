package workspaceops

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsProviderRuntime(t *testing.T) {
	cases := map[string]struct {
		ok       bool
		provider string
	}{
		"provider:ollama": {true, "ollama"},
		"route":           {true, ""},
		"codex":           {false, ""},
		"opencode":        {false, ""},
	}
	for rt, want := range cases {
		p, ok := isProviderRuntime(rt)
		if ok != want.ok || p != want.provider {
			t.Errorf("isProviderRuntime(%q) = (%q,%v), want (%q,%v)", rt, p, ok, want.provider, want.ok)
		}
	}
}

func TestStartProviderRunRecordsRun(t *testing.T) {
	dir := t.TempDir()
	ws := "wsProv"
	// minimal workspace snapshot
	if err := writeJSON(filepath.Join(dir, "workspaces", ws, "snapshot.json"),
		map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": t.TempDir()}}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":   "qwen2.5-coder",
				"choices": []map[string]any{{"message": map[string]any{"content": "done: refactored auth"}}},
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := AddOpenAIProvider(dir, OpenAIProvider{
		Name: "local", Kind: "ollama", BaseURL: srv.URL, DefaultModel: "qwen2.5-coder",
	}); err != nil {
		t.Fatal(err)
	}

	run, err := StartProviderRun(dir, ws, "local", "qwen2.5-coder", "refactor the auth handler")
	if err != nil {
		t.Fatalf("StartProviderRun: %v", err)
	}
	if run.Status != "succeeded" || run.Runtime != "provider:local" {
		t.Fatalf("unexpected run: status=%s runtime=%s", run.Status, run.Runtime)
	}
	out, _ := os.ReadFile(run.OutputPath)
	if !strings.Contains(string(out), "refactored auth") {
		t.Fatalf("run output not captured: %q", out)
	}

	// the run is persisted and shows up in the run list.
	runs, _ := listRuns(dir, ws)
	if len(runs) != 1 || runs[0].RunID != run.RunID {
		t.Fatalf("provider run not persisted in run list: %+v", runs)
	}
}

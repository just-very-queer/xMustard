package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Search must not write the agent-feedback store on the request path; the buffered
// retrievals must still be durable once the API shuts down gracefully.
func TestSearchFeedbackBufferedThenPersistedOnSIGTERM(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	hits := `{"workspace_id":"ws","query":"x","total":2,"hits":[` +
		`{"kind":"file","name":"a.go","path":"a.go","score":1,"reason":"match"},` +
		`{"kind":"file","name":"b.go","path":"b.go","score":1,"reason":"match"}]}`
	core := writeScript(t, "cat <<'EOF'\n"+hits+"\nEOF\n")
	p := startAPIProc(t, map[string]string{
		"XMUSTARD_DATA_DIR":                dir,
		"XMUSTARD_CORE_BIN":                core,
		"XMUSTARD_FEEDBACK_FLUSH_INTERVAL": "1h",
	})
	const searches = 3
	for i := 0; i < searches; i++ {
		resp, err := testClient.Get(p.base + "/api/workspaces/ws/search?q=x")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("search: status %d; log:\n%s", resp.StatusCode, p.stderr.String())
		}
	}
	store := filepath.Join(dir, "workspaces", "ws", "agent_feedback.json")
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("search wrote the feedback store on the request path (stat err=%v)", err)
	}

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	p.waitExit(t, 30*time.Second)

	raw, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("shutdown did not persist buffered feedback: %v; log:\n%s", err, p.stderr.String())
	}
	var entries []struct {
		Path           string `json:"path"`
		RetrievalCount int    `json:"retrieval_count"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, e := range entries {
		got[e.Path] = e.RetrievalCount
	}
	if got["a.go"] != searches || got["b.go"] != searches {
		t.Fatalf("want %d retrievals each for a.go and b.go, got %v", searches, got)
	}
}

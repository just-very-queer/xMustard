package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// diagnosticsReadFixture publishes one 2,500-row local baseline (the per-import row
// cap, shaped like the RSS workload's report) into a committed git repo and returns
// the handler and the GET path.
func diagnosticsReadFixture(tb testing.TB) (http.Handler, string, string) {
	tb.Helper()
	h, ws, repo := localDiagnosticsServer(tb, false)
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=b@xmustard.invalid", "-c", "user.name=b"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			tb.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	rows := make([]map[string]any, 2500)
	for i := range rows {
		rows[i] = map[string]any{"path": "src/app.go", "message": fmt.Sprintf("rss_diag_%d: synthetic", i), "severity": 2,
			"range": map[string]any{"start": map[string]int{"line": i % 60, "character": 0}, "end": map[string]int{"line": i % 60, "character": 8}}}
	}
	raw, _ := json.Marshal(rows)
	fixture := filepath.Join(repo, ".xmustard-e2e")
	_ = os.MkdirAll(fixture, 0o755)
	_ = os.WriteFile(filepath.Join(fixture, "d.json"), raw, 0o644)
	_ = os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".xmustard-e2e/\n"), 0o644)
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "fixture")
	base := "/api/workspaces/" + ws
	if rec := do(tb, h, "POST", base+"/diagnostics/run", `{"input_path":".xmustard-e2e/d.json","source_kind":"compiler","source_name":"bench"}`); rec.Code != http.StatusOK {
		tb.Fatalf("seed run: %d %s", rec.Code, rec.Body)
	}
	return h, base + "/diagnostics", repo
}

// BenchmarkLocalDiagnosticsGet measures one full GET /diagnostics (git probe,
// envelope read and verification, decode, rows, response encode).
func BenchmarkLocalDiagnosticsGet(b *testing.B) {
	h, path, _ := diagnosticsReadFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if rec := do(b, h, "GET", path, ""); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"available"`)) {
			b.Fatalf("GET: %d", rec.Code)
		}
	}
}

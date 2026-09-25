package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedGroundBenchWorkspace builds a workspace shaped like a real one: a multi-MB
// snapshot.json written as the scanner writes it (5.4 MB here; the measured one on
// this machine is 3.8 MB), a run history with failures, promoted memories with path
// baselines, and a fake Rust core that answers drift and working-changes instantly,
// so the benchmark measures the Go side of `ground`.
func seedGroundBenchWorkspace(tb testing.TB) (dataDir, ws string) {
	tb.Helper()
	dataDir = tb.TempDir()
	ws = "wsGroundBench"
	root := tb.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.go"), []byte("package f\n"), 0o644); err != nil {
		tb.Fatal(err)
	}
	summary := strings.Repeat("summary text for a synthetic issue ", 16)
	issues := make([]issueRecord, 0, 4000)
	for i := 0; i < 4000; i++ {
		line := i
		issues = append(issues, issueRecord{
			BugID: fmt.Sprintf("BUG-%05d", i), Title: fmt.Sprintf("issue %d", i), Severity: "P2",
			IssueStatus: "open", Source: "scanner", DocStatus: "ok", CodeStatus: "ok", Summary: &summary,
			Evidence: []evidenceRef{{Path: fmt.Sprintf("src/f%04d.go", i), Line: &line}},
			Labels:   []string{"a", "b"}, UpdatedAt: "2026-09-25T00:00:00Z",
		})
	}
	// written the way the scanner writes it: the workspaceSnapshot struct
	if err := writeJSON(filepath.Join(dataDir, "workspaces", ws, "snapshot.json"), workspaceSnapshot{
		ScannerVersion: scannerVersion,
		Workspace:      workspaceRecord{WorkspaceID: ws, Name: "bench", RootPath: root},
		Issues:         issues,
	}); err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		status := "completed"
		if i%5 == 0 {
			status = "failed"
		}
		if err := writeJSON(filepath.Join(dataDir, "workspaces", ws, "runs", fmt.Sprintf("run_%04d.json", i)), map[string]any{
			"run_id": fmt.Sprintf("run_%04d", i), "workspace_id": ws, "status": status,
			"prompt": summary, "created_at": fmt.Sprintf("2026-09-25T00:00:%02dZ", i),
		}); err != nil {
			tb.Fatal(err)
		}
	}
	entries := make([]ContextEntry, 0, 50)
	for i := 0; i < 50; i++ {
		entries = append(entries, ContextEntry{
			ID: fmt.Sprintf("m%04d", i), WorkspaceID: ws, Content: "c", Status: "verified", Promoted: true,
			Paths: []string{"f.go"}, PathHashes: capturePathHashes(root, []string{"f.go"}),
			SearchTokens: []string{"c"},
			CreatedAt:    fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i), UpdatedAt: fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
		})
	}
	if err := saveContextEntries(dataDir, ws, entries); err != nil {
		tb.Fatal(err)
	}
	core := filepath.Join(tb.TempDir(), "xmustard-core")
	script := `#!/bin/sh
case "$1 $2" in
"changetrack drift") echo '{"stale":false,"head_sha":"abc"}' ;;
"changetrack working-changes") echo '{"changed_files":[{"path":"f.go"}],"dirty_symbols":[{"path":"f.go","symbol":"F","contract_break":true,"signature_change":"args"}],"contract_breaks":1}' ;;
*) echo '{}' ;;
esac
`
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		tb.Fatal(err)
	}
	tb.Setenv("XMUSTARD_CORE_BIN", core)
	return dataDir, ws
}

// BenchmarkBuildSessionGrounding reports the Go allocations of one `ground` over a
// workspace with a multi-MB snapshot (run with -benchmem).
func BenchmarkBuildSessionGrounding(b *testing.B) {
	dataDir, ws := seedGroundBenchWorkspace(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := BuildSessionGrounding(dataDir, ws); err != nil {
			b.Fatal(err)
		}
	}
}

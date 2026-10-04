package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// WS-RT3: the core's signal scan reads at most 10,000 files, and the workspace scan
// reports how many it read and whether that bound cut the scan.
func TestTheWorkspaceScanReportsTheSignalScanBound(t *testing.T) {
	t.Setenv("XMUSTARD_CORE_BIN", rustCoreBin(t))
	t.Setenv("XMUSTARD_CORE_WORKER", "")
	dataDir := t.TempDir()
	src := filepath.Join(t.TempDir(), "repo", "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.py", "# TODO: fix export\n")
	write("b.py", "x = 1\n")
	snapshot, err := LoadWorkspace(dataDir, WorkspaceLoadRequest{
		RootPath:             filepath.Dir(src),
		AutoScan:             true,
		PreferCachedSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := func(s *workspaceSnapshot) [3]int {
		return [3]int{s.Summary["signals_total"], s.Summary["signals_files_read"], s.Summary["signals_truncated"]}
	}
	if got := counts(snapshot); got != [3]int{1, 2, 0} {
		t.Fatalf("signals, files read, truncated = %v, want [1 2 0]", got)
	}

	for i := range 9_999 {
		write(fmt.Sprintf("m%05d.py", i), "x = 1\n")
	}
	again, err := ScanWorkspace(dataDir, snapshot.Workspace.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if got := counts(again); got != [3]int{1, 10_000, 1} {
		t.Fatalf("signals, files read, truncated = %v, want [1 10000 1]", got)
	}
}

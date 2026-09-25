package workspaceops

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// Several agents' first MCP calls register their repositories at once (the MCP
// server auto-registers a roots or cwd repository). The registry is one JSON file
// rewritten whole, so concurrent registrations must not drop each other's records.
func TestConcurrentRegistrationsKeepEveryRecord(t *testing.T) {
	data := t.TempDir()
	const n = 24
	roots := make([]string, n)
	for i := range roots {
		roots[i] = filepath.Join(t.TempDir(), fmt.Sprintf("repo-%02d", i))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, root := range roots {
		wg.Add(1)
		go func(root string) {
			defer wg.Done()
			<-start
			// no snapshot and no scan: the record is saved, then ErrNotExist is reported
			_, _ = LoadWorkspace(data, WorkspaceLoadRequest{RootPath: root, PreferCachedSnapshot: true})
		}(root)
	}
	close(start)
	wg.Wait()
	list, err := ListWorkspaces(data)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, w := range list {
		got[w.RootPath] = true
	}
	var missing []string
	for _, root := range roots {
		if !got[root] {
			missing = append(missing, filepath.Base(root))
		}
	}
	if len(missing) > 0 || len(list) != n {
		t.Fatalf("lost update: %d of %d registrations missing (%v); registry holds %d", len(missing), n, missing, len(list))
	}
}

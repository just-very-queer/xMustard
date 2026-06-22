package workspaceops

import (
	"fmt"
	"sync"
	"testing"
)

// Concurrent proposals to the same workspace must not lose updates — the per-store
// transaction lock serializes the load→mutate→save window (P0-1). Without it, the
// unlocked read-modify-write drops most concurrent appends.
func TestConcurrentProposeNoLostUpdate(t *testing.T) {
	dir := t.TempDir()
	ws := "wsConc"
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = ProposeContext(dir, ws, ProposeContextRequest{
				Content: fmt.Sprintf("memory body %d", i),
				Title:   fmt.Sprintf("t%d", i),
			})
		}(i)
	}
	wg.Wait()
	entries, err := loadContextEntries(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != n {
		t.Fatalf("expected %d entries after concurrent proposals, got %d (lost updates)", n, len(entries))
	}
}

// Concurrent verify votes from distinct agents on one entry must all be recorded
// (the multi-agent promotion gate depends on no vote being silently lost).
func TestConcurrentVotesNotDropped(t *testing.T) {
	dir := t.TempDir()
	ws := "wsVote"
	entry, err := ProposeContext(dir, ws, ProposeContextRequest{Content: "shared fact", Title: "f"})
	if err != nil {
		t.Fatal(err)
	}
	const n = 25
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = VerifyContext(dir, ws, entry.ID, fmt.Sprintf("agent-%d", i), true, "")
		}(i)
	}
	wg.Wait()
	entries, err := loadContextEntries(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	var found *ContextEntry
	for i := range entries {
		if entries[i].ID == entry.ID {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatal("entry vanished")
	}
	distinct := map[string]bool{}
	for _, v := range found.Verifications {
		distinct[v.Agent] = true
	}
	if len(distinct) != n {
		t.Fatalf("expected %d distinct votes recorded, got %d (votes dropped)", n, len(distinct))
	}
}

// Concurrent feedback appends to the same store must not drop entries.
func TestConcurrentFeedbackNoLostUpdate(t *testing.T) {
	dir := t.TempDir()
	ws := "wsFb"
	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = RecordFeedback(dir, ws, "retrieval", []string{fmt.Sprintf("file-%d.go", i)})
		}(i)
	}
	wg.Wait()
	m, err := loadFeedback(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != n {
		t.Fatalf("expected %d feedback paths, got %d (lost updates)", n, len(m))
	}
}

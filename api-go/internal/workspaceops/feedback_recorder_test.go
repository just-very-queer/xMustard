package workspaceops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// useFeedbackRecorder installs a fresh recorder for one test. The flush interval
// defaults to an hour so only the test decides when a flush happens.
func useFeedbackRecorder(t *testing.T, maxPending int) *feedbackRecorder {
	t.Helper()
	t.Setenv("XMUSTARD_FEEDBACK_FLUSH_INTERVAL", "1h")
	prev := feedbackRec
	rec := newFeedbackRecorder(maxPending)
	feedbackRec = rec
	t.Cleanup(func() { feedbackRec = prev })
	return rec
}

func seedFeedbackWorkspace(t *testing.T, ws string) string {
	t.Helper()
	dir := t.TempDir()
	if err := writeJSON(filepath.Join(dir, "workspaces", ws, "snapshot.json"),
		map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func searchResultJSON(t *testing.T, ws string, paths ...string) json.RawMessage {
	t.Helper()
	hits := make([]searchHit, 0, len(paths))
	for _, p := range paths {
		hits = append(hits, searchHit{Kind: "file", Name: p, Path: p, Score: 1.0, Reason: "match"})
	}
	raw, err := json.Marshal(searchResult{WorkspaceID: ws, Query: "q", Total: len(hits), Hits: hits})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func feedbackStoreExists(dir, ws string) bool {
	_, err := os.Stat(feedbackPath(dir, ws))
	return err == nil
}

// The public search path must not write the feedback store per call; the retrievals
// it recorded become durable on flush.
func TestSearchWithFeedbackDoesNotWriteStorePerCall(t *testing.T) {
	ws := "wsSearchFb"
	dir := seedFeedbackWorkspace(t, ws)
	rec := useFeedbackRecorder(t, feedbackMaxPendingPaths)
	core := filepath.Join(t.TempDir(), "xmustard-core")
	script := "#!/bin/sh\ncat <<'EOF'\n" + string(searchResultJSON(t, ws, "a.go", "b.go")) + "\nEOF\n"
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)

	for i := 0; i < 3; i++ {
		if _, err := WorkspaceSearchWithFeedback(dir, ws, "q", "", 10); err != nil {
			t.Fatal(err)
		}
	}
	if feedbackStoreExists(dir, ws) {
		t.Fatal("search wrote agent_feedback.json on the request path")
	}
	rec.flush()
	m, err := loadFeedback(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.go", "b.go"} {
		if m[p] == nil || m[p].RetrievalCount != 3 {
			t.Fatalf("after flush %s should have 3 retrievals, got %+v", p, m[p])
		}
	}
}

// Many concurrent searches coalesce into one store write, and a synchronous verify
// racing them is not lost.
func TestConcurrentSearchesCoalesceIntoOneWrite(t *testing.T) {
	ws := "wsCoalesce"
	dir := seedFeedbackWorkspace(t, ws)
	rec := useFeedbackRecorder(t, feedbackMaxPendingPaths)
	raw := searchResultJSON(t, ws, "a.go", "b.go", "c.go")
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fuseSearchFeedback(dir, ws, raw)
		}()
	}
	if err := RecordFeedback(dir, ws, "verify", []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if got := rec.writes.Load(); got != 0 {
		t.Fatalf("searches wrote the store %d times before a flush", got)
	}
	rec.flush()
	if got := rec.writes.Load(); got != 1 {
		t.Fatalf("%d concurrent searches should flush in one write, got %d", n, got)
	}
	m, err := loadFeedback(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.go", "b.go", "c.go"} {
		if m[p] == nil || m[p].RetrievalCount != n {
			t.Fatalf("%s should have %d retrievals, got %+v", p, n, m[p])
		}
	}
	if m["a.go"].VerifyCount != 1 {
		t.Fatalf("synchronous verify lost next to the flush: %+v", m["a.go"])
	}
	if rec.pending(dir, ws) != nil {
		t.Fatal("a flushed store should leave no buffer behind")
	}
}

// Ranking sees buffered feedback before any flush, merged with the on-disk state.
func TestFeedbackBoostSeesPendingBeforeFlush(t *testing.T) {
	ws := "wsPending"
	dir := seedFeedbackWorkspace(t, ws)
	useFeedbackRecorder(t, feedbackMaxPendingPaths)
	if err := RecordFeedback(dir, ws, "run_fail", []string{"cold.go"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		fuseSearchFeedback(dir, ws, searchResultJSON(t, ws, "hot.go"))
	}
	boosts := feedbackBoosts(dir, ws)
	if boosts["hot.go"] <= 0 {
		t.Fatalf("buffered retrievals should boost hot.go before a flush, got %v", boosts["hot.go"])
	}
	if boosts["cold.go"] >= 0 {
		t.Fatalf("on-disk failure should still suppress cold.go, got %v", boosts["cold.go"])
	}
	var res searchResult
	if err := json.Unmarshal(fuseSearchFeedback(dir, ws, searchResultJSON(t, ws, "cold.go", "hot.go")), &res); err != nil {
		t.Fatal(err)
	}
	if res.Hits[0].Path != "hot.go" {
		t.Fatalf("buffered feedback should rank hot.go first, got %s", res.Hits[0].Path)
	}
	m, err := loadFeedback(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if m["hot.go"] != nil {
		t.Fatal("buffered retrievals reached the store without a flush")
	}
}

// Graceful shutdown persists buffered feedback.
func TestShutdownInFlightFlushesFeedback(t *testing.T) {
	ws := "wsShutdownFb"
	dir := seedFeedbackWorkspace(t, ws)
	rec := useFeedbackRecorder(t, feedbackMaxPendingPaths)
	fuseSearchFeedback(dir, ws, searchResultJSON(t, ws, "kept.go"))
	ShutdownInFlight(dir)
	m, err := loadFeedback(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if m["kept.go"] == nil || m["kept.go"].RetrievalCount != 1 {
		t.Fatalf("shutdown should persist buffered feedback, got %+v", m["kept.go"])
	}
	if rec.writes.Load() != 1 {
		t.Fatalf("expected one flush write, got %d", rec.writes.Load())
	}
}

// A full buffer evicts its oldest paths and counts them; enqueue never waits on the
// store, even while a flush is stuck behind the store lock, and readers still see the
// batch that flush is writing.
func TestFeedbackBufferOverflowIsCountedNotBlocking(t *testing.T) {
	ws := "wsOverflow"
	dir := seedFeedbackWorkspace(t, ws)
	rec := useFeedbackRecorder(t, 4)
	for i := 0; i < 10; i++ {
		rec.enqueue(dir, ws, "retrieval", []string{fmt.Sprintf("p%d.go", i)})
	}
	if got := rec.dropped.Load(); got != 6 {
		t.Fatalf("expected 6 evicted deltas, got %d", got)
	}
	pending := rec.pending(dir, ws)
	if len(pending) != 4 {
		t.Fatalf("buffer should hold 4 paths, got %d", len(pending))
	}
	for i := 6; i < 10; i++ {
		if _, ok := pending[fmt.Sprintf("p%d.go", i)]; !ok {
			t.Fatalf("newest path p%d.go was evicted; pending=%v", i, pending)
		}
	}

	unlock := lockStore(feedbackPath(dir, ws))
	flushed := make(chan struct{})
	go func() { rec.flush(); close(flushed) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.pending(dir, ws)) != 4 || !flushInFlight(rec, dir, ws) {
		if time.Now().After(deadline) {
			unlock()
			t.Fatal("flush never took the buffer")
		}
		time.Sleep(time.Millisecond)
	}
	enqueued := make(chan struct{})
	go func() {
		for i := 10; i < 20; i++ {
			rec.enqueue(dir, ws, "retrieval", []string{fmt.Sprintf("p%d.go", i)})
		}
		close(enqueued)
	}()
	select {
	case <-enqueued:
	case <-time.After(2 * time.Second):
		unlock()
		t.Fatal("enqueue blocked behind a flush waiting on the store lock")
	}
	if got := len(rec.pending(dir, ws)); got != 8 {
		t.Fatalf("readers should see the in-flight batch plus the new buffer (8 paths), got %d", got)
	}
	unlock()
	<-flushed
	m, err := loadFeedback(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 4 {
		t.Fatalf("flush should persist the 4 buffered paths, got %d", len(m))
	}
}

func flushInFlight(rec *feedbackRecorder, dir, ws string) bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	b := rec.buffers[feedbackStoreKey{dir, ws}]
	return b != nil && b.inflight != nil
}

// The timer flushes on its own after the configured interval.
func TestFeedbackTimerFlushes(t *testing.T) {
	ws := "wsTimer"
	dir := seedFeedbackWorkspace(t, ws)
	rec := useFeedbackRecorder(t, feedbackMaxPendingPaths)
	t.Setenv("XMUSTARD_FEEDBACK_FLUSH_INTERVAL", "20ms")
	fuseSearchFeedback(dir, ws, searchResultJSON(t, ws, "t.go"))
	deadline := time.Now().Add(5 * time.Second)
	for rec.writes.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timer never flushed the buffer")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !feedbackStoreExists(dir, ws) {
		t.Fatal("timer flush did not write the store")
	}
}

// A late flush does not recreate a workspace directory removed after the search.
func TestFeedbackFlushSkipsRemovedWorkspace(t *testing.T) {
	ws := "wsGone"
	dir := seedFeedbackWorkspace(t, ws)
	rec := useFeedbackRecorder(t, feedbackMaxPendingPaths)
	fuseSearchFeedback(dir, ws, searchResultJSON(t, ws, "x.go"))
	if err := os.RemoveAll(filepath.Join(dir, "workspaces", ws)); err != nil {
		t.Fatal(err)
	}
	rec.flush()
	if _, err := os.Stat(filepath.Join(dir, "workspaces", ws)); !os.IsNotExist(err) {
		t.Fatalf("flush recreated a removed workspace directory (stat err=%v)", err)
	}
}

func TestFeedbackFlushInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"":      defaultFeedbackFlushInterval,
		"500ms": 500 * time.Millisecond,
		"3":     3 * time.Second,
		"-1s":   defaultFeedbackFlushInterval,
		"0":     defaultFeedbackFlushInterval,
		"soon":  defaultFeedbackFlushInterval,
	}
	for in, want := range cases {
		t.Setenv("XMUSTARD_FEEDBACK_FLUSH_INTERVAL", in)
		if got := feedbackFlushInterval(); got != want {
			t.Errorf("interval(%q) = %v, want %v", in, got, want)
		}
	}
}

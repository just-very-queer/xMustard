package workspaceops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/govstore"
)

// WS-12: governed memory lives in the govstore database. Recall ranks on content-free
// metadata and reads the returned window's content from the served revisions, bound
// to the digest it was ranked with.

// Recall returns the served revision's content, and the derived binding fields never
// leak to callers. Nothing writes the legacy JSON files any more.
func TestRecallServesContentFromStoreRevision(t *testing.T) {
	dir := t.TempDir()
	ws := "wsContentStore"
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})

	entry, err := ProposeContext(dir, ws, ProposeContextRequest{
		Title: "budget rule", Content: "REAL spend ceiling MARKER_REAL", Source: "solo",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if !entry.Promoted {
		t.Fatalf("single-agent mode should promote immediately")
	}
	res, err := RecallContext(dir, ws, "spend ceiling MARKER_REAL", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != 1 || got[0].Content != "REAL spend ceiling MARKER_REAL" {
		t.Fatalf("recall must serve the promoted revision's content, got %+v", got)
	}
	if got[0].ContentHash != "" || got[0].ContentDigest != "" {
		t.Fatalf("derived binding fields must not leak to callers: %+v", got[0])
	}
	for _, name := range []string{"context_entries.json", "context_meta.json", "context_content"} {
		if _, err := os.Stat(filepath.Join(dir, "workspaces", ws, name)); !os.IsNotExist(err) {
			t.Fatalf("%s must no longer be written (stat err %v)", name, err)
		}
	}
}

// An edit serves a new revision; the previous version stays in the history, and the
// edit event binds the old and new digests (PAR-PROV-01).
func TestEditKeepsPriorRevisionInHistory(t *testing.T) {
	dir := t.TempDir()
	ws := "wsContentHistory"
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})

	entry, err := ProposeContext(dir, ws, ProposeContextRequest{
		Title: "note", Content: "v1 ORIGINAL_TOKEN", Source: "solo", Permission: "readwrite",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateContextContent(dir, ws, entry.ID, "v2 UPDATED_TOKEN", ContextActor{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "v2 UPDATED_TOKEN" || updated.Promoted {
		t.Fatalf("update should return the new, unverified content, got %+v", updated)
	}
	all, err := ListContextEntries(dir, ws, "all")
	if err != nil || len(all) != 1 || all[0].Content != "v2 UPDATED_TOKEN" {
		t.Fatalf("list must serve the new revision: %+v %v", all, err)
	}
	ctx := context.Background()
	err = memoryView(ctx, dir, ws, func(r govstore.Reader) error {
		revs, err := r.ListRevisions(ctx, entry.ID, govstore.RevisionFilter{WithContent: true})
		if err != nil {
			return err
		}
		if len(revs) != 2 || revs[1].Content != "v1 ORIGINAL_TOKEN" || revs[0].Content != "v2 UPDATED_TOKEN" {
			return fmt.Errorf("both versions must stay retrievable, got %+v", revs)
		}
		evs, err := r.ListEvents(ctx, govstore.EventFilter{EntryID: entry.ID, Types: []string{govstore.EventEdit}})
		if err != nil {
			return err
		}
		if len(evs) != 1 || evs[0].OldDigest != contentDigest("v1 ORIGINAL_TOKEN") ||
			evs[0].NewDigest != contentDigest("v2 UPDATED_TOKEN") || evs[0].Principal != adminEditor {
			return fmt.Errorf("edit event must bind principal and both digests, got %+v", evs)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Every governance transition appends an event carrying the principal, the time, the
// workspace HEAD and the content digest.
func TestGovernanceTransitionsAppendEventsWithHead(t *testing.T) {
	dir := t.TempDir()
	ws := "wsEvents"
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)
	const head = "0123456789abcdef0123456789abcdef01234567"
	for name, body := range map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/refs/heads/main": head + "\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	quorumSettings(t, dir)
	entry, err := ProposeContext(dir, ws, ProposeContextRequest{Content: "fact", Source: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"bob", "carol"} {
		if _, err := VerifyContext(dir, ws, entry.ID, agent, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	var events []govstore.Event
	if err := memoryView(ctx, dir, ws, func(r govstore.Reader) error {
		events, err = r.ListEvents(ctx, govstore.EventFilter{EntryID: entry.ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev.Type] = true
		if ev.Principal == "" || ev.At == "" || ev.HeadSHA != head {
			t.Fatalf("event %s lacks principal, time or HEAD: %+v", ev.Type, ev)
		}
	}
	for _, want := range []string{govstore.EventPropose, govstore.EventVote, govstore.EventPromote} {
		if !seen[want] {
			t.Fatalf("missing %s event in %+v", want, events)
		}
	}
}

// The legacy import runs once per file and is idempotent: a context_entries.json that
// reappears (an older build ran, or a backup was restored) imports as unchanged, and
// each import keeps its own backup.
func TestLegacyImportIsIdempotentAndKeepsBackups(t *testing.T) {
	dir := t.TempDir()
	ws := "wsReimport"
	legacy := []byte(`[{"id":"ctx_old","workspace_id":"wsReimport","title":"t","content":"legacy body","source":"alice",
		"permission":"readonly","status":"pending","promoted":false,"required_verifications":2,"verifications":[],
		"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`)
	src := legacyContextEntriesPath(dir, ws)
	for round := 1; round <= 2; round++ {
		if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(src, legacy, 0o644); err != nil {
			t.Fatal(err)
		}
		all, err := ListContextEntries(dir, ws, "all")
		if err != nil || len(all) != 1 || all[0].Content != "legacy body" {
			t.Fatalf("round %d: %+v %v", round, all, err)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Fatalf("round %d: the imported file must be moved aside", round)
		}
	}
	backups, _ := filepath.Glob(src + ".govstore-import*.bak")
	if len(backups) != 2 {
		t.Fatalf("each import keeps a backup, got %v", backups)
	}
}

// The legacy import sizes its file and govstore owns the heavy slot: a small file
// imports inline even while other heavy work holds the slot, and a file of
// legacyImportHeavyFrom or more runs in the slot exactly once (a second acquisition
// around govstore's own would wait on itself and be refused).
func TestLegacyImportSizesItsHeavySlot(t *testing.T) {
	prev := budget.Gov
	budget.Gov = budget.NewProcessGovernor(budget.GovernorConfig{
		HeavyWait:    100 * time.Millisecond,
		FreeOSMemory: func() {},
		Sampler: func() (budget.TreeSample, error) {
			return budget.TreeSample{At: time.Now(), Supported: true, Basis: "test", Processes: 1, RSSBytes: 10 << 20, SelfRSSBytes: 10 << 20}, nil
		},
	})
	t.Cleanup(func() { budget.Gov = prev })
	entry := func(id, body string) string {
		return fmt.Sprintf(`{"id":%q,"workspace_id":"wsSized","title":"t","content":%q,"source":"alice",`+
			`"permission":"readonly","status":"pending","promoted":false,"required_verifications":2,"verifications":[],`+
			`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`, id, body)
	}
	write := func(dir, body string) {
		src := legacyContextEntriesPath(dir, "wsSized")
		if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(src, []byte("["+body+"]"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	small := t.TempDir()
	write(small, entry("ctx_small", "small body"))
	release, err := budget.AcquireHeavy(context.Background(), "test_holder", 0)
	if err != nil {
		t.Fatal(err)
	}
	all, err := ListContextEntries(small, "wsSized", "all")
	release()
	if err != nil || len(all) != 1 {
		t.Fatalf("a small import behind a busy slot must run inline: %+v %v", all, err)
	}

	large := t.TempDir()
	write(large, entry("ctx_large", strings.Repeat("x", legacyImportHeavyFrom)))
	all, err = ListContextEntries(large, "wsSized", "all")
	if err != nil || len(all) != 1 {
		t.Fatalf("a large import must run in the heavy slot once: %+v %v", all, err)
	}
	if st := budget.Gov.Snapshot().HeavySlot; st.Busy {
		t.Fatalf("the heavy slot must be released after the import: %+v", st)
	}
}

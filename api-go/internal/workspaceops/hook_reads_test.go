package workspaceops

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/govstore"
)

// registerRoots writes workspaces.json and a snapshot for each id -> root.
func registerRoots(t *testing.T, dir string, roots map[string]string) {
	t.Helper()
	var recs []workspaceRecord
	for _, id := range slices.Sorted(maps.Keys(roots)) {
		recs = append(recs, workspaceRecord{WorkspaceID: id, RootPath: roots[id]})
		writeSnapshotWithRoot(t, dir, id, roots[id])
	}
	if err := writeJSON(filepath.Join(dir, "workspaces.json"), recs); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceForPathPicksTheDeepestRegisteredRoot(t *testing.T) {
	dir := t.TempDir()
	outer := canonicalRoot(t.TempDir())
	inner := filepath.Join(outer, "services", "api")
	if err := os.MkdirAll(filepath.Join(inner, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	registerRoots(t, dir, map[string]string{"wsOuter": outer, "wsInner": inner})
	link := filepath.Join(t.TempDir(), "api-link")
	if err := os.Symlink(inner, link); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		filepath.Join(inner, "pkg"):  "wsInner",
		inner:                        "wsInner",
		filepath.Join(outer, "docs"): "wsOuter",
		filepath.Join(link, "pkg"):   "wsInner", // resolved through the symlink
		t.TempDir():                  "",
		"relative/path":              "",
		outer + "-sibling":           "",
	}
	for cwd, want := range cases {
		ws, ok, err := WorkspaceForPath(dir, cwd)
		if err != nil || ok != (want != "") || ws.WorkspaceID != want {
			t.Errorf("WorkspaceForPath(%s) = %q %v %v, want %q", cwd, ws.WorkspaceID, ok, err, want)
		}
	}
	if _, ok, err := WorkspaceForPath(t.TempDir(), inner); ok || err != nil {
		t.Fatalf("a data dir without workspaces.json resolved: %v %v", ok, err)
	}
}

func TestRelativeToRoot(t *testing.T) {
	root := canonicalRoot(t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "src-link")
	if err := os.Symlink(filepath.Join(root, "src"), link); err != nil {
		t.Fatal(err)
	}
	ws := ResolvedWorkspace{WorkspaceID: "w", Root: root, Scope: root}
	cases := map[string]string{
		filepath.Join(root, "src", "a.go"): "src/a.go",
		"src/b.go":                         "src/b.go",
		filepath.Join(link, "c.go"):        "src/c.go",
		root:                               "",
		filepath.Join(root, "..", "x.go"):  "",
		"/etc/passwd":                      "",
		"":                                 "",
	}
	for in, want := range cases {
		if got := RelativeToRoot(ws, in); got != want {
			t.Errorf("RelativeToRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

// hookMemoryFixture is a single-agent workspace (memory is promoted on write) with a
// file memory can be anchored to.
func hookMemoryFixture(t *testing.T) (dir, ws, root string) {
	t.Helper()
	dir, ws, root = t.TempDir(), "wsHook", canonicalRoot(t.TempDir())
	registerRoots(t, dir, map[string]string{ws: root})
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "billing.go"), []byte("package billing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, ws, root
}

func hookPropose(t *testing.T, dir, ws string, req ProposeContextRequest) *ContextEntry {
	t.Helper()
	req.Source = "agent-a"
	e, err := ProposeContext(dir, ws, req)
	if err != nil || !e.Promoted {
		t.Fatalf("propose: %v %+v", err, e)
	}
	return e
}

func TestHookMemoriesRankByPathAndFlagStaleness(t *testing.T) {
	dir, ws, root := hookMemoryFixture(t)
	bound := hookPropose(t, dir, ws, ProposeContextRequest{Title: "billing rounding", Content: "invoices round half-even", Paths: []string{"src/billing.go"}})
	hookPropose(t, dir, ws, ProposeContextRequest{Title: "unrelated", Content: "the docs site is built with hugo"})
	if bound.PathHashes["src/billing.go"] == pathMissingSentinel {
		t.Fatal("a two-component anchor was baselined as missing")
	}
	ctx := context.Background()
	if got, err := HookMemories(ctx, dir, ws, "p", "", nil, 5); err != nil || got != nil {
		t.Fatalf("no query and no path must read nothing: %v %v", got, err)
	}
	got, err := HookMemories(ctx, dir, ws, "p", "", []string{"src/billing.go"}, 5)
	if err != nil || len(got) != 1 || got[0].ID != bound.ID || got[0].Stale {
		t.Fatalf("path-bound memories %+v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "billing.go"), []byte("package billing // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ = HookMemories(ctx, dir, ws, "p", "", []string{"src/billing.go"}, 5)
	if len(got) != 1 || !got[0].Stale || !reflect.DeepEqual(got[0].StalePaths, []string{"src/billing.go"}) {
		t.Fatalf("a changed anchor is not flagged stale: %+v", got)
	}
	got, _ = HookMemories(ctx, dir, ws, "p", "hugo docs", nil, 5)
	if len(got) != 1 || got[0].Title != "unrelated" {
		t.Fatalf("pattern memories %+v", got)
	}
}

func TestReadHookMemoryIndex(t *testing.T) {
	dir, ws, _ := hookMemoryFixture(t)
	deploy := hookPropose(t, dir, ws, ProposeContextRequest{Title: "deploys", Content: "deploys go through the canary", Tags: []string{"Trigger-Deploy", "ops"}, Paths: []string{"deploy/"}})
	core := hookPropose(t, dir, ws, ProposeContextRequest{Title: "style", Content: "errors are wrapped with %w", Paths: []string{"src/billing.go"}})
	err := memoryUpdate(context.Background(), dir, ws, func(tx govstore.Tx) error {
		_, err := tx.SetTier(context.Background(), core.ID, govstore.TierCore, govstore.Actor{Principal: "admin"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := ReadHookMemoryIndex(context.Background(), dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(idx.Core, []string{core.ID}) || !reflect.DeepEqual(idx.Triggers, map[string][]string{"deploy": {deploy.ID}}) {
		t.Fatalf("index %+v", idx)
	}
	if len(idx.Anchors) != 2 || !slices.Contains(idx.Anchors, "deploy") || !slices.Contains(idx.Anchors, "src/billing.go") {
		t.Fatalf("anchors %v", idx.Anchors)
	}
}

func TestHookGroundingSpawnsNothingAndListsWhatItSkipped(t *testing.T) {
	dir, ws, _ := hookMemoryFixture(t)
	hookPropose(t, dir, ws, ProposeContextRequest{Title: "t", Content: "c"})
	t.Setenv("XMUSTARD_CORE_BIN", "/nonexistent/xmustard-core")
	before := budget.Counters().SpawnsTotal
	g := HookGrounding(context.Background(), dir, ws, "p")
	if budget.Counters().SpawnsTotal != before {
		t.Fatal("hook grounding started a process")
	}
	for _, f := range hookUnsampled {
		if !g.isUnknown(f) {
			t.Fatalf("%s not listed unknown: %+v", f, g.Unknown)
		}
	}
	if g.StaleMemory == nil || g.BlockedByFailingVerification == nil || *g.BlockedByFailingVerification {
		t.Fatalf("memory or run section missing: %+v", g)
	}
}

// A client-reported change drops the cached identity; without a watcher on the root
// nothing takes the paths, and nothing is started to find one.
func TestNoteChangedPathsDropsTheCachedIdentity(t *testing.T) {
	root := canonicalRoot(t.TempDir())
	t.Setenv("XMUSTARD_CORE_WORKER", "1")
	epochOf := func() uint64 {
		identityCache.mu.Lock()
		defer identityCache.mu.Unlock()
		return identityCache.state(root).epoch
	}
	before, spawns := epochOf(), budget.Counters().SpawnsTotal
	if NoteChangedPaths(context.Background(), root, []string{"b.go", "a.go"}) {
		t.Fatal("no watcher watches the root, yet the paths were taken")
	}
	if epochOf() == before {
		t.Fatal("noting a change kept the cached identity")
	}
	if budget.Counters().SpawnsTotal != spawns {
		t.Fatal("noting a change started a process")
	}
	if NoteChangedPaths(context.Background(), root, nil) || NoteChangedPaths(context.Background(), "", []string{"a"}) {
		t.Fatal("nothing to note")
	}
	ForgetRoot(root) // never fails, watched or not
}

// A hook's search needs the index this process brought up and a running worker; it
// never starts a process to get either.
func TestHookSearchRunsOnlyOnAResidentIndex(t *testing.T) {
	root := canonicalRoot(t.TempDir())
	marker := filepath.Join(t.TempDir(), "ran")
	core := filepath.Join(t.TempDir(), "xmustard-core")
	if err := os.WriteFile(core, []byte("#!/bin/sh\ntouch "+marker+"\necho '{\"hits\":[]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	t.Setenv("XMUSTARD_CORE_WORKER", "1")
	ws := ResolvedWorkspace{WorkspaceID: "w", Root: root, Scope: root}
	before := budget.Counters().SpawnsTotal
	if _, err := HookSearch(context.Background(), ws, "handler", 5); !errors.Is(err, ErrIndexNotResident) {
		t.Fatalf("no index: %v", err)
	}
	codeIndex.Lock()
	codeIndexState(root).indexed = "key-1"
	codeIndex.Unlock()
	t.Cleanup(func() {
		codeIndex.Lock()
		delete(codeIndex.roots, root)
		codeIndex.Unlock()
	})
	if _, err := HookSearch(context.Background(), ws, "handler", 5); !errors.Is(err, ErrIndexNotResident) {
		t.Fatalf("no running worker: %v", err)
	}
	if _, err := HookSyntaxCheck(context.Background(), ws, []string{"a.go"}); !errors.Is(err, ErrIndexNotResident) {
		t.Fatalf("syntax check without a running worker: %v", err)
	}
	if _, err := os.Stat(marker); err == nil || budget.Counters().SpawnsTotal != before {
		t.Fatal("a hook read started a process")
	}
}

// The confined open walks each component with openat and closes the parent as it
// goes, so the kernel hands the root's descriptor number to a later component. A path
// of even depth ended on that number and was refused as "the root itself"; memory
// anchored to such a path was baselined as missing and never flagged stale.
func TestConfinedReadOfEveryDepth(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a.go", "src/a.go", "src/pkg/a.go", "src/pkg/sub/a.go"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
		if b, ok := readWorkspaceRegularFile(root, rel); !ok || string(b) != rel {
			t.Fatalf("confined read of %s: %q %v", rel, b, ok)
		}
	}
	if _, err := openWorkspaceFileBeneath(root, "."); err == nil {
		t.Fatal("the root itself opened as a file")
	}
}

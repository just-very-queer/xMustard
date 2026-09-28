package workspaceops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// agedWrite writes a registry source file with a past mtime and runs the registry
// clock ahead of the filesystem, so the registry may trust its stat key (the ctime,
// which cannot be set back, is outside the racy window too).
func agedWrite(t *testing.T, path string, v any, age time.Duration) {
	t.Helper()
	if err := writeJSON(path, v); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	agedRegistryClock(t)
}

func agedRegistryClock(t *testing.T) {
	prev := registryNow
	registryNow = func() time.Time { return time.Now().Add(3 * time.Second) }
	t.Cleanup(func() { registryNow = prev })
}

func snapshotWithRoot(ws, root string) map[string]any {
	return map[string]any{"scanner_version": scannerVersion,
		"workspace": map[string]any{"workspace_id": ws, "name": ws, "root_path": root},
		"issues":    []map[string]any{{"bug_id": "B1", "title": "t"}}}
}

func TestRegistryResolvesFromTheSnapshotHeaderOnce(t *testing.T) {
	dir, ws, root := t.TempDir(), "wsReg", t.TempDir()
	agedWrite(t, snapshotPath(dir, ws), snapshotWithRoot(ws, root), time.Hour)
	loads, headers := snapshotLoads.Load(), snapshotHeaderReads.Load()
	for i := 0; i < 3; i++ {
		got, _, err := resolveChangeRoot(dir, ws)
		if err != nil || got != root {
			t.Fatalf("resolve = %q, %v; want %q", got, err, root)
		}
	}
	if n := snapshotLoads.Load() - loads; n != 0 {
		t.Fatalf("registry parsed the full snapshot %d time(s)", n)
	}
	if n := snapshotHeaderReads.Load() - headers; n != 1 {
		t.Fatalf("header read %d times for 3 lookups of an unchanged snapshot; want 1", n)
	}
}

func TestRegistryInvalidatesOnSnapshotChange(t *testing.T) {
	dir, ws := t.TempDir(), "wsRegSnap"
	rootA, rootB := t.TempDir(), t.TempDir()
	agedWrite(t, snapshotPath(dir, ws), snapshotWithRoot(ws, rootA), time.Hour)
	if got, _, _ := resolveChangeRoot(dir, ws); got != rootA {
		t.Fatalf("got %q want %q", got, rootA)
	}
	agedWrite(t, snapshotPath(dir, ws), snapshotWithRoot(ws, rootB), 2*time.Hour)
	if got, _, _ := resolveChangeRoot(dir, ws); got != rootB {
		t.Fatalf("snapshot change not observed: got %q want %q", got, rootB)
	}
}

func TestRegistryInvalidatesOnWorkspaceRecordChange(t *testing.T) {
	dir, ws := t.TempDir(), "wsRegRec"
	agedWrite(t, snapshotPath(dir, ws), snapshotWithRoot(ws, "/snap/root"), time.Hour)
	records := filepath.Join(dir, "workspaces.json")
	agedWrite(t, records, []map[string]any{{"workspace_id": ws, "name": "first", "root_path": "/rec/one"}}, time.Hour)
	rec, err := getWorkspaceRecord(dir, ws)
	if err != nil || rec.Name != "first" {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	agedWrite(t, records, []map[string]any{{"workspace_id": ws, "name": "renamed", "root_path": "/rec/two"}}, 2*time.Hour)
	if rec, err = getWorkspaceRecord(dir, ws); err != nil || rec.Name != "renamed" || rec.RootPath != "/rec/two" {
		t.Fatalf("workspace-record change not observed: %+v, %v", rec, err)
	}
	// without a workspaces.json entry the snapshot's own record answers
	agedWrite(t, records, []map[string]any{}, 3*time.Hour)
	if rec, err = getWorkspaceRecord(dir, ws); err != nil || rec.RootPath != "/snap/root" {
		t.Fatalf("snapshot record fallback: %+v, %v", rec, err)
	}
	if _, err := getWorkspaceRecord(dir, "wsMissing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing workspace must be ErrNotExist, got %v", err)
	}
}

// A file rewritten inside one timestamp tick keeps its mtime; the registry must not
// trust a stat key that young (Git's racy-index rule).
func TestRegistryRereadsARacySnapshot(t *testing.T) {
	dir, ws := t.TempDir(), "wsRegRacy"
	path := snapshotPath(dir, ws)
	at := time.Now()
	write := func(root string) {
		if err := writeJSON(path, snapshotWithRoot(ws, root)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write("/r/aaaa")
	if got, _, _ := resolveChangeRoot(dir, ws); got != "/r/aaaa" {
		t.Fatalf("got %q", got)
	}
	write("/r/bbbb") // same size, same mtime
	if got, _, _ := resolveChangeRoot(dir, ws); got != "/r/bbbb" {
		t.Fatalf("racy rewrite served stale: %q", got)
	}
}

// A snapshot replaced by one of the same size with the mtime preserved (cp -p,
// rsync -a, a restore) still has a new ctime and inode: the registry must not keep
// serving the old root.
func TestRegistryInvalidatesOnSameSizeSameMtimeReplacement(t *testing.T) {
	agedRegistryClock(t)
	dir, ws := t.TempDir(), "wsRegSwap"
	path := snapshotPath(dir, ws)
	old := time.Now().Add(-time.Hour)
	write := func(p, root string) {
		if err := writeJSON(p, snapshotWithRoot(ws, root)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	write(path, "/repo/aaaa")
	if got, _, _ := resolveChangeRoot(dir, ws); got != "/repo/aaaa" {
		t.Fatalf("got %q", got)
	}
	before, _ := statMark(path)
	write(path+".new", "/repo/bbbb")
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	after, _ := statMark(path)
	if after.size != before.size || after.mtimeNs != before.mtimeNs {
		t.Fatalf("fixture must keep size and mtime: %+v %+v", before, after)
	}
	if got, _, _ := resolveChangeRoot(dir, ws); got != "/repo/bbbb" {
		t.Fatalf("same-size, same-mtime replacement served stale: %q", got)
	}
}

// The canonical scope follows a re-pointed symlinked root on the next lookup, while
// the registry keeps its cached record (no snapshot re-read).
func TestRegistryScopeFollowsARepointedSymlink(t *testing.T) {
	canon := func(p string) string {
		c, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := canon(t.TempDir()), canon(t.TempDir())
	link := filepath.Join(t.TempDir(), "current")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	dir, ws := t.TempDir(), "wsRegLink"
	agedWrite(t, snapshotPath(dir, ws), snapshotWithRoot(ws, link), time.Hour)
	if got := WorkspaceRepoScope(dir, ws); got != a {
		t.Fatalf("scope = %q want %q", got, a)
	}
	headers := snapshotHeaderReads.Load()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	if got := WorkspaceRepoScope(dir, ws); got != b {
		t.Fatalf("scope stayed on the old target: %q want %q", got, b)
	}
	if n := snapshotHeaderReads.Load() - headers; n != 0 {
		t.Fatalf("re-resolving the scope re-read the snapshot %d time(s)", n)
	}
}

func TestRegistryErrorsLikeTheSnapshotParse(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := resolveChangeRoot(dir, "wsNone"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing snapshot must wrap ErrNotExist, got %v", err)
	}
	for _, bad := range []string{"..", ".", "a/b", `a\b`, ""} {
		if _, _, err := resolveChangeRoot(dir, bad); err == nil {
			t.Fatalf("workspace id %q must be refused", bad)
		}
	}
	// a snapshot whose workspace record is not near the start is refused, never parsed in full
	ws := "wsFar"
	big := make([]string, 200_000)
	for i := range big {
		big[i] = "padding-padding"
	}
	agedWrite(t, snapshotPath(dir, ws), map[string]any{"aaa_issues": big, "workspace": map[string]any{"root_path": "/x"}}, time.Hour)
	loads := snapshotLoads.Load()
	if _, _, err := resolveChangeRoot(dir, ws); err == nil {
		t.Fatal("a workspace record beyond the header bound must be an error")
	}
	if snapshotLoads.Load() != loads {
		t.Fatal("the registry fell back to a full snapshot parse")
	}
	// a complete snapshot with no workspace record exists, as the full parse accepted
	// it (a zero Workspace); the root then comes from workspaces.json
	bare := "wsBare"
	agedWrite(t, snapshotPath(dir, bare), map[string]any{"workspace_id": bare}, time.Hour)
	if err := requireWorkspaceSnapshot(dir, bare); err != nil {
		t.Fatalf("a snapshot without a workspace record must still exist: %v", err)
	}
	agedWrite(t, filepath.Join(dir, "workspaces.json"), []map[string]any{{"workspace_id": bare, "root_path": "/from/registry"}}, time.Hour)
	if root, _, err := resolveChangeRoot(dir, bare); err != nil || root != "/from/registry" {
		t.Fatalf("root of a bare snapshot = %q, %v; want the workspaces.json record", root, err)
	}
}

// PAR-RT-06 acceptance: the nine tool paths never parse snapshot.json.
func TestNineToolPathsNeverParseTheSnapshot(t *testing.T) {
	dataDir, ws := seedGroundBenchWorkspace(t)
	root, _, err := resolveChangeRoot(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", ws, "runs", "run_fail.json"), map[string]any{
		"run_id": "run_fail", "workspace_id": ws, "status": "failed", "output_path": filepath.Join(root, "f.go"),
		"created_at": "2026-09-25T00:01:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before := snapshotLoads.Load()
	calls := map[string]func() error{
		"ground": func() error { _, err := BuildSessionGroundingCtx(ctx, dataDir, ws); return err },
		"recall": func() error { _, err := RecallContextCtx(ctx, dataDir, ws, "c", []string{"f.go"}, 5); return err },
		"remember+verify": func() error {
			e, err := ProposeContext(dataDir, ws, ProposeContextRequest{Content: "fact", Source: "a1", Paths: []string{"f.go"}})
			if err != nil {
				return err
			}
			_, err = VerifyContextAs(dataDir, ws, e.ID, ContextActor{ID: "a2"}, true, "ok")
			return err
		},
		"search":         func() error { _, err := WorkspaceSearchWithFeedbackCtx(ctx, dataDir, ws, "F", "", 5); return err },
		"search pattern": func() error { _, err := SearchSemanticPatternCtx(ctx, dataDir, ws, "$A", "go", "", 5); return err },
		"explain": func() error {
			if _, err := ExplainPathCtx(ctx, dataDir, ws, "f.go"); err != nil && !IsInvalidInput(err) {
				return nil // the fake core's explain output is not a real explanation
			}
			_, _ = PathGraphCtx(ctx, dataDir, ws, "f.go")
			return nil
		},
		"impact": func() error {
			if _, err := WorkspaceChangesSinceIndexCtx(ctx, dataDir, ws); err != nil {
				return err
			}
			if _, err := SymbolImpactCtx(ctx, dataDir, ws, "F", 2); err != nil {
				return err
			}
			if _, err := FileImpactCtx(ctx, dataDir, ws, "f.go", 2); err != nil {
				return err
			}
			_, err := TraceSymbolsCtx(ctx, dataDir, ws, "F", "G")
			return err
		},
		"diagnostics": func() error {
			if _, err := ReadDiagnosticsCtx(ctx, dataDir, ws, ""); err != nil && !errors.Is(err, ErrInvalidDiagnosticsRequest) {
				return err // no Postgres DSN: the read stops after the workspace lookup
			}
			return nil
		},
		"why_failed": func() error { _, err := ExplainRunFailureCtx(ctx, dataDir, ws, "run_fail"); return err },
		"evidence scope": func() error {
			if WorkspaceRepoScope(dataDir, ws) == "" {
				return errors.New("no scope")
			}
			return nil
		},
	}
	for name, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n := snapshotLoads.Load() - before; n != 0 {
			t.Fatalf("%s parsed snapshot.json %d time(s)", name, n)
		}
	}
}

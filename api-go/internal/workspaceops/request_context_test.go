//go:build unix

package workspaceops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// initGitRepo creates a committed repository with files in nested directories.
func initGitRepo(t *testing.T, extraInit ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	if canon, err := filepath.EvalSymlinks(root); err == nil {
		root = canon
	}
	runGit(t, root, append([]string{"init", "-q"}, extraInit...)...)
	runGit(t, root, "config", "user.email", "t@example.com")
	runGit(t, root, "config", "user.name", "t")
	for _, p := range []string{"a/x.go", "a/b/y.go", "c/z.go", "top.go"} {
		writeFile(t, filepath.Join(root, p), "package p // "+p+"\n")
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "init")
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// resetIdentityCache starts a test from an empty identity cache.
func resetIdentityCache(t *testing.T) {
	identityCache.mu.Lock()
	identityCache.roots = map[string]*rootState{}
	identityCache.mu.Unlock()
	t.Cleanup(func() {
		identityCache.mu.Lock()
		identityCache.roots = map[string]*rootState{}
		identityCache.mu.Unlock()
	})
}

// agedIdentityClock runs the identity clock ahead of the filesystem, so files the
// test just wrote are past the racy window (tests of the racy rule use
// frozenIdentityClock instead).
func agedIdentityClock(t *testing.T) {
	prev := identityNow
	identityNow = func() time.Time { return time.Now().Add(3 * time.Second) }
	t.Cleanup(func() { identityNow = prev })
}

// frozenIdentityClock stops the identity clock just past the racy window after the
// current moment: files written before this call are settled, files written after
// it (the caller sleeps a little first, for coarse timestamps) are racy.
func frozenIdentityClock(t *testing.T) time.Time {
	prev := identityNow
	at := time.Now().Add(fingerprintRacyWindow + time.Millisecond)
	identityNow = func() time.Time { return at }
	t.Cleanup(func() { identityNow = prev })
	time.Sleep(30 * time.Millisecond)
	return at
}

// fakeSampler replaces the repo-key sampler: it counts runs and reports key, which
// the test sets, as a complete (or incomplete) identity, with ignored as repo-key's
// ignored-directory listing (listing=false: no listing, as an older core answers).
type fakeSampler struct {
	runs     atomic.Int64
	mu       sync.Mutex
	key      string
	complete bool
	listing  bool
	ignored  []string
	during   func() // runs inside a sample (a concurrent edit)
	block    chan struct{}
}

func installFakeSampler(t *testing.T) *fakeSampler {
	t.Helper()
	f := &fakeSampler{key: "k1", complete: true, listing: true}
	prev, prevJudge := sampleRepoIdentity, fingerprintCostJudged
	sampleRepoIdentity = func(ctx context.Context, root string) identitySample {
		f.runs.Add(1)
		if f.block != nil {
			<-f.block
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.during != nil {
			f.during()
		}
		s := identitySample{id: RepoIdentity{Key: f.key, Complete: f.complete, Source: "repo-key"}}
		if f.listing {
			s.ign = newIgnoreSet(f.ignored)
		}
		return s
	}
	fingerprintCostJudged = false // the fake sampler is instant
	resetIdentityCache(t)
	agedIdentityClock(t)
	t.Cleanup(func() { sampleRepoIdentity, fingerprintCostJudged = prev, prevJudge })
	return f
}

func (f *fakeSampler) set(key string) {
	f.mu.Lock()
	f.key = key
	f.mu.Unlock()
}

func TestRequestContextSamplesIdentityOncePerRequest(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	dir, ws := t.TempDir(), "wsRC"
	writeSnapshotWithRoot(t, dir, ws, root)
	rc := NewRequestContext(dir, ws)
	ctx := WithRequestContext(context.Background(), rc)
	if RequestContextFrom(ctx) != rc {
		t.Fatal("request context not carried by ctx")
	}
	for i := 0; i < 3; i++ {
		id, _ := rc.Identity(ctx)
		if id.Key != "k1" {
			t.Fatalf("identity = %+v", id)
		}
	}
	if n := f.runs.Load(); n != 1 {
		t.Fatalf("one request sampled identity %d times; want 1", n)
	}
	// handlers reuse the request's resolved root
	got, _, err := resolveChangeRootCtx(ctx, dir, ws)
	if err != nil || got != root || rc.Scope() != root {
		t.Fatalf("root = %q scope = %q err = %v; want %q", got, rc.Scope(), err, root)
	}
	// a request that never asks for identity (remember/verify) samples nothing
	_ = NewRequestContext(dir, ws)
	if n := f.runs.Load(); n != 1 {
		t.Fatalf("an identity-free request sampled %d times", n-1)
	}
}

// The after-execution check costs a walk and no repo-key run when the tree held
// still, pairs the before-sample with that walk for later readers, and samples
// again when the tree moved during the handler.
func TestIdentityAfterExecutionSamplesOnlyWhenTheTreeMoved(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	dir, ws := t.TempDir(), "wsAfter"
	writeSnapshotWithRoot(t, dir, ws, root)
	ctx := context.Background()

	rc := NewRequestContext(dir, ws)
	rc.Identity(ctx)
	walks := fingerprintWalks.Load()
	id, obs := rc.IdentityAfter(ctx)
	if id.Key != "k1" || !obs.Cached || f.runs.Load() != 1 || fingerprintWalks.Load()-walks != 1 {
		t.Fatalf("quiet handler: %+v %+v runs=%d walks=%d", id, obs, f.runs.Load(), fingerprintWalks.Load()-walks)
	}
	// the pairing serves the next reader (an evidence page) without a run
	if _, obs := CurrentRepoIdentity(ctx, root); !obs.Cached || f.runs.Load() != 1 {
		t.Fatalf("after-check pairing not reused: %+v runs=%d", obs, f.runs.Load())
	}

	// a request whose before-identity came from the cache, and whose handler edits
	rc = NewRequestContext(dir, ws)
	if _, obs := rc.Identity(ctx); !obs.Cached {
		t.Fatalf("before-identity not cached: %+v", obs)
	}
	writeFile(t, filepath.Join(root, "a", "b", "y.go"), "package p // edited during the handler\n")
	f.set("k2")
	if id, obs := rc.IdentityAfter(ctx); id.Key != "k2" || obs.Cached || f.runs.Load() != 2 {
		t.Fatalf("moved tree must re-sample: %+v %+v runs=%d", id, obs, f.runs.Load())
	}
}

// The after-check does not depend on the TTL: a handler that outlives it still
// binds without a second run when nothing moved (PAR-FRESH-02).
func TestIdentityAfterExecutionIgnoresTheTTL(t *testing.T) {
	f := installFakeSampler(t)
	t.Setenv("XMUSTARD_IDENTITY_CACHE_MS", "300")
	root := initGitRepo(t)
	dir, ws := t.TempDir(), "wsSlow"
	writeSnapshotWithRoot(t, dir, ws, root)
	now := time.Now().Add(3 * time.Second)
	identityNow = func() time.Time { return now }
	rc := NewRequestContext(dir, ws)
	rc.Identity(context.Background())
	now = now.Add(2 * time.Second) // a slow handler: far past the 300 ms TTL
	if id, obs := rc.IdentityAfter(context.Background()); id.Key != "k1" || !obs.Cached || f.runs.Load() != 1 {
		t.Fatalf("slow handler: %+v %+v runs=%d", id, obs, f.runs.Load())
	}
}

// A file changed within the racy window before the sample cannot be vouched for by
// its stat key: the after-check samples again, and nothing is cached.
func TestIdentityAfterExecutionHonorsTheRacyWindow(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	dir, ws := t.TempDir(), "wsRacy"
	writeSnapshotWithRoot(t, dir, ws, root)
	frozenIdentityClock(t)
	writeFile(t, filepath.Join(root, "top.go"), "package p // just edited\n")
	rc := NewRequestContext(dir, ws)
	rc.Identity(context.Background())
	if _, obs := rc.IdentityAfter(context.Background()); obs.Cached || f.runs.Load() != 2 {
		t.Fatalf("a racy tree must be re-sampled: %+v runs=%d", obs, f.runs.Load())
	}
	if _, obs := CurrentRepoIdentity(context.Background(), root); obs.Cached {
		t.Fatal("a racy pairing must not be cached")
	}
}

// A file deleted during the handler leaves no stat key of its own, so on the cold
// path (a fresh before-sample, no settled walk to compare against) only its
// directory's stat key, which the deletion moves, shows the tree is not quiet. The
// after-check must then sample again rather than bind across the deletion, and the
// pre-deletion identity must not be cached. The clock is frozen: files written before
// the request are settled, the deletion is not (an aged clock would hide it).
func TestIdentityAfterExecutionSeesADeletionOnTheColdPath(t *testing.T) {
	for _, rel := range []string{"c/z.go", "u/v/one.txt"} { // tracked, untracked
		t.Run(rel, func(t *testing.T) {
			f := installFakeSampler(t)
			root := initGitRepo(t)
			writeFile(t, filepath.Join(root, "u", "v", "one.txt"), "one")
			writeFile(t, filepath.Join(root, "u", "v", "two.txt"), "two")
			dir, ws := t.TempDir(), "wsDeleted"
			writeSnapshotWithRoot(t, dir, ws, root)
			frozenIdentityClock(t)
			ctx := context.Background()
			rc := NewRequestContext(dir, ws)
			if _, obs := rc.Identity(ctx); obs.Cached {
				t.Fatal("cold cache: the before-identity must be sampled")
			}
			if err := os.Remove(filepath.Join(root, rel)); err != nil {
				t.Fatal(err)
			}
			f.set("k2")
			if id, obs := rc.IdentityAfter(ctx); id.Key != "k2" || obs.Cached || f.runs.Load() != 2 {
				t.Fatalf("a deletion during the handler must re-sample: %+v %+v runs=%d", id, obs, f.runs.Load())
			}
			if id, _ := CurrentRepoIdentity(ctx, root); id.Key != "k2" {
				t.Fatalf("the pre-deletion identity was cached: %+v", id)
			}
		})
	}
}

// A file present while repo-key runs and removed before the walk that would be paired
// with that sample: the pairing is refused (the directory's stat key is racy), so the
// next read samples instead of serving the pre-deletion key.
func TestIdentityCacheRefusesAPairingAcrossADeletion(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	frozenIdentityClock(t)
	gone := filepath.Join(root, "a", "b", "y.go")
	f.during = func() { _ = os.Remove(gone) } // after repo-key read it, before the walk
	ctx := context.Background()
	if id, _ := CurrentRepoIdentity(ctx, root); id.Key != "k1" {
		t.Fatalf("first read: %+v", id)
	}
	f.set("k2")
	if id, obs := CurrentRepoIdentity(ctx, root); id.Key != "k2" || obs.Cached || f.runs.Load() != 2 {
		t.Fatalf("a pairing across a deletion served %+v %+v (runs=%d); want a fresh k2", id, obs, f.runs.Load())
	}
}

// A branch moved without touching the index or the working tree (`reset --soft`,
// `update-ref`) rewrites only the loose ref. On the cold path (a fresh before-sample,
// no settled walk to compare with) only the ref's own stat key shows the tree is not
// quiet: the after-check must sample again rather than bind across the move, and the
// pre-move identity must not be cached. The clock is frozen, as for a deletion.
func TestIdentityAfterExecutionSeesARefMoveOnTheColdPath(t *testing.T) {
	for _, move := range [][]string{
		{"reset", "-q", "--soft", "HEAD~1"},
		{"update-ref", "HEAD", "HEAD~1"},
	} {
		t.Run(move[0], func(t *testing.T) {
			f := installFakeSampler(t)
			root := initGitRepo(t)
			writeFile(t, filepath.Join(root, "second.go"), "package p\n")
			runGit(t, root, "add", "second.go")
			runGit(t, root, "commit", "-q", "-m", "second")
			dir, ws := t.TempDir(), "wsRefMoved"
			writeSnapshotWithRoot(t, dir, ws, root)
			frozenIdentityClock(t)
			ctx := context.Background()
			rc := NewRequestContext(dir, ws)
			if _, obs := rc.Identity(ctx); obs.Cached {
				t.Fatal("cold cache: the before-identity must be sampled")
			}
			runGit(t, root, move...)
			f.set("k2")
			if id, obs := rc.IdentityAfter(ctx); id.Key != "k2" || obs.Cached || f.runs.Load() != 2 {
				t.Fatalf("a ref move during the handler must re-sample: %+v %+v runs=%d", id, obs, f.runs.Load())
			}
			if id, _ := CurrentRepoIdentity(ctx, root); id.Key != "k2" {
				t.Fatalf("the pre-move identity was cached: %+v", id)
			}
		})
	}
}

// A removal leaves no stat key of its own. Among the git files the fingerprint reads
// that holds for a loose ref (`update-ref -d`, the usual way to undo a root commit),
// the index and the files in info/. On the cold path the stat key of the directory
// that held one stands in (it moved when the file went), so the after-check samples
// again rather than bind across the removal, and the pre-removal identity is not
// cached. The clock is frozen, as for a deletion in the working tree.
func TestIdentityAfterExecutionSeesAGitFileRemovalOnTheColdPath(t *testing.T) {
	for _, c := range []struct {
		name   string
		setup  func(t *testing.T, root string) (tree string)
		remove func(t *testing.T, tree string)
	}{
		{"the checked-out branch",
			func(t *testing.T, root string) string { return root },
			func(t *testing.T, tree string) { runGit(t, tree, "update-ref", "-d", "HEAD") }},
		{"a branch in a nested ref directory",
			func(t *testing.T, root string) string {
				runGit(t, root, "checkout", "-q", "-b", "feature/x")
				return root
			},
			func(t *testing.T, tree string) { runGit(t, tree, "update-ref", "-d", "refs/heads/feature/x") }},
		{"a linked worktree's branch in the common dir",
			func(t *testing.T, root string) string {
				wt := filepath.Join(t.TempDir(), "wt")
				runGit(t, root, "worktree", "add", "-q", "-b", "side", wt)
				if canon, err := filepath.EvalSymlinks(wt); err == nil {
					wt = canon
				}
				return wt
			},
			func(t *testing.T, tree string) { runGit(t, tree, "update-ref", "-d", "HEAD") }},
		{"info/exclude",
			func(t *testing.T, root string) string {
				writeFile(t, filepath.Join(root, ".git", "info", "exclude"), "scratch.txt\n")
				writeFile(t, filepath.Join(root, "scratch.txt"), "excluded until info/exclude goes\n")
				return root
			},
			func(t *testing.T, tree string) { mustRemove(t, filepath.Join(tree, ".git", "info", "exclude")) }},
		{"the index",
			func(t *testing.T, root string) string { return root },
			func(t *testing.T, tree string) { mustRemove(t, filepath.Join(tree, ".git", "index")) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := installFakeSampler(t)
			tree := c.setup(t, initGitRepo(t))
			dir, ws := t.TempDir(), "wsGitFileRemoved"
			writeSnapshotWithRoot(t, dir, ws, tree)
			frozenIdentityClock(t)
			ctx := context.Background()
			rc := NewRequestContext(dir, ws)
			if _, obs := rc.Identity(ctx); obs.Cached {
				t.Fatal("cold cache: the before-identity must be sampled")
			}
			c.remove(t, tree)
			f.set("k2")
			if id, obs := rc.IdentityAfter(ctx); id.Key != "k2" || obs.Cached || f.runs.Load() != 2 {
				t.Fatalf("a removal during the handler must re-sample: %+v %+v runs=%d", id, obs, f.runs.Load())
			}
			if id, _ := CurrentRepoIdentity(ctx, tree); id.Key != "k2" {
				t.Fatalf("the pre-removal identity was cached: %+v", id)
			}
		})
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// A branch moved after repo-key read it and before the walk that would be paired with
// that sample: the pairing is refused (the ref's stat key is racy), so the next read
// samples instead of serving the pre-move key.
func TestIdentityCacheRefusesAPairingAcrossARefMove(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	moved := strings.TrimSpace(gitOutput(t, root, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "moved"))
	frozenIdentityClock(t)
	var once sync.Once
	f.during = func() { once.Do(func() { runGit(t, root, "update-ref", "HEAD", moved) }) }
	ctx := context.Background()
	if id, _ := CurrentRepoIdentity(ctx, root); id.Key != "k1" {
		t.Fatalf("first read: %+v", id)
	}
	f.set("k2")
	if id, obs := CurrentRepoIdentity(ctx, root); id.Key != "k2" || obs.Cached || f.runs.Load() != 2 {
		t.Fatalf("a pairing across a ref move served %+v %+v (runs=%d); want a fresh k2", id, obs, f.runs.Load())
	}
}

// A workspace root re-pointed (symlink) during a request has no after-identity, so
// evidence produced across the move cannot bind; the next request follows it.
func TestIdentityAfterExecutionRefusesAMovedRoot(t *testing.T) {
	installFakeSampler(t)
	a, b := initGitRepo(t), initGitRepo(t)
	link := filepath.Join(t.TempDir(), "current")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	dir, ws := t.TempDir(), "wsMoved"
	writeSnapshotWithRoot(t, dir, ws, link)
	rc := NewRequestContext(dir, ws)
	if rc.Identity(context.Background()); rc.Scope() != a {
		t.Fatalf("scope = %q want %q", rc.Scope(), a)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	id, _ := rc.IdentityAfter(context.Background())
	if id.Complete || len(id.Limitations) == 0 || id.Limitations[0].Reason != "workspace_root_moved" {
		t.Fatalf("moved root: %+v", id)
	}
	if got := NewRequestContext(dir, ws).Scope(); got != b {
		t.Fatalf("next request scope = %q want %q", got, b)
	}
}

func TestIdentityCacheReusesUntilTheWorkingStateChanges(t *testing.T) {
	f := installFakeSampler(t)
	f.ignored = []string{"build/", "a/node_modules/"}
	root := initGitRepo(t)
	writeFile(t, filepath.Join(root, "build", "out", "o.bin"), "obj")
	writeFile(t, filepath.Join(root, "a", "node_modules", "m", "index.js"), "x")
	writeFile(t, filepath.Join(root, "u", "v", "one.txt"), "one")
	ctx := context.Background()
	runs := int64(0)
	expect := func(step string, sampled bool) {
		t.Helper()
		if sampled {
			runs++
		}
		_, obs := CurrentRepoIdentity(ctx, root)
		if n := f.runs.Load(); n != runs || obs.Cached == sampled {
			t.Fatalf("%s: runs=%d cached=%v; want runs=%d cached=%v", step, n, obs.Cached, runs, !sampled)
		}
	}
	mkdir := func(rel string) {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	expect("first read samples", true)
	expect("unchanged state reuses", false)
	writeFile(t, filepath.Join(root, "a", "b", "y.go"), "package p // edited, longer\n")
	expect("edit to a tracked file", true)
	expect("then reuses", false)
	writeFile(t, filepath.Join(root, "a", "new_untracked.go"), "x")
	expect("new untracked file beside tracked files", true)
	writeFile(t, filepath.Join(root, "a", "new_untracked.go"), "xyz")
	expect("edit to that untracked file", true)
	runGit(t, root, "add", "a/new_untracked.go")
	expect("staging (index rewrite)", true)
	runGit(t, root, "commit", "-q", "-m", "two")
	expect("commit (HEAD ref)", true)
	// untracked subtrees at any depth (git status --untracked-files=all walks them)
	writeFile(t, filepath.Join(root, "u", "v", "three.txt"), "three")
	expect("file added two levels inside an untracked directory", true)
	writeFile(t, filepath.Join(root, "u", "v", "one.txt"), "ONE")
	expect("same-size edit inside an untracked subtree", true)
	if err := os.Remove(filepath.Join(root, "u", "v", "three.txt")); err != nil {
		t.Fatal(err)
	}
	expect("file removed inside an untracked subtree", true)
	mkdir("p/q")
	expect("mkdir -p of an untracked tree", true)
	writeFile(t, filepath.Join(root, "p", "q", "r.go"), "package r\n")
	expect("file written into an existing empty untracked tree", true)
	// ignore rules decide what is untracked
	writeFile(t, filepath.Join(root, ".git", "info", "exclude"), "*.txt\n")
	expect("edit to .git/info/exclude", true)
	excludes := filepath.Join(t.TempDir(), "global-ignore")
	writeFile(t, excludes, "*.log\n")
	runGit(t, root, "config", "core.excludesFile", excludes)
	expect("core.excludesFile set (repository config)", true)
	writeFile(t, excludes, "*.tmp\n")
	expect("edit to the configured excludes file", true)
	writeFile(t, filepath.Join(root, "a", ".gitignore"), "gen/\n")
	expect("new .gitignore in a traversed directory", true)
	// directories git ignores as a whole are not read: their content cannot move the key
	writeFile(t, filepath.Join(root, "build", "out", "o.bin"), "objobj")
	writeFile(t, filepath.Join(root, "build", "out", "p.bin"), "new")
	writeFile(t, filepath.Join(root, "a", "node_modules", "m", "index.js"), "changed")
	expect("changes inside ignored directories", false)
	if err := os.Remove(filepath.Join(root, "c", "z.go")); err != nil {
		t.Fatal(err)
	}
	expect("deleted tracked file", true)
	runGit(t, root, "checkout", "-q", "-b", "other")
	expect("branch switch (HEAD)", true)
	expect("settled", false)
}

func TestIdentityCacheTTLAndInvalidation(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	ctx := context.Background()
	now := time.Now().Add(3 * time.Second)
	identityNow = func() time.Time { return now }
	CurrentRepoIdentity(ctx, root)
	now = now.Add(defaultIdentityTTL - time.Millisecond)
	if _, obs := CurrentRepoIdentity(ctx, root); !obs.Cached || obs.Age != defaultIdentityTTL-time.Millisecond || f.runs.Load() != 1 {
		t.Fatalf("inside the TTL: %+v runs=%d", obs, f.runs.Load())
	}
	now = now.Add(time.Millisecond)
	if _, obs := CurrentRepoIdentity(ctx, root); obs.Cached || f.runs.Load() != 2 {
		t.Fatalf("at the TTL the identity must be re-sampled: %+v runs=%d", obs, f.runs.Load())
	}
	InvalidateRepoIdentity(root)
	if _, obs := CurrentRepoIdentity(ctx, root); obs.Cached || f.runs.Load() != 3 {
		t.Fatalf("an invalidated identity must be re-sampled: runs=%d", f.runs.Load())
	}
	// TTL 0 turns the cache and the fingerprint off: no reuse and no walk at all
	t.Setenv("XMUSTARD_IDENTITY_CACHE_MS", "0")
	walks := fingerprintWalks.Load()
	CurrentRepoIdentity(ctx, root)
	identityCache.after(ctx, identityCache.observe(ctx, root, false))
	if _, obs := CurrentRepoIdentity(ctx, root); obs.Cached || f.runs.Load() != 7 || fingerprintWalks.Load() != walks {
		t.Fatalf("TTL 0: runs=%d walks=%d", f.runs.Load(), fingerprintWalks.Load()-walks)
	}
}

// A read that finds no live pairing samples, then walks to pair the sample for the
// next read. That walk pays off only if another read follows within the TTL: after
// a pairing expired unused, an isolated read (none within the TTL before it) samples
// without walking, exactly as without the cache, and keeps its sample unpaired. A
// read within the TTL of it pairs that sample with its own walk (one walk, no run:
// the common burst of pages after an LLM turn longer than the TTL costs one run in
// all), and a pairing that was used keeps the walk on.
func TestIdentityCacheSkipsThePairingWalkForIsolatedReads(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	ctx := context.Background()
	now := time.Now().Add(3 * time.Second)
	identityNow = func() time.Time { return now }
	read := func(step string, after time.Duration, wantRuns, wantWalks int64, wantCached bool) {
		t.Helper()
		now = now.Add(after)
		walks := fingerprintWalks.Load()
		_, obs := CurrentRepoIdentity(ctx, root)
		if obs.Cached != wantCached || f.runs.Load() != wantRuns || fingerprintWalks.Load()-walks != wantWalks {
			t.Fatalf("%s: cached=%v runs=%d walks=%d; want cached=%v runs=%d walks=%d", step, obs.Cached,
				f.runs.Load(), fingerprintWalks.Load()-walks, wantCached, wantRuns, wantWalks)
		}
	}
	idle := defaultIdentityTTL + time.Second
	read("first read: sample, then a walk to pair it", 0, 1, 1, false)
	read("isolated read after the pairing expired unused: no walk", idle, 2, 0, false)
	read("another isolated read: no walk", idle, 3, 0, false)
	read("a read within the TTL of it: pairs its sample with this walk", time.Second, 3, 1, true)
	read("the next read uses the pairing", 100*time.Millisecond, 3, 1, true)
	read("the used pairing expired: the walk stays on", idle, 4, 1, false)
}

// The lazy pairing of an isolated read's sample follows the same rule as any pairing
// of a sample with a walk taken at another moment: a change after the sample makes
// the tree not quiet, so the next read samples instead of serving the pre-change key.
// The changes here show up only in git files: a branch moved by update-ref (only the
// loose ref is rewritten), the branch deleted (`update-ref -d HEAD`: the loose ref is
// gone, its directory moved) and the index removed.
func TestIdentityCacheRefusesALazyPairingAcrossAChange(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(t *testing.T, root, moved string)
	}{
		{"a branch moved", func(t *testing.T, root, moved string) { runGit(t, root, "update-ref", "HEAD", moved) }},
		{"the branch deleted", func(t *testing.T, root, _ string) { runGit(t, root, "update-ref", "-d", "HEAD") }},
		{"the index removed", func(t *testing.T, root, _ string) { mustRemove(t, filepath.Join(root, ".git", "index")) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := installFakeSampler(t)
			root := initGitRepo(t)
			moved := strings.TrimSpace(gitOutput(t, root, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "moved"))
			frozenIdentityClock(t)
			identityCache.mu.Lock()
			identityCache.state(root).unusedPairing = true // a pairing expired unused
			identityCache.mu.Unlock()
			ctx := context.Background()
			walks := fingerprintWalks.Load()
			if id, obs := CurrentRepoIdentity(ctx, root); id.Key != "k1" || obs.Cached || fingerprintWalks.Load() != walks {
				t.Fatalf("isolated read: %+v %+v walks=%d; want a sample and no walk", id, obs, fingerprintWalks.Load()-walks)
			}
			c.change(t, root, moved)
			f.set("k2")
			if id, obs := CurrentRepoIdentity(ctx, root); id.Key != "k2" || obs.Cached || f.runs.Load() != 2 {
				t.Fatalf("a lazy pairing across %s served %+v %+v (runs=%d); want a fresh k2", c.name, id, obs, f.runs.Load())
			}
		})
	}
}

func TestIdentityCacheOutsideGitReusesOnlyIncompleteIdentities(t *testing.T) {
	f := installFakeSampler(t)
	root := t.TempDir()
	ctx := context.Background()
	CurrentRepoIdentity(ctx, root)
	if _, obs := CurrentRepoIdentity(ctx, root); obs.Cached || f.runs.Load() != 2 {
		t.Fatalf("a complete identity outside Git cannot be guarded by stat and must not be reused (runs=%d)", f.runs.Load())
	}
	f.complete, f.listing = false, false // repo-key outside Git: incomplete, no listing
	CurrentRepoIdentity(ctx, root)
	if _, obs := CurrentRepoIdentity(ctx, root); !obs.Cached || f.runs.Load() != 3 {
		t.Fatalf("an incomplete identity (freshness unknown either way) may be reused (runs=%d)", f.runs.Load())
	}
	runGit(t, root, "init", "-q")
	if _, obs := CurrentRepoIdentity(ctx, root); obs.Cached {
		t.Fatal("git init must end reuse of the non-Git identity")
	}
}

func TestIdentityCacheDropsASampleThatRacedAnEdit(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	frozenIdentityClock(t)
	edits := 0
	f.during = func() {
		edits++
		writeFile(t, filepath.Join(root, "top.go"), strings.Repeat("x", 10+edits))
	}
	ctx := context.Background()
	CurrentRepoIdentity(ctx, root)
	f.during = nil
	if _, obs := CurrentRepoIdentity(ctx, root); obs.Cached || f.runs.Load() != 2 {
		t.Fatalf("a sample during which the tree changed must not be cached (runs=%d)", f.runs.Load())
	}
	// once the edit is past the racy window, a quiet sample is cached
	identityNow = func() time.Time { return time.Now().Add(3 * time.Second) }
	CurrentRepoIdentity(ctx, root)
	if _, obs := CurrentRepoIdentity(ctx, root); !obs.Cached {
		t.Fatal("a quiet sample is cached")
	}
}

func TestIdentityCacheSharesOneInFlightSample(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	f.block = make(chan struct{})
	const callers = 6
	var waiting atomic.Int64
	identityWaitHook = func() {
		if waiting.Add(1) == callers-1 {
			close(f.block)
		}
	}
	t.Cleanup(func() { identityWaitHook = nil })
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if id, _ := CurrentRepoIdentity(context.Background(), root); id.Key != "k1" {
				t.Errorf("shared identity = %+v", id)
			}
		}()
	}
	wg.Wait()
	if n := f.runs.Load(); n != 1 {
		t.Fatalf("%d concurrent callers ran repo-key %d times; want 1", callers, n)
	}
}

// A sample whose ignored-directory listing differs from the one the tree was walked
// with is not paired with that walk: a directory the walk skipped may no longer be
// ignored, so edits inside it would go unseen.
func TestIdentityCacheRewalksWhenTheIgnoredListingChanges(t *testing.T) {
	f := installFakeSampler(t)
	f.ignored = []string{"build/"}
	root := initGitRepo(t)
	writeFile(t, filepath.Join(root, "build", "x.txt"), "x")
	ctx := context.Background()
	CurrentRepoIdentity(ctx, root)
	if _, obs := CurrentRepoIdentity(ctx, root); !obs.Cached {
		t.Fatal("unchanged tree not reused")
	}
	// build/ stops being ignored (the rule lived in a .gitignore that is now edited)
	writeFile(t, filepath.Join(root, ".gitignore"), "# nothing ignored\n")
	f.mu.Lock()
	f.ignored = nil
	f.mu.Unlock()
	if _, obs := CurrentRepoIdentity(ctx, root); obs.Cached || f.runs.Load() != 2 {
		t.Fatalf("ignore-rule change must re-sample: runs=%d", f.runs.Load())
	}
	if _, obs := CurrentRepoIdentity(ctx, root); !obs.Cached || f.runs.Load() != 2 {
		t.Fatalf("re-paired with the new listing: runs=%d", f.runs.Load())
	}
	writeFile(t, filepath.Join(root, "build", "x.txt"), "now tracked by status")
	if _, obs := CurrentRepoIdentity(ctx, root); obs.Cached || f.runs.Load() != 3 {
		t.Fatalf("an edit in a no-longer-ignored directory must re-sample: runs=%d", f.runs.Load())
	}
}

// PAR-RT-12: where the fingerprint cannot help, the cache stops walking instead of
// adding a walk to every sample.
func TestIdentityCacheStopsWalkingWhereTheFingerprintCannotHelp(t *testing.T) {
	ctx := context.Background()
	t.Run("no ignored-directory listing", func(t *testing.T) {
		f := installFakeSampler(t)
		f.listing = false
		root := initGitRepo(t)
		walks := fingerprintWalks.Load()
		for i := 0; i < 3; i++ {
			CurrentRepoIdentity(ctx, root)
		}
		if f.runs.Load() != 3 || fingerprintWalks.Load() != walks {
			t.Fatalf("runs=%d walks=%d", f.runs.Load(), fingerprintWalks.Load()-walks)
		}
	})
	t.Run("walk over its bound", func(t *testing.T) {
		f := installFakeSampler(t)
		root := initGitRepo(t)
		for i := 0; i < fingerprintBatch+10; i++ {
			writeFile(t, filepath.Join(root, "data", fmt.Sprintf("f%05d", i)), "x")
		}
		prev := fingerprintEntryBound
		fingerprintEntryBound = fingerprintBatch
		t.Cleanup(func() { fingerprintEntryBound = prev })
		if fp := repoStatFingerprint(root, newIgnoreSet(nil)); fp.ok {
			t.Fatal("a walk over its entry bound must be unavailable")
		}
		walks := fingerprintWalks.Load()
		for i := 0; i < 3; i++ {
			CurrentRepoIdentity(ctx, root)
		}
		if f.runs.Load() != 3 || fingerprintWalks.Load()-walks != 1 {
			t.Fatalf("an unavailable fingerprint is retried only after %v: runs=%d walks=%d", fingerprintRetryAfter, f.runs.Load(), fingerprintWalks.Load()-walks)
		}
	})
	t.Run("walk not clearly cheaper than repo-key", func(t *testing.T) {
		f := installFakeSampler(t)
		fingerprintCostJudged = true // the fake repo-key costs ~nothing: any walk loses
		root := initGitRepo(t)
		walks := fingerprintWalks.Load()
		for i := 0; i < 4; i++ {
			CurrentRepoIdentity(ctx, root)
		}
		// read 1 runs and pairs (walk 1), read 2 is a hit (walk 2, the second costly
		// walk in a row turns the fingerprint off), reads 3 and 4 run without walking
		if f.runs.Load() != 3 || fingerprintWalks.Load()-walks != 2 {
			t.Fatalf("runs=%d walks=%d", f.runs.Load(), fingerprintWalks.Load()-walks)
		}
	})
}

// One stalled walk (a GC pause or preemption inside its wall time) must not turn the
// fingerprint off; two costly walks in a row do. The remote Linux gate lost the
// 16 MiB expansion's cache to one 3 ms walk among 90 us ones against a 4 ms fake
// repo-key: the fingerprint went off for a minute and most pages spawned repo-key.
func TestCostJudgeIgnoresOneStalledWalk(t *testing.T) {
	prev := fingerprintCostJudged
	fingerprintCostJudged = true
	t.Cleanup(func() { fingerprintCostJudged = prev })
	now := time.Now()
	const cheap, stall, key = 90_000, 3_000_000, 4_000_000
	st := &rootState{keyNs: key}
	for i, ns := range []int64{cheap, stall, cheap, cheap, stall, cheap} {
		if st.noteWalk(ns, now); !st.fpOffUntil.IsZero() {
			t.Fatalf("walk %d (%d ns) turned the fingerprint off after a single stall", i, ns)
		}
	}
	st.noteWalk(stall, now)
	if st.noteWalk(stall, now); !st.fpOffUntil.Equal(now.Add(fingerprintRetryAfter)) {
		t.Fatal("two costly walks in a row must turn the fingerprint off")
	}
	if st.walkNs != 0 || st.lastWalkNs != 0 {
		t.Fatalf("after judging off the next probes measure afresh: %+v", st)
	}
}

func TestFingerprintLayouts(t *testing.T) {
	ign := newIgnoreSet(nil)
	for _, init := range [][]string{nil, {"--object-format=sha256"}} {
		root := initGitRepo(t, init...)
		before := repoStatFingerprint(root, ign)
		if !before.ok || !before.git {
			t.Fatalf("%v: fingerprint unavailable", init)
		}
		if again := repoStatFingerprint(root, ign); !again.same(before) {
			t.Fatalf("%v: fingerprint unstable over an unchanged tree", init)
		}
	}
	// split and sparse indexes are read by stat, never parsed: both are supported
	root := initGitRepo(t)
	runGit(t, root, "update-index", "--split-index")
	split := repoStatFingerprint(root, ign)
	if !split.ok {
		t.Fatal("split index: fingerprint unavailable")
	}
	writeFile(t, filepath.Join(root, "c", "z.go"), "package p // staged\n")
	runGit(t, root, "add", "c/z.go")
	if after := repoStatFingerprint(root, ign); after.same(split) {
		t.Fatal("staging with a split index must change the fingerprint")
	}

	// a linked worktree: gitfile + commondir; its branch ref lives in the common dir
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, root, "worktree", "add", "-q", "-b", "side", wt)
	before := repoStatFingerprint(wt, ign)
	if !before.ok || !before.git {
		t.Fatalf("worktree fingerprint unavailable: %+v", before)
	}
	writeFile(t, filepath.Join(wt, "c", "z.go"), "package p // side edit\n")
	runGit(t, wt, "commit", "-q", "-am", "side")
	if after := repoStatFingerprint(wt, ign); after.same(before) {
		t.Fatal("a commit in a linked worktree must change its fingerprint")
	}
	// inside a Git worktree the walk needs repo-key's listing
	if fp := repoStatFingerprint(root, nil); fp.ok {
		t.Fatal("a Git fingerprint without the ignored-directory listing must be unavailable")
	}
}

// Every git metadata file the fingerprint reads enters by its stat key as well as its
// content: rewriting one in place with the same content changes the fingerprint, so a
// change to it is visible to the racy rule where no earlier walk exists to compare
// with. A symbolic ref chain (HEAD -> alias -> branch) is followed to the branch.
func TestFingerprintStatKeysTheGitFilesItReads(t *testing.T) {
	ign := newIgnoreSet(nil)
	root := initGitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, root, "worktree", "add", "-q", "-b", "side", wt)
	branch := strings.TrimSpace(gitOutput(t, root, "symbolic-ref", "HEAD"))
	wtGitDir := strings.TrimSpace(gitOutput(t, wt, "rev-parse", "--absolute-git-dir"))
	// git reads a linked worktree's sparse-checkout file from its own git dir
	writeFile(t, filepath.Join(wtGitDir, "info", "sparse-checkout"), "/*\n")
	for _, c := range []struct{ what, tree, file string }{
		{"HEAD", root, filepath.Join(root, ".git", "HEAD")},
		{"the branch ref", root, filepath.Join(root, ".git", branch)},
		{"a linked worktree's HEAD", wt, filepath.Join(wtGitDir, "HEAD")},
		{"a linked worktree's branch ref (common dir)", wt, filepath.Join(root, ".git", "refs", "heads", "side")},
		{"a linked worktree's gitfile", wt, filepath.Join(wt, ".git")},
		{"a linked worktree's commondir", wt, filepath.Join(wtGitDir, "commondir")},
		{"a linked worktree's sparse-checkout", wt, filepath.Join(wtGitDir, "info", "sparse-checkout")},
	} {
		before := repoStatFingerprint(c.tree, ign)
		if !before.ok {
			t.Fatalf("%s: fingerprint unavailable", c.what)
		}
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond) // a distinct mtime
		if err := os.WriteFile(c.file, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if after := repoStatFingerprint(c.tree, ign); !after.ok || after.same(before) {
			t.Fatalf("%s: rewriting %s must change the fingerprint", c.what, c.file)
		}
	}
	runGit(t, root, "symbolic-ref", "refs/heads/alias", branch)
	runGit(t, root, "symbolic-ref", "HEAD", "refs/heads/alias")
	before := repoStatFingerprint(root, ign)
	moved := strings.TrimSpace(gitOutput(t, root, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "moved"))
	runGit(t, root, "update-ref", "--no-deref", branch, moved)
	if after := repoStatFingerprint(root, ign); !after.ok || after.same(before) {
		t.Fatal("moving the branch at the end of a symbolic ref chain must change the fingerprint")
	}
}

// A removal leaves no stat key of its own, and where no earlier walk exists to compare
// with, only a newer stat key shows the racy rule a change. While the loose ref HEAD
// resolves through, the index, a file in info/ or the XDG ignore file is missing, the
// stat keys of the directories that held it stand in, so its removal adds a newer
// stat key. While the ref is present its directories are not keyed: writing another
// branch (a commit in another linked worktree, say) leaves the fingerprint alone.
func TestFingerprintDatesTheRemovalOfAGitFile(t *testing.T) {
	ign := newIgnoreSet(nil)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	root := initGitRepo(t)
	writeFile(t, filepath.Join(root, ".git", "info", "attributes"), "*.bin binary\n")
	writeFile(t, filepath.Join(xdg, "git", "ignore"), "*.log\n")
	root2 := initGitRepo(t)
	runGit(t, root2, "checkout", "-q", "-b", "feature/x")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, root2, "worktree", "add", "-q", "-b", "side", wt)

	before := repoStatFingerprint(root, ign)
	time.Sleep(20 * time.Millisecond)
	runGit(t, root, "branch", "other")
	if after := repoStatFingerprint(root, ign); !after.ok || !after.same(before) {
		t.Fatal("writing another branch must leave the fingerprint alone while the checked-out one is a loose ref")
	}
	for _, c := range []struct {
		what, tree string
		remove     func()
	}{
		{"info/exclude", root, func() { mustRemove(t, filepath.Join(root, ".git", "info", "exclude")) }},
		{"info/attributes", root, func() { mustRemove(t, filepath.Join(root, ".git", "info", "attributes")) }},
		{"the XDG ignore file", root, func() { mustRemove(t, filepath.Join(xdg, "git", "ignore")) }},
		{"the checked-out branch", root, func() { runGit(t, root, "update-ref", "-d", "HEAD") }},
		{"the index", root, func() { mustRemove(t, filepath.Join(root, ".git", "index")) }},
		{"a branch in a nested ref directory", root2, func() { runGit(t, root2, "update-ref", "-d", "refs/heads/feature/x") }},
		{"a linked worktree's branch (common dir)", wt, func() { runGit(t, wt, "update-ref", "-d", "HEAD") }},
	} {
		before := repoStatFingerprint(c.tree, ign)
		if !before.ok {
			t.Fatalf("%s: fingerprint unavailable", c.what)
		}
		time.Sleep(20 * time.Millisecond) // a distinct mtime
		c.remove()
		after := repoStatFingerprint(c.tree, ign)
		if !after.ok || after.same(before) || after.newestNs <= before.newestNs {
			t.Fatalf("%s: its removal must add a newer stat key (newest %d -> %d, ok=%v)", c.what, before.newestNs, after.newestNs, after.ok)
		}
	}
}

// The ignore and attributes files git reads can be named in config.worktree (with
// extensions.worktreeConfig) or in a config file pulled in by [include] /
// [includeIf]; editing any of them, or the included file itself, changes the
// fingerprint.
func TestFingerprintFollowsWorktreeConfigAndIncludes(t *testing.T) {
	ign := newIgnoreSet(nil)
	root := initGitRepo(t)
	edit := func(what, path, content string) {
		t.Helper()
		before := repoStatFingerprint(root, ign)
		if !before.ok {
			t.Fatalf("%s: fingerprint unavailable", what)
		}
		writeFile(t, path, content)
		if after := repoStatFingerprint(root, ign); after.same(before) {
			t.Fatalf("%s: an edit to %s must change the fingerprint", what, path)
		}
	}
	tmp := t.TempDir()
	wtExcludes := filepath.Join(tmp, "worktree-excludes")
	writeFile(t, wtExcludes, "*.log\n")
	runGit(t, root, "config", "extensions.worktreeConfig", "true")
	runGit(t, root, "config", "--worktree", "core.excludesFile", wtExcludes)
	edit("core.excludesFile set in config.worktree", wtExcludes, "*.tmp\n")

	incExcludes := filepath.Join(tmp, "included-excludes")
	writeFile(t, incExcludes, "*.log\n")
	inc := filepath.Join(tmp, "nested", "included.gitconfig")
	attrs := filepath.Join(tmp, "included-attributes")
	writeFile(t, inc, "[core]\n\tattributesFile = "+attrs+"\n")
	top := filepath.Join(tmp, "top.gitconfig")
	writeFile(t, top, "[include]\n\tpath = nested/included.gitconfig\n")
	runGit(t, root, "config", "include.path", top)
	runGit(t, root, "config", "includeIf.gitdir:"+root+"/.git.path", incExcludes+".cfg")
	writeFile(t, incExcludes+".cfg", "[core]\n\texcludesFile = "+incExcludes+"\n")
	edit("a config file included by a relative path", inc, "[core]\n\tattributesFile = "+attrs+"\n\tquotePath = false\n")
	edit("core.attributesFile set in an included config", attrs, "*.bin binary\n")
	edit("core.excludesFile set in an [includeIf] config", incExcludes, "*.tmp\n")
}

// git status runs with --ignore-submodules=none, so a submodule's working state is
// part of the identity: the fingerprint walks nested worktrees in full.
func TestFingerprintCoversSubmodules(t *testing.T) {
	ign := newIgnoreSet(nil)
	sub := initGitRepo(t)
	parent := initGitRepo(t)
	runGit(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "deps/sub")
	runGit(t, parent, "commit", "-q", "-m", "add submodule")
	before := repoStatFingerprint(parent, ign)
	if !before.ok || !before.git {
		t.Fatalf("initialized submodule: %+v", before)
	}
	if again := repoStatFingerprint(parent, ign); !again.same(before) {
		t.Fatal("fingerprint unstable over an unchanged tree with a submodule")
	}
	writeFile(t, filepath.Join(parent, "deps", "sub", "a", "x.go"), "package p // edited inside the submodule\n")
	edited := repoStatFingerprint(parent, ign)
	if edited.same(before) {
		t.Fatal("an edit inside a submodule must change the fingerprint")
	}
	writeFile(t, filepath.Join(parent, "deps", "sub", "new", "deep", "u.txt"), "u")
	if after := repoStatFingerprint(parent, ign); after.same(edited) {
		t.Fatal("an untracked file deep inside a submodule must change the fingerprint")
	}
	runGit(t, filepath.Join(parent, "deps", "sub"), "checkout", "-q", "-b", "sub-branch")
	if after := repoStatFingerprint(parent, ign); after.same(edited) {
		t.Fatal("a submodule HEAD change must change the fingerprint")
	}
}

// BenchmarkRepoStatFingerprint measures the spawn-free fingerprint an evidence page
// or a cached identity read pays instead of a repo-key run, on the repository named by
// XMUSTARD_BENCH_REPO (skipped when unset), with repo-key's own ignored listing
// (XMUSTARD_CORE_BIN must be the parity core), and reports the listing's retained
// bytes.
func BenchmarkRepoStatFingerprint(b *testing.B) {
	root := os.Getenv("XMUSTARD_BENCH_REPO")
	if root == "" {
		b.Skip("set XMUSTARD_BENCH_REPO to a Git worktree")
	}
	s := repoIdentity(context.Background(), root)
	if s.ign == nil {
		b.Fatalf("repo-key reported no ignored-directory listing for %s (%+v)", root, s.id)
	}
	if fp := repoStatFingerprint(root, s.ign); !fp.ok || !fp.git {
		b.Fatalf("fingerprint unavailable for %s", root)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		repoStatFingerprint(root, s.ign)
	}
	b.StopTimer()
	b.ReportMetric(float64(s.ign.residentBytes()), "listing-bytes")
	b.ReportMetric(float64(s.ign.len()), "ignored-dirs")
}

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
		for i := 0; i < 3; i++ {
			CurrentRepoIdentity(ctx, root)
		}
		if f.runs.Load() != 3 || fingerprintWalks.Load()-walks != 1 {
			t.Fatalf("runs=%d walks=%d", f.runs.Load(), fingerprintWalks.Load()-walks)
		}
	})
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

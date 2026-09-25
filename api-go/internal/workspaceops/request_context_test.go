package workspaceops

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// fakeSampler replaces the repo-key sampler: it counts runs and reports key, which
// the test sets, as a complete (or incomplete) identity.
type fakeSampler struct {
	runs     atomic.Int64
	mu       sync.Mutex
	key      string
	complete bool
	during   func() // runs inside a sample (a concurrent edit)
	block    chan struct{}
}

func installFakeSampler(t *testing.T) *fakeSampler {
	t.Helper()
	f := &fakeSampler{key: "k1", complete: true}
	prev := sampleRepoIdentity
	sampleRepoIdentity = func(ctx context.Context, root string) RepoIdentity {
		f.runs.Add(1)
		if f.block != nil {
			<-f.block
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.during != nil {
			f.during()
		}
		return RepoIdentity{Key: f.key, Complete: f.complete, Source: "repo-key"}
	}
	InvalidateRepoIdentity("")
	t.Cleanup(func() { sampleRepoIdentity = prev; InvalidateRepoIdentity("") })
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

func TestIdentityCacheReusesUntilTheWorkingStateChanges(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	ctx := context.Background()
	expect := func(step string, runs int64, cached bool) {
		t.Helper()
		_, obs := CurrentRepoIdentity(ctx, root)
		if n := f.runs.Load(); n != runs || obs.Cached != cached {
			t.Fatalf("%s: runs=%d cached=%v; want runs=%d cached=%v", step, n, obs.Cached, runs, cached)
		}
	}
	expect("first read samples", 1, false)
	expect("unchanged state reuses", 1, true)
	writeFile(t, filepath.Join(root, "a", "b", "y.go"), "package p // edited, longer\n")
	expect("edit to a tracked file", 2, false)
	expect("then reuses", 2, true)
	writeFile(t, filepath.Join(root, "a", "new_untracked.go"), "x")
	expect("new untracked file beside tracked files", 3, false)
	writeFile(t, filepath.Join(root, "a", "new_untracked.go"), "xyz")
	expect("edit to that untracked file", 4, false)
	runGit(t, root, "add", "a/new_untracked.go")
	expect("staging (index rewrite)", 5, false)
	runGit(t, root, "commit", "-q", "-m", "two")
	expect("commit (HEAD ref)", 6, false)
	if err := os.MkdirAll(filepath.Join(root, "newdir", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	expect("new untracked directory", 7, false)
	if err := os.Remove(filepath.Join(root, "c", "z.go")); err != nil {
		t.Fatal(err)
	}
	expect("deleted tracked file", 8, false)
	runGit(t, root, "checkout", "-q", "-b", "other")
	expect("branch switch (HEAD)", 9, false)
	expect("settled", 9, true)
}

func TestIdentityCacheTTLAndInvalidation(t *testing.T) {
	f := installFakeSampler(t)
	root := initGitRepo(t)
	ctx := context.Background()
	now := time.Now()
	prev := identityNow
	identityNow = func() time.Time { return now }
	t.Cleanup(func() { identityNow = prev })
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
	t.Setenv("XMUSTARD_IDENTITY_CACHE_MS", "0")
	CurrentRepoIdentity(ctx, root)
	if _, obs := CurrentRepoIdentity(ctx, root); obs.Cached || f.runs.Load() != 5 {
		t.Fatalf("TTL 0 disables reuse: runs=%d", f.runs.Load())
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
	f.complete = false
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

// fingerprintDirs returns the tracked directories the fingerprint walks.
func fingerprintDirs(t *testing.T, root string) []string {
	t.Helper()
	_, gitDir, commonDir, found, err := gitLayoutFor(root)
	if err != nil || !found {
		t.Fatalf("layout: %v found=%v", err, found)
	}
	f, err := os.Open(filepath.Join(gitDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	idx, err := parseIndexDirs(bufio.NewReader(f), fi.Size(), objectHashLen(commonDir))
	if err != nil {
		t.Fatalf("parse index: %v", err)
	}
	return idx.dirs
}

func TestFingerprintParsesIndexVersionsAndLayouts(t *testing.T) {
	want := []string{"", "a", "a/b", "c"}
	root := initGitRepo(t)
	if got := fingerprintDirs(t, root); !slices.Equal(got, want) {
		t.Fatalf("v2 dirs = %v, want %v", got, want)
	}
	runGit(t, root, "update-index", "--index-version", "4")
	if got := fingerprintDirs(t, root); !slices.Equal(got, want) {
		t.Fatalf("v4 dirs = %v, want %v", got, want)
	}
	if fp := repoStatFingerprint(root); !fp.ok || !fp.git {
		t.Fatalf("v4 fingerprint unavailable: %+v", fp)
	}

	sha := initGitRepo(t, "--object-format=sha256")
	if got := fingerprintDirs(t, sha); !slices.Equal(got, want) {
		t.Fatalf("sha256 dirs = %v, want %v", got, want)
	}

	// a linked worktree: gitfile + commondir; its branch ref lives in the common dir
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, root, "worktree", "add", "-q", "-b", "side", wt)
	before := repoStatFingerprint(wt)
	if !before.ok || !before.git {
		t.Fatalf("worktree fingerprint unavailable: %+v", before)
	}
	writeFile(t, filepath.Join(wt, "c", "z.go"), "package p // side edit\n")
	runGit(t, wt, "commit", "-q", "-am", "side")
	if after := repoStatFingerprint(wt); after == before {
		t.Fatal("a commit in a linked worktree must change its fingerprint")
	}

	// a path longer than the 12-bit name length field (NUL-terminated form)
	long := strings.Repeat(strings.Repeat("d", 200)+"/", 21) + "f.go"
	blob := strings.TrimSpace(gitOutput(t, root, "hash-object", "-w", "top.go"))
	runGit(t, root, "update-index", "--index-version", "2")
	runGit(t, root, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+long)
	if dirs := fingerprintDirs(t, root); !slices.Contains(dirs, filepath.ToSlash(filepath.Dir(long))) || !slices.Contains(dirs, "c") {
		t.Fatalf("long index path not parsed: %d dirs", len(dirs))
	}
	if fp := repoStatFingerprint(root); !fp.ok {
		t.Fatal("an unopenable tracked directory is a state, not a failure")
	}
}

func TestFingerprintUnavailableForSplitIndex(t *testing.T) {
	root := initGitRepo(t)
	runGit(t, root, "update-index", "--split-index")
	if fp := repoStatFingerprint(root); fp.ok {
		t.Fatal("a split index must make the fingerprint unavailable")
	}
}

// git status runs with --ignore-submodules=none, so a submodule's working state is
// part of the identity: the fingerprint walks initialized submodules too.
func TestFingerprintCoversSubmodules(t *testing.T) {
	// an uninitialized gitlink (no checkout) is a state, not a failure
	root := initGitRepo(t)
	head := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	runGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+head+",vendor/sub")
	if fp := repoStatFingerprint(root); !fp.ok || !fp.git {
		t.Fatalf("uninitialized submodule: %+v", fp)
	}

	sub := initGitRepo(t)
	parent := initGitRepo(t)
	runGit(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "deps/sub")
	runGit(t, parent, "commit", "-q", "-m", "add submodule")
	before := repoStatFingerprint(parent)
	if !before.ok || !before.git {
		t.Fatalf("initialized submodule: %+v", before)
	}
	if again := repoStatFingerprint(parent); again != before {
		t.Fatal("fingerprint unstable over an unchanged tree with a submodule")
	}
	// an in-place edit inside the submodule's checkout changes the parent's identity
	writeFile(t, filepath.Join(parent, "deps", "sub", "a", "x.go"), "package p // edited inside the submodule\n")
	if after := repoStatFingerprint(parent); after == before {
		t.Fatal("an edit inside a submodule must change the fingerprint")
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

// BenchmarkRepoStatFingerprint measures the spawn-free fingerprint an evidence page
// or a cached identity read pays instead of a repo-key run, on the repository named by
// XMUSTARD_BENCH_REPO (skipped when unset), and reports the resident bytes of its
// cached tracked-directory list.
func BenchmarkRepoStatFingerprint(b *testing.B) {
	root := os.Getenv("XMUSTARD_BENCH_REPO")
	if root == "" {
		b.Skip("set XMUSTARD_BENCH_REPO to a Git worktree")
	}
	if fp := repoStatFingerprint(root); !fp.ok || !fp.git {
		b.Fatalf("fingerprint unavailable for %s", root)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		repoStatFingerprint(root)
	}
	b.StopTimer()
	resident := 0
	indexDirsCache.Lock()
	for _, e := range indexDirsCache.m {
		resident += len(e.key) + 24*len(e.dirs)
		for _, d := range e.dirs {
			resident += len(d)
		}
	}
	dirs := 0
	for _, e := range indexDirsCache.m {
		dirs += len(e.dirs)
	}
	indexDirsCache.Unlock()
	b.ReportMetric(float64(resident), "cached-dir-bytes")
	b.ReportMetric(float64(dirs), "tracked-dirs")
}

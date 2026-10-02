package workspaceops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/govstore"
)

// WS-28C: a per-file outline-hash cache across ground calls. A second invalidation on an
// unchanged tree re-hashes nothing; an edit re-hashes its file and still marks; the cache
// is bounded by entries and by held bytes and a miss is never a wrong answer.

// resetOutlineCache empties the process-wide cache so one test cannot answer another, and
// returns the previous holder so a test can assert against its own writes alone.
func resetOutlineCache(t *testing.T) {
	t.Helper()
	clearOutlineHashes()
	t.Cleanup(clearOutlineHashes)
}

// A second pass over an unchanged tree re-hashes no file, and the first one hashed the
// anchored file: the counter is the acceptance.
//
// The cache is cleared after the fixture because the promotion itself baselines the symbol
// anchor and so warms the entry; that is why the cold pass below is measured from a
// cleared cache rather than from the first call after promotion.
func TestSecondPassOnAnUnchangedTreeRehashesNoFile(t *testing.T) {
	resetOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()

	first, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Hashed != 1 {
		t.Fatalf("a cold pass hashed %d files, want the one anchored file", first.Hashed)
	}
	second, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Hashed != 0 {
		t.Fatalf("an unchanged tree re-hashed %d files, want 0", second.Hashed)
	}
	if second.Checked != first.Checked {
		t.Fatalf("the cache skipped the check itself: %d anchors checked against %d", second.Checked, first.Checked)
	}
}

// The promotion that baselines a symbol anchor warms the same entry, so the first
// invalidation after it already re-hashes nothing. This is the common case and it is what
// makes a repeated ground free.
func TestThePromotionWarmsTheEntryTheFirstPassWouldHash(t *testing.T) {
	resetOutlineCache(t)
	f := newInvalidationFixture(t)
	ctx := context.Background()

	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Hashed != 0 {
		t.Fatalf("the first pass after promotion hashed %d files, want 0: the promotion warms it", rep.Hashed)
	}
	if rep.Checked != 1 || len(rep.Marked) != 0 {
		t.Fatalf("a warm entry changed the verdict: %+v", rep)
	}
}

// An edit to the anchored symbol's body re-hashes its file, and still marks the memory: a
// cached entry is never served for a file whose content moved.
func TestAnEditRehashesItsFileAndStillMarks(t *testing.T) {
	resetOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()
	if _, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"}); err != nil {
		t.Fatal(err)
	}

	writeRepoFile(t, f.root, "srv.go", strings.Replace(srvSource, "return 8081", "return 9090", 1))
	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Hashed != 1 {
		t.Fatalf("an edited file re-hashed %d files, want 1", rep.Hashed)
	}
	if len(rep.Marked) != 1 || !f.entry(t).NeedsReverify {
		t.Fatalf("an edit to the anchored body marked %v (needs_reverify=%v)", rep.Marked, f.entry(t).NeedsReverify)
	}
}

// The key carries the file's identity, not its path alone: a file rewritten in place keeps
// its path and may keep its size, so its mtime and inode identity are what must move for
// the next pass to re-hash.
func TestTheCacheKeyCarriesTheFileStatNotThePath(t *testing.T) {
	resetOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()
	if _, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"}); err != nil {
		t.Fatal(err)
	}

	// A file rewritten in place keeps its path and may keep its size; its mtime and inode
	// identity are what move, so the next pass must re-hash.
	body := strings.Replace(srvSource, "return 8081", "return 7070", 1)
	if len(body) != len(srvSource) {
		t.Fatalf("the fixture edit changed the file size, so it would not prove the key")
	}
	writeRepoFile(t, f.root, "srv.go", body)
	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Hashed != 1 {
		t.Fatalf("a rewritten file re-hashed %d files, want 1: the key follows the path alone", rep.Hashed)
	}
}

// The cache is bounded by entries and by held bytes. Past either bound it still reads
// correctly: refusing an entry costs a re-read, never a wrong verdict.
func TestTheOutlineCacheIsBoundedAndStillReads(t *testing.T) {
	resetOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()

	// Fill past the entry bound with real files, so every entry carries a genuine mark.
	for i := range outlineHashMaxEntries + 4 {
		rel := fmt.Sprintf("filler/f%04d/x.go", i)
		writeRepoFile(t, f.root, rel, "package filler\n\nfunc X() int { return 1 }\n")
		mark, ok := markOutlineFile(f.root, rel)
		if !ok {
			t.Fatalf("%s has no mark", rel)
		}
		storeOutlineHashes(f.ws, f.root, rel, mark, map[string]string{"X": strings.Repeat("a", 64)})
	}
	if n := outlineHashEntries(); n != outlineHashMaxEntries {
		t.Fatalf("the cache holds %d entries, want it full at its bound of %d", n, outlineHashMaxEntries)
	}

	// The bound costs a re-read, not correctness: srv.go's own entry was never stored
	// (the cache was already full when the promotion warmed it), so this pass is a miss
	// and still produces the same verdict as a warm one.
	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Checked != 1 || len(rep.Marked) != 0 {
		t.Fatalf("a full cache changed the verdict: %+v", rep)
	}
	if rep.Hashed != 1 {
		t.Fatalf("a full cache re-hashed %d files, want the one uncacheable miss", rep.Hashed)
	}
	// And because the store was refused, the next pass is a miss too: bounded, not sticky.
	again, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Hashed != 1 {
		t.Fatalf("an uncacheable file was cached anyway: %+v", again)
	}
}

// The byte ceiling refuses an entry past it, and a file whose hashes would exceed it is
// re-hashed rather than retained.
func TestTheOutlineCacheByteCeilingRefusesAndStillReads(t *testing.T) {
	resetOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()

	// One entry whose hashes alone exceed the ceiling is refused outright. The ceiling is
	// inclusive (an entry of exactly it is kept), so this one is sized well past it: one
	// declaration hashing more than the whole ceiling cannot be cached at all.
	big := map[string]string{"Huge": strings.Repeat("b", outlineHashMaxHeld+1)}
	rel := "big/x.go"
	writeRepoFile(t, f.root, rel, "package big\n\nfunc X() int { return 1 }\n")
	mark, ok := markOutlineFile(f.root, rel)
	if !ok {
		t.Fatal("big/x.go has no mark")
	}
	storeOutlineHashes(f.ws, f.root, rel, mark, big)
	if _, cached := outlineHashesFor(f.ws, f.root, rel, mark); cached {
		t.Fatalf("an entry of %d bytes was cached past the %d-byte ceiling", outlineHashMaxHeld, outlineHashMaxHeld)
	}
	if used, _ := outlineHashUsed(); used != 0 {
		t.Fatalf("a refused entry still holds %d bytes", used)
	}

	// And the invalidation that would have cached it still reads correctly.
	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Checked != 1 || rep.Hashed != 1 {
		t.Fatalf("past the byte ceiling: %+v", rep)
	}
}

// A reclaim drops the whole cache and the next pass re-reads. Nothing stale survives a
// reclaim, so the count after it is the count of a cold cache.
func TestReclaimingTheOutlineCacheLeavesNoStaleEntry(t *testing.T) {
	resetOutlineCache(t)
	f := newInvalidationFixture(t)
	ctx := context.Background()
	if _, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"}); err != nil {
		t.Fatal(err)
	}
	if outlineHashEntries() == 0 {
		t.Fatal("the first pass cached nothing, so the reclaim has nothing to prove")
	}
	if done := reclaimOutlineHashes(budget.Pressure{Reason: "test"}); done != nil {
		<-done
	} else {
		t.Fatal("the component is not reclaimable")
	}
	if n := outlineHashEntries(); n != 0 {
		t.Fatalf("a reclaim left %d entries", n)
	}
	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Hashed != 1 {
		t.Fatalf("after a reclaim the pass hashed %d files, want 1", rep.Hashed)
	}
}

// The governor sees the cache as a reclaimable component, so pressure can give the bytes
// back without lowering any other line.
func TestTheOutlineCacheIsAGoverorComponent(t *testing.T) {
	resetOutlineCache(t)
	f := newInvalidationFixture(t)
	if _, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"}); err != nil {
		t.Fatal(err)
	}
	used, ok := outlineHashUsed()
	if !ok {
		t.Fatal("the component does not report its usage")
	}
	if used <= 0 {
		t.Fatalf("a warm cache reports %d bytes", used)
	}
	if done := reclaimOutlineHashes(budget.Pressure{Reason: "test"}); done != nil {
		<-done
	} else {
		t.Fatal("the component is not reclaimable")
	}
	if used, ok := outlineHashUsed(); !ok || used != 0 {
		t.Fatalf("after a reclaim the component reports %d bytes (ok=%v)", used, ok)
	}
}

// An outline the index cut short is never cached. The first pass cannot say the symbol is
// gone, and a cache built from a partial outline would answer "missing" wrongly on the next
// pass; so the second pass, on a complete outline, must hash and mark like a cold cache.
func TestATruncatedOutlineIsNeverCached(t *testing.T) {
	resetOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()

	full := stubOutline(f.root)
	orig := symbolOutline
	t.Cleanup(func() { symbolOutline = orig })
	symbolOutline = func(ctx context.Context, a, b, rel string) (symbolSpans, error) {
		if _, err := full(ctx, a, b, rel); err != nil {
			return nil, err
		}
		spans, _ := full(ctx, a, b, rel)
		keep := symbolSpans{}
		for _, sp := range spans {
			if sp.Name == "ListenPort" {
				keep = append(keep, sp)
			}
		}
		return keep, errOutlineIncomplete
	}

	first, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Marked) != 0 {
		t.Fatalf("a truncated outline marked the memory: %+v", first)
	}
	if n := outlineHashEntries(); n != 0 {
		t.Fatalf("a truncated outline cached %d entries, want none", n)
	}

	// A complete outline on the same unchanged file must therefore hash again: nothing
	// partial was cached to answer the next pass. The body is unchanged, so it is not
	// drifted and nothing is marked; the count is the proof that the pass really hashed.
	symbolOutline = full
	second, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Hashed != 1 {
		t.Fatalf("after a truncated outline the next pass hashed %d, want 1: a partial outline was cached", second.Hashed)
	}
	if len(second.Marked) != 0 {
		t.Fatalf("an unchanged file was marked: %v", second.Marked)
	}
}

// One file's entry holds a hash per anchored declaration, and each is served from the same
// cached entry. Two memories on two symbols of one file therefore cost one hash, and an edit
// to one of them is still caught while the other stays clean.
func TestOneFilesEntryHoldsEveryAnchoredDeclaration(t *testing.T) {
	resetOutlineCache(t)
	dir, ws, root, git := reviewRepo(t)
	writeRepoFile(t, root, "srv.go", srvSource)
	git("add", ".")
	git("commit", "-q", "-m", "srv")
	origResolve, origOutline := resolveSymbols, symbolOutline
	t.Cleanup(func() { resolveSymbols, symbolOutline = origResolve, origOutline })
	resolveSymbols = func(_ context.Context, _, _ string, names []string) ([]symbolMatch, error) {
		var out []symbolMatch
		for _, n := range names {
			out = append(out, symbolMatch{Name: n, Count: 1, Definitions: []symbolDefinition{{Path: "srv.go", Line: 3}}})
		}
		return out, nil
	}
	symbolOutline = stubOutline(root)
	approvedBy(t, dir, ws, "author", "approver", "agent", "`ListenPort` returns the service port 8081.")
	approvedBy(t, dir, ws, "author", "approver", "agent", "`Other` returns a constant.")
	clearOutlineHashes()
	ctx := context.Background()

	rep, err := InvalidateMemory(ctx, dir, ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	// Two anchors, one file: the pass hashed the file once and checked both.
	if rep.Checked != 2 || rep.Hashed != 1 {
		t.Fatalf("two declarations in one file: checked %d, hashed %d, want 2 and 1", rep.Checked, rep.Hashed)
	}
	mark, ok := markOutlineFile(root, "srv.go")
	if !ok {
		t.Fatal("srv.go has no mark")
	}
	if hashes, hit := outlineHashesFor(ws, root, "srv.go", mark); !hit || len(hashes) != 2 {
		t.Fatalf("the file's entry holds %d hashes (hit=%v), want 2", len(hashes), hit)
	}

	// An edit to Other marks only Other's memory, and re-hashes the one file.
	writeRepoFile(t, root, "srv.go", strings.Replace(srvSource, "return 1", "return 2", 1))
	after, err := InvalidateMemory(ctx, dir, ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Hashed != 1 || len(after.Marked) != 1 || len(rep.Marked) != 0 {
		t.Fatalf("an edit to Other: hashed %d, marked %v (before %v), want 1 and one memory", after.Hashed, after.Marked, rep.Marked)
	}
}

// Concurrent passes over the same file must not race on the entry's map: a reader gets the
// cached hashes under the lock, and a later store must not write through that same map.
// Run under -race, this is what proves storeBody copies before it writes.
func TestConcurrentPassesDoNotRaceOnACachedEntry(t *testing.T) {
	resetOutlineCache(t)
	dir, ws, root, git := reviewRepo(t)
	writeRepoFile(t, root, "srv.go", srvSource)
	git("add", ".")
	git("commit", "-q", "-m", "srv")
	origResolve, origOutline := resolveSymbols, symbolOutline
	t.Cleanup(func() { resolveSymbols, symbolOutline = origResolve, origOutline })
	resolveSymbols = func(_ context.Context, _, _ string, names []string) ([]symbolMatch, error) {
		var out []symbolMatch
		for _, n := range names {
			out = append(out, symbolMatch{Name: n, Count: 1, Definitions: []symbolDefinition{{Path: "srv.go", Line: 3}}})
		}
		return out, nil
	}
	symbolOutline = stubOutline(root)
	approvedBy(t, dir, ws, "author", "approver", "agent", "`ListenPort` returns the service port 8081.")
	approvedBy(t, dir, ws, "author", "approver", "agent", "`Other` returns a constant.")
	clearOutlineHashes()

	// Several readers take the cached entry while a writer adds a third hash to it.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 64 {
			mark, ok := markOutlineFile(root, "srv.go")
			if !ok {
				return
			}
			if hashes, hit := outlineHashesFor(ws, root, "srv.go", mark); hit {
				for k, v := range hashes {
					_, _ = k, v
				}
			}
		}
	}()
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for range 16 {
				sess := newRehashSession(context.Background(), root, ws)
				sess.dataDir = dir
				sess.storeBody("srv.go", fmt.Sprintf("Extra%02d", i), strings.Repeat("c", 64))
			}
		}(i)
	}
	wg.Wait()

	// The entry is intact and every writer's key is in it exactly once. The cache was
	// cleared after the promotion, so the anchored hashes are not among them.
	mark, ok := markOutlineFile(root, "srv.go")
	if !ok {
		t.Fatal("srv.go has no mark")
	}
	hashes, hit := outlineHashesFor(ws, root, "srv.go", mark)
	if !hit {
		t.Fatal("the entry is gone after concurrent passes")
	}
	if len(hashes) != 8 {
		t.Fatalf("the entry holds %d hashes, want the 8 writers", len(hashes))
	}
	for i := range 8 {
		if _, ok := hashes[fmt.Sprintf("Extra%02d", i)]; !ok {
			t.Fatalf("writer %d's hash is missing from the entry", i)
		}
	}
}

// A file that is gone stays missing across a warm cache: the cache holds hashes, never a
// verdict, so it cannot turn a missing file into an unchanged one.
func TestACachedHashNeverBecomesAMissingVerdict(t *testing.T) {
	resetOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()
	if _, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.root, "srv.go")); err != nil {
		t.Fatal(err)
	}
	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	// A cached hash for a file that no longer exists must not be served as the current
	// state: the pass reports the anchor as missing, which is a different anchorState.
	if rep.Checked != 1 || rep.Hashed != 0 {
		t.Fatalf("a removed file: %+v (want the anchor checked against a missing file)", rep)
	}
	if len(rep.Marked) != 1 {
		t.Fatalf("removing the anchored file marked %v, want the one memory", rep.Marked)
	}
	var stale []govstore.Anchor
	for _, a := range f.anchors(t) {
		if a.Kind == govstore.AnchorSymbol {
			stale = append(stale, a)
		}
	}
	if len(stale) != 1 || stale[0].StaleSince == "" {
		t.Fatalf("the removed file's symbol anchor is not stale: %+v", stale)
	}
}

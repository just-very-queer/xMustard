package workspaceops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// setOutlineTrustWindow sets the racy window for one test. A fixture cannot be aged the way
// production ages a file, because ctime is not settable from a test, so the window is the
// seam: zero closes it (a fixture's marks count), and an hour opens it wide (nothing is
// trusted). Production never changes it.
func setOutlineTrustWindow(t *testing.T, d time.Duration) {
	t.Helper()
	old := outlineHashTrustWindow
	outlineHashTrustWindow = d
	t.Cleanup(func() { outlineHashTrustWindow = old })
}

// trustOutlineCache closes the racy window, so a fixture's own files count as old enough.
func trustOutlineCache(t *testing.T) { setOutlineTrustWindow(t, 0) }

// A file that changes between the read and the store must not be cached: the hash came
// from the old text, so storing it under the new mark would let a later pass serve that
// stale hash as current and the edit would never be marked. The stub outline rewrites the
// file, which is the moment the mark moves.
func TestAFileChangedWhileItWasReadIsNotCached(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()

	full := stubOutline(f.root)
	orig := symbolOutline
	t.Cleanup(func() { symbolOutline = orig })
	edited := strings.Replace(srvSource, "return 8081", "return 9090", 1)
	rewrite := false
	symbolOutline = func(ctx context.Context, a, b, rel string) (symbolSpans, error) {
		if rewrite {
			writeRepoFile(t, f.root, "srv.go", edited) // the file moves under the pass
		}
		return full(ctx, a, b, rel)
	}

	// The first pass reads the file and, at the outline, the file changes: the hash it
	// computed belongs to text the file no longer holds.
	rewrite = true
	if _, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"}); err != nil {
		t.Fatal(err)
	}
	if n := outlineHashEntries(); n != 0 {
		t.Fatalf("a file changed mid-pass cached %d entries, want none", n)
	}

	// So the next pass reads the new text and still marks the memory.
	rewrite = false
	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Hashed != 1 || len(rep.Marked) != 1 {
		t.Fatalf("after a mid-pass edit: hashed %d, marked %v, want 1 and the one memory", rep.Hashed, rep.Marked)
	}
}

// The cache must never be the weaker of the two readers. The workspace reader refuses a
// path with a symlink component; a mark taken by path through a symlink would let a warm
// entry answer a path that reader rejects, so the mark is taken from the same confined
// open and a symlinked file has no mark at all.
func TestASymlinkedFileHasNoMarkAndIsNeverServed(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("package secret\n\nfunc S() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "pkg", "link.go")
	if err := os.Symlink(filepath.Join(outside, "secret.go"), link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	if _, ok := markOutlineFile(root, "pkg/link.go"); ok {
		t.Fatal("a symlinked file has a mark, so a warm entry could answer a path the workspace reader refuses")
	}
	// And the reader the invalidation falls back to refuses it too, so nothing is hashed
	// from the symlink's target.
	if text, ok := readWorkspaceText(root, "pkg/link.go", maxSymbolFileBytes); ok {
		t.Fatalf("the workspace reader read a symlinked path: %q", text)
	}
}

// The byte ceiling is checked on every store, not only when an entry is created: growing an
// entry past the ceiling is refused, and the total the governor sees does not move.
func TestGrowingAnEntryPastTheByteCeilingIsRefused(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
	root := t.TempDir()
	rel := "grow/x.go"
	if err := os.MkdirAll(filepath.Join(root, "grow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), []byte("package grow\n\nfunc X() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mark, ok := markOutlineFile(root, rel)
	if !ok {
		t.Fatal("grow/x.go has no mark")
	}

	// One key that nearly fills the ceiling, then one that would cross it.
	storeOutlineHash("ws", root, rel, mark, time.Now(), "Big", strings.Repeat("a", outlineHashMaxHeld-1024))
	before, _ := outlineHashUsed()
	if before == 0 {
		t.Fatal("the first key was refused")
	}
	storeOutlineHash("ws", root, rel, mark, time.Now(), "Over", strings.Repeat("b", outlineHashMaxHeld))
	if after, _ := outlineHashUsed(); after != before {
		t.Fatalf("a refused growth moved the total from %d to %d", before, after)
	}
	hashes, hit := outlineHashesFor("ws", root, rel, mark, time.Now())
	if !hit {
		t.Fatal("the entry vanished")
	}
	if _, kept := hashes["Over"]; kept {
		t.Fatal("the key past the ceiling was kept")
	}
	if _, kept := hashes["Big"]; !kept {
		t.Fatal("the key that fitted was dropped")
	}
}

// The held total is exact across a replacement: replacing an entry releases its old bytes
// once, not twice, so the governor is not told the cache is smaller than it is.
func TestTheHeldTotalIsExactAcrossAReplacement(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
	root := t.TempDir()
	rel := "swap/x.go"
	if err := os.MkdirAll(filepath.Join(root, "swap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), []byte("package swap\n\nfunc X() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, _ := markOutlineFile(root, rel)
	storeOutlineHash("ws", root, rel, first, time.Now(), "A", strings.Repeat("a", 4096))
	afterFirst, _ := outlineHashUsed()
	if afterFirst == 0 {
		t.Fatal("the first entry stored nothing")
	}

	// The file is rewritten, so the mark moves and the entry is replaced, not grown.
	if err := os.WriteFile(filepath.Join(root, rel), []byte("package swap\n\nfunc X() int { return 2 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, ok := markOutlineFile(root, rel)
	if !ok || second == first {
		t.Fatal("the rewrite did not move the mark")
	}
	storeOutlineHash("ws", root, rel, second, time.Now(), "B", strings.Repeat("b", 8192))
	afterSecond, _ := outlineHashUsed()
	if afterSecond <= afterFirst {
		t.Fatalf("a replacement left the total at %d, want the new entry's own bytes counted once over %d", afterSecond, afterFirst)
	}
	// With one entry left, the total must be exactly that entry's own held bytes: an old
	// entry released twice would understate it.
	outlineHashes.Lock()
	entries := len(outlineHashes.byKey)
	var held int64
	for _, e := range outlineHashes.byKey {
		held += e.held
	}
	outlineHashes.Unlock()
	if entries != 1 {
		t.Fatalf("the cache holds %d entries after a replacement, want 1", entries)
	}
	if afterSecond != held {
		t.Fatalf("the total is %d but the entries hold %d: the old bytes were not released exactly once", afterSecond, held)
	}
}

// A mark inside the racy window is neither served nor stored, the way the workspace
// registry treats the same fileMark: on a coarse-timestamp filesystem two same-size writes
// inside one tick share a mark, so such an entry could outlive the edit it describes.
func TestAMarkInsideTheRacyWindowIsNeitherServedNorStored(t *testing.T) {
	resetOutlineCache(t)
	setOutlineTrustWindow(t, time.Hour) // wide open: nothing a test writes is trusted
	f := newInvalidationFixture(t)
	ctx := context.Background()

	first, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Hashed != 1 {
		t.Fatalf("a cold pass hashed %d files, want the one anchored file", first.Hashed)
	}
	if n := outlineHashEntries(); n != 0 {
		t.Fatalf("a file written inside the racy window cached %d entries, want none", n)
	}
	// So the next pass reads it again rather than trusting a mark that cannot identify it.
	second, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Hashed != 1 || second.Checked != 1 {
		t.Fatalf("an untrusted mark: hashed %d, checked %d, want 1 and 1", second.Hashed, second.Checked)
	}
}

// A second pass over an unchanged tree re-hashes no file, and the first one hashed the
// anchored file: the counter is the acceptance.
//
// The cache is cleared after the fixture because the promotion itself baselines the symbol
// anchor and so warms the entry; that is why the cold pass below is measured from a
// cleared cache rather than from the first call after promotion.
func TestSecondPassOnAnUnchangedTreeRehashesNoFile(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
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
// makes a repeated ground free. The fixture is built here rather than through
// newInvalidationFixture because the file has to be aged before the promotion reads it.
func TestThePromotionWarmsTheEntryTheFirstPassWouldHash(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
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

	rep, err := InvalidateMemory(context.Background(), dir, ws, []string{"srv.go"})
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
	trustOutlineCache(t)
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

// The key carries the file's identity, not its path alone. A same-size rewrite is the case
// a size-only key would miss, and the assertion is on the mark itself, so an
// implementation keyed only on size or only on mtime fails here rather than passing by
// accident.
func TestTheCacheKeyCarriesTheFileStatNotThePath(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()
	before, ok := markOutlineFile(f.root, "srv.go")
	if !ok {
		t.Fatal("srv.go has no mark")
	}
	if _, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"}); err != nil {
		t.Fatal(err)
	}

	// A file rewritten in place keeps its path and its size.
	body := strings.Replace(srvSource, "return 8081", "return 7070", 1)
	if len(body) != len(srvSource) {
		t.Fatalf("the fixture edit changed the file size, so it would not prove the key")
	}
	writeRepoFile(t, f.root, "srv.go", body)
	after, ok := markOutlineFile(f.root, "srv.go")
	if !ok {
		t.Fatal("srv.go has no mark after the rewrite")
	}
	if after.size != before.size {
		t.Fatalf("the rewrite changed the size, so it would not prove the key")
	}
	if after == before {
		t.Fatal("a same-size rewrite left the mark unchanged, so the key cannot see it")
	}
	if after.mtimeNs == before.mtimeNs && after.ctimeNs == before.ctimeNs && after.ino == before.ino {
		t.Fatalf("nothing in the mark moved: %+v against %+v", before, after)
	}

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
	trustOutlineCache(t)
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
		storeOutlineHash(f.ws, f.root, rel, mark, time.Now(), "X", strings.Repeat("a", 64))
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

// One entry grows across several stores, and the total the governor sees stays exact: a
// growth that added the whole new size instead of the difference would over-report and
// refuse later entries early.
func TestGrowingOneEntryAcrossStoresKeepsTheTotalExact(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
	root := t.TempDir()
	rel := "many/x.go"
	if err := os.MkdirAll(filepath.Join(root, "many"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), []byte("package many\n\nfunc X() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mark, ok := markOutlineFile(root, rel)
	if !ok {
		t.Fatal("many/x.go has no mark")
	}

	for i := range 5 {
		storeOutlineHash("ws", root, rel, mark, time.Now(), fmt.Sprintf("Sym%d", i), strings.Repeat("a", 1024))
	}
	if n := outlineHashEntries(); n != 1 {
		t.Fatalf("five stores made %d entries, want 1", n)
	}
	total, _ := outlineHashUsed()
	outlineHashes.Lock()
	var held int64
	for _, e := range outlineHashes.byKey {
		held += e.held
	}
	outlineHashes.Unlock()
	if total != held {
		t.Fatalf("after five stores the total is %d but the entry holds %d: the sizes were added, not differenced", total, held)
	}
	// And the entry really does hold all five, so the total is not right by being empty.
	hashes, hit := outlineHashesFor("ws", root, rel, mark, time.Now())
	if !hit || len(hashes) != 5 {
		t.Fatalf("the entry holds %d hashes, want 5", len(hashes))
	}
}

// A replacement is allowed at the entry bound, because it does not add an entry: a cache
// full of entries must still refresh one whose file was edited.
func TestAReplacementIsAllowedAtTheEntryBound(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
	root := t.TempDir()
	body := "package full\n\nfunc X() int { return 1 }\n"
	for i := range outlineHashMaxEntries {
		rel := fmt.Sprintf("full/f%04d/x.go", i)
		if err := os.MkdirAll(filepath.Join(root, "full", fmt.Sprintf("f%04d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		mark, ok := markOutlineFile(root, rel)
		if !ok {
			t.Fatalf("%s has no mark", rel)
		}
		storeOutlineHash("ws", root, rel, mark, time.Now(), "X", strings.Repeat("a", 64))
	}
	if n := outlineHashEntries(); n != outlineHashMaxEntries {
		t.Fatalf("the cache holds %d entries, want it full at %d", n, outlineHashMaxEntries)
	}

	// One of them is edited: its mark moves, so storing is a replacement, not a new entry.
	target := "full/f0000/x.go"
	if err := os.WriteFile(filepath.Join(root, target), []byte("package full\n\nfunc X() int { return 2 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	moved, ok := markOutlineFile(root, target)
	if !ok {
		t.Fatal("the edited file has no mark")
	}
	storeOutlineHash("ws", root, target, moved, time.Now(), "X", strings.Repeat("b", 64))
	if n := outlineHashEntries(); n != outlineHashMaxEntries {
		t.Fatalf("a replacement at the bound left %d entries, want the bound kept at %d", n, outlineHashMaxEntries)
	}
	hashes, hit := outlineHashesFor("ws", root, target, moved, time.Now())
	if !hit || hashes["X"] != strings.Repeat("b", 64) {
		t.Fatalf("the edited file's entry was not refreshed at the bound: %v (hit=%v)", hashes["X"], hit)
	}
}

// The byte ceiling refuses an entry past it, including while other entries occupy the
// budget, and a file whose hashes would exceed it is re-hashed rather than retained.
func TestTheOutlineCacheByteCeilingRefusesAndStillReads(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()

	// Occupy the budget with entries that leave less than one more free, so the refusal
	// below is a refusal against a nearly full cache and not against an empty one.
	const fillEntries = 7
	for i := range fillEntries {
		rel := fmt.Sprintf("fill/f%02d/x.go", i)
		writeRepoFile(t, f.root, rel, "package fill\n\nfunc X() int { return 1 }\n")
		mark, ok := markOutlineFile(f.root, rel)
		if !ok {
			t.Fatalf("%s has no mark", rel)
		}
		storeOutlineHash(f.ws, f.root, rel, mark, time.Now(), "X", strings.Repeat("a", outlineHashMaxHeld/(fillEntries+1)))
	}
	filled, _ := outlineHashUsed()
	if filled == 0 || filled+outlineHashMaxHeld/8 < outlineHashMaxHeld {
		t.Fatalf("the budget stands at %d of %d, want it nearly full", filled, outlineHashMaxHeld)
	}

	// The next entry does not fit in what is left, so it is refused. Its own bytes are
	// within the ceiling on their own; what refuses it is the budget already spent.
	rel := "big/x.go"
	writeRepoFile(t, f.root, rel, "package big\n\nfunc X() int { return 1 }\n")
	mark, ok := markOutlineFile(f.root, rel)
	if !ok {
		t.Fatal("big/x.go has no mark")
	}
	storeOutlineHash(f.ws, f.root, rel, mark, time.Now(), "X", strings.Repeat("b", outlineHashMaxHeld/4))
	if _, cached := outlineHashesFor(f.ws, f.root, rel, mark, time.Now()); cached {
		t.Fatal("an entry past the byte ceiling was cached")
	}
	if used, _ := outlineHashUsed(); used != filled {
		t.Fatalf("a refused entry moved the budget from %d to %d", filled, used)
	}
	if n := outlineHashEntries(); n != fillEntries {
		t.Fatalf("a refused entry left %d entries, want the %d that fit", n, fillEntries)
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
	trustOutlineCache(t)
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
	trustOutlineCache(t)
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
	trustOutlineCache(t)
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
	trustOutlineCache(t)
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
	if hashes, hit := outlineHashesFor(ws, root, "srv.go", mark, time.Now()); !hit || len(hashes) != 2 {
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
	trustOutlineCache(t)
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
			if hashes, hit := outlineHashesFor(ws, root, "srv.go", mark, time.Now()); hit {
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
			mark, ok := markOutlineFile(root, "srv.go")
			if !ok {
				return
			}
			for range 16 {
				sess := newRehashSession(context.Background(), root, ws)
				sess.dataDir = dir
				sess.storeBody("srv.go", mark, time.Now(), fmt.Sprintf("Extra%02d", i), strings.Repeat("c", 64))
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
	hashes, hit := outlineHashesFor(ws, root, "srv.go", mark, time.Now())
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

// A file removed between the two marks a hit is judged on is a miss, not a hit: the cached
// hash must never be served for a file that is gone. The mark hook removes the file on the
// third read, which is the window between the lookup and the hit's confirmation.
func TestAFileRemovedBeforeTheSecondMarkIsAMiss(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
	f := newInvalidationFixture(t)
	clearOutlineHashes()
	ctx := context.Background()
	if _, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"}); err != nil {
		t.Fatal(err)
	}
	if n := outlineHashEntries(); n == 0 {
		t.Fatal("the first pass cached nothing, so the removal has nothing to serve")
	}

	orig := markOutlineFile
	t.Cleanup(func() { markOutlineFile = orig })
	// The second read is the one that confirms a hit, so the file is removed there: the
	// entry was found, and by the time it would be served the file is gone.
	marks, remove := 0, true
	markOutlineFile = func(root, rel string) (fileMark, bool) {
		marks++
		if remove && marks >= 2 {
			remove = false
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				t.Fatal(err)
			}
		}
		return orig(root, rel)
	}
	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	markOutlineFile = orig
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Marked) != 1 {
		t.Fatalf("a file removed mid-pass marked %v, want the one memory: the cached hash was served as current", rep.Marked)
	}
}

// A file that is gone stays missing across a warm cache: the cache holds hashes, never a
// verdict, so it cannot turn a missing file into an unchanged one.
func TestACachedHashNeverBecomesAMissingVerdict(t *testing.T) {
	resetOutlineCache(t)
	trustOutlineCache(t)
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

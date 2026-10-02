package workspaceops

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// WS-28B: a quoted-code anchor follows its code when the lines above it move.

const quoteSource = "package srv\n\nconst (\n\tReadTimeout = 5\n\tWriteTimeout = 10\n)\n\nfunc Serve() {}\n"

const quoteMemory = "The timeouts are set here:\n" +
	"```go\nReadTimeout = 5\nWriteTimeout = 10\n```\n"

// codeFixture promotes a memory quoting the const block above, with the declared path srv.go.
type codeFixture struct {
	dir, ws, root string
	git           func(args ...string) string
	memory        *ContextEntry
}

// newCodeFixture promotes a memory quoting the const block above and declaring srv.go,
// the file quoteAnchors places the block in (the placement only looks in the author's
// declared paths and the paths the text names, and this text names none). The declared
// path carries WS-28's whole-file baseline, so every test that changes srv.go also marks
// the memory by that path; a test about what the code anchor itself decided re-baselines
// the path first, so the one mark under test is the code anchor's.
func newCodeFixture(t *testing.T) codeFixture {
	t.Helper()
	dir, ws, root, git := reviewRepo(t)
	writeRepoFile(t, root, "srv.go", quoteSource)
	git("add", ".")
	git("commit", "-q", "-m", "srv")
	e, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "timeouts", Content: quoteMemory, Permission: "readwrite", Paths: []string{"srv.go"}}},
		ContextActor{ID: "author"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	e, err = VerifyContext(dir, ws, e.ID, "peer-1", true, "")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	e, err = VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: "approver", Kind: "agent", Verifier: true}, VerifyRequest{})
	if err != nil || !e.Promoted {
		t.Fatalf("not promoted: %+v, %v", e, err)
	}
	return codeFixture{dir: dir, ws: ws, root: root, git: git, memory: e}
}

// rebaselinePath re-snapshots the declared path's baseline after the fixture wrote its
// file, so the next change marks the memory by the anchor under test and not by the path
// anchor every edit to a declared file reaches.
func (f codeFixture) rebaselinePath(t *testing.T) {
	t.Helper()
	f.rebaselinePathFor(t, f.memory.ID)
}

// rebaselinePathFor is rebaselinePath for a second entry in the same workspace.
func (f codeFixture) rebaselinePathFor(t *testing.T, entryID string) {
	t.Helper()
	actor := invalidationActor(f.root)
	if err := memoryUpdate(context.Background(), f.dir, f.ws, func(tx govstore.Tx) error {
		return tx.SetBaselines(context.Background(), entryID,
			pathBaselines(capturePathHashes(f.root, []string{"srv.go"})), actor.HeadSHA, actor)
	}); err != nil {
		t.Fatal(err)
	}
	if err := memoryUpdate(context.Background(), f.dir, f.ws, func(tx govstore.Tx) error {
		return tx.ClearDrift(context.Background(), entryID, actor)
	}); err != nil {
		t.Fatal(err)
	}
}

// codeAnchor returns the memory's one code anchor, or fails: the fixture is only useful
// when the quote was placed, and a test that quietly read no anchors would pass by
// asserting on an empty list.
func (f codeFixture) codeAnchor(t *testing.T) govstore.Anchor {
	t.Helper()
	var got []govstore.Anchor
	err := memoryView(context.Background(), f.dir, f.ws, func(r govstore.Reader) (err error) {
		got, err = r.ListAnchors(context.Background(), f.memory.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var out []govstore.Anchor
	for _, a := range got {
		if a.Kind == govstore.AnchorCode {
			out = append(out, a)
		}
	}
	if len(out) != 1 {
		t.Fatalf("want one code anchor, got %d of %+v", len(out), got)
	}
	return out[0]
}

func (f codeFixture) entry(t *testing.T) govstore.Entry {
	t.Helper()
	var e govstore.Entry
	err := memoryView(context.Background(), f.dir, f.ws, func(r govstore.Reader) (err error) {
		e, err = r.GetEntry(context.Background(), f.memory.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// An insert above the quoted block shifts its lines. The anchor follows the code to the
// new lines and the memory is not marked: it still points at what it quoted.
func TestCodeAnchorFollowsLinesShiftedByAnEditAbove(t *testing.T) {
	f := newCodeFixture(t)
	if got := f.codeAnchor(t).Value; got != "srv.go#L4-L5" {
		t.Fatalf("the quote was not placed on srv.go#L4-L5: %q", got)
	}
	ctx := context.Background()

	writeRepoFile(t, f.root, "srv.go", "package srv\n// a new line above the block\n"+quoteSource[len("package srv\n"):])
	f.rebaselinePath(t)
	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Marked) != 0 {
		t.Fatalf("a block that moved marked the memory: %+v", rep)
	}
	if rep.Reanchored != 1 {
		t.Fatalf("the block that moved was not counted: %+v", rep)
	}
	if got := f.codeAnchor(t).Value; got != "srv.go#L5-L6" {
		t.Fatalf("the anchor did not follow the code: %q", got)
	}
	if e := f.entry(t); e.NeedsReverify || e.StaleSince != "" {
		t.Fatalf("a block that moved put the memory at risk: %+v", e)
	}
	if got := f.codeAnchor(t).StaleSince; got != "" {
		t.Fatalf("a block that moved marked its anchor: %q", got)
	}

	// the move is in the history, once, with both ranges
	var events []govstore.Event
	if err := memoryView(ctx, f.dir, f.ws, func(r govstore.Reader) (err error) {
		events, err = r.ListEvents(ctx, govstore.EventFilter{WorkspaceID: f.ws, EntryID: f.memory.ID,
			Types: []string{govstore.EventAnchorReanchor}})
		return err
	}); err != nil || len(events) != 1 {
		t.Fatalf("anchor_reanchor events %v, %v", events, err)
	}
	var data struct {
		Commit string
		From   [2]int
		To     [2]int
	}
	if err := json.Unmarshal(events[0].Data, &data); err != nil || data.Commit != f.git("rev-parse", "HEAD") ||
		data.From != [2]int{4, 5} || data.To != [2]int{5, 6} {
		t.Fatalf("event data %s (want 4-5 -> 5-6 at HEAD): %v", events[0].Data, err)
	}

	// once moved, a second look records nothing more
	if rep, _ = InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"}); len(rep.Marked) != 0 {
		t.Fatalf("marked again: %+v", rep)
	}
}

// An insert below the quoted block leaves it where it was: the code did not move, so
// neither the anchor nor the memory's state may change.
func TestCodeAnchorStaysWhenTheEditIsBelowIt(t *testing.T) {
	f := newCodeFixture(t)
	writeRepoFile(t, f.root, "srv.go", strings.Replace(quoteSource, "func Serve() {}", "func Serve() {}\nfunc Extra() {}", 1))
	f.rebaselinePath(t)

	rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"})
	if err != nil || len(rep.Marked) != 0 || rep.Reanchored != 0 {
		t.Fatalf("an edit below the block: %+v, %v", rep, err)
	}
	if got := f.codeAnchor(t).Value; got != "srv.go#L4-L5" {
		t.Fatalf("an unmoved block was moved: %q", got)
	}
	if e := f.entry(t); e.NeedsReverify {
		t.Fatalf("an edit below the block marked the memory: %+v", e)
	}
}

// The quoted code deleted from the file is drift: the memory is at risk, the stale_observed
// event names the anchor and the commit, and a second look adds nothing.
func TestCodeAnchorDeletedMarksMemoryAtRisk(t *testing.T) {
	f := newCodeFixture(t)
	ctx := context.Background()
	writeRepoFile(t, f.root, "srv.go", "package srv\n\nfunc Serve() {}\n")
	head := f.git("rev-parse", "HEAD")
	f.rebaselinePath(t)

	rep, err := InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"})
	if err != nil || !slices.Equal(rep.Marked, []string{f.memory.ID}) {
		t.Fatalf("the deleted quote: %+v, %v", rep, err)
	}
	if e := f.entry(t); !e.NeedsReverify || e.StaleSince == "" {
		t.Fatalf("the deleted quote did not put the memory at risk: %+v", e)
	}
	var events []govstore.Event
	if err := memoryView(ctx, f.dir, f.ws, func(r govstore.Reader) (err error) {
		events, err = r.ListEvents(ctx, govstore.EventFilter{WorkspaceID: f.ws, EntryID: f.memory.ID,
			Types: []string{govstore.EventStaleObserved}})
		return err
	}); err != nil || len(events) != 1 {
		t.Fatalf("stale_observed events: want exactly 1, got %d (%v)", len(events), err)
	}
	var data struct {
		Commit  string
		Anchors []govstore.AnchorRef
	}
	if err := json.Unmarshal(events[0].Data, &data); err != nil || data.Commit != head ||
		!slices.Equal(data.Anchors, []govstore.AnchorRef{{Kind: govstore.AnchorCode, Value: "srv.go#L4-L5"}}) {
		t.Fatalf("event data %s (want commit %s): %v", events[0].Data, head, err)
	}
	if rep, _ = InvalidateMemory(ctx, f.dir, f.ws, []string{"srv.go"}); len(rep.Marked) != 0 {
		t.Fatalf("marked again: %+v", rep)
	}
}

// The quoted code rewritten in place no longer matches, so it is drift rather than a
// move: an anchor that stayed on the old lines would read as anchored to a block that is
// no longer there.
func TestCodeAnchorRewrittenInPlaceIsDriftNotAMove(t *testing.T) {
	f := newCodeFixture(t)
	writeRepoFile(t, f.root, "srv.go", strings.Replace(quoteSource, "ReadTimeout = 5", "ReadTimeout = 30", 1))
	f.rebaselinePath(t)

	rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"})
	if err != nil || !slices.Equal(rep.Marked, []string{f.memory.ID}) || rep.Reanchored != 0 {
		t.Fatalf("the rewritten quote: %+v, %v", rep, err)
	}
	if got := f.codeAnchor(t).Value; got != "srv.go#L4-L5" {
		t.Fatalf("a rewritten block was moved instead of marked: %q", got)
	}
}

// A block whose copy now sits above the anchor's own lines and whose original lines hold
// something else cannot be told from the one the anchor names: Reanchor returns an
// unanchored, ambiguous answer, the anchor stays and the memory is not marked. An edit
// elsewhere in the file put no memory's code at risk, and marking an ambiguous anchor
// would claim otherwise.
func TestCodeAnchorAmbiguousAfterTheEditIsUnknown(t *testing.T) {
	f := newCodeFixture(t)
	// The block appears twice: once at lines the anchor does not name, once where the
	// anchor's own lines now hold other code. Neither is the anchor's range, so the match
	// at the anchor's StartLine cannot decide it.
	dup := "package srv\n\nfunc Serve() {}\n\nconst (\n\tReadTimeout = 5\n\tWriteTimeout = 10\n)\n"
	writeRepoFile(t, f.root, "srv.go", dup+"\n"+dup)
	f.rebaselinePath(t)

	rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"})
	if err != nil || len(rep.Marked) != 0 || rep.Reanchored != 0 {
		t.Fatalf("an ambiguous quote: %+v, %v", rep, err)
	}
	if got := f.codeAnchor(t).Value; got != "srv.go#L4-L5" {
		t.Fatalf("an ambiguous quote was moved: %q", got)
	}
	if e := f.entry(t); e.NeedsReverify {
		t.Fatalf("an ambiguous quote marked the memory: %+v", e)
	}
}

// A memory quoting a block in each of two files re-anchors the one in the changed file,
// even though it is the second block of its content. AnchorsInPaths returns only the
// anchors lying in the changed paths, so a pass that paired that anchor with the first
// block of the content would compare it against the wrong snippet, find nothing it could
// move, and leave a moved block on its old lines.
func TestCodeAnchorFollowsItsBlockInTheSecondFile(t *testing.T) {
	f := newCodeFixture(t)
	const retrySource = "package retry\n\nconst (\n\tMaxRetries = 3\n)\n"
	const retry = "and the retry bound lives in retry.go:\n\n```go\nMaxRetries = 3\n```\n"
	writeRepoFile(t, f.root, "retry.go", retrySource)
	e, err := Remember(f.dir, f.ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "timeouts", Content: quoteMemory + retry, Permission: "readwrite", Paths: []string{"srv.go", "retry.go"}}},
		ContextActor{ID: "author"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	e, err = VerifyContext(f.dir, f.ws, e.ID, "peer-1", true, "")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if e, err = VerifyContextOutcome(f.dir, f.ws, e.ID, ContextActor{ID: "approver2", Kind: "agent", Verifier: true},
		VerifyRequest{}); err != nil || !e.Promoted {
		t.Fatalf("not promoted: %+v, %v", e, err)
	}
	var code []govstore.Anchor
	if err := memoryView(context.Background(), f.dir, f.ws, func(r govstore.Reader) (err error) {
		var anchors []govstore.Anchor
		if anchors, err = r.ListAnchors(context.Background(), e.ID); err != nil {
			return err
		}
		for _, a := range anchors {
			if a.Kind == govstore.AnchorCode {
				code = append(code, a)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(code) != 2 {
		t.Fatalf("want two code anchors, got %d of %+v", len(code), code)
	}
	var retryAnchor govstore.Anchor
	for _, a := range code {
		if strings.HasPrefix(a.Value, "retry.go") {
			retryAnchor = a
		}
	}
	if retryAnchor.Value != "retry.go#L4-L4" {
		t.Fatalf("the second block was not placed: %+v", code)
	}
	writeRepoFile(t, f.root, "retry.go", "// a new line above the block\n"+retrySource)
	f.rebaselinePathFor(t, e.ID)

	rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"retry.go"})
	// The declared path retry.go carries WS-28's whole-file baseline, so the edit marks the
	// memory by that path as well. What this test is about is the code anchor, so the
	// assertion is the move and the anchor's new lines, not the absence of a mark.
	if err != nil || rep.Reanchored != 1 {
		t.Fatalf("a block that moved in the second file: %+v, %v", rep, err)
	}
	var after []govstore.Anchor
	if err := memoryView(context.Background(), f.dir, f.ws, func(r govstore.Reader) (err error) {
		after, err = r.ListAnchors(context.Background(), e.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, a := range after {
		if a.Kind == govstore.AnchorCode && strings.HasPrefix(a.Value, "retry.go") && a.Value != "retry.go#L5-L5" {
			t.Fatalf("the block in the second file did not follow its code: %q", a.Value)
		}
	}
}

// A memory quoting more blocks than the placement bound re-anchors only the ones it
// placed: the surplus is unknown rather than re-found, so a change to the fifth block
// marks nothing and moves nothing.
func TestCodeAnchorBeyondThePlacementBoundIsUnknown(t *testing.T) {
	f := newCodeFixture(t)
	// One more block than maxQuotedBlocks (4). The file holds all of them, so every block
	// except the surplus is placed and the anchor set is what the bound leaves behind.
	var src, text strings.Builder
	for i := range maxQuotedBlocks + 1 {
		src.WriteString("const (\n\tLimit" + string(rune('a'+i)) + " = " + string(rune('1'+i)) + "\n)\n\n")
		text.WriteString("and bound " + string(rune('A'+i)) + ":\n\n```go\nLimit" + string(rune('a'+i)) +
			" = " + string(rune('1'+i)) + "\n```\n\n")
	}
	file := "package srv\n\n" + src.String() + "func Serve() {}\n"
	writeRepoFile(t, f.root, "srv.go", file)
	e, err := Remember(f.dir, f.ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "bounds", Content: text.String(), Permission: "readwrite", Paths: []string{"srv.go"}}},
		ContextActor{ID: "author"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	e, err = VerifyContext(f.dir, f.ws, e.ID, "peer-1", true, "")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if e, err = VerifyContextOutcome(f.dir, f.ws, e.ID, ContextActor{ID: "approver2", Kind: "agent", Verifier: true},
		VerifyRequest{}); err != nil || !e.Promoted {
		t.Fatalf("not promoted: %+v, %v", e, err)
	}
	var anchors []govstore.Anchor
	if err := memoryView(context.Background(), f.dir, f.ws, func(r govstore.Reader) (err error) {
		anchors, err = r.ListAnchors(context.Background(), e.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	placed := 0
	for _, a := range anchors {
		if a.Kind == govstore.AnchorCode {
			placed++
		}
	}
	if placed != maxQuotedBlocks {
		t.Fatalf("the placement placed %d code anchors, want the bound %d (%+v)", placed, maxQuotedBlocks, anchors)
	}
	// Remove the surplus block's text from the file: only an implementation that re-anchors
	// past the bound would try to place it, and would then mark the memory.
	surplus := "Limit" + string(rune('a'+maxQuotedBlocks)) + " = " + string(rune('1'+maxQuotedBlocks))
	writeRepoFile(t, f.root, "srv.go", strings.Replace(file, surplus+"\n", "", 1))
	f.rebaselinePathFor(t, e.ID)

	rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(rep.Marked, e.ID) {
		t.Fatalf("a block past the placement bound marked the memory: %+v", rep)
	}
}

// A served content holding fewer fenced blocks than the entry has code anchors is
// unknown, and the anchors past the last block are left alone rather than crashing the
// invalidation. This is the direction the positional pairing could not survive: it
// indexed the Nth block for the Nth anchor, so the second anchor of a one-block content
// read past the end. Both channels that raised it saw the index, not the behaviour, so
// the case is asserted here through the call that would have panicked.
func TestCodeAnchorBeyondTheContentBlocksIsUnknown(t *testing.T) {
	f := newCodeFixture(t)
	const retrySource = "\nconst (\n\tReadTimeout = 1\n\tWriteTimeout = 2\n)\n"
	const retry = "and the retry bounds:\n\n```go\nReadTimeout = 1\nWriteTimeout = 2\n```\n"
	writeRepoFile(t, f.root, "srv.go", strings.Replace(quoteSource, "func Serve() {}", retrySource+"func Serve() {}", 1))
	e, err := Remember(f.dir, f.ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "timeouts", Content: quoteMemory + retry, Permission: "readwrite", Paths: []string{"srv.go"}}},
		ContextActor{ID: "author"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	e, err = VerifyContext(f.dir, f.ws, e.ID, "peer-1", true, "")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if e, err = VerifyContextOutcome(f.dir, f.ws, e.ID, ContextActor{ID: "approver2", Kind: "agent", Verifier: true},
		VerifyRequest{}); err != nil || !e.Promoted {
		t.Fatalf("not promoted: %+v, %v", e, err)
	}
	var anchors []govstore.Anchor
	if err := memoryView(context.Background(), f.dir, f.ws, func(r govstore.Reader) (err error) {
		anchors, err = r.ListAnchors(context.Background(), e.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	code := 0
	for _, a := range anchors {
		if a.Kind == govstore.AnchorCode {
			code++
		}
	}
	if code != 2 {
		t.Fatalf("want two code anchors, got %d of %+v", code, anchors)
	}
	// The memory's served content keeps one block while both anchors stand, as it would if
	// an accepted edit dropped a fenced block without replacing the anchor set.
	sites := codeSites(quoteMemory, anchors)
	if len(sites) != 1 {
		t.Fatalf("one block in the content should pair one anchor, got %d", len(sites))
	}

	rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"})
	if err != nil {
		t.Fatalf("the invalidation panicked or failed on fewer blocks than anchors: %+v, %v", rep, err)
	}
}

// A code anchor whose snippet digest names no block of the memory's served content is
// unknown and marks nothing: the digest is the whole identity of the anchor, so a block
// that is not there cannot be re-found, and a block that is there under a different
// digest must not be paired with this anchor by position.
func TestCodeAnchorWithoutServedContentIsUnknown(t *testing.T) {
	f := newCodeFixture(t)
	if err := memoryUpdate(context.Background(), f.dir, f.ws, func(tx govstore.Tx) error {
		return tx.ReplaceAnchors(context.Background(), f.memory.ID, []string{"srv.go"},
			[]govstore.AnchorInput{{Kind: govstore.AnchorCode, Value: "srv.go#L4-L5", SymbolUID: "snippet:deadbeef"}},
			govstore.Actor{Principal: "tester", Owner: "tester", Kind: "agent"})
	}); err != nil {
		t.Fatal(err)
	}
	f.rebaselinePath(t)

	rep, err := InvalidateMemory(context.Background(), f.dir, f.ws, []string{"srv.go"})
	if err != nil || len(rep.Marked) != 0 {
		t.Fatalf("a code anchor with no matching snippet: %+v, %v", rep, err)
	}
}

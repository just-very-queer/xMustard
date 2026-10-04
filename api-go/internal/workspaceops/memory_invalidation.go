package workspaceops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"xmustard/api-go/internal/gitdiff"
	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
)

// Change-driven invalidation (WS-28, PAR-PROV-03). A change to the repository (the
// working tree ground sees, a HEAD move, the watcher's batch of paths) is looked up in
// the store's anchor index; only the anchors it lands on are re-hashed, and only against
// the baseline they were verified with: a whole file for a path anchor, the lines of the
// declaration for a symbol anchor (baseline kind symbol_body), so an edit elsewhere in
// the same file does not touch a memory anchored to one symbol. A mismatch persists a
// stale_observed event (commit and anchors) and sets needs_reverify on the memory; ground
// lists those memories as at_risk_memories, and recall labels them stale (its own drift
// check stays as the backstop). A distinct verifier's approval re-baselines the memory
// and clears the flag. What cannot be decided (the code index is unavailable) is never
// marked: unknown is not stale.

// Bounds: paths and anchors one invalidation reads, the file a symbol body is read
// from, the memories ground lists, and the time one invalidation may take.
const (
	maxInvalidationPaths   = 500
	maxInvalidationAnchors = 4000
	maxSymbolFileBytes     = 2 << 20
	maxAtRisk              = 20
	invalidationTimeout    = 15 * time.Second
	// baselineFileKind and baselineSymbolBody name what an anchor's baseline hashes.
	baselineFileKind   = "file"
	baselineSymbolBody = "symbol_body"
	// invalidator is the principal that records what a change did to memory.
	invalidator = "xmustard-invalidation"
)

// InvalidationReport is what one invalidation looked at and marked.
type InvalidationReport struct {
	Paths   int `json:"paths"`
	Checked int `json:"anchors_checked"`
	// Reanchored counts the quoted-code anchors that followed their code to new lines
	// (WS-28B). They are not drift and not marked: the memory still points at what it
	// quoted, one range over.
	Reanchored int `json:"anchors_reanchored,omitempty"`
	// Unknown counts the quoted-code anchors that could not be checked at all: no served
	// content to search for, a block past the placement bound, or an anchor whose digest
	// names no block. They are neither moved nor marked, and a caller reading an empty
	// answer needs to know they were passed over.
	Unknown int      `json:"anchors_unknown,omitempty"`
	Marked  []string `json:"marked"`
	// Truncated: more than maxInvalidationAnchors anchors lay in the paths; the rest were not checked.
	Truncated bool `json:"truncated,omitempty"`
}

// anchorState is what an anchor hashes to now, in the baseline's own terms.
type anchorState struct{ state, hash string }

var missingState = anchorState{state: govstore.BaselineMissing}

// anchorRehashers re-hash an anchor of one kind against the tree; false is unknown.
// A kind without one (identifiers, commands, quoted code) has no baseline to drift from.
var anchorRehashers = map[string]struct {
	baseline string
	current  func(*rehashSession, govstore.Anchor) (anchorState, bool)
	// atPromotion: the baseline is captured by the promotion itself, not afterwards.
	atPromotion bool
}{
	govstore.AnchorPath:   {baselineFileKind, (*rehashSession).path, true},
	govstore.AnchorSymbol: {baselineSymbolBody, (*rehashSession).symbol, false},
}

// rehashSession caches the files and outlines one invalidation reads. held is what those
// files cost together: an invalidation may touch up to maxInvalidationPaths paths, each of
// at most maxSymbolFileBytes, and a cache without an aggregate bound could retain all of
// them for the pass. Past maxInvalidationCacheBytes the session stops caching and reads
// each file once per anchor that needs it, which is slower and bounded.
type rehashSession struct {
	ctx               context.Context
	root, workspaceID string
	dataDir           string
	files             map[string]fileRead
	held              int
	outlines          map[string]outlineRead
}

// maxInvalidationCacheBytes bounds what one invalidation's file cache retains. It is below
// the process-tree line by a wide margin and above the working set of a normal pass, whose
// files are far smaller than maxSymbolFileBytes.
const maxInvalidationCacheBytes = 16 << 20

func newRehashSession(ctx context.Context, root, workspaceID string) *rehashSession {
	return &rehashSession{ctx: ctx, root: root, workspaceID: workspaceID, files: map[string]fileRead{}, outlines: map[string]outlineRead{}}
}

// path hashes a file's content; a file that is missing or unreadable is missing.
func (s *rehashSession) path(a govstore.Anchor) (anchorState, bool) {
	if h, ok := hashFileContent(s.root, a.Value); ok {
		return anchorState{govstore.BaselineHash, h}, true
	}
	return missingState, true
}

// fileRead is one read of a file: its text, or why there is none. A file that is absent
// is missing; one too large or unreadable is unknown, not missing.
type fileRead struct {
	text    string
	ok      bool
	missing bool
}

// text reads a file once. Past maxInvalidationCacheBytes the file is read and handed back
// without being retained, so one pass cannot hold every file a large change touches; the
// cost is one read per anchor that needs the same file.
func (s *rehashSession) text(rel string) fileRead {
	if r, ok := s.files[rel]; ok {
		return r
	}
	r := fileRead{}
	if r.text, r.ok = readWorkspaceText(s.root, rel, maxSymbolFileBytes); !r.ok {
		f, err := openWorkspaceFileBeneath(s.root, rel)
		if err == nil {
			f.Close()
		}
		r.missing = errors.Is(err, fs.ErrNotExist)
	}
	if s.held+len(r.text) > maxInvalidationCacheBytes {
		return r
	}
	s.held += len(r.text)
	s.files[rel] = r
	return r
}

// symbol hashes the lines of the declaration a symbol anchor names in the file its uid
// gives. A symbol that no longer exists in a complete outline, or a file that is gone,
// is missing; an outline the code index could not give or gave in part, or a file too
// large or unreadable, is unknown.
func (s *rehashSession) symbol(a govstore.Anchor) (anchorState, bool) {
	file, _, ok := strings.Cut(a.SymbolUID, "#")
	if !ok || file == "" {
		return anchorState{}, false
	}
	src := s.text(file)
	if !src.ok {
		return missingState, src.missing
	}
	outline, ok := s.outlines[file]
	if !ok {
		spans, err := symbolOutline(s.ctx, s.root, s.workspaceID, file)
		outline = outlineRead{spans: spans, incomplete: errors.Is(err, errOutlineIncomplete)}
		if err != nil && !outline.incomplete {
			return anchorState{}, false
		}
		s.outlines[file] = outline
	}
	body, found := outline.spans.body(src.text, a.Value)
	if !found {
		return missingState, !outline.incomplete
	}
	sum := sha256.Sum256([]byte(body))
	return anchorState{govstore.BaselineHash, hex.EncodeToString(sum[:])}, true
}

// outlineRead is a file's outline as read; incomplete when the code index cut it short.
type outlineRead struct {
	spans      symbolSpans
	incomplete bool
}

// errOutlineIncomplete is returned with the spans that were read when the index cut the
// outline short: a name missing from it is then unknown, not gone.
var errOutlineIncomplete = errors.New("the code index cut the outline short")

// symbolSpan is one declaration of a file: its name path and 1-based line range.
type symbolSpan struct {
	Name       string
	Start, End int
}

type symbolSpans []symbolSpan

// body is the text of the declarations named by name (a name, or a dotted or slashed name
// path, matched against the end of the outline's name paths), joined in source order.
func (spans symbolSpans) body(text, name string) (string, bool) {
	want := normalizeNamePath(name)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var parts []string
	for _, sp := range spans {
		have := normalizeNamePath(sp.Name)
		if have != want && !strings.HasSuffix(have, "."+want) {
			continue
		}
		if sp.Start >= 1 && sp.End >= sp.Start && sp.Start <= len(lines) {
			parts = append(parts, strings.Join(lines[sp.Start-1:min(sp.End, len(lines))], "\n"))
		}
	}
	return strings.Join(parts, "\n"), len(parts) > 0
}

// normalizeNamePath folds the index's "/A/b" and the anchors' "A.b" onto "A.b".
func normalizeNamePath(p string) string {
	return strings.ReplaceAll(strings.Trim(strings.TrimSpace(p), "/"), "/", ".")
}

// symbolOutline reads a file's declarations (containers' members included) from the code
// index; tests replace it.
var symbolOutline = symbolOutlineWithCore

type outlineNode struct {
	NamePath string        `json:"name_path"`
	Lines    [2]int        `json:"lines"`
	Children []outlineNode `json:"children"`
}

func symbolOutlineWithCore(ctx context.Context, root, workspaceID, rel string) (symbolSpans, error) {
	raw, err := overviewOf(ctx, root, workspaceID, rel, 3)
	if err != nil {
		return nil, err
	}
	var res struct {
		Kind      string        `json:"kind"`
		Symbols   []outlineNode `json:"symbols"`
		Truncated bool          `json:"symbols_truncated"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	if res.Kind != "file" {
		return nil, fmt.Errorf("%s is not in the code index", rel)
	}
	var out symbolSpans
	var walk func([]outlineNode)
	walk = func(nodes []outlineNode) {
		for _, n := range nodes {
			out = append(out, symbolSpan{Name: n.NamePath, Start: n.Lines[0], End: n.Lines[1]})
			walk(n.Children)
		}
	}
	walk(res.Symbols)
	if res.Truncated {
		return out, errOutlineIncomplete
	}
	return out, nil
}

// drifted reports whether the anchor now hashes to something else than its baseline.
func drifted(a govstore.Anchor, cur anchorState) bool {
	return cur.state != a.BaselineState || cur.hash != a.BaselineHash
}

// invalidationActor is the identity of an invalidation write, at the repository's HEAD.
func invalidationActor(root string) govstore.Actor {
	sha, branch := gitHead(root)
	return govstore.Actor{Principal: invalidator, Owner: invalidator, Kind: "system", HeadSHA: sha, Branch: branch}
}

// InvalidateMemory looks the changed repo-relative paths up in the anchor index and
// marks the served memories whose baselined anchors no longer hash as they did.
func InvalidateMemory(ctx context.Context, dataDir, workspaceID string, changed []string) (InvalidationReport, error) {
	rep := InvalidationReport{Marked: []string{}}
	paths := changedAnchorPaths(changed)
	if rep.Paths = len(paths); len(paths) == 0 {
		return rep, nil
	}
	root := contextRoot(dataDir, workspaceID)
	if root == "" {
		return rep, nil
	}
	ctx, cancel := context.WithTimeout(ctx, invalidationTimeout)
	defer cancel()
	var anchors []govstore.Anchor
	if err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) (err error) {
		anchors, err = r.AnchorsInPaths(ctx, workspaceID, paths, maxInvalidationAnchors+1)
		return err
	}); err != nil {
		return rep, err
	}
	if rep.Truncated = len(anchors) > maxInvalidationAnchors; rep.Truncated {
		anchors = anchors[:maxInvalidationAnchors]
	}
	session := newRehashSession(ctx, root, workspaceID)
	session.dataDir = dataDir
	hit := map[string][]govstore.AnchorRef{}
	for _, a := range anchors {
		rehash, ok := anchorRehashers[a.Kind]
		if !ok || a.BaselineState == govstore.BaselineNone || a.StaleSince != "" {
			continue
		}
		cur, known := rehash.current(session, a)
		if rep.Checked++; known && drifted(a, cur) {
			hit[a.EntryID] = append(hit[a.EntryID], govstore.AnchorRef{Kind: a.Kind, Value: a.Value})
		}
	}
	// A quoted-code anchor has no baseline: its lines shift with every edit above them,
	// so it is re-found and either followed or marked (WS-28B). The pass reports the
	// entries it marked from the store's own commit, so a rolled-back write is returned
	// as an error rather than as a marked memory, and its ids are merged with the ones
	// the baseline pass finds: an entry whose declared path and whose quote both reached
	// the same change is at risk once, not twice.
	reanchored, codeMarked, unknown, codeErr := reanchorCode(ctx, session, anchors)
	rep.Reanchored = reanchored
	rep.Unknown = unknown
	for id := range codeMarked {
		hit[id] = nil
	}
	if len(hit) == 0 {
		return rep, codeErr
	}
	actor := invalidationActor(root)
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		for _, id := range slices.Sorted(maps.Keys(hit)) {
			n, err := tx.MarkStale(ctx, id, hit[id], actor.HeadSHA, actor)
			if err != nil {
				return err
			}
			if n > 0 {
				rep.Marked = append(rep.Marked, id)
			}
		}
		return nil
	})
	for _, id := range slices.Sorted(maps.Keys(codeMarked)) {
		if !slices.Contains(rep.Marked, id) {
			rep.Marked = append(rep.Marked, id)
		}
	}
	slices.Sort(rep.Marked)
	return rep, errors.Join(codeErr, err)
}

// changedAnchorPaths cleans a change list into the paths the anchor index is asked
// about: repo-relative, confined, without duplicates, at most maxInvalidationPaths.
func changedAnchorPaths(changed []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range govstore.CleanPaths(changed) {
		clean, err := ConfineWorkspacePath("", p)
		if err != nil || seen[clean] {
			continue
		}
		seen[clean] = true
		if out = append(out, clean); len(out) == maxInvalidationPaths {
			break
		}
	}
	return out
}

// baselineSymbolAnchors captures the verified-against body hash of an entry's symbol
// anchors, after a promotion or a re-verification committed. The code index is read
// outside the store's write transaction; a symbol it cannot read keeps no baseline (and
// so cannot drift), which the next verification retries.
func baselineSymbolAnchors(dataDir, workspaceID, entryID string) {
	ctx, cancel := context.WithTimeout(context.Background(), invalidationTimeout)
	defer cancel()
	root := contextRoot(dataDir, workspaceID)
	if root == "" {
		return
	}
	var anchors []govstore.Anchor
	if memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) (err error) {
		anchors, err = r.ListAnchors(ctx, entryID)
		return err
	}) != nil {
		return
	}
	session := newRehashSession(ctx, root, workspaceID)
	var baselines []govstore.Baseline
	for _, a := range anchors {
		rehash, ok := anchorRehashers[a.Kind]
		if !ok || rehash.atPromotion {
			continue
		}
		if cur, known := rehash.current(session, a); known {
			baselines = append(baselines, govstore.Baseline{Kind: a.Kind, Value: a.Value, State: cur.state, Hash: cur.hash,
				BaselineKind: rehash.baseline})
		}
	}
	if len(baselines) == 0 {
		return
	}
	actor := invalidationActor(root)
	_ = memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		return tx.SetBaselines(ctx, entryID, baselines, actor.HeadSHA, actor)
	})
}

// afterBaseline is what follows a committed promotion or re-verification: the paths'
// feedback boost and the symbol anchors' baselines.
func afterBaseline(dataDir, workspaceID string, e ContextEntry) {
	recordVerifyFeedback(dataDir, workspaceID, e.ID, e.Paths)
	baselineSymbolAnchors(dataDir, workspaceID, e.ID)
}

// reverifyOnApprove re-baselines a drifted memory when a principal other than its author
// approves its served revision, and clears its drift flags: the approval is a fresh
// look at the code as it is now. The author's own approval, or an open-mode one (the
// identity is shared), does not count. It reports whether it cleared the memory, so the
// caller re-baselines the symbol anchors once the transaction commits.
func reverifyOnApprove(ctx context.Context, tx govstore.Tx, root string, e govstore.Entry, ce ContextEntry,
	voter ContextActor, actor govstore.Actor) (bool, error) {
	if !e.NeedsReverify || voter.OpenMode || strings.EqualFold(strings.TrimSpace(ce.Source), strings.TrimSpace(voter.ID)) {
		return false, nil
	}
	if err := tx.SetBaselines(ctx, e.ID, pathBaselines(capturePathHashes(root, ce.Paths)), actor.HeadSHA, actor); err != nil {
		return false, err
	}
	return true, tx.ClearDrift(ctx, e.ID, actor)
}

// --- head changes -----------------------------------------------------------------

// seenHeads remembers, per workspace, the HEAD ground last saw, to learn what a move of
// HEAD changed. A restart forgets it: recall's own drift check is the backstop.
var seenHeads = struct {
	sync.Mutex
	head map[string]string
}{head: map[string]string{}}

const seenHeadsMax = 256

// headMovedPaths returns the paths that differ between the HEAD ground last saw for
// this workspace and HEAD now, and remembers HEAD now. It is empty when HEAD did not
// move, was not seen before, or git could not say.
func headMovedPaths(ctx context.Context, dataDir, workspaceID, root string) []string {
	head, _ := gitHead(root)
	if head == "" {
		return nil
	}
	key := dataDir + "\x00" + workspaceID
	seenHeads.Lock()
	prev := seenHeads.head[key]
	if len(seenHeads.head) >= seenHeadsMax {
		clear(seenHeads.head)
	}
	seenHeads.head[key] = head
	seenHeads.Unlock()
	if prev == "" || prev == head {
		return nil
	}
	var out bytes.Buffer
	if err := gitdiff.Run(ctx, root, &out, gitTimeout, "diff", "--name-only", "-z", "--no-renames", "--end-of-options", prev, head, "--"); err != nil {
		return nil
	}
	return strings.FieldsFunc(out.String(), func(r rune) bool { return r == 0 })
}

// --- at risk (ground) -------------------------------------------------------------

// AtRiskMemory is a served memory a change reached: which anchors, since when and at
// which commit. The title is withheld from a quarantined or instruction-like memory:
// ground carries no agent-written text it should not.
type AtRiskMemory struct {
	ID          string   `json:"id"`
	Title       string   `json:"title,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	Anchors     []string `json:"anchors"`
	SinceCommit string   `json:"since_commit,omitempty"`
	Since       string   `json:"since"`
	Trust       string   `json:"trust"`
}

// readAtRisk lists the served memories marked needs_reverify, the longest at risk first.
// The caller's own narrowing keys apply here as they do on the ranking: an entry the
// caller's scope does not admit is not listed at all, because an id, title, path or
// anchor in at_risk_memories discloses the entry it names (round 3, r3-1).
//
// The filter runs per read rather than once over a fixed scan window, because a window
// applied before the filter drops the caller's own entry whenever the rows ahead of it
// are unreadable, and reports truncated=false for a page that is not complete (round 4,
// r4-5, found by three channels). Each read REPLACES the last, so the window can widen
// without counting a row twice, and it widens until it holds more visible rows than the
// page or the store ends inside it.
//
// truncated then means one thing only: the store holds more of this caller's at-risk rows
// than this answer lists. It is true when the window overflowed the page, or when the
// window stopped growing at atRiskScanCeiling with the store undrained. Below that
// ceiling the scan reads any store to the end, so a page of one row out of ten thousand
// is a COMPLETE page and says so — the unreadable rows in front of it were read and
// dropped, not skipped.
func readAtRisk(ctx context.Context, r govstore.Reader, workspaceID, caller string, req ScopeRequest) ([]AtRiskMemory, bool, error) {
	f := govstore.EntryFilter{WorkspaceID: workspaceID, ServedOnly: true, NeedsReverify: true,
		LongestStaleFirst: true, Limit: atRiskPage}
	// Each read REPLACES the last rather than extending it: the window widens, so every
	// read covers everything the previous one did. Appending would count the same row
	// twice on every doubling (round 4, r4-5, after three triage checks).
	var visible []govstore.Entry
	// exhausted records that the store ended inside the window, which is the only case
	// where "the page is full" also means "the store is empty".
	exhausted := false
	for {
		page, err := r.ListEntries(ctx, f)
		if err != nil {
			return nil, false, err
		}
		visible = visibleEntries(page, caller, req)
		if len(visible) > maxAtRisk || len(page) < f.Limit {
			exhausted = len(page) < f.Limit
			break
		}
		if f.Limit >= atRiskScanCeiling {
			break
		}
		f.Limit = min(f.Limit*2, atRiskScanCeiling)
	}
	// truncated is the STORE holding more of this caller's at-risk rows than the answer
	// lists. It is decidable only from the scan: either the window held more visible
	// rows than the page, or the window ended before the store did and this caller may
	// still have rows in it. When the store ended inside the window the answer is whole.
	truncated := len(visible) > maxAtRisk || !exhausted
	entries := visible[:min(len(visible), maxAtRisk)]
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	anchors, err := r.AnchorsFor(ctx, ids)
	if err != nil {
		return nil, false, err
	}
	out := make([]AtRiskMemory, 0, len(entries))
	for _, e := range entries {
		m := AtRiskMemory{ID: e.ID, Since: e.StaleSince, Trust: fallbackString(e.VerificationMode, "unverified"), Anchors: []string{}}
		if e.Metadata[govstore.MetaQuarantine] == "" && len(injection.Scan(e.Title).Flags) == 0 {
			m.Title = e.Title
		}
		for _, a := range anchors[e.ID] {
			if a.Kind == govstore.AnchorPath && a.Declared {
				m.Paths = append(m.Paths, a.Value)
			}
			if a.StaleSince != "" {
				m.Anchors = append(m.Anchors, a.Kind+":"+a.Value)
				m.SinceCommit = fallbackString(m.SinceCommit, a.StaleCommit)
			}
		}
		out = append(out, m)
	}
	slices.SortStableFunc(out, func(a, b AtRiskMemory) int { return strings.Compare(a.Since, b.Since) })
	return out, truncated, nil
}

// --- the watcher -------------------------------------------------------------------

// indexedPathsObserver hears the paths of each incremental refresh the watcher's batch
// drove (PAR-FRESH-03), so memory anchored there is looked at as the code changes; it is
// set once, at start-up.
var indexedPathsObserver func(root string, paths []string)

// ObserveIndexedPaths sets what hears a root's changed paths after its index refreshed.
func ObserveIndexedPaths(f func(root string, paths []string)) { indexedPathsObserver = f }

// InvalidateWatchedRoot is the observer the API installs: it invalidates memory in every
// workspace registered at root, under ctx (the API's, so shutdown cancels it). Batches
// of one root are serialized and coalesced: paths that arrive while a pass runs join one
// pending set that the next pass takes, so quick successive saves never overlap.
func InvalidateWatchedRoot(ctx context.Context, dataDir string) func(root string, paths []string) {
	var mu sync.Mutex
	pending := map[string]map[string]bool{} // root -> paths waiting; a key present means a pass is running
	return func(root string, paths []string) {
		mu.Lock()
		set, running := pending[root]
		if !running {
			set = map[string]bool{}
			pending[root] = set
		}
		for _, p := range paths {
			set[p] = true
		}
		mu.Unlock()
		if running {
			return
		}
		go func() {
			for {
				mu.Lock()
				batch := slices.Sorted(maps.Keys(pending[root]))
				if len(batch) == 0 || ctx.Err() != nil {
					delete(pending, root)
					mu.Unlock()
					return
				}
				clear(pending[root])
				mu.Unlock()
				invalidateRoot(ctx, dataDir, root, batch)
			}
		}()
	}
}

// invalidateRoot invalidates memory in every workspace registered at root.
func invalidateRoot(ctx context.Context, dataDir, root string, paths []string) {
	recs, err := ListWorkspaces(dataDir)
	if err != nil {
		return
	}
	for _, rec := range recs {
		if ws, err := resolveWorkspace(dataDir, rec.WorkspaceID); err == nil && canonicalRoot(ws.Root) == canonicalRoot(root) {
			_, _ = InvalidateMemory(ctx, dataDir, rec.WorkspaceID, paths)
		}
	}
}

// groundChangedFiles reads the working tree's changed files; tests replace it.
var groundChangedFiles = workingChangedFiles

// invalidateForGround looks the caller's changes up in the anchor index before ground
// lists what is at risk: the working tree's changed files, and the paths a move of HEAD
// since ground last looked changed. Changes that cannot be read are left to ground's
// own index section to report; only a failure to look them up is returned.
func invalidateForGround(ctx context.Context, dataDir, workspaceID string, changes json.RawMessage) error {
	root := contextRoot(dataDir, workspaceID)
	if root == "" {
		return nil
	}
	changed, _ := decodeChangedFiles(changes)
	if changes == nil {
		changed, _ = groundChangedFiles(ctx, dataDir, workspaceID)
	}
	changed = append(changed, headMovedPaths(ctx, dataDir, workspaceID, root)...)
	rep, err := InvalidateMemory(ctx, dataDir, workspaceID, changed)
	if err == nil && rep.Truncated {
		err = fmt.Errorf("more than %d anchors lie in the changed files; the rest were not checked", maxInvalidationAnchors)
	}
	if err == nil && rep.Unknown > 0 {
		// The same shape as the truncation above and for the same reason: ground lists what
		// is at risk, and a caller that cannot tell a fully checked change from one whose
		// quoted-code anchors were left unchecked would read an empty answer as a clean one.
		err = fmt.Errorf("%d quoted-code anchors in the changed files could not be checked; they are neither moved nor marked", rep.Unknown)
	}
	return err
}

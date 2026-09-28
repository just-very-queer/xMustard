package workspaceops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xmustard/api-go/internal/govstore"
)

// Governed memory lives in one govstore SQLite database per data dir (PAR-STORE-01).
// The store is the single source of truth: every propose, vote, promotion, edit and
// baseline is one transaction there, with its event appended to the history, and it is
// safe across processes (the API daemon, a second API process, the ops CLI). The legacy
// per-workspace context_entries.json is imported once, on first access, and then moved
// aside as a backup; nothing writes it any more.
//
// Rollback: stop the API, run govstore's ExportContextEntriesJSON for each workspace
// (or restore workspaces/<ws>/context_entries.json from its .govstore-import.bak),
// and start the previous build. Entries changed after the cutover exist only in the
// store, so export rather than restore when the store has been written.

// memoryStoreFile is the governance database, relative to the data dir.
const memoryStoreFile = "governance.db"

func memoryStorePath(dataDir string) string { return filepath.Join(dataDir, memoryStoreFile) }

// maxIdleMemoryStores bounds how many stores stay open with no caller. A daemon serves
// one data dir; the bound matters for tests and tools that touch many.
const maxIdleMemoryStores = 2

type memoryStoreHandle struct {
	store *govstore.SQLStore
	refs  int
	used  time.Time
}

var memoryStores = struct {
	sync.Mutex
	open map[string]*memoryStoreHandle
	// faults holds the stores that failed an integrity check, by path.
	faults map[string]error
}{open: map[string]*memoryStoreHandle{}, faults: map[string]error{}}

// acquireMemoryStore returns the data dir's store and the function that hands it back.
func acquireMemoryStore(ctx context.Context, dataDir string) (*govstore.SQLStore, func(), error) {
	return acquireGovStore(ctx, dataDir, false)
}

// acquireGovStore is acquireMemoryStore; with check it also runs PRAGMA quick_check before
// handing the store out (at open on a short-lived connection, or on the open store). A
// store that failed its check is refused from then on (memory_store_health.go).
func acquireGovStore(ctx context.Context, dataDir string, check bool) (*govstore.SQLStore, func(), error) {
	path, err := filepath.Abs(memoryStorePath(dataDir))
	if err != nil {
		return nil, nil, err
	}
	memoryStores.Lock()
	defer memoryStores.Unlock()
	if err := memoryStores.faults[path]; err != nil {
		return nil, nil, err
	}
	h := memoryStores.open[path]
	if h == nil {
		s, err := govstore.Open(ctx, path, govstore.Options{QuickCheckOnOpen: check})
		if err != nil {
			return nil, nil, noteStoreFault(path, fmt.Errorf("open memory store: %w", err))
		}
		h = &memoryStoreHandle{store: s}
		memoryStores.open[path] = h
	} else if check {
		if err := h.store.QuickCheck(ctx); err != nil {
			return nil, nil, noteStoreFault(path, fmt.Errorf("check memory store: %w", err))
		}
	}
	h.refs++
	var once sync.Once
	return h.store, func() {
		once.Do(func() {
			memoryStores.Lock()
			defer memoryStores.Unlock()
			h.refs--
			h.used = time.Now()
			closeIdleMemoryStoresLocked(maxIdleMemoryStores)
		})
	}, nil
}

// closeIdleMemoryStoresLocked closes the least recently used idle stores until at most
// keep idle stores stay open. A store in use is never closed.
func closeIdleMemoryStoresLocked(keep int) {
	for {
		var oldest string
		idle := 0
		for p, h := range memoryStores.open {
			if h.refs > 0 {
				continue
			}
			idle++
			if oldest == "" || h.used.Before(memoryStores.open[oldest].used) {
				oldest = p
			}
		}
		if idle <= keep {
			return
		}
		_ = memoryStores.open[oldest].store.Close()
		delete(memoryStores.open, oldest)
	}
}

// CloseMemoryStores closes every idle governance store at shutdown, which runs after
// the HTTP server has drained its handlers. A store still held by a caller (a request
// that outlived the drain) is left open rather than closed under it: every committed
// transaction is already in the WAL, and SQLite replays the WAL on the next open, so
// skipping its checkpoint loses nothing.
func CloseMemoryStores() {
	memoryStores.Lock()
	defer memoryStores.Unlock()
	closeIdleMemoryStoresLocked(0)
}

// withMemoryStore runs fn against the data dir's store after the workspace's legacy
// JSON, if any, has been imported. The workspace id is checked first: it names the
// legacy file the import reads and renames, so it must not reach a path unvalidated.
func withMemoryStore(ctx context.Context, dataDir, workspaceID string, fn func(*govstore.SQLStore) error) error {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return err
	}
	s, release, err := acquireMemoryStore(ctx, dataDir)
	if err != nil {
		return err
	}
	defer release()
	if err := importLegacyContextEntries(ctx, s, dataDir, workspaceID); err != nil {
		return err
	}
	return mapStoreErr(fn(s))
}

// memoryUpdate runs fn in one store write transaction.
func memoryUpdate(ctx context.Context, dataDir, workspaceID string, fn func(govstore.Tx) error) error {
	return withMemoryStore(ctx, dataDir, workspaceID, func(s *govstore.SQLStore) error { return s.Update(ctx, fn) })
}

// memoryView runs fn against one read snapshot of the store.
func memoryView(ctx context.Context, dataDir, workspaceID string, fn func(govstore.Reader) error) error {
	return withMemoryStore(ctx, dataDir, workspaceID, func(s *govstore.SQLStore) error { return s.View(ctx, fn) })
}

// mapStoreErr turns store errors into the package's error classes, so HTTP and MCP keep
// answering 404 / 400 / 409 as they did for the JSON store.
func mapStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, govstore.ErrNotFound):
		return fmt.Errorf("%w: %v", os.ErrNotExist, err)
	case errors.Is(err, govstore.ErrInvalid):
		return fmt.Errorf("%v: %w", err, ErrInvalidInput)
	case errors.Is(err, govstore.ErrConflict), errors.Is(err, govstore.ErrInvariant):
		return Conflict(err.Error()).WithCause(err)
	}
	return err
}

// --- legacy import -----------------------------------------------------------------

// legacyImportHeavyFrom is the file size from which the import is heavy work, run in the
// heavy slot that govstore takes (and declares) for its legacy imports. A smaller file
// imports in about the time of one ordinary write, so it runs inline instead of queueing
// behind an index build or being refused under memory pressure. The store's resident
// line is govstore's own "govstore" budget component.
const legacyImportHeavyFrom = 1 << 20

// legacyImportMu serializes imports inside the process; the importer is idempotent, so
// another process importing the same file at once only finds its entries unchanged.
var legacyImportMu sync.Mutex

func legacyContextEntriesPath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "context_entries.json")
}

// importLegacyContextEntries imports the workspace's legacy context_entries.json into
// the store once (a large file under the heavy slot), then moves the file aside as a backup. An
// unreadable or malformed file fails the call (fail closed) and stays in place, so an
// operator can repair it; the store never serves a half-imported workspace because the
// import is one transaction.
func importLegacyContextEntries(ctx context.Context, s *govstore.SQLStore, dataDir, workspaceID string) error {
	src := legacyContextEntriesPath(dataDir, workspaceID)
	info, err := os.Stat(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	legacyImportMu.Lock()
	defer legacyImportMu.Unlock()
	f, err := os.Open(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil // imported by the caller that held the lock before us
	}
	if err != nil {
		return err
	}
	defer f.Close()
	_, threshold := contextDefaults(dataDir)
	rep, err := s.ImportContextEntriesJSON(ctx, workspaceID, bufio.NewReader(f), govstore.ImportOptions{
		Threshold: threshold,
		Inline:    info.Size() < legacyImportHeavyFrom,
	})
	if err != nil {
		return fmt.Errorf("import legacy memory for workspace %s: %w", workspaceID, err)
	}
	if rep.ConflictCount > 0 || rep.RelabelledCount > 0 || rep.WarningCount > 0 {
		log.Printf("memory import %s: imported=%d unchanged=%d conflicts=%d relabelled=%d warnings=%d",
			workspaceID, rep.Imported, rep.Unchanged, rep.ConflictCount, rep.RelabelledCount, rep.WarningCount)
	}
	backup := src + ".govstore-import.bak"
	if _, err := os.Stat(backup); err == nil {
		backup = fmt.Sprintf("%s.govstore-import.%d.bak", src, time.Now().UnixNano())
	}
	// Another process may have imported the same file and moved it aside first; the
	// import above was idempotent, so its entries are in the store either way.
	if err := os.Rename(src, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// --- actors and repository state ---------------------------------------------------

// gitHead reads HEAD's commit and branch from the repository files, without spawning
// git. Worktrees and packed refs are followed; anything unreadable yields "".
func gitHead(root string) (sha, branch string) {
	if root == "" {
		return "", ""
	}
	gitDir := filepath.Join(root, ".git")
	if b, err := os.ReadFile(gitDir); err == nil { // a worktree: ".git" names its git dir
		dir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if !ok {
			return "", ""
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		gitDir = dir
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", ""
	}
	ref, symbolic := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: ")
	if !symbolic {
		return ref, "" // detached
	}
	branch = strings.TrimPrefix(ref, "refs/heads/")
	common := gitDir
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		if common = strings.TrimSpace(string(b)); !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
	}
	for _, dir := range []string{gitDir, common} {
		if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref))); err == nil {
			return strings.TrimSpace(string(b)), branch
		}
	}
	packed, err := os.ReadFile(filepath.Join(common, "packed-refs"))
	if err != nil {
		return "", branch
	}
	for _, line := range strings.Split(string(packed), "\n") {
		if hash, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok && name == ref {
			return hash, branch
		}
	}
	return "", branch
}

// --- projection ----------------------------------------------------------------------

// contextEntryFrom renders a store entry in the ContextEntry shape the API serves.
// Content is left empty; callers that return content read it bound to its digest.
func contextEntryFrom(e govstore.Entry, votes []govstore.Vote, anchors []govstore.Anchor) ContextEntry {
	ce := ContextEntry{
		ID: e.ID, WorkspaceID: e.WorkspaceID, Title: e.Title, Source: e.Source, Permission: e.Permission,
		Status: e.Status, Promoted: e.Promoted, Verifications: []ContextVerification{},
		RequiredVerifications: e.RequiredVerifications, RequireVerification: e.RequireVerification,
		VerificationMode: e.VerificationMode, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		SearchTokens: e.SearchTokens, ContentDigest: e.ContentDigest,
		Revision: e.Revision, SupersededBy: e.SupersededBy, InvalidatedAt: e.InvalidatedAt, ExpiresAt: e.ExpiresAt,
		Supersedes: pendingSupersedes(e),
		Kind:       e.Kind, Topic: e.Topic, Tags: e.Tags, Quarantine: e.Metadata[govstore.MetaQuarantine],
	}
	if e.HeadRevision > e.Revision {
		ce.PendingRevision = e.HeadRevision
	}
	if e.Lifecycle != govstore.LifecycleActive {
		ce.Lifecycle = e.Lifecycle
	}
	for _, v := range votes {
		if v.Verdict == govstore.VerdictApprove || v.Verdict == govstore.VerdictReject {
			ce.Verifications = append(ce.Verifications, ContextVerification{
				Agent: v.Principal, Approve: v.Verdict == govstore.VerdictApprove, Note: v.Note, At: v.At,
			})
		}
	}
	for _, a := range anchors {
		if a.Kind != govstore.AnchorPath {
			continue
		}
		if a.Declared {
			ce.Paths = append(ce.Paths, a.Value)
		}
		if h, ok := baselineHashes[a.BaselineState]; ok {
			if ce.PathHashes == nil {
				ce.PathHashes = map[string]string{}
			}
			ce.PathHashes[a.Value] = h(a)
		}
	}
	return ce
}

// baselineHashes renders a stored path baseline in the legacy PathHashes form; a path
// with no baseline is absent.
var baselineHashes = map[string]func(govstore.Anchor) string{
	govstore.BaselineHash:    func(a govstore.Anchor) string { return a.BaselineHash },
	govstore.BaselineMissing: func(govstore.Anchor) string { return pathMissingSentinel },
}

// pathBaselines is the inverse: PathHashes as store baselines.
func pathBaselines(hashes map[string]string) []govstore.Baseline {
	out := make([]govstore.Baseline, 0, len(hashes))
	for p, h := range hashes {
		b := govstore.Baseline{Kind: govstore.AnchorPath, Value: p, State: govstore.BaselineHash, Hash: h, BaselineKind: "file"}
		if h == pathMissingSentinel {
			b.State, b.Hash = govstore.BaselineMissing, ""
		}
		out = append(out, b)
	}
	return out
}

// loadEntryTx reads one entry of the workspace for a write. An entry of another
// workspace is reported missing, never touched.
func loadEntryTx(ctx context.Context, r govstore.Reader, workspaceID, entryID string) (govstore.Entry, ContextEntry, error) {
	e, ce, err := loadAnyEntryTx(ctx, r, workspaceID, entryID)
	if err == nil && e.Lifecycle != govstore.LifecycleActive {
		err = fmt.Errorf("entry %s: %w", entryID, os.ErrNotExist)
	}
	return e, ce, err
}

// loadAnyEntryTx reads one entry of the workspace in any lifecycle state, for history
// reads and lifecycle transitions.
func loadAnyEntryTx(ctx context.Context, r govstore.Reader, workspaceID, entryID string) (govstore.Entry, ContextEntry, error) {
	e, err := r.GetEntry(ctx, entryID)
	if err != nil {
		return govstore.Entry{}, ContextEntry{}, err
	}
	if e.WorkspaceID != workspaceID {
		return govstore.Entry{}, ContextEntry{}, fmt.Errorf("entry %s: %w", entryID, os.ErrNotExist)
	}
	ce, err := projectEntries(ctx, r, []govstore.Entry{e})
	if err != nil {
		return govstore.Entry{}, ContextEntry{}, err
	}
	return e, ce[0], nil
}

// projectEntries renders entries with their served votes and anchors (no content).
func projectEntries(ctx context.Context, r govstore.Reader, entries []govstore.Entry) ([]ContextEntry, error) {
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	votes, err := r.ServedVotes(ctx, ids)
	if err != nil {
		return nil, err
	}
	anchors, err := r.AnchorsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ContextEntry, len(entries))
	for i, e := range entries {
		out[i] = contextEntryFrom(e, votes[e.ID], anchors[e.ID])
	}
	return out, nil
}

// projectByID renders the given entries, in order, without content.
func projectByID(ctx context.Context, r govstore.Reader, ids []string) ([]ContextEntry, error) {
	entries := make([]govstore.Entry, 0, len(ids))
	for _, id := range ids {
		e, err := r.GetEntry(ctx, id)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return projectEntries(ctx, r, entries)
}

// storeListPage is how many entries one listing query reads.
const storeListPage = 500

// listWorkspaceEntries pages through the workspace's active entries matching f and
// renders them without content.
func listWorkspaceEntries(ctx context.Context, r govstore.Reader, f govstore.EntryFilter) ([]ContextEntry, error) {
	var out []ContextEntry
	f.Limit = storeListPage
	for {
		page, err := r.ListEntries(ctx, f)
		if err != nil {
			return nil, err
		}
		rendered, err := projectEntries(ctx, r, page)
		if err != nil {
			return nil, err
		}
		out = append(out, rendered...)
		if len(page) < storeListPage {
			return out, nil
		}
		f.AfterCursor = page[len(page)-1].Cursor
	}
}

// attachContent fills Content for entries from their served revisions. Content that no
// longer matches the digest the entry was rendered with is withheld: the entry is
// dropped and counted, never returned with another version's text.
func attachContent(ctx context.Context, r govstore.Reader, entries []ContextEntry) ([]ContextEntry, int, error) {
	ids := make([]string, len(entries))
	for i := range entries {
		ids[i] = entries[i].ID
	}
	contents, err := r.EntryContents(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	kept, withheld := entries[:0], 0
	for _, e := range entries {
		c, ok := contents[e.ID]
		if !ok || c.Withheld != "" || c.Digest != e.ContentDigest || contentDigest(c.Content) != e.ContentDigest {
			withheld++
			continue
		}
		e.Content = c.Content
		e.ContentDigest = "" // a transport field of the binding, not part of the served shape
		kept = append(kept, e)
	}
	return kept, withheld, nil
}

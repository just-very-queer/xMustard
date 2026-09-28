package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/rustcore"
)

// Spawn-free reads for the Claude Code hook service (WS-23; PAR-HAR-01, PAR-HAR-02,
// PAR-RT-12, PAR-FRESH-07). A hook fires on every tool call, so nothing here starts a
// process: Rust work runs only on a resident worker that is already running
// (rustcore.WithResidentOnly), the repository identity is never sampled, and the
// workspace registry, the memory store and files under the root are read directly.

// ErrIndexNotResident means a hook asked for index hits the resident worker cannot
// serve right now: this process has not brought the root's code index up, or no
// resident worker is running (rustcore.ErrNotResident).
var ErrIndexNotResident = errors.New("the code index is not resident")

// WorkspaceForPath returns the registered workspace whose root holds path, the deepest
// root when roots nest. ok is false when no registered root holds it. The workspace is
// checked the way every tool path checks it (resolveWorkspace), so a re-pointed root
// fails closed.
func WorkspaceForPath(dataDir, path string) (ResolvedWorkspace, bool, error) {
	if !filepath.IsAbs(path) {
		return ResolvedWorkspace{}, false, nil
	}
	mark, err := statMark(workspacesPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return ResolvedWorkspace{}, false, nil
	}
	if err != nil {
		return ResolvedWorkspace{}, false, err
	}
	byID, err := registryRecords(dataDir, mark, registryNow())
	if err != nil {
		return ResolvedWorkspace{}, false, err
	}
	target := canonicalRoot(filepath.Clean(path))
	best, bestRoot := "", ""
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		rec := byID[id]
		if rec.RootPath == "" || !validWorkspaceID(id) {
			continue
		}
		if root := canonicalRoot(rec.RootPath); pathWithin(target, root) && len(root) > len(bestRoot) {
			best, bestRoot = id, root
		}
	}
	if best == "" {
		return ResolvedWorkspace{}, false, nil
	}
	ws, err := resolveWorkspace(dataDir, best)
	if err != nil {
		return ResolvedWorkspace{}, false, err
	}
	return ws, true, nil
}

// LookupWorkspace returns a registered workspace by id, checked as every tool path
// checks it (a missing snapshot or a re-pointed root is an error).
func LookupWorkspace(dataDir, workspaceID string) (ResolvedWorkspace, error) {
	return resolveWorkspace(dataDir, workspaceID)
}

// pathWithin reports whether path is root or lies under it (both clean and absolute).
func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// RelativeToRoot returns path relative to the workspace root, or "" when it is not
// under it. The root is matched as registered and as resolved (Scope), and a path whose
// directory is reached through a symlink is matched by its resolved directory.
func RelativeToRoot(ws ResolvedWorkspace, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(ws.Root, path)
	}
	path = filepath.Clean(path)
	candidates := []string{path, filepath.Join(canonicalRoot(filepath.Dir(path)), filepath.Base(path))}
	for _, root := range []string{ws.Root, ws.Scope} {
		for _, p := range candidates {
			if root == "" || p == root || !pathWithin(p, root) {
				continue
			}
			rel, _ := filepath.Rel(root, p)
			return filepath.ToSlash(rel)
		}
	}
	return ""
}

// HookSearch runs the hybrid search (BM25 over code and docs, names, structure and
// graph proximity) on the resident index for a hook. It runs only when this process
// brought root's code index up, so the index exists and answers (no legacy graph build),
// and only on a resident worker that is already running. It passes no identity key:
// the index answers as of its last generation, and its freshness envelope says so.
func HookSearch(ctx context.Context, ws ResolvedWorkspace, query string, limit int) (json.RawMessage, error) {
	if !codeIndexReady(canonicalRoot(ws.Root)) {
		return nil, ErrIndexNotResident
	}
	out, err := rustcore.RunSearch(rustcore.WithResidentOnly(ctx), ws.Root, ws.WorkspaceID, query, strconv.Itoa(limit))
	if errors.Is(err, rustcore.ErrNotResident) {
		return nil, ErrIndexNotResident
	}
	return out, err
}

// codeIndexReady reports whether this process brought root's code index up.
func codeIndexReady(root string) bool {
	codeIndex.Lock()
	defer codeIndex.Unlock()
	st := codeIndex.roots[root]
	return st != nil && st.indexed != ""
}

// HookSyntaxCheck returns the tree-sitter syntax errors of files under the root
// (`xmustard-core syntax-check`), from a resident worker that is already running.
func HookSyntaxCheck(ctx context.Context, ws ResolvedWorkspace, rels []string) (json.RawMessage, error) {
	out, err := rustcore.RunSyntaxCheck(rustcore.WithResidentOnly(ctx), append([]string{ws.Root}, rels...)...)
	if errors.Is(err, rustcore.ErrNotResident) {
		return nil, ErrIndexNotResident
	}
	return out, err
}

// hookUnsampled are the ground fields a hook does not compute: they come from the
// change-tracking scan and the symbol graph, which run in the Rust core with git.
var hookUnsampled = []string{"drift", "baseline", "changed_files", "dirty_symbols", "contract_breaks", "coverage"}

// HookGrounding is ground's spawn-free part, for SessionStart and SubagentStart: the
// failed runs and open failures, and the memory section. The change and index fields
// are listed unknown with the reason, never guessed; the ground tool samples them.
func HookGrounding(ctx context.Context, dataDir, workspaceID, caller string) *SessionGrounding {
	g := &SessionGrounding{WorkspaceID: workspaceID}
	for _, f := range hookUnsampled {
		g.Unknown = append(g.Unknown, GroundingUnknown{Field: f, Reason: "not sampled by a hook (hooks start no Rust or git process); call ground"})
	}
	unknown, failuresKnown := g.groundingRuns.build(ctx, dataDir, workspaceID)
	g.Unknown = append(g.Unknown, unknown...)
	if failing := len(g.RecentFailedRuns) > 0; failing || failuresKnown {
		g.BlockedByFailingVerification = &failing
	}
	g.stampGenerated()
	g.Unknown = append(g.Unknown, g.groundingMemory.build(dataDir, workspaceID, caller)...)
	g.summarize()
	return g
}

// HookMemory is a served memory a hook may push, as recall ranked it, with its drift
// flag: Stale when a file it is anchored to changed since it was verified.
type HookMemory struct {
	ID         string
	Title      string
	Stale      bool
	StalePaths []string
}

// HookMemories ranks served memory for a hook the way recall does, by the paths a tool
// reads or edits or by a search pattern, relevance-gated, at most limit. Without a
// query or a path it returns nothing: recall would then fall back to the working
// changes, which runs the Rust core.
func HookMemories(ctx context.Context, dataDir, workspaceID, caller, query string, paths []string, limit int) ([]HookMemory, error) {
	if strings.TrimSpace(query) == "" && len(cleanPaths(paths)) == 0 {
		return nil, nil
	}
	res, err := RecallWith(ctx, dataDir, workspaceID, RecallRequest{Query: query, Paths: paths, Limit: limit, Caller: caller})
	if err != nil {
		return nil, err
	}
	entries, _ := res["entries"].([]ContextEntry)
	out := make([]HookMemory, 0, len(entries))
	for _, e := range entries {
		out = append(out, HookMemory{ID: e.ID, Title: e.Title, Stale: e.Stale, StalePaths: e.StalePaths})
	}
	return out, nil
}

// TriggerTagPrefix marks a memory tag as a keyword trigger: a memory tagged
// "trigger-deploy" is pushed when a prompt names "deploy" (UserPromptSubmit). Tags
// take letters, digits, '_', '.' and '-', so the keyword is one word.
const TriggerTagPrefix = "trigger-"

// HookMemoryIndex is what the session-level hooks read from the served ranking view in
// one pass: the core tier, the keyword triggers and the anchored paths.
type HookMemoryIndex struct {
	// Core lists the served core-tier entries, newest first.
	Core []string
	// Triggers maps a lowercase keyword to the entries it triggers, newest first.
	Triggers map[string][]string
	// Anchors lists the distinct paths served memory is anchored to, newest first.
	Anchors []string
}

// ReadHookMemoryIndex reads the served ranking view once (content-free).
func ReadHookMemoryIndex(ctx context.Context, dataDir, workspaceID string) (HookMemoryIndex, error) {
	var view []govstore.RankEntry
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		view, err = r.ServedRanking(ctx, workspaceID)
		return err
	})
	if err != nil {
		return HookMemoryIndex{}, err
	}
	sort.SliceStable(view, func(i, j int) bool { return view[i].UpdatedAt > view[j].UpdatedAt })
	idx := HookMemoryIndex{Triggers: map[string][]string{}}
	seen := map[string]bool{}
	for _, e := range view {
		if e.Tier == govstore.TierCore {
			idx.Core = append(idx.Core, e.ID)
		}
		for _, t := range e.Tags {
			if kw, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(t)), TriggerTagPrefix); ok && kw != "" {
				idx.Triggers[kw] = append(idx.Triggers[kw], e.ID)
			}
		}
		for _, p := range e.Paths {
			if !seen[p] {
				seen[p] = true
				idx.Anchors = append(idx.Anchors, p)
			}
		}
	}
	return idx, nil
}

// The dirty set (PAR-FRESH-07): paths the client reported changing (FileChanged, the
// edit tools, a Bash command's changed files) since the refresh loop last took them.
// Noting a path also drops the root's cached repository identity, so the next read
// samples it instead of trusting the cache for up to its TTL (PAR-RT-12). The set is
// bounded: past maxDirtyPaths a root is marked overflowed, which tells the consumer
// (WS-15's refresh loop) to run a full check.
const (
	maxDirtyPaths = 4096
	maxDirtyRoots = 64
)

type dirtyRoot struct {
	paths    map[string]time.Time
	overflow bool
	used     time.Time
}

var dirtySet = struct {
	sync.Mutex
	roots map[string]*dirtyRoot
}{roots: map[string]*dirtyRoot{}}

// NoteDirtyPaths records root-relative paths as changed and invalidates the root's
// cached identity. It returns how many paths the root now holds.
func NoteDirtyPaths(root string, rels []string) int {
	if root == "" || len(rels) == 0 {
		return 0
	}
	InvalidateRepoIdentity(root)
	now := time.Now()
	dirtySet.Lock()
	defer dirtySet.Unlock()
	d := dirtySet.roots[root]
	if d == nil {
		if len(dirtySet.roots) >= maxDirtyRoots {
			evictDirtyRoot()
		}
		d = &dirtyRoot{paths: map[string]time.Time{}}
		dirtySet.roots[root] = d
	}
	d.used = now
	for _, p := range rels {
		if len(d.paths) >= maxDirtyPaths {
			d.overflow = true
			break
		}
		d.paths[p] = now
	}
	return len(d.paths)
}

// TakeDirtyPaths returns and clears root's dirty set, sorted, and whether it overflowed.
func TakeDirtyPaths(root string) (paths []string, overflow bool) {
	dirtySet.Lock()
	defer dirtySet.Unlock()
	d := dirtySet.roots[root]
	if d == nil {
		return nil, false
	}
	delete(dirtySet.roots, root)
	return slices.Sorted(maps.Keys(d.paths)), d.overflow
}

// ForgetRoot drops what the identity cache and the dirty set hold for root, as given
// and as resolved (a removed worktree).
func ForgetRoot(root string) {
	for _, r := range slices.Compact([]string{root, canonicalRoot(root)}) {
		InvalidateRepoIdentity(r)
		dirtySet.Lock()
		delete(dirtySet.roots, r)
		dirtySet.Unlock()
	}
}

func evictDirtyRoot() {
	var oldest string
	for r, d := range dirtySet.roots {
		if oldest == "" || d.used.Before(dirtySet.roots[oldest].used) {
			oldest = r
		}
	}
	delete(dirtySet.roots, oldest)
}

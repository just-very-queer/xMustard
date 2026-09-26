package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"xmustard/api-go/internal/govstore"
)

// contentDigest is the full SHA-256 (hex) of an entry's content: the collision-resistant
// identity recall uses to bind returned text to the promoted revision.
func contentDigest(s string) string { return govstore.Digest(s) }

// hashFileContent returns the sha256 of a repo-relative file's current content.
// The path is confined to the workspace root (no `..`/absolute/symlink escape),
// must be a regular file, and is size-capped — so a memory reference cannot become
// an arbitrary host-file read/hash oracle or an I/O DoS (XM-NEW-002).
func hashFileContent(root, rel string) (string, bool) {
	data, ok := readWorkspaceRegularFile(root, rel)
	if !ok {
		return "", false
	}
	noteHashed(len(data))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
}

// pathMissingSentinel baselines a referenced path that was missing/unreadable/
// escaping at verification time, so its later creation is detectable as drift
// (rather than dropped and never tracked) — XM-NEW-003.
const pathMissingSentinel = "\x00missing"

// capturePathHashes snapshots the content hash of each referenced path — the
// "verified-against" baseline that drift-on-recall later compares to. EVERY
// requested path is represented: present paths by their hash, missing/unreadable
// ones by a sentinel, so a missing→present (or present→missing) transition is drift.
func capturePathHashes(root string, paths []string) map[string]string {
	if root == "" || len(paths) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, p := range paths {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if h, ok := hashFileContent(root, p); ok {
			out[p] = h
		} else {
			out[p] = pathMissingSentinel
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// computeStaleness flags a promoted entry whose referenced files changed since it
// was verified — so recall never serves silently-stale memory. A path that appears,
// disappears, or changes content relative to its baseline is stale.
func computeStaleness(root string, entry *ContextEntry) {
	if root == "" || len(entry.PathHashes) == 0 {
		return
	}
	stalenessChecks.Add(1)
	var stale []string
	for p, recorded := range entry.PathHashes {
		cur, ok := hashFileContent(root, p)
		switch {
		case !ok && recorded != pathMissingSentinel:
			stale = append(stale, p) // was present at verify time, now missing/unreadable
		case ok && recorded == pathMissingSentinel:
			stale = append(stale, p) // was missing at verify time, now present
		case ok && cur != recorded:
			stale = append(stale, p) // content changed
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		entry.Stale = true
		entry.StalePaths = stale
	}
}

// contextRoot resolves the workspace repo root (empty if unresolvable; callers
// degrade gracefully so memory still works without a live tree).
func contextRoot(dataDir, workspaceID string) string {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return ""
	}
	return root
}

// countVerificationModes counts the promoted entries per trust basis, so recall and
// ground can show how many facts are peer-verified versus self-asserted.
func countVerificationModes(entries []ContextEntry) map[string]int {
	counts := newModeCounts()
	for _, e := range entries {
		if e.Promoted && e.VerificationMode != "" {
			counts[e.VerificationMode]++
		}
	}
	return counts
}

// newModeCounts starts a per-mode count with every mode present.
func newModeCounts() map[string]int {
	return map[string]int{VerificationPeer: 0, VerificationSelfAssertedOpen: 0, VerificationSingleAgent: 0}
}

// contextFilters maps a list filter to the store query that selects it.
var contextFilters = map[string]func(*govstore.EntryFilter){
	"":         func(*govstore.EntryFilter) {},
	"all":      func(*govstore.EntryFilter) {},
	"pending":  func(f *govstore.EntryFilter) { f.Status = govstore.StatusPending },
	"rejected": func(f *govstore.EntryFilter) { f.Status = govstore.StatusRejected },
	"promoted": promotedFilter,
	"active":   promotedFilter,
	"verified": promotedFilter,
}

func promotedFilter(f *govstore.EntryFilter) {
	promoted := true
	f.Promoted = &promoted
}

// ListContextEntries returns entries filtered by status: "" / "all", "pending",
// "promoted"/"active"/"verified", "rejected". An unknown filter selects nothing.
func ListContextEntries(dataDir, workspaceID, filter string) ([]ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	apply, ok := contextFilters[strings.ToLower(strings.TrimSpace(filter))]
	if !ok {
		return []ContextEntry{}, nil
	}
	f := govstore.EntryFilter{WorkspaceID: workspaceID}
	apply(&f)
	ctx := context.Background()
	out := []ContextEntry{}
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		entries, err := listWorkspaceEntries(ctx, r, f)
		if err != nil {
			return err
		}
		if out, _, err = attachContent(ctx, r, entries); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// GetActiveContext returns only promoted (verified) entries — the read-only view
// that actually enters an agent's working context.
func GetActiveContext(dataDir, workspaceID string) (map[string]any, error) {
	promoted, err := ListContextEntries(dataDir, workspaceID, "promoted")
	if err != nil {
		return nil, err
	}
	// drift-on-recall: flag entries whose referenced files changed since they were
	// verified, so an agent never grounds on silently-stale memory.
	root := contextRoot(dataDir, workspaceID)
	staleCount := 0
	for i := range promoted {
		computeStaleness(root, &promoted[i])
		if promoted[i].Stale {
			staleCount++
		}
	}
	requireMulti, threshold := contextDefaults(dataDir)
	return map[string]any{
		"workspace_id":           workspaceID,
		"require_multi_agent":    requireMulti,
		"verification_threshold": threshold,
		"active_count":           len(promoted),
		"verification_modes":     countVerificationModes(promoted),
		"stale_count":            staleCount,
		"conflicts":              overlappingMemory(promoted),
		"entries":                promoted,
		"generated_at":           nowUTC(),
	}, nil
}

// RecallContext is ranked, query-aware recall — the fix for "recall dumps every
// memory." It scores each verified entry by multi-signal relevance (lexical match
// on title+content, path overlap with the query paths or the current working
// changes, verification strength, recency, with a stale penalty) and returns the
// top-N, so an agent grounds on the few facts that matter rather than the whole
// store. With no query or paths it falls back to recency-ranked top-N.
func RecallContext(dataDir, workspaceID, query string, paths []string, limit int) (map[string]any, error) {
	return RecallContextCtx(context.Background(), dataDir, workspaceID, query, paths, limit)
}

// RecallContextCtx is RecallContext bound to the request context, so the working-change
// lookup's Rust child is killed when the caller cancels.
func RecallContextCtx(ctx context.Context, dataDir, workspaceID, query string, paths []string, limit int) (map[string]any, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	// Content is bound to the exact promoted revision whose metadata was ranked: a
	// concurrent edit between the metadata pass and the content load yields a digest
	// mismatch, and the whole recall is retried against the new state. If the store
	// keeps moving, mismatched entries are withheld (fail closed) and counted.
	var res map[string]any
	for attempt := 1; attempt <= recallConsistencyAttempts; attempt++ {
		var mismatched int
		var err error
		res, mismatched, err = recallOnce(ctx, dataDir, workspaceID, query, paths, limit, attempt == recallConsistencyAttempts)
		if err != nil {
			return nil, err
		}
		res["consistency_attempts"] = attempt
		if mismatched == 0 {
			return res, nil
		}
		res["consistency_withheld"] = mismatched
	}
	return res, nil
}

// recallConsistencyAttempts bounds how often recall re-reads when content changed
// under it before withholding the mismatched entries.
const recallConsistencyAttempts = 3

type scoredEntry struct {
	entry ContextEntry
	score float64
}

func sortByScore(s []scoredEntry) {
	sort.SliceStable(s, func(a, b int) bool {
		if s[a].score != s[b].score {
			return s[a].score > s[b].score
		}
		return s[a].entry.UpdatedAt > s[b].entry.UpdatedAt
	})
}

// recallOnce is one ranked-recall pass. It reports how many candidates changed between
// ranking and loading (their served revision moved, or they stopped being served); when
// withhold is set those entries are removed from the result instead of being returned.
func recallOnce(ctx context.Context, dataDir, workspaceID, query string, paths []string, limit int, withhold bool) (map[string]any, int, error) {
	// metadata-first: rank on the lean ranking view (no content, no vote rows), so
	// recall's reads and allocation don't scale with content size; full state and
	// content are loaded only for the bounded candidate window below (XM-PRO-010).
	var ranking []govstore.RankEntry
	if err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		ranking, err = r.ServedRanking(ctx, workspaceID)
		return err
	}); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	// path signal: explicit query paths, else the files currently being worked on.
	// Explicit query/paths gate relevance; with neither, the current working changes
	// only boost matching memories so a dirty tree still yields recency top-N.
	focusPaths := cleanPaths(paths)
	explicitSignal := query != "" || len(focusPaths) > 0
	if !explicitSignal {
		focusPaths = recallChangedFiles(ctx, dataDir, workspaceID)
	}
	focusSet := map[string]struct{}{}
	for _, p := range focusPaths {
		focusSet[p] = struct{}{}
	}
	qtokens := memoryTokens(query)
	legacyTokens, err := legacySearchTokens(ctx, dataDir, workspaceID, ranking, len(qtokens) > 0)
	if err != nil {
		return nil, 0, err
	}

	newest := ""
	for _, e := range ranking {
		if e.UpdatedAt > newest {
			newest = e.UpdatedAt
		}
	}
	hasSignal := explicitSignal && (len(qtokens) > 0 || len(focusSet) > 0)
	ranked := make([]scoredEntry, 0, len(ranking))
	for _, e := range ranking {
		// relevance = the task-match signal (lexical + path overlap). boost = trust
		// + recency, applied on top but never enough on its own to surface an
		// irrelevant memory when a query/paths signal is present.
		relevance := 0.0
		if len(qtokens) > 0 {
			etoks := legacyTokens[e.ID]
			if etoks == nil {
				etoks = tokenSet(e.SearchTokens) // precomputed; no content scan per recall
			}
			for t := range qtokens {
				if _, ok := etoks[t]; ok {
					relevance += 1.0
				}
			}
		}
		for _, p := range e.Paths {
			if _, ok := focusSet[p]; ok {
				relevance += 1.5
			}
		}
		boost := 0.25 * float64(e.Approvals)
		if e.UpdatedAt == newest && newest != "" {
			boost += 0.5
		}
		// NB: no stale penalty here — staleness is unknown until the bounded drift
		// check below; the penalty is applied to the candidate window only.
		score := relevance + boost
		if hasSignal && relevance <= 0 {
			// explicit query/paths gate relevance: irrelevant memory is dropped below.
			// Implicit working-change focus only adds to the score.
			score = -1 // sentinel: filtered out
		}
		ranked = append(ranked, scoredEntry{ContextEntry{ID: e.ID, UpdatedAt: e.UpdatedAt, ContentDigest: e.ContentDigest}, score})
	}
	sortByScore(ranked)

	// Drop relevance-gated entries, then take a BOUNDED candidate window — only these
	// are loaded and drift-checked, so recall I/O is O(window), not O(history). The
	// window is a few × limit so the stale penalty can still re-order without missing a
	// result.
	candidates := make([]scoredEntry, 0, min(len(ranked), recallCandidateWindow(limit)))
	for _, s := range ranked {
		if hasSignal && s.score < 0 {
			continue
		}
		candidates = append(candidates, s)
		if len(candidates) >= recallCandidateWindow(limit) {
			break
		}
	}
	if recallBeforeContentLoad != nil {
		recallBeforeContentLoad()
	}
	candidates, mismatched, err := loadCandidates(ctx, dataDir, workspaceID, candidates, withhold)
	if err != nil {
		return nil, 0, err
	}
	// drift-check ONLY the candidate window; apply the stale penalty, then re-rank.
	root := contextRoot(dataDir, workspaceID)
	for i := range candidates {
		computeStaleness(root, &candidates[i].entry)
		if candidates[i].entry.Stale {
			candidates[i].score -= 1.0
		}
	}
	sortByScore(candidates)

	out := make([]ContextEntry, 0, limit)
	staleCount := 0
	for _, s := range candidates {
		if s.entry.Stale {
			staleCount++
		}
		out = append(out, s.entry)
		if len(out) >= limit {
			break
		}
	}
	requireMulti, threshold := contextDefaults(dataDir)
	return map[string]any{
		"workspace_id":           workspaceID,
		"query":                  query,
		"ranked":                 hasSignal,
		"bounded":                true,
		"limit":                  limit,
		"require_multi_agent":    requireMulti,
		"verification_threshold": threshold,
		"total_active":           len(ranking),
		"active_count":           len(ranking),
		"returned":               len(out),
		"verification_modes":     countVerificationModes(out), // per-mode counts of the returned entries
		"stale_count":            staleCount,
		"drift_checked":          len(candidates), // every returned entry is in this set (checked)
		"conflicts":              overlappingMemory(out),
		"entries":                out,
		"generated_at":           nowUTC(),
	}, mismatched, nil
}

// loadCandidates replaces each ranked candidate with its full state and content, read
// in one snapshot. Content is bound to the revision the candidate was ranked with: a
// candidate that is no longer served, or now serves another revision, is mismatched.
// A mismatched candidate is dropped when withhold is set; otherwise it stays in the
// ranking without content, and the caller retries against the new state.
func loadCandidates(ctx context.Context, dataDir, workspaceID string, candidates []scoredEntry, withhold bool) ([]scoredEntry, int, error) {
	if len(candidates) == 0 {
		return candidates, 0, nil
	}
	loaded := map[string]ContextEntry{}
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		served := make([]govstore.Entry, 0, len(candidates))
		ranked := map[string]string{}
		for _, c := range candidates {
			e, err := r.GetEntry(ctx, c.entry.ID)
			if err != nil {
				return err
			}
			if e.Served(time.Now()) && e.ContentDigest == c.entry.ContentDigest {
				served = append(served, e)
				ranked[e.ID] = c.entry.ContentDigest
			}
		}
		full, err := projectEntries(ctx, r, served)
		if err != nil {
			return err
		}
		for i := range full {
			full[i].ContentDigest = ranked[full[i].ID]
		}
		bound, _, err := attachContent(ctx, r, full)
		for _, e := range bound {
			loaded[e.ID] = e
		}
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	kept, mismatched := candidates[:0], 0
	for _, c := range candidates {
		e, ok := loaded[c.entry.ID]
		if !ok {
			mismatched++
			if withhold {
				continue
			}
			e = c.entry
		}
		kept = append(kept, scoredEntry{e, c.score})
	}
	return kept, mismatched, nil
}

// legacySearchTokens tokenizes the content of served entries stored without
// precomputed SearchTokens (written before the field existed), so they still rank. It
// reads content for those entries only, and only when the query has tokens.
func legacySearchTokens(ctx context.Context, dataDir, workspaceID string, ranking []govstore.RankEntry, needed bool) (map[string]map[string]struct{}, error) {
	var ids []string
	for _, e := range ranking {
		if needed && len(e.SearchTokens) == 0 {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	out := make(map[string]map[string]struct{}, len(ids))
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		contents, err := r.EntryContents(ctx, ids)
		for id, c := range contents {
			if c.Withheld == "" {
				out[id] = memoryTokens(c.Content)
			}
		}
		return err
	})
	return out, err
}

func tokenSet(tokens []string) map[string]struct{} {
	set := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		set[t] = struct{}{}
	}
	return set
}

// stalenessChecks counts drift checks that hash referenced files — a test hook for
// asserting that grounding/recall drift work is bounded, not O(promoted history).
var stalenessChecks atomic.Int64

// defaultRecallLimit is the top-N returned when the caller passes no limit.
const defaultRecallLimit = 8

// recallChangedFiles supplies the implicit focus paths (current working changes) for
// a recall without query/paths; a variable so tests can fix the working-tree state.
var recallChangedFiles = currentChangedFiles

// recallBeforeContentLoad is a test seam that runs between recall's metadata ranking
// and its content load, so a concurrent update can be interleaved deterministically.
var recallBeforeContentLoad func()

// recallCandidateWindow bounds how many top-ranked entries get drift-checked on a
// recall, so the check is O(window) not O(history). A few × limit gives the stale
// penalty room to re-order without dropping a result it would have returned.
const recallCandidateFloor = 16

func recallCandidateWindow(limit int) int {
	return max(limit*4, recallCandidateFloor)
}

// memoryTokenizeCalls counts full-text tokenizations of memory content — a test
// hook to assert recall is metadata-first (it should not grow with promoted-history
// size once entries carry precomputed SearchTokens). Not used in production logic.
var memoryTokenizeCalls atomic.Int64

// memoryTokens lowercases and splits text into a set of word tokens for matching.
func memoryTokens(text string) map[string]struct{} {
	memoryTokenizeCalls.Add(1)
	out := map[string]struct{}{}
	for _, tok := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}) {
		if len(tok) >= 3 {
			out[tok] = struct{}{}
		}
	}
	return out
}

// memoryTokenList is memoryTokens as a deduplicated slice, for persisting an entry's
// precomputed SearchTokens.
func memoryTokenList(text string) []string {
	set := memoryTokens(text)
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// entrySearchTokenSet returns the entry's relevance token set, using the persisted
// SearchTokens when present (no content scan) and falling back to tokenizing
// title+content only for legacy entries written before the field existed.
func entrySearchTokenSet(e *ContextEntry) map[string]struct{} {
	if len(e.SearchTokens) > 0 {
		return tokenSet(e.SearchTokens)
	}
	return memoryTokens(e.Title + " " + e.Content)
}

// MemoryConflict flags a file that two or more active memories reference, so the
// agent can reconcile them before trusting either. Detection is path-overlap only;
// it does not compare content for semantic contradictions.
type MemoryConflict struct {
	Path     string   `json:"path"`
	EntryIDs []string `json:"entry_ids"`
	Titles   []string `json:"titles"`
}

func overlappingMemory(entries []ContextEntry) []MemoryConflict {
	byPath := map[string][]ContextEntry{}
	for _, e := range entries {
		for _, p := range e.Paths {
			byPath[p] = append(byPath[p], e)
		}
	}
	out := []MemoryConflict{}
	for p, es := range byPath {
		if len(es) < 2 {
			continue
		}
		c := MemoryConflict{Path: p}
		for _, e := range es {
			c.EntryIDs = append(c.EntryIDs, e.ID)
			c.Titles = append(c.Titles, fallbackString(e.Title, e.ID))
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

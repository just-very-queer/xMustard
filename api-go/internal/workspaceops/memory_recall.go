package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
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

// promotedFilter keeps what is served: promoted, active and unexpired. An expired
// entry is hidden, never deleted (PAR-GOV-12); recall(entry_id) still fetches it.
func promotedFilter(f *govstore.EntryFilter) { f.ServedOnly = true }

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

// RecallContext is ranked, query-aware recall of served memory — RecallWith with only
// a query, focus paths and a limit.
func RecallContext(dataDir, workspaceID, query string, paths []string, limit int) (map[string]any, error) {
	return RecallContextCtx(context.Background(), dataDir, workspaceID, query, paths, limit)
}

// RecallContextCtx is RecallContext bound to the request context, so the working-change
// lookup's Rust child is killed when the caller cancels.
func RecallContextCtx(ctx context.Context, dataDir, workspaceID, query string, paths []string, limit int) (map[string]any, error) {
	return RecallWith(ctx, dataDir, workspaceID, RecallRequest{Query: query, Paths: paths, Limit: limit})
}

// RecallRequest is one recall (WS-20). Filters combine with AND across fields and OR
// within a list; status and the include flags pick the rank states read.
type RecallRequest struct {
	Query string
	Paths []string
	Limit int
	// Explain adds score_details (the signed signals and their reasons) to each entry.
	Explain bool
	Kinds   []string
	Tags    []string
	// Topic matches the topic or any topic under it ("a/b" matches "a/b/c").
	Topic      string
	PathPrefix string
	// Since and Until bound updated_at: a UTC date (YYYY-MM-DD) or an RFC 3339 time.
	Since, Until string
	// By keeps entries authored by this principal.
	By string
	// Status is "" or promoted (served memory), pending (the verification queue) or
	// awaiting_me (pending entries the caller neither authored nor voted on).
	Status            string
	IncludePending    bool
	IncludeSuperseded bool
	ShowExpired       bool
	// Cursor continues a previous page (next_cursor).
	Cursor string
	// NamesOnly returns ids, titles, topic, state, stale and paths only.
	NamesOnly bool
	// Render is full (default) or compact (one line per entry).
	Render string
	// MaxChars budgets the whole JSON result; 0 leaves it unbudgeted.
	MaxChars int
	// SessionID enables session-seen suppression: an entry already returned to this
	// caller's session is left out until its content, stale or state changes. At most
	// RecallMaxSessionID bytes.
	SessionID string
	// Caller is the principal recalling, for awaiting_me and the seen-set key.
	Caller string
}

// Recall output budget bounds, in characters of the JSON result (Mem0's default).
const (
	RecallDefaultMaxChars = 4000
	RecallMinMaxChars     = 1000
	RecallMaxMaxChars     = 10000
)

// recallStatuses maps a status to the rank states it reads.
var recallStatuses = map[string]func(RecallRequest) []string{
	"":            servedStates,
	"promoted":    servedStates,
	"pending":     pendingStates,
	"awaiting_me": pendingStates,
}

// servedStates is served memory plus the states the include flags opt into.
func servedStates(r RecallRequest) []string {
	states := []string{govstore.RankServed}
	for _, opt := range []struct {
		on    bool
		state string
	}{{r.IncludePending, govstore.RankPending}, {r.IncludeSuperseded, govstore.RankSuperseded}, {r.ShowExpired, govstore.RankExpired}} {
		if opt.on {
			states = append(states, opt.state)
		}
	}
	return states
}

func pendingStates(RecallRequest) []string { return []string{govstore.RankPending} }

// States is the rank states the request reads, or an error for an unknown status.
func (r RecallRequest) States() ([]string, error) {
	pick, ok := recallStatuses[r.Status]
	if !ok {
		return nil, fmt.Errorf("status %q is not promoted, pending or awaiting_me: %w", r.Status, ErrInvalidInput)
	}
	return pick(r), nil
}

// ReadsUnverified reports whether the request can return pending (unverified) text,
// which only a reviewer may read.
func (r RecallRequest) ReadsUnverified() bool {
	states, _ := r.States()
	return slices.Contains(states, govstore.RankPending)
}

// validate checks the arguments that are rejected, never clamped, and normalizes the
// topic the way remember stores it.
func (r *RecallRequest) validate() error {
	if _, err := r.States(); err != nil {
		return err
	}
	if len(r.SessionID) > RecallMaxSessionID {
		return fmt.Errorf("session_id is longer than %d bytes: %w", RecallMaxSessionID, ErrInvalidInput)
	}
	r.Topic = normalizeTopic(r.Topic)
	if !slices.Contains(recallRenderArgs, r.Render) {
		return fmt.Errorf("render %q is not full or compact: %w", r.Render, ErrInvalidInput)
	}
	if r.MaxChars != 0 && (r.MaxChars < RecallMinMaxChars || r.MaxChars > RecallMaxMaxChars) {
		return fmt.Errorf("max_chars %d is outside %d..%d: %w", r.MaxChars, RecallMinMaxChars, RecallMaxMaxChars, ErrInvalidInput)
	}
	for _, b := range []*string{&r.Since, &r.Until} {
		t, err := parseRecallTime(*b)
		if err != nil {
			return err
		}
		*b = t
	}
	return nil
}

// normalizeTopic is a topic as remember stores it and recall matches it: trimmed, with
// no leading or trailing "/".
func normalizeTopic(topic string) string { return strings.Trim(strings.TrimSpace(topic), "/") }

// parseRecallTime reads a since/until bound as the store's canonical time text.
func parseRecallTime(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(storeTimeLayout), nil
		}
	}
	return "", fmt.Errorf("time %q is not a date or RFC 3339 time: %w", s, ErrInvalidInput)
}

// storeTimeLayout is govstore's canonical time text: fixed width, so it orders as text.
const storeTimeLayout = "2006-01-02T15:04:05.000000000Z"

// filters are the request's entry predicates, one per given field. awaits decides
// awaiting_me (see awaitingCaller).
func (r RecallRequest) filters(awaits func(govstore.RankEntry) bool) []func(govstore.RankEntry) bool {
	var out []func(govstore.RankEntry) bool
	add := func(given bool, f func(govstore.RankEntry) bool) {
		if given {
			out = append(out, f)
		}
	}
	add(len(r.Kinds) > 0, func(e govstore.RankEntry) bool { return slices.Contains(r.Kinds, e.Kind) })
	add(len(r.Tags) > 0, func(e govstore.RankEntry) bool {
		return slices.ContainsFunc(e.Tags, func(t string) bool { return slices.Contains(r.Tags, t) })
	})
	add(r.Topic != "", func(e govstore.RankEntry) bool {
		return e.Topic == r.Topic || strings.HasPrefix(e.Topic, r.Topic+"/")
	})
	add(r.PathPrefix != "", func(e govstore.RankEntry) bool {
		return slices.ContainsFunc(e.Paths, func(p string) bool { return strings.HasPrefix(p, r.PathPrefix) })
	})
	add(r.Since != "", func(e govstore.RankEntry) bool { return e.UpdatedAt >= r.Since })
	add(r.Until != "", func(e govstore.RankEntry) bool { return e.UpdatedAt < r.Until })
	add(r.By != "", func(e govstore.RankEntry) bool {
		return strings.EqualFold(strings.TrimSpace(e.Author), strings.TrimSpace(r.By))
	})
	add(r.Status == "awaiting_me", awaits)
	return out
}

// awaitingCaller reports whether a pending entry awaits caller's verdict: caller
// neither wrote it nor voted on its revision and, under the owner-distinct policy on a
// peer-gated entry, it was not written under caller's owner either (RecordVote would
// refuse that vote with ErrSameOwner). An edit by a same-owner principal, or a sibling
// token's earlier vote, is refused only when the vote is cast.
func awaitingCaller(dataDir, caller string) func(govstore.RankEntry) bool {
	fresh := func(e govstore.RankEntry) bool { return !e.ByCaller && !e.VotedByCaller }
	if principalDistinctness(dataDir) != DistinctOwner {
		return fresh
	}
	owners := map[string]string{} // principal -> token-store owner, read once each
	ownerOf := func(recorded, principal string) string {
		if o := strings.TrimSpace(recorded); o != "" {
			return o
		}
		if _, ok := owners[principal]; !ok {
			owners[principal] = PrincipalOwner(dataDir, principal)
		}
		return owners[principal]
	}
	mine := ownerOf("", caller)
	return func(e govstore.RankEntry) bool {
		return fresh(e) && (e.RequiredVerifications <= 1 || !sameOwner(ownerOf(e.AuthorOwner, e.Author), mine))
	}
}

// RecallWith runs one recall. Content is bound to the exact revision whose metadata was
// ranked: a concurrent edit between the metadata pass and the content load yields a
// digest mismatch, and the whole recall is retried against the new state. If the store
// keeps moving, mismatched entries are withheld (fail closed) and counted.
func RecallWith(ctx context.Context, dataDir, workspaceID string, req RecallRequest) (map[string]any, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	var res map[string]any
	var page recallPage
	for attempt := 1; attempt <= recallConsistencyAttempts; attempt++ {
		var mismatched int
		var err error
		res, page, mismatched, err = recallOnce(ctx, dataDir, workspaceID, req)
		if err != nil {
			return nil, err
		}
		res["consistency_attempts"] = attempt
		if mismatched == 0 {
			break
		}
		res["consistency_withheld"] = mismatched
	}
	return finishRecall(workspaceID, req, res, page), nil
}

// recallConsistencyAttempts bounds how often recall re-reads when content changed
// under it before withholding the mismatched entries.
const recallConsistencyAttempts = 3

type scoredEntry struct {
	entry   ContextEntry
	rank    govstore.RankEntry
	details ScoreDetails
	// withheld: the entry changed between ranking and loading, so it is not returned.
	withheld bool
}

// sortByScore orders by score, then the newest first, then by id: a total order, so
// ranking and pagination are deterministic.
func sortByScore(s []scoredEntry) {
	sort.SliceStable(s, func(a, b int) bool {
		x, y := s[a], s[b]
		if x.details.Total != y.details.Total {
			return x.details.Total > y.details.Total
		}
		if x.rank.UpdatedAt != y.rank.UpdatedAt {
			return x.rank.UpdatedAt > y.rank.UpdatedAt
		}
		return x.rank.ID < y.rank.ID
	})
}

// recallRanking is the ranked, filtered and relevance-gated view one recall pass pages.
type recallRanking struct {
	ranked    []scoredEntry
	hasSignal bool
	// read counts the entries read in the request's states, before filters and gating.
	read int
}

// rankRecall reads the ranking view in the request's states, filters it, and scores
// every entry (content-free); the stale penalty is left to the candidate window.
func rankRecall(ctx context.Context, dataDir, workspaceID string, req RecallRequest) (recallRanking, error) {
	states, err := req.States()
	if err != nil {
		return recallRanking{}, err
	}
	qtokens := memoryTokens(req.Query)
	var ranking []govstore.RankEntry
	var bm25 map[string]float64
	err = memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		if ranking, err = r.Ranking(ctx, govstore.RankQuery{WorkspaceID: workspaceID, States: states, Caller: req.Caller}); err != nil {
			return err
		}
		if len(qtokens) > 0 {
			bm25, err = r.MemoryScores(ctx, workspaceID, strings.Join(slices.Sorted(maps.Keys(qtokens)), " "))
		}
		return err
	})
	if err != nil {
		return recallRanking{}, err
	}
	// path signal: explicit query paths, else the files currently being worked on.
	// Explicit query/paths gate relevance; with neither, the current working changes
	// only boost matching memories so a dirty tree still yields recency top-N.
	focus := cleanPaths(req.Paths)
	explicit := req.Query != "" || len(focus) > 0
	if !explicit {
		focus = recallChangedFiles(ctx, dataDir, workspaceID)
	}
	hasSignal := explicit && (len(qtokens) > 0 || len(focus) > 0)
	var awaits func(govstore.RankEntry) bool
	if req.Status == "awaiting_me" {
		awaits = awaitingCaller(dataDir, req.Caller)
	}
	filters := req.filters(awaits)
	read := len(ranking)
	// the signals are scaled over what the caller can see: an entry in another state,
	// or filtered out, never sets the bm25 scale or the recency origin
	visible := slices.DeleteFunc(ranking, func(e govstore.RankEntry) bool {
		return slices.ContainsFunc(filters, func(keep func(govstore.RankEntry) bool) bool { return !keep(e) })
	})
	sig := newRecallSignals(visible, bm25, focus)
	out := recallRanking{hasSignal: hasSignal, read: read, ranked: make([]scoredEntry, 0, len(visible))}
	for _, e := range visible {
		d := sig.score(e)
		// explicit query/paths gate relevance: irrelevant memory is dropped.
		// Implicit working-change focus only adds to the score.
		if hasSignal && d.relevance() < minRelevance {
			continue
		}
		out.ranked = append(out.ranked, scoredEntry{entry: ContextEntry{ID: e.ID, UpdatedAt: e.UpdatedAt, ContentDigest: e.ContentDigest}, rank: e, details: d})
	}
	sortByScore(out.ranked)
	return out, nil
}

// recallOnce is one ranked-recall pass. It reports how many candidates changed between
// ranking and loading (their revision or rank state moved); those are flagged withheld
// and never returned, and the caller retries while it has attempts left.
func recallOnce(ctx context.Context, dataDir, workspaceID string, req RecallRequest) (map[string]any, recallPage, int, error) {
	// metadata-first: rank on the lean ranking view (no content, no vote rows), so
	// recall's reads and allocation don't scale with content size; full state and
	// content are loaded only for the bounded candidate window below (XM-PRO-010).
	rk, err := rankRecall(ctx, dataDir, workspaceID, req)
	if err != nil {
		return nil, recallPage{}, 0, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	cur, err := req.decodeCursor(workspaceID)
	if err != nil {
		return nil, recallPage{}, 0, err
	}
	if cur.offset > len(rk.ranked) {
		return nil, recallPage{}, 0, fmt.Errorf("cursor offset %d is past the %d ranked entries (memory changed since that page); recall again without cursor: %w",
			cur.offset, len(rk.ranked), ErrInvalidInput)
	}
	if cur.block == 0 {
		cur.block = recallCandidateWindow(limit) // the first page fixes the block size for every later page
	}
	// Load a BOUNDED window: the whole blocks the page may scan, so recall I/O is
	// O(block + limit) whatever the offset or history size.
	span := pageSpan(cur, limit, len(rk.ranked))
	candidates := slices.Clone(rk.ranked[span.lo:span.hi])
	if recallBeforeContentLoad != nil {
		recallBeforeContentLoad()
	}
	mismatched, err := loadCandidates(ctx, dataDir, workspaceID, candidates)
	if err != nil {
		return nil, recallPage{}, 0, err
	}
	// drift-check ONLY the window; apply the stale penalty, then re-rank each block.
	root := contextRoot(dataDir, workspaceID)
	for i := range candidates {
		c := &candidates[i]
		computeStaleness(root, &c.entry)
		c.details.stale(c.entry.StalePaths)
	}
	for i := 0; i < len(candidates); i += cur.block {
		sortByScore(candidates[i:min(len(candidates), i+cur.block)])
	}

	requireMulti, threshold := contextDefaults(dataDir)
	return map[string]any{
		"workspace_id":           workspaceID,
		"query":                  req.Query,
		"status":                 fallbackString(req.Status, "promoted"),
		"ranked":                 rk.hasSignal,
		"bounded":                true,
		"limit":                  limit,
		"require_multi_agent":    requireMulti,
		"verification_threshold": threshold,
		"total_active":           rk.read,
		"active_count":           rk.read,
		"total_matches":          len(rk.ranked),
		"drift_checked":          len(candidates), // every returned entry is in this set (checked)
		"generated_at":           nowUTC(),
	}, recallPage{candidates: candidates, span: span, cursor: cur, limit: limit, total: len(rk.ranked)}, mismatched, nil
}

// recallSpan is the part of a ranking one page reads: it loads [lo, hi) and scans
// positions [offset, end) for the page's entries.
type recallSpan struct{ lo, hi, offset, end int }

// pageSpan covers the positions a page scans (limit entries, with room to skip what
// the session was already shown) with whole blocks. The stale penalty re-orders an
// entry only within its block, so every block is loaded whole and the order of the
// ranking does not depend on which page reads it: pages never repeat or skip an entry.
func pageSpan(cur recallCursor, limit, total int) recallSpan {
	if cur.offset >= total {
		return recallSpan{lo: total, hi: total, offset: total, end: total}
	}
	end := min(total, cur.offset+max(cur.block, recallCandidateWindow(limit)))
	return recallSpan{
		lo: cur.offset / cur.block * cur.block, hi: min(total, (end+cur.block-1)/cur.block*cur.block),
		offset: cur.offset, end: end,
	}
}

// loadCandidates replaces each ranked candidate with its full state and content, read
// in one snapshot. Content is bound to the revision the candidate was ranked with: a
// candidate that left its rank state, or now carries another revision, is mismatched,
// flagged withheld and left without content. It returns how many were mismatched.
func loadCandidates(ctx context.Context, dataDir, workspaceID string, candidates []scoredEntry) (int, error) {
	if len(candidates) == 0 {
		return 0, nil
	}
	loaded := map[string]ContextEntry{}
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		bound := make([]govstore.Entry, 0, len(candidates))
		ranked := map[string]string{}
		now := time.Now()
		for _, c := range candidates {
			e, err := r.GetEntry(ctx, c.entry.ID)
			if err != nil {
				return err
			}
			if e.RankState(now) == c.rank.State && e.ContentDigest == c.entry.ContentDigest {
				bound = append(bound, e)
				ranked[e.ID] = c.entry.ContentDigest
			}
		}
		full, err := projectEntries(ctx, r, bound)
		if err != nil {
			return err
		}
		for i := range full {
			full[i].ContentDigest = ranked[full[i].ID]
		}
		withContent, _, err := attachContent(ctx, r, full)
		for _, e := range withContent {
			loaded[e.ID] = e
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	mismatched := 0
	for i := range candidates {
		c := &candidates[i]
		e, ok := loaded[c.entry.ID]
		if !ok {
			mismatched++
			c.withheld = true
			continue
		}
		c.entry = e
	}
	return mismatched, nil
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

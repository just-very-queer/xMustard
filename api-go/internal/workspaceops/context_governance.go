package workspaceops

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Context governance: the trust layer for the MCP context engine. Agents don't
// write straight into shared context — they PROPOSE an entry, which other agents
// VERIFY. An entry is promoted into the active (shared) context only once enough
// distinct agents have approved it. Entries carry a permission: "readonly" entries
// can never be mutated after promotion (only superseded by a new proposal). A
// per-workspace/global toggle decides whether multi-agent verification is required
// at all — "run it through multiple agents, or not".

type ContextVerification struct {
	Agent   string `json:"agent"`
	Approve bool   `json:"approve"`
	Note    string `json:"note,omitempty"`
	At      string `json:"at"`
}

type ContextEntry struct {
	ID                    string                `json:"id"`
	WorkspaceID           string                `json:"workspace_id"`
	Title                 string                `json:"title"`
	Content               string                `json:"content"`
	Source                string                `json:"source"`     // proposing agent/actor
	Permission            string                `json:"permission"` // readonly | readwrite
	Status                string                `json:"status"`     // pending | verified | rejected
	Promoted              bool                  `json:"promoted"`   // visible in active shared context
	Verifications         []ContextVerification `json:"verifications"`
	RequiredVerifications int                   `json:"required_verifications"`
	CreatedAt             string                `json:"created_at"`
	UpdatedAt             string                `json:"updated_at"`
	// Paths the memory is ABOUT. PathHashes captures their content hash at the
	// moment the entry was promoted, so recall can detect drift: if a referenced
	// file changed since the memory was verified, the memory may be stale.
	Paths      []string          `json:"paths,omitempty"`
	PathHashes map[string]string `json:"path_hashes,omitempty"`
	// Stale / StalePaths are computed at read time (drift-on-recall), never stored.
	Stale      bool     `json:"stale,omitempty"`
	StalePaths []string `json:"stale_paths,omitempty"`
}

type ProposeContextRequest struct {
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Source     string   `json:"source"`
	Permission string   `json:"permission"`
	Paths      []string `json:"paths,omitempty"`
	// RequireVerification overrides the workspace default: when explicitly false,
	// the entry is promoted immediately (single-agent mode); when true, it needs
	// the multi-agent threshold. nil → use the workspace/global setting.
	RequireVerification *bool `json:"require_verification,omitempty"`
}

// safeIDPattern rejects anything that could escape the data dir or be a path
// traversal: only alphanumerics, dash, underscore, dot (with ".." rejected).
var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validateSafeID(kind, id string) error {
	if id == "" || strings.Contains(id, "..") || strings.ContainsRune(id, 0) || !safeIDPattern.MatchString(id) {
		return fmt.Errorf("invalid %s id", kind)
	}
	return nil
}

// hashFileContent returns the sha256 of a repo-relative file's current content.
// The path is confined to the workspace root (no `..`/absolute/symlink escape),
// must be a regular file, and is size-capped — so a memory reference cannot become
// an arbitrary host-file read/hash oracle or an I/O DoS (XM-NEW-002).
func hashFileContent(root, rel string) (string, bool) {
	data, ok := readWorkspaceRegularFile(root, rel)
	if !ok {
		return "", false
	}
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

func contextEntriesPath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "context_entries.json")
}

func loadContextEntries(dataDir, workspaceID string) ([]ContextEntry, error) {
	var entries []ContextEntry
	if err := readJSON(contextEntriesPath(dataDir, workspaceID), &entries); err != nil {
		if os.IsNotExist(err) {
			return []ContextEntry{}, nil
		}
		return nil, err
	}
	return entries, nil
}

func saveContextEntries(dataDir, workspaceID string, entries []ContextEntry) error {
	return writeJSON(contextEntriesPath(dataDir, workspaceID), entries)
}

// contextDefaults returns (requireMultiAgent, threshold) from settings.
func contextDefaults(dataDir string) (bool, int) {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return true, 2
	}
	require := true
	if settings.RequireMultiAgentVerification != nil {
		require = *settings.RequireMultiAgentVerification
	}
	threshold := settings.ContextVerificationThreshold
	if threshold <= 0 {
		threshold = 2
	}
	return require, threshold
}

// distinctApprovals counts unique agents that approved (latest verdict per agent).
// excludeAgent, when non-empty, drops that agent's vote from the tally — used to
// stop a proposer from self-approving toward a multi-agent threshold.
func distinctApprovals(verifications []ContextVerification, excludeAgent string) (approvals, rejections int) {
	latest := map[string]bool{}
	for _, v := range verifications {
		agent := strings.TrimSpace(v.Agent)
		if agent == "" {
			continue
		}
		latest[agent] = v.Approve
	}
	for agent, approve := range latest {
		if excludeAgent != "" && strings.EqualFold(agent, excludeAgent) {
			continue // author's own vote does not count toward a multi-agent gate
		}
		if approve {
			approvals++
		} else {
			rejections++
		}
	}
	return approvals, rejections
}

// promote/demote an entry based on its verifications vs its threshold.
func reconcileEntry(entry *ContextEntry) {
	// In multi-agent mode (threshold > 1) the author cannot count as one of the
	// required verifiers; single-agent mode (threshold 1) is the operator opting
	// out, so the proposer's self-assertion is allowed to promote.
	exclude := ""
	if entry.RequiredVerifications > 1 {
		exclude = entry.Source
	}
	approvals, rejections := distinctApprovals(entry.Verifications, exclude)
	switch {
	case rejections >= entry.RequiredVerifications && rejections > 0:
		entry.Status = "rejected"
		entry.Promoted = false
	case approvals >= entry.RequiredVerifications:
		entry.Status = "verified"
		entry.Promoted = true
	default:
		entry.Status = "pending"
		entry.Promoted = false
	}
}

// ProposeContext creates a pending context entry. In single-agent mode (multi-agent
// verification not required) it is promoted immediately.
func ProposeContext(dataDir, workspaceID string, req ProposeContextRequest) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("content is required")
	}
	permission := strings.ToLower(strings.TrimSpace(req.Permission))
	if permission != "readwrite" {
		permission = "readonly" // default to the safest permission
	}
	requireMulti, threshold := contextDefaults(dataDir)
	// A per-request override may only TIGHTEN the gate (force multi-agent ON),
	// never loosen it. Otherwise an untrusted proposer could pass
	// require_verification:false to self-promote and poison the shared context
	// that gets injected into every agent run. Single-agent mode is an operator
	// SETTING (require_multi_agent_verification), not a per-request choice.
	if req.RequireVerification != nil && *req.RequireVerification {
		requireMulti = true
	}
	required := threshold
	if !requireMulti {
		required = 1
	}

	// Serialize the whole load→mutate→save so concurrent proposals don't lose updates.
	unlock := lockStore(contextEntriesPath(dataDir, workspaceID))
	defer unlock()
	entries, err := loadContextEntries(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	entry := ContextEntry{
		ID:                    "ctx_" + hashID(workspaceID, req.Title, req.Content+now)[:12],
		WorkspaceID:           workspaceID,
		Title:                 strings.TrimSpace(req.Title),
		Content:               req.Content,
		Source:                fallbackString(strings.TrimSpace(req.Source), "unknown"),
		Permission:            permission,
		Verifications:         []ContextVerification{},
		RequiredVerifications: required,
		CreatedAt:             now,
		UpdatedAt:             now,
		Paths:                 cleanPaths(req.Paths),
	}
	if !requireMulti {
		// single-agent mode: the proposer's own assertion promotes it.
		entry.Verifications = append(entry.Verifications, ContextVerification{
			Agent: entry.Source, Approve: true, Note: "single-agent mode", At: now,
		})
	}
	reconcileEntry(&entry)
	if entry.Promoted {
		// snapshot the referenced files so drift-on-recall has a baseline, and boost
		// the verified paths in the agent-feedback layer (single-agent immediate promote).
		entry.PathHashes = capturePathHashes(contextRoot(dataDir, workspaceID), entry.Paths)
		_ = RecordFeedback(dataDir, workspaceID, "verify", entry.Paths)
	}
	entries = append(entries, entry)
	if err := saveContextEntries(dataDir, workspaceID, entries); err != nil {
		return nil, err
	}
	return &entry, nil
}

// VerifyContext records an agent's verdict on an entry and re-promotes if the
// approval threshold is now met. Distinct agents only — a single agent cannot
// satisfy a multi-agent gate by voting twice.
func VerifyContext(dataDir, workspaceID, entryID, agent string, approve bool, note string) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return nil, fmt.Errorf("agent is required")
	}
	// Serialize the verify/promote transaction so concurrent votes can't drop each
	// other (the multi-agent promotion gate depends on this).
	unlock := lockStore(contextEntriesPath(dataDir, workspaceID))
	defer unlock()
	entries, err := loadContextEntries(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	idx := -1
	for i := range entries {
		if entries[i].ID == entryID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, os.ErrNotExist
	}
	entry := &entries[idx]
	now := nowUTC()
	// replace this agent's prior verdict if it exists (distinct-agent semantics)
	replaced := false
	for i := range entry.Verifications {
		if strings.EqualFold(entry.Verifications[i].Agent, agent) {
			entry.Verifications[i] = ContextVerification{Agent: agent, Approve: approve, Note: note, At: now}
			replaced = true
			break
		}
	}
	if !replaced {
		entry.Verifications = append(entry.Verifications, ContextVerification{Agent: agent, Approve: approve, Note: note, At: now})
	}
	entry.UpdatedAt = now
	reconcileEntry(entry)
	if entry.Promoted && len(entry.PathHashes) == 0 {
		// just transitioned to promoted — snapshot referenced files for drift checks,
		// and boost the verified paths in the agent-feedback layer.
		entry.PathHashes = capturePathHashes(contextRoot(dataDir, workspaceID), entry.Paths)
		_ = RecordFeedback(dataDir, workspaceID, "verify", entry.Paths)
	}
	if err := saveContextEntries(dataDir, workspaceID, entries); err != nil {
		return nil, err
	}
	return entry, nil
}

// cleanPaths trims, drops empties, and de-duplicates referenced paths.
func cleanPaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimPrefix(strings.TrimSpace(p), "./")
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// UpdateContextContent amends an entry's content. Readonly entries that are
// already verified/promoted reject edits — they can only be superseded by a new
// proposal (this is the "readonly" permission guarantee).
func UpdateContextContent(dataDir, workspaceID, entryID, content string) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	unlock := lockStore(contextEntriesPath(dataDir, workspaceID))
	defer unlock()
	entries, err := loadContextEntries(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	idx := -1
	for i := range entries {
		if entries[i].ID == entryID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, os.ErrNotExist
	}
	entry := &entries[idx]
	if entry.Permission == "readonly" && (entry.Promoted || entry.Status == "verified") {
		return nil, fmt.Errorf("entry %s is readonly and verified; propose a new entry to supersede it", entryID)
	}
	entry.Content = content
	entry.UpdatedAt = nowUTC()
	// a content change resets verification — re-approval is required — AND must
	// discard the old drift baseline, so re-promotion captures a fresh snapshot
	// against the current files instead of reusing the previous assertion's
	// evidence (XM-NEW-004).
	entry.Verifications = []ContextVerification{}
	entry.PathHashes = nil
	entry.Stale = false
	entry.StalePaths = nil
	reconcileEntry(entry)
	if err := saveContextEntries(dataDir, workspaceID, entries); err != nil {
		return nil, err
	}
	return entry, nil
}

// ListContextEntries returns entries filtered by status: "" / "all", "pending",
// "promoted"/"active", "rejected".
func ListContextEntries(dataDir, workspaceID, filter string) ([]ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	entries, err := loadContextEntries(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	filter = strings.ToLower(strings.TrimSpace(filter))
	out := make([]ContextEntry, 0, len(entries))
	for _, e := range entries {
		switch filter {
		case "", "all":
			out = append(out, e)
		case "pending":
			if e.Status == "pending" {
				out = append(out, e)
			}
		case "promoted", "active", "verified":
			if e.Promoted {
				out = append(out, e)
			}
		case "rejected":
			if e.Status == "rejected" {
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
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
	promoted, err := ListContextEntries(dataDir, workspaceID, "promoted")
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 8
	}
	root := contextRoot(dataDir, workspaceID)
	for i := range promoted {
		computeStaleness(root, &promoted[i])
	}

	// path signal: explicit query paths, else the files currently being worked on.
	focusPaths := cleanPaths(paths)
	if len(focusPaths) == 0 && query == "" {
		focusPaths = currentChangedFiles(dataDir, workspaceID)
	}
	focusSet := map[string]struct{}{}
	for _, p := range focusPaths {
		focusSet[p] = struct{}{}
	}
	qtokens := memoryTokens(query)

	type scored struct {
		entry ContextEntry
		score float64
	}
	ranked := make([]scored, 0, len(promoted))
	newest := ""
	for _, e := range promoted {
		if e.UpdatedAt > newest {
			newest = e.UpdatedAt
		}
	}
	hasSignal := len(qtokens) > 0 || len(focusSet) > 0
	for _, e := range promoted {
		// relevance = the task-match signal (lexical + path overlap). boost = trust
		// + recency, applied on top but never enough on its own to surface an
		// irrelevant memory when a query/paths signal is present.
		relevance := 0.0
		if len(qtokens) > 0 {
			etoks := memoryTokens(e.Title + " " + e.Content)
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
		boost := 0.0
		approvals, _ := distinctApprovals(e.Verifications, "")
		boost += 0.25 * float64(approvals)
		if e.UpdatedAt == newest && newest != "" {
			boost += 0.5
		}
		if e.Stale {
			boost -= 1.0
		}
		score := boost
		if hasSignal {
			// gate on relevance: irrelevant memory is dropped below.
			if relevance <= 0 {
				score = -1 // sentinel: filtered out
			} else {
				score = relevance + boost
			}
		}
		ranked = append(ranked, scored{e, score})
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		if ranked[a].score != ranked[b].score {
			return ranked[a].score > ranked[b].score
		}
		return ranked[a].entry.UpdatedAt > ranked[b].entry.UpdatedAt
	})
	out := make([]ContextEntry, 0, limit)
	staleCount := 0
	for _, s := range ranked {
		if hasSignal && s.score < 0 {
			continue
		}
		if s.entry.Stale {
			staleCount++
		}
		out = append(out, s.entry)
		if len(out) >= limit {
			break
		}
	}
	return map[string]any{
		"workspace_id": workspaceID,
		"query":        query,
		"ranked":       hasSignal,
		"total_active": len(promoted),
		"returned":     len(out),
		"stale_count":  staleCount,
		"conflicts":    overlappingMemory(out),
		"entries":      out,
		"generated_at": nowUTC(),
	}, nil
}

// memoryTokens lowercases and splits text into a set of word tokens for matching.
func memoryTokens(text string) map[string]struct{} {
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

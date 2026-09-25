package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
)

// Context governance: the trust layer for the MCP context engine. Agents don't
// write straight into shared context — they PROPOSE an entry, which other agents
// VERIFY. An entry is promoted into the active (shared) context only once enough
// distinct agents have approved it. Entries carry a permission: "readonly" entries
// can never be mutated after promotion (only superseded by a new proposal). A
// per-workspace/global toggle decides whether multi-agent verification is required
// at all — "run it through multiple agents, or not".

// Verification modes record HOW a promoted memory earned its place in shared
// context, so an agent can weigh a peer-verified fact above a self-asserted one.
const (
	// VerificationPeer: a quorum of distinct authenticated principals other than the
	// author approved it.
	VerificationPeer = "peer_verified"
	// VerificationSelfAssertedOpen: promoted on its author's own word because the API
	// runs without credentials (open mode). Every caller is then OpenModeIdentity, so
	// no peer quorum can form.
	VerificationSelfAssertedOpen = "self_asserted_open_mode"
	// VerificationSingleAgent: promoted on the author's own authenticated word because
	// the operator opted out of multi-agent verification.
	VerificationSingleAgent = "single_agent"
)

// OpenModeIdentity is the one identity every unauthenticated caller collapses to when
// no credentials are configured, so fabricated names cannot pose as distinct verifiers.
const OpenModeIdentity = "anonymous"

// ErrNotEntryAuthor: only an entry's author or an admin may amend its content.
var ErrNotEntryAuthor = errors.New("only the entry's author or an admin may edit it")

// ErrReadonlyVerified: a readonly entry that is already verified can only be superseded
// by a new proposal. UpdateContextContent wraps it in a Conflict, so HTTP returns 409.
var ErrReadonlyVerified = errors.New("readonly entry is verified")

// IsOpenModeIdentity reports whether id is the open-mode identity, compared the way
// verification tallies compare agents (trimmed, case-insensitive). The identity is
// reserved: no token may be minted for it.
func IsOpenModeIdentity(id string) bool {
	return strings.EqualFold(strings.TrimSpace(id), OpenModeIdentity)
}

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
	// RequireVerification records that the proposal asked for peer verification
	// (require_verification:true), so an open-mode assertion never promotes it.
	RequireVerification bool `json:"require_verification,omitempty"`
	// VerificationMode is the trust basis of a promoted entry: peer_verified,
	// self_asserted_open_mode or single_agent. Empty while pending or rejected. Entries
	// written before the field existed are labelled on read (verificationMode).
	VerificationMode string `json:"verification_mode"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	// Paths the memory is ABOUT. PathHashes captures their content hash at the
	// moment the entry was promoted, so recall can detect drift: if a referenced
	// file changed since the memory was verified, the memory may be stale.
	Paths      []string          `json:"paths,omitempty"`
	PathHashes map[string]string `json:"path_hashes,omitempty"`
	// SearchTokens is the precomputed, deduplicated relevance token set for
	// title+content, persisted at write time so recall ranks on it WITHOUT
	// re-tokenizing every entry's full content on every call (the O(history) CPU /
	// allocation hot spot, XM-PRO-010). Empty on legacy entries → computed lazily.
	SearchTokens []string `json:"search_tokens,omitempty"`
	// ContentHash is a DERIVED transport field: it is never set on the source entry
	// (stays empty there), only populated from the recall meta cache so recall can
	// locate the entry's hash-named content file. See hashContent / loadWindowContent.
	ContentHash string `json:"content_hash,omitempty"`
	// ContentDigest is the full SHA-256 of the content version the metadata describes.
	// Like ContentHash it is DERIVED (meta cache / source scan only) and cleared before
	// entries are returned; recall's trust binding compares against it, never against
	// the 64-bit filename hash.
	ContentDigest string `json:"content_digest,omitempty"`
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
	// RequireVerification can only TIGHTEN the gate: true requires the multi-agent
	// threshold even in single-agent or open mode; false or nil uses the setting.
	RequireVerification *bool `json:"require_verification,omitempty"`
	// OpenMode is set by the HTTP layer, never decoded from a client, when the caller
	// is unauthenticated because no credentials are configured. The proposal is then
	// promoted at once as self_asserted_open_mode instead of waiting for a quorum that
	// a single identity can never form.
	OpenMode bool `json:"-"`
}

// ContextActor is who writes to an entry. ID is recorded as its author or verifier.
// Admin may edit any entry's content; anyone else only an entry whose Source is exactly
// their ID. OpenMode marks a write made with no credentials configured (ID is then
// OpenModeIdentity): open mode is a property of each write, not of the entry.
type ContextActor struct {
	ID       string
	Admin    bool
	OpenMode bool
}

// safeIDPattern rejects anything that could escape the data dir or be a path
// traversal: only alphanumerics, dash, underscore, dot (with ".." rejected).
var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validateSafeID(kind, id string) error {
	if id == "" || strings.Contains(id, "..") || strings.ContainsRune(id, 0) || !safeIDPattern.MatchString(id) {
		return fmt.Errorf("invalid %s id: %w", kind, ErrInvalidInput)
	}
	return nil
}

// hashContent returns a short stable digest of an entry's content, used to NAME its
// per-id content cache file. That makes the file immutable + self-validating: changing
// the content yields a new filename, so recall can never read content that no longer
// matches the entry — a failed cache write simply leaves the new-hash file absent and
// recall falls back to the source. Per-id namespacing makes 64 bits ample.
func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// contentDigest is the full SHA-256 (hex) of an entry's content: the collision-resistant
// identity recall uses to bind returned text to the promoted metadata version.
// hashContent's 64-bit prefix only names cache files and is never a trust comparison.
func contentDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// isContentDigest reports whether d is a full SHA-256 hex digest.
func isContentDigest(d string) bool {
	if len(d) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(d)
	return err == nil
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

// contextEntryMeta mirrors ContextEntry WITHOUT the Content field, so loading the
// promoted history for ranking skips allocating every entry's content string — recall's
// decode/allocation stops scaling with content size (XM-PRO-010). Content is loaded
// only for the returned window. KEEP IN SYNC with ContextEntry (minus Content/Stale).
type contextEntryMeta struct {
	ID                    string                `json:"id"`
	WorkspaceID           string                `json:"workspace_id"`
	Title                 string                `json:"title"`
	Source                string                `json:"source"`
	Permission            string                `json:"permission"`
	Status                string                `json:"status"`
	Promoted              bool                  `json:"promoted"`
	Verifications         []ContextVerification `json:"verifications"`
	RequiredVerifications int                   `json:"required_verifications"`
	RequireVerification   bool                  `json:"require_verification,omitempty"`
	VerificationMode      string                `json:"verification_mode"`
	CreatedAt             string                `json:"created_at"`
	UpdatedAt             string                `json:"updated_at"`
	Paths                 []string              `json:"paths,omitempty"`
	PathHashes            map[string]string     `json:"path_hashes,omitempty"`
	SearchTokens          []string              `json:"search_tokens,omitempty"`
	// ContentHash is set ONLY in the recall meta cache (writeContextMetaCache); the
	// source's metas decode it as empty (the source has no such field). It names the
	// entry's content file so recall reads the file matching the current content.
	ContentHash string `json:"content_hash,omitempty"`
	// ContentDigest is the full SHA-256 content identity (see ContextEntry). A cache
	// written before it existed lacks it and is treated as unusable (fail closed).
	ContentDigest string `json:"content_digest,omitempty"`
}

func (m *contextEntryMeta) toEntry() ContextEntry {
	return ContextEntry{
		ID: m.ID, WorkspaceID: m.WorkspaceID, Title: m.Title, Source: m.Source,
		Permission: m.Permission, Status: m.Status, Promoted: m.Promoted,
		Verifications: m.Verifications, RequiredVerifications: m.RequiredVerifications,
		RequireVerification: m.RequireVerification, VerificationMode: m.VerificationMode,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, Paths: m.Paths,
		PathHashes: m.PathHashes, SearchTokens: m.SearchTokens, ContentHash: m.ContentHash,
		ContentDigest: m.ContentDigest,
		// Content is loaded for the returned window; Stale/StalePaths at read time.
	}
}

// loadPromotedMeta loads the promoted entries for the recall ranking pass when the
// meta cache is unusable. Entries are streamed one at a time; each content body is
// hashed into ContentHash (binding the ranked metadata to that exact revision) and
// then dropped, so only the returned window's content is ever retained.
func loadPromotedMeta(dataDir, workspaceID string) ([]ContextEntry, error) {
	f, err := os.Open(contextEntriesPath(dataDir, workspaceID))
	if err != nil {
		if os.IsNotExist(err) {
			return []ContextEntry{}, nil
		}
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	if _, err := dec.Token(); err != nil { // opening '['
		return nil, err
	}
	out := []ContextEntry{}
	for dec.More() {
		var m struct {
			contextEntryMeta
			Content string `json:"content"`
		}
		if err := dec.Decode(&m); err != nil {
			return nil, err
		}
		if m.Promoted {
			m.ContentHash = hashContent(m.Content)
			m.ContentDigest = contentDigest(m.Content)
			out = append(out, m.toEntry())
		}
	}
	return out, nil
}

// --- recall read caches (XM-PRO-010): content-free meta cache + per-id content files,
// DERIVED from the unchanged context_entries.json source of truth. Recall parses the
// small meta cache (not the source's content bytes) for ranking, and reads only the
// returned window's content from per-id files — so recall's parse TIME, not just its
// allocation, stops scaling with total content size. Both caches fall back to the
// source when missing/stale, so they can never corrupt or hide a memory.

func contextMetaCachePath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "context_meta.json")
}

func contextContentDir(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "context_content")
}

// content files live in a per-id subdirectory (context_content/<id>/<hash>.txt) so a
// single validated id segment owns its own namespace — pruning an id's stale-hash
// files can never touch another id's, even though ids may legally contain dots.
func contextContentEntryDir(dataDir, workspaceID, id string) string {
	return filepath.Join(contextContentDir(dataDir, workspaceID), id)
}

func contextContentFilePath(dataDir, workspaceID, id, hash string) string {
	return filepath.Join(contextContentEntryDir(dataDir, workspaceID, id), hash+".txt")
}

// writeContextMetaCache writes the content-free metadata projection used by recall's
// ranking pass, stamping each entry's ContentHash so recall can find its content file.
// Best-effort: a failure just means recall falls back to the source.
func writeContextMetaCache(dataDir, workspaceID string, entries []ContextEntry) {
	metas := make([]contextEntryMeta, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		metas = append(metas, contextEntryMeta{
			ID: e.ID, WorkspaceID: e.WorkspaceID, Title: e.Title, Source: e.Source,
			Permission: e.Permission, Status: e.Status, Promoted: e.Promoted,
			Verifications: e.Verifications, RequiredVerifications: e.RequiredVerifications,
			RequireVerification: e.RequireVerification, VerificationMode: e.VerificationMode,
			CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, Paths: e.Paths,
			PathHashes: e.PathHashes, SearchTokens: e.SearchTokens,
			ContentHash:   hashContent(e.Content),
			ContentDigest: contentDigest(e.Content),
		})
	}
	_ = writeJSON(contextMetaCachePath(dataDir, workspaceID), metas)
}

// writeContextContentFile persists one entry's content to a hash-named file (a recall
// read cache). Called only where content is set (propose/update), so verify/promote
// don't rewrite content files (no write amplification). The hash name makes the file
// immutable + self-validating; stale-hash files for this id are pruned. Best-effort.
func writeContextContentFile(dataDir, workspaceID, id, content string) {
	dir := contextContentEntryDir(dataDir, workspaceID, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	hash := hashContent(content)
	if err := writeJSON(contextContentFilePath(dataDir, workspaceID, id, hash), content); err != nil {
		return
	}
	// drop any prior-content file(s) for this id (content changed → old hash obsolete).
	if ents, err := os.ReadDir(dir); err == nil {
		keep := hash + ".txt"
		for _, de := range ents {
			if name := de.Name(); name != keep {
				_ = os.Remove(filepath.Join(dir, name))
			}
		}
	}
}

// loadPromotedMetaCached reads the content-free meta cache for ranking (fast parse,
// no content bytes). Returns ok=false when the cache is absent so the caller falls
// back to loadPromotedMeta over the source.
func loadPromotedMetaCached(dataDir, workspaceID string) ([]ContextEntry, bool) {
	cachePath := contextMetaCachePath(dataDir, workspaceID)
	// the cache write is best-effort; never trust a cache older than the source (a
	// failed cache write would otherwise serve stale promotion state). writeJSON is
	// atomic, so this mtime compare is exact — the cache is whole or absent, never torn.
	cacheInfo, cerr := os.Stat(cachePath)
	srcInfo, serr := os.Stat(contextEntriesPath(dataDir, workspaceID))
	if cerr != nil || serr != nil || cacheInfo.ModTime().Before(srcInfo.ModTime()) {
		return nil, false
	}
	var metas []contextEntryMeta
	if err := readJSON(cachePath, &metas); err != nil {
		return nil, false
	}
	out := make([]ContextEntry, 0, len(metas))
	for i := range metas {
		if !metas[i].Promoted {
			continue
		}
		if !isContentDigest(metas[i].ContentDigest) {
			// Legacy cache (64-bit filename hash only): it cannot bind content to the
			// approved version, so fall back to the source and rebuild the cache.
			migrateContextMetaCache(dataDir, workspaceID)
			return nil, false
		}
		out = append(out, metas[i].toEntry())
	}
	return out, true
}

// migrateContextMetaCache rewrites a legacy meta cache with full content digests. It
// runs under the store lock so it cannot overwrite a cache written by a newer save.
// Best-effort: on failure recall keeps using the source fallback.
func migrateContextMetaCache(dataDir, workspaceID string) {
	unlock := lockStore(contextEntriesPath(dataDir, workspaceID))
	defer unlock()
	entries, err := loadContextEntries(dataDir, workspaceID)
	if err != nil {
		return
	}
	writeContextMetaCache(dataDir, workspaceID, entries)
}

// loadWindowContent returns the content of the requested ids, reading each id's
// hash-named content file first (O(window) reads, no full-source parse) and falling
// back to a streaming source read for any id whose content file is missing — covering
// legacy/unmigrated entries and the rare case of a failed content-file write. The
// source may hold a newer revision than the ranked metadata, so callers must compare
// each returned body against its expected hash. idHashes maps each window id to the
// ContentHash recorded in the (source-fresh) meta cache.
func loadWindowContent(dataDir, workspaceID string, idHashes map[string]string) map[string]string {
	out := make(map[string]string, len(idHashes))
	missing := make(map[string]struct{})
	for id, hash := range idHashes {
		var content string
		if err := readJSON(contextContentFilePath(dataDir, workspaceID, id, hash), &content); err == nil {
			out[id] = content
		} else {
			missing[id] = struct{}{}
		}
	}
	if len(missing) > 0 {
		if fromSource, err := loadContentsForIDs(dataDir, workspaceID, missing); err == nil {
			for id, c := range fromSource {
				out[id] = c
			}
		}
	}
	return out
}

// loadContentsForIDs streams the entry store and returns the content of ONLY the
// requested ids, so recall fetches content for just the returned window — the rest of
// the (potentially huge) content is decoded transiently and discarded, never retained.
func loadContentsForIDs(dataDir, workspaceID string, ids map[string]struct{}) (map[string]string, error) {
	f, err := os.Open(contextEntriesPath(dataDir, workspaceID))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	if _, err := dec.Token(); err != nil { // opening '['
		return nil, err
	}
	out := make(map[string]string, len(ids))
	for dec.More() {
		var e struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		}
		if err := dec.Decode(&e); err != nil {
			return nil, err
		}
		if _, want := ids[e.ID]; want {
			out[e.ID] = e.Content
		}
	}
	return out, nil
}

func saveContextEntries(dataDir, workspaceID string, entries []ContextEntry) error {
	if err := writeJSON(contextEntriesPath(dataDir, workspaceID), entries); err != nil {
		return err
	}
	// refresh the content-free meta cache (recall's fast ranking parse). Derived +
	// best-effort: a stale/missing cache only makes recall fall back to the source.
	writeContextMetaCache(dataDir, workspaceID, entries)
	return nil
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
	case approvals >= entry.RequiredVerifications || openModeAssertionStands(entry):
		// A self-asserted open-mode entry stays promoted while authenticated peers
		// build a quorum, so approving it never hides it; any dissent removes that basis.
		entry.Status = "verified"
		entry.Promoted = true
	default:
		entry.Status = "pending"
		entry.Promoted = false
	}
}

// openModeAssertionStands reports whether an entry the open-mode identity wrote still
// rests on that identity's own word: it approved the current content (an edit clears
// every vote), nobody has dissented since, and the proposal did not ask for peer
// verification. Such an entry is promoted as self_asserted_open_mode until a full peer
// quorum upgrades it.
func openModeAssertionStands(e *ContextEntry) bool {
	if e.RequireVerification || !IsOpenModeIdentity(e.Source) {
		return false
	}
	asserted := false
	for agent, approve := range latestVerdicts(e.Verifications) {
		if !approve {
			return false
		}
		asserted = asserted || agent == OpenModeIdentity
	}
	return asserted
}

// regateOpenModeEntry sets the quorum of an entry the open-mode identity wrote for the
// write happening now, because open mode is a property of each write. An open-mode
// write gets the single-assertion gate ProposeContext gives an open-mode proposal
// (unless it asked for peer verification), so entries that older builds left pending
// can still be asserted. An authenticated write raises a single-assertion gate to the
// workspace quorum, so once tokens exist no single principal can reject, rewrite and
// re-promote open-mode memory alone. Entries proposed under authentication keep the
// gate they were proposed with.
func regateOpenModeEntry(e *ContextEntry, openModeWrite, requireMulti bool, threshold int) {
	if !IsOpenModeIdentity(e.Source) {
		return
	}
	switch {
	case openModeWrite:
		if !e.RequireVerification {
			e.RequiredVerifications = 1
		}
	case e.RequiredVerifications <= 1 && (requireMulti || e.RequireVerification):
		e.RequiredVerifications = max(threshold, 1)
	}
}

// latestVerdicts returns each distinct agent's latest verdict, keyed case-insensitively
// (matching VerifyContext's replace-by-EqualFold semantics).
func latestVerdicts(verifications []ContextVerification) map[string]bool {
	latest := map[string]bool{}
	for _, v := range verifications {
		if agent := strings.ToLower(strings.TrimSpace(v.Agent)); agent != "" {
			latest[agent] = v.Approve
		}
	}
	return latest
}

// verificationMode labels how a promoted entry's CURRENT content earned promotion, from
// the votes on it ("" when not promoted). It is peer_verified once enough distinct
// principals other than the author and OpenModeIdentity approve: the entry's own
// quorum, or the workspace threshold for an entry that one assertion promoted.
// Otherwise it is self_asserted_open_mode if the open-mode identity approved the
// current content, else single_agent (one authenticated principal's word). Authorship
// alone never makes it self-asserted: an edit clears every vote, so content an
// authenticated editor rewrote carries no open-mode vote.
func verificationMode(e *ContextEntry, threshold int) string {
	if !e.Promoted {
		return ""
	}
	need := e.RequiredVerifications
	if need <= 1 {
		need = max(threshold, 1)
	}
	author := strings.ToLower(strings.TrimSpace(e.Source))
	peers, openAsserted := 0, false
	for agent, approve := range latestVerdicts(e.Verifications) {
		switch {
		case !approve:
		case agent == OpenModeIdentity:
			openAsserted = true // the open-mode identity's word, never a peer's
		case agent == author:
			// the author's own word is not an independent approval
		default:
			peers++
		}
	}
	switch {
	case peers >= need:
		return VerificationPeer
	case openAsserted:
		return VerificationSelfAssertedOpen
	default:
		return VerificationSingleAgent
	}
}

// labelVerificationModes fills VerificationMode on entries written before it existed
// and counts the promoted entries per mode, so recall and ground can show how many
// facts are peer-verified versus self-asserted.
func labelVerificationModes(entries []ContextEntry, threshold int) map[string]int {
	counts := map[string]int{VerificationPeer: 0, VerificationSelfAssertedOpen: 0, VerificationSingleAgent: 0}
	for i := range entries {
		if entries[i].VerificationMode == "" {
			entries[i].VerificationMode = verificationMode(&entries[i], threshold)
		}
		if entries[i].Promoted && entries[i].VerificationMode != "" {
			counts[entries[i].VerificationMode]++
		}
	}
	return counts
}

// ProposeContext creates a pending context entry. It is promoted immediately, on the
// proposer's own assertion, in single-agent mode (the operator setting) and in open
// mode (req.OpenMode), unless the request explicitly asks for peer verification.
func ProposeContext(dataDir, workspaceID string, req ProposeContextRequest) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("content is required: %w", ErrInvalidInput)
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
	tighten := req.RequireVerification != nil && *req.RequireVerification
	if tighten {
		requireMulti = true
	}
	source := fallbackString(strings.TrimSpace(req.Source), "unknown")
	if req.OpenMode {
		source = OpenModeIdentity
	}
	// selfNote, when set, promotes the entry on the proposer's own assertion.
	selfNote := ""
	switch {
	case req.OpenMode && !tighten:
		// No credentials are configured, so every caller is OpenModeIdentity and a
		// peer quorum can never form. Promote the entry labelled as self-asserted
		// instead of leaving it pending forever.
		selfNote = "open mode: self-asserted (no authentication configured)"
	case !requireMulti:
		selfNote = "single-agent mode"
	}
	required := threshold
	if selfNote != "" {
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
		Source:                source,
		Permission:            permission,
		Verifications:         []ContextVerification{},
		RequiredVerifications: required,
		RequireVerification:   tighten,
		CreatedAt:             now,
		UpdatedAt:             now,
		Paths:                 cleanPaths(req.Paths),
	}
	entry.SearchTokens = memoryTokenList(entry.Title + " " + entry.Content)
	if selfNote != "" {
		// single-agent or open mode: the proposer's own assertion promotes it.
		entry.Verifications = append(entry.Verifications, ContextVerification{
			Agent: entry.Source, Approve: true, Note: selfNote, At: now,
		})
	}
	reconcileEntry(&entry)
	entry.VerificationMode = verificationMode(&entry, threshold)
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
	// persist the per-id content file (recall's window read cache) — only here and in
	// UpdateContextContent, the two sites that set content, so verify/promote never
	// rewrite content files.
	writeContextContentFile(dataDir, workspaceID, entry.ID, entry.Content)
	return &entry, nil
}

// VerifyContext records an authenticated agent's verdict. See VerifyContextAs.
func VerifyContext(dataDir, workspaceID, entryID, agent string, approve bool, note string) (*ContextEntry, error) {
	return VerifyContextAs(dataDir, workspaceID, entryID, ContextActor{ID: agent}, approve, note)
}

// VerifyContextAs records voter's verdict on an entry and re-promotes if the approval
// threshold is now met. Distinct agents only — a single agent cannot satisfy a
// multi-agent gate by voting twice. The vote re-gates an entry the open-mode identity
// wrote for the kind of write it is (regateOpenModeEntry).
func VerifyContextAs(dataDir, workspaceID, entryID string, voter ContextActor, approve bool, note string) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	agent := strings.TrimSpace(voter.ID)
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
	requireMulti, threshold := contextDefaults(dataDir)
	regateOpenModeEntry(entry, voter.OpenMode, requireMulti, threshold)
	reconcileEntry(entry)
	entry.VerificationMode = verificationMode(entry, threshold)
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

// UpdateContextContent amends an entry's content on behalf of editor, who must be its
// author or an admin (ErrNotEntryAuthor otherwise). Readonly entries that are already
// verified/promoted reject edits with a Conflict wrapping ErrReadonlyVerified — they can
// only be superseded by a new proposal (this is the "readonly" permission guarantee).
// The edit resets verification and re-gates an entry the open-mode identity wrote for
// the kind of write it is (regateOpenModeEntry).
func UpdateContextContent(dataDir, workspaceID, entryID, content string, editor ContextActor) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("content is required: %w", ErrInvalidInput)
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
	if !editor.Admin && (editor.ID == "" || editor.ID != entry.Source) {
		return nil, ErrNotEntryAuthor
	}
	if entry.Permission == "readonly" && (entry.Promoted || entry.Status == "verified") {
		return nil, Conflict(fmt.Sprintf("entry %s is readonly and verified; propose a new entry to supersede it", entryID)).WithCause(ErrReadonlyVerified)
	}
	entry.Content = content
	entry.SearchTokens = memoryTokenList(entry.Title + " " + entry.Content)
	entry.UpdatedAt = nowUTC()
	// a content change resets verification — re-approval is required — AND must
	// discard the old drift baseline, so re-promotion captures a fresh snapshot
	// against the current files instead of reusing the previous assertion's
	// evidence (XM-NEW-004).
	entry.Verifications = []ContextVerification{}
	entry.PathHashes = nil
	entry.Stale = false
	entry.StalePaths = nil
	requireMulti, threshold := contextDefaults(dataDir)
	regateOpenModeEntry(entry, editor.OpenMode, requireMulti, threshold)
	reconcileEntry(entry)
	entry.VerificationMode = verificationMode(entry, threshold)
	if err := saveContextEntries(dataDir, workspaceID, entries); err != nil {
		return nil, err
	}
	// content changed → refresh the per-id content file (recall's window read cache).
	writeContextContentFile(dataDir, workspaceID, entry.ID, entry.Content)
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
	_, threshold := contextDefaults(dataDir)
	labelVerificationModes(entries, threshold) // label entries written before the field existed
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
	modes := labelVerificationModes(promoted, threshold)
	return map[string]any{
		"workspace_id":           workspaceID,
		"require_multi_agent":    requireMulti,
		"verification_threshold": threshold,
		"active_count":           len(promoted),
		"verification_modes":     modes,
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
	// Content is bound to the exact promoted version whose metadata was ranked: a
	// concurrent update between the metadata pass and the content load yields a hash
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

// recallOnce is one ranked-recall pass. It reports how many returned entries had
// content that no longer matched their ranked metadata; when withhold is set those
// entries are removed from the result instead of being returned.
func recallOnce(ctx context.Context, dataDir, workspaceID, query string, paths []string, limit int, withhold bool) (map[string]any, int, error) {
	// metadata-first load: rank on content-less metadata (precomputed SearchTokens), so
	// recall's decode/allocation doesn't scale with content size; content is fetched
	// only for the returned window below (XM-PRO-010). The content-free meta CACHE bounds
	// the ranking PARSE time too — parsing it never touches the source's content bytes —
	// with a transparent fallback to the source when the cache is absent.
	promoted, ok := loadPromotedMetaCached(dataDir, workspaceID)
	if !ok {
		var err error
		if promoted, err = loadPromotedMeta(dataDir, workspaceID); err != nil {
			return nil, 0, err
		}
	}
	metaFast := true
	for i := range promoted {
		if len(promoted[i].SearchTokens) == 0 {
			// a legacy entry without precomputed tokens needs its content to rank
			// correctly — fall back to the full load (transitional; entries written
			// since SearchTokens landed always carry them).
			full, ferr := ListContextEntries(dataDir, workspaceID, "promoted")
			if ferr != nil {
				return nil, 0, ferr
			}
			promoted = full
			metaFast = false
			break
		}
	}
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	root := contextRoot(dataDir, workspaceID)
	// NOTE: drift-check is deferred to a bounded candidate window AFTER ranking (see
	// below). Re-hashing every promoted entry here was O(history) per recall — a
	// `recall limit=1` over thousands of memories did thousands of file reads
	// (XM-POST-011). Ranking uses only cheap metadata; only the entries that may be
	// returned are hashed.

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
	hasSignal := explicitSignal && (len(qtokens) > 0 || len(focusSet) > 0)
	for _, e := range promoted {
		// relevance = the task-match signal (lexical + path overlap). boost = trust
		// + recency, applied on top but never enough on its own to surface an
		// irrelevant memory when a query/paths signal is present.
		relevance := 0.0
		if len(qtokens) > 0 {
			etoks := entrySearchTokenSet(&e) // precomputed; no content scan per recall
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
		// NB: no stale penalty here — staleness is unknown until the bounded drift
		// check below; the penalty is applied to the candidate window only.
		score := relevance + boost
		if hasSignal && relevance <= 0 {
			// explicit query/paths gate relevance: irrelevant memory is dropped below.
			// Implicit working-change focus only adds to the score.
			score = -1 // sentinel: filtered out
		}
		ranked = append(ranked, scored{e, score})
	}
	byScore := func(s []scored) {
		sort.SliceStable(s, func(a, b int) bool {
			if s[a].score != s[b].score {
				return s[a].score > s[b].score
			}
			return s[a].entry.UpdatedAt > s[b].entry.UpdatedAt
		})
	}
	byScore(ranked)

	// Drop relevance-gated entries, then take a BOUNDED candidate window — only these
	// are drift-checked, so recall I/O is O(window), not O(history). The window is a
	// few × limit so the stale penalty can still re-order without missing a result.
	candidates := make([]scored, 0, len(ranked))
	for _, s := range ranked {
		if hasSignal && s.score < 0 {
			continue
		}
		candidates = append(candidates, s)
		if len(candidates) >= recallCandidateWindow(limit) {
			break
		}
	}
	// drift-check ONLY the candidate window; apply the stale penalty, then re-rank.
	for i := range candidates {
		computeStaleness(root, &candidates[i].entry)
		if candidates[i].entry.Stale {
			candidates[i].score -= 1.0
		}
	}
	byScore(candidates)

	out := make([]ContextEntry, 0, limit)
	mismatched := 0
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
	// In the metadata-fast path the ranked entries carry no content — load it for ONLY
	// the returned window (the rest of the store's content is never allocated).
	if metaFast && len(out) > 0 {
		// The filename hash only locates a candidate body; acceptance below requires
		// the full digest recorded with the ranked metadata.
		idHashes := make(map[string]string, len(out))
		for i := range out {
			idHashes[out[i].ID] = out[i].ContentHash
		}
		if recallBeforeContentLoad != nil {
			recallBeforeContentLoad()
		}
		// per-id hash-named content files first (O(window) reads, no full-source parse);
		// any missing file falls back to a streaming source read inside loadWindowContent.
		contents := loadWindowContent(dataDir, workspaceID, idHashes)
		kept := out[:0]
		for i := range out {
			content, ok := contents[out[i].ID]
			if !ok || contentDigest(content) != out[i].ContentDigest {
				mismatched++
				if withhold {
					continue
				}
			}
			out[i].Content = content
			out[i].ContentHash = "" // derived transport fields — don't leak them to callers
			out[i].ContentDigest = ""
			kept = append(kept, out[i])
		}
		out = kept
	}
	requireMulti, threshold := contextDefaults(dataDir)
	modes := labelVerificationModes(out, threshold)
	return map[string]any{
		"workspace_id":           workspaceID,
		"query":                  query,
		"ranked":                 hasSignal,
		"bounded":                true,
		"limit":                  limit,
		"require_multi_agent":    requireMulti,
		"verification_threshold": threshold,
		"total_active":           len(promoted),
		"active_count":           len(promoted),
		"returned":               len(out),
		"verification_modes":     modes, // per-mode counts of the returned entries
		"stale_count":            staleCount,
		"drift_checked":          len(candidates), // every returned entry is in this set (checked)
		"conflicts":              overlappingMemory(out),
		"entries":                out,
		"generated_at":           nowUTC(),
	}, mismatched, nil
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
	w := limit * 4
	if w < recallCandidateFloor {
		w = recallCandidateFloor
	}
	return w
}

// memoryTokens lowercases and splits text into a set of word tokens for matching.
// memoryTokenizeCalls counts full-text tokenizations of memory content — a test
// hook to assert recall is metadata-first (it should not grow with promoted-history
// size once entries carry precomputed SearchTokens). Not used in production logic.
var memoryTokenizeCalls atomic.Int64

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
	return out
}

// entrySearchTokenSet returns the entry's relevance token set, using the persisted
// SearchTokens when present (no content scan) and falling back to tokenizing
// title+content only for legacy entries written before the field existed.
func entrySearchTokenSet(e *ContextEntry) map[string]struct{} {
	if len(e.SearchTokens) > 0 {
		set := make(map[string]struct{}, len(e.SearchTokens))
		for _, t := range e.SearchTokens {
			set[t] = struct{}{}
		}
		return set
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

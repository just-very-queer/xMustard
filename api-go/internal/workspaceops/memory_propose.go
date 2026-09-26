package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"xmustard/api-go/internal/govstore"
)

// Context governance: the trust layer for the MCP context engine. Agents don't
// write straight into shared context — they PROPOSE an entry, which other agents
// VERIFY. An entry is promoted into the active (shared) context only once enough
// distinct agents have approved it. Entries carry a permission: "readonly" entries
// can never be mutated after promotion (only superseded by a new proposal). A
// per-workspace/global toggle decides whether multi-agent verification is required
// at all — "run it through multiple agents, or not". The state lives in the govstore
// database (memory_store.go); the files memory_propose, memory_verify, memory_edit and
// memory_recall hold the kernel's operations.

// Verification modes record HOW a promoted memory earned its place in shared
// context, so an agent can weigh a peer-verified fact above a self-asserted one. The
// rule that assigns them is govstore.VerificationMode.
const (
	// VerificationPeer: a quorum of distinct authenticated principals other than the
	// author approved it.
	VerificationPeer = govstore.ModePeerVerified
	// VerificationSelfAssertedOpen: promoted on its author's own word because the API
	// runs without credentials (open mode). Every caller is then OpenModeIdentity, so
	// no peer quorum can form.
	VerificationSelfAssertedOpen = govstore.ModeSelfAssertedOpenMode
	// VerificationSingleAgent: promoted on the author's own authenticated word because
	// the operator opted out of multi-agent verification.
	VerificationSingleAgent = govstore.ModeSingleAgent
)

// OpenModeIdentity is the one identity every unauthenticated caller collapses to when
// no credentials are configured, so fabricated names cannot pose as distinct verifiers.
const OpenModeIdentity = govstore.OpenModeIdentity

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
	// self_asserted_open_mode or single_agent. Empty while pending or rejected.
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
	// re-tokenizing every entry's full content on every call (XM-PRO-010). Empty on
	// legacy entries → computed lazily.
	SearchTokens []string `json:"search_tokens,omitempty"`
	// ContentHash is a legacy transport field, never set.
	ContentHash string `json:"content_hash,omitempty"`
	// ContentDigest is the full SHA-256 of the served revision the metadata describes.
	// It is DERIVED: recall's trust binding compares loaded content against it, and it
	// is cleared before entries are returned.
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

// cleanPaths trims, drops empties, and de-duplicates referenced paths.
func cleanPaths(paths []string) []string {
	if out := govstore.CleanPaths(paths); len(out) > 0 {
		return out
	}
	return nil
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
	anchors, err := confineAnchorPaths(dataDir, workspaceID, cleanPaths(req.Paths))
	if err != nil {
		return nil, err
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

	ctx := context.Background()
	root := contextRoot(dataDir, workspaceID)
	actor := memoryActor(source, root)
	title := strings.TrimSpace(req.Title)
	var out ContextEntry
	var promoted bool
	err = memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		e, err := tx.InsertEntry(ctx, govstore.NewEntry{
			ID:          "ctx_" + hashID(workspaceID, req.Title, req.Content+nowUTC())[:12],
			WorkspaceID: workspaceID, Title: title, Content: req.Content, Permission: permission,
			RequiredVerifications: required, RequireVerification: tighten,
			Paths: cleanPaths(anchors), SearchTokens: memoryTokenList(title + " " + req.Content),
		}, actor)
		if err != nil {
			return err
		}
		if selfNote != "" {
			// single-agent or open mode: the proposer's own assertion promotes it.
			if _, err := tx.RecordVote(ctx, govstore.VoteInput{EntryID: e.ID, Verdict: govstore.VerdictApprove, Note: selfNote}, actor); err != nil {
				return err
			}
		}
		out, promoted, err = settleEntry(ctx, tx, workspaceID, e.ID, threshold, root, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	out.Content = req.Content
	if promoted {
		recordVerifyFeedback(dataDir, workspaceID, out.ID, out.Paths)
	}
	return &out, nil
}

package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/redact"
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

// ErrVerifierRequired: casting a verdict (a retire of promoted memory is a retract
// verdict) or reading unverified memory history needs the verifier role.
var ErrVerifierRequired = errors.New("only a principal with the verifier role may do this")

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
	// Lifecycle state (WS-19A). Revision is the served revision, the base_revision an
	// edit compares against; PendingRevision is a proposed edit awaiting verification.
	// Lifecycle is omitted while active. Supersedes lists the entries this one replaces
	// once it is promoted; SupersededBy names the entry that replaced this one.
	Revision        int64    `json:"revision,omitempty"`
	PendingRevision int64    `json:"pending_revision,omitempty"`
	Lifecycle       string   `json:"lifecycle,omitempty"`
	Supersedes      []string `json:"supersedes,omitempty"`
	SupersededBy    string   `json:"superseded_by,omitempty"`
	InvalidatedAt   string   `json:"invalidated_at,omitempty"`
	ExpiresAt       string   `json:"expires_at,omitempty"`
	// Diff is the line diff from the served revision to the pending revision a verify
	// voted on (PAR-GOV-05).
	Diff string `json:"diff,omitempty"`
	// Warnings reports input that was accepted but ignored, e.g. a malformed expiry.
	Warnings []string `json:"warnings,omitempty"`
	// Redactions reports the secrets removed from the write before it was stored
	// (PAR-SEC-04); nothing of a secret value is kept.
	Redactions *redact.Report `json:"redactions,omitempty"`
	// Kind, Topic and Tags classify the entry (PAR-GOV-09); recall filters on them.
	Kind  string   `json:"kind,omitempty"`
	Topic string   `json:"topic,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	// Recall labels (WS-20), set only on recall results: the rank state (served,
	// pending, superseded, expired), the trust (a verification mode, or unverified),
	// the peer approvals a pending entry still needs, the ranking signals when
	// explain=true, and whether the output budget cut the content.
	State            string        `json:"state,omitempty"`
	Trust            string        `json:"trust,omitempty"`
	VotesNeeded      *int          `json:"votes_needed,omitempty"`
	ScoreDetails     *ScoreDetails `json:"score_details,omitempty"`
	ContentTruncated bool          `json:"content_truncated,omitempty"`
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
	// Supersedes lists active entries this proposal replaces (PAR-GOV-04). Nothing
	// changes until the proposal is promoted; then every listed entry that is still
	// active becomes superseded in the same transaction, and is never deleted.
	Supersedes []string `json:"supersedes,omitempty"`
	// Expires hides the entry after a UTC date (inclusive, YYYY-MM-DD) or an RFC 3339
	// time (PAR-GOV-12). A malformed value fails open: no expiry, and a warning.
	Expires string `json:"expires,omitempty"`
	// Kind (one of MemoryKinds), Topic ("/"-separated) and Tags classify the entry.
	Kind  string   `json:"kind,omitempty"`
	Topic string   `json:"topic,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	// OpenMode is set by the HTTP layer, never decoded from a client, when the caller
	// is unauthenticated because no credentials are configured. The proposal is then
	// promoted at once as self_asserted_open_mode instead of waiting for a quorum that
	// a single identity can never form.
	OpenMode bool `json:"-"`
	// Caller carries the writer's owner, kind and provenance (Remember sets it); Source
	// and OpenMode still decide the author.
	Caller ContextActor `json:"-"`
}

// ContextActor is who writes to an entry. ID is recorded as its author or verifier.
// Admin may edit any entry's content; anyone else only an entry whose Source is exactly
// their ID. OpenMode marks a write made with no credentials configured (ID is then
// OpenModeIdentity): open mode is a property of each write, not of the entry.
type ContextActor struct {
	ID    string
	Admin bool
	// Approver holds the human-approver role: it may retract, restore and purge
	// directly, like an admin (PAR-GOV-06).
	Approver bool
	// Verifier holds the verifier role: it may cast verdicts, including the retract
	// verdict remember(op=retire) casts on promoted memory.
	Verifier bool
	OpenMode bool
	// Owner and Kind come from the principal's token (PAR-PROV-05); SessionID and CallID
	// from the transport. RunID and Evidence are the run and evidence handles the write
	// cites, set by bindProvenance once they are checked.
	Owner     string
	Kind      string
	SessionID string
	CallID    string
	RunID     string
	Evidence  []string
	// Approval labels a human approver's write with where it was made and whether an
	// agent process could have read the token (HumanApprovalLabel); "" otherwise.
	Approval string
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
	supersedes, err := cleanSupersedes(req.Supersedes)
	if err != nil {
		return nil, err
	}
	if req.Kind != "" && !slices.Contains(MemoryKinds, req.Kind) {
		return nil, fmt.Errorf("kind %q is not one of %s: %w", req.Kind, strings.Join(MemoryKinds, ", "), ErrInvalidInput)
	}
	expiresAt, expiresOK := parseExpiry(req.Expires)
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

	ctx := context.Background()
	root := contextRoot(dataDir, workspaceID)
	caller := req.Caller
	caller.ID, caller.OpenMode = source, req.OpenMode
	actor := caller.storeActor(root)
	title := strings.TrimSpace(req.Title)
	var out ContextEntry
	var promoted bool
	err = memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		// A replacement removes what it supersedes, so it passes the strictest gate
		// among them: an entry that needs peers is never superseded on one word.
		supersededGate := 0
		for _, id := range supersedes {
			_, old, err := loadEntryTx(ctx, tx, workspaceID, id)
			if err != nil {
				return err
			}
			supersededGate = max(supersededGate, old.RequiredVerifications)
		}
		peersOnly := tighten || supersededGate > 1
		// selfNote, when set, promotes the entry on the proposer's own assertion.
		selfNote := selfAssertion(req.OpenMode, peersOnly, requireMulti)
		required := max(threshold, supersededGate)
		if selfNote != "" {
			required = 1
		}
		e, err := tx.InsertEntry(ctx, govstore.NewEntry{
			ID:          "ctx_" + hashID(workspaceID, req.Title, req.Content+nowUTC())[:12],
			WorkspaceID: workspaceID, Title: title, Content: req.Content, Permission: permission,
			RequiredVerifications: required, RequireVerification: peersOnly,
			Paths: cleanPaths(anchors), SearchTokens: memoryTokenList(title + " " + req.Content),
			ExpiresAt: expiresAt, Metadata: supersedesMetadata(supersedes),
			Kind: req.Kind, Topic: normalizeTopic(req.Topic), Tags: cleanPaths(req.Tags),
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
	if !expiresOK {
		out.Warnings = append(out.Warnings, fmt.Sprintf("expires %q is not a date or RFC 3339 time; no expiry set", req.Expires))
	}
	if promoted {
		recordVerifyFeedback(dataDir, workspaceID, out.ID, out.Paths)
	}
	return &out, nil
}

// MemoryKinds are the kinds a memory may declare (PAR-GOV-09).
var MemoryKinds = []string{"project_knowledge", "decision", "constraint", "workflow", "procedure", "gotcha",
	"measurement", "convention", "handoff", "candidate", "maintenance"}

// selfAssertion is the note of the author's own approval that promotes a write at once,
// or "" when the write waits for peers. Open mode (no credentials, so a peer quorum can
// never form) and single-agent mode (the operator's setting) self-assert unless the
// entry asked for peer verification.
func selfAssertion(openMode, requirePeers, requireMulti bool) string {
	switch {
	case requirePeers:
		return ""
	case openMode:
		return "open mode: self-asserted (no authentication configured)"
	case !requireMulti:
		return "single-agent mode"
	}
	return ""
}

// maxSupersedes bounds how many entries one proposal may replace.
const maxSupersedes = 16

// supersedesKey is the entry metadata key that holds a proposal's pending supersession
// until promotion applies it.
const supersedesKey = "supersedes"

func cleanSupersedes(ids []string) ([]string, error) {
	out := cleanPaths(ids) // trimmed, non-empty, de-duplicated, order kept
	if len(out) > maxSupersedes {
		return nil, fmt.Errorf("supersedes lists more than %d entries: %w", maxSupersedes, ErrInvalidInput)
	}
	for _, id := range out {
		if err := validateSafeID("entry", id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func supersedesMetadata(ids []string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	return map[string]string{supersedesKey: strings.Join(ids, ",")}
}

// pendingSupersedes is the supersession a proposal still waits to apply.
func pendingSupersedes(e govstore.Entry) []string {
	if v := e.Metadata[supersedesKey]; v != "" {
		return strings.Split(v, ",")
	}
	return nil
}

// applySupersession runs when e is promoted: every entry it names that is still active
// becomes superseded by it, atomically with the promotion, and the pending intent is
// cleared so a later restore of an old entry is not undone by the next vote.
func applySupersession(ctx context.Context, tx govstore.Tx, e govstore.Entry, actor govstore.Actor) error {
	olds := pendingSupersedes(e)
	if len(olds) == 0 {
		return nil
	}
	var live []string
	for _, id := range olds {
		old, err := tx.GetEntry(ctx, id)
		if err != nil {
			return err
		}
		if old.WorkspaceID == e.WorkspaceID && old.Lifecycle == govstore.LifecycleActive {
			live = append(live, id)
		}
	}
	if len(live) > 0 {
		if err := tx.Supersede(ctx, govstore.SupersedeInput{NewID: e.ID, OldIDs: live,
			Reason: "superseded by promoted entry " + e.ID}, actor); err != nil {
			return err
		}
	}
	meta := maps.Clone(e.Metadata)
	delete(meta, supersedesKey)
	_, err := tx.SetClassification(ctx, e.ID, govstore.Classification{Kind: e.Kind, Topic: e.Topic, Tags: e.Tags, Metadata: meta}, actor)
	return err
}

// parseExpiry reads an expiry: a UTC date is inclusive (hidden from the next midnight
// UTC), an RFC 3339 time is exact. ok is false for a malformed value, which fails open
// to no expiry (PAR-GOV-12).
func parseExpiry(s string) (expiresAt string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	if d, err := time.Parse(time.DateOnly, s); err == nil {
		return d.AddDate(0, 0, 1).UTC().Format(time.RFC3339), true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(time.RFC3339Nano), true
	}
	return "", false
}

// RememberRequest is one remember write. Op selects it: propose (the default), supersede
// (a proposal that must name Supersedes), edit (a focused edit of EntryID, see
// EditRequest), retire (see RetireContext) or restore (see RestoreContext). Reason is
// required by all but propose and supersede.
type RememberRequest struct {
	ProposeContextRequest
	Op           string  `json:"op,omitempty"`
	EntryID      string  `json:"entry_id,omitempty"`
	BaseRevision int64   `json:"base_revision,omitempty"`
	Reason       string  `json:"reason,omitempty"`
	OldString    string  `json:"old_string,omitempty"`
	NewString    string  `json:"new_string,omitempty"`
	Description  *string `json:"description,omitempty"`
	// Evidence and RunID bind the write to the evidence handles and the run it was
	// derived from (PAR-PROV-04); both are checked before anything is written.
	Evidence []string `json:"evidence,omitempty"`
	RunID    string   `json:"run_id,omitempty"`
}

var rememberOps = map[string]func(dataDir, workspaceID string, req RememberRequest, actor ContextActor) (*ContextEntry, error){
	"propose": func(dataDir, workspaceID string, req RememberRequest, _ ContextActor) (*ContextEntry, error) {
		return ProposeContext(dataDir, workspaceID, req.ProposeContextRequest)
	},
	"supersede": func(dataDir, workspaceID string, req RememberRequest, _ ContextActor) (*ContextEntry, error) {
		if len(req.Supersedes) == 0 {
			return nil, fmt.Errorf("op supersede needs supersedes: %w", ErrInvalidInput)
		}
		return ProposeContext(dataDir, workspaceID, req.ProposeContextRequest)
	},
	"edit": func(dataDir, workspaceID string, req RememberRequest, actor ContextActor) (*ContextEntry, error) {
		return EditContext(dataDir, workspaceID, req.EntryID, EditRequest{
			BaseRevision: req.BaseRevision, Reason: req.Reason, OldString: req.OldString, NewString: req.NewString,
			Content: req.Content, Description: req.Description, Expires: req.Expires,
		}, actor)
	},
	"retire": func(dataDir, workspaceID string, req RememberRequest, actor ContextActor) (*ContextEntry, error) {
		return RetireContext(dataDir, workspaceID, req.EntryID, req.Reason, actor)
	},
	"restore": func(dataDir, workspaceID string, req RememberRequest, actor ContextActor) (*ContextEntry, error) {
		return RestoreContext(dataDir, workspaceID, req.EntryID, req.Reason, actor)
	},
}

// NormalizeRememberOp is the op Remember runs for raw: trimmed, lower case, propose
// when blank. An unknown op is invalid input.
func NormalizeRememberOp(raw string) (string, error) {
	op := fallbackString(strings.ToLower(strings.TrimSpace(raw)), "propose")
	if _, ok := rememberOps[op]; !ok {
		return "", fmt.Errorf("op %q is not propose, supersede, edit, retire or restore: %w", raw, ErrInvalidInput)
	}
	return op, nil
}

// Remember runs one remember write as actor, who is always the author: a proposal is
// attributed to actor, never to a Source in the request. The evidence and run the write
// cites are checked and bound to it first, and secrets are redacted from every text
// field that is stored (old_string only locates stored text and is never stored).
func Remember(dataDir, workspaceID string, req RememberRequest, actor ContextActor) (*ContextEntry, error) {
	op, err := NormalizeRememberOp(req.Op)
	if err != nil {
		return nil, err
	}
	run := rememberOps[op]
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	actor, err = bindProvenance(dataDir, workspaceID, actor, req.Evidence, req.RunID)
	if err != nil {
		return nil, err
	}
	var red ingestRedaction
	red.scrub(&req.Title, &req.Content, &req.NewString, &req.Reason)
	req.Description = red.scrubOptional(req.Description)
	req.Source, req.OpenMode, req.Caller = actor.ID, actor.OpenMode, actor
	out, err := run(dataDir, workspaceID, req, actor)
	if err != nil {
		return nil, err
	}
	red.annotate(out)
	return out, nil
}

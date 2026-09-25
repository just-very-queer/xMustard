package govstore

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Lifecycle states. Only an active entry is served; the others stay retrievable by id.
const (
	LifecycleActive     = "active"
	LifecycleSuperseded = "superseded"
	LifecycleRetracted  = "retracted"
	LifecycleMerged     = "merged"
	LifecycleArchived   = "archived"
	LifecyclePurged     = "purged"
)

// Vote-reconciliation statuses, as in the legacy JSON store.
const (
	StatusPending  = "pending"
	StatusVerified = "verified"
	StatusRejected = "rejected"
)

// Verification modes: how a promoted entry earned its place (w0-kernel labels).
const (
	ModePeerVerified         = "peer_verified"
	ModeSelfAssertedOpenMode = "self_asserted_open_mode"
	ModeSingleAgent          = "single_agent"
)

var (
	validScopes      = set("workspace", "dir", "private", "session", "run", "global", "shared")
	validTiers       = set("", "core", "deferred")
	validPermissions = set("readonly", "readwrite")
	validStatuses    = set(StatusPending, StatusVerified, StatusRejected)
	validModes       = set(ModePeerVerified, ModeSelfAssertedOpenMode, ModeSingleAgent)
	tagPattern       = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

func set(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// Entry is one memory's head state. It carries no content: content lives in
// revisions and is read through EntryContents or GetRevision.
type Entry struct {
	ID                    string            `json:"id"`
	WorkspaceID           string            `json:"workspace_id"`
	Scope                 string            `json:"scope"`
	ScopeKey              string            `json:"scope_key,omitempty"`
	Kind                  string            `json:"kind,omitempty"`
	Tier                  string            `json:"tier,omitempty"`
	Topic                 string            `json:"topic,omitempty"`
	Title                 string            `json:"title"`
	Description           string            `json:"description,omitempty"`
	Tags                  []string          `json:"tags,omitempty"`
	Metadata              map[string]string `json:"metadata,omitempty"`
	Permission            string            `json:"permission"`
	Status                string            `json:"status"`
	Promoted              bool              `json:"promoted"`
	VerificationMode      string            `json:"verification_mode"`
	Lifecycle             string            `json:"lifecycle"`
	RequiredVerifications int               `json:"required_verifications"`
	RequireVerification   bool              `json:"require_verification,omitempty"`
	// VoteEpoch counts restores from retracted, superseded or merged. Only verdicts cast
	// in the current epoch count toward promotion.
	VoteEpoch         int64    `json:"vote_epoch,omitempty"`
	Source            string   `json:"source"`
	SourceOwner       string   `json:"source_owner,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
	AgentID           string   `json:"agent_id,omitempty"`
	Revision          int64    `json:"revision"`
	HeadRevision      int64    `json:"head_revision"`
	ContentDigest     string   `json:"content_digest"`
	SearchTokens      []string `json:"search_tokens,omitempty"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
	PromotedAt        string   `json:"promoted_at,omitempty"`
	ValidFrom         string   `json:"valid_from,omitempty"`
	ValidFromCommit   string   `json:"valid_from_commit,omitempty"`
	InvalidatedAt     string   `json:"invalidated_at,omitempty"`
	InvalidatedCommit string   `json:"invalidated_commit,omitempty"`
	ExpiredAt         string   `json:"expired_at,omitempty"`
	ExpiresAt         string   `json:"expires_at,omitempty"`
	SupersededBy      string   `json:"superseded_by,omitempty"`
	MergedInto        string   `json:"merged_into,omitempty"`
	NeedsReverify     bool     `json:"needs_reverify,omitempty"`
	StaleSince        string   `json:"stale_since,omitempty"`
	ProposeHead       string   `json:"propose_head,omitempty"`
	ProposeBranch     string   `json:"propose_branch,omitempty"`
	ProposeDirty      *bool    `json:"propose_dirty,omitempty"`
	PromoteHead       string   `json:"promote_head,omitempty"`
	PromoteBranch     string   `json:"promote_branch,omitempty"`
	PromoteDirty      *bool    `json:"promote_dirty,omitempty"`
	Worktree          string   `json:"worktree,omitempty"`
	RetentionClass    string   `json:"retention_class"`
	// Cursor orders entries by insertion; pass it as EntryFilter.AfterCursor.
	Cursor int64 `json:"cursor"`
}

// Served reports whether the entry belongs in shared context at time now: promoted,
// active and not past its expiry.
func (e Entry) Served(now time.Time) bool {
	if !e.Promoted || e.Lifecycle != LifecycleActive {
		return false
	}
	return e.ExpiresAt == "" || e.ExpiresAt > canonTime(now)
}

// NewEntry is a proposal. The author is always the writing Actor's principal: scope
// and author are derived server-side, never taken from the payload (SEC-05).
type NewEntry struct {
	ID                    string
	WorkspaceID           string
	Scope                 string // default "workspace"
	ScopeKey              string
	Kind                  string
	Tier                  string
	Topic                 string
	Title                 string
	Description           string
	Content               string
	Tags                  []string
	Metadata              map[string]string
	Permission            string // readonly (default) | readwrite
	RequiredVerifications int    // default 2
	RequireVerification   bool
	Paths                 []string      // path anchors, order preserved
	Anchors               []AnchorInput // non-path anchors
	SearchTokens          []string
	ExpiresAt             string
	Worktree              string
	RetentionClass        string
}

// EntryFilter selects entries. WorkspaceID is required.
type EntryFilter struct {
	WorkspaceID   string
	Status        string
	Promoted      *bool
	Lifecycles    []string // default: active only; {"*"} selects every state
	Kind          string
	Tier          string
	Scope         string
	ScopeKey      string
	ServedOnly    bool // promoted, active and unexpired now
	NeedsReverify bool
	AfterCursor   int64
	Limit         int
}

// Promotion is the result of the caller's vote reconciliation.
type Promotion struct {
	Status           string // pending | verified | rejected
	Promoted         bool
	VerificationMode string // required when Promoted, empty otherwise
}

// SupersedeInput retires OldIDs in favour of the promoted NewID (PAR-GOV-04).
type SupersedeInput struct {
	NewID  string
	OldIDs []string
	Reason string
}

// TransitionInput moves an entry between lifecycle states. Supersession and purge
// have their own methods.
//
// Restoring (To "active") from retracted, superseded or merged clears the entry's
// promotion, verification mode and drift baselines, appends a demote event and starts
// a new vote epoch: the entry was withdrawn as wrong or replaced, so it must be verified
// again before it is served. Verdicts cast before the restore stay in the event history
// but no longer count, so peer_verified needs fresh approvals. Restoring from archived
// keeps the promotion and the votes, because archiving only puts a still-valid memory
// away.
type TransitionInput struct {
	To     string // retracted | archived | merged | active (restore)
	Target string // merge target
	Reason string
}

// Classification replaces an entry's kind, topic, tags and metadata.
type Classification struct {
	Kind     string
	Topic    string
	Tags     []string
	Metadata map[string]string
}

// EntryReader reads entries.
type EntryReader interface {
	GetEntry(ctx context.Context, id string) (Entry, error)
	ListEntries(ctx context.Context, f EntryFilter) ([]Entry, error)
}

// EntryWriter writes entries. Every method appends its event to the history.
type EntryWriter interface {
	// InsertEntry creates a pending entry with revision 1 authored by actor.
	InsertEntry(ctx context.Context, in NewEntry, actor Actor) (Entry, error)
	// SetPromotion records the caller's reconciliation. peer_verified is refused unless
	// enough distinct peers approved the served revision.
	SetPromotion(ctx context.Context, id string, p Promotion, actor Actor) (Entry, error)
	Supersede(ctx context.Context, in SupersedeInput, actor Actor) error
	Transition(ctx context.Context, id string, in TransitionInput, actor Actor) (Entry, error)
	SetExpiry(ctx context.Context, id, expiresAt string, actor Actor) (Entry, error)
	SetTier(ctx context.Context, id, tier string, actor Actor) (Entry, error)
	SetClassification(ctx context.Context, id string, c Classification, actor Actor) (Entry, error)
	// Purge hard-deletes an entry's text (secrets, PII) and keeps a digest tombstone.
	Purge(ctx context.Context, id, reason string, actor Actor) error
}

const entryCols = `e.id, e.workspace_id, e.scope, e.scope_key, e.kind, e.tier, e.topic, e.title, e.description,
	e.tags, e.metadata, e.permission, e.status, e.promoted, e.verification_mode, e.lifecycle,
	e.required_verifications, e.require_verification, e.source, e.source_owner, e.session_id, e.agent_id,
	e.revision, e.head_revision, e.content_digest, e.search_tokens, e.created_at, e.updated_at,
	coalesce(e.promoted_at, ''), coalesce(e.valid_from, ''), e.valid_from_commit, coalesce(e.invalidated_at, ''),
	e.invalidated_commit, coalesce(e.expired_at, ''), coalesce(e.expires_at, ''), e.superseded_by, e.merged_into,
	e.needs_reverify, coalesce(e.stale_since, ''), e.propose_head, e.propose_branch, e.propose_dirty,
	e.promote_head, e.promote_branch, e.promote_dirty, e.worktree, e.retention_class, e.vote_epoch, e.pk`

type scanner interface{ Scan(dest ...any) error }

func scanEntry(s scanner) (Entry, error) {
	var e Entry
	var tags, meta, tokens string
	var promoted, requireVerification, needsReverify int
	var proposeDirty, promoteDirty *int64
	err := s.Scan(&e.ID, &e.WorkspaceID, &e.Scope, &e.ScopeKey, &e.Kind, &e.Tier, &e.Topic, &e.Title, &e.Description,
		&tags, &meta, &e.Permission, &e.Status, &promoted, &e.VerificationMode, &e.Lifecycle,
		&e.RequiredVerifications, &requireVerification, &e.Source, &e.SourceOwner, &e.SessionID, &e.AgentID,
		&e.Revision, &e.HeadRevision, &e.ContentDigest, &tokens, &e.CreatedAt, &e.UpdatedAt,
		&e.PromotedAt, &e.ValidFrom, &e.ValidFromCommit, &e.InvalidatedAt,
		&e.InvalidatedCommit, &e.ExpiredAt, &e.ExpiresAt, &e.SupersededBy, &e.MergedInto,
		&needsReverify, &e.StaleSince, &e.ProposeHead, &e.ProposeBranch, &proposeDirty,
		&e.PromoteHead, &e.PromoteBranch, &promoteDirty, &e.Worktree, &e.RetentionClass, &e.VoteEpoch, &e.Cursor)
	if err != nil {
		return Entry{}, err
	}
	e.Promoted, e.RequireVerification, e.NeedsReverify = promoted == 1, requireVerification == 1, needsReverify == 1
	e.ProposeDirty, e.PromoteDirty = intPtrBool(proposeDirty), intPtrBool(promoteDirty)
	if err := decodeJSONColumn(tags, &e.Tags); err != nil {
		return Entry{}, err
	}
	if err := decodeJSONColumn(meta, &e.Metadata); err != nil {
		return Entry{}, err
	}
	if err := decodeJSONColumn(tokens, &e.SearchTokens); err != nil {
		return Entry{}, err
	}
	if len(e.Tags) == 0 {
		e.Tags = nil
	}
	if len(e.Metadata) == 0 {
		e.Metadata = nil
	}
	if len(e.SearchTokens) == 0 {
		e.SearchTokens = nil
	}
	return e, nil
}

func intPtrBool(v *int64) *bool {
	if v == nil {
		return nil
	}
	b := *v == 1
	return &b
}

func decodeJSONColumn(s string, dst any) error {
	if s == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), dst); err != nil {
		return fmt.Errorf("govstore: decode column: %w", err)
	}
	return nil
}

func encodeJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return string(b), nil
}

func encodeStrings(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	return encodeJSON(v)
}

func encodeMap(v map[string]string) (string, error) {
	if v == nil {
		v = map[string]string{}
	}
	return encodeJSON(v)
}

// GetEntry returns one entry by id.
func (r *reader) GetEntry(ctx context.Context, id string) (Entry, error) {
	e, err := scanEntry(r.queryRow(ctx, "SELECT "+entryCols+" FROM entries e WHERE e.id = ?", id))
	if err != nil {
		return Entry{}, fmt.Errorf("entry %s: %w", id, err)
	}
	return e, nil
}

// ListEntries returns entries in insertion order, bounded by f.Limit.
func (r *reader) ListEntries(ctx context.Context, f EntryFilter) ([]Entry, error) {
	if err := validID("workspace", f.WorkspaceID); err != nil {
		return nil, err
	}
	where := []string{"e.workspace_id = ?", "e.pk > ?"}
	args := []any{f.WorkspaceID, f.AfterCursor}
	lifecycles := f.Lifecycles
	if len(lifecycles) == 0 {
		lifecycles = []string{LifecycleActive}
	}
	if !(len(lifecycles) == 1 && lifecycles[0] == "*") {
		where = append(where, "e.lifecycle IN ("+placeholders(len(lifecycles))+")")
		for _, l := range lifecycles {
			args = append(args, l)
		}
	}
	if f.Status != "" {
		where = append(where, "e.status = ?")
		args = append(args, f.Status)
	}
	if f.Promoted != nil {
		where = append(where, "e.promoted = ?")
		args = append(args, boolInt(*f.Promoted))
	}
	for _, c := range []struct{ col, val string }{
		{"e.kind", f.Kind}, {"e.tier", f.Tier}, {"e.scope", f.Scope}, {"e.scope_key", f.ScopeKey},
	} {
		if c.val != "" {
			where = append(where, c.col+" = ?")
			args = append(args, c.val)
		}
	}
	if f.ServedOnly {
		where = append(where, "e.promoted = 1", "e.lifecycle = 'active'", "(e.expires_at IS NULL OR e.expires_at > ?)")
		args = append(args, r.nowText())
	}
	if f.NeedsReverify {
		where = append(where, "e.needs_reverify = 1")
	}
	args = append(args, clampLimit(f.Limit))
	rows, err := r.query(ctx, "SELECT "+entryCols+" FROM entries e WHERE "+strings.Join(where, " AND ")+
		" ORDER BY e.pk LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func validateTags(tags []string) error {
	for _, t := range tags {
		if !tagPattern.MatchString(t) {
			return fmt.Errorf("%w: tag %q (letters, digits, '_', '.', '-' only)", ErrInvalid, t)
		}
	}
	return nil
}

func validateMetadata(m map[string]string) error {
	for k, v := range m {
		if !tagPattern.MatchString(k) {
			return fmt.Errorf("%w: metadata key %q", ErrInvalid, k)
		}
		if len(v) > 1024 || strings.ContainsRune(v, 0) {
			return fmt.Errorf("%w: metadata value for %q", ErrInvalid, k)
		}
	}
	return nil
}

// InsertEntry creates a pending entry authored by actor.
func (t *txn) InsertEntry(ctx context.Context, in NewEntry, actor Actor) (Entry, error) {
	if err := actor.validate(); err != nil {
		return Entry{}, err
	}
	return t.insertEntry(ctx, in, entryOrigin{source: actor.Principal, op: "propose", event: EventPropose}, actor)
}

// entryOrigin lets the importer create entries with their legacy author and times;
// an empty event means the caller records its own.
type entryOrigin struct {
	source    string
	op        string
	event     string
	createdAt string
	updatedAt string
}

func (t *txn) insertEntry(ctx context.Context, in NewEntry, o entryOrigin, actor Actor) (Entry, error) {
	if err := validID("entry", in.ID); err != nil {
		return Entry{}, err
	}
	if err := validID("workspace", in.WorkspaceID); err != nil {
		return Entry{}, err
	}
	scope := in.Scope
	if scope == "" {
		scope = "workspace"
	}
	permission := in.Permission
	if permission == "" {
		permission = "readonly"
	}
	switch {
	case !validScopes[scope]:
		return Entry{}, fmt.Errorf("%w: scope %q", ErrInvalid, scope)
	case !validTiers[in.Tier]:
		return Entry{}, fmt.Errorf("%w: tier %q", ErrInvalid, in.Tier)
	case !validPermissions[permission]:
		return Entry{}, fmt.Errorf("%w: permission %q", ErrInvalid, permission)
	case strings.TrimSpace(in.Content) == "":
		return Entry{}, fmt.Errorf("%w: content is required", ErrInvalid)
	case in.RequiredVerifications < 0:
		return Entry{}, fmt.Errorf("%w: required_verifications %d", ErrInvalid, in.RequiredVerifications)
	}
	source := strings.TrimSpace(o.source)
	if source == "" || validName("source", source) != nil {
		return Entry{}, fmt.Errorf("%w: entry source %q", ErrInvalid, o.source)
	}
	for _, v := range []struct{ kind, val string }{
		{"scope key", in.ScopeKey}, {"kind", in.Kind}, {"topic", in.Topic}, {"worktree", in.Worktree},
		{"retention class", in.RetentionClass},
	} {
		if err := validName(v.kind, v.val); err != nil {
			return Entry{}, err
		}
	}
	if err := validateTags(in.Tags); err != nil {
		return Entry{}, err
	}
	if err := validateMetadata(in.Metadata); err != nil {
		return Entry{}, err
	}
	required := in.RequiredVerifications
	if required == 0 {
		required = 2
	}
	retention := in.RetentionClass
	if retention == "" {
		retention = "memory"
	}
	now := t.nowText()
	createdAt, err := inputTime("created_at", o.createdAt)
	if err != nil {
		return Entry{}, err
	}
	if createdAt == "" {
		createdAt = now
	}
	updatedAt, err := inputTime("updated_at", o.updatedAt)
	if err != nil {
		return Entry{}, err
	}
	if updatedAt == "" {
		updatedAt = createdAt
	}
	expiresAt, err := inputTime("expires_at", in.ExpiresAt)
	if err != nil {
		return Entry{}, err
	}
	tags, err := encodeStrings(in.Tags)
	if err != nil {
		return Entry{}, err
	}
	meta, err := encodeMap(in.Metadata)
	if err != nil {
		return Entry{}, err
	}
	tokens, err := encodeStrings(in.SearchTokens)
	if err != nil {
		return Entry{}, err
	}
	title, description := strings.TrimSpace(in.Title), strings.TrimSpace(in.Description)
	digest := Digest(in.Content)
	res, err := t.exec(ctx, `INSERT INTO entries (id, workspace_id, scope, scope_key, kind, tier, topic, title,
		description, tags, metadata, permission, required_verifications, require_verification, source, source_key,
		source_owner, session_id, agent_id, revision, head_revision, content_digest, search_tokens, created_at,
		updated_at, expires_at, propose_head, propose_branch, propose_dirty, worktree, retention_class)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.WorkspaceID, scope, in.ScopeKey, in.Kind, in.Tier, in.Topic, title,
		description, tags, meta, permission, required, boolInt(in.RequireVerification), source, principalKey(source),
		actor.Owner, actor.SessionID, actor.AgentID, digest, tokens, createdAt,
		updatedAt, nullText(expiresAt), actor.HeadSHA, actor.Branch, nullBool(actor.Dirty), in.Worktree, retention)
	if err != nil {
		return Entry{}, fmt.Errorf("insert entry %s: %w", in.ID, err)
	}
	pk, err := res.LastInsertId()
	if err != nil {
		return Entry{}, err
	}
	if _, err := t.exec(ctx, `INSERT INTO revisions (entry_id, revision, base_revision, state, op, title, description,
		content, content_digest, content_bytes, author, author_key, session_id, head_sha, created_at, decided_at)
		VALUES (?, 1, 0, 'accepted', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, o.op, title, description, in.Content, digest, len(in.Content), source, principalKey(source),
		actor.SessionID, actor.HeadSHA, createdAt, createdAt); err != nil {
		return Entry{}, err
	}
	anchorText, err := t.insertAnchors(ctx, in.ID, in.Paths, in.Anchors)
	if err != nil {
		return Entry{}, err
	}
	if err := t.ftsPut(ctx, pk, title, joinBody(description, in.Content), anchorText); err != nil {
		return Entry{}, err
	}
	if o.event != "" {
		if err := t.appendEvent(ctx, actor, eventRow{
			WorkspaceID: in.WorkspaceID, EntryID: in.ID, Type: o.event, Revision: 1, NewDigest: digest,
			Data: map[string]any{"scope": scope, "author": source},
		}); err != nil {
			return Entry{}, err
		}
	}
	return t.GetEntry(ctx, in.ID)
}

func joinBody(description, content string) string {
	if description == "" {
		return content
	}
	return description + "\n" + content
}

// SetPromotion records the caller's vote reconciliation for the served revision.
func (t *txn) SetPromotion(ctx context.Context, id string, p Promotion, actor Actor) (Entry, error) {
	if err := actor.validate(); err != nil {
		return Entry{}, err
	}
	switch {
	case !validStatuses[p.Status]:
		return Entry{}, fmt.Errorf("%w: status %q", ErrInvalid, p.Status)
	case p.Promoted && (p.Status != StatusVerified || !validModes[p.VerificationMode]):
		return Entry{}, fmt.Errorf("%w: a promoted entry needs status verified and a verification mode", ErrInvalid)
	case !p.Promoted && (p.VerificationMode != "" || p.Status == StatusVerified):
		return Entry{}, fmt.Errorf("%w: an unpromoted entry has no verification mode and is not verified", ErrInvalid)
	}
	cur, err := t.liveEntry(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	if p.Promoted && cur.Lifecycle != LifecycleActive {
		return Entry{}, fmt.Errorf("%w: entry %s is %s and cannot be promoted", ErrInvalid, id, cur.Lifecycle)
	}
	if cur.Status == p.Status && cur.Promoted == p.Promoted && cur.VerificationMode == p.VerificationMode {
		return cur, nil
	}
	now := t.nowText()
	t.touch(id)
	if p.Promoted && !cur.Promoted {
		_, err = t.exec(ctx, `UPDATE entries SET status = ?, promoted = 1, verification_mode = ?, updated_at = ?,
			promoted_at = ?, promote_head = ?, promote_branch = ?, promote_dirty = ?,
			valid_from = coalesce(valid_from, ?),
			valid_from_commit = CASE WHEN valid_from IS NULL THEN ? ELSE valid_from_commit END
			WHERE id = ?`,
			p.Status, p.VerificationMode, now, now, actor.HeadSHA, actor.Branch, nullBool(actor.Dirty), now, actor.HeadSHA, id)
	} else {
		_, err = t.exec(ctx, `UPDATE entries SET status = ?, promoted = ?, verification_mode = ?, updated_at = ? WHERE id = ?`,
			p.Status, boolInt(p.Promoted), p.VerificationMode, now, id)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("set promotion %s: %w", id, err)
	}
	evType := EventStatus
	switch {
	case p.Promoted && !cur.Promoted:
		evType = EventPromote
	case !p.Promoted && cur.Promoted:
		evType = EventDemote
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: cur.WorkspaceID, EntryID: id, Type: evType, Revision: cur.Revision,
		OldDigest: cur.ContentDigest, NewDigest: cur.ContentDigest,
		Data: map[string]any{
			"from": map[string]any{"status": cur.Status, "promoted": cur.Promoted, "verification_mode": cur.VerificationMode},
			"to":   map[string]any{"status": p.Status, "promoted": p.Promoted, "verification_mode": p.VerificationMode},
		},
	}); err != nil {
		return Entry{}, err
	}
	return t.GetEntry(ctx, id)
}

// Supersede retires each old entry in favour of the new one, atomically: the old
// entries become superseded with their validity closed, never deleted.
func (t *txn) Supersede(ctx context.Context, in SupersedeInput, actor Actor) error {
	if err := actor.validate(); err != nil {
		return err
	}
	if len(in.OldIDs) == 0 {
		return fmt.Errorf("%w: nothing to supersede", ErrInvalid)
	}
	newer, err := t.GetEntry(ctx, in.NewID)
	if err != nil {
		return err
	}
	if !newer.Promoted || newer.Lifecycle != LifecycleActive {
		return fmt.Errorf("%w: superseding entry %s must be promoted and active", ErrInvalid, in.NewID)
	}
	now := t.nowText()
	for _, oldID := range in.OldIDs {
		old, err := t.GetEntry(ctx, oldID)
		if err != nil {
			return err
		}
		switch {
		case oldID == in.NewID:
			return fmt.Errorf("%w: an entry cannot supersede itself", ErrInvalid)
		case old.WorkspaceID != newer.WorkspaceID:
			return fmt.Errorf("%w: %s and %s are in different workspaces", ErrInvalid, oldID, in.NewID)
		case old.Lifecycle != LifecycleActive:
			return fmt.Errorf("%w: entry %s is %s", ErrInvalid, oldID, old.Lifecycle)
		}
		if _, err := t.exec(ctx, `UPDATE entries SET lifecycle = 'superseded', superseded_by = ?, updated_at = ?,
			invalidated_at = coalesce(invalidated_at, ?), invalidated_commit = ?, expired_at = coalesce(expired_at, ?)
			WHERE id = ?`, in.NewID, now, now, actor.HeadSHA, now, oldID); err != nil {
			return err
		}
		if err := t.addRelation(ctx, in.NewID, oldID, RelationSupersedes, actor); err != nil {
			return err
		}
		if err := t.appendEvent(ctx, actor, eventRow{
			WorkspaceID: old.WorkspaceID, EntryID: oldID, Type: EventSupersede, Revision: old.Revision,
			OldDigest: old.ContentDigest, Note: in.Reason, Data: map[string]any{"superseded_by": in.NewID},
		}); err != nil {
			return err
		}
	}
	return t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: newer.WorkspaceID, EntryID: in.NewID, Type: EventSupersede, Revision: newer.Revision,
		NewDigest: newer.ContentDigest, Note: in.Reason, Data: map[string]any{"supersedes": in.OldIDs},
	})
}

// transitions maps lifecycle moves to their event type. Supersede and Purge are
// separate because they carry extra state.
var transitions = map[string]map[string]string{
	LifecycleActive:     {LifecycleRetracted: EventRetract, LifecycleArchived: EventArchive, LifecycleMerged: EventMerge},
	LifecycleArchived:   {LifecycleActive: EventRestore},
	LifecycleRetracted:  {LifecycleActive: EventRestore},
	LifecycleSuperseded: {LifecycleActive: EventRestore},
	LifecycleMerged:     {LifecycleActive: EventRestore},
}

// Transition retracts, archives, merges or restores an entry.
func (t *txn) Transition(ctx context.Context, id string, in TransitionInput, actor Actor) (Entry, error) {
	if err := actor.validate(); err != nil {
		return Entry{}, err
	}
	cur, err := t.GetEntry(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	evType, ok := transitions[cur.Lifecycle][in.To]
	if !ok {
		return Entry{}, fmt.Errorf("%w: cannot move entry %s from %s to %s", ErrInvalid, id, cur.Lifecycle, in.To)
	}
	now := t.nowText()
	demote := in.To == LifecycleActive && cur.Lifecycle != LifecycleArchived && (cur.Promoted || cur.Status == StatusVerified)
	switch in.To {
	case LifecycleActive:
		if cur.Lifecycle == LifecycleArchived {
			_, err = t.exec(ctx, `UPDATE entries SET lifecycle = 'active', updated_at = ?, invalidated_at = NULL,
				invalidated_commit = '', expired_at = NULL, superseded_by = '', merged_into = '' WHERE id = ?`, now, id)
			break
		}
		// Back from retracted, superseded or merged: nothing verified earlier carries over,
		// including the votes, which a new epoch leaves behind.
		if _, err = t.exec(ctx, `UPDATE entries SET lifecycle = 'active', updated_at = ?, invalidated_at = NULL,
			invalidated_commit = '', expired_at = NULL, superseded_by = '', merged_into = '',
			status = CASE WHEN status = 'verified' THEN 'pending' ELSE status END, promoted = 0, verification_mode = '',
			needs_reverify = 0, stale_since = NULL, vote_epoch = vote_epoch + 1 WHERE id = ?`, now, id); err == nil {
			err = t.clearBaselines(ctx, id)
		}
		t.touch(id)
	case LifecycleMerged:
		target, terr := t.GetEntry(ctx, in.Target)
		if terr != nil {
			return Entry{}, terr
		}
		if target.ID == id || target.WorkspaceID != cur.WorkspaceID || target.Lifecycle != LifecycleActive {
			return Entry{}, fmt.Errorf("%w: merge target %s must be another active entry in the workspace", ErrInvalid, in.Target)
		}
		if _, err = t.exec(ctx, `UPDATE entries SET lifecycle = 'merged', merged_into = ?, updated_at = ?,
			invalidated_at = coalesce(invalidated_at, ?), invalidated_commit = ?, expired_at = coalesce(expired_at, ?)
			WHERE id = ?`, target.ID, now, now, actor.HeadSHA, now, id); err == nil {
			err = t.addRelation(ctx, id, target.ID, RelationMergedInto, actor)
		}
	default:
		_, err = t.exec(ctx, `UPDATE entries SET lifecycle = ?, updated_at = ?,
			invalidated_at = coalesce(invalidated_at, ?), invalidated_commit = ?, expired_at = coalesce(expired_at, ?)
			WHERE id = ?`, in.To, now, now, actor.HeadSHA, now, id)
	}
	if err != nil {
		return Entry{}, err
	}
	data := map[string]any{"from": cur.Lifecycle, "to": in.To, "target": in.Target}
	if in.To == LifecycleActive && cur.Lifecycle != LifecycleArchived {
		data["vote_epoch"] = cur.VoteEpoch + 1
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: cur.WorkspaceID, EntryID: id, Type: evType, Revision: cur.Revision, OldDigest: cur.ContentDigest,
		Note: in.Reason, Data: data,
	}); err != nil {
		return Entry{}, err
	}
	if demote {
		if err := t.appendEvent(ctx, actor, eventRow{
			WorkspaceID: cur.WorkspaceID, EntryID: id, Type: EventDemote, Revision: cur.Revision,
			OldDigest: cur.ContentDigest, NewDigest: cur.ContentDigest,
			Data: map[string]any{
				"reason": "restored from " + cur.Lifecycle + "; must be verified again",
				"from":   map[string]any{"status": cur.Status, "promoted": cur.Promoted, "verification_mode": cur.VerificationMode},
			},
		}); err != nil {
			return Entry{}, err
		}
	}
	return t.GetEntry(ctx, id)
}

// SetExpiry sets or clears (expiresAt "") the time after which the entry is hidden.
// Expiry hides; it never deletes (PAR-GOV-12).
func (t *txn) SetExpiry(ctx context.Context, id, expiresAt string, actor Actor) (Entry, error) {
	if err := actor.validate(); err != nil {
		return Entry{}, err
	}
	v, err := inputTime("expires_at", expiresAt)
	if err != nil {
		return Entry{}, err
	}
	return t.updateField(ctx, id, actor, EventExpiry, "expires_at", nullText(v), func(e Entry) string { return e.ExpiresAt }, v)
}

// SetTier moves an entry between the core and deferred tiers (PAR-GOV-08).
func (t *txn) SetTier(ctx context.Context, id, tier string, actor Actor) (Entry, error) {
	if err := actor.validate(); err != nil {
		return Entry{}, err
	}
	if !validTiers[tier] {
		return Entry{}, fmt.Errorf("%w: tier %q", ErrInvalid, tier)
	}
	return t.updateField(ctx, id, actor, EventTierChange, "tier", tier, func(e Entry) string { return e.Tier }, tier)
}

func (t *txn) updateField(ctx context.Context, id string, actor Actor, evType, column string, value any,
	current func(Entry) string, display string) (Entry, error) {
	cur, err := t.GetEntry(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	if cur.Lifecycle == LifecyclePurged {
		return Entry{}, fmt.Errorf("%w: entry %s is purged", ErrInvalid, id)
	}
	if current(cur) == display {
		return cur, nil
	}
	if _, err := t.exec(ctx, "UPDATE entries SET "+column+" = ?, updated_at = ? WHERE id = ?", value, t.nowText(), id); err != nil {
		return Entry{}, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: cur.WorkspaceID, EntryID: id, Type: evType, Revision: cur.Revision,
		Data: map[string]any{"from": current(cur), "to": display},
	}); err != nil {
		return Entry{}, err
	}
	return t.GetEntry(ctx, id)
}

// SetClassification replaces kind, topic, tags and metadata.
func (t *txn) SetClassification(ctx context.Context, id string, c Classification, actor Actor) (Entry, error) {
	if err := actor.validate(); err != nil {
		return Entry{}, err
	}
	if err := validName("kind", c.Kind); err != nil {
		return Entry{}, err
	}
	if err := validName("topic", c.Topic); err != nil {
		return Entry{}, err
	}
	if err := validateTags(c.Tags); err != nil {
		return Entry{}, err
	}
	if err := validateMetadata(c.Metadata); err != nil {
		return Entry{}, err
	}
	cur, err := t.GetEntry(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	if cur.Lifecycle == LifecyclePurged {
		return Entry{}, fmt.Errorf("%w: entry %s is purged", ErrInvalid, id)
	}
	tags, err := encodeStrings(c.Tags)
	if err != nil {
		return Entry{}, err
	}
	meta, err := encodeMap(c.Metadata)
	if err != nil {
		return Entry{}, err
	}
	if _, err := t.exec(ctx, "UPDATE entries SET kind = ?, topic = ?, tags = ?, metadata = ?, updated_at = ? WHERE id = ?",
		c.Kind, c.Topic, tags, meta, t.nowText(), id); err != nil {
		return Entry{}, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: cur.WorkspaceID, EntryID: id, Type: EventClassify, Revision: cur.Revision,
		Data: map[string]any{"kind": c.Kind, "topic": c.Topic, "tags": c.Tags},
	}); err != nil {
		return Entry{}, err
	}
	return t.GetEntry(ctx, id)
}

// Purge hard-deletes an entry's text: every revision's content, title, description
// and edit reason, vote and outcome notes, anchors, claims, the search row, and the
// free text of its events. Pending revisions are withdrawn. What remains is a
// tombstone: the entry id, its lifecycle, the digests of every revision, and the event
// skeleton ending in a purge event, whose note is the purge reason. Nothing can attach
// free text to the entry afterwards.
//
// The text also leaves the database file. The writer runs with secure_delete=ON, so
// the pages the purge frees (large content lives in overflow pages) are zeroed, and
// the search index is rewritten with 'optimize': a contentless FTS5 delete only
// tombstones the row, leaving its terms in the index segments. The optimize costs a
// rewrite of the memory index, which is acceptable for a rare administrative purge.
// Until a Checkpoint the old page images stay in the database file and the WAL still
// holds the new ones, so call Checkpoint after the Update commits. Backups taken
// before the purge still hold the text. Purge is idempotent.
func (t *txn) Purge(ctx context.Context, id, reason string, actor Actor) error {
	if err := actor.validate(); err != nil {
		return err
	}
	cur, err := t.GetEntry(ctx, id)
	if err != nil {
		return err
	}
	if cur.Lifecycle == LifecyclePurged {
		return nil
	}
	now := t.nowText()
	steps := []struct {
		query string
		args  []any
	}{
		{`UPDATE entries SET lifecycle = 'purged', promoted = 0, verification_mode = '',
			status = CASE WHEN status = 'verified' THEN 'pending' ELSE status END,
			title = '', description = '', topic = '', tags = '[]', metadata = '{}', search_tokens = '[]',
			needs_reverify = 0, stale_since = NULL, head_revision = revision, updated_at = ?,
			invalidated_at = coalesce(invalidated_at, ?), expired_at = coalesce(expired_at, ?) WHERE id = ?`,
			[]any{now, now, now, id}},
		{`UPDATE revisions SET content = NULL, title = '', description = '', reason = '',
			state = CASE WHEN state = 'pending' THEN 'withdrawn' ELSE state END,
			decided_at = CASE WHEN state = 'pending' THEN ? ELSE decided_at END,
			content_dropped_at = coalesce(content_dropped_at, ?), content_dropped_reason = 'purge' WHERE entry_id = ?`,
			[]any{now, now, id}},
		{`UPDATE votes SET note = '' WHERE entry_id = ?`, []any{id}},
		{`UPDATE outcomes SET note = '' WHERE entry_id = ?`, []any{id}},
		{`DELETE FROM anchors WHERE entry_id = ?`, []any{id}},
		{`DELETE FROM claims WHERE entry_id = ?`, []any{id}},
		{`UPDATE events SET note = NULL, data = NULL, redacted = 1 WHERE entry_id = ? AND redacted = 0`, []any{id}},
	}
	for _, s := range steps {
		if _, err := t.exec(ctx, s.query, s.args...); err != nil {
			return fmt.Errorf("purge %s: %w", id, err)
		}
	}
	if err := t.ftsDelete(ctx, cur.Cursor); err != nil {
		return err
	}
	if err := t.ftsOptimize(ctx); err != nil {
		return err
	}
	t.touch(id)
	return t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: cur.WorkspaceID, EntryID: id, Type: EventPurge, Revision: cur.Revision,
		OldDigest: cur.ContentDigest, Note: reason,
	})
}

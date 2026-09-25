package govstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Revision states.
const (
	RevisionAccepted  = "accepted"
	RevisionPending   = "pending"
	RevisionRejected  = "rejected"
	RevisionWithdrawn = "withdrawn"
)

var validRevisionOps = set("edit", "str_replace", "insert", "update_description", "restore")

// Revision is one content version. Content is empty and ContentDropped true once
// retention or purge dropped it; the digest always remains.
type Revision struct {
	EntryID              string `json:"entry_id"`
	Revision             int64  `json:"revision"`
	BaseRevision         int64  `json:"base_revision"`
	State                string `json:"state"`
	Op                   string `json:"op"`
	Title                string `json:"title"`
	Description          string `json:"description,omitempty"`
	Content              string `json:"content,omitempty"`
	ContentDropped       bool   `json:"content_dropped,omitempty"`
	ContentDigest        string `json:"content_digest"`
	ContentBytes         int64  `json:"content_bytes"`
	Reason               string `json:"reason,omitempty"`
	Author               string `json:"author"`
	SessionID            string `json:"session_id,omitempty"`
	HeadSHA              string `json:"head_sha,omitempty"`
	CreatedAt            string `json:"created_at"`
	DecidedAt            string `json:"decided_at,omitempty"`
	ContentDroppedAt     string `json:"content_dropped_at,omitempty"`
	ContentDroppedReason string `json:"content_dropped_reason,omitempty"`
}

// RevisionInput proposes a new revision on top of BaseRevision, which must equal the
// entry's head revision (compare-and-set).
type RevisionInput struct {
	EntryID      string
	BaseRevision int64
	Op           string  // edit | str_replace | insert | update_description | restore
	Title        *string // nil keeps the base title
	Description  *string // nil keeps the base description
	Content      string  // the full new content
	Reason       string
	// Activate makes the revision the served one at once (the legacy in-place edit).
	// The entry then loses its promotion and must be verified again. Otherwise the
	// revision stays pending (PAR-GOV-05) until AcceptRevision.
	Activate     bool
	SearchTokens []string // replaces the entry's tokens when the revision becomes served
}

// AcceptInput makes a pending revision the served one.
type AcceptInput struct {
	EntryID      string
	Revision     int64
	SearchTokens []string
}

// RevisionFilter pages revisions newest first.
type RevisionFilter struct {
	BeforeRevision int64 // 0 = from the newest
	Limit          int
	WithContent    bool
}

// EntryContent is the served content of an entry, bound to its digest.
type EntryContent struct {
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
	Content  string `json:"content"`
	// Withheld explains why Content is empty: dropped by retention or purge, or it
	// failed the digest check. Withheld content is never returned.
	Withheld string `json:"withheld,omitempty"`
}

// RevisionReader reads revisions and served content.
type RevisionReader interface {
	GetRevision(ctx context.Context, entryID string, revision int64) (Revision, error)
	ListRevisions(ctx context.Context, entryID string, f RevisionFilter) ([]Revision, error)
	// EntryContents returns the served content for each id, re-hashed against the
	// recorded digest.
	EntryContents(ctx context.Context, ids []string) (map[string]EntryContent, error)
}

// RevisionWriter writes revisions.
type RevisionWriter interface {
	AppendRevision(ctx context.Context, in RevisionInput, actor Actor) (Revision, error)
	AcceptRevision(ctx context.Context, in AcceptInput, actor Actor) (Entry, error)
	RejectRevision(ctx context.Context, entryID string, revision int64, reason string, actor Actor) (Revision, error)
}

const revisionCols = `entry_id, revision, base_revision, state, op, title, description, coalesce(content, ''),
	content IS NULL, content_digest, content_bytes, reason, author, session_id, head_sha, created_at,
	coalesce(decided_at, ''), coalesce(content_dropped_at, ''), coalesce(content_dropped_reason, '')`

func scanRevision(s scanner) (Revision, error) {
	var rv Revision
	var dropped int
	err := s.Scan(&rv.EntryID, &rv.Revision, &rv.BaseRevision, &rv.State, &rv.Op, &rv.Title, &rv.Description,
		&rv.Content, &dropped, &rv.ContentDigest, &rv.ContentBytes, &rv.Reason, &rv.Author, &rv.SessionID,
		&rv.HeadSHA, &rv.CreatedAt, &rv.DecidedAt, &rv.ContentDroppedAt, &rv.ContentDroppedReason)
	rv.ContentDropped = dropped == 1
	return rv, err
}

// GetRevision returns one revision with its content.
func (r *reader) GetRevision(ctx context.Context, entryID string, revision int64) (Revision, error) {
	rv, err := scanRevision(r.queryRow(ctx, "SELECT "+revisionCols+" FROM revisions WHERE entry_id = ? AND revision = ?",
		entryID, revision))
	if err != nil {
		return Revision{}, fmt.Errorf("revision %s@%d: %w", entryID, revision, err)
	}
	return rv, nil
}

// ListRevisions returns an entry's revisions newest first.
func (r *reader) ListRevisions(ctx context.Context, entryID string, f RevisionFilter) ([]Revision, error) {
	before := f.BeforeRevision
	if before <= 0 {
		before = 1 << 62
	}
	cols := revisionCols
	if !f.WithContent {
		cols = strings.Replace(cols, "coalesce(content, '')", "''", 1)
	}
	rows, err := r.query(ctx, "SELECT "+cols+" FROM revisions WHERE entry_id = ? AND revision < ? ORDER BY revision DESC LIMIT ?",
		entryID, before, clampLimit(f.Limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		rv, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rv)
	}
	return out, rows.Err()
}

// EntryContents reads the served revision of each id. Content that no longer hashes to
// the recorded digest is withheld, never returned (the trust binding of recall).
func (r *reader) EntryContents(ctx context.Context, ids []string) (map[string]EntryContent, error) {
	out := make(map[string]EntryContent, len(ids))
	for start := 0; start < len(ids); start += maxInArgs {
		chunk := ids[start:min(start+maxInArgs, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := r.query(ctx, `SELECT e.id, e.revision, e.content_digest, r.content, r.content_dropped_reason
			FROM entries e JOIN revisions r ON r.entry_id = e.id AND r.revision = e.revision
			WHERE e.id IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, digest string
			var rev int64
			var content, dropped *string
			if err := rows.Scan(&id, &rev, &digest, &content, &dropped); err != nil {
				rows.Close()
				return nil, err
			}
			c := EntryContent{Revision: rev, Digest: digest}
			switch {
			case content == nil:
				c.Withheld = "content dropped"
				if dropped != nil {
					c.Withheld = "content dropped by " + *dropped
				}
			case Digest(*content) != digest:
				c.Withheld = "content does not match its digest"
			default:
				c.Content = *content
			}
			out[id] = c
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AppendRevision adds a revision with compare-and-set on the entry's head revision. A
// stale base returns a *ConflictError carrying the current revision and digest.
func (t *txn) AppendRevision(ctx context.Context, in RevisionInput, actor Actor) (Revision, error) {
	if err := actor.validate(); err != nil {
		return Revision{}, err
	}
	if !validRevisionOps[in.Op] {
		return Revision{}, fmt.Errorf("%w: revision op %q", ErrInvalid, in.Op)
	}
	if strings.TrimSpace(in.Content) == "" {
		return Revision{}, fmt.Errorf("%w: content is required", ErrInvalid)
	}
	cur, err := t.GetEntry(ctx, in.EntryID)
	if err != nil {
		return Revision{}, err
	}
	if cur.Lifecycle != LifecycleActive {
		return Revision{}, fmt.Errorf("%w: entry %s is %s", ErrInvalid, in.EntryID, cur.Lifecycle)
	}
	if in.BaseRevision != cur.HeadRevision {
		return Revision{}, &ConflictError{
			EntryID: in.EntryID, BaseRevision: in.BaseRevision, CurrentRevision: cur.Revision,
			CurrentDigest: cur.ContentDigest, HeadRevision: cur.HeadRevision, Reason: "base revision is not the head",
		}
	}
	base, err := t.GetRevision(ctx, in.EntryID, cur.HeadRevision)
	if err != nil {
		return Revision{}, err
	}
	if base.ContentDropped {
		return Revision{}, fmt.Errorf("%w: base revision %d has no content", ErrInvalid, base.Revision)
	}
	title, description := base.Title, base.Description
	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
	}
	if in.Description != nil {
		description = strings.TrimSpace(*in.Description)
	}
	digest := Digest(in.Content)
	if digest == base.ContentDigest && title == base.Title && description == base.Description {
		return Revision{}, fmt.Errorf("%w: revision %d already has this content", ErrNoChange, base.Revision)
	}
	// Revision numbers are never reused: a rejected revision keeps its number even
	// though the live head moves back below it.
	var next int64
	if err := t.queryRow(ctx, "SELECT max(revision) + 1 FROM revisions WHERE entry_id = ?", in.EntryID).Scan(&next); err != nil {
		return Revision{}, err
	}
	state := RevisionPending
	now := t.nowText()
	var decidedAt any
	if in.Activate {
		state, decidedAt = RevisionAccepted, now
	}
	if _, err := t.exec(ctx, `INSERT INTO revisions (entry_id, revision, base_revision, state, op, title, description,
		content, content_digest, content_bytes, reason, author, author_key, session_id, head_sha, created_at, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.EntryID, next, in.BaseRevision, state, in.Op, title, description, in.Content, digest, len(in.Content),
		in.Reason, strings.TrimSpace(actor.Principal), principalKey(actor.Principal), actor.SessionID, actor.HeadSHA,
		now, decidedAt); err != nil {
		return Revision{}, err
	}
	if in.Activate {
		if err := t.serveRevision(ctx, cur, next, title, description, digest, in.SearchTokens, actor); err != nil {
			return Revision{}, err
		}
	} else if _, err := t.exec(ctx, "UPDATE entries SET head_revision = ?, updated_at = ? WHERE id = ?", next, now, in.EntryID); err != nil {
		return Revision{}, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: cur.WorkspaceID, EntryID: in.EntryID, Type: EventEdit, Revision: next,
		OldDigest: base.ContentDigest, NewDigest: digest, Note: in.Reason,
		Data: map[string]any{"op": in.Op, "base_revision": in.BaseRevision, "state": state},
	}); err != nil {
		return Revision{}, err
	}
	if in.Activate && cur.Promoted {
		if err := t.appendEvent(ctx, actor, eventRow{
			WorkspaceID: cur.WorkspaceID, EntryID: in.EntryID, Type: EventDemote, Revision: next,
			OldDigest: cur.ContentDigest, NewDigest: digest,
			Data: map[string]any{"reason": "served revision changed; verification binds to a revision"},
		}); err != nil {
			return Revision{}, err
		}
	}
	return t.GetRevision(ctx, in.EntryID, next)
}

// serveRevision makes rev the served revision. A served-content change always clears
// promotion, the verification mode and the drift baselines: votes and baselines bind
// to the revision they were cast on, so nothing unverified can look verified. Pending
// revisions older than rev are withdrawn.
func (t *txn) serveRevision(ctx context.Context, cur Entry, rev int64, title, description, digest string,
	tokens []string, actor Actor) error {
	now := t.nowText()
	tokenJSON, err := encodeStrings(tokens)
	if err != nil {
		return err
	}
	if _, err := t.exec(ctx, `UPDATE entries SET revision = ?, head_revision = max(head_revision, ?), content_digest = ?,
		title = ?, description = ?, search_tokens = ?, status = 'pending', promoted = 0, verification_mode = '',
		needs_reverify = 0, stale_since = NULL, updated_at = ? WHERE id = ?`,
		rev, rev, digest, title, description, tokenJSON, now, cur.ID); err != nil {
		return err
	}
	if err := t.clearBaselines(ctx, cur.ID); err != nil {
		return err
	}
	res, err := t.exec(ctx, `UPDATE revisions SET state = 'withdrawn', decided_at = ?
		WHERE entry_id = ? AND state = 'pending' AND revision < ?`, now, cur.ID, rev)
	if err != nil {
		return err
	}
	if rowsAffected(res) > 0 {
		if err := t.appendEvent(ctx, actor, eventRow{
			WorkspaceID: cur.WorkspaceID, EntryID: cur.ID, Type: EventRevisionWithdrawn, Revision: rev,
			Data: map[string]any{"withdrawn_below": rev, "count": rowsAffected(res)},
		}); err != nil {
			return err
		}
	}
	t.touch(cur.ID)
	return t.refreshFTS(ctx, cur.ID)
}

// clearBaselines drops an entry's drift baselines and stale marks. Baselines are
// captured at promotion, so whatever clears a promotion clears them too.
func (t *txn) clearBaselines(ctx context.Context, entryID string) error {
	_, err := t.exec(ctx, `UPDATE anchors SET baseline_state = 'none', baseline_hash = '', baseline_kind = '',
		baseline_commit = '', baseline_at = NULL, stale_since = NULL, stale_commit = '' WHERE entry_id = ?`, entryID)
	return err
}

// AcceptRevision makes the pending head revision the served one. Like any change of
// served content it clears promotion; the caller re-promotes from the votes cast on
// the accepted revision within the same transaction.
func (t *txn) AcceptRevision(ctx context.Context, in AcceptInput, actor Actor) (Entry, error) {
	if err := actor.validate(); err != nil {
		return Entry{}, err
	}
	cur, err := t.GetEntry(ctx, in.EntryID)
	if err != nil {
		return Entry{}, err
	}
	if cur.Lifecycle != LifecycleActive {
		return Entry{}, fmt.Errorf("%w: entry %s is %s", ErrInvalid, in.EntryID, cur.Lifecycle)
	}
	rv, err := t.GetRevision(ctx, in.EntryID, in.Revision)
	if err != nil {
		return Entry{}, err
	}
	if rv.State != RevisionPending || in.Revision != cur.HeadRevision {
		return Entry{}, &ConflictError{
			EntryID: in.EntryID, BaseRevision: in.Revision, CurrentRevision: cur.Revision,
			CurrentDigest: cur.ContentDigest, HeadRevision: cur.HeadRevision,
			Reason: "only the pending head revision can be accepted",
		}
	}
	if _, err := t.exec(ctx, "UPDATE revisions SET state = 'accepted', decided_at = ? WHERE entry_id = ? AND revision = ?",
		t.nowText(), in.EntryID, in.Revision); err != nil {
		return Entry{}, err
	}
	if err := t.serveRevision(ctx, cur, in.Revision, rv.Title, rv.Description, rv.ContentDigest, in.SearchTokens, actor); err != nil {
		return Entry{}, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: cur.WorkspaceID, EntryID: in.EntryID, Type: EventRevisionAccepted, Revision: in.Revision,
		OldDigest: cur.ContentDigest, NewDigest: rv.ContentDigest,
	}); err != nil {
		return Entry{}, err
	}
	if cur.Promoted {
		if err := t.appendEvent(ctx, actor, eventRow{
			WorkspaceID: cur.WorkspaceID, EntryID: in.EntryID, Type: EventDemote, Revision: in.Revision,
			OldDigest: cur.ContentDigest, NewDigest: rv.ContentDigest,
			Data: map[string]any{"reason": "served revision changed; verification binds to a revision"},
		}); err != nil {
			return Entry{}, err
		}
	}
	return t.GetEntry(ctx, in.EntryID)
}

// RejectRevision closes a pending revision without serving it. A purged entry has no
// pending revisions (purge withdraws them) and takes no new free text.
func (t *txn) RejectRevision(ctx context.Context, entryID string, revision int64, reason string, actor Actor) (Revision, error) {
	if err := actor.validate(); err != nil {
		return Revision{}, err
	}
	cur, err := t.liveEntry(ctx, entryID)
	if err != nil {
		return Revision{}, err
	}
	rv, err := t.GetRevision(ctx, entryID, revision)
	if err != nil {
		return Revision{}, err
	}
	if rv.State != RevisionPending {
		return Revision{}, fmt.Errorf("%w: revision %d is %s, not pending", ErrInvalid, revision, rv.State)
	}
	now := t.nowText()
	if _, err := t.exec(ctx, "UPDATE revisions SET state = 'rejected', decided_at = ? WHERE entry_id = ? AND revision = ?",
		now, entryID, revision); err != nil {
		return Revision{}, err
	}
	// The live head falls back to the newest pending revision, or the served one, so
	// the next edit builds on content that is still alive.
	if _, err := t.exec(ctx, `UPDATE entries SET updated_at = ?1, head_revision = coalesce(
		(SELECT max(revision) FROM revisions WHERE entry_id = ?2 AND state = 'pending'), revision) WHERE id = ?2`,
		now, entryID); err != nil {
		return Revision{}, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: cur.WorkspaceID, EntryID: entryID, Type: EventRevisionRejected, Revision: revision,
		NewDigest: rv.ContentDigest, Note: reason,
	}); err != nil {
		return Revision{}, err
	}
	return t.GetRevision(ctx, entryID, revision)
}

// IsConflict unwraps a revision conflict.
func IsConflict(err error) (*ConflictError, bool) {
	var ce *ConflictError
	ok := errors.As(err, &ce)
	return ce, ok
}

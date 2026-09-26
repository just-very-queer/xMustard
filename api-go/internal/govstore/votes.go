package govstore

import (
	"context"
	"fmt"
	"strings"
)

// Verdicts a principal can cast on a revision.
const (
	VerdictApprove     = "approve"
	VerdictReject      = "reject"
	VerdictDuplicateOf = "duplicate_of"
	VerdictRetract     = "retract"
)

var validVerdicts = set(VerdictApprove, VerdictReject, VerdictDuplicateOf, VerdictRetract)

// Vote is a principal's latest verdict on one revision. A verdict binds to the
// revision and the content digest it was cast on, and to the entry's vote epoch: once
// a restore starts a new epoch, earlier verdicts stop counting and are no longer listed
// (the event history keeps them).
type Vote struct {
	EntryID        string `json:"entry_id"`
	Revision       int64  `json:"revision"`
	Principal      string `json:"principal"`
	PrincipalOwner string `json:"principal_owner,omitempty"`
	PrincipalKind  string `json:"principal_kind,omitempty"`
	Verdict        string `json:"verdict"`
	Target         string `json:"target,omitempty"`
	Note           string `json:"note,omitempty"`
	EvidenceHandle string `json:"evidence_handle,omitempty"`
	ContentDigest  string `json:"content_digest"`
	SessionID      string `json:"session_id,omitempty"`
	Ordinal        int64  `json:"ordinal"`
	Epoch          int64  `json:"epoch,omitempty"`
	At             string `json:"at"`
}

// VoteInput casts actor's verdict. Revision 0 means the served revision; a pending
// revision can be voted on too. A new verdict replaces the principal's earlier one.
type VoteInput struct {
	EntryID        string
	Revision       int64
	Verdict        string
	Target         string // duplicate_of target entry
	Note           string
	EvidenceHandle string
}

// Tally counts the distinct latest verdicts on one revision in the entry's current vote
// epoch.
type Tally struct {
	EntryID  string `json:"entry_id"`
	Revision int64  `json:"revision"`
	// Approvals and Rejections count every distinct principal.
	Approvals  int `json:"approvals"`
	Rejections int `json:"rejections"`
	// PeerApprovals and PeerRejections leave out the authors (the entry's author and
	// the author of this revision) and the open-mode identity: only these can make an
	// entry peer_verified.
	PeerApprovals  int `json:"peer_approvals"`
	PeerRejections int `json:"peer_rejections"`
	// AuthorApproved: the entry's author or this revision's author approved it.
	AuthorApproved   bool `json:"author_approved"`
	OpenModeApproved bool `json:"open_mode_approved"`
	Retractions      int  `json:"retractions"`
	Duplicates       int  `json:"duplicates"`
}

// VoteReader reads votes.
type VoteReader interface {
	ListVotes(ctx context.Context, entryID string, revision int64) ([]Vote, error)
	// ServedVotes returns ListVotes(id, 0) for many entries at once, keyed by entry id.
	ServedVotes(ctx context.Context, ids []string) (map[string][]Vote, error)
	Tally(ctx context.Context, entryID string, revision int64) (Tally, error)
}

// VoteWriter casts votes.
type VoteWriter interface {
	RecordVote(ctx context.Context, in VoteInput, actor Actor) (Vote, error)
}

func (r *reader) resolveRevision(ctx context.Context, entryID string, revision int64) (int64, error) {
	if revision > 0 {
		return revision, nil
	}
	var served int64
	if err := r.queryRow(ctx, "SELECT revision FROM entries WHERE id = ?", entryID).Scan(&served); err != nil {
		return 0, fmt.Errorf("entry %s: %w", entryID, err)
	}
	return served, nil
}

// ListVotes returns the latest verdicts on a revision (0 = served) in cast order, from
// the entry's current vote epoch: the verdicts that count.
func (r *reader) ListVotes(ctx context.Context, entryID string, revision int64) ([]Vote, error) {
	rev, err := r.resolveRevision(ctx, entryID, revision)
	if err != nil {
		return nil, err
	}
	rows, err := r.query(ctx, `SELECT v.entry_id, v.revision, v.principal, v.principal_owner, v.principal_kind, v.verdict,
		v.target, v.note, v.evidence_handle, v.content_digest, v.session_id, v.ordinal, v.epoch, v.at
		FROM votes v JOIN entries e ON e.id = v.entry_id AND e.vote_epoch = v.epoch
		WHERE v.entry_id = ? AND v.revision = ? ORDER BY v.ordinal, v.pk`, entryID, rev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Vote
	for rows.Next() {
		var v Vote
		if err := rows.Scan(&v.EntryID, &v.Revision, &v.Principal, &v.PrincipalOwner, &v.PrincipalKind, &v.Verdict,
			&v.Target, &v.Note, &v.EvidenceHandle, &v.ContentDigest, &v.SessionID, &v.Ordinal, &v.Epoch, &v.At); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ServedVotes reads the counting verdicts on each entry's served revision in bounded
// batches, so a listing costs one query per batch instead of one per entry.
func (r *reader) ServedVotes(ctx context.Context, ids []string) (map[string][]Vote, error) {
	out := make(map[string][]Vote, len(ids))
	for start := 0; start < len(ids); start += maxInArgs {
		chunk := ids[start:min(start+maxInArgs, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := r.query(ctx, `SELECT v.entry_id, v.revision, v.principal, v.principal_owner, v.principal_kind,
			v.verdict, v.target, v.note, v.evidence_handle, v.content_digest, v.session_id, v.ordinal, v.epoch, v.at
			FROM votes v JOIN entries e ON e.id = v.entry_id AND e.vote_epoch = v.epoch AND e.revision = v.revision
			WHERE v.entry_id IN (`+placeholders(len(chunk))+`) ORDER BY v.entry_id, v.ordinal, v.pk`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v Vote
			if err := rows.Scan(&v.EntryID, &v.Revision, &v.Principal, &v.PrincipalOwner, &v.PrincipalKind, &v.Verdict,
				&v.Target, &v.Note, &v.EvidenceHandle, &v.ContentDigest, &v.SessionID, &v.Ordinal, &v.Epoch, &v.At); err != nil {
				rows.Close()
				return nil, err
			}
			out[v.EntryID] = append(out[v.EntryID], v)
		}
		err = rows.Err()
		if cerr := rows.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// VerificationMode labels how a promoted entry's served revision earned promotion, from
// its tally. It is the one trust-label rule: the importer and the governance kernel
// both call it, and the peer_verified invariant (peerShortfall) enforces its first case.
// peer_verified needs `required` peer approvals, or the workspace threshold when a
// single assertion promoted the entry (required <= 1). Otherwise the open-mode
// identity's approval makes it self_asserted_open_mode, and anything else is one
// authenticated principal's word (single_agent).
func VerificationMode(t Tally, required, threshold int) string {
	need := required
	if need <= 1 {
		need = max(threshold, 1)
	}
	switch {
	case t.PeerApprovals >= need:
		return ModePeerVerified
	case t.OpenModeApproved:
		return ModeSelfAssertedOpenMode
	default:
		return ModeSingleAgent
	}
}

// Tally counts the verdicts on a revision (0 = served).
func (r *reader) Tally(ctx context.Context, entryID string, revision int64) (Tally, error) {
	rev, err := r.resolveRevision(ctx, entryID, revision)
	if err != nil {
		return Tally{}, err
	}
	tl := Tally{EntryID: entryID, Revision: rev}
	var authorApproved, openApproved int
	// A vote is an author's when it comes from the entry's author or from the author of
	// the revision it was cast on: an editor's approval of their own edit is not a peer's.
	err = r.queryRow(ctx, `SELECT
		coalesce(sum(v.verdict = 'approve'), 0),
		coalesce(sum(v.verdict = 'reject'), 0),
		coalesce(sum(v.verdict = 'approve' AND v.principal_key NOT IN (e.source_key, coalesce(rv.author_key, e.source_key), ?1)), 0),
		coalesce(sum(v.verdict = 'reject' AND v.principal_key NOT IN (e.source_key, coalesce(rv.author_key, e.source_key), ?1)), 0),
		coalesce(max(v.verdict = 'approve' AND v.principal_key IN (e.source_key, coalesce(rv.author_key, e.source_key))), 0),
		coalesce(max(v.verdict = 'approve' AND v.principal_key = ?1), 0),
		coalesce(sum(v.verdict = 'retract'), 0),
		coalesce(sum(v.verdict = 'duplicate_of'), 0)
		FROM entries e
		LEFT JOIN revisions rv ON rv.entry_id = e.id AND rv.revision = ?2
		LEFT JOIN votes v ON v.entry_id = e.id AND v.revision = ?2 AND v.epoch = e.vote_epoch
		WHERE e.id = ?3`, OpenModeIdentity, rev, entryID).Scan(
		&tl.Approvals, &tl.Rejections, &tl.PeerApprovals, &tl.PeerRejections, &authorApproved, &openApproved,
		&tl.Retractions, &tl.Duplicates)
	if err != nil {
		return Tally{}, fmt.Errorf("tally %s: %w", entryID, err)
	}
	tl.AuthorApproved, tl.OpenModeApproved = authorApproved == 1, openApproved == 1
	return tl, nil
}

// RecordVote stores actor's verdict on a live revision and appends a vote event.
func (t *txn) RecordVote(ctx context.Context, in VoteInput, actor Actor) (Vote, error) {
	if err := actor.validate(); err != nil {
		return Vote{}, err
	}
	if !validVerdicts[in.Verdict] {
		return Vote{}, fmt.Errorf("%w: verdict %q", ErrInvalid, in.Verdict)
	}
	if len(in.Note) > maxNoteLen {
		return Vote{}, fmt.Errorf("%w: note longer than %d bytes", ErrInvalid, maxNoteLen)
	}
	cur, err := t.GetEntry(ctx, in.EntryID)
	if err != nil {
		return Vote{}, err
	}
	if cur.Lifecycle != LifecycleActive {
		return Vote{}, fmt.Errorf("%w: entry %s is %s and takes no votes", ErrInvalid, in.EntryID, cur.Lifecycle)
	}
	rev := in.Revision
	if rev == 0 {
		rev = cur.Revision
	}
	rv, err := t.GetRevision(ctx, in.EntryID, rev)
	if err != nil {
		return Vote{}, err
	}
	if !(rev == cur.Revision || rv.State == RevisionPending) {
		return Vote{}, fmt.Errorf("%w: revision %d of %s is neither served nor pending", ErrInvalid, rev, in.EntryID)
	}
	if in.Verdict == VerdictDuplicateOf {
		if in.Target == "" || in.Target == in.EntryID {
			return Vote{}, fmt.Errorf("%w: duplicate_of needs another entry as target", ErrInvalid)
		}
		target, err := t.GetEntry(ctx, in.Target)
		if err != nil {
			return Vote{}, err
		}
		if target.WorkspaceID != cur.WorkspaceID {
			return Vote{}, fmt.Errorf("%w: duplicate_of target %s is in another workspace", ErrInvalid, in.Target)
		}
	} else if in.Target != "" {
		return Vote{}, fmt.Errorf("%w: only duplicate_of takes a target", ErrInvalid)
	}
	at := t.nowText()
	principal := strings.TrimSpace(actor.Principal)
	key := principalKey(principal)
	var ordinal int64
	err = t.queryRow(ctx, "SELECT ordinal FROM votes WHERE entry_id = ? AND revision = ? AND principal_key = ?",
		in.EntryID, rev, key).Scan(&ordinal)
	switch {
	case err == nil:
		// replacing: the principal keeps its place in the list
	case isNotFound(err):
		if err := t.queryRow(ctx, "SELECT coalesce(max(ordinal), -1) + 1 FROM votes WHERE entry_id = ? AND revision = ?",
			in.EntryID, rev).Scan(&ordinal); err != nil {
			return Vote{}, err
		}
	default:
		return Vote{}, err
	}
	if _, err := t.exec(ctx, `INSERT INTO votes (entry_id, revision, principal, principal_key, principal_owner,
		principal_kind, verdict, target, note, evidence_handle, content_digest, session_id, ordinal, epoch, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entry_id, revision, principal_key) DO UPDATE SET principal = excluded.principal,
		principal_owner = excluded.principal_owner, principal_kind = excluded.principal_kind,
		verdict = excluded.verdict, target = excluded.target, note = excluded.note,
		evidence_handle = excluded.evidence_handle, content_digest = excluded.content_digest,
		session_id = excluded.session_id, epoch = excluded.epoch, at = excluded.at`,
		in.EntryID, rev, principal, key, actor.Owner, actor.Kind, in.Verdict, in.Target, in.Note, in.EvidenceHandle,
		rv.ContentDigest, actor.SessionID, ordinal, cur.VoteEpoch, at); err != nil {
		return Vote{}, err
	}
	evType := EventVote
	if in.Verdict == VerdictReject {
		evType = EventReject
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: cur.WorkspaceID, EntryID: in.EntryID, Type: evType, Revision: rev, NewDigest: rv.ContentDigest,
		Note: in.Note, Data: map[string]any{"verdict": in.Verdict, "target": in.Target, "evidence_handle": in.EvidenceHandle},
	}); err != nil {
		return Vote{}, err
	}
	t.touch(in.EntryID)
	return Vote{
		EntryID: in.EntryID, Revision: rev, Principal: principal, PrincipalOwner: actor.Owner, PrincipalKind: actor.Kind,
		Verdict: in.Verdict, Target: in.Target, Note: in.Note, EvidenceHandle: in.EvidenceHandle,
		ContentDigest: rv.ContentDigest, SessionID: actor.SessionID, Ordinal: ordinal, Epoch: cur.VoteEpoch, At: at,
	}, nil
}

package govstore

import (
	"context"
	"fmt"
	"strings"
)

// Outcomes a principal reports after acting on a memory (PAR-GOV-16).
const (
	OutcomeHelpful    = "helpful"
	OutcomeMisleading = "misleading"
	OutcomeStaleHarm  = "stale_harm"
)

var (
	validOutcomes = set(OutcomeHelpful, OutcomeMisleading, OutcomeStaleHarm)
	// validSurfaces are where a memory can be delivered to an agent.
	validSurfaces = set("recall", "ground", "hook", "instructions", "explain", "impact", "export")
)

// Outcome is a principal's latest outcome report on an entry.
type Outcome struct {
	EntryID   string `json:"entry_id"`
	Revision  int64  `json:"revision"`
	Principal string `json:"principal"`
	Outcome   string `json:"outcome"`
	Note      string `json:"note,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	At        string `json:"at"`
}

// OutcomeInput reports an outcome on the entry's served revision.
type OutcomeInput struct {
	EntryID string
	Outcome string
	Note    string
}

// OutcomeSummary counts the latest outcome of each principal.
type OutcomeSummary struct {
	EntryID    string `json:"entry_id"`
	Helpful    int    `json:"helpful"`
	Misleading int    `json:"misleading"`
	StaleHarm  int    `json:"stale_harm"`
}

// Delivery records that a revision of an entry was shown to a session.
type Delivery struct {
	Seq           int64  `json:"seq"`
	EntryID       string `json:"entry_id"`
	Revision      int64  `json:"revision"`
	ContentDigest string `json:"content_digest"`
	WorkspaceID   string `json:"workspace_id"`
	SessionID     string `json:"session_id,omitempty"`
	Principal     string `json:"principal,omitempty"`
	Surface       string `json:"surface"`
	At            string `json:"at"`
}

// DeliveryInput names one delivered entry; the store binds it to the served revision
// and digest at write time.
type DeliveryInput struct {
	EntryID string
	Surface string
}

// DeliveryFilter pages deliveries by session or by entry.
type DeliveryFilter struct {
	SessionID string
	EntryID   string
	AfterSeq  int64
	Limit     int
}

// OutcomeReader reads outcomes and deliveries.
type OutcomeReader interface {
	ListOutcomes(ctx context.Context, entryID string) ([]Outcome, error)
	SummarizeOutcomes(ctx context.Context, entryID string) (OutcomeSummary, error)
	ListDeliveries(ctx context.Context, f DeliveryFilter) ([]Delivery, error)
}

// OutcomeWriter records outcomes and deliveries.
type OutcomeWriter interface {
	RecordOutcome(ctx context.Context, in OutcomeInput, actor Actor) (Outcome, error)
	RecordDeliveries(ctx context.Context, items []DeliveryInput, actor Actor) (int, error)
}

// ListOutcomes returns each principal's latest outcome on an entry.
func (r *reader) ListOutcomes(ctx context.Context, entryID string) ([]Outcome, error) {
	rows, err := r.query(ctx, `SELECT entry_id, revision, principal, outcome, note, session_id, at FROM outcomes
		WHERE entry_id = ? ORDER BY pk LIMIT ?`, entryID, maxListLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Outcome
	for rows.Next() {
		var o Outcome
		if err := rows.Scan(&o.EntryID, &o.Revision, &o.Principal, &o.Outcome, &o.Note, &o.SessionID, &o.At); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SummarizeOutcomes counts outcomes on an entry.
func (r *reader) SummarizeOutcomes(ctx context.Context, entryID string) (OutcomeSummary, error) {
	s := OutcomeSummary{EntryID: entryID}
	err := r.queryRow(ctx, `SELECT coalesce(sum(outcome = 'helpful'), 0), coalesce(sum(outcome = 'misleading'), 0),
		coalesce(sum(outcome = 'stale_harm'), 0) FROM outcomes WHERE entry_id = ?`, entryID).Scan(&s.Helpful, &s.Misleading, &s.StaleHarm)
	return s, err
}

// ListDeliveries pages deliveries by session or entry, in order.
func (r *reader) ListDeliveries(ctx context.Context, f DeliveryFilter) ([]Delivery, error) {
	var where []string
	var args []any
	switch {
	case f.SessionID != "":
		where = append(where, "session_id = ?")
		args = append(args, f.SessionID)
	case f.EntryID != "":
		where = append(where, "entry_id = ?")
		args = append(args, f.EntryID)
	default:
		return nil, fmt.Errorf("%w: ListDeliveries needs a session or an entry", ErrInvalid)
	}
	where = append(where, "seq > ?")
	args = append(args, f.AfterSeq, clampLimit(f.Limit))
	rows, err := r.query(ctx, `SELECT seq, entry_id, revision, content_digest, workspace_id, session_id, principal, surface, at
		FROM deliveries WHERE `+strings.Join(where, " AND ")+` ORDER BY seq LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.Seq, &d.EntryID, &d.Revision, &d.ContentDigest, &d.WorkspaceID, &d.SessionID, &d.Principal,
			&d.Surface, &d.At); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecordOutcome stores actor's latest outcome on an entry and appends a feedback event.
func (t *txn) RecordOutcome(ctx context.Context, in OutcomeInput, actor Actor) (Outcome, error) {
	if err := actor.validate(); err != nil {
		return Outcome{}, err
	}
	if !validOutcomes[in.Outcome] {
		return Outcome{}, fmt.Errorf("%w: outcome %q", ErrInvalid, in.Outcome)
	}
	if len(in.Note) > maxNoteLen {
		return Outcome{}, fmt.Errorf("%w: note longer than %d bytes", ErrInvalid, maxNoteLen)
	}
	e, err := t.liveEntry(ctx, in.EntryID)
	if err != nil {
		return Outcome{}, err
	}
	principal := strings.TrimSpace(actor.Principal)
	now := t.nowText()
	if _, err := t.exec(ctx, `INSERT INTO outcomes (entry_id, revision, principal, principal_key, outcome, note, session_id, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entry_id, principal_key) DO UPDATE SET revision = excluded.revision, principal = excluded.principal,
		outcome = excluded.outcome, note = excluded.note, session_id = excluded.session_id, at = excluded.at`,
		in.EntryID, e.Revision, principal, principalKey(principal), in.Outcome, in.Note, actor.SessionID, now); err != nil {
		return Outcome{}, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: e.WorkspaceID, EntryID: in.EntryID, Type: EventFeedback, Revision: e.Revision,
		NewDigest: e.ContentDigest, Note: in.Note, Data: map[string]any{"outcome": in.Outcome},
	}); err != nil {
		return Outcome{}, err
	}
	return Outcome{EntryID: in.EntryID, Revision: e.Revision, Principal: principal, Outcome: in.Outcome, Note: in.Note,
		SessionID: actor.SessionID, At: now}, nil
}

// RecordDeliveries logs that entries were shown to actor's session. Each row binds the
// served revision and digest at this moment, so stale-harm reports can be traced to
// the exact text an agent saw.
func (t *txn) RecordDeliveries(ctx context.Context, items []DeliveryInput, actor Actor) (int, error) {
	if err := actor.validate(); err != nil {
		return 0, err
	}
	now := t.nowText()
	n := 0
	for _, it := range items {
		if !validSurfaces[it.Surface] {
			return n, fmt.Errorf("%w: delivery surface %q", ErrInvalid, it.Surface)
		}
		res, err := t.exec(ctx, `INSERT INTO deliveries (entry_id, revision, content_digest, workspace_id, session_id,
			principal, surface, at) SELECT id, revision, content_digest, workspace_id, ?, ?, ?, ? FROM entries WHERE id = ?`,
			actor.SessionID, strings.TrimSpace(actor.Principal), it.Surface, now, it.EntryID)
		if err != nil {
			return n, err
		}
		if rowsAffected(res) == 0 {
			return n, fmt.Errorf("entry %s: %w", it.EntryID, ErrNotFound)
		}
		n++
	}
	return n, nil
}

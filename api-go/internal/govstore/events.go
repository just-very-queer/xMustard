package govstore

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

// Event types in the append-only history (PAR-PROV-01 plus the lifecycle and sharing
// transitions). Adding a type needs code here, never a migration.
const (
	EventPropose           = "propose"
	EventImport            = "import"
	EventVote              = "vote"
	EventReject            = "reject"
	EventEdit              = "edit"
	EventRevisionAccepted  = "revision_accepted"
	EventRevisionRejected  = "revision_rejected"
	EventRevisionWithdrawn = "revision_withdrawn"
	EventPromote           = "promote"
	EventDemote            = "demote"
	EventStatus            = "status"
	EventSupersede         = "supersede"
	EventMerge             = "merge"
	EventRetract           = "retract"
	EventArchive           = "archive"
	EventRestore           = "restore"
	EventPurge             = "purge"
	EventExpiry            = "expiry"
	EventTierChange        = "tier_change"
	EventClassify          = "classify"
	EventAnchors           = "anchors"
	EventBaseline          = "baseline"
	EventStaleObserved     = "stale_observed"
	EventDriftCleared      = "drift_cleared"
	EventClaim             = "claim"
	EventRelation          = "relation"
	EventFeedback          = "feedback"
	EventCollection        = "collection"
	EventGrant             = "grant"
	EventRevoke            = "revoke"
	EventApplicability     = "applicability"
	EventNote              = "note"
	// EventGate records a change of an entry's required verification count.
	EventGate = "gate"
	// EventIndexBaseline records a workspace's change-tracking index baseline being
	// built or replaced (PAR-FRESH-06): automatically or by an explicit rebaseline, the
	// reason, and the HEAD it was taken at. It names no entry.
	EventIndexBaseline = "index_baseline"
)

var validEventTypes = set(EventPropose, EventImport, EventVote, EventReject, EventEdit, EventRevisionAccepted,
	EventRevisionRejected, EventRevisionWithdrawn, EventPromote, EventDemote, EventStatus, EventSupersede,
	EventMerge, EventRetract, EventArchive, EventRestore, EventPurge, EventExpiry, EventTierChange, EventClassify,
	EventAnchors, EventBaseline, EventStaleObserved, EventDriftCleared, EventClaim, EventRelation, EventFeedback,
	EventCollection, EventGrant, EventRevoke, EventApplicability, EventNote, EventGate, EventIndexBaseline)

// Event is one immutable history record.
type Event struct {
	Seq         int64           `json:"seq"`
	WorkspaceID string          `json:"workspace_id"`
	EntryID     string          `json:"entry_id,omitempty"`
	Type        string          `json:"type"`
	Principal   string          `json:"principal"`
	SessionID   string          `json:"session_id,omitempty"`
	AgentID     string          `json:"agent_id,omitempty"`
	HeadSHA     string          `json:"head_sha,omitempty"`
	Revision    int64           `json:"revision,omitempty"`
	OldDigest   string          `json:"old_digest,omitempty"`
	NewDigest   string          `json:"new_digest,omitempty"`
	Note        string          `json:"note,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Redacted    bool            `json:"redacted,omitempty"`
	At          string          `json:"at"`
}

// EventFilter pages through the history by sequence number (a cursor feed).
type EventFilter struct {
	WorkspaceID string
	EntryID     string
	Types       []string
	// Revision, when set, keeps only the events of that revision.
	Revision int64
	AfterSeq int64
	Limit    int
}

// EventInput is a caller-authored event, for transitions the typed writers do not
// cover yet.
type EventInput struct {
	WorkspaceID string
	EntryID     string
	Type        string
	Revision    int64
	Note        string
	Data        any
}

// EventReader reads the history.
type EventReader interface {
	ListEvents(ctx context.Context, f EventFilter) ([]Event, error)
}

// EventWriter appends to the history. There is no update or delete.
type EventWriter interface {
	AppendEvent(ctx context.Context, in EventInput, actor Actor) (Event, error)
}

type eventRow struct {
	WorkspaceID string
	EntryID     string
	Type        string
	Revision    int64
	OldDigest   string
	NewDigest   string
	Note        string
	Data        any
}

func (t *txn) appendEvent(ctx context.Context, actor Actor, ev eventRow) error {
	_, err := t.insertEvent(ctx, actor, ev)
	return err
}

func (t *txn) insertEvent(ctx context.Context, actor Actor, ev eventRow) (int64, error) {
	if !validEventTypes[ev.Type] {
		return 0, fmt.Errorf("%w: event type %q", ErrInvalid, ev.Type)
	}
	var data any
	if p := actor.provenance(); p != nil {
		ev.Data = withProvenance(ev.Data, p)
	}
	if ev.Data != nil {
		s, err := encodeJSON(ev.Data)
		if err != nil {
			return 0, err
		}
		data = s
	}
	note := ev.Note
	if note == "" {
		note = actor.Note
	}
	var revision any
	if ev.Revision > 0 {
		revision = ev.Revision
	}
	res, err := t.exec(ctx, `INSERT INTO events (workspace_id, entry_id, type, principal, session_id, agent_id, head_sha,
		revision, old_digest, new_digest, note, data, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.WorkspaceID, nullText(ev.EntryID), ev.Type, strings.TrimSpace(actor.Principal), actor.SessionID,
		actor.AgentID, actor.HeadSHA, revision, ev.OldDigest, ev.NewDigest, nullText(note), data, t.nowText())
	if err != nil {
		return 0, fmt.Errorf("append %s event: %w", ev.Type, err)
	}
	return res.LastInsertId()
}

// withProvenance adds the write's provenance to an event's data. Data of another shape
// than a JSON object is nested under "data" so the provenance is never dropped.
func withProvenance(data any, p map[string]any) map[string]any {
	switch d := data.(type) {
	case nil:
		return map[string]any{"provenance": p}
	case map[string]any:
		out := make(map[string]any, len(d)+1)
		maps.Copy(out, d)
		out["provenance"] = p
		return out
	}
	return map[string]any{"data": data, "provenance": p}
}

// AppendEvent appends a caller-authored event. An entry event must name an existing
// entry that is not purged, and its workspace comes from that entry.
func (t *txn) AppendEvent(ctx context.Context, in EventInput, actor Actor) (Event, error) {
	if err := actor.validate(); err != nil {
		return Event{}, err
	}
	ws := in.WorkspaceID
	if in.EntryID != "" {
		e, err := t.liveEntry(ctx, in.EntryID)
		if err != nil {
			return Event{}, err
		}
		if ws != "" && ws != e.WorkspaceID {
			return Event{}, fmt.Errorf("%w: entry %s is not in workspace %s", ErrInvalid, in.EntryID, ws)
		}
		ws = e.WorkspaceID
	}
	if err := validID("workspace", ws); err != nil {
		return Event{}, err
	}
	seq, err := t.insertEvent(ctx, actor, eventRow{
		WorkspaceID: ws, EntryID: in.EntryID, Type: in.Type, Revision: in.Revision, Note: in.Note, Data: in.Data,
	})
	if err != nil {
		return Event{}, err
	}
	evs, err := t.ListEvents(ctx, EventFilter{WorkspaceID: ws, AfterSeq: seq - 1, Limit: 1})
	if err != nil {
		return Event{}, err
	}
	if len(evs) == 0 {
		return Event{}, ErrNotFound
	}
	return evs[0], nil
}

// ListEvents returns events in sequence order after f.AfterSeq. It needs a workspace
// or an entry, so no call reads the whole log.
func (r *reader) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	var where []string
	var args []any
	switch {
	case f.EntryID != "":
		where = append(where, "entry_id = ?")
		args = append(args, f.EntryID)
		if f.WorkspaceID != "" {
			where = append(where, "workspace_id = ?")
			args = append(args, f.WorkspaceID)
		}
	case f.WorkspaceID != "":
		where = append(where, "workspace_id = ?")
		args = append(args, f.WorkspaceID)
	default:
		return nil, fmt.Errorf("%w: ListEvents needs a workspace or an entry", ErrInvalid)
	}
	where = append(where, "seq > ?")
	args = append(args, f.AfterSeq)
	if f.Revision > 0 {
		where = append(where, "revision = ?")
		args = append(args, f.Revision)
	}
	if len(f.Types) > 0 {
		where = append(where, "type IN ("+placeholders(len(f.Types))+")")
		for _, ty := range f.Types {
			args = append(args, ty)
		}
	}
	args = append(args, clampLimit(f.Limit))
	rows, err := r.query(ctx, `SELECT seq, workspace_id, coalesce(entry_id, ''), type, principal, session_id, agent_id,
		head_sha, coalesce(revision, 0), old_digest, new_digest, coalesce(note, ''), coalesce(data, ''), redacted, at
		FROM events WHERE `+strings.Join(where, " AND ")+` ORDER BY seq LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		var data string
		var redacted int
		if err := rows.Scan(&ev.Seq, &ev.WorkspaceID, &ev.EntryID, &ev.Type, &ev.Principal, &ev.SessionID, &ev.AgentID,
			&ev.HeadSHA, &ev.Revision, &ev.OldDigest, &ev.NewDigest, &ev.Note, &data, &redacted, &ev.At); err != nil {
			return nil, err
		}
		if data != "" {
			ev.Data = json.RawMessage(data)
		}
		ev.Redacted = redacted == 1
		out = append(out, ev)
	}
	return out, rows.Err()
}

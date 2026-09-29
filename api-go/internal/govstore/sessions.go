package govstore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Session statuses.
const (
	SessionActive      = "active"
	SessionEnded       = "ended"
	SessionInterrupted = "interrupted"
)

// Session event record classes: raw observations, derived summaries and verified
// records stay distinct (PAR-PROV-06).
const (
	RecordRaw      = "raw"
	RecordDerived  = "derived"
	RecordVerified = "verified"
)

var validRecordClasses = set(RecordRaw, RecordDerived, RecordVerified)

// Session is one agent session in a workspace. Identity fields are audit metadata,
// never authorization (PAR-PROV-07).
type Session struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	Client         string `json:"client,omitempty"`
	Principal      string `json:"principal,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	ExecutionKey   string `json:"execution_key,omitempty"`
	ParentID       string `json:"parent_id,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Worktree       string `json:"worktree,omitempty"`
	HeadSHA        string `json:"head_sha,omitempty"`
	Status         string `json:"status"`
	Summary        string `json:"summary,omitempty"`
	StartedAt      string `json:"started_at"`
	LastSeenAt     string `json:"last_seen_at"`
	EndedAt        string `json:"ended_at,omitempty"`
	RetentionClass string `json:"retention_class"`
	Cursor         int64  `json:"cursor"`
}

// SessionInput starts or touches a session. The principal comes from the writing
// Actor; a session can never change principal or workspace once started.
type SessionInput struct {
	ID             string
	WorkspaceID    string
	Client         string
	AgentID        string
	ExecutionKey   string
	ParentID       string
	ThreadID       string
	Branch         string
	Worktree       string
	HeadSHA        string
	RetentionClass string
	// At (RFC 3339) dates an imported session: the time it started, or a later time
	// its last_seen_at moves forward to. Empty is now.
	At string
	// Summary is set only on the insert of an imported session.
	Summary string
}

// SessionFilter pages a workspace's sessions, most recently seen first.
type SessionFilter struct {
	WorkspaceID string
	ThreadID    string
	Status      string
	// ExcludeRetention leaves out the sessions of one retention class, in SQL, so a
	// LIMIT counts only the rest (imported transcripts must not crowd out live sessions).
	ExcludeRetention string
	Limit            int
}

// SessionEvent is one immutable ledger row.
type SessionEvent struct {
	Seq            int64           `json:"seq"`
	SessionID      string          `json:"session_id"`
	WorkspaceID    string          `json:"workspace_id"`
	Kind           string          `json:"kind"`
	RecordClass    string          `json:"record_class"`
	Tool           string          `json:"tool,omitempty"`
	ArgsDigest     string          `json:"args_digest,omitempty"`
	Handle         string          `json:"handle,omitempty"`
	Status         string          `json:"status,omitempty"`
	Path           string          `json:"path,omitempty"`
	EntryID        string          `json:"entry_id,omitempty"`
	Role           string          `json:"role,omitempty"`
	Body           string          `json:"body,omitempty"`
	BodyDigest     string          `json:"body_digest,omitempty"`
	Data           json.RawMessage `json:"data,omitempty"`
	At             string          `json:"at"`
	RetentionClass string          `json:"retention_class"`
}

// SessionEventInput appends to a session's ledger. Body is transcript or prompt
// text; it is indexed for search.
type SessionEventInput struct {
	SessionID      string
	Kind           string
	RecordClass    string // default raw
	Tool           string
	ArgsDigest     string
	Handle         string
	Status         string
	Path           string
	EntryID        string
	Role           string
	Body           string
	Data           any
	RetentionClass string // default: the session's class
	// At (RFC 3339) is when the event happened, for an imported one. Empty is now.
	At string
}

// SessionEventFilter pages one session's ledger by sequence.
type SessionEventFilter struct {
	SessionID string
	AfterSeq  int64
	Kinds     []string
	Limit     int
}

// EventsAround reads the ledger rows around one event of a session (a hit's
// expansion): up to Before rows before Seq, the row itself and up to After rows after.
type EventsAround struct {
	SessionID     string
	Seq           int64
	Before, After int
}

// RecentEventFilter reads the newest ledger rows of some kinds, newest first: of one
// session, or of a workspace's most recently seen sessions (WS-33: the latest snapshot,
// the latest handoff capsules).
type RecentEventFilter struct {
	WorkspaceID string
	// SessionID reads one session; "" reads the workspace's Sessions most recently seen
	// sessions (default 16), leaving out ExcludeSession.
	SessionID      string
	ExcludeSession string
	Sessions       int
	// ExcludeRetention leaves out the sessions of one retention class before the
	// Sessions window is cut (a workspace-wide read only).
	ExcludeRetention string
	// Kinds selects the kinds; nil selects every kind.
	Kinds []string
	Limit int
}

// SessionReader reads sessions and their ledgers.
type SessionReader interface {
	GetSession(ctx context.Context, id string) (Session, error)
	ListSessions(ctx context.Context, f SessionFilter) ([]Session, error)
	ListSessionEvents(ctx context.Context, f SessionEventFilter) ([]SessionEvent, error)
	RecentSessionEvents(ctx context.Context, f RecentEventFilter) ([]SessionEvent, error)
	SessionEventsAround(ctx context.Context, f EventsAround) ([]SessionEvent, error)
}

// SessionWriter writes sessions and ledger rows. Ledger rows are immutable: they are
// appended, and deleted by retention or by a bound (TrimSessionEvents), never rewritten.
type SessionWriter interface {
	UpsertSession(ctx context.Context, in SessionInput, actor Actor) (Session, error)
	EndSession(ctx context.Context, id, status, summary string, actor Actor) (Session, error)
	// EndSessionAt ends a session at a given time (RFC 3339; an imported session ends
	// when its last message was written) without moving last_seen_at past it.
	EndSessionAt(ctx context.Context, id, status, summary, at string, actor Actor) (Session, error)
	AppendSessionEvent(ctx context.Context, in SessionEventInput, actor Actor) (SessionEvent, error)
	TrimSessionEvents(ctx context.Context, sessionID string, kinds []string, keep int) (int64, error)
}

const sessionCols = `id, workspace_id, client, principal, agent_id, execution_key, parent_id, thread_id, branch,
	worktree, head_sha, status, summary, started_at, last_seen_at, coalesce(ended_at, ''), retention_class, pk`

func scanSession(s scanner) (Session, error) {
	var ss Session
	err := s.Scan(&ss.ID, &ss.WorkspaceID, &ss.Client, &ss.Principal, &ss.AgentID, &ss.ExecutionKey, &ss.ParentID,
		&ss.ThreadID, &ss.Branch, &ss.Worktree, &ss.HeadSHA, &ss.Status, &ss.Summary, &ss.StartedAt, &ss.LastSeenAt,
		&ss.EndedAt, &ss.RetentionClass, &ss.Cursor)
	return ss, err
}

// GetSession returns one session.
func (r *reader) GetSession(ctx context.Context, id string) (Session, error) {
	s, err := scanSession(r.queryRow(ctx, "SELECT "+sessionCols+" FROM sessions WHERE id = ?", id))
	if err != nil {
		return Session{}, fmt.Errorf("session %s: %w", id, err)
	}
	return s, nil
}

// ListSessions returns a workspace's sessions, most recently seen first.
func (r *reader) ListSessions(ctx context.Context, f SessionFilter) ([]Session, error) {
	if err := validID("workspace", f.WorkspaceID); err != nil {
		return nil, err
	}
	where := []string{"workspace_id = ?"}
	args := []any{f.WorkspaceID}
	if f.ThreadID != "" {
		where = append(where, "thread_id = ?")
		args = append(args, f.ThreadID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.ExcludeRetention != "" {
		where = append(where, "retention_class <> ?")
		args = append(args, f.ExcludeRetention)
	}
	args = append(args, clampLimit(f.Limit))
	rows, err := r.query(ctx, "SELECT "+sessionCols+" FROM sessions WHERE "+strings.Join(where, " AND ")+
		" ORDER BY last_seen_at DESC, pk DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListSessionEvents pages one session's ledger in order.
func (r *reader) ListSessionEvents(ctx context.Context, f SessionEventFilter) ([]SessionEvent, error) {
	if f.SessionID == "" {
		return nil, fmt.Errorf("%w: session id is required", ErrInvalid)
	}
	where := []string{"session_id = ?", "seq > ?"}
	args := []any{f.SessionID, f.AfterSeq}
	if len(f.Kinds) > 0 {
		where = append(where, "kind IN ("+placeholders(len(f.Kinds))+")")
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	args = append(args, clampLimit(f.Limit))
	return r.sessionEvents(ctx, strings.Join(where, " AND ")+" ORDER BY seq LIMIT ?", args...)
}

const sessionEventCols = `seq, session_id, workspace_id, kind, record_class, tool, args_digest, handle, status,
	path, entry_id, role, coalesce(body, ''), body_digest, coalesce(data, ''), at, retention_class`

// sessionEvents reads the ledger rows a WHERE clause (with its ORDER and LIMIT) selects.
func (r *reader) sessionEvents(ctx context.Context, clause string, args ...any) ([]SessionEvent, error) {
	rows, err := r.query(ctx, "SELECT "+sessionEventCols+" FROM session_events WHERE "+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionEvent
	for rows.Next() {
		var ev SessionEvent
		var data string
		if err := rows.Scan(&ev.Seq, &ev.SessionID, &ev.WorkspaceID, &ev.Kind, &ev.RecordClass, &ev.Tool, &ev.ArgsDigest,
			&ev.Handle, &ev.Status, &ev.Path, &ev.EntryID, &ev.Role, &ev.Body, &ev.BodyDigest, &data, &ev.At,
			&ev.RetentionClass); err != nil {
			return nil, err
		}
		if data != "" {
			ev.Data = json.RawMessage(data)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// SessionEventsAround returns the rows around one event in sequence order.
func (r *reader) SessionEventsAround(ctx context.Context, f EventsAround) ([]SessionEvent, error) {
	if f.SessionID == "" {
		return nil, fmt.Errorf("%w: session id is required", ErrInvalid)
	}
	before, err := r.sessionEvents(ctx, "session_id = ? AND seq < ? ORDER BY seq DESC LIMIT ?", f.SessionID, f.Seq, clampLimit(f.Before))
	if err != nil {
		return nil, err
	}
	after, err := r.sessionEvents(ctx, "session_id = ? AND seq >= ? ORDER BY seq LIMIT ?", f.SessionID, f.Seq, clampLimit(f.After+1))
	if err != nil {
		return nil, err
	}
	slices.Reverse(before)
	return append(before, after...), nil
}

// defaultRecentSessions is how many recently seen sessions a workspace-wide
// RecentSessionEvents reads.
const defaultRecentSessions = 16

// RecentSessionEvents returns the newest ledger rows of the selected kinds, newest
// first. Both forms read through the (session_id, seq) index: a workspace-wide read
// takes the workspace's most recently seen sessions first (sessions_workspace).
func (r *reader) RecentSessionEvents(ctx context.Context, f RecentEventFilter) ([]SessionEvent, error) {
	if err := validID("workspace", f.WorkspaceID); err != nil {
		return nil, err
	}
	where, args := "", []any{}
	if f.SessionID != "" {
		where, args = "session_id = ? AND workspace_id = ?", append(args, f.SessionID, f.WorkspaceID)
	} else {
		n := f.Sessions
		if n <= 0 {
			n = defaultRecentSessions
		}
		where = `session_id IN (SELECT id FROM sessions WHERE workspace_id = ? AND id <> ? AND retention_class <> ?
			ORDER BY last_seen_at DESC, pk DESC LIMIT ?)`
		args = append(args, f.WorkspaceID, f.ExcludeSession, f.ExcludeRetention, clampLimit(n))
	}
	if len(f.Kinds) > 0 {
		where += " AND kind IN (" + placeholders(len(f.Kinds)) + ")"
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	args = append(args, clampLimit(f.Limit))
	return r.sessionEvents(ctx, where+" ORDER BY seq DESC LIMIT ?", args...)
}

// UpsertSession starts a session, or refreshes last_seen_at and the mutable context
// (head, branch, worktree) of an existing one.
func (t *txn) UpsertSession(ctx context.Context, in SessionInput, actor Actor) (Session, error) {
	if err := actor.validate(); err != nil {
		return Session{}, err
	}
	if err := validID("session", in.ID); err != nil {
		return Session{}, err
	}
	if err := validID("workspace", in.WorkspaceID); err != nil {
		return Session{}, err
	}
	for _, v := range []struct{ kind, val string }{
		{"client", in.Client}, {"agent", in.AgentID}, {"execution key", in.ExecutionKey}, {"parent", in.ParentID},
		{"thread", in.ThreadID}, {"branch", in.Branch}, {"worktree", in.Worktree}, {"head", in.HeadSHA},
		{"retention class", in.RetentionClass},
	} {
		if err := validName(v.kind, v.val); err != nil {
			return Session{}, err
		}
	}
	principal := strings.TrimSpace(actor.Principal)
	now, err := t.eventTime(in.At)
	if err != nil {
		return Session{}, err
	}
	existing, err := t.GetSession(ctx, in.ID)
	switch {
	case err == nil:
		if existing.WorkspaceID != in.WorkspaceID || existing.Principal != principal {
			return Session{}, fmt.Errorf("%w: session %s belongs to another workspace or principal", ErrConflict, in.ID)
		}
		if _, err := t.exec(ctx, `UPDATE sessions SET last_seen_at = max(last_seen_at, ?),
			head_sha = CASE WHEN ? <> '' THEN ? ELSE head_sha END,
			branch = CASE WHEN ? <> '' THEN ? ELSE branch END,
			worktree = CASE WHEN ? <> '' THEN ? ELSE worktree END WHERE id = ?`,
			now, in.HeadSHA, in.HeadSHA, in.Branch, in.Branch, in.Worktree, in.Worktree, in.ID); err != nil {
			return Session{}, err
		}
	case isNotFound(err):
		retention := in.RetentionClass
		if retention == "" {
			retention = "session"
		}
		if _, err := t.exec(ctx, `INSERT INTO sessions (id, workspace_id, client, principal, principal_key, agent_id,
			execution_key, parent_id, thread_id, branch, worktree, head_sha, started_at, last_seen_at, retention_class, summary)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.ID, in.WorkspaceID, in.Client, principal, principalKey(principal), in.AgentID, in.ExecutionKey,
			in.ParentID, in.ThreadID, in.Branch, in.Worktree, in.HeadSHA, now, now, retention, in.Summary); err != nil {
			return Session{}, err
		}
	default:
		return Session{}, err
	}
	return t.GetSession(ctx, in.ID)
}

// EndSession closes a session as ended or interrupted. Only the session's own principal
// may end it.
func (t *txn) EndSession(ctx context.Context, id, status, summary string, actor Actor) (Session, error) {
	return t.EndSessionAt(ctx, id, status, summary, "", actor)
}

// EndSessionAt is EndSession with the end time given (empty is now).
func (t *txn) EndSessionAt(ctx context.Context, id, status, summary, at string, actor Actor) (Session, error) {
	if err := actor.validate(); err != nil {
		return Session{}, err
	}
	if status != SessionEnded && status != SessionInterrupted {
		return Session{}, fmt.Errorf("%w: session end status %q", ErrInvalid, status)
	}
	if len(summary) > maxNoteLen {
		return Session{}, fmt.Errorf("%w: summary longer than %d bytes", ErrInvalid, maxNoteLen)
	}
	s, err := t.GetSession(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if s.Principal != strings.TrimSpace(actor.Principal) {
		return Session{}, fmt.Errorf("%w: session %s belongs to another principal", ErrConflict, id)
	}
	now, err := t.eventTime(at)
	if err != nil {
		return Session{}, err
	}
	res, err := t.exec(ctx, `UPDATE sessions SET status = ?, summary = ?, ended_at = ?, last_seen_at = max(last_seen_at, ?)
		WHERE id = ? AND status = 'active'`, status, summary, now, now, id)
	if err != nil {
		return Session{}, err
	}
	if rowsAffected(res) == 0 {
		return Session{}, fmt.Errorf("%w: session %s is not active", ErrInvalid, id)
	}
	return t.GetSession(ctx, id)
}

// AppendSessionEvent appends one immutable ledger row and touches the session.
func (t *txn) AppendSessionEvent(ctx context.Context, in SessionEventInput, actor Actor) (SessionEvent, error) {
	if err := actor.validate(); err != nil {
		return SessionEvent{}, err
	}
	s, err := t.GetSession(ctx, in.SessionID)
	if err != nil {
		return SessionEvent{}, err
	}
	if s.Principal != strings.TrimSpace(actor.Principal) {
		return SessionEvent{}, fmt.Errorf("%w: session %s belongs to another principal", ErrConflict, in.SessionID)
	}
	class := in.RecordClass
	if class == "" {
		class = RecordRaw
	}
	if !validRecordClasses[class] {
		return SessionEvent{}, fmt.Errorf("%w: record class %q", ErrInvalid, class)
	}
	if strings.TrimSpace(in.Kind) == "" || validName("kind", in.Kind) != nil {
		return SessionEvent{}, fmt.Errorf("%w: session event kind %q", ErrInvalid, in.Kind)
	}
	retention := in.RetentionClass
	if retention == "" {
		retention = s.RetentionClass
	}
	var data any
	if in.Data != nil {
		v, err := encodeJSON(in.Data)
		if err != nil {
			return SessionEvent{}, err
		}
		data = v
	}
	var body any
	digest := ""
	if in.Body != "" {
		body, digest = in.Body, Digest(in.Body)
	}
	now, err := t.eventTime(in.At)
	if err != nil {
		return SessionEvent{}, err
	}
	res, err := t.exec(ctx, `INSERT INTO session_events (session_id, workspace_id, kind, record_class, tool, args_digest,
		handle, status, path, entry_id, role, body, body_digest, data, at, retention_class)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.SessionID, s.WorkspaceID, in.Kind, class, in.Tool, in.ArgsDigest, in.Handle, in.Status, in.Path, in.EntryID,
		in.Role, body, digest, data, now, retention)
	if err != nil {
		return SessionEvent{}, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return SessionEvent{}, err
	}
	if _, err := t.exec(ctx, "UPDATE sessions SET last_seen_at = max(last_seen_at, ?) WHERE id = ?", now, in.SessionID); err != nil {
		return SessionEvent{}, err
	}
	evs, err := t.ListSessionEvents(ctx, SessionEventFilter{SessionID: in.SessionID, AfterSeq: seq - 1, Limit: 1})
	if err != nil {
		return SessionEvent{}, err
	}
	if len(evs) == 0 {
		return SessionEvent{}, ErrNotFound
	}
	return evs[0], nil
}

// TrimSessionEvents bounds one session's ledger: of the rows of the given kinds (nil:
// every kind) it keeps the newest keep and deletes the older ones, returning how many
// it deleted. Deleting is how retention and bounds end a row; nothing rewrites one.
func (t *txn) TrimSessionEvents(ctx context.Context, sessionID string, kinds []string, keep int) (int64, error) {
	if err := validID("session", sessionID); err != nil {
		return 0, err
	}
	if keep < 0 {
		return 0, fmt.Errorf("%w: keep %d", ErrInvalid, keep)
	}
	where, args := "session_id = ?", []any{sessionID}
	if len(kinds) > 0 {
		where += " AND kind IN (" + placeholders(len(kinds)) + ")"
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	args = append(args, keep)
	res, err := t.exec(ctx, `DELETE FROM session_events WHERE seq IN (SELECT seq FROM session_events WHERE `+where+`
		ORDER BY seq DESC LIMIT -1 OFFSET ?)`, args...)
	if err != nil {
		return 0, err
	}
	return rowsAffected(res), nil
}

// eventTime is the canonical time of a row: at when given (an imported row's own
// time), else now.
func (t *txn) eventTime(at string) (string, error) {
	if at == "" {
		return t.nowText(), nil
	}
	return inputTime("at", at)
}

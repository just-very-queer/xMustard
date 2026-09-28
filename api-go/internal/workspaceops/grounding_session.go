package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xmustard/api-go/internal/govstore"
)

// groundingSession is the session section of `ground`: the one-line summary an
// agent reads first and when the grounding was generated, and (WS-33) the state earlier
// sessions left: the handoff from the prior session (or, for session_id, the caller's
// own session to restore after compaction), what is still open across recent sessions,
// the latest handoff capsules with their stale paths, and the last sessions. The
// session members are omitted when the workspace has no session ledger.
type groundingSession struct {
	Summary     string `json:"summary"`
	GeneratedAt string `json:"generated_at"`
	// SessionHandoff is the prior session's state (PAR-HAR-05), or the caller's own
	// session's when session_id names one with a ledger (restore, PAR-HAR-04).
	SessionHandoff *SessionHandoff `json:"session_handoff,omitempty"`
	// Continuity is what recent sessions left open (PAR-PROV-06).
	Continuity *Continuity `json:"continuity,omitempty"`
	// Handoffs are the latest handoff capsules, newest first (PAR-PROV-08).
	Handoffs []HandoffView `json:"handoffs,omitempty"`
	// LastSessions are the most recently seen sessions (main threads), newest first.
	LastSessions []SessionBrief `json:"last_sessions,omitempty"`
}

func (s *groundingSession) stampGenerated() {
	s.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
}

// SessionHandoffLabel says what a handoff is: derived session state, never memory.
const SessionHandoffLabel = "derived from the session ledger: lightweight context, not verified memory"

// SessionHandoff is the state one session hands the next (in any client): its snapshot,
// the unexpired evidence handles of its latest tool calls, the proposals it left
// waiting for a distinct verifier, and the workspace's current drift.
type SessionHandoff struct {
	FromSession string `json:"from_session"`
	Client      string `json:"client,omitempty"`
	Principal   string `json:"principal,omitempty"`
	Status      string `json:"status"`
	LastSeenAt  string `json:"last_seen_at"`
	// Restore is true when the session is the caller's own (session_id): its snapshot
	// restores the state a compaction dropped.
	Restore          bool               `json:"restore,omitempty"`
	Snapshot         *SessionSnapshot   `json:"snapshot"`
	Handles          []HandoffHandle    `json:"handles,omitempty"`
	PendingProposals []SnapshotProposal `json:"pending_proposals,omitempty"`
	// Capsule is the session's latest handoff capsule, when it wrote or got one, with
	// the paths changed since.
	Capsule *HandoffRecord `json:"capsule,omitempty"`
	// DriftStale is the drift section's stale flag (null when drift is unknown).
	DriftStale *bool  `json:"drift_stale"`
	Label      string `json:"label"`
}

// HandoffHandle is an evidence handle a session's tool call left, unexpired.
type HandoffHandle struct {
	Handle    string `json:"handle"`
	Tool      string `json:"tool"`
	What      string `json:"what,omitempty"`
	Status    string `json:"status,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// Continuity is what recent sessions left open: failures not yet followed by a passing
// run (recent_actionable) and next steps and pending proposals (open_items).
type Continuity struct {
	RecentActionable []ContinuityItem `json:"recent_actionable"`
	OpenItems        []ContinuityItem `json:"open_items"`
}

// ContinuityItem is one open thread of a recent session.
type ContinuityItem struct {
	Session string `json:"session"`
	Kind    string `json:"kind"` // failure | next_step | proposal
	Text    string `json:"text"`
	Handle  string `json:"handle,omitempty"`
	EntryID string `json:"entry_id,omitempty"`
}

// HandoffView is a capsule as ground lists it: its gist and the paths that changed
// since it was stored (the full capsule of the prior session is session_handoff.capsule).
type HandoffView struct {
	ID          string   `json:"id"`
	SessionID   string   `json:"session_id"`
	Client      string   `json:"client,omitempty"`
	At          string   `json:"at"`
	RecordClass string   `json:"record_class"`
	Words       int      `json:"words"`
	Goals       []string `json:"goals"`
	State       string   `json:"current_state"`
	NextStep    string   `json:"next_step"`
	StalePaths  []string `json:"stale_paths,omitempty"`
	// InjectionFlags label a capsule whose text matches instruction patterns.
	InjectionFlags []string `json:"injection_flags,omitempty"`
}

// SessionBrief is one session as ground lists it.
type SessionBrief struct {
	ID         string `json:"id"`
	Client     string `json:"client,omitempty"`
	Principal  string `json:"principal,omitempty"`
	Status     string `json:"status"`
	Branch     string `json:"branch,omitempty"`
	StartedAt  string `json:"started_at"`
	LastSeenAt string `json:"last_seen_at"`
	Summary    string `json:"summary,omitempty"`
}

// Bounds of the session members.
const (
	groundLastSessions  = 5
	groundHandoffs      = 3
	groundHandles       = 5
	groundContinuity    = 5
	groundRecentWindow  = 3   // sessions continuity reads
	groundRecentCalls   = 200 // tool-call rows continuity reads across them
	groundViewTextMax   = 240
	groundViewGoalCount = 2
)

// buildSessions fills the session members. sessionID is the caller's execution key
// ("" when it gave none); driftStale is the drift section's flag.
func (s *groundingSession) buildSessions(ctx context.Context, dataDir, workspaceID, sessionID string, driftStale *bool) []GroundingUnknown {
	now := time.Now().UTC()
	root := contextRoot(dataDir, workspaceID)
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		sessions, err := r.ListSessions(ctx, govstore.SessionFilter{WorkspaceID: workspaceID, Limit: 4 * groundLastSessions})
		if err != nil || len(sessions) == 0 {
			return err
		}
		main := mainSessions(sessions)
		for _, ss := range main[:min(len(main), groundLastSessions)] {
			s.LastSessions = append(s.LastSessions, SessionBrief{ID: ss.ID, Client: ss.Client, Principal: ss.Principal,
				Status: ss.Status, Branch: ss.Branch, StartedAt: ss.StartedAt, LastSeenAt: ss.LastSeenAt,
				Summary: cutBytes(ss.Summary, groundViewTextMax)})
		}
		if s.SessionHandoff, err = handoffFrom(ctx, r, root, workspaceID, sessionID, sessionID != "", true, main, now); err != nil {
			return err
		}
		if s.SessionHandoff != nil {
			s.SessionHandoff.DriftStale = driftStale
		}
		if s.Continuity, err = continuity(ctx, r, workspaceID, main, now); err != nil {
			return err
		}
		recs, err := latestHandoffs(ctx, r, root, workspaceID, groundHandoffs)
		for _, rec := range recs {
			s.Handoffs = append(s.Handoffs, handoffView(rec))
		}
		return err
	})
	if err != nil {
		reason := "session ledger unreadable: " + err.Error()
		var out []GroundingUnknown
		for _, f := range []string{"session_handoff", "continuity", "handoffs", "last_sessions"} {
			out = append(out, GroundingUnknown{Field: f, Reason: reason})
		}
		return out
	}
	return nil
}

// mainSessions leaves out subagent rows (they have a parent).
func mainSessions(list []govstore.Session) []govstore.Session {
	var out []govstore.Session
	for _, s := range list {
		if s.ParentID == "" {
			out = append(out, s)
		}
	}
	return out
}

// HookSessionHandoff is the handoff a SessionStart hook restores: the session's own
// after a compaction (own), the most recently seen other session's at a new session
// (prior). nil when there is none. Hooks sample no drift: DriftStale stays null.
func HookSessionHandoff(ctx context.Context, dataDir, workspaceID, key string, own, prior bool) (*SessionHandoff, error) {
	var h *SessionHandoff
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		sessions, err := r.ListSessions(ctx, govstore.SessionFilter{WorkspaceID: workspaceID, Limit: 4 * groundLastSessions})
		if err != nil {
			return err
		}
		h, err = handoffFrom(ctx, r, contextRoot(dataDir, workspaceID), workspaceID, key, own, prior, mainSessions(sessions), time.Now().UTC())
		return err
	})
	return h, err
}

// handoffFrom picks the session to hand over and builds its handoff: with own, the
// caller's session (sessionID) when it has a ledger; with prior (and no own session),
// the most recently seen other session.
func handoffFrom(ctx context.Context, r govstore.Reader, root, workspaceID, sessionID string, own, prior bool, main []govstore.Session, now time.Time) (*SessionHandoff, error) {
	var from *govstore.Session
	restore := false
	if own && sessionID != "" {
		if s, err := r.GetSession(ctx, sessionID); err == nil && s.WorkspaceID == workspaceID {
			from, restore = &s, true
		}
	}
	for i := 0; prior && from == nil && i < len(main); i++ {
		if main[i].ID != sessionID {
			from = &main[i]
		}
	}
	if from == nil {
		return nil, nil
	}
	snap, err := latestSnapshot(ctx, r, workspaceID, from.ID, now)
	if err != nil {
		return nil, err
	}
	h := &SessionHandoff{FromSession: from.ID, Client: from.Client, Principal: from.Principal, Status: from.Status,
		LastSeenAt: from.LastSeenAt, Restore: restore, Snapshot: snap, Label: SessionHandoffLabel}
	if h.Handles, err = recentHandles(ctx, r, workspaceID, from.ID, now); err != nil {
		return nil, err
	}
	if h.PendingProposals, err = awaitingVerifier(ctx, r, workspaceID, from.Principal, snap.PendingProposals); err != nil {
		return nil, err
	}
	if _, row, ok := latestCapsule(ctx, r, workspaceID, from.ID); ok {
		h.Capsule, _ = capsuleRecord(ctx, r, root, workspaceID, row)
	}
	return h, nil
}

// recentHandles are the unexpired handles of a session's latest captured tool calls.
func recentHandles(ctx context.Context, r govstore.Reader, workspaceID, key string, now time.Time) ([]HandoffHandle, error) {
	rows, err := r.RecentSessionEvents(ctx, govstore.RecentEventFilter{WorkspaceID: workspaceID, SessionID: key,
		Kinds: []string{LedgerToolCall, LedgerFileRead}, Limit: 4 * groundHandles})
	if err != nil {
		return nil, err
	}
	var out []HandoffHandle
	for _, ev := range rows {
		d := rowText(ev)
		if ev.Handle == "" || !unexpired(d.ExpiresAt, now) || len(out) == groundHandles {
			continue
		}
		out = append(out, HandoffHandle{Handle: ev.Handle, Tool: ev.Tool, What: cutBytes(fallbackString(d.Text, ev.Path), snapItemMax),
			Status: ev.Status, ExpiresAt: d.ExpiresAt})
	}
	return out, nil
}

// awaitingVerifier is what a new session can verify first: the proposals the prior
// session recorded, then the other pending memory its principal wrote, newest first.
func awaitingVerifier(ctx context.Context, r govstore.Reader, workspaceID, principal string, recorded []SnapshotProposal) ([]SnapshotProposal, error) {
	out := append([]SnapshotProposal(nil), recorded...)
	view, err := r.Ranking(ctx, govstore.RankQuery{WorkspaceID: workspaceID, States: []string{govstore.RankPending}})
	if err != nil {
		return nil, err
	}
	for i := len(view) - 1; i >= 0 && len(out) < snapMaxProposals; i-- {
		e := view[i]
		if !strings.EqualFold(e.Author, principal) || containsProposal(out, e.ID) {
			continue
		}
		out = append(out, SnapshotProposal{ID: e.ID, Title: cutBytes(e.Title, snapTitleMax),
			VotesNeeded: max(1, e.RequiredVerifications-e.PeerApprovals)})
	}
	return out, nil
}

func containsProposal(list []SnapshotProposal, id string) bool {
	for _, p := range list {
		if p.ID == id {
			return true
		}
	}
	return false
}

// continuity reads what the most recent sessions left open.
func continuity(ctx context.Context, r govstore.Reader, workspaceID string, main []govstore.Session, now time.Time) (*Continuity, error) {
	c := &Continuity{RecentActionable: []ContinuityItem{}, OpenItems: []ContinuityItem{}}
	recent := main[:min(len(main), groundRecentWindow)]
	pending, err := pendingByID(ctx, r, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, sess := range recent {
		rows, err := r.RecentSessionEvents(ctx, govstore.RecentEventFilter{WorkspaceID: workspaceID, SessionID: sess.ID,
			Kinds: []string{LedgerToolCall, LedgerMemoryProposed}, Limit: groundRecentCalls / groundRecentWindow})
		if err != nil {
			return nil, err
		}
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
		for _, f := range openFailures(rows, now) {
			if len(c.RecentActionable) < groundContinuity {
				c.RecentActionable = append(c.RecentActionable, ContinuityItem{Session: sess.ID, Kind: "failure",
					Text: f.text(), Handle: f.Handle})
			}
		}
		if capsule, _, ok := latestCapsule(ctx, r, workspaceID, sess.ID); ok && len(c.OpenItems) < groundContinuity {
			c.OpenItems = append(c.OpenItems, ContinuityItem{Session: sess.ID, Kind: "next_step",
				Text: cutBytes(capsule.NextStep, groundViewTextMax)})
		}
		for _, ev := range rows {
			if p, ok := pending[ev.EntryID]; ok && ev.Kind == LedgerMemoryProposed && len(c.OpenItems) < groundContinuity {
				c.OpenItems = append(c.OpenItems, ContinuityItem{Session: sess.ID, Kind: "proposal",
					Text: fmt.Sprintf("%q needs %d more vote(s)", p.Title, p.VotesNeeded), EntryID: p.ID})
			}
		}
	}
	if len(c.RecentActionable) == 0 && len(c.OpenItems) == 0 {
		return nil, nil
	}
	return c, nil
}

func handoffView(rec HandoffRecord) HandoffView {
	goals := rec.Capsule.Goals[:min(len(rec.Capsule.Goals), groundViewGoalCount)]
	v := HandoffView{ID: rec.ID, SessionID: rec.SessionID, Client: rec.Client, At: rec.At, RecordClass: rec.RecordClass,
		Words: rec.Words, State: cutBytes(rec.Capsule.CurrentState, groundViewTextMax),
		NextStep: cutBytes(rec.Capsule.NextStep, groundViewTextMax), StalePaths: rec.StalePaths, InjectionFlags: rec.InjectionFlags}
	for _, g := range goals {
		v.Goals = append(v.Goals, cutBytes(g, groundViewTextMax/2))
	}
	return v
}

// driftStaleFlag reads the drift report's stale flag (nil when drift is unknown).
func driftStaleFlag(drift json.RawMessage) *bool {
	var d struct {
		Stale *bool `json:"stale"`
	}
	if len(drift) == 0 || json.Unmarshal(drift, &d) != nil {
		return nil
	}
	return d.Stale
}

// GroundingUnknown names a `ground` field that could not be determined and why.
// Such a field is null (or, for the stale-memory counts, incomplete), never a
// silent 0.
type GroundingUnknown struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// summarize writes the summary from the other sections; "?" marks an unknown count.
func (g *SessionGrounding) summarize() {
	count := func(n *int) string {
		if n == nil {
			return "?"
		}
		return fmt.Sprint(*n)
	}
	runs := fmt.Sprint(len(g.RecentFailedRuns))
	if g.BlockedByFailingVerification == nil {
		runs = "?"
	}
	g.Summary = fmt.Sprintf("%s changed file(s), %s dirty symbol(s), %s contract break(s), %s failed run(s), %s stale memory.",
		count(g.ChangedFiles), count(g.DirtySymbols), count(g.ContractBreaks), runs, count(g.StaleMemory))
	if n := g.MemoryVerificationModes[VerificationSelfAssertedOpen]; n > 0 {
		g.Summary += fmt.Sprintf(" %d memory self-asserted in open mode (not peer-verified).", n)
	}
	if g.PendingForYou != nil && *g.PendingForYou > 0 {
		g.Summary += fmt.Sprintf(" %d pending memory await your verification (recall status=awaiting_me).", *g.PendingForYou)
	}
	if h := g.SessionHandoff; h != nil {
		verb := "Handoff from session"
		if h.Restore {
			verb = "Restore of session"
		}
		g.Summary += fmt.Sprintf(" %s %s (session_handoff).", verb, h.FromSession)
	}
	if len(g.Unknown) > 0 {
		g.Summary += fmt.Sprintf(" %d field(s) unknown (see unknown).", len(g.Unknown))
	}
}

// isUnknown reports whether field is listed unknown.
func (g *SessionGrounding) isUnknown(field string) bool {
	for _, u := range g.Unknown {
		if u.Field == field {
			return true
		}
	}
	return false
}

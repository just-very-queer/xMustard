package workspaceops

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
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
	// Caveat says why a prior session is not one of the caller's own ended sessions,
	// the ones a new session takes over first: "not ended" (running in another terminal,
	// or it never sent SessionEnd), "another principal's", or both.
	Caveat string `json:"caveat,omitempty"`
	// Capsule is the session's handoff capsule, when it wrote or got one, with the
	// paths changed since: its agent's newest one, else the ledger's.
	Capsule *HandoffRecord `json:"capsule,omitempty"`
	// DriftStale is the drift section's stale flag (null when drift is unknown).
	DriftStale *bool `json:"drift_stale"`
	// InjectionFlags are the instruction patterns the snapshot's text matches (WS-56):
	// it carries prompt, command and error excerpts; a SessionStart hook withholds a
	// flagged one.
	InjectionFlags []string `json:"injection_flags,omitempty"`
	Label          string   `json:"label"`
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
	groundRecentCalls   = 200 // tool-call and file rows continuity reads across them
	groundViewTextMax   = 240
	groundViewGoalCount = 2
	groundViewPaths     = 8 // changed paths a hook names for a capsule
)

// buildSessions fills the session members. caller is the principal asking, sessionID
// its execution key ("" when it gave none); driftStale is the drift section's flag.
func (s *groundingSession) buildSessions(ctx context.Context, dataDir, workspaceID, caller, sessionID string, driftStale *bool) []GroundingUnknown {
	now := time.Now().UTC()
	root := contextRoot(dataDir, workspaceID)
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		sessions, err := r.ListSessions(ctx, govstore.SessionFilter{WorkspaceID: workspaceID, Limit: 4 * groundLastSessions})
		if err != nil || len(sessions) == 0 {
			return err
		}
		main := mainSessions(sessions)
		view, err := pendingRanking(ctx, r, workspaceID)
		if err != nil {
			return err
		}
		for _, ss := range main[:min(len(main), groundLastSessions)] {
			s.LastSessions = append(s.LastSessions, SessionBrief{ID: ss.ExecutionKey, Client: ss.Client, Principal: ss.Principal,
				Status: ss.Status, Branch: ss.Branch, StartedAt: ss.StartedAt, LastSeenAt: ss.LastSeenAt,
				Summary: cutBytes(ss.Summary, groundViewTextMax)})
		}
		pick := handoffPick{principal: caller, key: sessionID, own: sessionID != "", prior: true}
		if s.SessionHandoff, err = pick.handoff(ctx, r, root, workspaceID, main, view, now); err != nil {
			return err
		}
		if s.SessionHandoff != nil {
			s.SessionHandoff.DriftStale = driftStale
		}
		if s.Continuity, err = continuity(ctx, r, workspaceID, main, pendingByID(view), now); err != nil {
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

// HookSessionHandoff is the handoff a SessionStart hook restores for principal's
// session key: the session's own after a compaction (own), a prior session's at a new
// session (prior, handoffPick). nil when there is none. Hooks sample no drift:
// DriftStale stays null.
func HookSessionHandoff(ctx context.Context, dataDir, workspaceID, principal, key string, own, prior bool) (*SessionHandoff, error) {
	var h *SessionHandoff
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		sessions, err := r.ListSessions(ctx, govstore.SessionFilter{WorkspaceID: workspaceID, Limit: 4 * groundLastSessions})
		if err != nil {
			return err
		}
		view, err := pendingRanking(ctx, r, workspaceID)
		if err != nil {
			return err
		}
		pick := handoffPick{principal: principal, key: key, own: own, prior: prior}
		h, err = pick.handoff(ctx, r, contextRoot(dataDir, workspaceID), workspaceID, mainSessions(sessions), view, time.Now().UTC())
		return err
	})
	return h, err
}

// handoffPick is whose handoff a caller gets: with own, its own session's (key) when
// that has a ledger; with prior (and no own session), a prior session's (priorRank).
type handoffPick struct {
	principal, key string
	own, prior     bool
}

// priorCaveats label a prior session by its priorRank.
var priorCaveats = [...]string{"", "not ended", "another principal's", "another principal's, not ended"}

// priorRank ranks a session a new session may take over, 0 best: the caller's own
// before another principal's, and an ended session before one that is not (running in
// another terminal, or it never sent SessionEnd). Ties go to the most recently seen.
func priorRank(s govstore.Session, principal string) int {
	rank := 0
	if !strings.EqualFold(s.Principal, ledgerPrincipal(principal)) {
		rank += 2
	}
	if s.Status == govstore.SessionActive {
		rank++
	}
	return rank
}

// from is the session to hand over and its caveat, nil when there is none; main is most
// recently seen first.
func (p handoffPick) from(ctx context.Context, r govstore.Reader, workspaceID string, main []govstore.Session) (*govstore.Session, bool, string) {
	if p.own && p.key != "" {
		if s, err := workspaceSession(ctx, r, workspaceID, p.key); err == nil {
			return &s, true, ""
		}
	}
	best, rank := -1, len(priorCaveats)
	for i := 0; p.prior && i < len(main); i++ {
		if n := priorRank(main[i], p.principal); main[i].ExecutionKey != p.key && n < rank {
			best, rank = i, n
		}
	}
	if best < 0 {
		return nil, false, ""
	}
	return &main[best], false, priorCaveats[rank]
}

// handoff builds the handoff of the session picked.
func (p handoffPick) handoff(ctx context.Context, r govstore.Reader, root, workspaceID string, main []govstore.Session,
	pending []govstore.RankEntry, now time.Time) (*SessionHandoff, error) {
	from, restore, caveat := p.from(ctx, r, workspaceID, main)
	if from == nil {
		return nil, nil
	}
	key := from.ExecutionKey
	snap, err := latestSnapshot(ctx, r, workspaceID, key, now)
	if err != nil {
		return nil, err
	}
	h := &SessionHandoff{FromSession: key, Client: from.Client, Principal: from.Principal, Status: from.Status,
		LastSeenAt: from.LastSeenAt, Restore: restore, Caveat: caveat, Snapshot: snap, Label: SessionHandoffLabel,
		InjectionFlags: injection.Scan(snap.Render()).Flags}
	if h.Handles, err = recentHandles(ctx, r, workspaceID, from.ID, now); err != nil {
		return nil, err
	}
	h.PendingProposals = awaitingVerifier(pending, from.Principal, snap.PendingProposals)
	if _, row, ok := sessionCapsule(ctx, r, workspaceID, key, false); ok {
		h.Capsule, _ = capsuleRecord(ctx, r, root, workspaceID, row)
	}
	return h, nil
}

// SnapshotText is the handoff's snapshot as a SessionStart hook injects it: the
// snapshot, then the other proposals of its principal that await a distinct verifier,
// as many as fit within SnapshotMaxBytes, the rest counted.
func (h *SessionHandoff) SnapshotText() string {
	text := h.Snapshot.Render()
	extra := h.PendingProposals[min(len(h.PendingProposals), len(h.Snapshot.PendingProposals)):]
	more := func(n int) string {
		return fmt.Sprintf("\n%d more proposal(s) await a distinct verifier: recall(status=\"awaiting_me\")", n)
	}
	kept := 0
	for i, p := range extra {
		line := fmt.Sprintf("\nawaiting a distinct verifier: %s %q needs %d more vote(s)", p.ID, p.Title, p.VotesNeeded)
		room := SnapshotMaxBytes - len(line)
		if i < len(extra)-1 { // leave room to count the ones after it
			room -= len(more(len(extra)))
		}
		if len(text) > room {
			break
		}
		text, kept = text+line, kept+1
	}
	if n := len(extra) - kept; n > 0 && len(text)+len(more(n)) <= SnapshotMaxBytes {
		text += more(n)
	}
	return text
}

// recentHandles are the unexpired handles of a session row's latest captured tool calls.
func recentHandles(ctx context.Context, r govstore.Reader, workspaceID, row string, now time.Time) ([]HandoffHandle, error) {
	rows, err := r.RecentSessionEvents(ctx, govstore.RecentEventFilter{WorkspaceID: workspaceID, SessionID: row,
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
		out = append(out, HandoffHandle{Handle: ev.Handle, Tool: ev.Tool, What: cutBytes(cmp.Or(d.Text, ev.Path), snapItemMax),
			Status: ev.Status, ExpiresAt: d.ExpiresAt})
	}
	return out, nil
}

// awaitingVerifier is what a new session can verify first: the proposals the prior
// session recorded, then the other pending memory its principal wrote, newest first.
func awaitingVerifier(pending []govstore.RankEntry, principal string, recorded []SnapshotProposal) []SnapshotProposal {
	out := append([]SnapshotProposal(nil), recorded...)
	for i := len(pending) - 1; i >= 0 && len(out) < snapMaxProposals; i-- {
		if e := pending[i]; strings.EqualFold(e.Author, principal) && !containsProposal(out, e.ID) {
			out = append(out, proposalOf(e))
		}
	}
	return out
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
func continuity(ctx context.Context, r govstore.Reader, workspaceID string, main []govstore.Session, pending map[string]SnapshotProposal,
	now time.Time) (*Continuity, error) {
	c := &Continuity{RecentActionable: []ContinuityItem{}, OpenItems: []ContinuityItem{}}
	listed := map[string]bool{} // a proposal edited in a session is recorded again
	for _, sess := range main[:min(len(main), groundRecentWindow)] {
		// file rows close a failed file tool's call (openFailures)
		rows, err := r.RecentSessionEvents(ctx, govstore.RecentEventFilter{WorkspaceID: workspaceID, SessionID: sess.ID,
			Kinds: []string{LedgerToolCall, LedgerFileRead, LedgerFileModified, LedgerMemoryProposed}, Limit: groundRecentCalls / groundRecentWindow})
		if err != nil {
			return nil, err
		}
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
		key := sess.ExecutionKey
		for _, f := range openFailures(rows, now) {
			if len(c.RecentActionable) < groundContinuity {
				c.RecentActionable = append(c.RecentActionable, ContinuityItem{Session: key, Kind: "failure",
					Text: f.text(), Handle: f.Handle})
			}
		}
		if capsule, _, ok := sessionCapsule(ctx, r, workspaceID, key, false); ok && len(c.OpenItems) < groundContinuity {
			c.OpenItems = append(c.OpenItems, ContinuityItem{Session: key, Kind: "next_step",
				Text: cutBytes(capsule.NextStep, groundViewTextMax)})
		}
		for _, ev := range rows {
			if p, ok := pending[ev.EntryID]; ok && ev.Kind == LedgerMemoryProposed && !listed[p.ID] && len(c.OpenItems) < groundContinuity {
				listed[p.ID] = true
				c.OpenItems = append(c.OpenItems, ContinuityItem{Session: key, Kind: "proposal",
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

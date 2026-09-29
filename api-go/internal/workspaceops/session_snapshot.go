package workspaceops

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/govstore"
)

// Compaction snapshot (WS-33; PAR-HAR-04). At a client's compaction boundary the
// ledger builds a snapshot of the session: its goal, open failures with their evidence
// handles, the files it touched, the proposals it left pending, its decisions and its
// next step. Rendered, it is at most SnapshotMaxBytes; the tiers are filled in priority
// order and what does not fit is counted, never cut silently. It is stored as a derived
// ledger row (never memory, never verified) and re-injected after compaction (Claude
// Code: SessionStart source=compact) or handed to the next session in any client
// (ground session_handoff). The format is client-neutral (SnapshotSchema): a Codex,
// OpenCode or Pi adapter reads the same JSON and the same rendered text.

// SnapshotSchema names the snapshot format.
const SnapshotSchema = "xmustard.snapshot/v1"

// SnapshotMaxBytes bounds a rendered snapshot (PAR-HAR-04: 2 KB).
const SnapshotMaxBytes = 2048

// SessionSnapshot is one session's state at a point of its ledger.
type SessionSnapshot struct {
	Schema    string `json:"schema"`
	SessionID string `json:"session_id"`
	Client    string `json:"client,omitempty"`
	Principal string `json:"principal,omitempty"`
	// ThroughSeq is the newest ledger row the snapshot covers.
	ThroughSeq       int64              `json:"through_seq"`
	At               string             `json:"at"`
	Goal             string             `json:"goal,omitempty"`
	OpenFailures     []SnapshotFailure  `json:"open_failures,omitempty"`
	ActiveFiles      []SnapshotFile     `json:"active_files,omitempty"`
	PendingProposals []SnapshotProposal `json:"pending_proposals,omitempty"`
	Decisions        []string           `json:"decisions,omitempty"`
	NextStep         string             `json:"next_step,omitempty"`
	// Omitted counts, per tier, the items the size bound left out.
	Omitted map[string]int `json:"omitted,omitempty"`
	// WorkspaceID is where Handle's originals live (the expansion hint names it).
	WorkspaceID string `json:"workspace_id"`
}

// SnapshotFailure is a failed tool call not yet followed by a passing run of the same
// call. Handle expands the failure's captured output while it is unexpired.
type SnapshotFailure struct {
	Tool string `json:"tool"`
	// What is the call (a shell command); Signature names the failure.
	What      string `json:"what,omitempty"`
	Signature string `json:"signature,omitempty"`
	Handle    string `json:"handle,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// SnapshotFile is a file the session read or modified, most recent first.
type SnapshotFile struct {
	Path     string `json:"path"`
	Modified bool   `json:"modified,omitempty"`
}

// SnapshotProposal is a memory the session proposed that still waits for verifiers.
type SnapshotProposal struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	VotesNeeded int    `json:"votes_needed"`
}

// Per-item bounds before the size fit; the size fit bounds the items (and counts the
// ones it leaves out), and the ledger tail bounds the rows read.
const (
	snapGoalMax  = 300
	snapItemMax  = 160
	snapTitleMax = 100
	// snapMaxProposals bounds the proposals a handoff lists besides the session's own.
	snapMaxProposals = 8
)

// snapshotTier is one priority tier: its rendered lines, most important first, and how
// to keep the first n of them.
type snapshotTier struct {
	name  string
	lines func(*SessionSnapshot) []string
	keep  func(*SessionSnapshot, int)
}

// snapshotTiers are filled in this order (PAR-HAR-04): goal, open failures with
// handles, active files, pending proposals, decisions, next step.
var snapshotTiers = []snapshotTier{
	{"goal",
		func(s *SessionSnapshot) []string { return optionalLine("goal: ", s.Goal) },
		func(s *SessionSnapshot, n int) { s.Goal = keepIf(s.Goal, n) }},
	{"failure",
		func(s *SessionSnapshot) []string { return mapLines(s.OpenFailures, SnapshotFailure.line) },
		func(s *SessionSnapshot, n int) { s.OpenFailures = s.OpenFailures[:n] }},
	{"file",
		func(s *SessionSnapshot) []string { return mapLines(s.ActiveFiles, SnapshotFile.line) },
		func(s *SessionSnapshot, n int) { s.ActiveFiles = s.ActiveFiles[:n] }},
	{"proposal",
		func(s *SessionSnapshot) []string { return mapLines(s.PendingProposals, SnapshotProposal.line) },
		func(s *SessionSnapshot, n int) { s.PendingProposals = s.PendingProposals[:n] }},
	{"decision",
		func(s *SessionSnapshot) []string {
			return mapLines(s.Decisions, func(d string) string { return "decision: " + d })
		},
		func(s *SessionSnapshot, n int) { s.Decisions = s.Decisions[:n] }},
	{"next",
		func(s *SessionSnapshot) []string { return optionalLine("next: ", s.NextStep) },
		func(s *SessionSnapshot, n int) { s.NextStep = keepIf(s.NextStep, n) }},
}

// keepIf keeps a one-item tier's value when its item was kept.
func keepIf(v string, n int) string {
	if n == 0 {
		return ""
	}
	return v
}

func optionalLine(label, v string) []string {
	if v == "" {
		return nil
	}
	return []string{label + v}
}

func mapLines[T any](items []T, line func(T) string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = line(it)
	}
	return out
}

func (f SnapshotFailure) line() string {
	l := "failure: " + f.text()
	if f.Handle != "" {
		l += " (handle " + f.Handle + ")"
	}
	return l
}

// text is the failure without its handle: the tool, the call and the signature.
func (f SnapshotFailure) text() string {
	t := f.Tool
	if f.What != "" {
		t += " `" + f.What + "`"
	}
	if f.Signature != "" {
		t += ": " + f.Signature
	}
	return t
}

func (f SnapshotFile) line() string {
	if f.Modified {
		return "file: " + f.Path + " (modified)"
	}
	return "file: " + f.Path
}

func (p SnapshotProposal) line() string {
	return fmt.Sprintf("proposal: %s %q needs %d more vote(s)", p.ID, p.Title, p.VotesNeeded)
}

func (s *SessionSnapshot) header() string {
	return fmt.Sprintf("[xmustard snapshot] session %s, ledger through #%d, %s", s.SessionID, s.ThroughSeq, s.At)
}

// expandHint says how to expand a handle; it is rendered only when a failure keeps one.
func (s *SessionSnapshot) expandHint() string {
	return "expand a handle: resources/read " + evidence.ResourceURI("<handle>", s.WorkspaceID) + " (Pi: xmustard_expand)"
}

func (s *SessionSnapshot) hasHandle() bool {
	return slices.ContainsFunc(s.OpenFailures, func(f SnapshotFailure) bool { return f.Handle != "" })
}

// Render is the snapshot as the text a client injects: one line per item, tiers in
// priority order, at most SnapshotMaxBytes once fitted.
func (s *SessionSnapshot) Render() string {
	lines := []string{s.header()}
	for _, t := range snapshotTiers {
		lines = append(lines, t.lines(s)...)
	}
	if s.hasHandle() {
		lines = append(lines, s.expandHint())
	}
	if len(s.Omitted) > 0 {
		var parts []string
		for _, t := range snapshotTiers {
			if n := s.Omitted[t.name]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, t.name))
			}
		}
		lines = append(lines, "omitted for size: "+strings.Join(parts, ", ")+" (ground session_id="+s.SessionID+")")
	}
	return strings.Join(lines, "\n")
}

// fit keeps the items that fit in max bytes in strict priority order and counts the
// rest: tiers are filled in order, items within a tier most important first, and at
// the first item that does not fit everything after it (in its tier and in every lower
// tier) is left out. The kept items are always a prefix of the priority order. The
// omitted line is then made room for by leaving out more from the end.
func (s *SessionSnapshot) fit(max int) {
	room := max - len(s.header())
	if s.hasHandle() {
		room -= len(s.expandHint()) + 1
	}
	full := false
	for _, t := range snapshotTiers {
		lines := t.lines(s)
		kept := 0
		for !full && kept < len(lines) {
			if full = len(lines[kept])+1 > room; !full {
				room -= len(lines[kept]) + 1
				kept++
			}
		}
		s.omit(t, len(lines)-kept)
	}
	for len(s.Render()) > max && s.dropLast() {
	}
}

// omit counts n items of a tier left out and keeps the others.
func (s *SessionSnapshot) omit(t snapshotTier, n int) {
	if n == 0 {
		return
	}
	if s.Omitted == nil {
		s.Omitted = map[string]int{}
	}
	s.Omitted[t.name] += n
	t.keep(s, len(t.lines(s))-n)
}

// dropLast leaves out the last kept item of the lowest tier that has one.
func (s *SessionSnapshot) dropLast() bool {
	for i := len(snapshotTiers) - 1; i >= 0; i-- {
		if t := snapshotTiers[i]; len(t.lines(s)) > 0 {
			s.omit(t, 1)
			return true
		}
	}
	return false
}

// BuildSessionSnapshot builds a session's snapshot from its ledger and the memory store:
// read-only, so ground and a hook can build one when none is stored.
func BuildSessionSnapshot(ctx context.Context, dataDir, workspaceID, key string) (*SessionSnapshot, error) {
	var snap *SessionSnapshot
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		snap, err = buildSnapshot(ctx, r, workspaceID, key, time.Now().UTC())
		return err
	})
	return snap, err
}

// buildSnapshot folds a session's newest rows into its snapshot and fits it.
func buildSnapshot(ctx context.Context, r govstore.Reader, workspaceID, key string, now time.Time) (*SessionSnapshot, error) {
	sess, evs, err := sessionTail(ctx, r, workspaceID, key)
	if err != nil {
		return nil, err
	}
	s := &SessionSnapshot{Schema: SnapshotSchema, SessionID: key, Client: sess.Client, Principal: sess.Principal,
		WorkspaceID: workspaceID, At: now.Format(time.RFC3339)}
	var proposed []string
	for _, ev := range evs {
		s.ThroughSeq = max(s.ThroughSeq, ev.Seq)
		if ev.Kind == LedgerPrompt && s.Goal == "" {
			s.Goal = cutBytes(rowText(ev).Text, snapGoalMax)
		}
		if ev.Kind == LedgerMemoryProposed && ev.EntryID != "" && !slices.Contains(proposed, ev.EntryID) {
			proposed = append(proposed, ev.EntryID)
		}
	}
	if s.Goal == "" { // the session's first prompt may be older than the tail
		first, err := r.ListSessionEvents(ctx, govstore.SessionEventFilter{SessionID: sess.ID, Kinds: []string{LedgerPrompt}, Limit: 1})
		if err != nil {
			return nil, err
		}
		for _, ev := range first {
			s.Goal = cutBytes(rowText(ev).Text, snapGoalMax)
		}
	}
	s.OpenFailures = openFailures(evs, now)
	s.ActiveFiles = activeFiles(evs)
	// the goal and next step an agent wrote in its own capsule; a derived capsule only
	// restates the ledger, and its next step is no plan of the agent's
	if c, _, ok := sessionCapsule(ctx, r, workspaceID, key, true); ok {
		s.Goal = cmp.Or(cutBytes(firstOf(c.Goals), snapGoalMax), s.Goal)
		s.NextStep = cutBytes(c.NextStep, snapGoalMax)
	}
	s.PendingProposals, s.Decisions, err = proposalState(ctx, r, workspaceID, proposed)
	if err != nil {
		return nil, err
	}
	s.fit(SnapshotMaxBytes)
	return s, nil
}

func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// openFailures are the failed calls not followed by a passing call of the same call
// (callOf: a failed Edit is closed by a later Edit of its file), newest first; an
// expired handle is dropped (its original is gone).
func openFailures(evs []govstore.SessionEvent, now time.Time) []SnapshotFailure {
	fixed := map[string]bool{}
	var out []SnapshotFailure
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		call := callOf(ev)
		if passed(ev) {
			fixed[call] = true
			continue
		}
		if ev.Kind != LedgerToolCall || ev.Status != LedgerError || fixed[call] {
			continue
		}
		fixed[call] = true // one entry per call
		d := rowText(ev)
		f := SnapshotFailure{Tool: ev.Tool, What: cutBytes(d.Text, snapItemMax), Signature: cutBytes(d.Signature, snapItemMax)}
		if ev.Handle != "" && unexpired(d.ExpiresAt, now) {
			f.Handle, f.ExpiresAt = ev.Handle, d.ExpiresAt
		}
		out = append(out, f)
	}
	return out
}

// unexpired reports whether an RFC 3339 expiry is after now ("" never expires).
func unexpired(expiresAt string, now time.Time) bool {
	if expiresAt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, expiresAt)
	return err == nil && t.After(now)
}

// activeFiles are the files the session modified, then those it only read, each most
// recently touched first.
func activeFiles(evs []govstore.SessionEvent) []SnapshotFile {
	modified, read := map[string]bool{}, map[string]bool{}
	var mod, rd []SnapshotFile
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		p := cutBytes(ev.Path, snapItemMax)
		switch {
		case p == "":
		case ev.Kind == LedgerFileModified && !modified[p]:
			modified[p] = true
			mod = append(mod, SnapshotFile{Path: p, Modified: true})
		case ev.Kind == LedgerFileRead && !read[p]:
			read[p] = true
			rd = append(rd, SnapshotFile{Path: p})
		}
	}
	rd = slices.DeleteFunc(rd, func(f SnapshotFile) bool { return modified[f.Path] })
	return append(mod, rd...)
}

// proposalState reads the memories a session proposed: those still pending, with the
// peer votes they need, and the titles of its decisions.
func proposalState(ctx context.Context, r govstore.Reader, workspaceID string, ids []string) ([]SnapshotProposal, []string, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	view, err := pendingRanking(ctx, r, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	pending := pendingByID(view)
	var props []SnapshotProposal
	var decisions []string
	for i := len(ids) - 1; i >= 0; i-- { // newest first
		e, err := r.GetEntry(ctx, ids[i])
		if err != nil || e.WorkspaceID != workspaceID {
			continue
		}
		if p, ok := pending[e.ID]; ok {
			props = append(props, p)
		}
		if e.Kind == "decision" {
			decisions = append(decisions, cutBytes(e.Title, snapItemMax))
		}
	}
	return props, decisions, nil
}

// pendingRanking is the workspace's pending memory, oldest first.
func pendingRanking(ctx context.Context, r govstore.Reader, workspaceID string) ([]govstore.RankEntry, error) {
	return r.Ranking(ctx, govstore.RankQuery{WorkspaceID: workspaceID, States: []string{govstore.RankPending}})
}

// proposalOf is a pending memory with the peer votes it still needs.
func proposalOf(e govstore.RankEntry) SnapshotProposal {
	return SnapshotProposal{ID: e.ID, Title: cutBytes(e.Title, snapTitleMax), VotesNeeded: max(1, e.RequiredVerifications-e.PeerApprovals)}
}

// pendingByID indexes pending memory by id.
func pendingByID(view []govstore.RankEntry) map[string]SnapshotProposal {
	out := make(map[string]SnapshotProposal, len(view))
	for _, e := range view {
		out[e.ID] = proposalOf(e)
	}
	return out
}

// storeSnapshot builds a session's snapshot and appends it as a derived ledger row. It
// is a ledger job (PreCompact, SessionEnd): it runs after the rows queued before it.
func storeSnapshot(ctx context.Context, dataDir, workspaceID, key, principal string) error {
	row := sessionRow(workspaceID, key)
	defer touched(dataDir, row)
	return memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		snap, err := buildSnapshot(ctx, tx, workspaceID, key, time.Now().UTC())
		if err != nil {
			return err
		}
		_, err = tx.AppendSessionEvent(ctx, govstore.SessionEventInput{SessionID: row, Kind: LedgerSnapshot,
			RecordClass: govstore.RecordDerived, Data: snap}, govstore.Actor{Principal: ledgerPrincipal(principal), SessionID: key})
		return err
	})
}

// LatestSessionSnapshot is the snapshot to restore for a session: the newest one stored
// for it when it covers every row but the compaction markers after it, else one built
// now from the ledger (the PreCompact job may not have landed, or none ran). found is
// false when the session has no ledger.
func LatestSessionSnapshot(ctx context.Context, dataDir, workspaceID, key string) (*SessionSnapshot, bool, error) {
	var snap *SessionSnapshot
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		snap, err = latestSnapshot(ctx, r, workspaceID, key, time.Now().UTC())
		return err
	})
	if isNotExist(err) {
		return nil, false, nil
	}
	return snap, err == nil, err
}

func latestSnapshot(ctx context.Context, r govstore.Reader, workspaceID, key string, now time.Time) (*SessionSnapshot, error) {
	row := sessionRow(workspaceID, key)
	stored, err := r.RecentSessionEvents(ctx, govstore.RecentEventFilter{WorkspaceID: workspaceID, SessionID: row,
		Kinds: []string{LedgerSnapshot}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(stored) == 1 {
		var s SessionSnapshot
		if json.Unmarshal(stored[0].Data, &s) == nil && s.Schema == SnapshotSchema {
			newer, err := r.RecentSessionEvents(ctx, govstore.RecentEventFilter{WorkspaceID: workspaceID, SessionID: row,
				Kinds: slices.DeleteFunc(slices.Clone(activityKinds), isCompactionMarker), Limit: 1})
			if err != nil {
				return nil, err
			}
			if len(newer) == 0 || newer[0].Seq <= s.ThroughSeq {
				dropExpiredHandles(&s, now)
				return &s, nil
			}
		}
	}
	return buildSnapshot(ctx, r, workspaceID, key, now)
}

// isCompactionMarker: the rows a compaction itself appends do not make its snapshot old.
func isCompactionMarker(kind string) bool {
	return kind == LedgerCompaction || kind == LedgerSessionStart
}

// dropExpiredHandles removes the handles whose originals expired since the snapshot.
func dropExpiredHandles(s *SessionSnapshot, now time.Time) {
	for i := range s.OpenFailures {
		if f := &s.OpenFailures[i]; f.Handle != "" && !unexpired(f.ExpiresAt, now) {
			f.Handle, f.ExpiresAt = "", ""
		}
	}
}

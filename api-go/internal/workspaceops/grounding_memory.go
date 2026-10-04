package workspaceops

import (
	"cmp"
	"context"
	"encoding/json"
	"sort"

	"xmustard/api-go/internal/govstore"
)

// groundingMemory is the memory section of `ground`: promoted memory whose
// referenced files drifted, how promoted memory is trusted, the verification
// queue waiting on the caller, the memory revision and what the session is pushed
// (WS-31), onboarding for an empty workspace, and what changed since a revision the
// caller names.
type groundingMemory struct {
	// StaleMemory is null when the memory store could not be read (see unknown).
	StaleMemory *int `json:"stale_memory"`
	// StaleMemoryChecked / Total / Complete make the bounded drift check explicit:
	// only the most recent groundStaleWindow memories with path baselines are hashed.
	// Total is null with StaleMemory; Checked is then 0 (nothing was checked).
	StaleMemoryChecked  int  `json:"stale_memory_checked"`
	StaleMemoryTotal    *int `json:"stale_memory_total"`
	StaleMemoryComplete bool `json:"stale_memory_complete"`
	// MemoryVerificationModes counts promoted memories by trust basis (peer_verified,
	// self_asserted_open_mode, single_agent), so an agent can tell peer-verified
	// shared memory from self-asserted memory before it relies on recall.
	MemoryVerificationModes map[string]int `json:"memory_verification_modes"`
	// PendingForYou counts the pending entries awaiting the caller's verdict: what
	// recall(status=awaiting_me) lists (PAR-GOV-02).
	PendingForYou *int `json:"pending_for_you"`
	// MemoryPressure sizes the store (PAR-RCL-06).
	MemoryPressure *MemoryPressure `json:"memory_pressure"`
	// MemoryRevision is the workspace's memory revision ground read (PAR-HAR-03): pass it
	// as since_revision to learn what changed after it.
	MemoryRevision *int64 `json:"memory_revision"`
	// MemoryBudget is what the standing-memory projection pushes into a session
	// (PAR-CTX-13); null when it could not be compiled.
	MemoryBudget *MemoryBudget `json:"memory_budget,omitempty"`
	// Onboarding is set when the workspace has no promoted memory (PAR-GOV-15).
	Onboarding *Onboarding `json:"onboarding,omitempty"`
	// MemoryDelta answers since_revision (PAR-HAR-03).
	MemoryDelta *MemoryDelta `json:"memory_delta,omitempty"`
	// AtRiskMemories are the served memories a change reached: the caller's working
	// changes and HEAD moves are looked up in the anchor index before this list is read,
	// and each is marked needs_reverify (WS-28, PAR-PROV-03). At most maxAtRisk.
	AtRiskMemories []AtRiskMemory `json:"at_risk_memories"`
	// AtRiskTruncated: more than maxAtRisk memories are at risk.
	AtRiskTruncated bool `json:"at_risk_truncated,omitempty"`
}

// MemoryPressure is how full shared memory is: the percentage of served memory in the
// core tier (always delivered), and how many proposals wait for verification.
type MemoryPressure struct {
	CorePct      int `json:"core_pct"`
	PendingCount int `json:"pending_count"`
}

// groundMemoryOptions select the optional parts of the memory section.
type groundMemoryOptions struct {
	// since is the since_revision argument; negative when not given.
	since int64
	// budget adds memory_budget (the projection); hooks compile the projection
	// themselves and leave it out.
	budget bool
	// invalidate looks the working changes and HEAD moves up in the anchor index before
	// at_risk_memories is read. It runs git and the core, so a hook (which starts no
	// process) leaves it off and lists what earlier changes marked.
	invalidate bool
	// ctx bounds the invalidation (the caller's request); changes are the working-changes
	// result the index section already read, so the invalidation does not read it again.
	ctx     context.Context
	changes json.RawMessage
	// The caller's own scope keys (WS-29b): a private, run, session or directory
	// memory the caller may not read is not counted in ground's totals or at-risk
	// details either. Empty means the caller named none, so none is served.
	scopeDir, scopeRunID, scopeSessionID string
}

// noSince is the since value of a ground call that names no revision.
const noSince = -1

// build runs the stale verified-memory check (drift-on-recall): memory whose
// referenced files changed. Bounded: only the most recent groundStaleWindow
// baselined memories are hashed, and the result says whether that covered every one.
// When the memory store cannot be read, nothing was checked: stale_memory,
// stale_memory_total, memory_verification_modes, pending_for_you, memory_pressure and
// memory_revision are null, stale_memory_complete is false, and the fields are listed
// unknown.
func (s *groundingMemory) build(dataDir, workspaceID, caller string, opts groundMemoryOptions) []GroundingUnknown {
	scan, err := scanGroundMemory(dataDir, workspaceID, caller, groundStaleWindow, opts.since, opts)
	s.StaleMemoryChecked, s.StaleMemoryComplete, s.MemoryVerificationModes = scan.checked, scan.complete, scan.modes
	if err != nil {
		reason := "memory store unreadable: " + err.Error()
		var out []GroundingUnknown
		for _, f := range []string{"stale_memory", "stale_memory_total", "memory_verification_modes", "pending_for_you", "memory_pressure", "memory_revision", "at_risk_memories"} {
			out = append(out, GroundingUnknown{Field: f, Reason: reason})
		}
		return out
	}
	s.StaleMemory, s.StaleMemoryTotal, s.PendingForYou = &scan.stale, &scan.total, &scan.forYou
	s.MemoryPressure, s.MemoryRevision, s.MemoryDelta = &scan.pressure, &scan.revision, scan.delta
	s.AtRiskMemories, s.AtRiskTruncated = scan.atRisk, scan.atRiskTruncated
	var unknown []GroundingUnknown
	if scan.invalidationErr != nil {
		unknown = append(unknown, GroundingUnknown{Field: "at_risk_memories", Reason: "changes not checked against memory anchors: " + scan.invalidationErr.Error()})
	}
	if scan.total == 0 {
		s.Onboarding = &Onboarding{Needed: true, Propose: onboardingChecklist}
	}
	if !opts.budget {
		return unknown
	}
	p, err := CoreProjection(context.Background(), dataDir, workspaceID, ProjectionDefaultChars)
	if err != nil {
		return append(unknown, GroundingUnknown{Field: "memory_budget", Reason: "memory projection unavailable: " + err.Error()})
	}
	s.MemoryBudget = memoryBudgetOf(p)
	return unknown
}

// groundStaleWindow bounds how many promoted memories `ground` drift-checks.
const groundStaleWindow = 64

// groundMemoryScan is what ground reads from the memory store.
type groundMemoryScan struct {
	stale, checked, total int
	complete              bool
	modes                 map[string]int
	forYou                int
	pressure              MemoryPressure
	revision              int64
	delta                 *MemoryDelta
	atRisk                []AtRiskMemory
	atRiskTruncated       bool
	// invalidationErr: the changes could not be read or looked up, so at_risk lists only
	// what earlier changes marked.
	invalidationErr error
}

// scanGroundMemory reads served and pending memory from content-free metadata in one
// ranking view. It drift-checks at most window served memories that carry path
// baselines, most recently updated first, counts the served memory per verification
// mode and in the core tier, counts the pending entries awaiting the caller, and reads
// the memory revision and, when since >= 0, the delta after since, from the same
// snapshot.
func scanGroundMemory(dataDir, workspaceID, caller string, window int, since int64, opts groundMemoryOptions) (groundMemoryScan, error) {
	ctx := cmp.Or(opts.ctx, context.Background())
	var view []govstore.RankEntry
	var checkedEntries []ContextEntry
	var events deltaEvents
	baselined := 0
	scan := groundMemoryScan{modes: newModeCounts()}
	if opts.invalidate {
		scan.invalidationErr = invalidateForGround(ctx, dataDir, workspaceID, opts.changes)
	}
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		if scan.revision, err = r.MemoryRevision(ctx, workspaceID); err != nil {
			return err
		}
		// The caller's own keys narrow this scan as they narrow recall (WS-29b): a
		// private, run, session or directory memory the caller may not read is not
		// counted here either, so ground's totals and at-risk details cannot disclose
		// it. A caller naming no key sees no confined entry.
		view, err = r.Ranking(ctx, govstore.RankQuery{WorkspaceID: workspaceID, Caller: caller,
			States:   []string{govstore.RankServed, govstore.RankPending},
			ScopeDir: opts.scopeDir, ScopeRunID: opts.scopeRunID, ScopeSessionID: opts.scopeSessionID})
		if err != nil {
			return err
		}
		sort.SliceStable(view, func(a, b int) bool { return view[a].UpdatedAt > view[b].UpdatedAt })
		var ids []string
		for _, e := range view {
			if e.State != govstore.RankServed || !e.Baselined {
				continue
			}
			if baselined++; len(ids) < window {
				ids = append(ids, e.ID)
			}
		}
		if checkedEntries, err = projectByID(ctx, r, ids); err != nil {
			return err
		}
		if since >= 0 {
			if events, err = readDeltaEvents(ctx, r, workspaceID, since); err != nil {
				return err
			}
		}
		scan.atRisk, scan.atRiskTruncated, err = readAtRisk(ctx, r, workspaceID, caller,
			ScopeRequest{Dir: opts.scopeDir, RunID: opts.scopeRunID, SessionID: opts.scopeSessionID})
		return err
	})
	if err != nil {
		return groundMemoryScan{}, err
	}
	awaits := awaitingCaller(dataDir, caller)
	core := 0
	served := map[string]bool{}
	for _, e := range view {
		if e.State == govstore.RankPending {
			scan.pressure.PendingCount++
			if awaits(e) {
				scan.forYou++
			}
			continue
		}
		served[e.ID] = true
		scan.total++
		if e.VerificationMode != "" {
			scan.modes[e.VerificationMode]++
		}
		if e.Tier == govstore.TierCore {
			core++
		}
	}
	if scan.total > 0 {
		scan.pressure.CorePct = core * 100 / scan.total
	}
	root := contextRoot(dataDir, workspaceID)
	var staleNow []string
	for i := range checkedEntries {
		computeStaleness(root, &checkedEntries[i])
		if checkedEntries[i].Stale {
			scan.stale++
			staleNow = append(staleNow, checkedEntries[i].ID)
		}
	}
	scan.checked, scan.complete = len(checkedEntries), len(checkedEntries) == baselined
	if since >= 0 {
		scan.delta = events.delta(since, scan.revision, served, staleNow)
	}
	return scan, nil
}

// --- onboarding (PAR-GOV-15) ----------------------------------------------------------

// Onboarding is ground's checklist for a workspace with no promoted memory: what to
// propose first.
type Onboarding struct {
	Needed  bool             `json:"needed"`
	Propose []OnboardingItem `json:"propose"`
}

// OnboardingItem is one category of memory worth proposing, with the kind to give it.
type OnboardingItem struct {
	Category string `json:"category"`
	Kind     string `json:"kind"`
	Prompt   string `json:"prompt"`
}

var onboardingChecklist = []OnboardingItem{
	{Category: "build_and_test", Kind: "procedure", Prompt: "the exact commands that build the project and run its tests"},
	{Category: "conventions", Kind: "convention", Prompt: "the style, structure and review rules a change must follow"},
	{Category: "task_completion", Kind: "workflow", Prompt: "what must hold before a task counts as done"},
	{Category: "module_map", Kind: "project_knowledge", Prompt: "the main modules, what each owns and where to start reading"},
}

// --- since_revision (PAR-HAR-03) -------------------------------------------------------

// MemoryDelta is what changed in served memory after Since, up to Revision: entry ids
// only (ground carries no agent-written text), newest change first within each list.
// Each entry is listed once, resolved against the served state at Revision ("newer
// wins"): an entry served now that was not served at Since is added; one served at both
// is stale when its newest drift signal says so, else changed; one served at Since and
// not now is superseded when another entry replaced it, else removed. An entry that was
// neither served at Since nor now is not listed.
type MemoryDelta struct {
	Since      int64              `json:"since"`
	Revision   int64              `json:"revision"`
	Added      []string           `json:"added"`
	Changed    []string           `json:"changed"`
	Superseded []SupersededMemory `json:"superseded"`
	Stale      []string           `json:"stale"`
	Removed    []string           `json:"removed"`
	// Truncated: more events followed than one call reads; Revision is the last one read,
	// so call again with since_revision=Revision.
	Truncated bool `json:"truncated,omitempty"`
}

// SupersededMemory is an entry another one replaced.
type SupersededMemory struct {
	ID string `json:"id"`
	By string `json:"by,omitempty"`
}

// deltaChange folds one entry's events after since.
type deltaChange struct {
	seq     int64 // the newest event, for ordering
	touched bool  // an event was folded
	// firstTransition is what the first serve/remove-type event folded for this entry
	// did to served memory ("serve" or "remove"); edit-type events (tier_change,
	// classify, revision_accepted, expiry) never set it, so they cannot hide whether
	// the entry was served at since. Empty means no serve/remove event fell in the
	// window, so the served-at-since state equals the served-now state.
	firstTransition string
	// bornInWindow: the entry's own propose or import event (its first event, ever) fell
	// inside the window, so it cannot have been served at since regardless of what a
	// later serve/remove-type event in the window looks like.
	bornInWindow bool
	stale        bool   // the newest drift signal flags it stale
	by           string // the entry that superseded it
}

// servedAtSince reports whether the entry was part of served memory immediately after
// since, before any event in the window: an entry created inside the window was not
// served at since; otherwise a serve-type event first means it was not served yet; a
// remove-type event first means it still was; with neither, the served-at-since state
// matches whether it is served now.
func (c *deltaChange) servedAtSince(servedNow bool) bool {
	if c.bornInWindow {
		return false
	}
	switch c.firstTransition {
	case "serve":
		return false
	case "remove":
		return true
	default:
		return servedNow
	}
}

// Effects of the history's event types on served memory. A type missing here (a vote,
// a feedback outcome, an anchor edit) changes nothing an agent was served.
var (
	deltaServe = func(c *deltaChange, _ govstore.Event) {
		if c.firstTransition == "" {
			c.firstTransition = "serve"
		}
	}
	deltaEdit   = func(*deltaChange, govstore.Event) {}
	deltaRemove = func(c *deltaChange, _ govstore.Event) {
		if c.firstTransition == "" {
			c.firstTransition = "remove"
		}
	}
	deltaBorn    = func(c *deltaChange, _ govstore.Event) { c.bornInWindow = true }
	deltaEffects = map[string]func(*deltaChange, govstore.Event){
		govstore.EventPropose:          deltaBorn,
		govstore.EventImport:           deltaBorn,
		govstore.EventPromote:          deltaServe,
		govstore.EventRestore:          deltaServe, // from archived; a restore that demotes is followed by a demote
		govstore.EventRevisionAccepted: deltaEdit,
		govstore.EventTierChange:       deltaEdit,
		govstore.EventClassify:         deltaEdit,
		govstore.EventExpiry:           deltaEdit,
		govstore.EventDriftCleared:     func(c *deltaChange, _ govstore.Event) { c.stale = false },
		govstore.EventStaleObserved:    func(c *deltaChange, _ govstore.Event) { c.stale = true },
		govstore.EventDemote:           deltaRemove,
		govstore.EventRetract:          deltaRemove,
		govstore.EventArchive:          deltaRemove,
		govstore.EventMerge:            deltaRemove,
		govstore.EventPurge:            deltaRemove,
		govstore.EventSupersede: func(c *deltaChange, ev govstore.Event) {
			var data struct {
				SupersededBy string `json:"superseded_by"`
			}
			// the replacing entry's own supersede event names what it replaced; its
			// promotion already says it was added
			if json.Unmarshal(ev.Data, &data) == nil && data.SupersededBy != "" {
				c.by = data.SupersededBy
				if c.firstTransition == "" {
					c.firstTransition = "remove"
				}
			}
		},
	}
)

// deltaEventTypes are the event types readDeltaEvents asks for.
var deltaEventTypes = func() []string {
	out := make([]string, 0, len(deltaEffects))
	for t := range deltaEffects {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}()

// maxDeltaEvents bounds the events one ground call reads for its delta.
const maxDeltaEvents = 1000

// deltaEvents is the history after since folded per entry.
type deltaEvents struct {
	byEntry   map[string]*deltaChange
	lastSeq   int64
	truncated bool
}

// readDeltaEvents reads the workspace's events after since that can change served
// memory, at most maxDeltaEvents, and folds them per entry.
func readDeltaEvents(ctx context.Context, r govstore.Reader, workspaceID string, since int64) (deltaEvents, error) {
	evs, err := r.ListEvents(ctx, govstore.EventFilter{WorkspaceID: workspaceID, Types: deltaEventTypes, AfterSeq: since,
		Limit: maxDeltaEvents + 1})
	if err != nil {
		return deltaEvents{}, err
	}
	out := deltaEvents{byEntry: map[string]*deltaChange{}}
	if len(evs) > maxDeltaEvents {
		evs, out.truncated = evs[:maxDeltaEvents], true
	}
	for _, ev := range evs {
		out.lastSeq = ev.Seq
		if ev.EntryID == "" {
			continue
		}
		c := out.byEntry[ev.EntryID]
		if c == nil {
			c = &deltaChange{}
			out.byEntry[ev.EntryID] = c
		}
		deltaEffects[ev.Type](c, ev)
		c.touched, c.seq = true, ev.Seq
	}
	return out, nil
}

// delta resolves the folded changes against the served state at revision: served lists
// the entries served then, staleNow the ones ground's drift check found stale (file
// drift is no memory event, so a served entry stale now is listed stale unless a newer
// change lists it).
func (d deltaEvents) delta(since, revision int64, served map[string]bool, staleNow []string) *MemoryDelta {
	out := &MemoryDelta{Since: since, Revision: revision, Added: []string{}, Changed: []string{},
		Superseded: []SupersededMemory{}, Stale: []string{}, Removed: []string{}, Truncated: d.truncated}
	if d.truncated {
		out.Revision = d.lastSeq
	}
	ids := make([]string, 0, len(d.byEntry))
	for id := range d.byEntry {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return d.byEntry[ids[a]].seq > d.byEntry[ids[b]].seq })
	driftNow := map[string]bool{}
	for _, id := range staleNow {
		driftNow[id] = true
	}
	for _, id := range ids {
		c := d.byEntry[id]
		sas := c.servedAtSince(served[id]) // served immediately after since
		switch {
		case !served[id] && !sas:
			// served neither at since nor now: nothing an agent saw changed
		case !served[id] && c.by != "":
			out.Superseded = append(out.Superseded, SupersededMemory{ID: id, By: c.by})
		case !served[id]:
			out.Removed = append(out.Removed, id)
		case !sas:
			out.Added = append(out.Added, id)
		case c.stale || driftNow[id]:
			out.Stale = append(out.Stale, id)
		default:
			out.Changed = append(out.Changed, id)
		}
	}
	for _, id := range staleNow {
		if served[id] && d.byEntry[id] == nil {
			out.Stale = append(out.Stale, id)
		}
	}
	return out
}

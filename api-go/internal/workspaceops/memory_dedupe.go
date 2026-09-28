package workspaceops

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"xmustard/api-go/internal/dedupe"
	"xmustard/api-go/internal/govstore"
)

// Duplicate detection at propose time (WS-27, PAR-GOV-03). A proposal is normalized
// and profiled (internal/dedupe): its exact key and, past the entropy gate, its MinHash
// band keys look candidates up in the store's postings, and the entries sharing its
// anchors or its claims' subjects join them as neighbours. Every candidate's served
// content is compared: an exact restatement of served memory is recorded as a
// corroboration of it instead of a new entry, and exact, near (Jaccard >= 0.9) and
// similar (>= 0.5) matches come back as possible_duplicates for the agent to act on.
// The neighbours are what the proposal is checked for conflicts against
// (memory_conflicts.go). Nothing is merged or invalidated here: verify(duplicate_of)
// and remember(supersedes) are the agent's decisions (PAR-GOV-21).

// Review bounds: the entries one proposal is compared with, the duplicates and conflicts
// it reports, and the older entries whose postings each proposal fills in.
const (
	maxConflictSides      = 32
	maxPossibleDuplicates = 5
	maxPossibleConflicts  = 16
	dedupeBackfillBatch   = 256
	similarThreshold      = 0.5
)

// Duplicate match classes.
const (
	MatchExact   = "exact"
	MatchNear    = "near"
	MatchSimilar = "similar"
)

// PossibleDuplicate is an active entry a proposal restates, fully or in part.
type PossibleDuplicate struct {
	ID         string  `json:"id"`
	Title      string  `json:"title,omitempty"`
	Similarity float64 `json:"similarity"`
	Match      string  `json:"match"`
	// State is served or pending; Quarantine is the entry's quarantine, when it has one.
	State      string `json:"state"`
	Quarantine string `json:"quarantine,omitempty"`
}

// Write outcomes of a proposal.
const (
	OutcomeCreated      = "created"
	OutcomeCorroborated = "corroborated"
)

// duplicateMatch classifies a similarity, the first class it reaches.
func duplicateMatch(exact bool, sim float64) (string, bool) {
	switch {
	case exact:
		return MatchExact, true
	case sim >= dedupe.FuzzyThreshold:
		return MatchNear, true
	case sim >= similarThreshold:
		return MatchSimilar, true
	}
	return "", false
}

// reviewCandidate is one active entry a proposal is compared with.
type reviewCandidate struct {
	entry  govstore.Entry
	view   ContextEntry
	side   *conflictSide
	served bool
	dup    *PossibleDuplicate
}

// proposalReview is a proposal checked against the workspace before it is written.
type proposalReview struct {
	candidates []*reviewCandidate
	duplicates []PossibleDuplicate
	// corroborate is a served entry the proposal restates exactly, or "".
	corroborate string
	// notes say why an exact duplicate of served memory is created anyway.
	notes []string
}

// reviewInput is what the review reads of a proposal.
type reviewInput struct {
	workspaceID, content string
	// scope and scopeKey are the proposal's scope: only entries of the same scope are
	// its duplicates or neighbours (every proposal is workspace-scoped until WS-29).
	scope, scopeKey string
	declared        []string
	anchors         []govstore.AnchorInput
	claims          []MemoryClaim
	// intent: the proposal names supersedes, claims or relations, so it is more than
	// a restatement.
	intent bool
	// the classification and expiry the proposal asks for
	kind, topic, expires string
	tags                 []string
	quarantine           string
	// reviewer: the proposer may read unverified memory (verifier or human-approver).
	// Pending entries are its duplicates and neighbours only then, so a proposal is
	// never an oracle for pending text its author may not read.
	reviewer bool
}

// classifiedOtherwise reports whether the proposal asks for a classification or expiry
// the entry does not have.
func (in reviewInput) classifiedOtherwise(e govstore.Entry) bool {
	return in.expires != "" || (in.kind != "" && in.kind != e.Kind) || (in.topic != "" && in.topic != e.Topic) ||
		(len(in.tags) > 0 && !slices.Equal(slices.Sorted(slices.Values(in.tags)), slices.Sorted(slices.Values(e.Tags))))
}

// corroborationBlock is a reason an exact duplicate of served memory is created as its
// own entry anyway.
type corroborationBlock struct {
	why   string
	holds func(in reviewInput, c *reviewCandidate) bool
}

// corroborationBlocks are checked in order; the first that holds is reported.
var corroborationBlocks = []corroborationBlock{
	{"the proposal names supersedes, claims or relations",
		func(in reviewInput, _ *reviewCandidate) bool { return in.intent }},
	{"the proposal asks for another kind, topic, tags or expiry",
		func(in reviewInput, c *reviewCandidate) bool { return in.classifiedOtherwise(c.entry) }},
	{"its declared paths differ", func(in reviewInput, c *reviewCandidate) bool {
		return len(in.declared) > 0 && len(c.view.Paths) > 0 &&
			!slices.ContainsFunc(in.declared, func(p string) bool { return slices.Contains(c.view.Paths, p) })
	}},
}

// reviewProposal finds a proposal's duplicates and conflict neighbours in tx.
func reviewProposal(ctx context.Context, tx govstore.Tx, in reviewInput) (proposalReview, error) {
	if _, err := tx.BackfillDedupeKeys(ctx, dedupeBackfillBatch); err != nil {
		return proposalReview{}, err
	}
	profile := dedupe.NewProfile(in.content)
	hits, err := tx.DedupeCandidates(ctx, govstore.DedupeQuery{WorkspaceID: in.workspaceID,
		Keys: dedupe.KeysOf(in.workspaceID, profile), Limit: maxConflictSides})
	if err != nil {
		return proposalReview{}, err
	}
	ids := make([]string, 0, maxConflictSides)
	for _, h := range hits {
		ids = append(ids, h.EntryID)
	}
	neighbours, err := anchorNeighbours(ctx, tx, in)
	if err != nil {
		return proposalReview{}, err
	}
	rivals, err := claimRivals(ctx, tx, in.workspaceID, in.claims)
	if err != nil {
		return proposalReview{}, err
	}
	for _, id := range slices.Concat(neighbours, rivals) {
		if len(ids) < maxConflictSides && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	var rv proposalReview
	if rv.candidates, err = loadReviewCandidates(ctx, tx, in, ids); err != nil {
		return proposalReview{}, err
	}
	for _, c := range rv.candidates {
		rv.classify(profile, c)
	}
	slices.SortStableFunc(rv.duplicates, compareDuplicates)
	rv.duplicates = rv.duplicates[:min(len(rv.duplicates), maxPossibleDuplicates)]
	rv.pickCorroboration(in)
	return rv, nil
}

// compareDuplicates orders the most similar first, served before pending, then by id.
func compareDuplicates(a, b PossibleDuplicate) int {
	// "served" sorts after "pending", so b's state first puts served first
	return cmp.Or(cmp.Compare(b.Similarity, a.Similarity), strings.Compare(b.State, a.State), strings.Compare(a.ID, b.ID))
}

// classify compares one candidate's served content with the proposal. The entropy gate
// keeps short generic text out of fuzzy matching: only an exact restatement of it
// matches.
func (rv *proposalReview) classify(p dedupe.Profile, c *reviewCandidate) {
	cp := dedupe.NewProfile(c.view.Content)
	exact := cp.Exact == p.Exact
	sim := 0.0
	switch {
	case exact:
		sim = 1
	case p.Gated && cp.Gated:
		sim = dedupe.Similarity(p, cp)
	}
	match, ok := duplicateMatch(exact, sim)
	if !ok {
		return
	}
	state := govstore.RankPending
	if c.served {
		state = govstore.RankServed
	}
	c.dup = &PossibleDuplicate{ID: c.entry.ID, Title: c.view.Title, Similarity: round3(sim), Match: match, State: state,
		Quarantine: c.view.Quarantine}
	rv.duplicates = append(rv.duplicates, *c.dup)
}

// pickCorroboration chooses the served entry the proposal restates exactly, if one may
// take it as a corroboration, and notes why the others may not.
func (rv *proposalReview) pickCorroboration(in reviewInput) {
	for _, d := range rv.duplicates {
		c := rv.candidate(d.ID)
		if d.Match != MatchExact || !c.served {
			continue // only served memory takes a corroboration
		}
		// A write citing an untrusted capture never corroborates clean memory: that
		// would launder the capture into it, or let a capture quarantine it. Dedupe
		// never launders quarantine; the write lands as its own quarantined entry.
		if in.quarantine != "" && c.view.Quarantine == "" {
			rv.notes = append(rv.notes, fmt.Sprintf("an exact duplicate of served entry %s was not corroborated: the "+
				"proposal is quarantined (%s) and %s is not", d.ID, in.quarantine, d.ID))
			continue
		}
		if i := slices.IndexFunc(corroborationBlocks, func(b corroborationBlock) bool { return b.holds(in, c) }); i >= 0 {
			rv.notes = append(rv.notes, fmt.Sprintf("an exact duplicate of served entry %s was not corroborated: %s",
				d.ID, corroborationBlocks[i].why))
			continue
		}
		rv.corroborate = d.ID
		return
	}
}

func (rv *proposalReview) candidate(id string) *reviewCandidate {
	for _, c := range rv.candidates {
		if c.entry.ID == id {
			return c
		}
	}
	return nil
}

// anchorNeighbours are the newest active entries sharing one of the proposal's anchors.
func anchorNeighbours(ctx context.Context, r govstore.Reader, in reviewInput) ([]string, error) {
	byKind := map[string][]string{}
	for _, p := range in.declared {
		byKind[govstore.AnchorPath] = append(byKind[govstore.AnchorPath], p)
	}
	for _, a := range in.anchors {
		for _, kind := range sameAnchorKinds(a.Kind) {
			byKind[kind] = append(byKind[kind], a.Value)
		}
	}
	var ids []string
	for _, kind := range slices.Sorted(maps.Keys(byKind)) {
		hits, err := r.EntriesByAnchor(ctx, govstore.AnchorQuery{WorkspaceID: in.workspaceID, Kind: kind,
			Values: byKind[kind], Limit: maxConflictSides, NewestFirst: true})
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			if !slices.Contains(ids, h.EntryID) {
				ids = append(ids, h.EntryID)
			}
		}
	}
	return ids, nil
}

// loadReviewCandidates reads the active entries of the proposal's workspace and scope
// among ids that the proposer may read, with their served content, anchors and claims.
// An entry whose content is withheld is skipped.
func loadReviewCandidates(ctx context.Context, r govstore.Reader, in reviewInput, ids []string) ([]*reviewCandidate, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	contents, err := r.EntryContents(ctx, ids)
	if err != nil {
		return nil, err
	}
	anchors, err := r.AnchorsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	claims, err := r.ClaimsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []*reviewCandidate
	for _, id := range ids {
		e, err := r.GetEntry(ctx, id)
		if err != nil {
			return nil, err
		}
		content, ok := contents[id]
		inScope := e.WorkspaceID == in.workspaceID && e.Scope == in.scope && e.ScopeKey == in.scopeKey
		readable := e.Served(now) || in.reviewer
		if !inScope || !readable || e.Lifecycle != govstore.LifecycleActive || !ok || content.Withheld != "" {
			continue
		}
		view := contextEntryFrom(e, nil, anchors[id])
		view.Content = content.Content
		out = append(out, &reviewCandidate{entry: e, view: view, served: e.Served(now),
			side: newConflictSide(view, claims[id], anchorRefs(anchors[id]))})
	}
	return out, nil
}

func anchorRefs(anchors []govstore.Anchor) []govstore.AnchorRef {
	out := make([]govstore.AnchorRef, len(anchors))
	for i, a := range anchors {
		out[i] = govstore.AnchorRef{Kind: a.Kind, Value: a.Value}
	}
	return out
}

// conflictsWith reports the proposal's conflict candidates among the reviewed entries,
// highest tier first, and the served entries it may be meant to replace: its near
// duplicates and its T1 and T2 conflicts that it does not already supersede.
func (rv *proposalReview) conflictsWith(self *conflictSide, supersedes []string) ([]MemoryConflict, []string) {
	var conflicts []MemoryConflict
	var suggest []string
	for _, c := range rv.candidates {
		if c.entry.ID == self.id {
			continue
		}
		conflict, ok := pairConflict(self, c.side, conflictTiers)
		if ok {
			conflicts = append(conflicts, conflict)
		}
		replaceable := (ok && conflict.Tier <= 2) || (c.dup != nil && c.dup.Match == MatchNear)
		if replaceable && c.served && !slices.Contains(supersedes, c.entry.ID) && len(suggest) < maxSupersedes {
			suggest = append(suggest, c.entry.ID)
		}
	}
	slices.SortStableFunc(conflicts, func(a, b MemoryConflict) int {
		return cmp.Or(cmp.Compare(a.Tier, b.Tier), strings.Compare(a.EntryIDs[1], b.EntryIDs[1]))
	})
	slices.Sort(suggest)
	return conflicts[:min(len(conflicts), maxPossibleConflicts)], suggest
}

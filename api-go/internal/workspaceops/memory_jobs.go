package workspaceops

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"xmustard/api-go/internal/govstore"
)

// The consolidation preparation layer (WS-32a, PAR-GOV-13). govstore/jobs.go holds the
// queue's storage half: the job table, its closed kind set, the compare-and-set lease and
// the per-source watermark. This file is the half above it. A pass reads the memory store,
// derives candidate work deterministically, enqueues each candidate under its conflict
// signature, and advances the source watermark only when the pass finished.
//
// Nothing here reasons. xMustard prepares candidates and the connected agent decides
// (PAR-GOV-21), so this file writes job and watermark rows only: it never merges,
// supersedes, invalidates or promotes a memory. A candidate is a question, not a verdict.

// Preparation bounds. The entries one pass reads, the anchors it reads per entry, the
// clusters it derives and the jobs it enqueues are all bounded, so a large store costs a
// bounded pass rather than an unbounded one.
const (
	// consolidationEntries is how many entries one pass reflects over. The store's own
	// ListEntries clamps a limit to its maxListLimit of 1,000, so a bound above that would
	// be a number this pass cannot reach: the watermark would record the clamp, not the
	// store. The ceiling is named here so the two cannot drift apart silently.
	consolidationEntries = 1000
	// consolidationCandidatesPerPass bounds the candidates one pass derives. It is below the
	// entry bound divided by the smallest cluster (two entries), so a store can hold more
	// work than one pass takes and the cut is reachable: what it leaves out is counted
	// (Skipped) rather than dropped in silence.
	consolidationCandidatesPerPass = 400
	// consolidationJobsPerRun bounds the jobs one pass enqueues. It matches the candidate
	// bound, so a pass that took candidates queues all of them.
	consolidationJobsPerRun = consolidationCandidatesPerPass
	// consolidationMinCluster is the smallest cluster worth a job: two memories on one
	// anchor is a question, one memory is not.
	consolidationMinCluster = 2
	// consolidationMaxCluster bounds one cluster's candidate ids, so a single popular path
	// cannot produce a job naming thousands of memories.
	consolidationMaxCluster = 50
	// ageingProposalBefore is how long an unverified proposal waits before it is worth a
	// job. It is a duration in days, read from the entry's created_at.
	ageingProposalBefore = 14
)

// passInput is one consolidation pass.
type passInput struct {
	// Source is the watermark's source: what the pass reflects over.
	Source string
	Actor  govstore.Actor
	// Err is the failure the pass reports. It exists so a test can run a failing pass
	// through the same path as a succeeding one; no production caller sets it.
	Err error
}

// ConsolidationPass is the result of one pass.
type ConsolidationPass struct {
	Jobs []govstore.Job `json:"jobs"`
	// Seen is how many entries the pass reflected over: the watermark's reflected count.
	Seen int `json:"seen"`
	// Skipped is how many candidates the per-run bound left out; a later pass takes them.
	Skipped int `json:"skipped"`
}

// RunConsolidationPass prepares the consolidation candidates for one source and advances
// that source's watermark on success. The pass is idempotent: a candidate whose signature
// already has a job is not enqueued again, so running it twice changes nothing.
//
// A failing pass stamps the watermark's start and returns its error with the watermark
// where it was (PAR-GOV-13: the watermark advances only on success).
func RunConsolidationPass(ctx context.Context, dataDir, workspaceID string, in passInput) (ConsolidationPass, error) {
	var res ConsolidationPass
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return res, err
	}
	if err := validConsolidationSource(in.Source); err != nil {
		return res, err
	}
	// The start stamp is what a reached-the-store pass looks like. It never advances the
	// watermark, so a pass that dies mid-way leaves the next one exactly where this found
	// the source, and last_started_at shows the attempt happened.
	startErr := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		_, err := tx.StartWatermark(ctx, workspaceID, in.Source, in.Actor)
		return err
	})
	if in.Err != nil {
		if startErr != nil {
			return res, startErr
		}
		return res, in.Err
	}
	if startErr != nil {
		return res, startErr
	}

	candidates, seen, skipped, err := consolidationCandidates(ctx, dataDir, workspaceID)
	if err != nil {
		return res, err
	}
	res.Seen = seen

	if err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		for _, c := range candidates {
			if len(res.Jobs) >= consolidationJobsPerRun {
				res.Skipped++
				continue
			}
			job, _, err := tx.EnqueueJob(ctx, c, in.Actor)
			if err != nil {
				return fmt.Errorf("enqueue %s: %w", c.ConflictSignature, err)
			}
			res.Jobs = append(res.Jobs, job)
		}
		return nil
	}); err != nil {
		return res, err
	}
	res.Skipped += skipped

	// The watermark advances only here, once every candidate is queued, and it records how
	// far this pass reflected. ExpectVersion is re-read inside the transaction, so two
	// passes that overlap cannot both advance the same source: the second sees a moved
	// version and is refused.
	if err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		cur, err := tx.GetWatermark(ctx, workspaceID, in.Source)
		if err != nil {
			return err
		}
		_, err = tx.AdvanceWatermark(ctx, govstore.WatermarkAdvance{WorkspaceID: workspaceID, Source: in.Source,
			ExpectVersion: cur.Version, ReflectedThrough: in.Source, TotalCount: int64(seen),
			ReflectedCount: int64(seen)}, in.Actor)
		return err
	}); err != nil {
		return res, err
	}
	return res, nil
}

// PrepareConsolidationJobs derives the candidates without queueing them, for a caller that
// wants the work before it is queued. RunConsolidationPass is what the loop calls.
func PrepareConsolidationJobs(ctx context.Context, dataDir, workspaceID string) ([]govstore.JobInput, error) {
	candidates, _, _, err := consolidationCandidates(ctx, dataDir, workspaceID)
	return candidates, err
}

// jobInstructions are what the agent reads to do the job. Each asks for a judgement and
// names the decision; none states one, because a prepared job is a question (PAR-GOV-21).
var jobInstructions = map[string]string{
	govstore.JobDedupeCluster:     "These memories share one anchor. Read each one and decide whether any restates another; if so propose the merge with remember(supersedes=) and name the keeper. If they are genuinely different, do nothing.",
	govstore.JobStaleAnchor:       "This memory's anchor drifted. Read the memory and the code at that anchor now, then decide whether the memory is still true: propose a revision with remember, or retract it.",
	govstore.JobOrphanedAnchor:    "This memory is anchored to something that no longer exists. Decide whether it still holds without it: revise it with remember, or retract it.",
	govstore.JobSupersessionChain: "These memories cover one subject in sequence. Read them oldest first and decide whether a newer one replaces an older one; propose it with remember(supersedes=).",
	govstore.JobAgeingProposal:    "This proposal has waited unverified. Read it and decide whether it still deserves to be memory: verify or reject it, with distinct principals.",
}

// consolidationCandidate is one kind's candidates: the generator is a pure function of the
// snapshot (no store access, no clock), so preparation is reproducible.
type consolidationCandidate struct {
	Kind      string
	Signature string
	Members   []string
}

// jobGenerators is the closed set of candidate kinds this pass derives. Adding a kind is
// one line here and one instructions entry.
var jobGenerators = map[string]func(consolidationSnapshot) []consolidationCandidate{
	govstore.JobDedupeCluster:     dedupeClusterCandidates,
	govstore.JobStaleAnchor:       staleAnchorCandidates,
	govstore.JobOrphanedAnchor:    orphanedAnchorCandidates,
	govstore.JobSupersessionChain: supersessionChainCandidates,
	govstore.JobAgeingProposal:    ageingProposalCandidates,
}

// jobKindOrder is the order kinds are prepared in, so the prepared sequence is stable.
var jobKindOrder = []string{
	govstore.JobDedupeCluster, govstore.JobStaleAnchor, govstore.JobOrphanedAnchor,
	govstore.JobSupersessionChain, govstore.JobAgeingProposal,
}

// consolidationSnapshot is the read view the generators work from: every entry of the
// workspace with its anchors, both sorted by entry id.
type consolidationSnapshot struct {
	Entries []govstore.Entry
	Anchors map[string][]govstore.Anchor
}

// now is the pass's clock, replaced in tests so the ageing generator is deterministic.
var consolidationNow = time.Now

// consolidationCandidates derives the candidate jobs from the store in a deterministic
// order: kinds run in jobKindOrder and each kind's candidates are sorted by signature, so
// two passes over one snapshot produce the same sequence.
// The second return is how many entries the pass reflected over; the third is how many
// candidates the per-run bound left out, so a caller can see that work remains.
func consolidationCandidates(ctx context.Context, dataDir, workspaceID string) ([]govstore.JobInput, int, int, error) {
	var snap consolidationSnapshot
	seen := 0
	if err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		entries, err := r.ListEntries(ctx, govstore.EntryFilter{WorkspaceID: workspaceID, Lifecycles: []string{"*"}, Limit: consolidationEntries})
		if err != nil {
			return err
		}
		seen = len(entries)
		slices.SortFunc(entries, func(a, b govstore.Entry) int { return strings.Compare(a.ID, b.ID) })
		ids := make([]string, 0, len(entries))
		for _, e := range entries {
			ids = append(ids, e.ID)
		}
		anchors, err := r.AnchorsFor(ctx, ids)
		if err != nil {
			return err
		}
		snap = consolidationSnapshot{Entries: entries, Anchors: anchors}
		return nil
	}); err != nil {
		return nil, 0, 0, err
	}

	var out []govstore.JobInput
	skipped := 0
	for _, kind := range jobKindOrder {
		gen, ok := jobGenerators[kind]
		if !ok {
			// Unreachable while jobKindOrder and jobGenerators are edited together; a
			// missing generator is a programming error, not a silently skipped kind.
			return nil, 0, 0, fmt.Errorf("consolidation: no generator for kind %q", kind)
		}
		candidates := gen(snap)
		// Kinds run in order and each kind's candidates are sorted by signature, so the
		// prepared sequence is a function of the snapshot alone. The bound is applied after
		// the sort, which is what makes the cut deterministic: the same store always leaves
		// the same tail for a later pass.
		slices.SortFunc(candidates, func(a, b consolidationCandidate) int { return strings.Compare(a.Signature, b.Signature) })
		for _, c := range candidates {
			if len(out) >= consolidationCandidatesPerPass {
				// Counted, not dropped in silence: a pass that quietly left work out would
				// read as a pass that found nothing more to do.
				skipped++
				continue
			}
			out = append(out, govstore.JobInput{
				WorkspaceID:       workspaceID,
				Kind:              kind,
				ConflictSignature: c.Signature,
				CandidateIDs:      c.Members,
				Instructions:      jobInstructions[kind],
			})
		}
	}
	return out, seen, skipped, nil
}

// dedupeClusterCandidates asks whether two or more memories on one anchor restate each
// other. Every memory anchored on a shared value joins its cluster, and the agent judges.
func dedupeClusterCandidates(snap consolidationSnapshot) []consolidationCandidate {
	byAnchor := map[string][]string{}
	for _, e := range snap.Entries {
		if !consolidationEntryLive(e) {
			continue
		}
		for _, a := range snap.Anchors[e.ID] {
			byAnchor[a.Kind+"\x00"+a.Value] = append(byAnchor[a.Kind+"\x00"+a.Value], e.ID)
		}
	}
	var clusters [][]string
	for _, members := range byAnchor {
		if len(members) < consolidationMinCluster {
			continue
		}
		slices.Sort(members)
		if len(members) > consolidationMaxCluster {
			members = members[:consolidationMaxCluster]
		}
		clusters = append(clusters, members)
	}
	// No sort here: consolidationCandidates sorts each kind's candidates by signature, so
	// sorting the clusters first would be a second order the caller overwrites. Mutation M12
	// deleted this line and every test still passed, which is the measure that it was dead.
	out := make([]consolidationCandidate, 0, len(clusters))
	for _, members := range clusters {
		out = append(out, consolidationCandidate{Signature: clusterSignature(govstore.JobDedupeCluster, members), Members: members})
	}
	return out
}

// staleAnchorCandidates asks about a memory whose anchor drifted: the anchor is stale and
// the entry still lives.
func staleAnchorCandidates(snap consolidationSnapshot) []consolidationCandidate {
	var out []consolidationCandidate
	for _, e := range snap.Entries {
		if !consolidationEntryLive(e) {
			continue
		}
		stale := staleAnchorRefs(snap.Anchors[e.ID])
		if len(stale) == 0 {
			continue
		}
		out = append(out, consolidationCandidate{Signature: clusterSignature(govstore.JobStaleAnchor, stale), Members: []string{e.ID}})
	}
	return out
}

// orphanedAnchorCandidates asks about a memory whose anchor no longer resolves: its
// baseline recorded the anchor as missing, or its anchor is not in the workspace's tree.
func orphanedAnchorCandidates(snap consolidationSnapshot) []consolidationCandidate {
	var out []consolidationCandidate
	for _, e := range snap.Entries {
		if !consolidationEntryLive(e) {
			continue
		}
		var gone []string
		for _, a := range snap.Anchors[e.ID] {
			if a.BaselineState == govstore.BaselineMissing {
				gone = append(gone, a.Kind+":"+a.Value)
			}
		}
		if len(gone) == 0 {
			continue
		}
		out = append(out, consolidationCandidate{Signature: clusterSignature(govstore.JobOrphanedAnchor, gone), Members: []string{e.ID}})
	}
	return out
}

// supersessionChainCandidates asks about a subject several memories cover in sequence. The
// chain is the live entries sharing one identifier anchor that more than one of them
// asserts, which is the shape an agent resolves with remember(supersedes=).
func supersessionChainCandidates(snap consolidationSnapshot) []consolidationCandidate {
	bySubject := map[string][]string{}
	for _, e := range snap.Entries {
		if !consolidationEntryLive(e) {
			continue
		}
		for _, a := range snap.Anchors[e.ID] {
			if a.Kind != govstore.AnchorIdentifier {
				continue
			}
			bySubject[a.Value] = append(bySubject[a.Value], e.ID)
		}
	}
	var out []consolidationCandidate
	for _, members := range bySubject {
		if len(members) < consolidationMinCluster {
			continue
		}
		slices.Sort(members)
		if len(members) > consolidationMaxCluster {
			members = members[:consolidationMaxCluster]
		}
		out = append(out, consolidationCandidate{Signature: clusterSignature(govstore.JobSupersessionChain, members), Members: members})
	}
	return out
}

// ageingProposalCandidates asks about a proposal that has waited unverified past
// ageingProposalBefore days. It prepares the question; it never promotes or rejects.
func ageingProposalCandidates(snap consolidationSnapshot) []consolidationCandidate {
	cutoff := consolidationNow().Add(-ageingProposalBefore * 24 * time.Hour)
	var out []consolidationCandidate
	for _, e := range snap.Entries {
		if !consolidationEntryLive(e) || e.Promoted || e.Status != govstore.StatusPending {
			continue
		}
		created, ok := parseConsolidationTime(e.CreatedAt)
		if !ok || created.After(cutoff) {
			continue
		}
		out = append(out, consolidationCandidate{Signature: clusterSignature(govstore.JobAgeingProposal, []string{e.ID}), Members: []string{e.ID}})
	}
	return out
}

// consolidationEntryLive is whether an entry is worth preparing work about: it is active
// and not merged away. A superseded, retracted, archived or purged entry needs none, and
// an expired one is still live (GOV-12 hides, it does not delete).
func consolidationEntryLive(e govstore.Entry) bool {
	return e.Lifecycle == govstore.LifecycleActive
}

// staleAnchorRefs names an entry's stale anchors, one per value.
func staleAnchorRefs(anchors []govstore.Anchor) []string {
	var out []string
	for _, a := range anchors {
		if a.StaleSince != "" {
			out = append(out, a.Kind+":"+a.Value)
		}
	}
	slices.Sort(out)
	return out
}

// clusterSignature identifies the work a candidate is: it names the kind and every member,
// so two candidates that differ in any one member are different work. The order the members
// arrive in does not change it.
func clusterSignature(kind string, members []string) string {
	sorted := slices.Clone(members)
	slices.Sort(sorted)
	return kind + ":" + strings.Join(sorted, ",")
}

func validConsolidationSource(s string) error {
	if strings.TrimSpace(s) == "" || strings.ContainsAny(s, "\x00\r\n") {
		return fmt.Errorf("%w: consolidation source %q", govstore.ErrInvalid, s)
	}
	return nil
}

// parseConsolidationTime reads an entry timestamp. An unparseable stamp is not a candidate
// rather than a zero time, which would read as infinitely old.
func parseConsolidationTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

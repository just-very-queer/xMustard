package workspaceops

import (
	"context"
	"fmt"
	"slices"
	"strconv"
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
	// maxSignatureLen is govstore's own limit on a conflict signature; a signature at or
	// under it is kept in its readable form, and one longer is shortened.
	maxSignatureLen = 256
	// signatureDigestLen is how much of the digest a shortened signature keeps.
	signatureDigestLen = 32
	// consolidationQueuedSignatures bounds how many queued signatures one pass compares
	// against. It is well above the job bound, because a signature set read short would call
	// a queued candidate absent and hand it to EnqueueJob again.
	consolidationQueuedSignatures = 20000
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
	// consolidationAnchors bounds the anchor rows one pass reads across all its entries.
	// The store holds no per-entry anchor cap, so without this a pass would read an
	// unbounded anchor set and the budget the section claims would be fiction. Anchors read
	// past it are counted (AnchorsRead) and their entries contribute no candidates.
	consolidationAnchors = 20000
	// consolidationPage bounds how many entries one pass reflects over, and
	// consolidationPages bounds how many pages of those it may walk in one pass. A workspace
	// with more entries than this needs a later pass, which resumes at the cursor this one
	// recorded: without a cursor, every pass would re-read the first page forever.
	consolidationPage  = 500
	consolidationPages = 4
	// consolidationClusterWindow bounds how far back a pass keeps entries when deciding which
	// memories share an anchor. It is larger than one pass reads, because a window equal to a
	// pass would leave a pair sharing an anchor candidates only when both fall inside the same
	// pass: the cursor resumes where the last pass stopped, so a pair either side of that point
	// is met by neither pass on its own.
	consolidationClusterWindow = consolidationPage * consolidationPages * 2
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

// ConsolidationPassInput is one consolidation pass.
type ConsolidationPassInput struct {
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
	// Cursor is the entry cursor the pass reached, recorded in the watermark so a later pass
	// resumes here rather than re-reading the first page.
	Cursor int64 `json:"cursor"`
	// Complete is whether the pass reached the end of the entry table. A source that has
	// caught up is scanned from the start on the next pass, so a memory added after the last
	// pass is still seen.
	Complete bool `json:"complete"`
	// Skipped is how many candidates the per-run bound left out; a later pass takes them.
	Skipped int `json:"skipped"`
}

// RunConsolidationPass prepares the consolidation candidates for one source and advances
// that source's watermark on success. The pass is idempotent: a candidate whose signature
// already has a job is not enqueued again, so running it twice changes nothing.
//
// A failing pass stamps the watermark's start and returns its error with the watermark
// where it was (PAR-GOV-13: the watermark advances only on success).
func RunConsolidationPass(ctx context.Context, dataDir, workspaceID string, in ConsolidationPassInput) (ConsolidationPass, error) {
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
	// the source, and last_started_at shows the attempt happened. Its version is kept: the
	// advance below compares against the version this pass started from, so a pass that
	// overlapped another is refused rather than silently merged over it.
	var startVersion, startReflected int64
	var startCursor int64
	startErr := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		w, err := tx.StartWatermark(ctx, workspaceID, in.Source, in.Actor)
		if err != nil {
			return err
		}
		startVersion, startReflected = w.Version, w.ReflectedCount
		startCursor = cursorOf(w.ReflectedThrough)
		return nil
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

	// A pass reads from the cursor the last one stopped at, then, when that reach holds
	// nothing the queue has not already, from the start again. The cursor is a bound on how
	// much one pass walks, not a promise to walk forward for ever: a workspace whose entries
	// all sit before the cursor would otherwise never be looked at again, which is exactly
	// the case after a signature is cancelled and freed. Two reads, the second only when the
	// first found no unqueued work, so a caught-up pass costs one scan rather than two.
	candidates, seen, skipped, cursor, complete, err := consolidationCandidates(ctx, dataDir, workspaceID, startCursor)
	if err != nil {
		return res, err
	}
	if complete && len(candidates) == 0 {
		candidates, seen, skipped, cursor, complete, err = consolidationCandidates(ctx, dataDir, workspaceID, 0)
		if err != nil {
			return res, err
		}
	}
	// Reading from the cursor can find candidates whose signatures the queue already holds:
	// they are work, so the read did not return empty, but the pass has nothing new to
	// queue. When that happens and the read reached the end of the table, the entries the
	// cursor sits past may hold the freed work, so the pass starts again from the beginning.
	free, err := unqueuedCandidates(ctx, dataDir, workspaceID, candidates)
	if err != nil {
		return res, err
	}
	// A pair either side of the cursor is met by neither pass alone, so the snapshot keeps the
	// entries the previous pass reached: the pass reads from the cursor back through the
	// window, which is what makes two memories sharing an anchor candidates for each other
	// however the boundary fell. Without this the window only bounds memory and buys nothing.
	if startCursor > 0 {
		back, _, _, _, _, err := consolidationCandidates(ctx, dataDir, workspaceID, max(startCursor-consolidationClusterWindow, 0))
		if err != nil {
			return res, err
		}
		candidates = mergeCandidates(back, candidates)
	}
	if complete && len(free) == 0 {
		fromStart, s2, k2, c2, comp2, err2 := consolidationCandidates(ctx, dataDir, workspaceID, 0)
		if err2 != nil {
			return res, err2
		}
		freeStart, err := unqueuedCandidates(ctx, dataDir, workspaceID, fromStart)
		if err != nil {
			return res, err
		}
		if len(freeStart) > 0 {
			candidates, seen, skipped, cursor, complete = fromStart, s2, k2, c2, comp2
		}
	}
	res.Seen = seen
	res.Cursor = cursor
	res.Complete = complete

	// Candidates whose signature has no job yet are taken first, and the per-pass bound is
	// applied after that split rather than before it. Cutting first would re-select the same
	// leading candidates every pass, so a store with more candidates than one pass takes would
	// starve the tail for ever however the queue is ordered afterwards: the omitted candidate
	// never reaches the comparison that would have rescued it.
	if err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		existing, err := queuedSignatures(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		pending := make([]govstore.JobInput, 0, len(candidates))
		var done []govstore.JobInput
		for _, c := range candidates {
			if existing[c.ConflictSignature] {
				done = append(done, c)
				continue
			}
			pending = append(pending, c)
		}
		// The already-queued candidates come last, so a pass at its bound spends it on work
		// the queue does not hold. EnqueueJob returns the existing row for those, and it is
		// not counted as this pass's work: Jobs is what the pass added, so a caller can tell
		// a pass that queued from one that found everything already queued.
		for _, c := range append(pending, done...) {
			// Only work the queue does not hold counts as left out. An already-queued
			// candidate past the bound is not remaining work, and reporting it as such would
			// make a fully consolidated workspace look unfinished.
			if len(res.Jobs) >= consolidationJobsPerRun {
				if !existing[c.ConflictSignature] {
					res.Skipped++
				}
				continue
			}
			job, created, err := tx.EnqueueJob(ctx, c, in.Actor)
			if err != nil {
				return fmt.Errorf("enqueue %s: %w", c.ConflictSignature, err)
			}
			if !created {
				continue
			}
			res.Jobs = append(res.Jobs, job)
		}
		return nil
	}); err != nil {
		return res, err
	}
	res.Skipped += skipped

	// The watermark advances only here, once every candidate is queued, and it records the
	// cursor this pass reached, so a workspace larger than one pass reads resumes where it
	// stopped instead of re-reading its first page for ever.
	if err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		// ReflectedCount accumulates across the source's passes: it counts the entries the
		// source has reflected over in total, which is what the store's monotonicity check
		// needs. A pass that read four pages adds those pages to what earlier passes read,
		// and the position it stopped at is the cursor, which is what the next pass resumes
		// from.
		reflected := startReflected + int64(seen)
		_, err := tx.AdvanceWatermark(ctx, govstore.WatermarkAdvance{WorkspaceID: workspaceID, Source: in.Source,
			ExpectVersion: startVersion, ReflectedThrough: formatCursor(cursor), TotalCount: reflected,
			ReflectedCount: reflected}, in.Actor)
		return err
	}); err != nil {
		return res, err
	}
	return res, nil
}

// unqueuedCandidates is the candidates whose signature the queue does not hold, which is what
// a pass can actually add. It reads the queue, so it is the test for "this pass has work".
func unqueuedCandidates(ctx context.Context, dataDir, workspaceID string, candidates []govstore.JobInput) ([]govstore.JobInput, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	var out []govstore.JobInput
	if err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		jobs, err := r.ListJobs(ctx, govstore.JobFilter{WorkspaceID: workspaceID, States: []string{
			govstore.JobQueued, govstore.JobLeased, govstore.JobDone, govstore.JobFailed},
			Limit: consolidationQueuedSignatures})
		if err != nil {
			return err
		}
		held := make(map[string]bool, len(jobs))
		for _, j := range jobs {
			held[j.ConflictSignature] = true
		}
		for _, c := range candidates {
			if !held[c.ConflictSignature] {
				out = append(out, c)
			}
		}
		return nil
	}); err != nil {
		// The caller decides whether to re-read from the start, and a transient read failure
		// returned as an empty queue would look exactly like a fully consolidated workspace.
		return nil, err
	}
	return out, nil
}

// mergeCandidates joins two derived lists, keeping the first occurrence of a signature and
// sorting the result, so a candidate found in both reaches is prepared once.
func mergeCandidates(first, second []govstore.JobInput) []govstore.JobInput {
	seen := make(map[string]bool, len(first)+len(second))
	out := make([]govstore.JobInput, 0, len(first)+len(second))
	for _, list := range [][]govstore.JobInput{first, second} {
		for _, c := range list {
			if seen[c.ConflictSignature] {
				continue
			}
			seen[c.ConflictSignature] = true
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b govstore.JobInput) int { return strings.Compare(a.ConflictSignature, b.ConflictSignature) })
	return out
}

// lastCursorOf is the largest insertion cursor on a page, which is the position after it.
func lastCursorOf(entries []govstore.Entry) int64 {
	last := entries[0].Cursor
	for _, e := range entries[1:] {
		if e.Cursor > last {
			last = e.Cursor
		}
	}
	return last
}

// queuedSignatures is every signature that already has a job the agent has not cancelled, so
// a pass can put the work it has not queued first.
func queuedSignatures(ctx context.Context, tx govstore.Tx, workspaceID string) (map[string]bool, error) {
	jobs, err := tx.ListJobs(ctx, govstore.JobFilter{WorkspaceID: workspaceID, States: []string{
		govstore.JobQueued, govstore.JobLeased, govstore.JobDone, govstore.JobFailed},
		Limit: consolidationQueuedSignatures})
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		out[j.ConflictSignature] = true
	}
	return out, nil
}

// cursorOf reads the entry cursor a watermark recorded. A watermark that recorded something
// else, or nothing, starts at the beginning.
func cursorOf(reflectedThrough string) int64 {
	if reflectedThrough == "" {
		return 0
	}
	n, err := strconv.ParseInt(reflectedThrough, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// formatCursor records the entry cursor a pass reached. It is a bare number so cursorOf can
// read it back without knowing the source's name.
func formatCursor(cursor int64) string { return strconv.FormatInt(cursor, 10) }

// ListConsolidationJobs is a workspace's consolidation jobs, oldest first, for an operator
// asking what the queue holds. It reads only; claiming and completing a job is the agent's,
// through the store's own CAS lease.
func ListConsolidationJobs(ctx context.Context, dataDir, workspaceID string, limit int) ([]govstore.Job, error) {
	var out []govstore.Job
	if err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		jobs, err := r.ListJobs(ctx, govstore.JobFilter{WorkspaceID: workspaceID, Limit: limit})
		if err != nil {
			return err
		}
		out = jobs
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// PrepareConsolidationJobs derives the candidates without queueing them, for a caller that
// wants the work before it is queued. RunConsolidationPass is what the loop calls.
func PrepareConsolidationJobs(ctx context.Context, dataDir, workspaceID string) ([]govstore.JobInput, error) {
	candidates, _, _, _, _, err := consolidationCandidates(ctx, dataDir, workspaceID, 0)
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
// consolidationCandidates derives the candidates from the entries past the cursor, and
// reports whether it reached the end of the table.
func consolidationCandidates(ctx context.Context, dataDir, workspaceID string, from int64) ([]govstore.JobInput, int, int, int64, bool, error) {
	var snap consolidationSnapshot
	seen := 0
	cursor := from
	// anchorsRead is the pass's whole anchor budget across every page, and anchorsLeft counts
	// what it could not read, so a store with more anchors than the budget says so rather
	// than quietly reflecting over a prefix of itself.
	anchorsRead := 0
	anchorsLeft := 0
	complete := false
	if err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		// Pages from the cursor the watermark recorded, not from the start: a workspace with
		// more entries than one pass reads would otherwise have every entry past the first
		// page ignored for ever, while the watermark still recorded a successful pass.
		for page := 0; page < consolidationPages; page++ {
			entries, err := r.ListEntries(ctx, govstore.EntryFilter{WorkspaceID: workspaceID,
				Lifecycles: []string{"*"}, AfterCursor: cursor, Limit: consolidationPage})
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				// An empty page is the end of the table. Without this the pass could not tell
				// "the cursor is past everything" from "there is nothing here", and a
				// workspace whose entries all sit before the cursor would never be looked at
				// again.
				complete = true
				return nil
			}
			seen += len(entries)
			slices.SortFunc(entries, func(a, b govstore.Entry) int { return strings.Compare(a.ID, b.ID) })
			ids := make([]string, 0, len(entries))
			for _, e := range entries {
				ids = append(ids, e.ID)
			}
			// Anchors are read one entry at a time, in entry order, and only while the pass's
			// budget lasts. Reading the whole page at once would materialise every anchor of
			// 500 entries before the budget could apply, which is the memory the bound is
			// supposed to prevent. The order is the entries' own, so a store snapshot yields
			// the same candidates however Go chooses to range a map.
			kept := make([]govstore.Entry, 0, len(entries))
			page := make(map[string][]govstore.Anchor, len(entries))
			// partial is the cursor of an entry whose anchors did not fit the budget, 0 when
			// every entry read was read whole.
			var partial int64
			for _, e := range entries {
				if anchorsRead >= consolidationAnchors {
					// The entry's anchors are not read at all. It is not kept, so the cursor
					// stops behind it and the next pass reads it: an entry dropped silently
					// here would never be looked at again.
					anchorsLeft += 1
					continue
				}
				list, err := r.AnchorsFor(ctx, []string{e.ID})
				if err != nil {
					return err
				}
				full := list[e.ID]
				if anchorsRead+len(full) > consolidationAnchors {
					room := consolidationAnchors - anchorsRead
					if room >= len(full) {
						room = len(full)
					}
					anchorsLeft += len(full) - room
					page[e.ID] = full[:room]
					anchorsRead += room
					kept = append(kept, e)
					// The entry is kept but its anchors are short, so the cursor must stop
					// behind it: the remainder is work this pass did not see.
					partial = e.Cursor
					continue
				}
				anchorsRead += len(full)
				kept = append(kept, e)
				if len(full) > 0 {
					page[e.ID] = full
				}
			}
			if snap.Entries == nil {
				snap.Entries = kept
				snap.Anchors = page
			} else {
				snap.Entries = append(snap.Entries, kept...)
				for id, list := range page {
					snap.Anchors[id] = list
				}
				// The window is what keeps a pass's memory bounded, and it is also the reach in
				// which two memories sharing an anchor are candidates for each other. Entries
				// older than the window are dropped from the snapshot rather than accumulated:
				// keeping them would make the memory grow with the table, and treating them as
				// candidates would make a pair that shares an anchor only by coincidence look
				// like a cluster.
				if over := len(snap.Entries) - consolidationClusterWindow; over > 0 {
					drop := snap.Entries[:over]
					for _, e := range drop {
						delete(snap.Anchors, e.ID)
					}
					snap.Entries = snap.Entries[over:]
				}
			}
			// The cursor is the store's own: ListEntries orders by pk and AfterCursor is
			// that pk, so the last row read is where the next page starts. An entry whose
			// anchors the budget did not reach keeps the cursor behind it, so the next pass
			// re-reads it rather than passing over work it never saw.
			if len(kept) == 0 {
				complete = true
				return nil
			}
			if partial != 0 {
				// An entry was read with its anchors cut short, so the table is not exhausted
				// and the cursor stops before that entry rather than after it.
				return nil
			}
			// The cursor is the store's insertion order, so it is the largest Cursor on the
			// page, not the cursor of whichever entry sorted last: the snapshot is ordered by
			// id so preparation is deterministic, and taking the cursor from that order would
			// re-read part of every page for ever.
			cursor = lastCursorOf(kept)
			if len(kept) < len(entries) {
				// The budget ran out inside this page, so the table is not exhausted.
				return nil
			}
			if len(entries) < consolidationPage {
				complete = true
				return nil
			}
		}
		return nil
	}); err != nil {
		return nil, 0, 0, 0, false, err
	}
	// Anchors left unread are work the pass did not cover: counted with the candidates it
	// could not take, so the pass never reads as a pass that saw everything.
	anchorSkips := 0
	if anchorsLeft > 0 {
		anchorSkips = 1
	}

	// out is every candidate the snapshot yields. The bound is not applied here: a pass that
	// cut the list before consulting the queue could never recover the tail (see
	// RunConsolidationPass), so the queue decides what a pass takes and this function reports
	// what the store holds.
	var out []govstore.JobInput
	for _, kind := range jobKindOrder {
		gen, ok := jobGenerators[kind]
		if !ok {
			// Unreachable while jobKindOrder and jobGenerators are edited together; a
			// missing generator is a programming error, not a silently skipped kind.
			return nil, 0, 0, 0, false, fmt.Errorf("consolidation: no generator for kind %q", kind)
		}
		candidates := gen(snap)
		// Kinds run in order and each kind's candidates are sorted by signature, so the
		// prepared sequence is a function of the snapshot alone. The bound is applied after
		// the sort, which is what makes the cut deterministic: the same store always leaves
		// the same tail for a later pass.
		slices.SortFunc(candidates, func(a, b consolidationCandidate) int { return strings.Compare(a.Signature, b.Signature) })
		for _, c := range candidates {
			out = append(out, govstore.JobInput{
				WorkspaceID:       workspaceID,
				Kind:              kind,
				ConflictSignature: c.Signature,
				CandidateIDs:      c.Members,
				Instructions:      jobInstructions[kind],
			})
		}
	}
	return out, seen, anchorSkips, cursor, complete, nil
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
	// A cluster larger than the cap becomes several candidates rather than one truncated one:
	// cutting it to the first members would leave the rest never asked about, and every pass
	// would make the same cut.
	return clusterCandidates(govstore.JobDedupeCluster, byAnchor)
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
		// The entry id is in the signature as well as in the members: one job is one
		// question about one memory, so two memories stale on one anchor are two jobs.
		out = append(out, consolidationCandidate{Signature: singleEntrySignature(govstore.JobStaleAnchor, e.ID, stale), Members: []string{e.ID}})
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
		out = append(out, consolidationCandidate{Signature: singleEntrySignature(govstore.JobOrphanedAnchor, e.ID, gone), Members: []string{e.ID}})
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
	return clusterCandidates(govstore.JobSupersessionChain, bySubject)
}

// clusterCandidates turns the groups a generator found into candidates: each group sorted,
// split into candidates of at most consolidationMaxCluster members so a subject with more
// members than one candidate can hold asks about all of them across several. A leftover
// smaller than the minimum joins the previous candidate rather than becoming one no agent
// could act on.
func clusterCandidates(kind string, byKey map[string][]string) []consolidationCandidate {
	var out []consolidationCandidate
	for _, members := range byKey {
		if len(members) < consolidationMinCluster {
			continue
		}
		slices.Sort(members)
		for start := 0; start < len(members); start += consolidationMaxCluster {
			end := min(start+consolidationMaxCluster, len(members))
			chunk := slices.Clone(members[start:end])
			if len(chunk) < consolidationMinCluster {
				if len(out) > 0 {
					out[len(out)-1].Members = append(out[len(out)-1].Members, chunk...)
					out[len(out)-1].Signature = clusterSignature(kind, out[len(out)-1].Members)
					continue
				}
				continue
			}
			out = append(out, consolidationCandidate{Signature: clusterSignature(kind, chunk), Members: chunk})
		}
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
		out = append(out, consolidationCandidate{Signature: singleEntrySignature(govstore.JobAgeingProposal, e.ID, nil), Members: []string{e.ID}})
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
//
// EnqueueJob refuses a conflict signature over maxSignatureLen bytes, and the ids Remember
// mints are longer than a cluster of fifteen members fits, so a signature that would exceed
// the limit is replaced by the digest of its own joined form. The digest keeps the property
// that matters, two candidates differing in any member are different work, at a fixed length.
func clusterSignature(kind string, members []string) string {
	sorted := slices.Clone(members)
	slices.Sort(sorted)
	joined := strings.Join(sorted, ",")
	if len(kind)+1+len(joined) <= maxSignatureLen {
		return kind + ":" + joined
	}
	return kind + ":" + govstore.Digest(joined)[:signatureDigestLen]
}

// singleEntrySignature is a candidate about one memory: it names the kind, the entry and the
// anchors that prompted it. The entry is what keeps two memories asking the same question
// about one shared anchor two separate jobs.
func singleEntrySignature(kind, entryID string, refs []string) string {
	sorted := slices.Clone(refs)
	slices.Sort(sorted)
	return clusterSignature(kind, append([]string{entryID}, sorted...))
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

// groundConsolidationJobs is how many jobs ground shows and how many the queue holds. The
// bound is ground's, not a pass's: ground answers at once, and a block listing a hundred
// jobs would cost more context than the agent saves reading them. Ten is enough to see the
// shape of the queue; jobs-list reads the rest.
const groundConsolidationJobs = 10

// ConsolidationJob is one queued question about memory, as ground shows it (PAR-GOV-13). It
// carries the instructions because the agent is the one who acts on them, and the evidence
// refs because the agent checks its answer against the same handles.
type ConsolidationJob struct {
	JobID             string   `json:"job_id"`
	Kind              string   `json:"kind"`
	CandidateIDs      []string `json:"candidate_ids"`
	EvidenceRefs      []string `json:"evidence_refs,omitempty"`
	Instructions      string   `json:"instructions,omitempty"`
	ConflictSignature string   `json:"conflict_signature,omitempty"`
}

// readGroundingConsolidationJobs reads the queue for ground: the oldest jobs still waiting,
// and the queue's total. A cancelled or finished job is not waiting for anyone.
func readGroundingConsolidationJobs(dataDir, workspaceID string) ([]ConsolidationJob, *int, error) {
	var out []ConsolidationJob
	var total int
	if err := memoryView(context.Background(), dataDir, workspaceID, func(r govstore.Reader) error {
		all, err := r.ListJobs(context.Background(), govstore.JobFilter{WorkspaceID: workspaceID,
			States: []string{govstore.JobQueued, govstore.JobLeased}, Limit: groundQueuedJobsTotal})
		if err != nil {
			return err
		}
		total = len(all)
		for i, j := range all {
			if i >= groundConsolidationJobs {
				break
			}
			out = append(out, ConsolidationJob{JobID: j.ID, Kind: j.Kind, CandidateIDs: j.CandidateIDs,
				EvidenceRefs: j.EvidenceRefs, Instructions: j.Instructions, ConflictSignature: j.ConflictSignature})
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return out, &total, nil
}

// groundQueuedJobsTotal bounds the count ground reads to name the queue's size. It is well
// above the number shown, so the total is a count and not the list's length.
const groundQueuedJobsTotal = 20000

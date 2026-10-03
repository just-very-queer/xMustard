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

	// The cursor is where the pass resumes in the entry table, but a workspace that is fully
	// consolidated must still be revisited: a new memory lands at a cursor past the one the
	// last pass reached, so a pass that only ever walked forward would never see it. The
	// scan therefore starts at the beginning when the source has caught up, and the cursor
	// only matters while entries remain.
	candidates, seen, skipped, cursor, complete, err := consolidationCandidates(ctx, dataDir, workspaceID, startCursor)
	if err != nil {
		return res, err
	}
	if complete {
		candidates, seen, skipped, cursor, complete, err = consolidationCandidates(ctx, dataDir, workspaceID, 0)
		if err != nil {
			return res, err
		}
	}
	res.Seen = seen
	res.Cursor = cursor
	res.Complete = complete

	// Candidates whose signature has no job yet are taken first, so a pass that hit its
	// bound last time advances into the work it has not queued instead of re-selecting the
	// same first candidates and starving the rest forever.
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
		for _, c := range append(pending, done...) {
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

// queuedSignatures is every signature that already has a job the agent has not cancelled, so
// a pass can put the work it has not queued first.
func queuedSignatures(ctx context.Context, tx govstore.Tx, workspaceID string) (map[string]bool, error) {
	jobs, err := tx.ListJobs(ctx, govstore.JobFilter{WorkspaceID: workspaceID, States: []string{
		govstore.JobQueued, govstore.JobLeased, govstore.JobDone, govstore.JobFailed}, Limit: consolidationJobsPerRun})
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
				return nil
			}
			seen += len(entries)
			slices.SortFunc(entries, func(a, b govstore.Entry) int { return strings.Compare(a.ID, b.ID) })
			ids := make([]string, 0, len(entries))
			for _, e := range entries {
				ids = append(ids, e.ID)
			}
			anchors, err := r.AnchorsFor(ctx, ids)
			if err != nil {
				return err
			}
			// The anchor budget is the whole pass's, not one page's: a pass that reads one
			// page of a thousand anchors per page still reads four thousand.
			for id, list := range anchors {
				if anchorsRead >= consolidationAnchors {
					anchorsLeft += len(list)
					delete(anchors, id)
					continue
				}
				if room := consolidationAnchors - anchorsRead; len(list) > room {
					anchorsLeft += len(list) - room
					anchors[id] = list[:room]
					anchorsRead = consolidationAnchors
				} else {
					anchorsRead += len(list)
				}
			}
			if snap.Entries == nil {
				snap.Entries = entries
				snap.Anchors = anchors
			} else {
				snap.Entries = append(snap.Entries, entries...)
				for id, list := range anchors {
					snap.Anchors[id] = list
				}
			}
			// The cursor is the store's own: ListEntries orders by pk and AfterCursor is
			// that pk, so the last row read is where the next page starts.
			cursor = entries[len(entries)-1].Cursor
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

	var out []govstore.JobInput
	skipped := 0
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
	return out, seen, skipped + anchorSkips, cursor, complete, nil
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

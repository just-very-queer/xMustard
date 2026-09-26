package workspaceops

import (
	"context"
	"fmt"
	"log"
	"strings"

	"xmustard/api-go/internal/govstore"
)

// VerifyContext records an authenticated agent's verdict. See VerifyContextAs.
func VerifyContext(dataDir, workspaceID, entryID, agent string, approve bool, note string) (*ContextEntry, error) {
	return VerifyContextAs(dataDir, workspaceID, entryID, ContextActor{ID: agent}, approve, note)
}

// VerifyContextAs records voter's approve or reject on an entry's served revision. See
// VerifyContextOutcome.
func VerifyContextAs(dataDir, workspaceID, entryID string, voter ContextActor, approve bool, note string) (*ContextEntry, error) {
	outcome := OutcomeReject
	if approve {
		outcome = OutcomeApprove
	}
	return VerifyContextOutcome(dataDir, workspaceID, entryID, voter, VerifyRequest{Outcome: outcome, Note: note})
}

// Verify outcomes (WS-19A). WS-19B adds duplicate_of and the per-memory feedback
// outcomes with evidence-bound votes.
const (
	OutcomeApprove = "approve"
	OutcomeReject  = "reject"
	OutcomeRetract = "retract"
)

// verifyVerdicts maps a verify outcome to the verdict it stores.
var verifyVerdicts = map[string]string{
	OutcomeApprove: govstore.VerdictApprove,
	OutcomeReject:  govstore.VerdictReject,
	OutcomeRetract: govstore.VerdictRetract,
}

// VerifyRequest is one verdict. Revision 0 votes on the served revision; the number of
// a pending revision votes on that edit. retract applies to the served revision only.
type VerifyRequest struct {
	Outcome  string
	Revision int64
	Note     string
}

// VerifyContextOutcome records voter's verdict and settles what it decides. Distinct
// principals only: a principal's new verdict on a revision replaces its earlier one, so
// one agent cannot satisfy a multi-agent gate by voting twice. A vote on the served
// revision re-promotes or demotes the entry; a vote on a pending revision accepts or
// rejects that edit once its quorum is reached, and the result carries the diff it voted
// on; a retract takes the entry out of recall once enough distinct principals retract
// it. The vote re-gates an entry the open-mode identity wrote for the kind of write it
// is (regatedRequirement). The whole read-vote-reconcile runs in one store transaction,
// so concurrent votes from any process are never lost.
func VerifyContextOutcome(dataDir, workspaceID, entryID string, voter ContextActor, req VerifyRequest) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	agent := strings.TrimSpace(voter.ID)
	if agent == "" {
		return nil, fmt.Errorf("agent is required")
	}
	verdict, ok := verifyVerdicts[fallbackString(strings.ToLower(strings.TrimSpace(req.Outcome)), OutcomeApprove)]
	if !ok {
		return nil, fmt.Errorf("outcome %q is not approve, reject or retract: %w", req.Outcome, ErrInvalidInput)
	}
	if req.Revision < 0 {
		return nil, fmt.Errorf("revision must be positive: %w", ErrInvalidInput)
	}
	ctx := context.Background()
	root := contextRoot(dataDir, workspaceID)
	actor := memoryActor(agent, root)
	var out ContextEntry
	var promoted bool
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		e, ce, err := loadEntryTx(ctx, tx, workspaceID, entryID)
		if err != nil {
			return err
		}
		out, promoted, err = castVerdict(ctx, tx, dataDir, workspaceID, root, e, ce, voter, actor,
			govstore.VoteInput{EntryID: entryID, Revision: req.Revision, Verdict: verdict, Note: req.Note})
		return err
	})
	if err != nil {
		return nil, err
	}
	if promoted {
		recordVerifyFeedback(dataDir, workspaceID, out.ID, out.Paths)
	}
	return &out, nil
}

// castVerdict records one verdict inside tx and settles what it decides (see
// VerifyContextOutcome). newlyBaselined reports a promotion that captured a baseline.
func castVerdict(ctx context.Context, tx govstore.Tx, dataDir, workspaceID, root string, e govstore.Entry,
	ce ContextEntry, voter ContextActor, actor govstore.Actor, in govstore.VoteInput) (ContextEntry, bool, error) {
	onPending := in.Revision != 0 && in.Revision != e.Revision
	if onPending && in.Verdict == govstore.VerdictRetract {
		return ContextEntry{}, false, fmt.Errorf("retract applies to the served revision %d, not revision %d: %w",
			e.Revision, in.Revision, ErrInvalidInput)
	}
	if onPending && in.Revision != e.HeadRevision {
		return ContextEntry{}, false, &govstore.ConflictError{EntryID: in.EntryID, BaseRevision: in.Revision,
			CurrentRevision: e.Revision, CurrentDigest: e.ContentDigest, HeadRevision: e.HeadRevision,
			Reason: "only the served revision or the pending head revision takes votes"}
	}
	if _, err := tx.RecordVote(ctx, in, actor); err != nil {
		return ContextEntry{}, false, err
	}
	requireMulti, threshold := contextDefaults(dataDir)
	if err := regate(ctx, tx, ce, voter.OpenMode, requireMulti, threshold, actor); err != nil {
		return ContextEntry{}, false, err
	}
	if onPending {
		diff, err := revisionDiff(ctx, tx, in.EntryID, e.Revision, in.Revision)
		if err != nil {
			return ContextEntry{}, false, err
		}
		out, promoted, err := settleRevision(ctx, tx, workspaceID, in.EntryID, in.Revision, threshold, root, actor)
		out.Diff = diff
		return out, promoted, err
	}
	out, promoted, err := settleEntry(ctx, tx, workspaceID, in.EntryID, threshold, root, actor)
	if err != nil || in.Verdict != govstore.VerdictRetract {
		return out, promoted, err
	}
	return retractOnQuorum(ctx, tx, workspaceID, out, in.Note, actor)
}

// retractOnQuorum retracts an entry once as many distinct principals have retracted its
// served revision as its gate requires. A retracted entry leaves recall, keeps its
// history and can be restored by an admin, after which it must be verified again.
func retractOnQuorum(ctx context.Context, tx govstore.Tx, workspaceID string, ce ContextEntry, reason string,
	actor govstore.Actor) (ContextEntry, bool, error) {
	tally, err := tx.Tally(ctx, ce.ID, 0)
	if err != nil {
		return ContextEntry{}, false, err
	}
	if tally.Retractions < max(ce.RequiredVerifications, 1) {
		return ce, false, nil
	}
	if _, err := tx.Transition(ctx, ce.ID, govstore.TransitionInput{To: govstore.LifecycleRetracted, Reason: reason}, actor); err != nil {
		return ContextEntry{}, false, err
	}
	out, err := entryAfter(ctx, tx, workspaceID, ce.ID)
	return out, false, err
}

// settleEntry re-derives an entry's status and promotion from the votes on its served
// revision (reconcileEntry), labels a promoted entry with the store's trust rule
// (govstore.VerificationMode), and records both. An entry promoted without a drift
// baseline gets one captured now; newlyBaselined reports that, so the caller can boost
// its paths in the feedback layer after the commit.
func settleEntry(ctx context.Context, tx govstore.Tx, workspaceID, entryID string, threshold int, root string,
	actor govstore.Actor) (out ContextEntry, newlyBaselined bool, err error) {
	e0, ce, err := loadEntryTx(ctx, tx, workspaceID, entryID)
	if err != nil {
		return ContextEntry{}, false, err
	}
	served, err := tx.GetRevision(ctx, entryID, e0.Revision)
	if err != nil {
		return ContextEntry{}, false, err
	}
	reconcileEntry(&ce, served.Author)
	ce.VerificationMode = ""
	if ce.Promoted {
		tally, err := tx.Tally(ctx, entryID, 0)
		if err != nil {
			return ContextEntry{}, false, err
		}
		ce.VerificationMode = govstore.VerificationMode(tally, ce.RequiredVerifications, threshold)
	}
	e, err := tx.SetPromotion(ctx, entryID, govstore.Promotion{Status: ce.Status, Promoted: ce.Promoted,
		VerificationMode: ce.VerificationMode}, actor)
	if err != nil {
		return ContextEntry{}, false, err
	}
	ce.UpdatedAt = e.UpdatedAt
	if ce.Promoted {
		if err := applySupersession(ctx, tx, e, actor); err != nil {
			return ContextEntry{}, false, err
		}
		ce.Supersedes = nil
	}
	if ce.Promoted && len(ce.PathHashes) == 0 {
		// just promoted: snapshot the referenced files so drift-on-recall has a baseline.
		ce.PathHashes = capturePathHashes(root, ce.Paths)
		if err := tx.SetBaselines(ctx, entryID, pathBaselines(ce.PathHashes), actor.HeadSHA, actor); err != nil {
			return ContextEntry{}, false, err
		}
		newlyBaselined = true
	}
	ce.ContentDigest = ""
	return ce, newlyBaselined, nil
}

// regate records the gate regatedRequirement gives the entry for this write.
func regate(ctx context.Context, tx govstore.Tx, ce ContextEntry, openModeWrite, requireMulti bool, threshold int,
	actor govstore.Actor) error {
	n := regatedRequirement(&ce, openModeWrite, requireMulti, threshold)
	if n == ce.RequiredVerifications {
		return nil
	}
	_, err := tx.SetRequiredVerifications(ctx, ce.ID, n, actor)
	return err
}

// recordVerifyFeedback boosts a newly promoted entry's paths in the agent-feedback
// layer. The boost is a best-effort ranking signal, so a failure is logged rather
// than failing the memory write that promoted the entry.
func recordVerifyFeedback(dataDir, workspaceID, entryID string, paths []string) {
	if err := RecordFeedback(dataDir, workspaceID, "verify", paths); err != nil {
		log.Printf("feedback: verify boost for workspace %s entry %s failed: %v", workspaceID, entryID, err)
	}
}

// distinctApprovals counts unique agents that approved (latest verdict per agent).
// excluded agents' votes are dropped from the tally — used to stop an author from
// self-approving toward a multi-agent threshold.
func distinctApprovals(verifications []ContextVerification, excluded ...string) (approvals, rejections int) {
	skip := map[string]bool{}
	for _, a := range excluded {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			skip[a] = true
		}
	}
	for agent, approve := range latestVerdicts(verifications) {
		switch {
		case skip[agent]:
			// an author's own vote does not count toward a multi-agent gate
		case approve:
			approvals++
		default:
			rejections++
		}
	}
	return approvals, rejections
}

// reconcileEntry promotes or demotes an entry from its verifications and threshold.
// revisionAuthor wrote the served content (the proposer, or the editor of a rewrite).
func reconcileEntry(entry *ContextEntry, revisionAuthor string) {
	// In multi-agent mode (threshold > 1) neither the entry's author nor the author of
	// the served revision counts as one of the required verifiers, matching the store's
	// peer rule; single-agent mode (threshold 1) is the operator opting out, so the
	// author's self-assertion is allowed to promote.
	var excluded []string
	if entry.RequiredVerifications > 1 {
		excluded = []string{entry.Source, revisionAuthor}
	}
	approvals, rejections := distinctApprovals(entry.Verifications, excluded...)
	switch {
	case rejections >= entry.RequiredVerifications && rejections > 0:
		entry.Status = "rejected"
		entry.Promoted = false
	case approvals >= entry.RequiredVerifications || openModeAssertionStands(entry):
		// A self-asserted open-mode entry stays promoted while authenticated peers
		// build a quorum, so approving it never hides it; any dissent removes that basis.
		entry.Status = "verified"
		entry.Promoted = true
	default:
		entry.Status = "pending"
		entry.Promoted = false
	}
}

// openModeAssertionStands reports whether an entry the open-mode identity wrote still
// rests on that identity's own word: it approved the current content (an edit starts a
// new revision with no votes), nobody has dissented since, and the proposal did not ask
// for peer verification. Such an entry is promoted as self_asserted_open_mode until a
// full peer quorum upgrades it.
func openModeAssertionStands(e *ContextEntry) bool {
	if e.RequireVerification || !IsOpenModeIdentity(e.Source) {
		return false
	}
	asserted := false
	for agent, approve := range latestVerdicts(e.Verifications) {
		if !approve {
			return false
		}
		asserted = asserted || agent == OpenModeIdentity
	}
	return asserted
}

// regatedRequirement is the quorum of an entry the open-mode identity wrote for the
// write happening now, because open mode is a property of each write. An open-mode
// write gets the single-assertion gate ProposeContext gives an open-mode proposal
// (unless it asked for peer verification), so entries that older builds left pending
// can still be asserted. An authenticated write raises a single-assertion gate to the
// workspace quorum, so once tokens exist no single principal can reject, rewrite and
// re-promote open-mode memory alone. Entries proposed under authentication keep the
// gate they were proposed with.
func regatedRequirement(e *ContextEntry, openModeWrite, requireMulti bool, threshold int) int {
	switch {
	case !IsOpenModeIdentity(e.Source):
		return e.RequiredVerifications
	case openModeWrite && !e.RequireVerification:
		return 1
	case !openModeWrite && e.RequiredVerifications <= 1 && (requireMulti || e.RequireVerification):
		return max(threshold, 1)
	}
	return e.RequiredVerifications
}

// latestVerdicts returns each distinct agent's latest verdict, keyed case-insensitively.
func latestVerdicts(verifications []ContextVerification) map[string]bool {
	latest := map[string]bool{}
	for _, v := range verifications {
		if agent := strings.ToLower(strings.TrimSpace(v.Agent)); agent != "" {
			latest[agent] = v.Approve
		}
	}
	return latest
}

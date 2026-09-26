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

// VerifyContextAs records voter's verdict on an entry's served revision and re-promotes
// if the approval threshold is now met. Distinct agents only — a principal's new verdict
// replaces its earlier one, so a single agent cannot satisfy a multi-agent gate by
// voting twice. The vote re-gates an entry the open-mode identity wrote for the kind of
// write it is (regatedRequirement). The whole read-vote-reconcile runs in one store
// transaction, so concurrent votes from any process are never lost.
func VerifyContextAs(dataDir, workspaceID, entryID string, voter ContextActor, approve bool, note string) (*ContextEntry, error) {
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
	verdict := govstore.VerdictReject
	if approve {
		verdict = govstore.VerdictApprove
	}
	requireMulti, threshold := contextDefaults(dataDir)
	ctx := context.Background()
	root := contextRoot(dataDir, workspaceID)
	actor := memoryActor(agent, root)
	var out ContextEntry
	var promoted bool
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		_, ce, err := loadEntryTx(ctx, tx, workspaceID, entryID)
		if err != nil {
			return err
		}
		if _, err := tx.RecordVote(ctx, govstore.VoteInput{EntryID: entryID, Verdict: verdict, Note: note}, actor); err != nil {
			return err
		}
		if err := regate(ctx, tx, ce, voter.OpenMode, requireMulti, threshold, actor); err != nil {
			return err
		}
		out, promoted, err = settleEntry(ctx, tx, workspaceID, entryID, threshold, root, actor)
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

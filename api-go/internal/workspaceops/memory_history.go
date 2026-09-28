package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
)

// Memory history and the administrative lifecycle (WS-19A, PAR-GOV-04/05/06/12). An
// entry leaves recall by supersession, retirement, retraction or expiry and is never
// deleted by any of them: GetContextEntry still fetches it by id with its lifecycle
// state, and RestoreContext brings it back. Only PurgeContext deletes, and it keeps a
// digest tombstone.

// ErrApproverRequired: the lifecycle change needs an admin or a human approver.
var ErrApproverRequired = errors.New("only an admin or a human approver may do this")

// historyRevisionLimit and historyEventLimit bound one history read, and
// historyByteBudget bounds its encoded size: the oldest events and revisions are
// dropped first. Revisions are listed without their content.
const (
	historyRevisionLimit = 50
	historyEventLimit    = 200
	historyByteBudget    = 48 << 10
)

// unverifiedWithheld is why a reader does not see an entry's content.
const unverifiedWithheld = "content is not verified; reading it needs the verifier or human-approver role"

// GetContextEntry returns one entry by id in any lifecycle state, with its lifecycle
// fields and content digest. The served content is bound to its digest (withheld when
// purged or tampered). A plain reader sees it only once it is verified, as recall
// would; a reviewer (a verifier, a human approver or an admin) also sees unverified
// text, the pending edit with its diff and votes, and with history the revisions,
// relations and event log. History without reviewer is refused (ErrVerifierRequired).
func GetContextEntry(dataDir, workspaceID, entryID string, history, reviewer bool) (map[string]any, error) {
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	if history && !reviewer {
		return nil, fmt.Errorf("memory history: %w", ErrVerifierRequired)
	}
	ctx := context.Background()
	var out map[string]any
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		e, ce, err := loadAnyEntryTx(ctx, r, workspaceID, entryID)
		if err != nil {
			return err
		}
		out = map[string]any{"workspace_id": workspaceID, "content_digest": e.ContentDigest, "data_notice": injection.DataNotice}
		if withheld, err := servedContent(ctx, r, e, &ce, reviewer); err != nil {
			return err
		} else if withheld != "" {
			out["content_withheld"] = withheld
		}
		ce.InjectionFlags = injection.Scan(ce.Title, ce.Content).Flags
		ce.ContentDigest = ""
		out["entry"] = ce
		if err := addDerivation(ctx, r, e, out); err != nil {
			return err
		}
		if !reviewer {
			return nil
		}
		if e.HeadRevision > e.Revision {
			pending, err := pendingRevisionView(ctx, r, e)
			if err != nil {
				return err
			}
			out["pending"] = pending
		}
		if !history {
			return nil
		}
		return addHistory(ctx, r, e, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// servedContent fills ce.Content with the served revision when the caller may read it
// and it still matches its digest; otherwise it says why the content is withheld.
func servedContent(ctx context.Context, r govstore.Reader, e govstore.Entry, ce *ContextEntry, reviewer bool) (string, error) {
	if !reviewer && !ce.Promoted {
		return unverifiedWithheld, nil
	}
	contents, err := r.EntryContents(ctx, []string{e.ID})
	if err != nil {
		return "", err
	}
	c := contents[e.ID]
	if c.Withheld != "" || c.Digest != e.ContentDigest {
		return fallbackString(c.Withheld, "content does not match its digest"), nil
	}
	ce.Content = c.Content
	return "", nil
}

// pendingRevisionView is the newest pending revision as a verifier reads it.
func pendingRevisionView(ctx context.Context, r govstore.Reader, e govstore.Entry) (map[string]any, error) {
	rv, err := r.GetRevision(ctx, e.ID, e.HeadRevision)
	if err != nil {
		return nil, err
	}
	diff, err := revisionDiff(ctx, r, e.ID, e.Revision, rv.Revision)
	if err != nil {
		return nil, err
	}
	votes, err := r.ListVotes(ctx, e.ID, rv.Revision)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"revision": rv.Revision, "base_revision": rv.BaseRevision, "op": rv.Op, "reason": rv.Reason,
		"author": rv.Author, "content_digest": rv.ContentDigest, "diff": diff, "votes": votes,
	}
	if flags := injection.Scan(rv.Content).Flags; len(flags) > 0 {
		out["injection_flags"] = flags
	}
	return out, nil
}

// addDerivation adds how the entry was derived and what its verification rests on
// (PAR-PROV-04/05): provenance, the counting verdicts with their owners and evidence, and
// the outcome feedback principals reported.
func addDerivation(ctx context.Context, r govstore.Reader, e govstore.Entry, out map[string]any) error {
	prov, err := entryProvenance(ctx, r, e)
	if err != nil {
		return err
	}
	basis, err := verificationBasis(ctx, r, e)
	if err != nil {
		return err
	}
	feedback, err := r.SummarizeOutcomes(ctx, e.ID)
	if err != nil {
		return err
	}
	out["provenance"], out["verification_basis"], out["feedback"] = prov, basis, feedback
	return nil
}

// addHistory adds the revisions (newest first, without content), the relations and the
// event log (oldest first), within historyByteBudget.
func addHistory(ctx context.Context, r govstore.Reader, e govstore.Entry, out map[string]any) error {
	revisions, err := r.ListRevisions(ctx, e.ID, govstore.RevisionFilter{Limit: historyRevisionLimit})
	if err != nil {
		return err
	}
	relations, err := r.ListRelations(ctx, e.ID)
	if err != nil {
		return err
	}
	events, err := r.ListEvents(ctx, govstore.EventFilter{WorkspaceID: e.WorkspaceID, EntryID: e.ID, Limit: historyEventLimit})
	if err != nil {
		return err
	}
	revSizes, evSizes := encodedSizes(revisions), encodedSizes(events)
	kept := len(revisions) + len(events)
	total := sumInts(revSizes) + sumInts(evSizes)
	// Drop the oldest of whichever list is longer, keeping the newest of each.
	for total > historyByteBudget && len(revisions)+len(events) > 2 {
		if len(events) >= len(revisions) {
			total -= evSizes[0]
			events, evSizes = events[1:], evSizes[1:]
		} else {
			total -= revSizes[len(revSizes)-1]
			revisions, revSizes = revisions[:len(revisions)-1], revSizes[:len(revSizes)-1]
		}
	}
	if len(revisions)+len(events) < kept {
		out["history_truncated"] = true
	}
	out["revisions"], out["relations"], out["events"] = revisions, relations, events
	return nil
}

// encodedSizes is each item's JSON size.
func encodedSizes[T any](items []T) []int {
	out := make([]int, len(items))
	for i, it := range items {
		b, _ := json.Marshal(it)
		out[i] = len(b)
	}
	return out
}

func sumInts(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

// revisionDiff is the line diff from revision from to revision to of an entry.
func revisionDiff(ctx context.Context, r govstore.Reader, entryID string, from, to int64) (string, error) {
	a, err := r.GetRevision(ctx, entryID, from)
	if err != nil {
		return "", err
	}
	b, err := r.GetRevision(ctx, entryID, to)
	if err != nil {
		return "", err
	}
	diff := lineDiff(a.Content, b.Content)
	if a.Description != b.Description {
		diff = lineDiff("description: "+a.Description, "description: "+b.Description) + diff
	}
	return diff, nil
}

// maxDiffCells bounds the line-matching table; a larger change is shown as a whole
// replacement of the differing block.
const maxDiffCells = 1 << 20

// lineDiff renders the changed block between a and b: a "@@ -line +line @@" header, then
// " ", "-" and "+" lines, with the common prefix and suffix left out.
func lineDiff(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	p := 0
	for p < len(x) && p < len(y) && x[p] == y[p] {
		p++
	}
	s := 0
	for s < len(x)-p && s < len(y)-p && x[len(x)-1-s] == y[len(y)-1-s] {
		s++
	}
	x, y = x[p:len(x)-s], y[p:len(y)-s]
	if len(x) == 0 && len(y) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "@@ -%d +%d @@\n", p+1, p+1)
	for _, l := range diffLines(x, y) {
		out.WriteString(l)
		out.WriteByte('\n')
	}
	return out.String()
}

// diffLines aligns x and y on their longest common subsequence of lines.
func diffLines(x, y []string) []string {
	if len(x)*len(y) > maxDiffCells {
		return append(prefixed("-", x), prefixed("+", y)...)
	}
	// lcs[i][j] is the LCS length of x[i:] and y[j:].
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			out = append(out, " "+x[i])
			i, j = i+1, j+1
		case j == len(y) || (i < len(x) && lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, "-"+x[i])
			i++
		default:
			out = append(out, "+"+y[j])
			j++
		}
	}
	return out
}

func prefixed(p string, lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = p + l
	}
	return out
}

// hasLifecycleAuthority reports whether actor may change lifecycle state directly: an
// admin or a human approver (the local operator in open mode).
func (a ContextActor) hasLifecycleAuthority() bool { return a.Admin || a.Approver }

// RestoreContext brings an entry back into recall. An expired entry is restored by
// clearing its expiry (authorizeExpiry: its author, unless peers verified it); an archived, retracted or superseded
// entry needs an admin or a human approver, and one restored from retracted or
// superseded must be verified again (govstore.TransitionInput). A purged entry cannot
// be restored.
func RestoreContext(dataDir, workspaceID, entryID, reason string, actor ContextActor) (*ContextEntry, error) {
	return lifecycleWrite(dataDir, workspaceID, entryID, reason, actor, func(ctx context.Context, tx govstore.Tx,
		e govstore.Entry, ce ContextEntry, by govstore.Actor) error {
		switch {
		case e.Lifecycle == govstore.LifecycleActive && e.ExpiresAt == "":
			return fmt.Errorf("entry %s is active and unexpired; nothing to restore: %w", entryID, ErrInvalidInput)
		case e.Lifecycle == govstore.LifecycleActive:
			if err := actor.authorizeExpiry(ce); err != nil {
				return err
			}
			_, err := tx.SetExpiry(ctx, entryID, "", by)
			return err
		case !actor.hasLifecycleAuthority():
			return ErrApproverRequired
		}
		_, err := tx.Transition(ctx, entryID, govstore.TransitionInput{To: govstore.LifecycleActive, Reason: reason}, by)
		return err
	})
}

// RetractContext retracts an entry at once on an admin's or human approver's word
// (PAR-GOV-06); agents retract through verify(outcome=retract) and its quorum.
func RetractContext(dataDir, workspaceID, entryID, reason string, actor ContextActor) (*ContextEntry, error) {
	return lifecycleWrite(dataDir, workspaceID, entryID, reason, actor, func(ctx context.Context, tx govstore.Tx,
		_ govstore.Entry, _ ContextEntry, by govstore.Actor) error {
		if !actor.hasLifecycleAuthority() {
			return ErrApproverRequired
		}
		_, err := tx.Transition(ctx, entryID, govstore.TransitionInput{To: govstore.LifecycleRetracted, Reason: reason}, by)
		return err
	})
}

// PurgeContext hard-deletes an entry's text for secrets or PII (PAR-GOV-06): an admin or
// a human approver only. What remains is a tombstone with every revision's digest and the
// event skeleton ending in the purge event; the store is checkpointed so the text also
// leaves the WAL.
func PurgeContext(dataDir, workspaceID, entryID, reason string, actor ContextActor) (*ContextEntry, error) {
	if !actor.hasLifecycleAuthority() {
		return nil, ErrApproverRequired
	}
	out, err := lifecycleWrite(dataDir, workspaceID, entryID, reason, actor, func(ctx context.Context, tx govstore.Tx,
		_ govstore.Entry, _ ContextEntry, by govstore.Actor) error {
		return tx.Purge(ctx, entryID, reason, by)
	})
	if err != nil {
		return nil, err
	}
	// The purge is committed; a failed checkpoint only delays the text leaving the WAL
	// until the next one, so it is reported, not returned as a failure to retry.
	ctx := context.Background()
	if err := withMemoryStore(ctx, dataDir, workspaceID, func(s *govstore.SQLStore) error { return s.Checkpoint(ctx) }); err != nil {
		log.Printf("memory purge: checkpoint for workspace %s entry %s failed: %v", workspaceID, entryID, err)
		out.Warnings = append(out.Warnings, "checkpoint failed; the purged text may remain in the WAL until the next checkpoint")
	}
	return out, nil
}

// lifecycleWrite runs one reasoned lifecycle change in a store transaction and returns
// the entry as it stands afterwards, in whatever state that is.
func lifecycleWrite(dataDir, workspaceID, entryID, reason string, actor ContextActor,
	fn func(context.Context, govstore.Tx, govstore.Entry, ContextEntry, govstore.Actor) error) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("reason is required: %w", ErrInvalidInput)
	}
	var red ingestRedaction
	red.scrub(&reason)
	ctx := context.Background()
	by := actor.storeActor(contextRoot(dataDir, workspaceID))
	var out ContextEntry
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		e, ce, err := loadAnyEntryTx(ctx, tx, workspaceID, entryID)
		if err != nil {
			return err
		}
		if err := fn(ctx, tx, e, ce, by); err != nil {
			return err
		}
		out, err = entryAfter(ctx, tx, workspaceID, entryID)
		return err
	})
	if err != nil {
		return nil, err
	}
	red.annotate(&out)
	return &out, nil
}

// entryAfter renders an entry after a lifecycle change, without content.
func entryAfter(ctx context.Context, r govstore.Reader, workspaceID, entryID string) (ContextEntry, error) {
	_, ce, err := loadAnyEntryTx(ctx, r, workspaceID, entryID)
	ce.ContentDigest = ""
	return ce, err
}

package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// parityFixtureArms is how many candidates parityFixture builds, and
// parityFixtureRefused how many of them checkOwnerDistinct refuses. Both are asserted so
// a fixture that stopped reaching an arm, or one whose arm stopped being a refusal,
// fails here rather than weakening the parity assertion to nothing.
const (
	parityFixtureArms    = 8
	parityFixtureRefused = 5
)

// guardCase is one candidate of the owner-distinct parity fixture, named after the arm
// of checkOwnerDistinct it is built to reach.
type guardCase struct {
	name  string
	entry string
	rev   int64
}

// parityFixture builds one candidate per arm of checkOwnerDistinct, so the batched
// guard is compared against the per-row one on inputs that reach every branch. The
// voter is bob; the owner policy is on and the gate needs peers.
func parityFixture(t *testing.T, dir, ws, voterID string) []guardCase {
	t.Helper()
	var cases []guardCase

	// the entry's author arm: bob owns bob-bot's proposal, so only that arm can
	// refuse it. The writer's id differs from bob's, so the entry-author arm is
	// reached before the verdict arm could be.
	mine := proposeAs(t, dir, ws, ContextActor{ID: "bob-bot", Owner: "bob"}, "from bob's agent")
	cases = append(cases, guardCase{"entry author", mine.ID, 0})

	// the revision's author arm, reached through the edit event's recorded owner
	// rather than the proposal's. The editor is an admin holding no token of its own,
	// so only the owner its edit recorded ties this revision to bob.
	edited := promoted(t, dir, ws, "carol", "the build uses make")
	if _, err := EditContext(dir, ws, edited.ID, EditRequest{BaseRevision: 1, Reason: "add go", NewString: " and go"},
		ContextActor{ID: "editor-1", Owner: "bob", Admin: true}); err != nil {
		t.Fatal(err)
	}
	// the pending edit is the entry's head revision, which ContextEntry does not carry
	cases = append(cases, guardCase{"revision author", edited.ID, headRevision(t, dir, ws, edited.ID)})

	// the owner-bound verdict arm: another of bob's principals already approved.
	// bob-bot is not bob's id, so only the verdict arm can refuse this one. The
	// verdict is cast with bob's owner recorded, or the row would tie to bob-bot
	// itself and the arm would not be reached.
	cast := proposeAs(t, dir, ws, ContextActor{ID: "carol"}, "already cast")
	if _, err := VerifyContextAs(dir, ws, cast.ID, ContextActor{ID: "bob-bot", Owner: "bob"}, true, ""); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, guardCase{"owner-bound verdict", cast.ID, 0})

	// the same arm through a rejection: the policy is about the owner having a voice,
	// not about which way it voted.
	refused := proposeAs(t, dir, ws, ContextActor{ID: "carol"}, "already refused")
	if _, err := VerifyContextAs(dir, ws, refused.ID, ContextActor{ID: "bob-bot", Owner: "bob"}, false, "disagree"); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, guardCase{"owner-bound rejection", refused.ID, 0})

	// control: an unrelated author and none of bob's principals on it. A guard that
	// refused everything would fail the parity assertion rather than pass vacuously.
	cases = append(cases, guardCase{"unrelated author", proposeAs(t, dir, ws, ContextActor{ID: "dave"}, "dave's").ID, 0})

	// control: bob's OWN earlier verdict. sameOwner(v.Principal, voter.ID) excludes it,
	// so this is not a refusal either.
	own := proposeAs(t, dir, ws, ContextActor{ID: "dave"}, "dave's other")
	if _, err := VerifyContext(dir, ws, own.ID, voterID, true, "my own earlier verdict"); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, guardCase{"own earlier verdict", own.ID, 0})

	// the recordedOwner fallback on the REVISION arm. The entry is authored by a stranger,
	// so the entry-author arm admits it; only the revision's author ties it to bob, and
	// it does so through the token store: the workspaceops write path always records an
	// owner (storeActor fills it), so a revision that records none is only reachable
	// through the store API with no owner on the actor. A guard that skipped the
	// fallback would admit this row (mutation M2).
	mintPrincipal(t, dir, "bob-token-only", "bob")
	viaStore := promotedUnrecordedRevision(t, dir, ws, "dave", "bob-token-only")
	if got := PrincipalOwner(dir, "bob-token-only"); got != "bob" {
		t.Fatalf("the token store answers %q, want bob, so the arm was not built", got)
	}
	cases = append(cases, guardCase{"revision author through the token store", viaStore, headRevision(t, dir, ws, viaStore)})

	// a verdict cast in a superseded epoch is not a voice: the entry is retracted and
	// restored, which starts a new epoch, so the old approve must not refuse bob.
	stale := proposeAs(t, dir, ws, ContextActor{ID: "erin"}, "erin retracted me")
	if _, err := VerifyContextAs(dir, ws, stale.ID, ContextActor{ID: "bob-bot", Owner: "bob"}, true, ""); err != nil {
		t.Fatal(err)
	}
	by := ContextActor{ID: "erin", Admin: true}
	if _, err := RetractContext(dir, ws, stale.ID, "fixture: supersede the epoch", by); err != nil {
		t.Fatal(err)
	}
	if err := transitionEntry(t, dir, ws, stale.ID, govstore.LifecycleActive, by); err != nil {
		t.Fatal(err)
	}
	if epoch := entryVoteEpoch(t, dir, ws, stale.ID); epoch == 0 {
		t.Fatalf("%s is still in epoch 0, so its earlier verdict still counts and the "+
			"superseded-epoch arm was not reached", stale.ID)
	}
	cases = append(cases, guardCase{"superseded epoch verdict", stale.ID, 0})

	return cases
}

// mintPrincipal records a principal whose owner is owner, so a write that recorded no
// owner of its own can still be tied to one by the token store.
func mintPrincipal(t *testing.T, dir, id, owner string) {
	t.Helper()
	if _, err := MintIdentityToken(dir, id, RoleReader, 0, nil, TokenIdentity{Owner: owner, Kind: PrincipalAgent}); err != nil {
		t.Fatalf("mint %s: %v", id, err)
	}
	if got := PrincipalOwner(dir, id); got != owner {
		t.Fatalf("the token store answers %s's owner as %q, want %q, so the arm was not built",
			id, got, owner)
	}
}

// proposeUnrecordedOwner writes an entry through the store API with no owner on the
// actor, which is the only way a write records none: every workspaceops write fills the
// owner from the actor's own id.
func proposeUnrecordedOwner(t *testing.T, dir, ws, principal string) string {
	t.Helper()
	id := "ctx_unrecorded_" + principal
	err := memoryUpdate(context.Background(), dir, ws, func(tx govstore.Tx) error {
		_, err := tx.InsertEntry(context.Background(), govstore.NewEntry{
			ID: id, WorkspaceID: ws, Title: "t", Content: "the build uses make",
			RequiredVerifications: 2, RequireVerification: true,
		}, govstore.Actor{Principal: principal})
		return err
	})
	if err != nil {
		t.Fatalf("propose without a recorded owner: %v", err)
	}
	return id
}

// promotedUnrecordedRevision promotes an entry written by author and then appends a
// second revision by editor through the store API with no owner on the actor, which is
// the only way a revision records none. The second revision is pending (Activate
// false), so it is the entry's head revision and the queue's edit candidate.
func promotedUnrecordedRevision(t *testing.T, dir, ws, author, editor string) string {
	t.Helper()
	entry := promoted(t, dir, ws, author, "the build uses make")
	base := headRevision(t, dir, ws, entry.ID)
	err := memoryUpdate(context.Background(), dir, ws, func(tx govstore.Tx) error {
		_, err := tx.AppendRevision(context.Background(), govstore.RevisionInput{
			EntryID: entry.ID, BaseRevision: base, Op: "edit",
			Content: "the build uses make and go", Reason: "add go",
		}, govstore.Actor{Principal: editor})
		return err
	})
	if err != nil {
		t.Fatalf("append an unrecorded-owner revision: %v", err)
	}
	// the token store is the only answer, and it must answer bob for this arm to be the
	// one it claims to be
	if got := revisionOwnerForTest(t, dir, ws, entry.ID, headRevision(t, dir, ws, entry.ID)); got != "bob" {
		t.Fatalf("the revision's owner resolved to %q, want bob from the token store, so the arm is not the one described", got)
	}
	if got := PrincipalOwner(dir, editor); got != "bob" {
		t.Fatalf("the token store answers %q for %s, want bob, so the fallback cannot fire",
			got, editor)
	}
	return entry.ID
}

// revisionOwnerForTest is the owner revisionOwner resolves for a revision: the owner its
// edit event recorded, or the token store's answer for its author. A revision written
// through the store API with no owner on the actor carries no edit-event owner, so the
// token store is the only answer.
func revisionOwnerForTest(t *testing.T, dir, ws, entryID string, rev int64) string {
	t.Helper()
	var owner string
	err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		e, err := r.GetEntry(context.Background(), entryID)
		if err != nil {
			return err
		}
		rv, err := r.GetRevision(context.Background(), entryID, rev)
		if err != nil {
			return err
		}
		owner, err = revisionOwner(context.Background(), r, dir, e, rv)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

// entryVoteEpoch is the entry's vote epoch, which the guard's votes query joins on.
func entryVoteEpoch(t *testing.T, dir, ws, entryID string) int64 {
	t.Helper()
	var epoch int64
	if err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		e, err := r.GetEntry(context.Background(), entryID)
		if err != nil {
			return err
		}
		epoch = e.VoteEpoch
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// transitionEntry moves an entry to a lifecycle state, which is what starts a new
// vote epoch on the way back from retracted.
func transitionEntry(t *testing.T, dir, ws, entryID, to string, by ContextActor) error {
	t.Helper()
	return memoryUpdate(context.Background(), dir, ws, func(tx govstore.Tx) error {
		_, err := tx.Transition(context.Background(), entryID,
			govstore.TransitionInput{To: to, Reason: "fixture: epoch"}, by.storeActor(""))
		return err
	})
}

// headRevision is the entry's pending head revision, which ContextEntry does not
// carry: a voter names it on the entry, the context view does not expose it.
func headRevision(t *testing.T, dir, ws, entryID string) int64 {
	t.Helper()
	var head int64
	err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		e, err := r.GetEntry(context.Background(), entryID)
		if err != nil {
			return err
		}
		head = e.HeadRevision
		return nil
	})
	if err != nil {
		t.Fatalf("head revision of %s: %v", entryID, err)
	}
	return head
}

// candidatesOf reads the store's view of every fixture case, so both guards are
// compared on the same govstore.Entry values rather than on separate reads.
func candidatesOf(t *testing.T, r govstore.Reader, cases []guardCase) []queueCandidate {
	t.Helper()
	out := make([]queueCandidate, 0, len(cases))
	for _, c := range cases {
		e, err := r.GetEntry(context.Background(), c.entry)
		if err != nil {
			t.Fatalf("%s: get entry: %v", c.name, err)
		}
		rev := c.rev
		if rev == 0 {
			rev = e.Revision
		}
		out = append(out, queueCandidate{entry: e, revision: rev})
	}
	return out
}

// TestTheBatchedOwnerGuardAnswersExactlyWhatThePerRowGuardAnswers pins the batched
// guard to checkOwnerDistinct. The batched path re-derives the rule from one query, so
// it can drift from the rule Verify enforces; this test is what makes that drift red
// (WS-FIX-22b, the owner's parity requirement).
func TestTheBatchedOwnerGuardAnswersExactlyWhatThePerRowGuardAnswers(t *testing.T) {
	dir, ws := ownerPolicyDir(t, DistinctOwner), "ws"
	h := humanApprover("bob", "bob")
	voter := h.actor()
	cases := parityFixture(t, dir, ws, h.Principal.ID)
	if len(cases) != parityFixtureArms {
		t.Fatalf("fixture arms: %d, want %d", len(cases), parityFixtureArms)
	}

	err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		guarded := candidatesOf(t, r, cases)
		batched, err := batchOwnerVoices(context.Background(), r, dir, guarded, h)
		if err != nil {
			return err
		}
		if len(batched) != len(guarded) {
			return fmt.Errorf("the batched guard answered %d of %d candidates", len(batched), len(guarded))
		}

		refused, allowed := 0, 0
		var verdicts []string
		for i, c := range guarded {
			perRow := checkOwnerDistinct(context.Background(), r, dir, c.entry,
				govstore.VoteInput{EntryID: c.entry.ID, Revision: c.revision, Verdict: govstore.VerdictApprove}, voter)
			switch {
			case errors.Is(perRow, ErrSameOwner):
				refused++
				verdicts = append(verdicts, "refused "+cases[i].name)
			case perRow != nil:
				return fmt.Errorf("%s: checkOwnerDistinct failed on a readable store: %v", cases[i].name, perRow)
			default:
				allowed++
				verdicts = append(verdicts, "allowed "+cases[i].name)
			}
			if errors.Is(batched[i], ErrSameOwner) != errors.Is(perRow, ErrSameOwner) {
				return fmt.Errorf("%s: the batched guard said %v, checkOwnerDistinct said %v",
					cases[i].name, batched[i], perRow)
			}
		}
		// The arms are asserted by count, so a guard that agreed with a per-row guard
		// because neither reached anything would fail here instead of passing quietly.
		if refused != parityFixtureRefused || allowed != parityFixtureArms-parityFixtureRefused {
			return fmt.Errorf("the fixture reached %d refused and %d allowed arms, want %d and %d: %v",
				refused, allowed, parityFixtureRefused, parityFixtureArms-parityFixtureRefused, verdicts)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestTheBatchedOwnerGuardRefusesEachArmOnItsOwn pins each arm of the batched guard
// against a fixture where that arm is the ONLY reason the row is refused. The parity
// test cannot do this on its own: it compares the batched guard with
// checkOwnerDistinct, and the per-row guard keeps every arm, so a batched arm that is
// absent still agrees whenever a later arm would have refused the same row. That is
// mutation M4, and it survived the parity test on correct code for exactly that
// reason. Here each arm is exercised alone, so losing one is red.
func TestTheBatchedOwnerGuardRefusesEachArmOnItsOwn(t *testing.T) {
	dir, ws := ownerPolicyDir(t, DistinctOwner), "ws"
	h := humanApprover("bob", "bob")

	arms := []struct {
		name    string
		build   func(t *testing.T) guardCase
		wantWhy string
	}{
		{"the entry's author", func(t *testing.T) guardCase {
			// bob owns bob-bot, and the row records no owner of its own, so only the
			// entry-author arm can refuse it
			e := proposeAs(t, dir, ws, ContextActor{ID: "bob-bot", Owner: "bob"}, "bob's agent")
			return guardCase{"entry author", e.ID, 0}
		}, "the entry's author"},
		{"the revision's author", func(t *testing.T) guardCase {
			// an admin editor holding no token of its own: only the owner its edit
			// recorded ties this revision to bob
			e := promoted(t, dir, ws, "carol", "the build uses make")
			if _, err := EditContext(dir, ws, e.ID, EditRequest{BaseRevision: 1, Reason: "add go",
				NewString: " and go"}, ContextActor{ID: "editor-2", Owner: "bob", Admin: true}); err != nil {
				t.Fatal(err)
			}
			return guardCase{"revision author", e.ID, headRevision(t, dir, ws, e.ID)}
		}, "the revision's author"},
		{"a principal that already voted", func(t *testing.T) guardCase {
			// carol's proposal, already approved by another of bob's principals; the
			// entry author is a stranger, so only the verdict arm can refuse it
			e := proposeAs(t, dir, ws, ContextActor{ID: "carol"}, "already cast, alone")
			if _, err := VerifyContextAs(dir, ws, e.ID, ContextActor{ID: "bob-bot", Owner: "bob"}, true, ""); err != nil {
				t.Fatal(err)
			}
			return guardCase{"owner-bound verdict", e.ID, 0}
		}, "already voted"},
	}

	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			c := arm.build(t)
			err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
				guarded := candidatesOf(t, r, []guardCase{c})
				got, err := batchOwnerVoices(context.Background(), r, dir, guarded, h)
				if err != nil {
					return err
				}
				if got[0] == nil {
					return fmt.Errorf("the batched guard admitted %s, whose %s arm should have refused it",
						c.name, arm.name)
				}
				if !errors.Is(got[0], ErrSameOwner) {
					return fmt.Errorf("the batched guard refused %s with %v, want ErrSameOwner", c.name, got[0])
				}
				if !strings.Contains(got[0].Error(), arm.wantWhy) {
					return fmt.Errorf("the batched guard refused %s with %q, which does not name the %s arm",
						c.name, got[0], arm.wantWhy)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestTheBatchedOwnerGuardNeverAsksAboutAnEntryTheApproverCannotSee pins the order of
// the two guards in the approval queue. WS-29b round 4 (r4-6) put ScopesVisible ahead
// of every per-row read, so nothing about an entry this approver may not see was even
// loaded; batching the owner-distinct reads for a whole page moved those reads in front
// of the scope check, so a page's revisions, edit events and votes were read for entries
// the approver cannot see at all.
//
// The contract is the filter, so the filter is what is asserted: given a page, the
// candidates handed to the batch are the visible ones and nothing else. Asserting the
// queue's final answer instead cannot see the order at all, because consider holds the
// same check and returns the same answer either way — the filter would have to be
// deleted, not merely moved, and that is not the defect this finding is about.
func TestTheBatchedOwnerGuardNeverAsksAboutAnEntryTheApproverCannotSee(t *testing.T) {
	dir, ws := ownerPolicyDir(t, DistinctOwner), "ws"
	h := humanApprover("bob", "bob")

	// a stranger's private proposal: visible to its author alone, so not to bob
	private := proposeAs(t, dir, ws, ContextActor{ID: "carol"}, "carol's private")
	entry, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "carol's private", Content: "carol's private note", Permission: "readwrite",
		Visibility: ScopePrivate}}, ContextActor{ID: "carol"})
	if err != nil {
		t.Fatalf("propose carol's private entry: %v", err)
	}
	if ScopesVisible(entry.Scope, entry.ScopeKey, entry.Source, h.Principal.ID, ScopeRequest{}) {
		t.Fatalf("%s is scope %q, so the fixture does not reach the scope check", entry.ID, entry.Scope)
	}
	// a proposal bob may see, so the filter has to admit something
	visible := proposeAs(t, dir, ws, ContextActor{ID: "dave"}, "dave's, visible to bob")

	err = memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		page := make([]govstore.Entry, 0, 3)
		for _, id := range []string{private.ID, entry.ID, visible.ID} {
			e, err := r.GetEntry(context.Background(), id)
			if err != nil {
				return err
			}
			page = append(page, e)
		}

		q := &ApprovalQueue{Approver: h.Principal.ID,
			Skipped: map[string]int{skipOwn: 0, skipVoted: 0, skipSameOwner: 0, skipNotVisible: 0}}
		got := q.visibleCandidates(page)
		for _, c := range got {
			if c.entry.ID == entry.ID {
				return fmt.Errorf("%s is a stranger's private proposal and reached the batched "+
					"owner-distinct read, which loads its revision, edit event and votes", c.entry.ID)
			}
		}
		if q.Skipped[skipNotVisible] != 1 {
			return fmt.Errorf("skipped not_visible = %d, want 1: the private proposal was not counted",
				q.Skipped[skipNotVisible])
		}
		if len(got) != 2 {
			return fmt.Errorf("the filter kept %d of 3 candidates, want 2", len(got))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// and the queue answers on the same terms
	q, err := HumanApprovalQueue(dir, ws, h, 0, 0)
	if err != nil {
		t.Fatalf("the batched guard read an entry the approver cannot see: %v", err)
	}
	if got := q.Skipped[skipNotVisible]; got != 1 {
		t.Fatalf("skipped not_visible = %d, want 1: the private proposal was counted by another reason",
			got)
	}
	for _, it := range q.Items {
		if it.EntryID == entry.ID {
			t.Fatalf("%s is carol's private proposal and was listed in bob's approval queue", entry.ID)
		}
	}
}

// TestTheBatchedOwnerGuardCoversTheLastPartialPageOfAMultiPageQueue guards the case a
// page-boundary loop gets wrong. The queue lists in pages of storeListPage and batches
// the owner-distinct guard per page, so a candidate in the last, short page must be
// guarded like any other; a loop that guarded a whole chunk but let the tail through
// unguarded, or dropped it, would still pass a single-page fixture.
//
// The fixture is storeListPage proposals that bob may see, then one more bob cannot:
// that last one is the tail, and it is refused because bob owns its author. Every row
// before it is somebody else's, so it is the only same-owner row and the only one
// counted as skipped rather than listed.
func TestTheBatchedOwnerGuardCoversTheLastPartialPageOfAMultiPageQueue(t *testing.T) {
	dir, ws := ownerPolicyDir(t, DistinctOwner), "ws"
	h := humanApprover("bob", "bob")

	// enough rows that the queue needs a second page, so the tail is a partial one
	for i := 0; i < storeListPage; i++ {
		proposeAs(t, dir, ws, ContextActor{ID: "author-" + strconv.Itoa(i)}, "the build uses make")
	}
	// the tail: bob owns its author, so only the owner-distinct guard can refuse it
	tail := proposeAs(t, dir, ws, ContextActor{ID: "bob-bot", Owner: "bob"}, "bob's own, in the last page")

	q, err := HumanApprovalQueue(dir, ws, h, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Total counts the rows listed for approval, so the refused tail is not in it: one
	// of the storeListPage+1 rows is bob's own, and the rest are all listed.
	if q.Total != storeListPage {
		t.Fatalf("Total = %d, want %d: a row was capped, withheld or listed across the page "+
			"boundary (skipped %v)", q.Total, storeListPage, q.Skipped)
	}
	if got := q.Skipped[skipSameOwner]; got != 1 {
		t.Fatalf("skipped same_owner = %d, want 1: the last row of the last page was not guarded "+
			"(the queue listed %d of %d)", got, q.Total, q.Total+q.Skipped[skipSameOwner])
	}
	// Items is the rendered page, bounded by limit, so it is not the row count; the tail
	// must be absent from what is rendered, not merely uncounted
	for _, it := range q.Items {
		if it.EntryID == tail.ID {
			t.Fatalf("%s is bob's own proposal and was listed in his own approval queue", tail.ID)
		}
	}
}

// TestTheBatchedOwnerGuardExemptsTheGateAndPolicyThatThePerRowGuardExempts covers the
// two early returns checkOwnerDistinct makes before it reads anything: a verdict that
// does not decide verification, and an entry whose gate asks for one assertion. The
// batched path is the expensive one, so it must reach them too.
func TestTheBatchedOwnerGuardExemptsTheGateAndPolicyThatThePerRowGuardExempts(t *testing.T) {
	dir, ws := ownerPolicyDir(t, DistinctOwner), "ws"
	h := humanApprover("bob", "bob")
	cases := parityFixture(t, dir, ws, h.Principal.ID)

	err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		guarded := candidatesOf(t, r, cases)

		// a verdict that does not decide verification is not owner-bound
		got, err := batchOwnerVoices(context.Background(), r, dir, guarded, h,
			batchVerdict(govstore.VerdictRetract))
		if err != nil {
			return err
		}
		for i, err := range got {
			if err != nil {
				return fmt.Errorf("%s: a retract is not owner-bound, got %v", cases[i].name, err)
			}
		}

		// a single-assertion gate is the operator opting out of peers
		guarded[0].entry.RequiredVerifications = 1
		got, err = batchOwnerVoices(context.Background(), r, dir, guarded[:1], h)
		if err != nil {
			return err
		}
		if got[0] != nil {
			return fmt.Errorf("a single-assertion gate is not owner-gated, got %v", got[0])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestTheBatchedOwnerGuardSkipsEveryCheckUnderTheTokenPolicy is the third early
// return: under the token policy the batched query must read nothing at all.
func TestTheBatchedOwnerGuardSkipsEveryCheckUnderTheTokenPolicy(t *testing.T) {
	dir, ws := ownerPolicyDir(t, DistinctToken), "ws"
	h := humanApprover("bob", "bob")
	cases := parityFixture(t, dir, ws, h.Principal.ID)

	err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		guarded := candidatesOf(t, r, cases)
		got, err := batchOwnerVoices(context.Background(), r, dir, guarded, h)
		if err != nil {
			return err
		}
		for i, err := range got {
			if err != nil {
				return fmt.Errorf("%s: the token policy guards nothing, got %v", cases[i].name, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

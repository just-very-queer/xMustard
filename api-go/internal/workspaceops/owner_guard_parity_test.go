package workspaceops

import (
	"context"
	"errors"
	"fmt"
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

	// the recordedOwner fallback on the REVISION arm. The workspaceops write path always
	// records an owner (storeActor fills it), so the fallback is only reachable for a
	// write made through the store API with no owner on the actor. The token store's
	// answer is then the only thing that ties that revision to bob, so a guard that
	// skipped the fallback would admit the row (mutation M2).
	mintPrincipal(t, dir, "bob-token-only", "bob")
	viaStore := proposeUnrecordedOwner(t, dir, ws, "bob-token-only")
	if got := entryRecordedOwner(t, dir, ws, viaStore); got != "" {
		t.Fatalf("the fixture recorded owner %q on %s, so the fallback is not needed on this arm",
			got, viaStore)
	}
	if got := PrincipalOwner(dir, "bob-token-only"); got != "bob" {
		t.Fatalf("the token store answers %q, want bob, so the arm was not built", got)
	}
	cases = append(cases, guardCase{"revision author through the token store", viaStore, 0})

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

// entryRecordedOwner is the owner the entry's own write recorded, empty when it
// recorded none.
func entryRecordedOwner(t *testing.T, dir, ws, entryID string) string {
	t.Helper()
	var owner string
	if err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		e, err := r.GetEntry(context.Background(), entryID)
		if err != nil {
			return err
		}
		owner = e.SourceOwner
		return nil
	}); err != nil {
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

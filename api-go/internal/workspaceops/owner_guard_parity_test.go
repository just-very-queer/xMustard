package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"xmustard/api-go/internal/govstore"
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

	// the entry's author arm: bob owns alice-bot's proposal, so only that arm can
	// refuse it.
	mine := proposeAs(t, dir, ws, ContextActor{ID: "alice-bot", Owner: "alice"}, "from bob's agent")
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

	return cases
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
	if len(cases) != 6 {
		t.Fatalf("fixture arms: %d, want 6", len(cases))
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
		for i, c := range guarded {
			perRow := checkOwnerDistinct(context.Background(), r, dir, c.entry,
				govstore.VoteInput{EntryID: c.entry.ID, Revision: c.revision, Verdict: govstore.VerdictApprove}, voter)
			switch {
			case errors.Is(perRow, ErrSameOwner):
				refused++
			case perRow != nil:
				return fmt.Errorf("%s: checkOwnerDistinct failed on a readable store: %v", cases[i].name, perRow)
			default:
				allowed++
			}
			if errors.Is(batched[i], ErrSameOwner) != errors.Is(perRow, ErrSameOwner) {
				return fmt.Errorf("%s: the batched guard said %v, checkOwnerDistinct said %v",
					cases[i].name, batched[i], perRow)
			}
		}
		// The arms are asserted by count, so a guard that agreed with a per-row guard
		// because neither reached anything would fail here instead of passing quietly.
		if refused != 3 || allowed != 3 {
			return fmt.Errorf("the fixture reached %d refused and %d allowed arms, want 3 and 3", refused, allowed)
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

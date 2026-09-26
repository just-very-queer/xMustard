package govstore

import (
	"context"
	"errors"
	"testing"
)

// VerificationMode is the one trust-label rule: peer approvals against the entry's
// quorum (or the threshold for a single-assertion entry), then the open-mode
// identity's word, then a single principal's.
func TestVerificationModeRule(t *testing.T) {
	cases := []struct {
		tally               Tally
		required, threshold int
		want                string
	}{
		{Tally{PeerApprovals: 2}, 2, 2, ModePeerVerified},
		{Tally{PeerApprovals: 1, OpenModeApproved: true}, 2, 2, ModeSelfAssertedOpenMode},
		{Tally{PeerApprovals: 1, AuthorApproved: true}, 2, 2, ModeSingleAgent},
		{Tally{PeerApprovals: 1}, 1, 2, ModeSingleAgent}, // one assertion needs the threshold to be peer
		{Tally{PeerApprovals: 2}, 1, 2, ModePeerVerified},
		{Tally{PeerApprovals: 1}, 1, 0, ModePeerVerified},
	}
	for _, c := range cases {
		if got := VerificationMode(c.tally, c.required, c.threshold); got != c.want {
			t.Errorf("VerificationMode(%+v, %d, %d) = %q, want %q", c.tally, c.required, c.threshold, got, c.want)
		}
	}
	// the importer labels through the same rule
	le := LegacyEntry{Source: "alice", RequiredVerifications: 2, Verifications: []LegacyVerification{
		{Agent: "Alice", Approve: true}, {Agent: "bob", Approve: true}, {Agent: OpenModeIdentity, Approve: true},
	}}
	if got := deriveVerificationMode(le, 2); got != ModeSelfAssertedOpenMode {
		t.Fatalf("import label = %q, want %s", got, ModeSelfAssertedOpenMode)
	}
}

// ServedRanking returns only served entries, with approval counts and declared paths,
// and an unserved entry leaves no trace in it.
func TestServedRanking(t *testing.T) {
	s := openTestStore(t, newClock())
	propose(t, s, "r1", "one", "first body", "a.go", "b.go")
	propose(t, s, "r2", "two", "second body")
	vote(t, s, "r1", bob, VerdictApprove)
	vote(t, s, "r1", carol, VerdictApprove)
	promotePeer(t, s, "r1")
	ctx := context.Background()
	got, err := s.ServedRanking(ctx, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "r1" || got[0].Approvals != 2 || len(got[0].Paths) != 2 ||
		got[0].VerificationMode != ModePeerVerified || got[0].ContentDigest != Digest("first body") || got[0].Baselined {
		t.Fatalf("ranking = %+v", got)
	}
	mustUpdate(t, s, func(tx Tx) error {
		return tx.SetBaselines(ctx, "r1", []Baseline{{Kind: AnchorPath, Value: "a.go", State: BaselineMissing}}, "", bob)
	})
	if got, _ = s.ServedRanking(ctx, "ws1"); !got[0].Baselined {
		t.Fatalf("a baselined anchor must be reported: %+v", got)
	}
}

// Raising the gate above the peer approvals of a peer_verified entry breaks the
// invariant and rolls back; a gate below 1 is invalid.
func TestSetRequiredVerificationsKeepsInvariant(t *testing.T) {
	s := openTestStore(t, newClock())
	propose(t, s, "g1", "gate", "gate body")
	vote(t, s, "g1", bob, VerdictApprove)
	vote(t, s, "g1", carol, VerdictApprove)
	promotePeer(t, s, "g1")
	ctx := context.Background()
	err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.SetRequiredVerifications(ctx, "g1", 3, bob)
		return err
	})
	if err == nil {
		t.Fatalf("raising the gate past the approvals of a peer_verified entry must fail, got %v", err)
	}
	if err := s.Update(ctx, func(tx Tx) error { _, err := tx.SetRequiredVerifications(ctx, "g1", 0, bob); return err }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("gate 0 must be invalid, got %v", err)
	}
	var n int
	mustUpdate(t, s, func(tx Tx) error {
		e, err := tx.SetRequiredVerifications(ctx, "g1", 1, bob)
		n = e.RequiredVerifications
		return err
	})
	if n != 1 {
		t.Fatalf("gate = %d, want 1", n)
	}
	evs, err := s.ListEvents(ctx, EventFilter{EntryID: "g1", Types: []string{EventGate}})
	if err != nil || len(evs) != 1 {
		t.Fatalf("a gate change appends one gate event: %+v %v", evs, err)
	}
}

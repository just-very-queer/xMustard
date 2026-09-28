package govstore

import (
	"context"
	"errors"
	"testing"
	"time"
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

// Ranking reads the requested rank states, labels each entry with the state its Go
// twin (Entry.RankState) computes, flags the caller's authorship and votes, and counts
// outcomes on the served revision; MemoryScores ranks with IDF across states.
func TestRankingStatesCallerFlagsAndOutcomes(t *testing.T) {
	clk := newClock()
	s := openTestStore(t, clk)
	ctx := context.Background()
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.InsertEntry(ctx, NewEntry{ID: "x1", WorkspaceID: "ws1", Title: "expiring", Content: "expiring body",
			ExpiresAt: canonTime(clk.Now().Add(time.Hour))}, alice)
		return err
	})
	vote(t, s, "x1", bob, VerdictApprove)
	vote(t, s, "x1", carol, VerdictApprove)
	promotePeer(t, s, "x1")
	clk.Advance(2 * time.Hour)
	propose(t, s, "p1", "pending", "queue body")
	propose(t, s, "p2", "pending two", "queue body rare")
	vote(t, s, "p2", bob, VerdictApprove)
	propose(t, s, "v1", "served", "served body")
	vote(t, s, "v1", bob, VerdictApprove)
	vote(t, s, "v1", carol, VerdictApprove)
	promotePeer(t, s, "v1")
	mustUpdate(t, s, func(tx Tx) error {
		if _, err := tx.RecordOutcome(ctx, OutcomeInput{EntryID: "v1", Outcome: OutcomeHelpful}, bob); err != nil {
			return err
		}
		_, err := tx.RecordOutcome(ctx, OutcomeInput{EntryID: "v1", Outcome: OutcomeStaleHarm}, carol)
		return err
	})
	got, err := s.Ranking(ctx, RankQuery{WorkspaceID: "ws1", States: []string{RankServed, RankPending}, Caller: "BOB"})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]RankEntry{}
	for _, e := range got {
		byID[e.ID] = e
		full, err := s.GetEntry(ctx, e.ID)
		if err != nil || full.RankState(time.Now()) != e.State {
			t.Fatalf("%s: SQL state %q, Go twin %q (%v)", e.ID, e.State, full.RankState(time.Now()), err)
		}
	}
	if expired, err := s.Ranking(ctx, RankQuery{WorkspaceID: "ws1", States: []string{RankServed, RankExpired, RankSuperseded}}); err != nil ||
		len(expired) != 2 || expired[0].ID != "x1" || expired[0].State != RankExpired || expired[1].ID != "v1" {
		t.Fatalf("expired view = %+v %v", expired, err)
	}
	p1, p2, v1 := byID["p1"], byID["p2"], byID["v1"]
	if len(got) != 3 || p1.State != RankPending || v1.State != RankServed || p1.VotedByCaller || !p2.VotedByCaller ||
		p2.PeerApprovals != 1 || v1.Helpful != 1 || v1.StaleHarm != 1 || p1.ByCaller || p1.Author != "alice" {
		t.Fatalf("ranking = %+v", got)
	}
	if served, _ := s.ServedRanking(ctx, "ws1"); len(served) != 1 || served[0].ID != "v1" {
		t.Fatalf("served ranking = %+v", served)
	}
	if _, err := s.Ranking(ctx, RankQuery{WorkspaceID: "ws1", States: []string{"'; DROP TABLE entries; --"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown state: %v", err)
	}
	scores, err := s.MemoryScores(ctx, "ws1", "queue rare")
	if err != nil || len(scores) != 2 || scores["p2"] <= scores["p1"] {
		t.Fatalf("scores = %v %v", scores, err)
	}
}

// Only an entry that was once promoted ranks as superseded: a pending proposal that a
// promoted entry replaced matches no rank state, in SQL and in the Go twin.
func TestRankingSupersededNeedsPromotion(t *testing.T) {
	s := openTestStore(t, newClock())
	ctx := context.Background()
	for _, id := range []string{"old", "raw", "new"} {
		propose(t, s, id, id, id+" body")
	}
	for _, id := range []string{"old", "new"} {
		vote(t, s, id, bob, VerdictApprove)
		vote(t, s, id, carol, VerdictApprove)
		promotePeer(t, s, id)
	}
	mustUpdate(t, s, func(tx Tx) error {
		return tx.Supersede(ctx, SupersedeInput{NewID: "new", OldIDs: []string{"old", "raw"}}, carol)
	})
	got, err := s.Ranking(ctx, RankQuery{WorkspaceID: "ws1", States: []string{RankServed, RankPending, RankSuperseded}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"new": RankServed, "old": RankSuperseded}
	if len(got) != len(want) {
		t.Fatalf("ranking = %+v, want only %v", got, want)
	}
	for _, e := range got {
		if want[e.ID] != e.State {
			t.Fatalf("%s ranks %q, want %q", e.ID, e.State, want[e.ID])
		}
	}
	for _, id := range []string{"old", "raw", "new"} {
		if full, err := s.GetEntry(ctx, id); err != nil || full.RankState(time.Now()) != want[id] {
			t.Fatalf("%s: Go twin %q, want %q (%v)", id, full.RankState(time.Now()), want[id], err)
		}
	}
}

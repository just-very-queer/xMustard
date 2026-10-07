package govstore

import (
	"context"
	"encoding/json"
	"testing"
)

// TestPendingOwnerVoicesReadsTheCountingVerdictsOfManyRevisions pins the batched read
// the owner-distinct guard uses: it must return what ListVotes returns for one revision
// at a time, for every key it was given, and nothing else (WS-FIX-22b).
func TestPendingOwnerVoicesReadsTheCountingVerdictsOfManyRevisions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "ctx_a", "a", "a's content")
	propose(t, s, "ctx_b", "b", "b's content")
	propose(t, s, "ctx_quiet", "q", "no verdicts here")

	vote(t, s, "ctx_a", alice, VerdictApprove)
	vote(t, s, "ctx_a", carol, VerdictReject)
	// a verdict that does not decide verification is not an owner-bound voice
	vote(t, s, "ctx_b", alice, VerdictRetract)
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.RecordVote(ctx, VoteInput{EntryID: "ctx_b", Verdict: VerdictDuplicateOf, Target: "ctx_a"}, carol)
		return err
	})

	var got map[PendingKey][]Vote
	if err := s.View(ctx, func(r Reader) error {
		var err error
		got, err = r.PendingOwnerVoices(ctx, []PendingKey{
			{EntryID: "ctx_a", Revision: 1},
			{EntryID: "ctx_b", Revision: 1},
			{EntryID: "ctx_quiet", Revision: 1},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if n := len(got[PendingKey{EntryID: "ctx_a", Revision: 1}]); n != 2 {
		t.Fatalf("ctx_a voices = %d, want the approve and the reject: %+v", n, got)
	}
	for _, v := range got[PendingKey{EntryID: "ctx_a", Revision: 1}] {
		if v.Verdict != VerdictApprove && v.Verdict != VerdictReject {
			t.Fatalf("a verdict that does not decide verification came back: %+v", v)
		}
	}
	if n := len(got[PendingKey{EntryID: "ctx_b", Revision: 1}]); n != 0 {
		t.Fatalf("ctx_b voices = %d, want 0 (duplicate_of and retract do not decide): %+v", n, got)
	}
	// a key with no counting verdict is present and empty, so a caller can tell
	// "checked, nothing there" from "not checked"
	quiet, ok := got[PendingKey{EntryID: "ctx_quiet", Revision: 1}]
	if !ok || len(quiet) != 0 {
		t.Fatalf("ctx_quiet = %+v (present %v), want a present empty list", quiet, ok)
	}
	if len(got) != 3 {
		t.Fatalf("the result has %d keys, want one per requested key", len(got))
	}
}

// TestPendingOwnerVoicesAgreesWithListVotesRowForRow is the batched read's own parity
// check: for every entry, the batched result must be exactly what ListVotes returns,
// so the guard's batched path cannot see a different set of voices than Verify does.
func TestPendingOwnerVoicesAgreesWithListVotesRowForRow(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	for _, id := range []string{"ctx_a", "ctx_b", "ctx_c", "ctx_d"} {
		propose(t, s, id, id, id+" content")
	}
	vote(t, s, "ctx_a", alice, VerdictApprove)
	vote(t, s, "ctx_b", carol, VerdictReject)
	vote(t, s, "ctx_c", alice, VerdictApprove)
	vote(t, s, "ctx_c", carol, VerdictReject)
	// a superseding verdict replaces the principal's earlier one, as ListVotes shows
	vote(t, s, "ctx_d", alice, VerdictApprove)
	vote(t, s, "ctx_d", alice, VerdictReject)

	var keys []PendingKey
	for _, id := range []string{"ctx_a", "ctx_b", "ctx_c", "ctx_d"} {
		keys = append(keys, PendingKey{EntryID: id, Revision: 1})
	}
	err := s.View(ctx, func(r Reader) error {
		batched, err := r.PendingOwnerVoices(ctx, keys)
		if err != nil {
			return err
		}
		for _, k := range keys {
			perRow, err := r.ListVotes(ctx, k.EntryID, k.Revision)
			if err != nil {
				return err
			}
			// ListVotes also lists the verdicts that do not decide verification, so
			// compare against the owner-bound subset of it, which is what the guard reads
			var want []Vote
			for _, v := range perRow {
				if v.Verdict == VerdictApprove || v.Verdict == VerdictReject {
					want = append(want, v)
				}
			}
			if len(batched[k]) != len(want) {
				t.Fatalf("%s: batched %d voices, ListVotes %d: %+v vs %+v",
					k.EntryID, len(batched[k]), len(want), batched[k], want)
			}
			for i := range want {
				if batched[k][i].Principal != want[i].Principal || batched[k][i].Verdict != want[i].Verdict ||
					batched[k][i].Revision != want[i].Revision || batched[k][i].PrincipalOwner != want[i].PrincipalOwner {
					t.Fatalf("%s voice %d: batched %+v, ListVotes %+v", k.EntryID, i, batched[k][i], want[i])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRevisionOwnersReadsTheHeaderWithoutTheBody pins the other batched read: it
// returns the revision number, the author and the owner the edit event recorded, and
// it must never carry a revision body (the guard that reads it must stay off the
// content path).
func TestRevisionOwnersReadsTheHeaderWithoutTheBody(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "ctx_edit", "e", "the build uses make")
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AppendRevision(ctx, RevisionInput{EntryID: "ctx_edit", BaseRevision: 1, Op: "edit",
			Content: "the build uses make and go", Reason: "add go", Activate: true}, carol)
		return err
	})
	// AppendRevision already appended this revision's edit event, so the batched read
	// sees that one first, exactly as revisionOwner does with Limit: 1
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AppendEvent(ctx, EventInput{WorkspaceID: "ws1", EntryID: "ctx_edit", Type: EventEdit,
			Revision: 2, Data: map[string]any{"provenance": map[string]any{"owner": "ops"}}}, carol)
		return err
	})

	var got map[PendingKey]RevisionOwner
	err := s.View(ctx, func(r Reader) error {
		var err error
		got, err = r.RevisionOwners(ctx, []PendingKey{
			{EntryID: "ctx_edit", Revision: 1},
			{EntryID: "ctx_edit", Revision: 2},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	first, ok := got[PendingKey{EntryID: "ctx_edit", Revision: 1}]
	if !ok || first.Revision != 1 || first.Author != "alice" {
		t.Fatalf("revision 1 = %+v (present %v), want revision 1 by alice", first, ok)
	}
	second, ok := got[PendingKey{EntryID: "ctx_edit", Revision: 2}]
	if !ok || second.Revision != 2 || second.Author != "carol" {
		t.Fatalf("revision 2 = %+v (present %v), want revision 2 by carol", second, ok)
	}
	// the entry's own recorded author fields ride along, because the revision-1 arm of
	// revisionOwner reads them rather than the token store
	if first.Source != "alice" || second.Source != "alice" {
		t.Fatalf("entry source = %q and %q, want alice on both", first.Source, second.Source)
	}

	// the recorded owner must be what the per-row read sees: revisionOwner takes the
	// FIRST edit event for a revision, so the batched read must not read a later one
	var wantOwner string
	err = s.View(ctx, func(r Reader) error {
		events, err := r.ListEvents(ctx, EventFilter{WorkspaceID: "ws1", EntryID: "ctx_edit",
			Types: []string{EventEdit}, Revision: 2, Limit: 1})
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		var parsed struct {
			Provenance struct {
				Owner string `json:"owner"`
			} `json:"provenance"`
		}
		if err := json.Unmarshal(events[0].Data, &parsed); err == nil {
			wantOwner = parsed.Provenance.Owner
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Owner != wantOwner {
		t.Fatalf("revision 2's recorded owner = %q, want the first edit event's %q", second.Owner, wantOwner)
	}

	// The first edit event for a revision is the one revisionOwner reads, so it decides
	// the owner even when it cannot be decoded: the batched read must leave the owner
	// empty rather than let a later event answer for the revision, or it disagrees with
	// Verify about who owns the row. This is the shape a caller-authored AppendEvent
	// produces, since it does not check the event it is about to duplicate.
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AppendRevision(ctx, RevisionInput{EntryID: "ctx_edit", BaseRevision: 2, Op: "edit",
			Content: "the build uses make and go and make", Reason: "add make", Activate: true}, carol)
		return err
	})
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AppendEvent(ctx, EventInput{WorkspaceID: "ws1", EntryID: "ctx_edit", Type: EventEdit,
			Revision: 3, Data: "not an object"}, carol)
		return err
	})
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.AppendEvent(ctx, EventInput{WorkspaceID: "ws1", EntryID: "ctx_edit", Type: EventEdit,
			Revision: 3, Data: map[string]any{"provenance": map[string]any{"owner": "ops"}}}, carol)
		return err
	})

	var third RevisionOwner
	err = s.View(ctx, func(r Reader) error {
		m, err := r.RevisionOwners(ctx, []PendingKey{{EntryID: "ctx_edit", Revision: 3}})
		if err != nil {
			return err
		}
		third = m[PendingKey{EntryID: "ctx_edit", Revision: 3}]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if third.Owner != "" {
		t.Fatalf("revision 3's recorded owner = %q, want empty: a later edit event answered for a "+
			"revision whose first event could not be decoded, which is how the batched read "+
			"disagreed with the per-row one", third.Owner)
	}
}

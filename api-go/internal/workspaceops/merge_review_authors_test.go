package workspaceops

import (
	"context"
	"errors"
	"slices"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// storeReviewRecord writes a review record of the reviewRepo change straight through
// the store, as the review build's `review record` would, and returns its id.
func storeReviewRecord(t *testing.T, dir, ws string, author ContextActor) string {
	t.Helper()
	ctx := context.Background()
	change, err := DiffReviewedChange(ctx, dir, ws, "main", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	err = memoryUpdate(ctx, dir, ws, func(tx govstore.Tx) error {
		rec, _, err := tx.RecordReview(ctx, govstore.ReviewRecordInput{WorkspaceID: ws, Lineage: "main@" + change.MergeBase,
			Change: govstore.ReviewChange{Repository: change.Repository, BaseRef: change.BaseRef, MergeBase: change.MergeBase,
				Head: change.Head, DiffSHA256: change.DiffSHA256, DiffBytes: change.DiffBytes},
			ChangedFiles: []string{"a.go", "b.go"}, Producer: "agent"}, author.storeActor(change.Repository))
		id = rec.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// WS-57 binds review record ids as given; once review records exist (WS-66) the
// approver may not rest a merge on a record they wrote or one written in open mode, nor,
// under the owner-distinct policy, on one written under their owner. An id that names no
// record stays bound and is listed as unknown.
func TestMergeApprovalReviewAuthorsAreDistinct(t *testing.T) {
	ctx := context.Background()
	dir, ws, _, _ := reviewRepo(t)
	h := trustedApprover(t, dir, "alice", "team")
	own := storeReviewRecord(t, dir, ws, ContextActor{ID: "alice", Owner: "team", Kind: PrincipalHuman})
	teammate := storeReviewRecord(t, dir, ws, ContextActor{ID: "bot", Owner: "team", Kind: PrincipalAgent})
	peer := storeReviewRecord(t, dir, ws, ContextActor{ID: "carol", Owner: "carol", Kind: PrincipalAgent})

	anonymous := storeReviewRecord(t, dir, ws, ContextActor{ID: OpenModeIdentity, OpenMode: true})
	for name, ids := range map[string][]string{"the approver's own review": {peer, own}, "an open-mode review": {anonymous}} {
		if _, err := ApproveMerge(ctx, dir, ws, h, "main", "HEAD", ids, ""); !errors.Is(err, ErrReviewerNotDistinct) {
			t.Fatalf("resting on %s: %v", name, err)
		}
	}
	a, err := ApproveMerge(ctx, dir, ws, h, "main", "HEAD", []string{peer, teammate, "external-review-7"}, "")
	if err != nil {
		t.Fatalf("token policy, a same-owner reviewer: %v", err)
	}
	if !slices.Equal(a.ReviewRecords, []string{peer, teammate, "external-review-7"}) ||
		!slices.Equal(a.ReviewRecordsUnknown, []string{"external-review-7"}) {
		t.Fatalf("attestation records %v, unknown %v", a.ReviewRecords, a.ReviewRecordsUnknown)
	}
	if st := mergeState(t, dir, ws); st.Status != MergeApprovalCurrent || !slices.Equal(st.Approval.ReviewRecordsUnknown, []string{"external-review-7"}) {
		t.Fatalf("gate: %+v", st)
	}

	writeTestSettings(t, dir, appSettings{PrincipalDistinctness: DistinctOwner})
	if _, err := ApproveMerge(ctx, dir, ws, h, "main", "HEAD", []string{teammate}, ""); !errors.Is(err, ErrReviewerNotDistinct) {
		t.Fatalf("owner policy, a same-owner reviewer: %v", err)
	}
	if _, err := ApproveMerge(ctx, dir, ws, h, "main", "HEAD", []string{peer}, ""); err != nil {
		t.Fatalf("owner policy, a distinct reviewer: %v", err)
	}
}

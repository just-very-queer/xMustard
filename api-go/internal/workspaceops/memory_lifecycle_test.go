package workspaceops

import (
	"errors"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// multiAgentDir is a data dir with a two-principal quorum.
func multiAgentDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require := true
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &require, ContextVerificationThreshold: 2})
	return dir
}

// promoted proposes content as author and has two peers approve it.
func promoted(t *testing.T, dir, ws, author, content string, extra ...func(*RememberRequest)) *ContextEntry {
	t.Helper()
	req := RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: content, Permission: "readwrite"}}
	for _, f := range extra {
		f(&req)
	}
	e, err := Remember(dir, ws, req, ContextActor{ID: author})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	for _, peer := range []string{"peer-1", "peer-2"} {
		if e, err = VerifyContext(dir, ws, e.ID, peer, true, ""); err != nil {
			t.Fatalf("verify: %v", err)
		}
	}
	if !e.Promoted || e.VerificationMode != VerificationPeer {
		t.Fatalf("not peer-verified: %+v", e)
	}
	return e
}

func entryView(t *testing.T, dir, ws, id string, history bool) (ContextEntry, map[string]any) {
	t.Helper()
	res, err := GetContextEntry(dir, ws, id, history)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return res["entry"].(ContextEntry), res
}

func recalledIDs(t *testing.T, dir, ws string) map[string]bool {
	t.Helper()
	res, err := RecallContext(dir, ws, "", nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, e := range res["entries"].([]ContextEntry) {
		out[e.ID] = true
	}
	return out
}

func TestSupersedeAppliesAtomicallyOnPromotion(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	old := promoted(t, dir, ws, "author", "the build uses make")
	newer, err := Remember(dir, ws, RememberRequest{Op: "supersede", ProposeContextRequest: ProposeContextRequest{
		Title: "t", Content: "the build uses just", Supersedes: []string{old.ID}}}, ContextActor{ID: "author"})
	if err != nil {
		t.Fatal(err)
	}
	if len(newer.Supersedes) != 1 {
		t.Fatalf("pending supersession not shown: %+v", newer)
	}
	// nothing changes while the replacement is pending
	if got, _ := entryView(t, dir, ws, old.ID, false); got.Lifecycle != "" || !got.Promoted {
		t.Fatalf("old entry changed before the replacement was promoted: %+v", got)
	}
	VerifyContext(dir, ws, newer.ID, "peer-1", true, "")
	after, err := VerifyContext(dir, ws, newer.ID, "peer-2", true, "")
	if err != nil || !after.Promoted || len(after.Supersedes) != 0 {
		t.Fatalf("promotion: %v %+v", err, after)
	}
	got, res := entryView(t, dir, ws, old.ID, true)
	if got.Lifecycle != govstore.LifecycleSuperseded || got.SupersededBy != newer.ID || got.InvalidatedAt == "" {
		t.Fatalf("old entry not superseded: %+v", got)
	}
	if got.Content != "the build uses make" || len(res["revisions"].([]govstore.Revision)) != 1 {
		t.Fatalf("superseded history lost: %+v", res)
	}
	if ids := recalledIDs(t, dir, ws); ids[old.ID] || !ids[newer.ID] {
		t.Fatalf("recall serves %v", ids)
	}
	if _, err := Remember(dir, ws, RememberRequest{Op: "supersede", ProposeContextRequest: ProposeContextRequest{Content: "x"}},
		ContextActor{ID: "author"}); !IsInvalidInput(err) {
		t.Fatalf("supersede without targets: %v", err)
	}
}

func TestEditIsPendingRevisionWithCASAndDiff(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	e := promoted(t, dir, ws, "author", "line one\nport is 8042\nline three")
	edit := func(req EditRequest) (*ContextEntry, error) {
		return Remember(dir, ws, RememberRequest{Op: "edit", EntryID: e.ID, BaseRevision: req.BaseRevision, Reason: req.Reason,
			OldString: req.OldString, NewString: req.NewString}, ContextActor{ID: "author"})
	}
	if _, err := edit(EditRequest{BaseRevision: 1, OldString: "8042", NewString: "9000"}); !IsInvalidInput(err) {
		t.Fatalf("an edit without a reason: %v", err)
	}
	if _, err := edit(EditRequest{Reason: "r", OldString: "8042", NewString: "9000"}); !IsInvalidInput(err) {
		t.Fatalf("an edit without base_revision: %v", err)
	}
	if _, err := edit(EditRequest{BaseRevision: 1, Reason: "r", OldString: "8042", NewString: "8042"}); !IsInvalidInput(err) {
		t.Fatalf("a no-op edit: %v", err)
	}
	pending, err := edit(EditRequest{BaseRevision: 1, Reason: "port moved", OldString: "8042", NewString: "9000"})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Revision != 1 || pending.PendingRevision != 2 || !pending.Promoted {
		t.Fatalf("the edit must stay pending while revision 1 is served: %+v", pending)
	}
	// a stale base is a conflict naming the current revision and digest
	_, err = edit(EditRequest{BaseRevision: 1, Reason: "again", OldString: "line one", NewString: "line 1"})
	var ce *govstore.ConflictError
	if de, ok := AsDomainError(err); !ok || de.Class != ClassConflict || !errors.As(err, &ce) ||
		ce.CurrentRevision != 1 || ce.HeadRevision != 2 || ce.CurrentDigest == "" || !strings.Contains(de.Public, ce.CurrentDigest) {
		t.Fatalf("stale base: %v", err)
	}
	// the author's own approval of their edit is not a peer's
	voteRev := func(voter string) *ContextEntry {
		t.Helper()
		out, err := VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: voter}, VerifyRequest{Outcome: OutcomeApprove, Revision: 2})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := voteRev("author"); got.Revision != 1 {
		t.Fatalf("the author accepted their own edit: %+v", got)
	}
	got := voteRev("peer-1")
	if got.Revision != 1 || !strings.Contains(got.Diff, "-port is 8042\n+port is 9000") {
		t.Fatalf("one peer must not accept, and the vote returns the diff: %+v", got)
	}
	_, view := entryView(t, dir, ws, e.ID, false)
	if p := view["pending"].(map[string]any); p["revision"] != int64(2) || !strings.Contains(p["diff"].(string), "+port is 9000") {
		t.Fatalf("pending view: %+v", view["pending"])
	}
	got = voteRev("peer-2")
	if got.Revision != 2 || got.PendingRevision != 0 || !got.Promoted || got.VerificationMode != VerificationPeer {
		t.Fatalf("two peers accept and promote revision 2: %+v", got)
	}
	served, _ := entryView(t, dir, ws, e.ID, false)
	if served.Content != "line one\nport is 9000\nline three" {
		t.Fatalf("served content: %q", served.Content)
	}
	// a verdict binds to its revision: voting on a revision that is neither served nor pending fails
	if _, err := VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: "peer-3"}, VerifyRequest{Revision: 1}); err == nil {
		t.Fatal("a vote on a superseded revision was accepted")
	}
}

func TestEditRejectedByPeersKeepsServedRevision(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	e := promoted(t, dir, ws, "author", "alpha")
	if _, err := Remember(dir, ws, RememberRequest{Op: "edit", EntryID: e.ID, BaseRevision: 1, Reason: "r", NewString: "beta"},
		ContextActor{ID: "author"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"peer-1", "peer-2"} {
		VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: p}, VerifyRequest{Outcome: OutcomeReject, Revision: 2})
	}
	got, _ := entryView(t, dir, ws, e.ID, false)
	if got.Revision != 1 || got.PendingRevision != 0 || !got.Promoted || got.Content != "alpha" {
		t.Fatalf("rejected edit changed the entry: %+v", got)
	}
}

func TestRetireAndRetractAreGoverned(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	e := promoted(t, dir, ws, "author", "use tabs")
	// retiring promoted memory takes the same quorum that verified it
	got, err := Remember(dir, ws, RememberRequest{Op: "retire", EntryID: e.ID, Reason: "obsolete"}, ContextActor{ID: "author"})
	if err != nil || got.Lifecycle != "" {
		t.Fatalf("one retire must not remove shared memory: %v %+v", err, got)
	}
	if _, err := Remember(dir, ws, RememberRequest{Op: "retire", EntryID: e.ID}, ContextActor{ID: "peer-1"}); !IsInvalidInput(err) {
		t.Fatalf("retire without a reason: %v", err)
	}
	got, err = VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: "peer-1"}, VerifyRequest{Outcome: OutcomeRetract, Note: "wrong"})
	if err != nil || got.Lifecycle != govstore.LifecycleRetracted {
		t.Fatalf("two retractions retract: %v %+v", err, got)
	}
	if recalledIDs(t, dir, ws)[e.ID] {
		t.Fatal("a retracted entry is still recalled")
	}
	// the author withdraws an unpromoted proposal at once
	own, _ := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: "draft"}}, ContextActor{ID: "author"})
	if _, err := Remember(dir, ws, RememberRequest{Op: "retire", EntryID: own.ID, Reason: "r"}, ContextActor{ID: "peer-1"}); err != nil {
		t.Fatal(err) // a peer's retire of a pending entry is a retract vote
	}
	got, err = Remember(dir, ws, RememberRequest{Op: "retire", EntryID: own.ID, Reason: "draft"}, ContextActor{ID: "author"})
	if err != nil || got.Lifecycle != govstore.LifecycleArchived {
		t.Fatalf("author retire of an unpromoted entry: %v %+v", err, got)
	}
	// restoring retired memory needs an approver, and a retracted entry comes back unverified
	if _, err := Remember(dir, ws, RememberRequest{Op: "restore", EntryID: e.ID, Reason: "r"}, ContextActor{ID: "author"}); !errors.Is(err, ErrApproverRequired) {
		t.Fatalf("author restore: %v", err)
	}
	got, err = RestoreContext(dir, ws, e.ID, "was right", ContextActor{ID: "human", Approver: true})
	if err != nil || got.Lifecycle != "" || got.Promoted {
		t.Fatalf("approver restore: %v %+v", err, got)
	}
}

func TestPurgeIsApproverOnlyAndLeavesDigestTombstone(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	e := promoted(t, dir, ws, "author", "token is sk-secret")
	if _, err := PurgeContext(dir, ws, e.ID, "secret", ContextActor{ID: "author"}); !errors.Is(err, ErrApproverRequired) {
		t.Fatalf("non-approver purge: %v", err)
	}
	if _, err := RetractContext(dir, ws, e.ID, "secret", ContextActor{ID: "peer-1"}); !errors.Is(err, ErrApproverRequired) {
		t.Fatalf("non-approver retract: %v", err)
	}
	if _, err := PurgeContext(dir, ws, e.ID, "", ContextActor{ID: "admin", Admin: true}); !IsInvalidInput(err) {
		t.Fatalf("purge without a reason: %v", err)
	}
	got, err := PurgeContext(dir, ws, e.ID, "leaked credential", ContextActor{ID: "admin", Admin: true})
	if err != nil || got.Lifecycle != govstore.LifecyclePurged {
		t.Fatalf("purge: %v %+v", err, got)
	}
	entry, res := entryView(t, dir, ws, e.ID, true)
	if entry.Content != "" || res["content_withheld"] == nil || len(res["content_digest"].(string)) != 64 {
		t.Fatalf("tombstone: %+v", res)
	}
	revs := res["revisions"].([]govstore.Revision)
	if len(revs) != 1 || revs[0].ContentDigest == "" || !revs[0].ContentDropped {
		t.Fatalf("revision digests must survive the purge: %+v", revs)
	}
	events := res["events"].([]govstore.Event)
	if last := events[len(events)-1]; last.Type != govstore.EventPurge {
		t.Fatalf("history does not end in the purge: %+v", last)
	}
	if _, err := RestoreContext(dir, ws, e.ID, "undo", ContextActor{ID: "admin", Admin: true}); err == nil {
		t.Fatal("a purged entry was restored")
	}
}

func TestExpiredEntriesHiddenFetchableRestorable(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	e := promoted(t, dir, ws, "author", "freeze until release", func(r *RememberRequest) { r.Expires = "2001-01-01" })
	if !strings.HasPrefix(e.ExpiresAt, "2001-01-02T00:00:00") {
		t.Fatalf("a date expires after that day (inclusive): %q", e.ExpiresAt)
	}
	if recalledIDs(t, dir, ws)[e.ID] {
		t.Fatal("an expired entry is recalled")
	}
	if active, _ := GetActiveContext(dir, ws); active["active_count"].(int) != 0 {
		t.Fatalf("an expired entry is listed as active: %v", active["active_count"])
	}
	if got, _ := entryView(t, dir, ws, e.ID, false); got.Content != "freeze until release" {
		t.Fatalf("expired entry not fetchable by id: %+v", got)
	}
	if _, err := Remember(dir, ws, RememberRequest{Op: "restore", EntryID: e.ID, Reason: "extended"}, ContextActor{ID: "peer-1"}); !errors.Is(err, ErrNotEntryAuthor) {
		t.Fatalf("a non-author cleared the expiry: %v", err)
	}
	got, err := Remember(dir, ws, RememberRequest{Op: "restore", EntryID: e.ID, Reason: "extended"}, ContextActor{ID: "author"})
	if err != nil || got.ExpiresAt != "" || !got.Promoted {
		t.Fatalf("restore: %v %+v", err, got)
	}
	if !recalledIDs(t, dir, ws)[e.ID] {
		t.Fatal("a restored entry is not recalled")
	}
	// a malformed expiry fails open with a warning
	bad, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: "x", Expires: "next week"}}, ContextActor{ID: "author"})
	if err != nil || bad.ExpiresAt != "" || len(bad.Warnings) != 1 {
		t.Fatalf("malformed expiry: %v %+v", err, bad)
	}
}

func TestRememberRejectsUnknownOp(t *testing.T) {
	if _, err := Remember(t.TempDir(), "ws", RememberRequest{Op: "delete"}, ContextActor{ID: "a"}); !IsInvalidInput(err) {
		t.Fatalf("unknown op: %v", err)
	}
}

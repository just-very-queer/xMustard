package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/govstore"
)

// WS-57: the human-approval surface. A human approver's verdict is one more distinct
// principal's, labelled with where it was made and how its token was read; the queue
// shows what awaits that approver; nothing trusts a token that is not a human
// approver's.

func humanApprover(id, owner string) HumanApprover {
	return HumanApprover{Principal: Principal{ID: id, Owner: owner, Kind: PrincipalHuman, Role: RoleHumanApprover,
		Roles: ExpandRoles(RoleHumanApprover)}, Assurance: AssuranceUserPresence}
}

func proposeAs(t *testing.T, dir, ws string, author ContextActor, content string) *ContextEntry {
	t.Helper()
	e, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t",
		Content: content, Permission: "readwrite"}}, author)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	return e
}

// voteProvenance is the provenance of the latest vote event of principal on entryID.
func voteProvenance(t *testing.T, dir, ws, entryID, principal string) map[string]any {
	t.Helper()
	var out map[string]any
	err := memoryView(context.Background(), dir, ws, func(r govstore.Reader) error {
		evs, err := r.ListEvents(context.Background(), govstore.EventFilter{EntryID: entryID,
			Types: []string{govstore.EventVote, govstore.EventReject}})
		for _, ev := range evs {
			if ev.Principal == principal {
				var d struct {
					Provenance map[string]any `json:"provenance"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return err
				}
				out = d.Provenance
			}
		}
		return err
	})
	if err != nil || out == nil {
		t.Fatalf("no vote event of %s on %s: %v", principal, entryID, err)
	}
	return out
}

func TestAuthorizeHumanApproverFailsClosed(t *testing.T) {
	dir, ws := t.TempDir(), "ws"
	mint := func(id, role string, scope []string, kind string) string {
		t.Helper()
		raw, err := MintIdentityToken(dir, id, role, 0, scope, TokenIdentity{Owner: id, Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	good := mint("alice", RoleHumanApprover, nil, PrincipalHuman)
	admin := mint("root-human", RoleAdmin, nil, PrincipalHuman)
	agentApprover := mint("bot", RoleHumanApprover, nil, PrincipalAgent)
	humanVerifier := mint("bob", RoleVerifier, nil, PrincipalHuman)
	otherScope := mint("carol", RoleHumanApprover, []string{"other"}, PrincipalHuman)
	revoked := mint("dave", RoleHumanApprover, nil, PrincipalHuman)
	if err := RevokeToken(dir, "dave"); err != nil {
		t.Fatal(err)
	}
	presence, err := MintIdentityToken(dir, "erin", RoleHumanApprover, 0, nil,
		TokenIdentity{Owner: "erin", Kind: PrincipalHuman, PresenceOnly: true})
	if err != nil {
		t.Fatal(err)
	}

	deniedThrottleMu.Lock() // the process-global denied-event throttle: order-independent
	lastDeniedWrite, deniedSuppressed = time.Time{}, 0
	deniedThrottleMu.Unlock()
	// the assurance belongs to the token: an ordinary token is advisory even when typed,
	// a presence-only one is user presence and only when typed
	for _, tc := range []struct{ raw, source, label string }{
		{good, TokenTyped, "ops/advisory"}, {good, TokenFile, "ops/advisory"}, {admin, TokenEnviron, "ops/advisory"},
		{presence, TokenTyped, "ops/user_presence"},
	} {
		h, err := AuthorizeHumanApprover(dir, ws, tc.raw, tc.source)
		if err != nil || h.Label() != tc.label {
			t.Fatalf("%s from the %s: want %s, got %v %+v", h.Principal.ID, tc.source, tc.label, err, h)
		}
	}
	for name, tc := range map[string]struct{ raw, source, why string }{
		"no token":               {"", TokenTyped, "no token was given"},
		"unknown token":          {"xmt_not-a-token", TokenTyped, "unknown, revoked or expired"},
		"revoked token":          {revoked, TokenTyped, "unknown, revoked or expired"},
		"agent kind":             {agentApprover, TokenTyped, "of kind agent, not human"},
		"missing role":           {humanVerifier, TokenTyped, "lacks the human-approver role"},
		"other workspace":        {otherScope, TokenTyped, "not scoped to workspace ws"},
		"presence-only in env":   {presence, TokenEnviron, "presence-only token, accepted only when typed"},
		"presence-only in files": {presence, TokenFile, "read from the file"},
	} {
		_, err := AuthorizeHumanApprover(dir, ws, tc.raw, tc.source)
		if !errors.Is(err, ErrHumanApproverRequired) || !strings.Contains(err.Error(), tc.why) {
			t.Fatalf("%s: want a refusal naming %q, got %v", name, tc.why, err)
		}
	}
	if _, err := AuthorizeHumanApprover(dir, ws, good, "clipboard"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unknown token source was accepted: %v", err)
	}
	if _, err := AuthorizeHumanApprover(dir, "../x", good, TokenTyped); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unsafe workspace id was accepted: %v", err)
	}
	// only a human's token is presence-only, and rotation keeps it presence-only
	if _, err := MintIdentityToken(dir, "bot2", RoleHumanApprover, 0, nil, TokenIdentity{PresenceOnly: true}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an agent's token was minted presence-only: %v", err)
	}
	rotated, err := RotateToken(dir, "erin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p := ResolveToken(dir, rotated); p == nil || !p.PresenceOnly {
		t.Fatalf("rotation dropped presence-only: %+v", p)
	}
	denied := 0
	for _, ev := range ListAuthAudit(dir, 0) {
		if ev.Action == "denied" && strings.HasPrefix(ev.Detail, "human approval:") {
			denied++
		}
	}
	if denied == 0 {
		t.Fatal("refusals were not audited")
	}
}

func TestHumanVerdictIsOneDistinctLabelledVote(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	e := proposeAs(t, dir, ws, ContextActor{ID: "author"}, "the build uses make")
	h := humanApprover("alice", "alice")
	got, err := HumanVerdict(dir, ws, e.ID, h, OutcomeApprove, 0, "read it")
	if err != nil {
		t.Fatal(err)
	}
	if got.Promoted {
		t.Fatalf("one human approval promoted a two-peer gate: %+v", got)
	}
	prov := voteProvenance(t, dir, ws, e.ID, "alice")
	if prov["approval"] != "ops/user_presence" || prov["kind"] != PrincipalHuman || prov["owner"] != "alice" {
		t.Fatalf("the vote does not record the human approval: %v", prov)
	}
	got, err = VerifyContext(dir, ws, e.ID, "peer-1", true, "")
	if err != nil || !got.Promoted || got.VerificationMode != VerificationPeer {
		t.Fatalf("a peer did not complete the quorum with the human: %v %+v", err, got)
	}

	// a human reject is a reject
	e2 := proposeAs(t, dir, ws, ContextActor{ID: "author"}, "the build uses bazel")
	h.Assurance = AssuranceAdvisory
	if _, err := HumanVerdict(dir, ws, e2.ID, h, OutcomeReject, 0, "wrong"); err != nil {
		t.Fatal(err)
	}
	if prov := voteProvenance(t, dir, ws, e2.ID, "alice"); prov["approval"] != "ops/advisory" {
		t.Fatalf("an advisory token recorded %v", prov["approval"])
	}
	if _, err := HumanVerdict(dir, ws, e2.ID, h, OutcomeApprove, -1, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a negative revision was accepted: %v", err)
	}
	if _, err := HumanVerdict(dir, ws, e2.ID, h, OutcomeRetract, 0, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a human approver cast %s: %v", OutcomeRetract, err)
	}
}

func TestHumanVerdictKeepsPrincipalsDistinct(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	h := humanApprover("alice", "alice")
	own := proposeAs(t, dir, ws, ContextActor{ID: "alice"}, "mine")
	if _, err := HumanVerdict(dir, ws, own.ID, h, OutcomeApprove, 0, ""); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("a human approved their own proposal: %v", err)
	}
	// an edit the approver wrote on someone else's memory
	e := promoted(t, dir, ws, "author", "the build uses make")
	if _, err := EditContext(dir, ws, e.ID, EditRequest{BaseRevision: 1, Reason: "fix", NewString: " and go"},
		ContextActor{ID: "alice", Admin: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := HumanVerdict(dir, ws, e.ID, h, OutcomeApprove, 2, ""); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("a human approved their own edit: %v", err)
	}
	// under the owner-distinct policy a human cannot approve their own agent's memory
	dir = ownerPolicyDir(t, DistinctOwner)
	mine := proposeAs(t, dir, ws, ContextActor{ID: "alice-bot", Owner: "alice"}, "from my agent")
	if _, err := HumanVerdict(dir, ws, mine.ID, h, OutcomeApprove, 0, ""); !errors.Is(err, ErrSameOwner) {
		t.Fatalf("owner policy: %v", err)
	}
}

func TestHumanApprovalQueueListsWhatAwaitsTheApprover(t *testing.T) {
	dir, ws := ownerPolicyDir(t, DistinctOwner), "ws"
	h := humanApprover("alice", "alice")
	waiting := proposeAs(t, dir, ws, ContextActor{ID: "author"}, strings.Repeat("x", maxQueueContent+10))
	proposeAs(t, dir, ws, ContextActor{ID: "alice"}, "mine")                           // own
	proposeAs(t, dir, ws, ContextActor{ID: "alice-bot", Owner: "alice"}, "my agent's") // same owner
	voted := proposeAs(t, dir, ws, ContextActor{ID: "author"}, "already voted")
	if _, err := HumanVerdict(dir, ws, voted.ID, h, OutcomeApprove, 0, ""); err != nil {
		t.Fatal(err)
	}
	edited := promoted(t, dir, ws, "author", "the build uses make")
	if _, err := EditContext(dir, ws, edited.ID, EditRequest{BaseRevision: 1, Reason: "add go", NewString: " and go"},
		ContextActor{ID: "author"}); err != nil {
		t.Fatal(err)
	}
	// another human already approved the waiting proposal
	if _, err := HumanVerdict(dir, ws, waiting.ID, humanApprover("bob", "bob"), OutcomeApprove, 0, ""); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		appendBaselineEvent(t, dir, ws, fmt.Sprintf("h%d", i))
	}

	q, err := HumanApprovalQueue(dir, ws, h, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if q.Total != 2 || len(q.Items) != 2 {
		t.Fatalf("want the waiting proposal and the edit, got %d: %+v", q.Total, q.Items)
	}
	want := map[string]int{skipOwn: 1, skipVoted: 1, skipSameOwner: 1}
	for k, n := range want {
		if q.Skipped[k] != n {
			t.Fatalf("skipped %s = %d, want %d (%v)", k, q.Skipped[k], n, q.Skipped)
		}
	}
	prop, edit := q.Items[0], q.Items[1]
	if prop.EntryID != waiting.ID || prop.Kind != "proposal" || !prop.ContentTruncated || len(prop.Content) != maxQueueContent ||
		prop.VotesNeeded != 1 || len(prop.HumanApprovals) != 1 || prop.HumanApprovals[0] != "bob" {
		t.Fatalf("proposal item: %+v", prop)
	}
	if edit.EntryID != edited.ID || edit.Kind != "edit" || edit.Revision != 2 || edit.Reason != "add go" ||
		!strings.Contains(edit.Diff, "and go") || edit.VotesNeeded != 2 {
		t.Fatalf("edit item: %+v", edit)
	}
	if len(q.IndexBaselines) != 2 || q.IndexBaselines[0].Head != "h1" || q.IndexBaselines[1].Head != "h2" ||
		q.IndexBaselines[1].Reason != "head_changed" || !q.IndexBaselines[1].Auto {
		t.Fatalf("want the two newest index baselines, oldest first: %+v", q.IndexBaselines)
	}

	// the limit bounds the items rendered, not the total
	if q, err = HumanApprovalQueue(dir, ws, h, 1, 0); err != nil || q.Total != 2 || len(q.Items) != 1 || len(q.IndexBaselines) != 3 {
		t.Fatalf("limit: %v %+v", err, q)
	}
}

// The queue is ordered by when the pending revision was written, not by entry: a
// recent edit of an old entry comes after an older proposal, and the limit keeps the
// oldest.
func TestHumanApprovalQueueIsOldestPendingRevisionFirst(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	h := humanApprover("alice", "alice")
	old := promoted(t, dir, ws, "author", "the build uses make")
	waiting := proposeAs(t, dir, ws, ContextActor{ID: "author"}, "tests need docker")
	time.Sleep(2 * time.Millisecond) // the edit is strictly younger than the proposal
	if _, err := EditContext(dir, ws, old.ID, EditRequest{BaseRevision: 1, Reason: "add go", NewString: " and go"},
		ContextActor{ID: "author"}); err != nil {
		t.Fatal(err)
	}
	q, err := HumanApprovalQueue(dir, ws, h, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if q.Total != 2 || q.Items[0].EntryID != waiting.ID || q.Items[1].EntryID != old.ID || q.Items[1].Kind != "edit" {
		t.Fatalf("want the older proposal before the newer edit: %+v", q.Items)
	}
	if q, err = HumanApprovalQueue(dir, ws, h, 1, 0); err != nil || q.Total != 2 || len(q.Items) != 1 || q.Items[0].EntryID != waiting.ID {
		t.Fatalf("the limit must keep the oldest: %v %+v", err, q)
	}
}

func appendBaselineEvent(t *testing.T, dir, ws, head string) {
	t.Helper()
	err := memoryUpdate(context.Background(), dir, ws, func(tx govstore.Tx) error {
		_, err := tx.AppendEvent(context.Background(), govstore.EventInput{WorkspaceID: ws, Type: govstore.EventIndexBaseline,
			Data: map[string]any{"auto": true, "reason": "head_changed", "head": head, "replaced": true}},
			govstore.Actor{Principal: "xmustard:auto-baseline"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

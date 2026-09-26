package workspaceops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/govstore"
)

// retainedEvidence captures an original in the workspace's evidence store and returns
// its handle, issued to actor ("" for unauthenticated issuance).
func retainedEvidence(t *testing.T, dir, ws, actor string) string {
	t.Helper()
	s := evidence.NewStore(dir, evidence.DefaultLimits())
	sp, err := s.NewSpool(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Write([]byte("--- FAIL: TestX\n")); err != nil {
		t.Fatal(err)
	}
	d, err := s.Capture(context.Background(), sp, evidence.CaptureRequest{WorkspaceID: ws, Tool: "why_failed",
		ContentType: "text/plain", Actor: actor, AuthEnforced: actor != "", Retain: true})
	if err != nil || d.Handle == "" {
		t.Fatalf("capture: %v %+v", err, d)
	}
	return d.Handle
}

func ownerPolicyDir(t *testing.T, policy string) string {
	t.Helper()
	dir := t.TempDir()
	require := true
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &require, ContextVerificationThreshold: 2,
		PrincipalDistinctness: policy})
	return dir
}

func TestProvenanceBoundAtProposeAndPromote(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	handle := retainedEvidence(t, dir, ws, "author")
	author := ContextActor{ID: "author", Owner: "team-a", Kind: PrincipalAgent, SessionID: "mcp-1", CallID: "7"}
	e, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "the build uses make"},
		Evidence: []string{handle}}, author)
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range []string{"peer-1", "peer-2"} {
		if _, err := VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: peer, SessionID: "mcp-" + peer}, VerifyRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	_, res := entryView(t, dir, ws, e.ID, true)
	prov := res["provenance"].(map[string]any)
	if prov["session_id"] != "mcp-1" || prov["source_owner"] != "team-a" || prov["promote"] == nil || prov["propose"] == nil {
		t.Fatalf("provenance: %+v", prov)
	}
	derived := prov["derived_from"].(map[string]any)
	if derived["call_id"] != "7" || len(derived["evidence"].([]any)) != 1 || derived["evidence"].([]any)[0] != handle {
		t.Fatalf("propose event provenance: %+v", derived)
	}
	basis := res["verification_basis"].([]map[string]any)
	if len(basis) != 2 || basis[0]["session_id"] != "mcp-peer-1" || basis[0]["owner"] != "peer-1" {
		t.Fatalf("verification basis: %+v", basis)
	}
}

func TestProvenanceInputsFailClosed(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	foreign := retainedEvidence(t, dir, ws, "someone-else")
	cases := map[string]RememberRequest{
		"unknown handle": {Evidence: []string{"ev_0123456789abcdef0123456789abcdef"}},
		"foreign handle": {Evidence: []string{foreign}},
		"unknown run":    {RunID: "run_missing"},
		"too many":       {Evidence: strings.Split("a,b,c,d,e,f,g,h,i", ",")},
	}
	for name, req := range cases {
		req.ProposeContextRequest = ProposeContextRequest{Title: "t", Content: "c"}
		if _, err := Remember(dir, ws, req, ContextActor{ID: "author"}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: want invalid input, got %v", name, err)
		}
	}
	if entries, _ := loadContextEntries(dir, ws); len(entries) != 0 {
		t.Fatalf("a refused write stored something: %+v", entries)
	}
}

func TestRememberRedactsSecretsBeforeStoring(t *testing.T) {
	dir, ws := t.TempDir(), "ws"
	secret := "ghp_" + strings.Repeat("aB3dE5", 6)
	e, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "deploy",
		Content: "deploy with token " + secret}}, ContextActor{ID: "author"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Redactions == nil || e.Redactions.Count != 1 || e.Redactions.Rules["github_token"] != 1 {
		t.Fatalf("redaction not reported: %+v", e.Redactions)
	}
	if len(e.Warnings) == 0 || !strings.Contains(e.Warnings[0], "github_token") {
		t.Fatalf("warning does not name the pattern: %v", e.Warnings)
	}
	got, res := entryView(t, dir, ws, e.ID, true)
	if strings.Contains(got.Content, secret) || !strings.Contains(got.Content, "[REDACTED:github_token]") {
		t.Fatalf("stored content: %q", got.Content)
	}
	for _, rv := range res["revisions"].([]govstore.Revision) {
		if strings.Contains(rv.Content, secret) {
			t.Fatalf("secret kept in history")
		}
	}
	// an edit's replacement text and a vote's note are scrubbed too
	e2, err := Remember(dir, ws, RememberRequest{Op: "edit", EntryID: e.ID, BaseRevision: 1, Reason: "rotate " + secret,
		NewString: "backup " + secret}, ContextActor{ID: "author"})
	if err != nil || e2.Redactions == nil || e2.Redactions.Count != 2 {
		t.Fatalf("edit redaction: %v %+v", err, e2)
	}
	v, err := VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: "peer"}, VerifyRequest{Outcome: OutcomeHelpful, Note: secret})
	if err != nil || v.Redactions == nil {
		t.Fatalf("note redaction: %v %+v", err, v)
	}
	_, res = entryView(t, dir, ws, e.ID, true)
	for _, ev := range res["events"].([]govstore.Event) {
		if strings.Contains(ev.Note, secret) || strings.Contains(string(ev.Data), secret) {
			t.Fatalf("secret kept in event %s", ev.Type)
		}
	}
}

func TestEvidenceBoundVotes(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	e := promoted(t, dir, ws, "author", "the build uses make")
	dup := promoted(t, dir, ws, "author", "make builds it")
	handle := retainedEvidence(t, dir, ws, "peer-3")
	if _, err := VerifyContextOutcome(dir, ws, dup.ID, ContextActor{ID: "peer-3"},
		VerifyRequest{Outcome: OutcomeDuplicateOf, Target: e.ID, EvidenceHandle: handle}); err != nil {
		t.Fatal(err)
	}
	_, res := entryView(t, dir, ws, dup.ID, false)
	var found bool
	for _, b := range res["verification_basis"].([]map[string]any) {
		found = found || (b["verdict"] == "duplicate_of" && b["target"] == e.ID && b["evidence_handle"] == handle)
	}
	if !found {
		t.Fatalf("duplicate_of vote not bound to its evidence: %+v", res["verification_basis"])
	}
	// the handle is checked at vote time: another principal cannot cite it, and a revoked one fails
	if _, err := VerifyContextOutcome(dir, ws, dup.ID, ContextActor{ID: "peer-4"},
		VerifyRequest{Outcome: OutcomeReject, EvidenceHandle: handle}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign evidence accepted: %v", err)
	}
	if err := evidence.NewStore(dir, evidence.DefaultLimits()).Revoke(evidence.ReadRequest{WorkspaceID: ws, Handle: handle,
		Actor: "peer-3", AuthEnforced: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyContextOutcome(dir, ws, dup.ID, ContextActor{ID: "peer-3"},
		VerifyRequest{Outcome: OutcomeApprove, EvidenceHandle: handle}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("revoked evidence accepted: %v", err)
	}
	if _, err := VerifyContextOutcome(dir, ws, dup.ID, ContextActor{ID: "peer-3"}, VerifyRequest{Outcome: OutcomeDuplicateOf}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate_of without a target: %v", err)
	}
	if _, err := VerifyContextOutcome(dir, ws, dup.ID, ContextActor{ID: "peer-3"}, VerifyRequest{Outcome: "bogus"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown outcome: %v", err)
	}
}

func TestFeedbackOutcomesNeverChangeVerification(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	e := promoted(t, dir, ws, "author", "the build uses make")
	for peer, outcome := range map[string]string{"peer-1": OutcomeHelpful, "peer-2": OutcomeStaleHarm, "peer-3": OutcomeMisleading} {
		got, err := VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: peer}, VerifyRequest{Outcome: outcome, Note: "used it"})
		if err != nil || !got.Promoted || got.VerificationMode != VerificationPeer {
			t.Fatalf("%s: %v %+v", outcome, err, got)
		}
	}
	_, res := entryView(t, dir, ws, e.ID, false)
	if fb := res["feedback"].(govstore.OutcomeSummary); fb.Helpful != 1 || fb.StaleHarm != 1 || fb.Misleading != 1 {
		t.Fatalf("feedback summary: %+v", fb)
	}
	if _, err := VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: "peer-1"}, VerifyRequest{Outcome: OutcomeHelpful, Revision: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("feedback on a named revision: %v", err)
	}
}

func TestOwnerDistinctPolicyBlocksSelfVerificationAcrossTokens(t *testing.T) {
	ws := "ws"
	propose := func(dir string) *ContextEntry {
		e, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "c"}},
			ContextActor{ID: "alice-1", Owner: "alice"})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	sibling := ContextActor{ID: "alice-2", Owner: "alice", Verifier: true}

	// default token-distinct: a second token of the same owner is a distinct verifier
	dir := ownerPolicyDir(t, "")
	if _, err := VerifyContextOutcome(dir, ws, propose(dir).ID, sibling, VerifyRequest{}); err != nil {
		t.Fatalf("token policy refused a distinct token: %v", err)
	}

	dir = ownerPolicyDir(t, DistinctOwner)
	e := propose(dir)
	for _, outcome := range []string{OutcomeApprove, OutcomeReject} {
		if _, err := VerifyContextOutcome(dir, ws, e.ID, sibling, VerifyRequest{Outcome: outcome}); !errors.Is(err, ErrSameOwner) {
			t.Fatalf("%s by a same-owner token: %v", outcome, err)
		}
	}
	// feedback and duplicate reports are not verification, and other owners still verify
	if _, err := VerifyContextOutcome(dir, ws, e.ID, sibling, VerifyRequest{Outcome: OutcomeHelpful}); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []string{"bob", "carol"} {
		if _, err := VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: peer}, VerifyRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := entryView(t, dir, ws, e.ID, false)
	if !got.Promoted || got.VerificationMode != VerificationPeer {
		t.Fatalf("distinct owners did not promote: %+v", got)
	}

	// the revision author's owner comes from the token store when the edit is voted on
	if _, err := MintIdentityToken(dir, "bob", "agent", 0, nil, TokenIdentity{Owner: "bob-corp"}); err != nil {
		t.Fatal(err)
	}
	e2, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "d", Permission: "readwrite"}},
		ContextActor{ID: "bob", Owner: "bob-corp"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyContextOutcome(dir, ws, e2.ID, ContextActor{ID: "bob-2", Owner: "bob-corp"}, VerifyRequest{}); !errors.Is(err, ErrSameOwner) {
		t.Fatalf("same owner via token store: %v", err)
	}
}

func TestPrincipalDistinctnessSettingIsValidated(t *testing.T) {
	dir := t.TempDir()
	if _, err := UpdateSettings(dir, AppSettings{PrincipalDistinctness: "team"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown policy accepted: %v", err)
	}
	got, err := UpdateSettings(dir, AppSettings{PrincipalDistinctness: " Owner "})
	if err != nil || got.PrincipalDistinctness != DistinctOwner || principalDistinctness(dir) != DistinctOwner {
		t.Fatalf("owner policy: %v %+v", err, got)
	}
	// a hand-edited unknown value takes the stricter policy
	writeTestSettings(t, dir, appSettings{PrincipalDistinctness: "bogus"})
	if principalDistinctness(dir) != DistinctOwner {
		t.Fatal("unknown policy did not fail closed")
	}
}

func TestTokenIdentityOwnerAndKind(t *testing.T) {
	dir := t.TempDir()
	raw, err := MintIdentityToken(dir, "ci-bot", "agent", 0, nil, TokenIdentity{Owner: "alice", Kind: "Human"})
	if err != nil {
		t.Fatal(err)
	}
	p := ResolveToken(dir, raw)
	if p == nil || p.Owner != "alice" || p.Kind != PrincipalHuman || PrincipalOwner(dir, "ci-bot") != "alice" {
		t.Fatalf("identity not resolved: %+v", p)
	}
	rotated, err := RotateToken(dir, "ci-bot", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p := ResolveToken(dir, rotated); p == nil || p.Owner != "alice" || p.Kind != PrincipalHuman {
		t.Fatalf("rotation lost the identity: %+v", p)
	}
	plain, err := MintToken(dir, "plain", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if p := ResolveToken(dir, plain); p.Owner != "plain" || p.Kind != PrincipalAgent {
		t.Fatalf("default identity: %+v", p)
	}
	for _, bad := range []TokenIdentity{{Kind: "robot"}, {Owner: "../x"}} {
		if _, err := MintIdentityToken(dir, "x", "agent", 0, nil, bad); err == nil {
			t.Fatalf("identity %+v accepted", bad)
		}
	}
}

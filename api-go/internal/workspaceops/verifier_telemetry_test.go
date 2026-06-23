package workspaceops

import "testing"

// Feature-depth: verifier outcome + correlation telemetry (the data a weighted quorum
// would later use), computed over the real propose/verify history. The gate itself stays
// an unweighted distinct-agent threshold.
func TestComputeVerifierTelemetry(t *testing.T) {
	dir := t.TempDir()
	ws := "wsTel"
	require := true
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &require, ContextVerificationThreshold: 2})

	// entry 1: alice + bob approve -> promoted (both aligned with the approve outcome).
	// Proposed by a non-voter ("system") so neither alice nor bob is the excluded author.
	e1, err := ProposeContext(dir, ws, ProposeContextRequest{Content: "fact one", Source: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyContext(dir, ws, e1.ID, "alice", true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyContext(dir, ws, e1.ID, "bob", true, ""); err != nil {
		t.Fatal(err)
	}

	// entry 2: alice approves, bob + carol REJECT (threshold 2) -> rejected. bob/carol
	// aligned with the reject outcome, alice misaligned.
	e2, err := ProposeContext(dir, ws, ProposeContextRequest{Content: "fact two", Source: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyContext(dir, ws, e2.ID, "alice", true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyContext(dir, ws, e2.ID, "bob", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyContext(dir, ws, e2.ID, "carol", false, ""); err != nil {
		t.Fatal(err)
	}

	tel, err := ComputeVerifierTelemetry(dir, ws)
	if err != nil {
		t.Fatal(err)
	}

	byAgent := map[string]VerifierAgentStats{}
	for _, a := range tel.Agents {
		byAgent[a.Agent] = a
	}
	// alice: voted on both, approved both. e1 promoted (aligned), e2 rejected (misaligned)
	// => decided=2, aligned=1, rate 0.5.
	if a := byAgent["alice"]; a.Votes != 2 || a.Approvals != 2 || a.DecidedVotes != 2 || a.AlignedWithOutcome != 1 || a.AlignmentRate != 0.5 {
		t.Fatalf("alice stats wrong: %+v", a)
	}
	// bob: e1 approve (promoted, aligned), e2 reject (rejected, aligned) => 2/2 = 1.0.
	if b := byAgent["bob"]; b.DecidedVotes != 2 || b.AlignedWithOutcome != 2 || b.AlignmentRate != 1.0 {
		t.Fatalf("bob stats wrong: %+v", b)
	}

	// pairwise: alice/bob shared both entries — agree on e1 (both approve), disagree on
	// e2 (alice approve, bob reject) => 1/2 = 0.5.
	var ab *VerifierPairAgreement
	for i := range tel.Pairs {
		if tel.Pairs[i].AgentA == "alice" && tel.Pairs[i].AgentB == "bob" {
			ab = &tel.Pairs[i]
		}
	}
	if ab == nil || ab.SharedEntries != 2 || ab.Agreements != 1 || ab.AgreementRate != 0.5 {
		t.Fatalf("alice/bob pair agreement wrong: %+v", ab)
	}
}

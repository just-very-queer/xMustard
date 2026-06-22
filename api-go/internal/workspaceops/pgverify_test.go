package workspaceops

import "testing"

func TestBuildVerificationOutcomePostgresRowMapsObservedFields(t *testing.T) {
	runID := "run-verify-1"
	observedAt := "2026-06-18T12:00:00Z"
	row := verificationOutcomeToPostgresRow(
		RepoTargetRecord{
			TargetID: "verify-profile-backend-pytest",
			Label:    "Backend pytest verification",
		},
		ObservedVerificationOutcome{
			ObservationKind: "verification_profile_execution",
			MatchBasis:      "profile_id_exact",
			State:           "success",
			LastStatus:      "passed",
			LastRunAt:       &observedAt,
			RunID:           &runID,
		},
	)

	if row.TargetID != "verify-profile-backend-pytest" {
		t.Fatalf("unexpected target id: %q", row.TargetID)
	}
	if row.TargetName != "Backend pytest verification" {
		t.Fatalf("unexpected target name: %q", row.TargetName)
	}
	if row.Status != "passed" || row.Outcome != "success" {
		t.Fatalf("unexpected status/outcome: %q/%q", row.Status, row.Outcome)
	}
	if row.RunID != "run-verify-1" || row.ObservedAt != "2026-06-18T12:00:00Z" {
		t.Fatalf("unexpected run or observed_at: %q / %q", row.RunID, row.ObservedAt)
	}
	if row.Doc != "Backend pytest verification passed success" {
		t.Fatalf("unexpected doc: %q", row.Doc)
	}
}

package workspaceops

import "testing"

// The migrated error SOURCES now carry a typed class, so HTTP status follows the
// type (via respondError) instead of a wording-sensitive substring match (XM-PRO-013).
func TestRunControlWrongStatusErrorIsTypedConflict(t *testing.T) {
	dataDir, workspaceID, issueID, _ := writeIssueContextFixture(t, false)

	// a run in a terminal state cannot generate a plan -> Conflict (wrong state)
	run := runRecord{
		RunID:       "rt-typed",
		WorkspaceID: workspaceID,
		IssueID:     issueID,
		Runtime:     "opencode",
		Model:       "fake/test-model",
		Status:      "completed",
		CreatedAt:   nowUTC(),
	}
	if err := saveRunRecord(dataDir, run); err != nil {
		t.Fatal(err)
	}
	_, err := GenerateRunPlan(dataDir, workspaceID, "rt-typed")
	de, ok := AsDomainError(err)
	if !ok || de.Class != ClassConflict {
		t.Fatalf("wrong-status GenerateRunPlan should be a ClassConflict DomainError, got %v (%T)", err, err)
	}

	// a missing plan -> NotFound
	_, perr := GetRunPlan(dataDir, workspaceID, "rt-typed")
	if pde, ok := AsDomainError(perr); ok && pde.Class != ClassNotFound {
		t.Fatalf("no-plan error, when typed, must be ClassNotFound, got %v", perr)
	}
}

// The constructors map to the classes the HTTP layer expects.
func TestTypedErrorConstructorClasses(t *testing.T) {
	if Invalid("x").Class != ClassInvalidInput {
		t.Error("Invalid -> ClassInvalidInput")
	}
	if Conflict("x").Class != ClassConflict {
		t.Error("Conflict -> ClassConflict")
	}
	if NotFoundErr("x").Class != ClassNotFound {
		t.Error("NotFoundErr -> ClassNotFound")
	}
	if Unavailable("x").Class != ClassUnavailable {
		t.Error("Unavailable -> ClassUnavailable")
	}
	if !IsInvalidInput(Invalid("x")) {
		t.Error("Invalid must satisfy IsInvalidInput")
	}
}

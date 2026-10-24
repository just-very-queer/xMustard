package mcpserver

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/toolbudget"
	"xmustard/api-go/internal/workspaceops"
)

// The advertised maxResultSizeChars stays within what Claude Code honors, covers the
// evidence projection target (duplicated so the shim does not link the evidence
// store) and the largest measured projection, and is declared on exactly the tools
// that serve reads (why_failed reads a run or an outcome without a write argument).
func TestMaxResultCharsCoversTheProjectionCap(t *testing.T) {
	if projectionTarget != evidence.DefaultProjectionTarget {
		t.Fatalf("projectionTarget %d != evidence.DefaultProjectionTarget %d", projectionTarget, evidence.DefaultProjectionTarget)
	}
	if boundedResultChars > claudeCodeResultCeiling {
		t.Fatalf("maxResultSizeChars %d exceeds Claude Code's %d-character ceiling", boundedResultChars, claudeCodeResultCeiling)
	}
	if boundedResultChars < evidence.DefaultProjectionTarget+400 || boundedResultChars < measuredLargestProjection+400 {
		t.Fatalf("maxResultSizeChars %d does not cover a projection plus the recovery note", boundedResultChars)
	}
	for _, tl := range Tools() {
		if tl.servesReads() != (tl.MaxResultChars == boundedResultChars) {
			t.Errorf("%s: maxResultSizeChars should be declared on exactly the tools that serve reads", tl.Name)
		}
	}
}

// The recall budget and session_id bounds and the memory kinds are duplicated from
// workspaceops so the shim does not link the memory store; they must stay equal.
func TestRecallBoundsAndKindsMirrorWorkspaceops(t *testing.T) {
	got := [4]int{recallDefaultMaxChars, recallMinMaxChars, recallMaxMaxChars, recallMaxSessionID}
	want := [4]int{workspaceops.RecallDefaultMaxChars, workspaceops.RecallMinMaxChars, workspaceops.RecallMaxMaxChars, workspaceops.RecallMaxSessionID}
	if got != want {
		t.Fatalf("recall bounds %v != workspaceops %v", got, want)
	}
	if instructionsMemoryChars != workspaceops.ProjectionDefaultChars {
		t.Fatalf("the instructions' projection budget %d != workspaceops %d", instructionsMemoryChars, workspaceops.ProjectionDefaultChars)
	}
	if !slices.Equal(memoryKinds, workspaceops.MemoryKinds) {
		t.Fatalf("memory kinds %v != workspaceops %v", memoryKinds, workspaceops.MemoryKinds)
	}
}

// Impact's lean and full schemas duplicate vocabularies and bounds from the API
// validators. Keep the published values pinned to those authorities.
func TestImpactSchemaMirrorsWorkspaceopsAndToolbudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"relation_types", workspaceops.RelationTypes},
		{"op", workspaceops.OccurrenceOps},
		{"scope", workspaceops.ChangeScopes},
	} {
		arg, ok := impactTool.arg(tc.name)
		if !ok || !slices.Equal(arg.Enum, tc.want) {
			t.Errorf("impact %s enum %v != validator values %v", tc.name, arg.Enum, tc.want)
		}
	}

	arg, ok := impactTool.arg("max_chars")
	if !ok {
		t.Fatal("impact max_chars argument is missing")
	}
	budget := budgetSpec("impact")
	if arg.Min != toolbudget.MinMaxChars || arg.Max != toolbudget.MaxMaxChars ||
		!strings.Contains(arg.Desc, fmt.Sprint(budget.DefaultMaxChars)) {
		t.Errorf("impact max_chars (%d..%d, %q) does not mirror toolbudget (%d..%d, default %d)",
			arg.Min, arg.Max, arg.Desc, toolbudget.MinMaxChars, toolbudget.MaxMaxChars, budget.DefaultMaxChars)
	}
	if impactTool.Aliases["depth"] != "max_depth" || impactTool.Aliases["uid"] != "symbol" {
		t.Errorf("impact aliases %v do not preserve depth and uid", impactTool.Aliases)
	}
}

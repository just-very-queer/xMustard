package mcpserver

import (
	"testing"

	"xmustard/api-go/internal/evidence"
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

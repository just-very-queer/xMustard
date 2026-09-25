package mcpserver

import (
	"testing"

	"xmustard/api-go/internal/evidence"
)

// The advertised maxResultSizeChars is the evidence projection hard cap (duplicated so
// the shim does not link the evidence store): keep the two in step.
func TestMaxResultCharsCoversTheProjectionCap(t *testing.T) {
	if projectionHardCap != evidence.DefaultMaxProjection {
		t.Fatalf("projectionHardCap %d != evidence.DefaultMaxProjection %d", projectionHardCap, evidence.DefaultMaxProjection)
	}
	if boundedResultChars < evidence.DefaultMaxProjection+400 {
		t.Fatalf("maxResultSizeChars %d leaves no room for the recovery note", boundedResultChars)
	}
	for _, tl := range Tools() {
		if tl.Annotations.ReadOnly != (tl.MaxResultChars == boundedResultChars) {
			t.Errorf("%s: maxResultSizeChars should be declared on exactly the read tools", tl.Name)
		}
	}
}

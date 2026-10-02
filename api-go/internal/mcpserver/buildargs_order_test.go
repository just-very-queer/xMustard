package mcpserver

import (
	"maps"
	"strings"
	"testing"
)

// The same call must be answered the same way every time: which argument is
// reported, the reason and the message are a function of the arguments alone,
// not of Go's randomized map iteration order.
//
// impact carries hidden aliases (depth -> max_depth, uid -> symbol), so a call
// that mixed a repairable alias with a genuinely bad argument was answered
// about whichever the map iterator reached first, and {"uid":"A","symbol":"B"}
// silently kept whichever spelling came last.
func TestBuildArgsAnswersTheSameCallTheSameWayEveryTime(t *testing.T) {
	impact, _ := ToolByName("impact")
	recall, _ := ToolByName("recall")

	for _, c := range []struct {
		name          string
		tool          *Tool
		raw           map[string]any
		wantArgument  string
		wantReasonHas string
	}{
		// A repairable alias (uid) beside a bad argument (max_depth, not an
		// integer). The bad argument is reported, never the alias.
		{"alias beside a bad argument", impact,
			map[string]any{"workspace_id": "ws", "uid": "A", "max_depth": "x"},
			"max_depth", "integer"},
		// Two bad arguments: the first in sorted order is named.
		{"two bad arguments", recall,
			map[string]any{"workspace_id": "ws", "zebra": 1, "alpha": 2},
			"alpha", "unknown"},
		// A hidden alias and its canonical name with different values: refused,
		// and the refusal names the conflict and both spellings.
		{"alias conflicting with its canonical name", impact,
			map[string]any{"workspace_id": "ws", "uid": "A", "symbol": "B"},
			"uid", `conflicts with "symbol"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			// 200 iterations, each over a freshly cloned map, so a randomized
			// order would change the answer well inside this loop.
			for i := range 200 {
				_, _, aerr := BuildArgs(c.tool, maps.Clone(c.raw))
				if aerr == nil {
					t.Fatalf("%d: %s: want a rejection, got none", i, c.name)
				}
				if aerr.Argument != c.wantArgument {
					t.Fatalf("%d: %s: reported argument %q, want %q (reason %q, message %q)",
						i, c.name, aerr.Argument, c.wantArgument, aerr.Reason, aerr.Message)
				}
				if !strings.Contains(aerr.Reason, c.wantReasonHas) {
					t.Fatalf("%d: %s: reason %q does not name %q", i, c.name, aerr.Reason, c.wantReasonHas)
				}
			}
		})
	}
}

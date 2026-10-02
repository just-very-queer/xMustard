package mcpserver

import (
	"maps"
	"reflect"
	"testing"
)

// The same call must be answered the same way every time: which argument is
// reported, the reason, the message and the arguments built for a call that
// succeeds are all a function of the arguments alone, not of Go's randomized
// map iteration order.
//
// impact carries hidden aliases (depth -> max_depth, uid -> symbol), so a call
// that mixed a repairable alias with a genuinely bad argument was answered
// about whichever the map iterator reached first, and {"uid":"A","symbol":"B"}
// silently kept whichever spelling came last.
func TestBuildArgsAnswersTheSameCallTheSameWayEveryTime(t *testing.T) {
	impact, _ := ToolByName("impact")
	recall, _ := ToolByName("recall")

	for _, c := range []struct {
		name         string
		tool         *Tool
		raw          map[string]any
		wantArgument string
		// wantReason and wantMessage are the whole error, not a substring: a
		// message that varied while the reported argument held would pass a
		// Contains check, and the message is what an agent reads.
		wantReason  string
		wantMessage string
	}{
		// A repairable alias (uid) beside a bad argument (max_depth, not an
		// integer). The bad argument is reported, never the alias.
		{"alias beside a bad argument", impact,
			map[string]any{"workspace_id": "ws", "uid": "A", "max_depth": "x"},
			"max_depth", "must be an integer",
			`argument "max_depth" for tool impact must be an integer`},
		// Two bad arguments: the first in sorted order is named.
		{"two bad arguments", recall,
			map[string]any{"workspace_id": "ws", "zebra": 1, "alpha": 2},
			"alpha", "unknown",
			`unknown argument "alpha" for tool recall`},
		// A hidden alias and its canonical name with different values: refused,
		// and the refusal names the conflict and both spellings.
		{"alias conflicting with its canonical name", impact,
			map[string]any{"workspace_id": "ws", "uid": "A", "symbol": "B"},
			"uid", `conflicts with "symbol"`,
			`argument "uid" for tool impact conflicts with "symbol"`},
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
				if aerr.Reason != c.wantReason {
					t.Fatalf("%d: %s: reason %q, want %q", i, c.name, aerr.Reason, c.wantReason)
				}
				if aerr.Message != c.wantMessage {
					t.Fatalf("%d: %s: message %q, want %q", i, c.name, aerr.Message, c.wantMessage)
				}
			}
		})
	}

	// A call that succeeds must build the same arguments every time, so a hidden
	// alias folds onto its canonical name the same way whatever the visit order:
	// the canonical spelling wins, the alias is recorded, and the built map holds
	// the canonical key.
	aliasOK, _ := ToolByName("impact")
	wantArgs := map[string]string{"workspace_id": "ws", "symbol": "Store/Capture", "from": "A", "to": "B"}
	wantAliased := map[string]string{"uid": "symbol"}
	for i := range 200 {
		args, aliased, aerr := BuildArgs(aliasOK, map[string]any{
			"workspace_id": "ws", "uid": "Store/Capture", "from": "A", "to": "B",
		})
		if aerr != nil {
			t.Fatalf("%d: a call with only a hidden alias must succeed, got %v", i, aerr)
		}
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("%d: built arguments %v, want %v", i, args, wantArgs)
		}
		if !reflect.DeepEqual(aliased, wantAliased) {
			t.Fatalf("%d: recorded aliases %v, want %v", i, aliased, wantAliased)
		}
	}
}

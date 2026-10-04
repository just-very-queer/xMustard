package mcpserver

import (
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// WS-59d: op= reaches the API as the occurrence form's own argument, and the lean
// schema does not carry it (it is Advanced, documented at xmustard://docs/tools).
func TestImpactForwardsTheOccurrenceOperationAsAnAdvancedArgument(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	args := map[string]any{"workspace_id": "ws", "symbol": "Store.Put", "op": "implementations", "limit": 25, "max_chars": 9000}
	res, rerr := call(t, s, "impact", args)
	if rerr != nil || res["isError"] == true {
		t.Fatalf("%v refused: %v %v", args, rerr, res)
	}
	path := api.lastTool(t).Path
	if !strings.Contains(path, "op=implementations") || !strings.Contains(path, "limit=25") || !strings.Contains(path, "max_chars=9000") {
		t.Fatalf("op= or its paging did not reach the API: %s", path)
	}
}

// op= is Advanced: the lean profile's properties must not grow a member, the
// occurrence page's members are documented rather than listed (5.1 rule 9), and the
// docs paragraph must carry the contract.
func TestTheOccurrenceOperationIsAdvancedAndDocumented(t *testing.T) {
	advanced := false
	for _, a := range impactTool.Advanced {
		if a.Name == "op" {
			advanced = true
			if a.Enum == nil || !slices.Equal(a.Enum, workspaceops.OccurrenceOps) {
				t.Fatalf("op's enum is %v, want the three operations", a.Enum)
			}
		}
	}
	if !advanced {
		t.Fatal("op is not an Advanced argument")
	}
	for _, a := range impactTool.Args {
		if a.Name == "op" {
			t.Fatal("op is in the lean argument list; it must be Advanced")
		}
	}
	// every occurrence member the row promises is named in the docs paragraph, since
	// the output schema lists only what the forms share
	for _, member := range []string{"op=refs", "op=definition", "op=implementations",
		"kind File", "provenance", "next_cursor", "lower_bound"} {
		if !strings.Contains(impactTool.Doc, member) {
			t.Fatalf("the docs paragraph does not document %q", member)
		}
	}
	// and none of the occurrence-only members grew the lean output schema.
	// has_more and next_cursor are not among them: WS-59e put both in the lean schema
	// for the blast form's tier paging, so they are now members every paged form shares
	// and this row does not take them back out — §5.1 rule 9 freezes lean caps downward,
	// and removing a member the tip added would be a second, unmeasured edit to the same
	// schema.
	for _, member := range []string{"occurrences", "epistemic"} {
		if impactTool.Output[member] != "" {
			t.Fatalf("%q is listed in the lean output schema; the docs carry it", member)
		}
	}
}

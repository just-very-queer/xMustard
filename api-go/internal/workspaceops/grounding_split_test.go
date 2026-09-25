package workspaceops

import (
	"encoding/json"
	"strings"
	"testing"
)

// The section split of grounding.go must not change the `ground` wire shape: the
// same keys, in the same order, with the same values.
func TestGroundingSplitKeepsWireShape(t *testing.T) {
	dataDir, ws := seedGroundBenchWorkspace(t)
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"workspace_id", "drift", "changed_files", "dirty_symbols", "contract_breaks", "broken_contracts",
		"recent_failed_runs", "blocked_by_dirty_state", "blocked_by_failing_verification", "stale_memory",
		"stale_memory_checked", "stale_memory_total", "stale_memory_complete", "memory_verification_modes",
		"summary", "generated_at"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("ground keys changed:\n got %v\nwant %v", keys, want)
	}
	if g.ChangedFiles != 1 || g.DirtySymbols != 1 || g.ContractBreaks != 1 || len(g.BrokenContracts) != 1 ||
		len(g.RecentFailedRuns) != 4 || !g.BlockedByDirtyState || !g.BlockedByFailingVerification ||
		g.StaleMemoryTotal != 50 || g.StaleMemoryChecked != 50 || !g.StaleMemoryComplete {
		t.Fatalf("ground values changed: %s", raw)
	}
	if want := "1 changed file(s), 1 dirty symbol(s), 1 contract break(s), 4 failed run(s), 0 stale memory."; g.Summary != want {
		t.Fatalf("summary = %q, want %q", g.Summary, want)
	}
}

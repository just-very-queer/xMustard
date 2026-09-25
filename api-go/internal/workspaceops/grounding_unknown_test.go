package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func unknownFields(g *SessionGrounding) []string {
	out := []string{}
	for _, u := range g.Unknown {
		out = append(out, u.Field)
	}
	return out
}

// PAR-RT-11 (ground slice): a working-changes result that does not decode used to
// be reported as 0 changed files, 0 dirty symbols and not blocked.
func TestGroundReportsUndecodableChangesAsUnknown(t *testing.T) {
	dataDir, ws := seedGroundBenchWorkspace(t)
	core := filepath.Join(t.TempDir(), "xmustard-core")
	script := "#!/bin/sh\ncase \"$1 $2\" in\n\"changetrack drift\") echo '{\"stale\":false}' ;;\n\"changetrack working-changes\") echo 'not json' ;;\n*) echo '{}' ;;\nesac\n"
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if g.ChangedFiles != nil || g.DirtySymbols != nil || g.ContractBreaks != nil || g.BlockedByDirtyState != nil {
		t.Fatalf("undecodable changes must be unknown, got changed=%v dirty=%v breaks=%v blocked=%v",
			g.ChangedFiles, g.DirtySymbols, g.ContractBreaks, g.BlockedByDirtyState)
	}
	for _, f := range []string{"changed_files", "dirty_symbols", "contract_breaks"} {
		if !slices.Contains(unknownFields(g), f) {
			t.Fatalf("%s not listed unknown: %+v", f, g.Unknown)
		}
	}
	raw, _ := json.Marshal(g)
	var wire map[string]any
	_ = json.Unmarshal(raw, &wire)
	if wire["changed_files"] != nil || wire["blocked_by_dirty_state"] != nil {
		t.Fatalf("unknown counts must be null on the wire: %s", raw)
	}
	if !strings.HasPrefix(g.Summary, "? changed file(s), ? dirty symbol(s), ? contract break(s), 4 failed run(s)") ||
		!strings.Contains(g.Summary, "unknown") {
		t.Fatalf("summary = %q", g.Summary)
	}
}

// A result missing a field is not a change set: that field is unknown, not 0.
func TestGroundReportsMissingChangeFieldsAsUnknown(t *testing.T) {
	dataDir, ws := seedGroundBenchWorkspace(t)
	core := filepath.Join(t.TempDir(), "xmustard-core")
	script := "#!/bin/sh\ncase \"$1 $2\" in\n\"changetrack working-changes\") echo '{\"changed_files\":[]}' ;;\n*) echo '{}' ;;\nesac\n"
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if g.ChangedFiles == nil || *g.ChangedFiles != 0 || *g.BlockedByDirtyState || g.DirtySymbols != nil || g.ContractBreaks != nil {
		t.Fatalf("present empty list is 0, missing fields unknown: %+v", g.groundingIndex)
	}
	if got := unknownFields(g); !slices.Equal(got, []string{"dirty_symbols", "contract_breaks"}) {
		t.Fatalf("unknown = %v", got)
	}
}

// A run history that cannot be listed used to read as "no failed runs, not blocked".
func TestGroundReportsUnlistableRunsAsUnknown(t *testing.T) {
	dataDir, ws := seedGroundBenchWorkspace(t)
	runs := filepath.Join(dataDir, "workspaces", ws, "runs")
	if err := os.RemoveAll(runs); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runs, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if g.RecentFailedRuns != nil || g.BlockedByFailingVerification != nil || !slices.Contains(unknownFields(g), "recent_failed_runs") {
		t.Fatalf("unlistable runs must be unknown: runs=%v blocked=%v unknown=%+v", g.RecentFailedRuns, g.BlockedByFailingVerification, g.Unknown)
	}
	if !strings.Contains(g.Summary, "? failed run(s)") {
		t.Fatalf("summary = %q", g.Summary)
	}
}

// Unreadable run records are counted, not skipped silently; a listed failure still blocks.
func TestGroundReportsUnreadableRunRecords(t *testing.T) {
	dataDir, ws := seedGroundBenchWorkspace(t)
	runs := filepath.Join(dataDir, "workspaces", ws, "runs")
	if err := os.WriteFile(filepath.Join(runs, "run_torn.json"), []byte(`{"run_id":"run_torn","status":`), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.RecentFailedRuns) != 4 || g.BlockedByFailingVerification == nil || !*g.BlockedByFailingVerification {
		t.Fatalf("listed failures must still block: %v %v", g.RecentFailedRuns, g.BlockedByFailingVerification)
	}
	if len(g.Unknown) != 1 || g.Unknown[0].Field != "recent_failed_runs" || !strings.Contains(g.Unknown[0].Reason, "1 run record(s) unreadable") {
		t.Fatalf("unknown = %+v", g.Unknown)
	}
	// newest first, as the run list orders them
	if g.RecentFailedRuns[0] != "run_0015" || g.RecentFailedRuns[3] != "run_0000" {
		t.Fatalf("order = %v", g.RecentFailedRuns)
	}
	// with no listed failure, unreadable records leave "blocked" unknown
	for _, id := range g.RecentFailedRuns {
		if err := os.Remove(filepath.Join(runs, id+".json")); err != nil {
			t.Fatal(err)
		}
	}
	if g, err = BuildSessionGrounding(dataDir, ws); err != nil || g.BlockedByFailingVerification != nil {
		t.Fatalf("blocked must be unknown when failures may be hidden: %v %v", g.BlockedByFailingVerification, err)
	}
}

// An unreadable memory store used to read as "0 stale memory".
func TestGroundReportsUnreadableMemoryAsUnknown(t *testing.T) {
	dataDir, ws := seedGroundBenchWorkspace(t)
	if err := os.WriteFile(contextEntriesPath(dataDir, ws), []byte(`[{"id":"torn",`), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if g.StaleMemoryComplete || g.MemoryVerificationModes != nil || !slices.Contains(unknownFields(g), "stale_memory") {
		t.Fatalf("unreadable memory must be unknown: %+v unknown=%+v", g.groundingMemory, g.Unknown)
	}
	if !strings.Contains(g.Summary, "? stale memory") {
		t.Fatalf("summary = %q", g.Summary)
	}
}

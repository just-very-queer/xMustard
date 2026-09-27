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
	script := "#!/bin/sh\ncase \"$1 $2\" in\n\"changetrack working-changes\") echo '{\"changed_files\":[]}' ;;\n\"symbolgraph coverage\") echo '{\"languages\":{}}' ;;\n*) echo '{}' ;;\nesac\n"
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
	// the drift answer ({}) does not say whether a baseline exists either
	if got := unknownFields(g); !slices.Equal(got, []string{"baseline", "dirty_symbols", "contract_breaks"}) {
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

// An unreadable memory store used to read as "0 stale memory". A torn legacy file
// that cannot be imported leaves the store unreadable for the workspace (fail closed).
func TestGroundReportsUnreadableMemoryAsUnknown(t *testing.T) {
	dataDir, ws := seedGroundBenchWorkspace(t)
	src := legacyContextEntriesPath(dataDir, ws)
	if err := os.WriteFile(src, []byte(`[{"id":"torn",`), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if g.StaleMemory != nil || g.StaleMemoryTotal != nil || g.StaleMemoryComplete || g.MemoryVerificationModes != nil ||
		!slices.Contains(unknownFields(g), "stale_memory") || !slices.Contains(unknownFields(g), "stale_memory_total") {
		t.Fatalf("unreadable memory must be unknown: %+v unknown=%+v", g.groundingMemory, g.Unknown)
	}
	raw, _ := json.Marshal(g)
	var wire map[string]any
	_ = json.Unmarshal(raw, &wire)
	if v, ok := wire["stale_memory"]; !ok || v != nil || wire["stale_memory_total"] != nil || wire["stale_memory_checked"] != float64(0) {
		t.Fatalf("an unreadable store must put null stale_memory on the wire, not 0: %s", raw)
	}
	if !strings.Contains(g.Summary, "? stale memory") {
		t.Fatalf("summary = %q", g.Summary)
	}
}

// WS-22: ground takes the core's own unknowns (a failed Git listing, a partial symbol
// pass) with their reasons, and counts past the listing caps from the totals.
func TestGroundTakesCoreUnknownsAndTotals(t *testing.T) {
	dataDir, ws := seedGroundBenchWorkspace(t)
	for _, tc := range []struct {
		name, changes  string
		changed, dirty *int
		unknown        map[string]string
	}{
		{
			name: "capped listings",
			changes: `{"changed_files":[{"path":"a.go","change":"modified"}],"changed_files_total":1500,` +
				`"dirty_symbols":[{"path":"a.go","symbol":"A","contract_break":true,"signature_change":"params 1→2"}],"dirty_symbols_total":3000,"contract_breaks":7}`,
			changed: ptr(1500), dirty: ptr(3000), unknown: map[string]string{},
		},
		{
			name: "partial symbol pass",
			changes: `{"changed_files":[],"changed_files_total":900,"dirty_symbols":[],"dirty_symbols_total":null,"contract_breaks":null,` +
				`"unknown":[{"field":"dirty_symbols","reason":"partial: symbols were read from 200 of 900 changed source files (cap 200)"},` +
				`{"field":"contract_breaks","reason":"symbols were read from 200 of 900 changed source files (cap 200)"}]}`,
			changed: ptr(900),
			unknown: map[string]string{"dirty_symbols": "partial: symbols were read", "contract_breaks": "symbols were read from 200 of 900"},
		},
		{
			name: "failed listing",
			changes: `{"changed_files":null,"changed_files_total":null,"dirty_symbols":null,"contract_breaks":null,"unknown":[` +
				`{"field":"changed_files","reason":"git status: git exited nonzero"},{"field":"dirty_symbols","reason":"git status: git exited nonzero"},` +
				`{"field":"contract_breaks","reason":"git status: git exited nonzero"}]}`,
			unknown: map[string]string{"changed_files": "git status", "dirty_symbols": "git status", "contract_breaks": "git status"},
		},
	} {
		core := filepath.Join(t.TempDir(), "xmustard-core")
		script := "#!/bin/sh\ncase \"$1 $2\" in\n" +
			"\"changetrack drift\") echo '{\"has_baseline\":true,\"stale\":false,\"head_changed\":false,\"dirty\":true,\"baseline_head\":\"abc\",\"baseline_indexed_at\":\"2026-09-25T00:00:00Z\",\"baseline_reason\":\"admin\"}' ;;\n" +
			"\"changetrack working-changes\") echo '" + tc.changes + "' ;;\n" +
			"\"symbolgraph coverage\") echo '{\"complete\":true,\"languages\":{}}' ;;\n*) echo '{}' ;;\nesac\n"
		if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XMUSTARD_CORE_BIN", core)
		g, err := BuildSessionGrounding(dataDir, ws)
		if err != nil {
			t.Fatal(err)
		}
		same := func(a, b *int) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
		if !same(g.ChangedFiles, tc.changed) || !same(g.DirtySymbols, tc.dirty) {
			t.Fatalf("%s: changed=%v dirty=%v", tc.name, g.ChangedFiles, g.DirtySymbols)
		}
		got := map[string]string{}
		for _, u := range g.Unknown {
			got[u.Field] = u.Reason
		}
		for field, want := range tc.unknown {
			if !strings.Contains(got[field], want) {
				t.Fatalf("%s: unknown %s = %q, want the core's reason %q (all %v)", tc.name, field, got[field], want, g.Unknown)
			}
		}
		if len(got) != len(tc.unknown) {
			t.Fatalf("%s: unknown = %v", tc.name, g.Unknown)
		}
		if b := g.Baseline; b == nil || b.Auto || b.Reason != BaselineAdmin {
			t.Fatalf("%s: an admin baseline is not automatic: %+v", tc.name, b)
		}
	}
}

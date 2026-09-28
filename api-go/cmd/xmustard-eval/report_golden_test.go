package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden")

// TestReportWarningsGolden pins what buildReport derives itself (warnings in their
// order, the RSS gate, counts, runs that did not complete, memory summaries) for a
// manifest and run set that raise every warning it knows.
func TestReportWarningsGolden(t *testing.T) {
	m := syntheticManifest()
	m.DryRun, m.Interrupted, m.Aborted = true, true, "corpus_changed: t9 oracle"
	m.Containment = ContainNone
	m.Config.KeepWorktrees = true
	m.ExecutorMaxRSS = 42 << 20
	one := 1
	var recs []RunRecord
	for _, r := range syntheticRecords() {
		switch {
		case r.Arm == ArmXmustardMCP && r.TaskID == "t11":
			continue // the harmful-memory run of t11 has no pair
		case r.Arm == ArmBaseline && r.TaskID == "t1":
			r.Transcript.MCPServers = []string{"github", "XMustard-shadow"}
		case r.Arm == ArmXmustardMCP && r.TaskID == "t2":
			r.Transcript.FinalEvent, r.Transcript.UsageReported = false, false
		case r.Arm == ArmXmustardMCP && r.TaskID == "t3":
			r.ClientError = "exit 1; error event: rate limited"
		case r.Arm == ArmBaseline && r.TaskID == "t4":
			r.Isolation = &Isolation{VerifyChangedTree: true, EscapedKilled: 2}
		case r.Arm == ArmBaseline && r.TaskID == "t5":
			r.Oracle = &CheckResult{ExitCode: 127}
		case r.Arm == ArmXmustardMCP && r.TaskID == "t6":
			r.Oracle = &CheckResult{ExitCode: 126}
			r.RSS = &RSSSummary{XmPeakKiB: 150_000, XmRolesObserved: []string{"xmustard-api"}, AgentPeakKiB: 400_000}
		case r.Arm == ArmXmustardMCP && r.TaskID == "t7":
			r.RSS = &RSSSummary{XmPeakKiB: 40_000, XmRolesObserved: []string{"xmustard-mcp"}, AgentPeakKiB: 90_000}
		case r.Arm == ArmBaseline && r.TaskID == "t8":
			r.RSS = &RSSSummary{AgentPeakKiB: 500_000}
		case r.Arm == ArmXmustardMemory && r.TaskID == "t3":
			r.Memory.ScopeLeakage, r.Memory.PendingServed, r.Memory.PendingAsPeerVerified = 2, 1, 1
			r.Memory.PromotionErrors = &one
			r.Memory.AdversarialServed, r.Memory.AdversarialUnflagged = 2, 1
		case r.Arm == ArmXmustardMCP && r.TaskID == "t9":
			r.Status, r.Reason = StatusError, "setup step 0 failed (exit 2)"
		case r.Arm == ArmBaseline && r.TaskID == "t10":
			r.Status, r.Reason = StatusInterrupted, "interrupted during the agent run"
		}
		recs = append(recs, r)
	}
	rep := buildReport(m, recs)
	got, err := json.MarshalIndent(struct {
		Warnings     []string          `json:"warnings"`
		RSSGate      RSSGateSummary    `json:"rss_gate"`
		Counts       map[string]int    `json:"counts"`
		NotCompleted []NotCompletedRun `json:"not_completed"`
		Memory       []MemorySummary   `json:"memory"`
		Driver       string            `json:"driver"`
	}{rep.Warnings, rep.RSSGate, rep.Counts, rep.NotCompleted, rep.Memory, rep.Driver}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	// a second report of a real, contained run raises none of the run-set warnings
	clean := buildReport(syntheticManifest(), syntheticRecords())
	cleanJSON, _ := json.MarshalIndent(struct {
		Warnings []string       `json:"warnings"`
		RSSGate  RSSGateSummary `json:"rss_gate"`
	}{clean.Warnings, clean.RSSGate}, "", " ")
	got = append(append(append(got, '\n'), cleanJSON...), '\n')
	path := filepath.Join("testdata", "report_warnings.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run with -update)", path, err)
	}
	if string(want) != string(got) {
		t.Fatalf("report differs from %s:\n%s", path, got)
	}
}

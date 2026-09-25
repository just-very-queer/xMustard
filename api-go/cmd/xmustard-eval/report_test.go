package main

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

func syntheticRecords() []RunRecord {
	var recs []RunRecord
	cost := func(v float64) *float64 { return &v }
	for i := 0; i < 12; i++ {
		task := "t" + strconv.Itoa(i)
		for _, arm := range []string{ArmBaseline, ArmXmustardMCP, ArmXmustardMemory} {
			solved := map[string]bool{ArmBaseline: i < 4, ArmXmustardMCP: i < 10, ArmXmustardMemory: i < 11}[arm]
			r := RunRecord{Schema: RunSchema, TaskID: task, TaskClass: "bugfix", Arm: arm, Status: StatusCompleted, Resolved: solved,
				Client:       &ClientExit{WallMS: int64(1000 + 10*i)},
				Transcript:   &Transcript{FinalEvent: true, Usage: Usage{Total: int64(5000 + 100*i)}, CostUSD: cost(0.01 * float64(i+1))},
				Churn:        &DiffChurn{Insertions: i, Deletions: 1},
				Localization: &Localization{Gold: 1, Hits: map[bool]int{true: 1}[solved], Recall: map[bool]float64{true: 1}[solved]},
			}
			if arm == ArmXmustardMemory {
				zero := 0
				r.Memory = &MemoryMetrics{Seeded: map[string]int{LabelCurrent: 1}, Served: map[string]int{LabelCurrent: 1},
					CurrentFactRecall: ratio(1, 1), PromotionErrors: &zero, HarmfulServed: i == 11}
			}
			recs = append(recs, r)
		}
	}
	recs = append(recs, RunRecord{Schema: RunSchema, TaskID: "t0", Arm: ArmXmustardMCPHooks, Status: StatusSkipped, Reason: "placeholder: no hooks"})
	return recs
}

func syntheticManifest() *Manifest {
	return &Manifest{Schema: manifestSchema, CorpusName: "c", CorpusSHA256: "abc", Containment: ContainSandbox, ClientVersion: "1.0",
		Config: RunConfig{Driver: "claude", Model: "m", Seed: 7, Repeats: 1, ReferenceArm: ArmBaseline,
			Arms: []string{ArmBaseline, ArmXmustardMCP, ArmXmustardMCPHooks, ArmXmustardMemory}, Stack: StackConfig{Kind: StackReal},
			Thresholds: ThresholdConfig{MaxP: 0.05, BootstrapIters: 2000, Alpha: 0.05}}}
}

func TestReportDeterministic(t *testing.T) {
	m := syntheticManifest()
	recs := syntheticRecords()
	a, _ := json.Marshal(buildReport(m, recs))
	shuffled := append([]RunRecord(nil), recs...)
	rand.New(rand.NewSource(3)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	b, _ := json.Marshal(buildReport(m, shuffled))
	if !bytes.Equal(a, b) {
		t.Fatal("report depends on record order")
	}
	if renderMarkdown(buildReport(m, recs)) != renderMarkdown(buildReport(m, shuffled)) {
		t.Fatal("markdown depends on record order")
	}

	// the files written from disk are byte-identical across regenerations
	dir := t.TempDir()
	if err := writeJSONFile(filepath.Join(dir, "eval.json"), m); err != nil {
		t.Fatal(err)
	}
	var lines []byte
	for _, r := range shuffled {
		l, _ := json.Marshal(r)
		lines = append(append(lines, l...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "runs.jsonl"), lines, 0o644); err != nil {
		t.Fatal(err)
	}
	var first [2][]byte
	for i := 0; i < 2; i++ {
		if _, err := writeReport(dir); err != nil {
			t.Fatal(err)
		}
		j, _ := os.ReadFile(filepath.Join(dir, "report.json"))
		md, _ := os.ReadFile(filepath.Join(dir, "report.md"))
		if i == 0 {
			first = [2][]byte{j, md}
		} else if !bytes.Equal(first[0], j) || !bytes.Equal(first[1], md) {
			t.Fatal("regenerated report differs")
		}
	}
}

// TestReportStatisticsComeFromMemoryHarness: the paired verdict equals a direct call
// to memory_harness.go's CompareArms on the same (task, rep) outcomes.
func TestReportStatisticsComeFromMemoryHarness(t *testing.T) {
	m := syntheticManifest()
	rep := buildReport(m, syntheticRecords())
	var got *Comparison
	for i := range rep.Comparisons {
		if rep.Comparisons[i].ExperimentArm == ArmXmustardMCP {
			got = &rep.Comparisons[i]
		}
	}
	if got == nil {
		t.Fatal("no comparison for xmustard_mcp")
	}
	var base, exp []workspaceops.TaskOutcome
	for i := 0; i < 12; i++ {
		id := "t" + strconv.Itoa(i) + "#r0"
		base = append(base, workspaceops.TaskOutcome{TaskID: id, Solved: i < 4})
		exp = append(exp, workspaceops.TaskOutcome{TaskID: id, Solved: i < 10})
	}
	th := workspaceops.GoNoGoThreshold{MaxP: 0.05, BootstrapIters: 2000, Seed: 7, Alpha: 0.05}
	want := workspaceops.CompareArms(base, exp, ArmBaseline, ArmXmustardMCP, th)
	if got.McNemar.BImprovedOverA != want.McNemar.BImprovedOverA || got.McNemar.AImprovedOverB != want.McNemar.AImprovedOverB ||
		got.McNemar.PValue != round6(want.McNemar.PValue) || got.Decision != want.Decision || got.PairedTasks != 12 ||
		got.SolveDeltaCI.Lo != round6(want.SolveDeltaCI.Lo) || got.SolveDeltaCI.Hi != round6(want.SolveDeltaCI.Hi) {
		t.Fatalf("comparison %+v, want %+v", got.ArmComparison, want)
	}
	if got.McNemar.BImprovedOverA != 6 || got.Decision != "GO" {
		t.Fatalf("6-0 discordant improvements should be GO at p<=0.05: %+v", got.McNemar)
	}
	// the token delta is exactly 0 on every pair, so its CI collapses to [0,0]
	if got.TokensDelta == nil || *got.TokensDelta != (workspaceops.BootstrapCI{}) {
		t.Fatalf("tokens delta %+v", got.TokensDelta)
	}
	for _, c := range rep.Comparisons {
		if c.ExperimentArm == ArmXmustardMCPHooks && c.Decision != "NO-DATA" {
			t.Fatalf("an arm without runs must be NO-DATA, got %s", c.Decision)
		}
	}
	if len(rep.NotCompleted) != 1 || rep.NotCompleted[0].Reason != "placeholder: no hooks" {
		t.Fatalf("skipped runs must be named: %+v", rep.NotCompleted)
	}
	// t11's memory run delivered a harmful memory and failed, but xmustard_mcp failed
	// t11 too, so the failure is not attributed to memory
	if len(rep.Memory) != 1 || rep.Memory[0].StaleMemoryHarm != 0 {
		t.Fatalf("memory summary %+v", rep.Memory)
	}
}

func TestStaleMemoryHarmNeedsPairedNoMemoryResolve(t *testing.T) {
	mk := func(arm string, resolved, harmful bool) *RunRecord {
		r := &RunRecord{TaskID: "t", Arm: arm, Resolved: resolved}
		if arm == ArmXmustardMemory {
			r.Memory = &MemoryMetrics{HarmfulServed: harmful}
		}
		return r
	}
	cases := []struct {
		mcp, mem, harmful bool
		want              int
	}{
		{true, false, true, 1},  // memory failed where no-memory solved, after a harmful delivery
		{false, false, true, 0}, // both failed: not attributable
		{true, true, true, 0},   // memory still solved
		{true, false, false, 0}, // failed without a harmful delivery
	}
	for i, c := range cases {
		byArm := map[string][]*RunRecord{ArmXmustardMCP: {mk(ArmXmustardMCP, c.mcp, false)}, ArmXmustardMemory: {mk(ArmXmustardMemory, c.mem, c.harmful)}}
		if got := staleMemoryHarm(byArm)[ArmXmustardMemory]; got != c.want {
			t.Fatalf("case %d: harm %d, want %d", i, got, c.want)
		}
	}
}

func TestReportFlagsGovernanceAndRSS(t *testing.T) {
	m := syntheticManifest()
	recs := syntheticRecords()
	for i := range recs {
		if recs[i].Arm == ArmXmustardMemory && recs[i].TaskID == "t3" {
			recs[i].Memory.ScopeLeakage = 1
			recs[i].Memory.PendingServed = 1
		}
		if recs[i].Arm == ArmXmustardMCP && recs[i].TaskID == "t5" {
			recs[i].RSS = &RSSSummary{XmPeakKiB: 100_000, XmRolesObserved: []string{"xmustard-api"}, AgentPeakKiB: 300_000}
		}
	}
	rep := buildReport(m, recs)
	var gov int
	for _, w := range rep.Warnings {
		if len(w) > 10 && w[:10] == "GOVERNANCE" {
			gov++
		}
	}
	if gov != 2 {
		t.Fatalf("governance warnings %v", rep.Warnings)
	}
	if rep.RSSGate.MaxXmKiB != 100_000 || len(rep.RSSGate.OverGate) != 1 || rep.RSSGate.MaxAgentKiB != 300_000 {
		t.Fatalf("rss gate %+v", rep.RSSGate)
	}
	if !reflect.DeepEqual(rep.Arms, m.Config.Arms) {
		t.Fatal("arms")
	}
}

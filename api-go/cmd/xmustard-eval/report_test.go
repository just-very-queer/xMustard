package main

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
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
				Transcript:   &Transcript{FinalEvent: true, UsageReported: true, Usage: Usage{Total: int64(5000 + 100*i)}, CostUSD: cost(0.01 * float64(i+1)), CostSource: CostClientFinal},
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
// to memory_harness.go's CompareArms on the same task outcomes.
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
		id := "t" + strconv.Itoa(i)
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
	if len(rep.Memory) != 1 || rep.Memory[0].StaleMemoryHarm == nil || *rep.Memory[0].StaleMemoryHarm != 0 || rep.Memory[0].HarmUnpaired != 0 {
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
		h := staleMemoryHarm(byArm)[ArmXmustardMemory]
		if got := h.value(); got == nil || *got != c.want || h.unpaired != 0 {
			t.Fatalf("case %d: harm %+v, want %d", i, h, c.want)
		}
	}
	// no completed xmustard_mcp partner: the failure can be neither blamed on memory
	// nor cleared, so harm is n/a rather than a measured 0
	unpaired := staleMemoryHarm(map[string][]*RunRecord{ArmXmustardMemory: {mk(ArmXmustardMemory, false, true)}})[ArmXmustardMemory]
	if unpaired.value() != nil || unpaired.unpaired != 1 {
		t.Fatalf("unpaired harm %+v", unpaired)
	}
	// with no failing harmful delivery at all, 0 is measured
	if none := staleMemoryHarm(map[string][]*RunRecord{ArmXmustardMemory: {mk(ArmXmustardMemory, true, true)}})[ArmXmustardMemory].value(); none == nil || *none != 0 {
		t.Fatalf("no eligible runs: %v", none)
	}
	// the report says n/a and warns
	m := syntheticManifest()
	recs := []RunRecord{{Schema: RunSchema, TaskID: "t", Arm: ArmXmustardMemory, Status: StatusCompleted,
		Memory: &MemoryMetrics{HarmfulServed: true}, Transcript: &Transcript{UsageReported: true}}}
	rep := buildReport(m, recs)
	if len(rep.Memory) != 1 || rep.Memory[0].StaleMemoryHarm != nil || rep.Memory[0].HarmUnpaired != 1 || !hasWarning(rep, "cannot attribute") {
		t.Fatalf("report harm %+v warnings %v", rep.Memory, rep.Warnings)
	}
	if md := renderMarkdown(rep); !strings.Contains(md, "| n/a (1) |") {
		t.Fatalf("markdown harm cell:\n%s", md)
	}
}

// TestReportAdversarialMemory: adversarial deliveries are summed per arm, and one that
// reached the model without injection_flags is an injection-safety warning.
func TestReportAdversarialMemory(t *testing.T) {
	m := syntheticManifest()
	rec := func(task string, served, unflagged int) RunRecord {
		return RunRecord{Schema: RunSchema, TaskID: task, Arm: ArmXmustardMemory, Status: StatusCompleted, Transcript: &Transcript{UsageReported: true},
			Memory: &MemoryMetrics{AdversarialServed: served, AdversarialUnflagged: unflagged, HarmfulServed: served > 0}}
	}
	rep := buildReport(m, []RunRecord{rec("a", 1, 0), rec("b", 1, 1), rec("c", 0, 0)})
	if len(rep.Memory) != 1 || rep.Memory[0].AdversarialServed != 2 || rep.Memory[0].AdversarialUnflagged != 1 {
		t.Fatalf("memory summary %+v", rep.Memory)
	}
	if !hasWarning(rep, "INJECTION SAFETY: xmustard_memory delivered 1 adversarial memories without injection_flags") {
		t.Fatalf("warnings %v", rep.Warnings)
	}
	if md := renderMarkdown(rep); !strings.Contains(md, "| 2 (1) |") {
		t.Fatalf("markdown adversarial cell:\n%s", md)
	}
	if clean := buildReport(m, []RunRecord{rec("a", 1, 0)}); hasWarning(clean, "INJECTION SAFETY") {
		t.Fatalf("labelled deliveries must not warn: %v", clean.Warnings)
	}
}

func hasWarning(r *Report, substr string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestRepetitionsAreNotSeparatePairs: repetitions of one task are aggregated before
// McNemar and the bootstrap, so three repetitions of two discordant tasks are two
// discordant pairs, not six.
func TestRepetitionsAreNotSeparatePairs(t *testing.T) {
	m := syntheticManifest()
	m.Config.Repeats = 3
	m.Config.Arms = []string{ArmBaseline, ArmXmustardMCP}
	var recs []RunRecord
	for i := 0; i < 4; i++ {
		for r := 0; r < 3; r++ {
			for _, arm := range m.Config.Arms {
				// the experiment arm solves t0 and t1, which the baseline never solves;
				// both arms solve t2 and t3
				solved := i >= 2 || arm == ArmXmustardMCP
				recs = append(recs, RunRecord{Schema: RunSchema, TaskID: "t" + strconv.Itoa(i), Arm: arm, Rep: r, Status: StatusCompleted, Resolved: solved,
					Transcript: &Transcript{UsageReported: true, Usage: Usage{Total: 1000}}})
			}
		}
	}
	c := buildReport(m, recs).Comparisons[0]
	if c.PairedTasks != 4 || c.McNemar.Discordant != 2 || c.McNemar.PValue != 0.5 || c.Decision != "NO-GO" {
		t.Fatalf("task-level pairing: tasks %d discordant %d p %v decision %s", c.PairedTasks, c.McNemar.Discordant, c.McNemar.PValue, c.Decision)
	}
	if c.SolveRateDelta == nil || c.SolveRateDelta.Mean != 0.5 || c.Aggregation != taskAggregation {
		t.Fatalf("solve-rate delta %+v", c.SolveRateDelta)
	}
	// a strict majority decides a task: 2 of 3 solved counts, 1 of 3 does not
	recs = recs[:0]
	for r := 0; r < 3; r++ {
		recs = append(recs,
			RunRecord{Schema: RunSchema, TaskID: "a", Arm: ArmBaseline, Rep: r, Status: StatusCompleted, Resolved: r == 0},
			RunRecord{Schema: RunSchema, TaskID: "a", Arm: ArmXmustardMCP, Rep: r, Status: StatusCompleted, Resolved: r != 0})
	}
	c = buildReport(m, recs).Comparisons[0]
	if c.PairedTasks != 1 || c.BaselineSolveRate != 0 || c.ExperimentSolveRate != 1 {
		t.Fatalf("majority: %+v", c.ArmComparison)
	}
}

// TestUnreportedUsageIsNotZero: a run whose client never reported session usage (cut
// off before its final event, or a codex turn.failed) stays out of token medians and
// deltas instead of counting as a free run.
func TestUnreportedUsageIsNotZero(t *testing.T) {
	m := syntheticManifest()
	m.Config.Arms = []string{ArmBaseline, ArmXmustardMCP}
	mk := func(task, arm string, reported bool, total int64) RunRecord {
		return RunRecord{Schema: RunSchema, TaskID: task, Arm: arm, Status: StatusCompleted, Resolved: true,
			Transcript: &Transcript{UsageReported: reported, FinalEvent: reported, Usage: Usage{Total: total}}}
	}
	recs := []RunRecord{
		mk("slow", ArmBaseline, false, 0), mk("fast", ArmBaseline, true, 5708),
		mk("slow", ArmXmustardMCP, true, 4000), mk("fast", ArmXmustardMCP, true, 5000),
	}
	rep := buildReport(m, recs)
	base := rep.ArmSummaries[0]
	if base.MedianTokens == nil || *base.MedianTokens != 5708 || base.UsageUnknown != 1 {
		t.Fatalf("arm median must ignore the unreported run: %+v", base)
	}
	for _, tm := range rep.TaskMedians {
		if tm.TaskID == "slow" && tm.Arm == ArmBaseline && tm.MedianTokens != nil {
			t.Fatalf("task median for a run without usage must be n/a, got %v", *tm.MedianTokens)
		}
	}
	c := rep.Comparisons[0]
	if c.TokensPairs != 1 || c.TokensDelta == nil || c.TokensDelta.Mean != -708 {
		t.Fatalf("token delta must use only tasks with usage on both sides: %d %+v", c.TokensPairs, c.TokensDelta)
	}
	if !hasWarning(rep, "without session usage") {
		t.Fatalf("warnings %v", rep.Warnings)
	}
}

// TestClientErrorsAreFlagged: a client failure is visible in the report, and a run
// config can pre-register leaving such runs out of the pairs.
func TestClientErrorsAreFlagged(t *testing.T) {
	m := syntheticManifest()
	recs := syntheticRecords()
	recs[0].ClientError = "error event: rate limited"
	rep := buildReport(m, recs)
	if !hasWarning(rep, "rate limited") || rep.ArmSummaries[0].ClientErrors+rep.ArmSummaries[1].ClientErrors+rep.ArmSummaries[3].ClientErrors != 1 {
		t.Fatalf("client error not reported: %v", rep.Warnings)
	}
	if clientError(ClientExit{ExitCode: 1, TimedOut: true}, &Transcript{IsError: true}) != "" {
		t.Fatal("a timeout is the agent's outcome, not a client error")
	}
	if got := clientError(ClientExit{ExitCode: 2}, &Transcript{IsError: true, ErrorText: "auth"}); got != "exit 2; error event: auth" {
		t.Fatalf("client error %q", got)
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

// TestPeerOwnerDecisionRules: the noncommercial guard does not depend on the exact
// words of the free-text license field.
func TestPeerOwnerDecisionRules(t *testing.T) {
	needs := []PeerConfig{
		{Name: "gitnexus", Command: "gitnexus", License: "PolyForm-Noncommercial-1.0.0"},
		{Name: "graph", Command: "/opt/bin/gitnexus", Args: []string{"mcp"}, License: "see upstream"},
		{Name: "graph", Command: "npx", Args: []string{"-y", "gitnexus@latest", "mcp"}, License: "MIT"},
		{Name: "p1", Command: "x", License: "PolyForm-NC-1.0.0"},
		{Name: "p2", Command: "x", License: "PolyForm Small Business"},
		{Name: "p3", Command: "x", License: "CC-BY-NC-4.0"},
		{Name: "p4", Command: "x", License: "Non-Commercial use only"},
	}
	for _, p := range needs {
		if _, err := p.check(); err == nil || !strings.Contains(err.Error(), "owner_decision") {
			t.Fatalf("%+v: want an owner_decision error, got %v", p, err)
		}
		p.OwnerDecision = "approved by the owner, 2026-09-25"
		if rule, err := p.check(); err != nil || rule == "" {
			t.Fatalf("%+v with a decision: rule %q err %v", p, rule, err)
		}
	}
	for _, lic := range []string{"MIT", "Apache-2.0", "GPL-3.0-or-later", "BSD-3-Clause"} {
		if rule, err := (PeerConfig{Name: "serena", Command: "serena", License: lic}).check(); err != nil || rule != "" {
			t.Fatalf("%s: rule %q err %v", lic, rule, err)
		}
	}
	// prepare records the rule in the manifest's config
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	cfg := prepared(t, &RunConfig{Driver: "fake:claude", Peers: []PeerConfig{{Name: "gitnexus", Command: "gitnexus", License: "PolyForm-NC-1.0.0", OwnerDecision: "yes"}}}, c, t.TempDir())
	if cfg.Peers[0].OwnerDecisionRule == "" {
		t.Fatal("owner decision rule not recorded")
	}
}

// TestRSSAttributionUsesConfiguredNames: a renamed stdio shim under the agent counts
// toward the xMustard tree when its binary is configured; the containment wrapper
// counts toward neither tree.
func TestRSSAttributionUsesConfiguredNames(t *testing.T) {
	procs := []psProc{
		{pid: 10, ppid: 1, rssKiB: 1000, comm: "bwrap"},
		{pid: 11, ppid: 10, rssKiB: 50000, comm: "claude"},
		{pid: 12, ppid: 11, rssKiB: 8000, comm: "xmustard-relay"},
		{pid: 13, ppid: 12, rssKiB: 2000, comm: "git"},
		{pid: 20, ppid: 1, rssKiB: 30000, comm: "xmustard-api"},
	}
	xm, agent, all := splitTrees(procs, []int{20}, 10, xmustardProcNames)
	if sumRSS(xm) != 30000 || sumRSS(agent) != 60000 || len(all) != 4 {
		t.Fatalf("fixed names: xm %d agent %d all %v", sumRSS(xm), sumRSS(agent), all)
	}
	xm, agent, _ = splitTrees(procs, []int{20}, 10, append(slices.Clone(xmustardProcNames), "xmustard-relay"))
	if sumRSS(xm) != 40000 || sumRSS(agent) != 50000 {
		t.Fatalf("configured names: xm %d agent %d", sumRSS(xm), sumRSS(agent))
	}
	// Linux truncates comm to 15 bytes
	if !procNameMatches("xmustard-relay-", []string{"xmustard-relay-stdio"}) || procNameMatches("xmustard-rel", []string{"xmustard-relay"}) {
		t.Fatal("comm truncation")
	}
	c := loadTestCorpus(t, writeFixtureCorpus(t, nil))
	cfg := prepared(t, &RunConfig{Driver: "fake:claude", Stack: StackConfig{Kind: StackReal, APIBin: "/bin/sh", MCPBin: "/bin/echo", CoreBin: "/bin/cat"}}, c, t.TempDir())
	for _, n := range []string{"sh", "echo", "cat", "xmustard-mcp"} {
		if !slices.Contains(cfg.xmNames, n) {
			t.Fatalf("configured binary %s not attributed: %v", n, cfg.xmNames)
		}
	}
}

func sumRSS(ps []psProc) int64 {
	var n int64
	for _, p := range ps {
		n += p.rssKiB
	}
	return n
}

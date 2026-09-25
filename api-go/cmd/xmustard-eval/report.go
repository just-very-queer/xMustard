package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// The report is a pure function of eval.json and runs.jsonl: records are sorted, the
// bootstrap is seeded from the run config, and nothing reads the clock, so the same
// inputs always produce byte-identical report.json and report.md. The statistics are
// memory_harness.go's (CompareArms: exact McNemar + paired bootstrap), reused as is.

const reportSchema = "xmustard.eval.report/v1"

// Report is report.json.
type Report struct {
	Schema        string            `json:"schema"`
	Corpus        string            `json:"corpus"`
	CorpusSHA256  string            `json:"corpus_sha256"`
	Driver        string            `json:"driver"`
	Model         string            `json:"model"`
	ClientVersion string            `json:"client_version"`
	DryRun        bool              `json:"dry_run"`
	Interrupted   bool              `json:"interrupted,omitempty"`
	Stack         string            `json:"stack"`
	Containment   string            `json:"containment"`
	Seed          int64             `json:"seed"`
	Repeats       int               `json:"repeats"`
	ReferenceArm  string            `json:"reference_arm"`
	Arms          []string          `json:"arms"`
	Threshold     ThresholdConfig   `json:"threshold"`
	Counts        map[string]int    `json:"counts"`
	Warnings      []string          `json:"warnings,omitempty"`
	ArmSummaries  []ArmSummary      `json:"arm_summaries"`
	TaskMedians   []TaskArmMedian   `json:"task_medians"`
	Comparisons   []Comparison      `json:"comparisons"`
	ByClass       []ClassSummary    `json:"by_class"`
	Memory        []MemorySummary   `json:"memory_lifecycle,omitempty"`
	NotCompleted  []NotCompletedRun `json:"not_completed"`
	RSSGate       RSSGateSummary    `json:"rss_gate"`
}

// ArmSummary aggregates completed runs of one arm.
type ArmSummary struct {
	Arm               string   `json:"arm"`
	Completed         int      `json:"completed"`
	Resolved          int      `json:"resolved"`
	ResolveRate       float64  `json:"resolve_rate"`
	VisibleVerifyPass int      `json:"visible_verify_pass"`
	MedianTokens      float64  `json:"median_total_tokens"`
	MedianCostUSD     *float64 `json:"median_cost_usd,omitempty"`
	CostCoverage      int      `json:"runs_with_cost"`
	MedianWallMS      float64  `json:"median_wall_ms"`
	MedianChurn       float64  `json:"median_churn_lines"`
	MedianLocRecall   *float64 `json:"median_edit_localization_recall,omitempty"`
	XmustardToolCalls int      `json:"xmustard_tool_calls"`
	MaxXmRSSKiB       int64    `json:"max_xmustard_rss_kib"`
	MaxAgentRSSKiB    int64    `json:"max_agent_rss_kib"`
	NoFinalEvent      int      `json:"runs_without_final_event"`
	TimedOut          int      `json:"timed_out"`
}

// TaskArmMedian is the per-task, per-arm median over repetitions (§6.12).
type TaskArmMedian struct {
	TaskID       string   `json:"task_id"`
	Arm          string   `json:"arm"`
	Runs         int      `json:"runs"`
	Resolved     int      `json:"resolved"`
	MedianTokens float64  `json:"median_total_tokens"`
	MedianCost   *float64 `json:"median_cost_usd,omitempty"`
	MedianWallMS float64  `json:"median_wall_ms"`
	MedianChurn  float64  `json:"median_churn_lines"`
}

// Comparison is one arm against the reference arm on the same (task, rep) pairs.
type Comparison struct {
	workspaceops.ArmComparison
	TokensDelta       *workspaceops.BootstrapCI `json:"tokens_delta_ci,omitempty"`
	CostDelta         *workspaceops.BootstrapCI `json:"cost_delta_ci,omitempty"`
	WallMSDelta       *workspaceops.BootstrapCI `json:"wall_ms_delta_ci,omitempty"`
	LocalizationDelta *workspaceops.BootstrapCI `json:"edit_localization_recall_delta_ci,omitempty"`
	StaleMemoryHarm   int                       `json:"stale_memory_harm,omitempty"`
}

// ClassSummary is resolve rate per task class and arm.
type ClassSummary struct {
	Class     string  `json:"class"`
	Arm       string  `json:"arm"`
	Completed int     `json:"completed"`
	Resolved  int     `json:"resolved"`
	Rate      float64 `json:"resolve_rate"`
}

// MemorySummary aggregates PAR-EVAL-02 metrics for one arm.
type MemorySummary struct {
	Arm                    string   `json:"arm"`
	Runs                   int      `json:"runs"`
	CurrentFactRecall      *float64 `json:"current_fact_recall,omitempty"`
	StaleServedRate        *float64 `json:"stale_served_rate,omitempty"`
	SupersededServedRate   *float64 `json:"superseded_served_rate,omitempty"`
	DuplicateRate          *float64 `json:"duplicate_rate,omitempty"`
	ContradictionPrecision *float64 `json:"contradiction_precision,omitempty"`
	ContradictionRecall    *float64 `json:"contradiction_recall,omitempty"`
	ScopeLeakage           int      `json:"scope_leakage"`
	PendingServed          int      `json:"pending_served"`
	PendingAsPeerVerified  int      `json:"pending_as_peer_verified"`
	PromotionErrors        int      `json:"promotion_errors"`
	PromotionUnmeasured    int      `json:"promotion_unmeasured_runs,omitempty"`
	EstTokensPerRecall     *float64 `json:"est_tokens_per_recall,omitempty"`
	StaleMemoryHarm        int      `json:"stale_memory_harm"`
	HarmUnpaired           int      `json:"harm_unpaired_runs,omitempty"`
}

// NotCompletedRun names every skipped, failed or interrupted run.
type NotCompletedRun struct {
	TaskID string `json:"task_id"`
	Arm    string `json:"arm"`
	Rep    int    `json:"rep"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// RSSGateSummary is the sampled xMustard-tree peak across runs, against the gate.
type RSSGateSummary struct {
	GateBytes     int64    `json:"gate_bytes"`
	RunsSampled   int      `json:"runs_with_xmustard_samples"`
	MaxXmKiB      int64    `json:"max_xmustard_peak_kib"`
	MaxXmRun      string   `json:"max_xmustard_peak_run,omitempty"`
	OverGate      []string `json:"runs_over_gate,omitempty"`
	MaxAgentKiB   int64    `json:"max_agent_peak_kib"`
	ExecutorBytes int64    `json:"executor_max_rss_bytes,omitempty"` // separate line, outside the tree
	Note          string   `json:"note"`
	RealStack     bool     `json:"real_stack"`
}

// writeReport rebuilds report.json and report.md from the output directory.
func writeReport(outDir string) (*Report, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(outDir, "eval.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("eval.json: %w", err)
	}
	recs, err := readRuns(filepath.Join(outDir, "runs.jsonl"))
	if err != nil {
		return nil, err
	}
	rep := buildReport(&m, recs)
	if err := writeJSONFile(filepath.Join(outDir, "report.json"), rep); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "report.md"), []byte(renderMarkdown(rep)), 0o644); err != nil {
		return nil, err
	}
	return rep, nil
}

func readRuns(path string) ([]RunRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []RunRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r RunRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if r.Schema != RunSchema {
			return nil, fmt.Errorf("%s:%d: schema %q, want %q", path, n, r.Schema, RunSchema)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func pairID(r *RunRecord) string { return r.TaskID + "#r" + strconv.Itoa(r.Rep) }

func buildReport(m *Manifest, recs []RunRecord) *Report {
	recs = slices.Clone(recs)
	sort.SliceStable(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		if a.TaskID != b.TaskID {
			return a.TaskID < b.TaskID
		}
		if a.Arm != b.Arm {
			return a.Arm < b.Arm
		}
		return a.Rep < b.Rep
	})
	cfg := m.Config
	r := &Report{Schema: reportSchema, Corpus: m.CorpusName, CorpusSHA256: m.CorpusSHA256, Driver: cfg.Driver, Model: cfg.Model,
		ClientVersion: m.ClientVersion, DryRun: m.DryRun, Interrupted: m.Interrupted, Stack: cfg.Stack.Kind, Containment: m.Containment,
		Seed: cfg.Seed, Repeats: cfg.Repeats, ReferenceArm: cfg.ReferenceArm, Arms: slices.Clone(cfg.Arms), Threshold: cfg.Thresholds,
		Counts: map[string]int{}, RSSGate: RSSGateSummary{GateBytes: GateBytes, RealStack: cfg.Stack.Kind == StackReal, ExecutorBytes: m.ExecutorMaxRSS}}
	if cfg.Fake {
		r.Driver = "fake:" + cfg.Driver
	}
	if m.DryRun {
		r.Warnings = append(r.Warnings, "dry run (fake driver and/or stub stack): these numbers exercise the harness and are not experimental evidence")
	}
	if m.Containment == ContainNone {
		r.Warnings = append(r.Warnings, "containment none: hidden oracle files and other runs' artifacts were readable by the agent")
	}
	if m.Interrupted {
		r.Warnings = append(r.Warnings, "run interrupted: the result set is incomplete")
	}

	byArm := map[string][]*RunRecord{}
	for i := range recs {
		rec := &recs[i]
		r.Counts[rec.Status]++
		if rec.Status != StatusCompleted {
			r.NotCompleted = append(r.NotCompleted, NotCompletedRun{rec.TaskID, rec.Arm, rec.Rep, rec.Status, rec.Reason})
			continue
		}
		byArm[rec.Arm] = append(byArm[rec.Arm], rec)
		if rec.Arm == ArmBaseline && rec.Transcript != nil {
			for _, s := range rec.Transcript.MCPServers {
				if strings.Contains(strings.ToLower(s), "xmustard") {
					r.Warnings = append(r.Warnings, fmt.Sprintf("baseline run %s had an operator MCP server %q: the baseline is not xMustard-free", pairID(rec), s))
				}
			}
		}
		if rec.Transcript != nil && !rec.Transcript.FinalEvent {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s/%s: no final client event; tokens and cost are incomplete", pairID(rec), rec.Arm))
		}
		if rec.RSS != nil {
			if rec.RSS.AgentPeakKiB > r.RSSGate.MaxAgentKiB {
				r.RSSGate.MaxAgentKiB = rec.RSS.AgentPeakKiB
			}
			if len(rec.RSS.XmRolesObserved) > 0 {
				r.RSSGate.RunsSampled++
				if rec.RSS.XmPeakKiB > r.RSSGate.MaxXmKiB {
					r.RSSGate.MaxXmKiB = rec.RSS.XmPeakKiB
					r.RSSGate.MaxXmRun = pairID(rec) + "/" + rec.Arm
				}
				if rec.RSS.XmPeakKiB*1024 > GateBytes {
					r.RSSGate.OverGate = append(r.RSSGate.OverGate, pairID(rec)+"/"+rec.Arm)
				}
			}
		}
	}
	switch {
	case !r.RSSGate.RealStack:
		r.RSSGate.Note = "no product stack was measured (stub or none); RSS numbers are harness plumbing only"
	case r.RSSGate.RunsSampled == 0:
		r.RSSGate.Note = "no xMustard process was sampled"
	default:
		r.RSSGate.Note = "sampled ps-RSS peaks (100 ms, v1 method); the parity-scale gate (WS-10/WS-50) is authoritative"
	}
	if len(r.NotCompleted) > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d run(s) did not complete; see not_completed", len(r.NotCompleted)))
	}

	armNames := slices.Clone(cfg.Arms)
	for _, arm := range armNames {
		r.ArmSummaries = append(r.ArmSummaries, summarizeArm(arm, byArm[arm]))
	}
	r.TaskMedians = taskMedians(recs)
	r.ByClass = classSummaries(armNames, byArm)

	th := workspaceops.GoNoGoThreshold{MaxP: cfg.Thresholds.MaxP, MinSolvedDelta: cfg.Thresholds.MinSolvedDelta,
		BootstrapIters: cfg.Thresholds.BootstrapIters, Seed: cfg.Seed, Alpha: cfg.Thresholds.Alpha}
	ref := byArm[cfg.ReferenceArm]
	harm := staleMemoryHarm(byArm)
	for _, arm := range armNames {
		if arm == cfg.ReferenceArm {
			continue
		}
		r.Comparisons = append(r.Comparisons, compareArm(cfg.ReferenceArm, arm, ref, byArm[arm], th, harm[arm]))
	}
	for _, arm := range armNames {
		if ms := summarizeMemory(arm, byArm[arm], harm); ms != nil {
			r.Memory = append(r.Memory, *ms)
			if ms.ScopeLeakage > 0 {
				r.Warnings = append(r.Warnings, fmt.Sprintf("GOVERNANCE: %s delivered %d foreign-scope memories (scope leakage must be 0)", arm, ms.ScopeLeakage))
			}
			if ms.PendingServed > 0 || ms.PendingAsPeerVerified > 0 {
				r.Warnings = append(r.Warnings, fmt.Sprintf("GOVERNANCE: %s delivered %d unverified memories (%d labelled peer_verified)", arm, ms.PendingServed, ms.PendingAsPeerVerified))
			}
			if ms.PromotionErrors > 0 {
				r.Warnings = append(r.Warnings, fmt.Sprintf("GOVERNANCE: %s promoted %d memories during single-agent runs", arm, ms.PromotionErrors))
			}
		}
	}
	return r
}

func summarizeArm(arm string, runs []*RunRecord) ArmSummary {
	s := ArmSummary{Arm: arm, Completed: len(runs)}
	var tokens, costs, walls, churn, loc []float64
	for _, rec := range runs {
		if rec.Resolved {
			s.Resolved++
		}
		if rec.Verify != nil && rec.Verify.Passed {
			s.VisibleVerifyPass++
		}
		if rec.Transcript != nil {
			tokens = append(tokens, float64(rec.Transcript.Usage.Total))
			if rec.Transcript.CostUSD != nil {
				costs = append(costs, *rec.Transcript.CostUSD)
			}
			if !rec.Transcript.FinalEvent {
				s.NoFinalEvent++
			}
			s.XmustardToolCalls += len(rec.Transcript.XmResults)
		}
		if rec.Client != nil {
			walls = append(walls, float64(rec.Client.WallMS))
			if rec.Client.TimedOut {
				s.TimedOut++
			}
		}
		if rec.Churn != nil {
			churn = append(churn, float64(rec.Churn.Insertions+rec.Churn.Deletions))
		}
		if rec.Localization != nil {
			loc = append(loc, rec.Localization.Recall)
		}
		if rec.RSS != nil {
			s.MaxXmRSSKiB = max(s.MaxXmRSSKiB, rec.RSS.XmPeakKiB)
			s.MaxAgentRSSKiB = max(s.MaxAgentRSSKiB, rec.RSS.AgentPeakKiB)
		}
	}
	if s.Completed > 0 {
		s.ResolveRate = round6(float64(s.Resolved) / float64(s.Completed))
	}
	s.MedianTokens = median(tokens)
	s.CostCoverage = len(costs)
	if len(costs) > 0 {
		c := round6(median(costs))
		s.MedianCostUSD = &c
	}
	s.MedianWallMS = median(walls)
	s.MedianChurn = median(churn)
	if len(loc) > 0 {
		l := round6(median(loc))
		s.MedianLocRecall = &l
	}
	return s
}

func taskMedians(recs []RunRecord) []TaskArmMedian {
	type key struct{ task, arm string }
	groups := map[key][]*RunRecord{}
	var keys []key
	for i := range recs {
		rec := &recs[i]
		if rec.Status != StatusCompleted {
			continue
		}
		k := key{rec.TaskID, rec.Arm}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], rec)
	}
	var out []TaskArmMedian
	for _, k := range keys {
		runs := groups[k]
		t := TaskArmMedian{TaskID: k.task, Arm: k.arm, Runs: len(runs)}
		var tokens, costs, walls, churn []float64
		for _, rec := range runs {
			if rec.Resolved {
				t.Resolved++
			}
			if rec.Transcript != nil {
				tokens = append(tokens, float64(rec.Transcript.Usage.Total))
				if rec.Transcript.CostUSD != nil {
					costs = append(costs, *rec.Transcript.CostUSD)
				}
			}
			if rec.Client != nil {
				walls = append(walls, float64(rec.Client.WallMS))
			}
			if rec.Churn != nil {
				churn = append(churn, float64(rec.Churn.Insertions+rec.Churn.Deletions))
			}
		}
		t.MedianTokens, t.MedianWallMS, t.MedianChurn = median(tokens), median(walls), median(churn)
		if len(costs) > 0 {
			c := round6(median(costs))
			t.MedianCost = &c
		}
		out = append(out, t)
	}
	return out
}

func classSummaries(arms []string, byArm map[string][]*RunRecord) []ClassSummary {
	var out []ClassSummary
	for _, arm := range arms {
		per := map[string]*ClassSummary{}
		for _, rec := range byArm[arm] {
			c := per[rec.TaskClass]
			if c == nil {
				c = &ClassSummary{Class: rec.TaskClass, Arm: arm}
				per[rec.TaskClass] = c
			}
			c.Completed++
			if rec.Resolved {
				c.Resolved++
			}
		}
		for _, k := range sortedKeys(per) {
			c := per[k]
			c.Rate = round6(float64(c.Resolved) / float64(c.Completed))
			out = append(out, *c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return slices.Index(arms, out[i].Arm) < slices.Index(arms, out[j].Arm)
	})
	return out
}

// compareArm pairs runs on (task, rep) and hands the solved outcomes to CompareArms.
func compareArm(refName, armName string, ref, exp []*RunRecord, th workspaceops.GoNoGoThreshold, harm int) Comparison {
	toOutcomes := func(runs []*RunRecord) []workspaceops.TaskOutcome {
		out := make([]workspaceops.TaskOutcome, 0, len(runs))
		for _, rec := range runs {
			o := workspaceops.TaskOutcome{TaskID: pairID(rec), Solved: rec.Resolved}
			if rec.Transcript != nil {
				o.Tokens = int(rec.Transcript.Usage.Total)
				if rec.Transcript.CostUSD != nil {
					o.CostUSD = *rec.Transcript.CostUSD
				}
			}
			if rec.Client != nil {
				o.DurationMS = int(rec.Client.WallMS)
			}
			if rec.Localization != nil {
				o.LocalizationRecallAtK = rec.Localization.Recall
			}
			if rec.Memory != nil && rec.Memory.PromotionErrors != nil {
				o.PromotionError = *rec.Memory.PromotionErrors > 0
			}
			out = append(out, o)
		}
		return out
	}
	c := Comparison{ArmComparison: workspaceops.CompareArms(toOutcomes(ref), toOutcomes(exp), refName, armName, th), StaleMemoryHarm: harm}
	if c.PairedTasks == 0 {
		// CompareArms says NO-GO for an empty pairing; the report says why instead.
		c.Decision, c.Rationale = "NO-DATA", "no completed run pairs with the reference arm"
	}
	refBy := map[string]*RunRecord{}
	for _, rec := range ref {
		refBy[pairID(rec)] = rec
	}
	var dTok, dCost, dWall, dLoc []float64
	for _, e := range exp {
		b, ok := refBy[pairID(e)]
		if !ok {
			continue
		}
		if e.Transcript != nil && b.Transcript != nil {
			dTok = append(dTok, float64(e.Transcript.Usage.Total-b.Transcript.Usage.Total))
			if e.Transcript.CostUSD != nil && b.Transcript.CostUSD != nil {
				dCost = append(dCost, *e.Transcript.CostUSD-*b.Transcript.CostUSD)
			}
		}
		if e.Client != nil && b.Client != nil {
			dWall = append(dWall, float64(e.Client.WallMS-b.Client.WallMS))
		}
		if e.Localization != nil && b.Localization != nil {
			dLoc = append(dLoc, e.Localization.Recall-b.Localization.Recall)
		}
	}
	ci := func(d []float64) *workspaceops.BootstrapCI {
		if len(d) == 0 {
			return nil
		}
		v := workspaceops.PairedBootstrapMeanCI(d, th.BootstrapIters, th.Seed, th.Alpha)
		v.Mean, v.Lo, v.Hi = round6(v.Mean), round6(v.Lo), round6(v.Hi)
		return &v
	}
	c.SolveDeltaCI.Mean, c.SolveDeltaCI.Lo, c.SolveDeltaCI.Hi = round6(c.SolveDeltaCI.Mean), round6(c.SolveDeltaCI.Lo), round6(c.SolveDeltaCI.Hi)
	c.McNemar.PValue = round6(c.McNemar.PValue)
	c.BaselineSolveRate, c.ExperimentSolveRate = round6(c.BaselineSolveRate), round6(c.ExperimentSolveRate)
	c.TokensDelta, c.CostDelta, c.WallMSDelta, c.LocalizationDelta = ci(dTok), ci(dCost), ci(dWall), ci(dLoc)
	return c
}

// staleMemoryHarm counts memory-arm runs that failed the oracle after a stale,
// superseded or contradicted memory reached the model unflagged, while the paired
// xmustard_mcp run (same task, same rep, no memory) resolved.
func staleMemoryHarm(byArm map[string][]*RunRecord) map[string]int {
	out := map[string]int{}
	noMem := map[string]bool{}
	for _, rec := range byArm[ArmXmustardMCP] {
		noMem[pairID(rec)] = rec.Resolved
	}
	for arm, runs := range byArm {
		for _, rec := range runs {
			if rec.Memory == nil || !rec.Memory.HarmfulServed || rec.Resolved {
				continue
			}
			if resolved, ok := noMem[pairID(rec)]; ok && resolved {
				out[arm]++
			}
		}
	}
	return out
}

func summarizeMemory(arm string, runs []*RunRecord, harm map[string]int) *MemorySummary {
	var withMem []*MemoryMetrics
	for _, rec := range runs {
		if rec.Memory != nil {
			withMem = append(withMem, rec.Memory)
		}
	}
	if len(withMem) == 0 {
		return nil
	}
	s := &MemorySummary{Arm: arm, Runs: len(withMem), StaleMemoryHarm: harm[arm]}
	var cur, stale, sup, dup, cprec, crec, tpr []float64
	add := func(dst *[]float64, v *float64) {
		if v != nil {
			*dst = append(*dst, *v)
		}
	}
	for _, mm := range withMem {
		add(&cur, mm.CurrentFactRecall)
		add(&stale, mm.StaleServedRate)
		add(&sup, mm.SupersededServedRate)
		add(&dup, mm.DuplicateRate)
		add(&cprec, mm.ContradictionPrecision)
		add(&crec, mm.ContradictionRecall)
		add(&tpr, mm.TokensPerRecall)
		s.ScopeLeakage += mm.ScopeLeakage
		s.PendingServed += mm.PendingServed
		s.PendingAsPeerVerified += mm.PendingAsPeerVerified
		if mm.PromotionErrors != nil {
			s.PromotionErrors += *mm.PromotionErrors
		} else {
			s.PromotionUnmeasured++
		}
	}
	s.CurrentFactRecall, s.StaleServedRate, s.SupersededServedRate = meanPtr(cur), meanPtr(stale), meanPtr(sup)
	s.DuplicateRate, s.ContradictionPrecision, s.ContradictionRecall = meanPtr(dup), meanPtr(cprec), meanPtr(crec)
	s.EstTokensPerRecall = meanPtr(tpr)
	return s
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return round6(s[n/2])
	}
	return round6((s[n/2-1] + s[n/2]) / 2)
}

func meanPtr(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	m := round6(sum / float64(len(v)))
	return &m
}

func round6(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*1e6) / 1e6
}

// ---- markdown ----

func renderMarkdown(r *Report) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# xMustard outcome evaluation: %s\n\n", r.Corpus)
	w("| | |\n|---|---|\n")
	w("| Driver | %s |\n| Client version | %s |\n| Model | %s |\n| Stack | %s |\n| Containment | %s |\n", r.Driver, mdCell(r.ClientVersion), mdCell(firstNonEmpty(r.Model, "n/a")), r.Stack, r.Containment)
	w("| Seed / repeats | %d / %d |\n| Reference arm | %s |\n| Corpus sha256 | `%s` |\n", r.Seed, r.Repeats, r.ReferenceArm, r.CorpusSHA256)
	w("| Runs | %s |\n\n", countsLine(r.Counts))
	if len(r.Warnings) > 0 {
		w("## Warnings\n\n")
		for _, x := range r.Warnings {
			w("- %s\n", x)
		}
		w("\n")
	}
	w("## Arms\n\n| Arm | Completed | Resolved (hidden oracle) | Visible verify pass | Median tokens | Median cost USD | Median wall ms | Median churn lines | Median edit-loc recall | xMustard calls | Max xMustard RSS KiB | Max agent RSS KiB |\n")
	w("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, a := range r.ArmSummaries {
		w("| %s | %d | %d (%.3f) | %d | %.0f | %s | %.0f | %.0f | %s | %d | %d | %d |\n", a.Arm, a.Completed, a.Resolved, a.ResolveRate, a.VisibleVerifyPass,
			a.MedianTokens, fmtPtr(a.MedianCostUSD, "%.6f"), a.MedianWallMS, a.MedianChurn, fmtPtr(a.MedianLocRecall, "%.3f"), a.XmustardToolCalls, a.MaxXmRSSKiB, a.MaxAgentRSSKiB)
	}
	w("\n## Paired comparisons against %s\n\nMcNemar (exact) and paired bootstrap from `memory_harness.go`; pre-registered threshold max_p=%g, min_solved_delta=%g, alpha=%g, %d bootstrap iterations.\n\n",
		r.ReferenceArm, r.Threshold.MaxP, r.Threshold.MinSolvedDelta, r.Threshold.Alpha, r.Threshold.BootstrapIters)
	w("| Arm | Pairs | Solve rate ref / arm | Discordant (arm+ / ref+) | p | Solve delta CI | Tokens delta CI | Wall ms delta CI | Edit-loc delta CI | Decision |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range r.Comparisons {
		w("| %s | %d | %.3f / %.3f | %d (%d / %d) | %.4f | %s | %s | %s | %s | %s |\n", c.ExperimentArm, c.PairedTasks, c.BaselineSolveRate, c.ExperimentSolveRate,
			c.McNemar.Discordant, c.McNemar.BImprovedOverA, c.McNemar.AImprovedOverB, c.McNemar.PValue, fmtCI(&c.SolveDeltaCI, c.PairedTasks > 0),
			fmtCI(c.TokensDelta, true), fmtCI(c.WallMSDelta, true), fmtCI(c.LocalizationDelta, true), c.Decision)
	}
	if len(r.Memory) > 0 {
		w("\n## Coding-memory lifecycle (PAR-EVAL-02)\n\n| Arm | Runs | Current-fact recall | Stale served (unflagged) | Superseded served | Duplicate rate | Contradiction P / R | Scope leakage | Pending served | Promotion errors | Est. tokens/recall | Stale-memory harm |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
		for _, m := range r.Memory {
			w("| %s | %d | %s | %s | %s | %s | %s / %s | %d | %d | %d | %s | %d |\n", m.Arm, m.Runs, fmtPtr(m.CurrentFactRecall, "%.3f"), fmtPtr(m.StaleServedRate, "%.3f"),
				fmtPtr(m.SupersededServedRate, "%.3f"), fmtPtr(m.DuplicateRate, "%.3f"), fmtPtr(m.ContradictionPrecision, "%.3f"), fmtPtr(m.ContradictionRecall, "%.3f"),
				m.ScopeLeakage, m.PendingServed, m.PromotionErrors, fmtPtr(m.EstTokensPerRecall, "%.0f"), m.StaleMemoryHarm)
		}
	}
	w("\n## Per task and arm (medians over repetitions)\n\n| Task | Arm | Runs | Resolved | Median tokens | Median cost USD | Median wall ms | Median churn |\n|---|---|---|---|---|---|---|---|\n")
	for _, t := range r.TaskMedians {
		w("| %s | %s | %d | %d | %.0f | %s | %.0f | %.0f |\n", t.TaskID, t.Arm, t.Runs, t.Resolved, t.MedianTokens, fmtPtr(t.MedianCost, "%.6f"), t.MedianWallMS, t.MedianChurn)
	}
	if len(r.ByClass) > 0 {
		w("\n## By task class\n\n| Class | Arm | Completed | Resolved | Rate |\n|---|---|---|---|---|\n")
		for _, c := range r.ByClass {
			w("| %s | %s | %d | %d | %.3f |\n", c.Class, c.Arm, c.Completed, c.Resolved, c.Rate)
		}
	}
	w("\n## xMustard process tree (sampled ps-RSS)\n\n%s. Gate %d bytes (95.4 MiB). Runs sampled: %d. Max xMustard peak: %d KiB (%s). Max agent peak (external line): %d KiB. Executor peak (external line, getrusage): %d bytes.\n",
		r.RSSGate.Note, r.RSSGate.GateBytes, r.RSSGate.RunsSampled, r.RSSGate.MaxXmKiB, firstNonEmpty(r.RSSGate.MaxXmRun, "n/a"), r.RSSGate.MaxAgentKiB, r.RSSGate.ExecutorBytes)
	if len(r.RSSGate.OverGate) > 0 {
		w("\nOver the gate: %s\n", strings.Join(r.RSSGate.OverGate, ", "))
	}
	w("\n## Runs not completed\n\n")
	if len(r.NotCompleted) == 0 {
		w("None.\n")
	} else {
		w("| Task | Arm | Rep | Status | Reason |\n|---|---|---|---|---|\n")
		for _, n := range r.NotCompleted {
			w("| %s | %s | %d | %s | %s |\n", n.TaskID, n.Arm, n.Rep, n.Status, mdCell(n.Reason))
		}
	}
	return b.String()
}

func countsLine(c map[string]int) string {
	var parts []string
	for _, k := range sortedKeys(c) {
		parts = append(parts, fmt.Sprintf("%s %d", k, c[k]))
	}
	return strings.Join(parts, ", ")
}

func fmtPtr(v *float64, format string) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf(format, *v)
}

func fmtCI(ci *workspaceops.BootstrapCI, ok bool) string {
	if ci == nil || !ok {
		return "n/a"
	}
	return fmt.Sprintf("%.3f [%.3f, %.3f]", ci.Mean, ci.Lo, ci.Hi)
}

func mdCell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}

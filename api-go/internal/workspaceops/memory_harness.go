package workspaceops

import (
	"math"
	"math/rand"
	"sort"
)

// Paired held-out memory harness — the STATISTICAL + data core (the rigorous, testable
// part). The arms compare the governed-memory product against ablations on the SAME tasks
// with the SAME seeds, so differences are attributable to memory, not task variance. Task
// EXECUTION (a fixed model/budget/tools/timeout, the SWE-bench-style + private/post-cutoff
// corpus, sandboxed agent runs) is operator infrastructure layered on top of this; this
// file is what turns the per-task outcomes into a defensible go/no-go decision.

// MemoryArm names a comparison condition.
const (
	ArmMemoryOff        = "memory_off"
	ArmUngoverned       = "ungoverned"
	ArmGovernedNoDrift  = "governed_no_drift"
	ArmGovernedWithDrift = "governed_drift"
)

// TaskOutcome is one task run under one arm. Solved = an accepted, test-verified patch.
type TaskOutcome struct {
	TaskID        string  `json:"task_id"`
	Solved        bool    `json:"solved"`
	LocalizationRecallAtK float64 `json:"localization_recall_at_k"`
	Tokens        int     `json:"tokens"`
	CostUSD       float64 `json:"cost_usd"`
	DurationMS    int     `json:"duration_ms"`
	StaleMemoryHarm bool  `json:"stale_memory_harm"` // a stale memory misled this task
	PromotionError  bool  `json:"promotion_error"`   // a wrong fact was promoted
	HandoffSuccess  bool  `json:"handoff_success"`
}

// pairOutcomes aligns two arms' outcomes by TaskID, returning only the tasks both ran.
func pairOutcomes(a, b []TaskOutcome) (aSolved, bSolved []bool, ids []string) {
	byID := make(map[string]TaskOutcome, len(b))
	for _, o := range b {
		byID[o.TaskID] = o
	}
	// stable order for determinism
	sorted := append([]TaskOutcome(nil), a...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TaskID < sorted[j].TaskID })
	for _, oa := range sorted {
		if ob, ok := byID[oa.TaskID]; ok {
			aSolved = append(aSolved, oa.Solved)
			bSolved = append(bSolved, ob.Solved)
			ids = append(ids, oa.TaskID)
		}
	}
	return aSolved, bSolved, ids
}

// McNemarResult reports the discordant-pair counts and the two-sided exact-binomial
// p-value for whether two arms differ on the SAME tasks (the correct test for paired
// binary solved/not outcomes — not an unpaired proportion test).
type McNemarResult struct {
	Discordant      int     `json:"discordant"`        // b + c
	BImprovedOverA  int     `json:"b_improved_over_a"` // A failed, B solved
	AImprovedOverB  int     `json:"a_improved_over_b"` // A solved, B failed
	PValue          float64 `json:"p_value"`
}

// McNemar computes the exact-binomial McNemar test on paired solved outcomes (b = the
// experimental arm fixed a task the baseline missed; c = the reverse). Under H0 each
// discordant pair is a fair coin, so p is the two-sided binomial tail at min(b,c).
func McNemar(baselineSolved, experimentSolved []bool) McNemarResult {
	b, c := 0, 0 // b: baseline fail & experiment solve ; c: baseline solve & experiment fail
	n := min(len(baselineSolved), len(experimentSolved))
	for i := 0; i < n; i++ {
		switch {
		case !baselineSolved[i] && experimentSolved[i]:
			b++
		case baselineSolved[i] && !experimentSolved[i]:
			c++
		}
	}
	return McNemarResult{
		Discordant:     b + c,
		BImprovedOverA: b,
		AImprovedOverB: c,
		PValue:         twoSidedBinomialP(min(b, c), b+c),
	}
}

// twoSidedBinomialP is the two-sided p-value of observing k or fewer successes out of n
// fair-coin trials (×2, clamped at 1). Exact — fine for the discordant-pair counts.
func twoSidedBinomialP(k, n int) float64 {
	if n == 0 {
		return 1.0
	}
	cum := 0.0
	for i := 0; i <= k; i++ {
		cum += binomPMF(i, n, 0.5)
	}
	p := 2 * cum
	if p > 1 {
		p = 1
	}
	return p
}

func binomPMF(k, n int, p float64) float64 {
	return math.Exp(logChoose(n, k) + float64(k)*math.Log(p) + float64(n-k)*math.Log(1-p))
}

func logChoose(n, k int) float64 {
	return logFactorial(n) - logFactorial(k) - logFactorial(n-k)
}

func logFactorial(n int) float64 {
	s := 0.0
	for i := 2; i <= n; i++ {
		s += math.Log(float64(i))
	}
	return s
}

// BootstrapCI is a paired-bootstrap confidence interval for a mean per-task delta.
type BootstrapCI struct {
	Mean float64 `json:"mean"`
	Lo   float64 `json:"lo"`
	Hi   float64 `json:"hi"`
}

// PairedBootstrapMeanCI resamples the per-task deltas (experiment − baseline) WITH
// replacement to estimate the CI of the mean delta — a distribution-free interval that
// respects the pairing. seed makes it reproducible (the harness pre-registers it).
func PairedBootstrapMeanCI(deltas []float64, iters int, seed int64, alpha float64) BootstrapCI {
	if len(deltas) == 0 || iters <= 0 {
		return BootstrapCI{}
	}
	rng := rand.New(rand.NewSource(seed))
	means := make([]float64, iters)
	n := len(deltas)
	for it := 0; it < iters; it++ {
		sum := 0.0
		for i := 0; i < n; i++ {
			sum += deltas[rng.Intn(n)]
		}
		means[it] = sum / float64(n)
	}
	sort.Float64s(means)
	loIdx := int(math.Floor(alpha / 2 * float64(iters)))
	hiIdx := int(math.Ceil((1-alpha/2)*float64(iters))) - 1
	loIdx = clampIdx(loIdx, iters)
	hiIdx = clampIdx(hiIdx, iters)
	raw := 0.0
	for _, d := range deltas {
		raw += d
	}
	return BootstrapCI{Mean: raw / float64(n), Lo: means[loIdx], Hi: means[hiIdx]}
}

func clampIdx(i, n int) int {
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// GoNoGoThreshold is the PRE-REGISTERED bar an arm must clear over the baseline to ship:
// the paired solved-rate improvement must be statistically significant (p ≤ MaxP) AND the
// solved-rate delta CI must lie entirely above MinSolvedDelta (a real, not trivial, gain).
type GoNoGoThreshold struct {
	MaxP            float64 `json:"max_p"`
	MinSolvedDelta  float64 `json:"min_solved_delta"`
	BootstrapIters  int     `json:"bootstrap_iters"`
	Seed            int64   `json:"seed"`
	Alpha           float64 `json:"alpha"`
}

// ArmComparison is the full paired verdict of an experiment arm vs a baseline arm.
type ArmComparison struct {
	BaselineArm   string        `json:"baseline_arm"`
	ExperimentArm string        `json:"experiment_arm"`
	PairedTasks   int           `json:"paired_tasks"`
	BaselineSolveRate   float64 `json:"baseline_solve_rate"`
	ExperimentSolveRate float64 `json:"experiment_solve_rate"`
	McNemar       McNemarResult `json:"mcnemar"`
	SolveDeltaCI  BootstrapCI   `json:"solve_delta_ci"`
	Decision      string        `json:"decision"` // GO | NO-GO
	Rationale     string        `json:"rationale"`
}

// CompareArms produces the paired statistical verdict + the pre-registered go/no-go
// decision for an experiment arm against a baseline arm over the tasks they both ran.
func CompareArms(baseline, experiment []TaskOutcome, name1, name2 string, th GoNoGoThreshold) ArmComparison {
	aSolved, bSolved, _ := pairOutcomes(baseline, experiment)
	n := len(aSolved)
	deltas := make([]float64, n)
	baseSolved, expSolved := 0, 0
	for i := 0; i < n; i++ {
		bi, ei := 0.0, 0.0
		if aSolved[i] {
			bi = 1
			baseSolved++
		}
		if bSolved[i] {
			ei = 1
			expSolved++
		}
		deltas[i] = ei - bi
	}
	cmp := ArmComparison{
		BaselineArm: name1, ExperimentArm: name2, PairedTasks: n,
		McNemar: McNemar(aSolved, bSolved),
	}
	if n > 0 {
		cmp.BaselineSolveRate = float64(baseSolved) / float64(n)
		cmp.ExperimentSolveRate = float64(expSolved) / float64(n)
		cmp.SolveDeltaCI = PairedBootstrapMeanCI(deltas, th.BootstrapIters, th.Seed, th.Alpha)
	}
	// pre-registered decision: significant AND the whole CI clears the minimum effect.
	if n > 0 && cmp.McNemar.PValue <= th.MaxP && cmp.SolveDeltaCI.Lo > th.MinSolvedDelta {
		cmp.Decision = "GO"
		cmp.Rationale = "paired improvement is significant (p≤MaxP) and the solve-rate delta CI clears the minimum effect"
	} else {
		cmp.Decision = "NO-GO"
		cmp.Rationale = "did not clear the pre-registered bar (significance and/or minimum-effect CI)"
	}
	return cmp
}

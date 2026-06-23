package workspaceops

import (
	"math"
	"testing"
)

func TestMcNemarDetectsPairedImprovement(t *testing.T) {
	// 20 tasks: baseline solves 5; experiment solves those 5 PLUS 10 more, regresses 0.
	// All 10 discordant pairs favor the experiment -> highly significant.
	var base, exp []bool
	for i := 0; i < 20; i++ {
		base = append(base, i < 5)
		exp = append(exp, i < 15)
	}
	r := McNemar(base, exp)
	if r.BImprovedOverA != 10 || r.AImprovedOverB != 0 {
		t.Fatalf("discordant counts wrong: %+v", r)
	}
	if r.PValue > 0.01 {
		t.Fatalf("10-vs-0 discordant should be significant, p=%.4f", r.PValue)
	}
}

func TestMcNemarNoDifferenceIsNotSignificant(t *testing.T) {
	// equal, symmetric discordance -> not significant.
	base := []bool{true, false, true, false, true, false}
	exp := []bool{false, true, false, true, true, false}
	r := McNemar(base, exp)
	if r.BImprovedOverA != 2 || r.AImprovedOverB != 2 {
		t.Fatalf("expected 2/2 discordant, got %+v", r)
	}
	if r.PValue < 0.5 {
		t.Fatalf("symmetric discordance should be far from significant, p=%.4f", r.PValue)
	}
}

func TestPairedBootstrapCIBracketsConstantDelta(t *testing.T) {
	// every task improved by exactly +1 -> the mean-delta CI must be a tight [1,1].
	deltas := make([]float64, 50)
	for i := range deltas {
		deltas[i] = 1.0
	}
	ci := PairedBootstrapMeanCI(deltas, 2000, 42, 0.05)
	if math.Abs(ci.Mean-1.0) > 1e-9 || math.Abs(ci.Lo-1.0) > 1e-9 || math.Abs(ci.Hi-1.0) > 1e-9 {
		t.Fatalf("constant +1 deltas must yield CI [1,1], got %+v", ci)
	}
}

func TestPairedBootstrapCIIsReproducible(t *testing.T) {
	deltas := []float64{1, 0, 1, -1, 0, 1, 1, 0, -1, 1}
	a := PairedBootstrapMeanCI(deltas, 1000, 7, 0.05)
	b := PairedBootstrapMeanCI(deltas, 1000, 7, 0.05)
	if a != b {
		t.Fatalf("same seed must reproduce the CI: %+v vs %+v", a, b)
	}
}

func TestCompareArmsGoNoGoDecision(t *testing.T) {
	th := GoNoGoThreshold{MaxP: 0.05, MinSolvedDelta: 0.0, BootstrapIters: 3000, Seed: 1, Alpha: 0.05}

	mkArm := func(solveUpTo int) []TaskOutcome {
		var out []TaskOutcome
		for i := 0; i < 30; i++ {
			out = append(out, TaskOutcome{TaskID: string(rune('A'+i/26)) + string(rune('a'+i%26)), Solved: i < solveUpTo})
		}
		return out
	}
	// governed+drift solves 25/30, memory_off solves 8/30, all gains are net positive.
	baseline := mkArm(8)
	experiment := mkArm(25)

	cmp := CompareArms(baseline, experiment, ArmMemoryOff, ArmGovernedWithDrift, th)
	if cmp.PairedTasks != 30 {
		t.Fatalf("expected 30 paired tasks, got %d", cmp.PairedTasks)
	}
	if cmp.Decision != "GO" {
		t.Fatalf("a large significant improvement must be GO, got %s (%+v)", cmp.Decision, cmp)
	}
	if cmp.SolveDeltaCI.Lo <= 0 {
		t.Fatalf("the solve-delta CI should be entirely positive, got %+v", cmp.SolveDeltaCI)
	}

	// equal arms -> NO-GO.
	cmp2 := CompareArms(mkArm(15), mkArm(15), ArmMemoryOff, ArmGovernedWithDrift, th)
	if cmp2.Decision != "NO-GO" {
		t.Fatalf("identical arms must be NO-GO, got %s", cmp2.Decision)
	}
}

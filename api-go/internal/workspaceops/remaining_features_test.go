package workspaceops

import (
	"strings"
	"testing"
)

func TestScoreRunConfidence(t *testing.T) {
	zero := 0
	good := scoreRunConfidence("ws", runRecord{RunID: "r1", Status: "succeeded", ExitCode: &zero, Plan: &RunPlan{}})
	if good.Level != "high" || good.Confidence < 70 {
		t.Fatalf("expected high confidence, got %+v", good)
	}
	bad := scoreRunConfidence("ws", runRecord{RunID: "r2", Status: "failed"})
	if bad.Level != "low" {
		t.Fatalf("expected low confidence for failed run, got %+v", bad)
	}
}

func TestSuggestOwners(t *testing.T) {
	fp := "fp1"
	v := "alice"
	src := issueRecord{BugID: "a", Title: "Login broken", Summary: strPtr("login fails"), Fingerprint: &fp}
	all := []issueRecord{
		src,
		{BugID: "b", Title: "Login broken", Summary: strPtr("login fails"), Fingerprint: &fp, VerifiedBy: &v},
	}
	suggestions := suggestOwners(src, all)
	if len(suggestions) == 0 || suggestions[0].Owner != "alice" {
		t.Fatalf("expected alice suggested from duplicate's verifier, got %+v", suggestions)
	}
}

func TestBuildEvalTimelineEntries(t *testing.T) {
	batches := []EvalReplayBatchRecord{
		{BatchID: "b1", CreatedAt: "2026-01-01T00:00:00Z", QueuedRunIDs: []string{"r1", "r2"}},
		{BatchID: "b2", CreatedAt: "2026-01-02T00:00:00Z", QueuedRunIDs: []string{"r3", "r4"}},
	}
	runStatus := map[string]string{"r1": "succeeded", "r2": "failed", "r3": "succeeded", "r4": "succeeded"}
	runMetrics := map[string]*RunMetrics{
		"r1": {DurationMS: 1000, EstimatedCost: 0.10},
		"r2": {DurationMS: 2000, EstimatedCost: 0.20},
		"r3": {DurationMS: 500, EstimatedCost: 0.05},
		"r4": {DurationMS: 700, EstimatedCost: 0.05},
	}
	entries := buildEvalTimelineEntries(batches, runStatus, runMetrics)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// b2 (100%) should rank above b1 (50%); chronologically b2 is second and improved
	if entries[1].BatchID != "b2" || entries[1].SuccessRate != 1.0 || entries[1].Rank != 1 {
		t.Fatalf("b2 should be rank 1 at 100%%: %+v", entries[1])
	}
	if entries[1].Movement <= 0 {
		t.Fatalf("expected positive movement for improved batch, got %d", entries[1].Movement)
	}
	// enriched columns: cost + avg duration aggregated from run metrics.
	if entries[1].TotalCostUSD != 0.10 || entries[1].AvgDurationMS != 600 {
		t.Fatalf("b2 cost/duration aggregation wrong: %+v", entries[1])
	}
	// causal why-moved attributes the improvement to the metric deltas.
	if !strings.Contains(entries[1].WhyMoved, "rank improved") || !strings.Contains(entries[1].WhyMoved, "success rate") {
		t.Fatalf("why_moved must explain the rank change causally, got %q", entries[1].WhyMoved)
	}
	if entries[0].WhyMoved == "" {
		t.Fatalf("first batch should still have a why_moved note")
	}
}

func TestExtractAcceptanceCriteria(t *testing.T) {
	desc := "Some intro\n- [ ] User can log in\n- [x] Password is hashed\nAC: rate limiting enforced\nrandom line"
	ac := extractAcceptanceCriteria(desc)
	if len(ac) != 3 {
		t.Fatalf("expected 3 criteria, got %d: %v", len(ac), ac)
	}
}

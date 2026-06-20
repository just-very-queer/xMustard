package workspaceops

import (
	"testing"
)

func TestFeedbackBoostsAndRanking(t *testing.T) {
	dir := t.TempDir()
	ws := "wsFb"
	if err := writeJSON(dir+"/workspaces/"+ws+"/snapshot.json",
		map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": t.TempDir()}}); err != nil {
		t.Fatal(err)
	}

	// a verified path gets a positive boost; a failing path gets suppressed.
	if err := RecordFeedback(dir, ws, "verify", []string{"good.go"}); err != nil {
		t.Fatal(err)
	}
	RecordFeedback(dir, ws, "run_success", []string{"good.go"})
	RecordFeedback(dir, ws, "run_fail", []string{"bad.go"})

	boosts := feedbackBoosts(dir, ws)
	if boosts["good.go"] <= 0 {
		t.Fatalf("verified+succeeded path should have a positive boost, got %v", boosts["good.go"])
	}
	if boosts["bad.go"] >= 0 {
		t.Fatalf("failing path should be suppressed (<=0), got %v", boosts["bad.go"])
	}

	// applyFeedbackToHits reorders: an equal-score good path rises above a bad one.
	hits := []searchHit{
		{Kind: "file", Name: "bad.go", Path: "bad.go", Score: 1.0, Reason: "match"},
		{Kind: "file", Name: "good.go", Path: "good.go", Score: 1.0, Reason: "match"},
	}
	ranked := applyFeedbackToHits(dir, ws, hits)
	if ranked[0].Path != "good.go" {
		t.Fatalf("feedback should rank good.go first, got %s", ranked[0].Path)
	}
}

func TestRecordFeedbackRejectsBadWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := RecordFeedback(dir, "../escape", "verify", []string{"x.go"}); err == nil {
		t.Fatal("expected rejection for traversal workspace id")
	}
}

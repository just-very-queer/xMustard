package workspaceops

import "testing"

func TestScoreIssueQualityRecord(t *testing.T) {
	line := 42
	excerpt := "panic: nil deref"
	strong := issueRecord{
		BugID:    "bug-1",
		Title:    "Crash on empty workspace load",
		Severity: "P1",
		Summary:  strPtr("Loading an empty workspace panics. Steps to reproduce: open an empty repo and call load. Expected graceful handling."),
		Impact:   strPtr("All users with empty repos hit a crash."),
		Evidence: []evidenceRef{{Path: "app/load.go", Line: &line, Excerpt: &excerpt}},
		Labels:   []string{"backend"},
	}
	score := scoreIssueQualityRecord("ws", strong)
	if score.Overall < 70 {
		t.Fatalf("expected strong issue to score high, got %d (%+v)", score.Overall, score)
	}
	if !score.HasSeverity || !score.HasSummary || !score.HasImpact || !score.HasEvidence || !score.HasRepro {
		t.Fatalf("expected all presence flags true: %+v", score)
	}
	if len(score.Suggestions) != 0 {
		t.Fatalf("expected no suggestions for a complete issue, got %v", score.Suggestions)
	}

	bare := issueRecord{BugID: "bug-2", Title: "x"}
	weak := scoreIssueQualityRecord("ws", bare)
	if weak.Overall > 30 {
		t.Fatalf("expected bare issue to score low, got %d", weak.Overall)
	}
	if len(weak.Suggestions) == 0 {
		t.Fatalf("expected suggestions for a bare issue")
	}
}

func TestDuplicateMatches(t *testing.T) {
	fp := "abc123"
	src := issueRecord{BugID: "a", Title: "Login button does nothing", Summary: strPtr("clicking login is a no-op"), Severity: "P2", Fingerprint: &fp, Source: "scan"}
	others := []issueRecord{
		{BugID: "b", Title: "Login button does nothing", Summary: strPtr("clicking login is a no-op"), Severity: "P2", Fingerprint: &fp, Source: "scan"}, // fingerprint
		{BugID: "c", Title: "Login button does nothing on click", Summary: strPtr("clicking login is a no-op")}, // fuzzy (high token overlap, not identical title)
		{BugID: "d", Title: "Totally unrelated database migration", Summary: strPtr("add a postgres index")},                                               // none
	}
	matches := duplicateMatches(src, others)
	if len(matches) < 2 {
		t.Fatalf("expected >=2 matches (fingerprint + fuzzy), got %d: %+v", len(matches), matches)
	}
	if matches[0].MatchType != "fingerprint" || matches[0].Similarity != 1.0 {
		t.Fatalf("expected fingerprint match first, got %+v", matches[0])
	}
	for _, m := range matches {
		if m.TargetID == "d" {
			t.Fatalf("unrelated issue d should not match: %+v", m)
		}
	}
}

func TestTriageIssueRecord(t *testing.T) {
	sec := issueRecord{BugID: "a", Title: "Auth bypass via SQL injection", Summary: strPtr("a crash and security exploit in login")}
	s := triageIssueRecord("ws", sec)
	if s.SuggestedSeverity == nil || *s.SuggestedSeverity != "P0" {
		t.Fatalf("expected P0 from crash/security keywords, got %v", s.SuggestedSeverity)
	}
	found := false
	for _, l := range s.SuggestedLabels {
		if l == "security" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 'security' label, got %v", s.SuggestedLabels)
	}
	if s.Confidence <= 0.3 {
		t.Fatalf("expected confidence boosted by matches, got %v", s.Confidence)
	}
}

func TestTestSuggestionsForRecord(t *testing.T) {
	withEvidence := issueRecord{BugID: "a", Title: "Bug in parser", Severity: "P1", Evidence: []evidenceRef{{Path: "src/parser.py"}}}
	out := testSuggestionsForRecord("ws", withEvidence)
	if len(out) != 1 || out[0].TestFile != "src/test_parser.py" {
		t.Fatalf("expected python test path src/test_parser.py, got %+v", out)
	}
	if out[0].Priority != "high" {
		t.Fatalf("expected high priority for P1, got %s", out[0].Priority)
	}

	noEvidence := issueRecord{BugID: "b", Title: "Some UI glitch", Severity: "P3"}
	out2 := testSuggestionsForRecord("ws", noEvidence)
	if len(out2) != 1 || out2[0].Priority != "low" {
		t.Fatalf("expected one low-priority reproduction suggestion, got %+v", out2)
	}
}

package workspaceops

import "testing"

func TestSummarizeSecurityReview(t *testing.T) {
	d := []SecurityDisposition{
		{FindingID: "f1", Disposition: "open", Exploitability: "exploited"},
		{FindingID: "f2", Disposition: "accepted_risk", Exploitability: "theoretical", RiskAcceptanceNote: "ok"},
		{FindingID: "f3", Disposition: "suppressed", Exploitability: "none", Suppressed: true, SuppressionReason: "dup"},
		{FindingID: "f4", Disposition: "confirmed", Exploitability: "poc"}, // no linked profiles -> unverified + open_exploitable
	}
	p := summarizeSecurityReview("ws", d)
	if p.TotalDispositioned != 4 {
		t.Fatalf("expected 4, got %d", p.TotalDispositioned)
	}
	if p.OpenExploitable != 2 {
		t.Fatalf("expected 2 open_exploitable (open/exploited + confirmed/poc), got %d", p.OpenExploitable)
	}
	if p.AcceptedRiskCount != 1 || p.SuppressedCount != 1 {
		t.Fatalf("expected 1 accepted + 1 suppressed, got %d/%d", p.AcceptedRiskCount, p.SuppressedCount)
	}
	if p.UnverifiedConfirmed != 1 {
		t.Fatalf("expected 1 unverified_confirmed, got %d", p.UnverifiedConfirmed)
	}
	if p.ByDisposition["open"] != 1 || p.ByExploitability["poc"] != 1 {
		t.Fatalf("breakdown wrong: %+v / %+v", p.ByDisposition, p.ByExploitability)
	}
}

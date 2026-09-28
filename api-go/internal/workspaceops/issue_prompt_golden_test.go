package workspaceops

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/rustcore"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden")

// TestIssueContextPromptGolden pins the issue-context prompt byte for byte: once with
// every section populated past its item limits (optional fields set and blank), once
// with every input empty (each section's fallback line).
func TestIssueContextPromptGolden(t *testing.T) {
	s := func(v string) *string { return &v }
	n := func(v int) *int { return &v }
	many := func(prefix string, k int) []string {
		out := make([]string, k)
		for i := range out {
			out[i] = prefix + string(rune('a'+i))
		}
		return out
	}
	workspace := workspaceRecord{WorkspaceID: "ws1", Name: "demo", RootPath: "/repo/demo"}
	issue := issueRecord{BugID: "BUG-7", Title: "Export drops rows", Severity: "high", Source: "tracker", DocStatus: "open", CodeStatus: "confirmed",
		Summary: s("CSV export loses the last row."), Impact: s("  "),
		Evidence:             []evidenceRef{{Path: "api/export.go", Line: n(42)}, {Path: "api/rows.go"}},
		VerificationEvidence: []evidenceRef{{Path: "api/export_test.go", Line: n(7)}}}
	for i := 0; i < 10; i++ {
		issue.Evidence = append(issue.Evidence, evidenceRef{Path: "extra/e" + string(rune('a'+i)) + ".go"})
	}
	fixes := []FixRecord{
		{FixID: "fix-1", Status: "merged", Summary: "clamp index", Actor: activityActor{Name: "agent", Label: "Codex"}, ChangedFiles: many("f", 6)},
		{FixID: "fix-2", Status: "draft", Summary: "retry", Actor: activityActor{Name: "alice"}},
	}
	for i := 0; i < 4; i++ {
		fixes = append(fixes, FixRecord{FixID: "fix-x", Status: "x", Actor: activityActor{Name: "x"}})
	}
	activity := []activityRecord{
		{CreatedAt: "2026-09-01", Summary: "status changed", Details: map[string]any{"before_after": map[string]any{
			"status": map[string]any{"from": "open", "to": "triaged"}, "severity": "high", "labels": map[string]any{"from": nil, "to": []string{"x"}}}}},
		{CreatedAt: "2026-09-02", Summary: "commented", Details: map[string]any{"before_after": map[string]any{}}},
		{CreatedAt: "2026-09-03", Summary: "no details"},
	}
	for i := 0; i < 6; i++ {
		activity = append(activity, activityRecord{CreatedAt: "2026-09-1" + string(rune('0'+i)), Summary: "later"})
	}
	var guidance []RepoGuidanceRecord
	for i := 0; i < replayGuidanceLimit+2; i++ {
		guidance = append(guidance, RepoGuidanceRecord{Path: "docs/g" + string(rune('a'+i)) + ".md", Kind: "doc", Title: "Guide", AlwaysOn: i%2 == 0,
			Summary: map[bool]string{true: "", false: "summary"}[i%3 == 0]})
	}
	profiles := []rustcore.VerificationProfileInput{
		{Name: "unit", TestCommand: "go test ./...", CoverageCommand: s("go test -cover"), CoverageReportPath: s("cover.out"), CoverageFormat: "go"},
		{Name: "blank", TestCommand: "make test", CoverageCommand: s(" "), CoverageReportPath: s(""), CoverageFormat: "unknown"},
		{Name: "plain", TestCommand: "npm test"}, {Name: "fourth", TestCommand: "t4"}, {Name: "fifth", TestCommand: "t5"},
	}
	tickets := []TicketContextRecord{
		{Provider: "jira", ExternalID: s("APP-1"), Status: s("In Progress"), Title: "Export", Summary: "fix it", AcceptanceCriteria: many("c", 5)},
		{Provider: "github", ExternalID: s(" "), Title: "Bare", Summary: " "},
	}
	threats := []ThreatModelRecord{
		{ThreatModelID: "tm1", Title: "Export abuse", Methodology: "STRIDE", Status: "draft", Summary: "exfil", Assets: many("asset-", 5), AbuseCases: many("abuse-", 3), Mitigations: many("mit-", 3)},
		{ThreatModelID: "tm2", Title: "Empty model", Methodology: "LINDDUN", Status: "new"},
		{ThreatModelID: "tm3", Title: "Third"}, {ThreatModelID: "tm4", Title: "Fourth"},
	}
	vulns := []VulnerabilityFindingRecord{
		{Title: "SQLi", Scanner: "semgrep", Source: "sast", Severity: "critical", Status: "open", Summary: "tainted query",
			LocationPath: s("api/q.go"), LocationLine: n(9), RuleID: s("go.sqli"), CWEIDs: many("CWE-", 4), CVEIDs: many("CVE-", 3),
			Evidence: many("ev-", 3), ThreatModelIDs: []string{"tm1", "missing", "tm4"}},
		{Title: "Bare", Scanner: "x", Source: "y", Severity: "low", Status: "new", LocationPath: s(" "), LocationLine: n(3), RuleID: s("")},
	}
	dumps := []BrowserDumpRecord{
		{Label: "export page", Source: "playwright", PageTitle: s("Export"), PageURL: s("/api/export"), Summary: "",
			ConsoleMessages: many("console-", 3), NetworkRequests: many("GET /r", 3), DOMSnapshot: " <div>\n" + strings.Repeat("row ", 80) + "</div>\n"},
		{Label: "bare", Source: "manual", PageTitle: s(" "), Summary: "user report", DOMSnapshot: "   "},
	}
	repoMap := &rustcore.RepoMapSummary{TopDirectories: []rustcore.RepoMapDirectoryRecord{
		{Path: "api", SourceFileCount: 12, TestFileCount: 4}, {Path: "web", SourceFileCount: 30}, {Path: "a"}, {Path: "b"}, {Path: "c"}, {Path: "d"}}}
	dynamic := &DynamicContextBundle{
		SymbolContext: []RepoMapSymbolRecord{
			{Path: "api/export.go", Symbol: "Export", Kind: "func", LineStart: n(40), EnclosingScope: s("Handler"), Reason: s("named in the issue")},
			{Path: "api/rows.go", Symbol: "rows", Kind: "var", EnclosingScope: s(" "), Reason: s("")},
		},
		SemanticMatches: []SemanticPatternMatchRecord{
			{Path: "api/export.go", LineStart: n(44), Language: s("go"), Reason: s("loop bound"), MatchedText: strings.Repeat("m", 130)},
			{Path: "api/rows.go", Language: s(" "), MatchedText: "rows[i]"},
		},
		RelatedContext: []RelatedContextRecord{
			{ArtifactType: "run", Title: "run 3", Path: s("runs/3.log"), Reason: s(" failed on export "), MatchedTerms: many("t", 4)},
			{ArtifactType: "fix", Title: "fix 1"},
		},
	}
	semantic := &SemanticIndexStatus{Status: "stale", StaleReasons: many("reason-", 4), Warnings: many("warn-", 4)}
	ledger := []ContextRetrievalLedgerEntry{
		{SourceType: "file", Title: "export.go", Path: s("api/export.go"), Reason: "evidence", MatchedTerms: many("x", 4), Score: 9},
		{SourceType: "fix", Title: "fix-1", Path: s(" "), Reason: "prior fix", Score: 3},
	}
	repoConfig := &RepoConfigRecord{Description: "Demo service", CodeGuidelines: many("g", 8), PathFilters: many("p", 2),
		MCPServers: []RepoMCPServerRecord{{Name: "docs", Description: "Docs server"}, {Name: "db", Usage: "query rows"}, {Name: "bare"}, {Name: "fourth"}}}
	pathInstructions := []RepoPathInstructionMatch{
		{Path: "api/**", Title: s("API rules"), Instructions: "keep handlers thin", MatchedPaths: many("api/", 5)},
		{Path: "web/**", Title: s(" "), Instructions: "no inline styles", MatchedPaths: []string{"web/a.tsx"}},
	}
	full := buildIssueContextPrompt(workspace, issue, many("focus/", 14), fixes, activity, guidance, profiles, tickets, threats, dumps, vulns,
		many("related/", 9), repoMap, dynamic, semantic, ledger, repoConfig, pathInstructions)
	empty := buildIssueContextPrompt(workspaceRecord{RootPath: "/r"}, issueRecord{BugID: "B"}, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, &DynamicContextBundle{}, nil, nil, &RepoConfigRecord{Description: " "}, nil)
	got := full + "\n\n=== empty inputs ===\n" + empty + "\n"
	path := filepath.Join("testdata", "issue_context_prompt.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run with -update)", path, err)
	}
	if string(want) != got {
		t.Fatalf("prompt differs from %s:\n%s", path, got)
	}
}

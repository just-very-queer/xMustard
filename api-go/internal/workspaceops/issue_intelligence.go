package workspaceops

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Issue intelligence: deterministic, inspectable heuristics over the workspace
// issue records — quality scoring, duplicate detection, triage suggestions, and
// test suggestions. These back the issue-detail UI actions. They are pure (the
// scoring functions take records and return scores) so they are easy to test;
// the exported entry points load the workspace snapshot and delegate.

// ---- response shapes (match frontend/src/lib/types.ts) ----

type IssueQualityScore struct {
	IssueID         string   `json:"issue_id"`
	WorkspaceID     string   `json:"workspace_id"`
	Overall         int      `json:"overall"`
	Completeness    int      `json:"completeness"`
	Clarity         int      `json:"clarity"`
	EvidenceQuality int      `json:"evidence_quality"`
	HasRepro        bool     `json:"has_repro"`
	HasSeverity     bool     `json:"has_severity"`
	HasEvidence     bool     `json:"has_evidence"`
	HasImpact       bool     `json:"has_impact"`
	HasSummary      bool     `json:"has_summary"`
	TitleLength     int      `json:"title_length"`
	SummaryLength   int      `json:"summary_length"`
	EvidenceCount   int      `json:"evidence_count"`
	Suggestions     []string `json:"suggestions"`
	CalculatedAt    string   `json:"calculated_at"`
}

type DuplicateMatch struct {
	SourceID     string   `json:"source_id"`
	TargetID     string   `json:"target_id"`
	Similarity   float64  `json:"similarity"`
	MatchType    string   `json:"match_type"` // exact | fuzzy | fingerprint
	SharedFields []string `json:"shared_fields"`
}

type TriageSuggestion struct {
	IssueID          string   `json:"issue_id"`
	WorkspaceID      string   `json:"workspace_id"`
	SuggestedSeverity *string `json:"suggested_severity"`
	SuggestedLabels  []string `json:"suggested_labels"`
	SuggestedOwner   *string  `json:"suggested_owner"`
	Confidence       float64  `json:"confidence"`
	Reasoning        string   `json:"reasoning"`
	CalculatedAt     string   `json:"calculated_at"`
}

type TestSuggestion struct {
	SuggestionID    string  `json:"suggestion_id"`
	IssueID         string  `json:"issue_id"`
	WorkspaceID     string  `json:"workspace_id"`
	TestFile        string  `json:"test_file"`
	TestDescription string  `json:"test_description"`
	Priority        string  `json:"priority"` // high | medium | low
	Rationale       string  `json:"rationale"`
	SuggestedCode   *string `json:"suggested_code"`
	CreatedAt       string  `json:"created_at"`
}

// ---- helpers ----

func derefStr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func strPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	v := value
	return &v
}

func findIssueRecord(dataDir, workspaceID, issueID string) (issueRecord, []issueRecord, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return issueRecord{}, nil, err
	}
	for _, issue := range snapshot.Issues {
		if issue.BugID == issueID {
			return issue, snapshot.Issues, nil
		}
	}
	return issueRecord{}, snapshot.Issues, os.ErrNotExist
}

func clampScore(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// ---- quality scoring ----

func scoreIssueQualityRecord(workspaceID string, issue issueRecord) IssueQualityScore {
	summary := derefStr(issue.Summary)
	impact := derefStr(issue.Impact)
	notes := derefStr(issue.Notes)

	hasSeverity := strings.TrimSpace(issue.Severity) != ""
	hasSummary := strings.TrimSpace(summary) != ""
	hasImpact := strings.TrimSpace(impact) != ""
	hasEvidence := len(issue.Evidence) > 0
	reproText := strings.ToLower(summary + " " + notes)
	hasRepro := strings.Contains(reproText, "repro") ||
		strings.Contains(reproText, "steps to reproduce") ||
		strings.Contains(reproText, "reproduce") ||
		evidenceHasLocation(issue.Evidence)

	titleLen := len(strings.TrimSpace(issue.Title))
	summaryLen := len(strings.TrimSpace(summary))
	evidenceCount := len(issue.Evidence)

	// completeness: presence-weighted
	completeness := 0
	if hasSeverity {
		completeness += 15
	}
	if hasSummary {
		completeness += 20
	}
	if hasImpact {
		completeness += 15
	}
	if hasEvidence {
		completeness += 25
	}
	if hasRepro {
		completeness += 15
	}
	if len(issue.Labels) > 0 {
		completeness += 10
	}
	completeness = clampScore(completeness)

	// clarity: title + summary length sweet spots
	clarity := 0
	if titleLen >= 8 && titleLen <= 120 {
		clarity += 50
	} else if titleLen > 0 {
		clarity += 20
	}
	if summaryLen >= 40 {
		clarity += 50
	} else if summaryLen >= 12 {
		clarity += 30
	} else if summaryLen > 0 {
		clarity += 10
	}
	clarity = clampScore(clarity)

	// evidence_quality: count + located + verification evidence
	evidenceQuality := 0
	switch {
	case evidenceCount >= 3:
		evidenceQuality += 50
	case evidenceCount == 2:
		evidenceQuality += 40
	case evidenceCount == 1:
		evidenceQuality += 25
	}
	if evidenceHasLocation(issue.Evidence) {
		evidenceQuality += 25
	}
	if len(issue.VerificationEvidence) > 0 {
		evidenceQuality += 25
	}
	evidenceQuality = clampScore(evidenceQuality)

	overall := clampScore(int(math.Round(float64(completeness)*0.45 + float64(clarity)*0.25 + float64(evidenceQuality)*0.30)))

	suggestions := []string{}
	if !hasSeverity {
		suggestions = append(suggestions, "Set a severity so the issue can be prioritized.")
	}
	if !hasSummary {
		suggestions = append(suggestions, "Add a summary describing the problem.")
	} else if summaryLen < 40 {
		suggestions = append(suggestions, "Expand the summary; it is quite short.")
	}
	if !hasImpact {
		suggestions = append(suggestions, "Describe the impact (who/what is affected).")
	}
	if !hasEvidence {
		suggestions = append(suggestions, "Attach evidence (file paths, lines, or excerpts).")
	}
	if !hasRepro {
		suggestions = append(suggestions, "Add reproduction steps or located evidence.")
	}
	if len(issue.Labels) == 0 {
		suggestions = append(suggestions, "Add labels to aid triage and routing.")
	}

	return IssueQualityScore{
		IssueID:         issue.BugID,
		WorkspaceID:     workspaceID,
		Overall:         overall,
		Completeness:    completeness,
		Clarity:         clarity,
		EvidenceQuality: evidenceQuality,
		HasRepro:        hasRepro,
		HasSeverity:     hasSeverity,
		HasEvidence:     hasEvidence,
		HasImpact:       hasImpact,
		HasSummary:      hasSummary,
		TitleLength:     titleLen,
		SummaryLength:   summaryLen,
		EvidenceCount:   evidenceCount,
		Suggestions:     suggestions,
		CalculatedAt:    nowUTC(),
	}
}

func evidenceHasLocation(evidence []evidenceRef) bool {
	for _, e := range evidence {
		if e.Line != nil || (e.Excerpt != nil && strings.TrimSpace(*e.Excerpt) != "") {
			return true
		}
	}
	return false
}

// ScoreIssueQuality scores a single issue.
func ScoreIssueQuality(dataDir, workspaceID, issueID string) (*IssueQualityScore, error) {
	issue, _, err := findIssueRecord(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	score := scoreIssueQualityRecord(workspaceID, issue)
	return &score, nil
}

// ScoreAllIssueQuality scores every issue in the workspace.
func ScoreAllIssueQuality(dataDir, workspaceID string) ([]IssueQualityScore, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]IssueQualityScore, 0, len(snapshot.Issues))
	for _, issue := range snapshot.Issues {
		out = append(out, scoreIssueQualityRecord(workspaceID, issue))
	}
	return out, nil
}

// ---- duplicate detection ----

func tokenize(text string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(field) >= 3 {
			set[field] = struct{}{}
		}
	}
	return set
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func duplicateMatches(source issueRecord, others []issueRecord) []DuplicateMatch {
	matches := []DuplicateMatch{}
	srcTokens := tokenize(source.Title + " " + derefStr(source.Summary))
	srcTitle := strings.ToLower(strings.TrimSpace(source.Title))
	for _, other := range others {
		if other.BugID == source.BugID {
			continue
		}
		shared := []string{}
		matchType := ""
		similarity := 0.0

		// fingerprint match is strongest
		if source.Fingerprint != nil && other.Fingerprint != nil &&
			strings.TrimSpace(*source.Fingerprint) != "" &&
			*source.Fingerprint == *other.Fingerprint {
			matchType = "fingerprint"
			similarity = 1.0
			shared = append(shared, "fingerprint")
		} else if srcTitle != "" && srcTitle == strings.ToLower(strings.TrimSpace(other.Title)) {
			matchType = "exact"
			similarity = 1.0
			shared = append(shared, "title")
		} else {
			sim := jaccard(srcTokens, tokenize(other.Title+" "+derefStr(other.Summary)))
			if sim >= 0.5 {
				matchType = "fuzzy"
				similarity = math.Round(sim*100) / 100
			}
		}
		if matchType == "" {
			continue
		}
		if source.Severity != "" && source.Severity == other.Severity {
			shared = appendUnique(shared, "severity")
		}
		if source.Source != "" && source.Source == other.Source {
			shared = appendUnique(shared, "source")
		}
		matches = append(matches, DuplicateMatch{
			SourceID:     source.BugID,
			TargetID:     other.BugID,
			Similarity:   similarity,
			MatchType:    matchType,
			SharedFields: shared,
		})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Similarity > matches[j].Similarity
	})
	return matches
}

// FindDuplicates returns candidate duplicates for an issue.
func FindDuplicates(dataDir, workspaceID, issueID string) ([]DuplicateMatch, error) {
	issue, all, err := findIssueRecord(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	return duplicateMatches(issue, all), nil
}

// ---- triage ----

var triageSeverityKeywords = map[string][]string{
	"P0": {"crash", "data loss", "security", "exploit", "rce", "outage", "corruption", "panic"},
	"P1": {"fail", "broken", "regression", "leak", "deadlock", "race"},
	"P3": {"typo", "cosmetic", "nit", "polish", "wording", "doc"},
}

var triageLabelKeywords = map[string][]string{
	"security":    {"security", "vuln", "exploit", "cve", "auth", "injection"},
	"performance": {"slow", "performance", "latency", "memory", "leak", "timeout"},
	"testing":     {"test", "coverage", "flaky"},
	"ui":          {"ui", "button", "layout", "css", "render"},
	"docs":        {"doc", "readme", "comment", "typo"},
}

func triageIssueRecord(workspaceID string, issue issueRecord) TriageSuggestion {
	text := strings.ToLower(issue.Title + " " + derefStr(issue.Summary) + " " + derefStr(issue.Impact) + " " + derefStr(issue.Notes))

	var suggestedSeverity *string
	confidence := 0.3
	reasons := []string{}
	for sev, keywords := range triageSeverityKeywords {
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				if suggestedSeverity == nil || severityRank(sev) < severityRank(*suggestedSeverity) {
					s := sev
					suggestedSeverity = &s
				}
				reasons = append(reasons, fmt.Sprintf("matched %q -> %s", kw, sev))
				confidence += 0.15
				break
			}
		}
	}
	if suggestedSeverity == nil && issue.Severity != "" {
		s := issue.Severity
		suggestedSeverity = &s
		reasons = append(reasons, "kept existing severity")
	}

	labels := []string{}
	for label, keywords := range triageLabelKeywords {
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				labels = appendUnique(labels, label)
				reasons = append(reasons, fmt.Sprintf("matched %q -> label %q", kw, label))
				confidence += 0.1
				break
			}
		}
	}
	sort.Strings(labels)

	var owner *string
	if issue.VerifiedBy != nil && strings.TrimSpace(*issue.VerifiedBy) != "" {
		owner = issue.VerifiedBy
		reasons = append(reasons, "owner from prior verifier")
	}

	if confidence > 1 {
		confidence = 1
	}
	reasoning := "No strong triage signals found in the issue text."
	if len(reasons) > 0 {
		reasoning = strings.Join(reasons, "; ")
	}

	return TriageSuggestion{
		IssueID:           issue.BugID,
		WorkspaceID:       workspaceID,
		SuggestedSeverity: suggestedSeverity,
		SuggestedLabels:   labels,
		SuggestedOwner:    owner,
		Confidence:        math.Round(confidence*100) / 100,
		Reasoning:         reasoning,
		CalculatedAt:      nowUTC(),
	}
}

func severityRank(sev string) int {
	switch strings.ToUpper(strings.TrimSpace(sev)) {
	case "P0":
		return 0
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	default:
		return 4
	}
}

// TriageIssue suggests severity/labels/owner for one issue.
func TriageIssue(dataDir, workspaceID, issueID string) (*TriageSuggestion, error) {
	issue, _, err := findIssueRecord(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	suggestion := triageIssueRecord(workspaceID, issue)
	return &suggestion, nil
}

// TriageAllIssues triages every issue in the workspace.
func TriageAllIssues(dataDir, workspaceID string) ([]TriageSuggestion, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]TriageSuggestion, 0, len(snapshot.Issues))
	for _, issue := range snapshot.Issues {
		out = append(out, triageIssueRecord(workspaceID, issue))
	}
	return out, nil
}

// ---- test suggestions ----

func testFileForPath(path string) string {
	if path == "" {
		return "tests/test_new_case"
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch ext {
	case ".py":
		return filepath.Join(dir, "test_"+stem+".py")
	case ".go":
		return filepath.Join(dir, stem+"_test.go")
	case ".ts", ".tsx", ".js", ".jsx":
		return filepath.Join(dir, stem+".test"+ext)
	case ".rs":
		return filepath.Join(dir, stem+"_test.rs")
	default:
		return filepath.Join(dir, stem+".test"+ext)
	}
}

func priorityForSeverity(sev string) string {
	switch severityRank(sev) {
	case 0, 1:
		return "high"
	case 2:
		return "medium"
	default:
		return "low"
	}
}

func testSuggestionsForRecord(workspaceID string, issue issueRecord) []TestSuggestion {
	priority := priorityForSeverity(issue.Severity)
	now := nowUTC()
	title := strings.TrimSpace(issue.Title)
	if title == "" {
		title = issue.BugID
	}
	out := []TestSuggestion{}
	seen := map[string]struct{}{}
	for _, e := range issue.Evidence {
		if strings.TrimSpace(e.Path) == "" {
			continue
		}
		tf := testFileForPath(e.Path)
		if _, ok := seen[tf]; ok {
			continue
		}
		seen[tf] = struct{}{}
		out = append(out, TestSuggestion{
			SuggestionID:    fmt.Sprintf("ts_%s_%d", issue.BugID, len(out)+1),
			IssueID:         issue.BugID,
			WorkspaceID:     workspaceID,
			TestFile:        tf,
			TestDescription: fmt.Sprintf("Add a regression test for %q covering %s.", title, e.Path),
			Priority:        priority,
			Rationale:       fmt.Sprintf("Evidence points at %s; a test there pins the fixed behavior.", e.Path),
			SuggestedCode:   nil,
			CreatedAt:       now,
		})
	}
	if len(out) == 0 {
		out = append(out, TestSuggestion{
			SuggestionID:    fmt.Sprintf("ts_%s_1", issue.BugID),
			IssueID:         issue.BugID,
			WorkspaceID:     workspaceID,
			TestFile:        "tests/test_" + slugProfileID(title) + ".py",
			TestDescription: fmt.Sprintf("Add a test reproducing %q before fixing it.", title),
			Priority:        priority,
			Rationale:       "No located evidence yet; start with a reproduction test.",
			SuggestedCode:   nil,
			CreatedAt:       now,
		})
	}
	return out
}

func testSuggestionsPath(dataDir, workspaceID, issueID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "test_suggestions", issueID+".json")
}

// GenerateTestSuggestions computes and persists test suggestions for an issue.
func GenerateTestSuggestions(dataDir, workspaceID, issueID string) ([]TestSuggestion, error) {
	issue, _, err := findIssueRecord(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	suggestions := testSuggestionsForRecord(workspaceID, issue)
	if err := writeJSON(testSuggestionsPath(dataDir, workspaceID, issueID), suggestions); err != nil {
		return nil, err
	}
	return suggestions, nil
}

// ListTestSuggestions returns persisted suggestions (regenerating if absent).
func ListTestSuggestions(dataDir, workspaceID, issueID string) ([]TestSuggestion, error) {
	var suggestions []TestSuggestion
	if err := readJSON(testSuggestionsPath(dataDir, workspaceID, issueID), &suggestions); err != nil {
		if os.IsNotExist(err) {
			return GenerateTestSuggestions(dataDir, workspaceID, issueID)
		}
		return nil, err
	}
	return suggestions, nil
}

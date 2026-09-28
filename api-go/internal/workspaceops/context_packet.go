package workspaceops

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"xmustard/api-go/internal/rustcore"
)

type WorktreeStatus struct {
	Available      bool     `json:"available"`
	IsGitRepo      bool     `json:"is_git_repo"`
	Branch         *string  `json:"branch,omitempty"`
	HeadSHA        *string  `json:"head_sha,omitempty"`
	DirtyFiles     int      `json:"dirty_files"`
	StagedFiles    int      `json:"staged_files"`
	UntrackedFiles int      `json:"untracked_files"`
	Ahead          int      `json:"ahead"`
	Behind         int      `json:"behind"`
	DirtyPaths     []string `json:"dirty_paths"`
}

type FixRecord struct {
	FixID        string          `json:"fix_id"`
	WorkspaceID  string          `json:"workspace_id"`
	IssueID      string          `json:"issue_id"`
	Status       string          `json:"status"`
	Summary      string          `json:"summary"`
	How          *string         `json:"how,omitempty"`
	Actor        activityActor   `json:"actor"`
	RunID        *string         `json:"run_id,omitempty"`
	SessionID    *string         `json:"session_id,omitempty"`
	ChangedFiles []string        `json:"changed_files"`
	TestsRun     []string        `json:"tests_run"`
	Evidence     []evidenceRef   `json:"evidence"`
	Worktree     *WorktreeStatus `json:"worktree,omitempty"`
	Notes        *string         `json:"notes,omitempty"`
	UpdatedAt    string          `json:"updated_at"`
	RecordedAt   string          `json:"recorded_at"`
}

type RepoGuidanceRecord struct {
	GuidanceID      string   `json:"guidance_id"`
	WorkspaceID     string   `json:"workspace_id"`
	Kind            string   `json:"kind"`
	Title           string   `json:"title"`
	Path            string   `json:"path"`
	AlwaysOn        bool     `json:"always_on"`
	Priority        int      `json:"priority"`
	Summary         string   `json:"summary"`
	Excerpt         *string  `json:"excerpt,omitempty"`
	TriggerKeywords []string `json:"trigger_keywords"`
	UpdatedAt       *string  `json:"updated_at,omitempty"`
}

type RunbookRecord struct {
	RunbookID   string `json:"runbook_id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Template    string `json:"template"`
	BuiltIn     bool   `json:"built_in"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type RepoMapSymbolRecord struct {
	Path           string  `json:"path"`
	Symbol         string  `json:"symbol"`
	Kind           string  `json:"kind"`
	LineStart      *int    `json:"line_start,omitempty"`
	LineEnd        *int    `json:"line_end,omitempty"`
	EnclosingScope *string `json:"enclosing_scope,omitempty"`
	EvidenceSource string  `json:"evidence_source,omitempty"`
	Reason         *string `json:"reason,omitempty"`
	Score          int     `json:"score"`
}

type RelatedContextRecord struct {
	ArtifactType string   `json:"artifact_type"`
	ArtifactID   string   `json:"artifact_id"`
	Title        string   `json:"title"`
	Path         *string  `json:"path,omitempty"`
	Reason       *string  `json:"reason,omitempty"`
	MatchedTerms []string `json:"matched_terms"`
	Score        int      `json:"score"`
}

type VulnerabilityFindingRecord struct {
	FindingID      string   `json:"finding_id"`
	WorkspaceID    string   `json:"workspace_id"`
	IssueID        string   `json:"issue_id"`
	Scanner        string   `json:"scanner"`
	Source         string   `json:"source"`
	Severity       string   `json:"severity"`
	Status         string   `json:"status"`
	Title          string   `json:"title"`
	Summary        string   `json:"summary"`
	RuleID         *string  `json:"rule_id,omitempty"`
	LocationPath   *string  `json:"location_path,omitempty"`
	LocationLine   *int     `json:"location_line,omitempty"`
	CWEIDs         []string `json:"cwe_ids"`
	CVEIDs         []string `json:"cve_ids"`
	References     []string `json:"references"`
	Evidence       []string `json:"evidence"`
	ThreatModelIDs []string `json:"threat_model_ids"`
	RawPayload     *string  `json:"raw_payload,omitempty"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

type ContextRetrievalLedgerEntry struct {
	EntryID      string   `json:"entry_id"`
	SourceType   string   `json:"source_type"`
	SourceID     string   `json:"source_id"`
	Title        string   `json:"title"`
	Path         *string  `json:"path,omitempty"`
	Reason       string   `json:"reason"`
	MatchedTerms []string `json:"matched_terms"`
	Score        int      `json:"score"`
}

type DynamicContextBundle struct {
	SymbolContext     []RepoMapSymbolRecord                `json:"symbol_context"`
	SemanticMatches   []SemanticPatternMatchRecord         `json:"semantic_matches"`
	SemanticQueries   []SemanticQueryMaterializationRecord `json:"semantic_queries"`
	SemanticMatchRows []SemanticMatchMaterializationRecord `json:"semantic_match_rows"`
	RelatedContext    []RelatedContextRecord               `json:"related_context"`
}

type IssueContextPacket struct {
	Issue                         issueRecord                         `json:"issue"`
	Workspace                     workspaceRecord                     `json:"workspace"`
	SemanticStatus                *SemanticIndexStatus                `json:"semantic_status,omitempty"`
	TreeFocus                     []string                            `json:"tree_focus"`
	RelatedPaths                  []string                            `json:"related_paths"`
	EvidenceBundle                []evidenceRef                       `json:"evidence_bundle"`
	RecentFixes                   []FixRecord                         `json:"recent_fixes"`
	RecentActivity                []activityRecord                    `json:"recent_activity"`
	Guidance                      []RepoGuidanceRecord                `json:"guidance"`
	Runbook                       []string                            `json:"runbook"`
	AvailableRunbooks             []RunbookRecord                     `json:"available_runbooks"`
	AvailableVerificationProfiles []rustcore.VerificationProfileInput `json:"available_verification_profiles"`
	TicketContexts                []TicketContextRecord               `json:"ticket_contexts"`
	ThreatModels                  []ThreatModelRecord                 `json:"threat_models"`
	BrowserDumps                  []BrowserDumpRecord                 `json:"browser_dumps"`
	VulnerabilityFindings         []VulnerabilityFindingRecord        `json:"vulnerability_findings"`
	RepoMap                       *rustcore.RepoMapSummary            `json:"repo_map,omitempty"`
	DynamicContext                *DynamicContextBundle               `json:"dynamic_context,omitempty"`
	RetrievalLedger               []ContextRetrievalLedgerEntry       `json:"retrieval_ledger"`
	RepoConfig                    *RepoConfigRecord                   `json:"repo_config,omitempty"`
	MatchedPathInstructions       []RepoPathInstructionMatch          `json:"matched_path_instructions"`
	Worktree                      *WorktreeStatus                     `json:"worktree,omitempty"`
	Prompt                        string                              `json:"prompt"`
}

func BuildIssueContextPacket(dataDir string, workspaceID string, issueID string) (*IssueContextPacket, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	var issue *issueRecord
	for idx := range snapshot.Issues {
		if snapshot.Issues[idx].BugID == issueID {
			issue = &snapshot.Issues[idx]
			break
		}
	}
	if issue == nil {
		return nil, os.ErrNotExist
	}

	treeFocus := buildTreeFocus(*issue)
	evidenceBundle := append(append([]evidenceRef{}, issue.Evidence...), issue.VerificationEvidence...)

	runbooks, err := listRunbooks(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	defaultRunbook, err := resolveRunbook(dataDir, workspaceID, "fix")
	if err != nil {
		return nil, err
	}

	guidance, err := collectWorkspaceGuidance(snapshot.Workspace.RootPath, workspaceID)
	if err != nil {
		return nil, err
	}
	if len(guidance) > replayGuidanceLimit {
		guidance = guidance[:replayGuidanceLimit]
	}

	verificationProfiles, err := listVerificationProfilesForContext(dataDir, snapshot.Workspace, guidance)
	if err != nil {
		return nil, err
	}
	ticketContexts, err := ListTicketContexts(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	threatModels, err := ListThreatModels(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	browserDumps, err := ListBrowserDumps(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	vulnerabilityFindings, err := listVulnerabilityFindings(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	repoConfig, err := ReadWorkspaceRepoConfig(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	repoMap, err := loadOrBuildRepoMap(dataDir, workspaceID, snapshot.Workspace.RootPath)
	if err != nil {
		return nil, err
	}

	relatedPaths := rankRelatedPathsForIssue(*issue, treeFocus, ticketContexts, vulnerabilityFindings, repoMap)
	semanticStatus := bestEffortSemanticStatus(dataDir, workspaceID, relatedPaths)
	evidencePaths := make([]string, 0, len(evidenceBundle))
	for _, item := range evidenceBundle {
		if item.NormalizedPath != nil && strings.TrimSpace(*item.NormalizedPath) != "" {
			evidencePaths = append(evidencePaths, *item.NormalizedPath)
			continue
		}
		if strings.TrimSpace(item.Path) != "" {
			evidencePaths = append(evidencePaths, item.Path)
		}
	}
	matchedPathInstructions := matchRepoPathInstructions(repoConfig, append(append([]string{}, treeFocus...), append(relatedPaths, evidencePaths...)...))
	recentFixes, err := loadFixRecords(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	if len(recentFixes) > 5 {
		recentFixes = recentFixes[:5]
	}
	recentActivity, err := listReplayActivity(dataDir, workspaceID, issueID, 16)
	if err != nil {
		return nil, err
	}
	if len(recentActivity) > 8 {
		recentActivity = recentActivity[:8]
	}
	worktree := readWorktreeStatus(snapshot.Workspace.RootPath)
	dynamicContext := buildDynamicContext(dataDir, snapshot.Workspace, *issue, treeFocus, ticketContexts, threatModels, browserDumps, vulnerabilityFindings, recentFixes, recentActivity, relatedPaths, repoMap)
	if dynamicContext != nil {
		semanticPaths := make([]string, 0, len(dynamicContext.SemanticMatches))
		for _, item := range dynamicContext.SemanticMatches {
			semanticPaths = append(semanticPaths, item.Path)
		}
		relatedPaths = dedupeText(append(relatedPaths, semanticPaths...))
		if len(relatedPaths) > 8 {
			relatedPaths = relatedPaths[:8]
		}
	}
	matchedPathInstructions = matchRepoPathInstructions(repoConfig, append(append([]string{}, treeFocus...), append(relatedPaths, evidencePaths...)...))
	retrievalLedger := buildContextRetrievalLedger(*issue, treeFocus, evidenceBundle, relatedPaths, guidance, dynamicContext, matchedPathInstructions)

	packet := &IssueContextPacket{
		Issue:                         *issue,
		Workspace:                     snapshot.Workspace,
		SemanticStatus:                semanticStatus,
		TreeFocus:                     treeFocus[:min(len(treeFocus), 12)],
		RelatedPaths:                  relatedPaths,
		EvidenceBundle:                evidenceBundle[:min(len(evidenceBundle), 20)],
		RecentFixes:                   recentFixes,
		RecentActivity:                recentActivity,
		Guidance:                      guidance,
		Runbook:                       renderRunbookSteps(defaultRunbook.Template),
		AvailableRunbooks:             runbooks,
		AvailableVerificationProfiles: verificationProfiles,
		TicketContexts:                ticketContexts,
		ThreatModels:                  threatModels,
		BrowserDumps:                  browserDumps,
		VulnerabilityFindings:         vulnerabilityFindings,
		RepoMap:                       repoMap,
		DynamicContext:                dynamicContext,
		RetrievalLedger:               retrievalLedger,
		RepoConfig:                    repoConfig,
		MatchedPathInstructions:       matchedPathInstructions,
		Worktree:                      worktree,
	}
	packet.Prompt = buildIssueContextPrompt(
		packet.Workspace,
		packet.Issue,
		packet.TreeFocus,
		packet.RecentFixes,
		packet.RecentActivity,
		packet.Guidance,
		packet.AvailableVerificationProfiles,
		packet.TicketContexts,
		packet.ThreatModels,
		packet.BrowserDumps,
		packet.VulnerabilityFindings,
		packet.RelatedPaths,
		packet.RepoMap,
		packet.DynamicContext,
		packet.SemanticStatus,
		packet.RetrievalLedger,
		packet.RepoConfig,
		packet.MatchedPathInstructions,
	)
	return packet, nil
}

func BuildIssueWorkPacket(dataDir string, workspaceID string, issueID string, runbookID string) (*IssueContextPacket, error) {
	packet, err := BuildIssueContextPacket(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(runbookID) == "" {
		return packet, nil
	}

	runbook, err := resolveRunbook(dataDir, workspaceID, runbookID)
	if err != nil {
		return nil, err
	}
	packet.Runbook = renderRunbookSteps(runbook.Template)
	packet.Prompt = packet.Prompt + "\n\nSelected runbook: " + runbook.Name + "\n" + strings.TrimSpace(runbook.Template)
	return packet, nil
}

func buildTreeFocus(issue issueRecord) []string {
	focus := make([]string, 0, len(issue.Evidence)+len(issue.VerificationEvidence))
	seen := map[string]struct{}{}
	for _, item := range append(append([]evidenceRef{}, issue.Evidence...), issue.VerificationEvidence...) {
		focusPath := item.Path
		if item.NormalizedPath != nil && strings.TrimSpace(*item.NormalizedPath) != "" {
			focusPath = strings.TrimSpace(*item.NormalizedPath)
		}
		if focusPath == "" {
			continue
		}
		if _, exists := seen[focusPath]; exists {
			continue
		}
		seen[focusPath] = struct{}{}
		focus = append(focus, focusPath)
	}
	return focus
}

func listRunbooks(dataDir string, workspaceID string) ([]RunbookRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}

	saved, err := loadSavedRunbooks(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	merged := map[string]RunbookRecord{}
	for _, runbook := range defaultRunbooks(workspaceID) {
		merged[runbook.RunbookID] = runbook
	}
	for _, runbook := range saved {
		merged[runbook.RunbookID] = runbook
	}

	out := make([]RunbookRecord, 0, len(merged))
	for _, runbook := range merged {
		out = append(out, runbook)
	}
	slices.SortFunc(out, func(a, b RunbookRecord) int {
		if a.BuiltIn != b.BuiltIn {
			if a.BuiltIn {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Name)
		right := strings.ToLower(b.Name)
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
		if a.CreatedAt < b.CreatedAt {
			return -1
		}
		if a.CreatedAt > b.CreatedAt {
			return 1
		}
		return 0
	})
	return out, nil
}

func resolveRunbook(dataDir string, workspaceID string, runbookID string) (*RunbookRecord, error) {
	runbooks, err := listRunbooks(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	for idx := range runbooks {
		if runbooks[idx].RunbookID == runbookID {
			return &runbooks[idx], nil
		}
	}
	return nil, os.ErrNotExist
}

func loadSavedRunbooks(dataDir string, workspaceID string) ([]RunbookRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "runbooks.json")
	var runbooks []RunbookRecord
	if err := readJSON(path, &runbooks); err != nil {
		if os.IsNotExist(err) {
			return []RunbookRecord{}, nil
		}
		return nil, err
	}
	return runbooks, nil
}

func defaultRunbooks(workspaceID string) []RunbookRecord {
	now := nowUTC()
	return []RunbookRecord{
		{
			RunbookID:   "verify",
			WorkspaceID: workspaceID,
			Name:        "Verify",
			Description: "Validate reproduction and current failure shape before changing code.",
			Scope:       "issue",
			Template:    "1. Reproduce or validate the reported behavior in the current tree.\n2. Confirm the cited evidence and likely failing surface.\n3. Stop after verification and return findings, impacted files, and candidate tests.",
			BuiltIn:     true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			RunbookID:   "fix",
			WorkspaceID: workspaceID,
			Name:        "Fix",
			Description: "Default bug-fix workflow with tests and tracker provenance.",
			Scope:       "issue",
			Template:    "1. Verify the bug still reproduces in the current workspace tree.\n2. Inspect the cited evidence paths before changing code.\n3. Make the minimal safe fix.\n4. Add or update tests that fail before the fix and pass after the fix.\n5. Record a fix entry with files changed, tests run, and agent provenance.",
			BuiltIn:     true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			RunbookID:   "reproduce",
			WorkspaceID: workspaceID,
			Name:        "Reproduce",
			Description: "Focus only on reproduction, scope, and proof.",
			Scope:       "issue",
			Template:    "1. Reproduce the reported bug.\n2. Narrow the failing boundary to concrete files and functions.\n3. Suggest the smallest next-step fix plan.\n4. Do not modify code.",
			BuiltIn:     true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			RunbookID:   "drift-audit",
			WorkspaceID: workspaceID,
			Name:        "Drift Audit",
			Description: "Compare tracker evidence, code state, and tests for drift.",
			Scope:       "issue",
			Template:    "1. Check whether the tracker claim still matches code.\n2. Identify missing evidence, missing tests, or outdated status.\n3. Return only drift findings and recommended tracker updates.",
			BuiltIn:     true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}
}

func renderRunbookSteps(template string) []string {
	steps := []string{}
	normalized := strings.ReplaceAll(template, "\\n", "\n")
	numbering := regexp.MustCompile(`^\d+\.\s*`)
	for _, rawLine := range strings.Split(normalized, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "-*"))
		line = strings.TrimSpace(numbering.ReplaceAllString(line, ""))
		if line != "" {
			steps = append(steps, line)
		}
	}
	return steps
}

func loadFixRecords(dataDir string, workspaceID string, issueID string) ([]FixRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "fix_records.json")
	var fixes []FixRecord
	if err := readJSON(path, &fixes); err != nil {
		if os.IsNotExist(err) {
			return []FixRecord{}, nil
		}
		return nil, err
	}
	filtered := make([]FixRecord, 0, len(fixes))
	for _, fix := range fixes {
		if issueID == "" || fix.IssueID == issueID {
			filtered = append(filtered, fix)
		}
	}
	slices.SortFunc(filtered, func(a, b FixRecord) int {
		if a.RecordedAt > b.RecordedAt {
			return -1
		}
		if a.RecordedAt < b.RecordedAt {
			return 1
		}
		return 0
	})
	return filtered, nil
}

func listVerificationProfilesForContext(
	dataDir string,
	workspace workspaceRecord,
	guidance []RepoGuidanceRecord,
) ([]rustcore.VerificationProfileInput, error) {
	saved, err := loadSavedVerificationProfiles(dataDir, workspace.WorkspaceID)
	if err != nil {
		return nil, err
	}
	merged := map[string]rustcore.VerificationProfileInput{}
	inferred := inferVerificationProfilesFromGuidance(workspace, guidance)
	if len(inferred) == 0 {
		inferred = []rustcore.VerificationProfileInput{defaultVerificationProfile(workspace.WorkspaceID)}
	}
	for _, profile := range inferred {
		merged[profile.ProfileID] = profile
	}
	for _, profile := range saved {
		merged[profile.ProfileID] = profile
	}
	out := make([]rustcore.VerificationProfileInput, 0, len(merged))
	for _, profile := range merged {
		out = append(out, profile)
	}
	slices.SortFunc(out, func(a, b rustcore.VerificationProfileInput) int {
		if a.BuiltIn != b.BuiltIn {
			if a.BuiltIn {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Name)
		right := strings.ToLower(b.Name)
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
		if a.CreatedAt < b.CreatedAt {
			return -1
		}
		if a.CreatedAt > b.CreatedAt {
			return 1
		}
		return 0
	})
	return out, nil
}

func inferVerificationProfilesFromGuidance(workspace workspaceRecord, guidance []RepoGuidanceRecord) []rustcore.VerificationProfileInput {
	profiles := []rustcore.VerificationProfileInput{}
	seenCommands := map[string]struct{}{}
	for _, item := range guidance {
		// no-follow, bounded, same-fd read (item.Path is repo-relative) — never a raw
		// os.ReadFile join that would follow a swapped-in symlink out of the workspace.
		content, ok := readWorkspaceRegularFile(workspace.RootPath, item.Path)
		if !ok {
			continue
		}
		text := string(content)
		for _, command := range extractTestCommands(text) {
			normalized := strings.Join(strings.Fields(command), " ")
			if _, exists := seenCommands[normalized]; exists {
				continue
			}
			seenCommands[normalized] = struct{}{}
			reportPath := extractCoverageReportPath(text)
			if reportPath == nil {
				reportPath = extractCoverageReportPath(normalized)
			}
			coverageFormat := inferCoverageFormat(normalized, reportPath)
			var coverageCommand *string
			if coverageFormat != "unknown" || reportPath != nil {
				coverageCommand = &normalized
			}
			profileID := slugProfileID("inferred-" + normalized)
			profiles = append(profiles, rustcore.VerificationProfileInput{
				ProfileID:          profileID,
				WorkspaceID:        workspace.WorkspaceID,
				Name:               verificationProfileName(normalized),
				Description:        "Inferred from " + item.Path + ".",
				TestCommand:        normalized,
				CoverageCommand:    coverageCommand,
				CoverageReportPath: reportPath,
				CoverageFormat:     coverageFormat,
				MaxRuntimeSeconds:  60,
				RetryCount:         1,
				SourcePaths:        []string{item.Path},
				ChecklistItems:     []string{},
				BuiltIn:            true,
				CreatedAt:          nowUTC(),
				UpdatedAt:          nowUTC(),
			})
		}
	}
	if len(profiles) > 6 {
		profiles = profiles[:6]
	}
	return profiles
}

func extractTestCommands(text string) []string {
	pattern := regexp.MustCompile(`((?:python3?\s+-m\s+pytest|pytest|uv\s+run\s+pytest|npm\s+test|pnpm\s+test|yarn\s+test|cargo\s+test|go\s+test)[^\n\r;]*)`)
	commands := []string{}
	seen := map[string]struct{}{}
	for _, match := range pattern.FindAllString(text, -1) {
		command := strings.Join(strings.Fields(strings.TrimSpace(match)), " ")
		if command == "" {
			continue
		}
		if _, exists := seen[command]; exists {
			continue
		}
		seen[command] = struct{}{}
		commands = append(commands, command)
	}
	if len(commands) > 6 {
		commands = commands[:6]
	}
	return commands
}

func extractCoverageReportPath(text string) *string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`--cov-report=xml:?([^\s]+)?`),
		regexp.MustCompile(`(coverage(?:/[\w./-]+)?\.xml)`),
		regexp.MustCompile(`(coverage\.info)`),
		regexp.MustCompile(`(build/reports/jacoco/test/jacocoTestReport\.(?:csv|xml))`),
		regexp.MustCompile(`(target/site/jacoco/jacoco\.(?:csv|xml))`),
	}
	for idx, pattern := range patterns {
		matches := pattern.FindStringSubmatch(text)
		if len(matches) == 0 {
			continue
		}
		if idx == 0 {
			reportTarget := ""
			if len(matches) > 1 {
				reportTarget = strings.TrimSpace(matches[1])
			}
			if reportTarget == "" || reportTarget == "xml" {
				value := "coverage.xml"
				return &value
			}
			return &reportTarget
		}
		value := matches[len(matches)-1]
		return &value
	}
	return nil
}

func inferCoverageFormat(command string, reportPath *string) string {
	haystack := strings.ToLower(command + " " + firstNonEmptyPtr(reportPath))
	switch {
	case strings.Contains(haystack, "jacoco"):
		return "jacoco"
	case strings.Contains(haystack, "lcov") || strings.HasSuffix(firstNonEmptyPtr(reportPath), ".info"):
		return "lcov"
	case strings.Contains(haystack, "go test -cover") || strings.Contains(haystack, "coverprofile"):
		return "go"
	case strings.Contains(haystack, "cov-report=xml") || strings.HasSuffix(firstNonEmptyPtr(reportPath), ".xml"):
		return "cobertura"
	default:
		return "unknown"
	}
}

func verificationProfileName(command string) string {
	normalized := strings.ToLower(command)
	switch {
	case strings.Contains(normalized, "pytest"):
		return "Pytest verification"
	case strings.Contains(normalized, "npm run test:coverage") || strings.Contains(normalized, "vitest") || strings.Contains(normalized, "jest"):
		return "JavaScript coverage"
	case strings.Contains(normalized, "npm test") || strings.Contains(normalized, "pnpm test") || strings.Contains(normalized, "yarn test"):
		return "JavaScript tests"
	case strings.Contains(normalized, "cargo test"):
		return "Cargo tests"
	case strings.Contains(normalized, "go test"):
		return "Go verification"
	default:
		return "Repository verification"
	}
}

func contextTokens(issue issueRecord, ticketContexts []TicketContextRecord, vulnerabilityFindings []VulnerabilityFindingRecord) []string {
	textParts := []string{
		issue.Title,
		firstNonEmptyPtr(issue.Summary),
		firstNonEmptyPtr(issue.Impact),
		strings.Join(issue.Labels, " "),
	}
	for _, item := range ticketContexts[:min(len(ticketContexts), 4)] {
		textParts = append(textParts, item.Title, item.Summary, strings.Join(item.Labels, " "), strings.Join(item.AcceptanceCriteria, " "))
	}
	for _, item := range vulnerabilityFindings[:min(len(vulnerabilityFindings), 4)] {
		textParts = append(textParts, item.Title, item.Summary, item.Scanner, firstNonEmptyPtr(item.RuleID), strings.Join(item.CWEIDs, " "), strings.Join(item.CVEIDs, " "), firstNonEmptyPtr(item.LocationPath), strings.Join(item.Evidence, " "))
	}
	tokens := regexp.MustCompile(`[a-z0-9_./-]{3,}`).FindAllString(strings.ToLower(strings.Join(textParts, " ")), -1)
	stopWords := map[string]struct{}{
		"the": {}, "and": {}, "for": {}, "with": {}, "that": {}, "this": {}, "from": {}, "issue": {}, "bug": {}, "current": {}, "branch": {},
	}
	ordered := []string{}
	seen := map[string]struct{}{}
	for _, token := range tokens {
		if _, stop := stopWords[token]; stop {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		ordered = append(ordered, token)
		if len(ordered) >= 24 {
			break
		}
	}
	return ordered
}

func rankRelatedPathsForIssue(
	issue issueRecord,
	treeFocus []string,
	ticketContexts []TicketContextRecord,
	vulnerabilityFindings []VulnerabilityFindingRecord,
	repoMap *rustcore.RepoMapSummary,
) []string {
	candidates := append([]string{}, treeFocus...)
	if repoMap != nil {
		for _, item := range repoMap.KeyFiles {
			candidates = append(candidates, item.Path)
		}
		for _, item := range repoMap.TopDirectories {
			candidates = append(candidates, item.Path)
		}
	}
	for _, item := range vulnerabilityFindings[:min(len(vulnerabilityFindings), 6)] {
		if item.LocationPath != nil && strings.TrimSpace(*item.LocationPath) != "" {
			candidates = append(candidates, *item.LocationPath)
		}
	}
	tokens := contextTokens(issue, ticketContexts, vulnerabilityFindings)
	scored := map[string]int{}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		score := 0
		lowered := strings.ToLower(path)
		if slices.Contains(treeFocus, path) {
			score += 6
		}
		for _, token := range tokens {
			if strings.Contains(lowered, token) {
				score += 2
			}
		}
		if strings.HasSuffix(lowered, ".py") || strings.HasSuffix(lowered, ".ts") || strings.HasSuffix(lowered, ".tsx") || strings.HasSuffix(lowered, ".js") || strings.HasSuffix(lowered, ".jsx") || strings.HasSuffix(lowered, ".go") || strings.HasSuffix(lowered, ".rs") || strings.HasSuffix(lowered, ".java") {
			score++
		}
		if strings.Contains(lowered, "test") {
			score++
		}
		if score > 0 && score > scored[path] {
			scored[path] = score
		}
	}
	ordered := make([]string, 0, len(scored))
	for path := range scored {
		ordered = append(ordered, path)
	}
	slices.SortFunc(ordered, func(a, b string) int {
		if scored[a] != scored[b] {
			if scored[a] > scored[b] {
				return -1
			}
			return 1
		}
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	})
	if len(ordered) > 8 {
		ordered = ordered[:8]
	}
	return ordered
}

func listVulnerabilityFindings(dataDir string, workspaceID string, issueID string) ([]VulnerabilityFindingRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "vulnerability_findings.json")
	var items []VulnerabilityFindingRecord
	if err := readJSON(path, &items); err != nil {
		if os.IsNotExist(err) {
			return []VulnerabilityFindingRecord{}, nil
		}
		return nil, err
	}
	filtered := make([]VulnerabilityFindingRecord, 0, len(items))
	for _, item := range items {
		if issueID == "" || item.IssueID == issueID {
			filtered = append(filtered, item)
		}
	}
	slices.SortFunc(filtered, func(a, b VulnerabilityFindingRecord) int {
		if a.IssueID != b.IssueID {
			if a.IssueID < b.IssueID {
				return -1
			}
			return 1
		}
		if a.Severity != b.Severity {
			if a.Severity < b.Severity {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Title)
		right := strings.ToLower(b.Title)
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
		if a.CreatedAt < b.CreatedAt {
			return -1
		}
		if a.CreatedAt > b.CreatedAt {
			return 1
		}
		return 0
	})
	return filtered, nil
}

func bestEffortSemanticStatus(dataDir string, workspaceID string, candidatePaths []string) *SemanticIndexStatus {
	paths := []string{}
	for _, path := range dedupeText(candidatePaths) {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".py", ".ts", ".tsx", ".js", ".jsx", ".go", ".rs", ".java":
			paths = append(paths, path)
		}
		if len(paths) >= 12 {
			break
		}
	}
	status, err := ReadSemanticIndexStatus(dataDir, workspaceID, SemanticIndexRequest{
		Surface:  "cli",
		Strategy: "paths",
		Paths:    paths,
		Limit:    12,
	})
	if err != nil {
		return nil
	}
	return status
}

func collectIssueSemanticMatches(
	workspaceID string,
	root string,
	issue issueRecord,
	tokens []string,
	candidatePaths []string,
	repoMap *rustcore.RepoMapSummary,
) ([]SemanticPatternMatchRecord, []SemanticQueryMaterializationRecord, []SemanticMatchMaterializationRecord) {
	if astGrepBinary() == "" {
		return nil, nil, nil
	}
	queries := deriveIssueSemanticPatterns(tokens, candidatePaths, repoMap)
	matches := []SemanticPatternMatchRecord{}
	queryRows := []SemanticQueryMaterializationRecord{}
	matchRows := []SemanticMatchMaterializationRecord{}
	seen := map[string]struct{}{}
	issueID := issue.BugID
	for _, query := range queries {
		pattern := query.pattern
		language := optionalString(query.language)
		pathGlob := trimOptional(optionalString(query.pathGlob))
		found, binaryPath, queryError, truncated := runAstGrepSemanticQuery(root, pattern, language, pathGlob, 6)
		engine := "none"
		if binaryPath != nil {
			engine = "ast_grep"
		}
		reason := query.reason
		queryRow := buildSemanticQueryRow(workspaceID, pattern, language, pathGlob, engine, len(found), truncated, queryError, "issue_context", &reason, &issueID, nil)
		queryRows = append(queryRows, *queryRow)
		if queryError != nil {
			continue
		}
		queryMatchBuffer := []SemanticPatternMatchRecord{}
		for _, item := range found {
			key := fmt.Sprintf("%s\x00%d\x00%s", item.Path, intPtrValue(item.LineStart), item.MatchedText)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			lowered := strings.ToLower(item.Path + " " + item.MatchedText + " " + firstNonEmptyPtr(item.ContextLines))
			matchedTerms := []string{}
			for _, token := range tokens[:min(len(tokens), 12)] {
				if strings.Contains(lowered, token) {
					matchedTerms = append(matchedTerms, token)
				}
			}
			score := max(3, len(matchedTerms)*2)
			for _, candidate := range candidatePaths[:min(len(candidatePaths), 4)] {
				if item.Path == candidate {
					score += 2
					break
				}
			}
			item.Reason = &reason
			item.Score = score
			matches = append(matches, item)
			queryMatchBuffer = append(queryMatchBuffer, item)
			if len(matches) >= 8 {
				break
			}
		}
		matchRows = append(matchRows, buildSemanticMatchRows(workspaceID, queryRow.QueryRef, queryMatchBuffer)...)
		if len(matches) >= 8 {
			break
		}
	}
	sortSemanticMatches(matches)
	sortSemanticMatchRows(matchRows)
	return matches[:min(len(matches), 8)], queryRows, matchRows[:min(len(matchRows), 8)]
}

type issueSemanticPattern struct {
	pattern  string
	language string
	pathGlob string
	reason   string
}

func deriveIssueSemanticPatterns(tokens []string, candidatePaths []string, repoMap *rustcore.RepoMapSummary) []issueSemanticPattern {
	identifierTokens := []string{}
	blocked := map[string]struct{}{"export": {}, "backend": {}, "frontend": {}, "render": {}, "error": {}, "issue": {}}
	identifierPattern := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{2,}$`)
	for _, token := range tokens {
		if !identifierPattern.MatchString(token) {
			continue
		}
		if _, ok := blocked[token]; ok {
			continue
		}
		identifierTokens = append(identifierTokens, token)
	}
	if len(identifierTokens) == 0 {
		return nil
	}

	languagePaths := map[string]string{}
	for _, path := range candidatePaths[:min(len(candidatePaths), 8)] {
		if language := detectAstGrepLanguage(path); language != "" {
			if _, exists := languagePaths[language]; !exists {
				languagePaths[language] = path
			}
		}
	}
	if len(languagePaths) == 0 && repoMap != nil {
		extensionLanguages := map[string]string{
			".py":  "python",
			".ts":  "typescript",
			".tsx": "tsx",
			".js":  "javascript",
			".go":  "go",
			".rs":  "rust",
		}
		for extension := range repoMap.TopExtensions {
			if language := extensionLanguages[extension]; language != "" {
				if _, exists := languagePaths[language]; !exists {
					languagePaths[language] = "repo map extension " + extension
				}
			}
		}
	}
	languages := make([]string, 0, len(languagePaths))
	for language := range languagePaths {
		languages = append(languages, language)
	}
	slices.Sort(languages)

	out := []issueSemanticPattern{}
	seen := map[string]struct{}{}
	for _, language := range languages {
		samplePath := languagePaths[language]
		for _, name := range identifierTokens[:min(len(identifierTokens), 4)] {
			for _, template := range semanticPatternsForName(language, name) {
				key := language + "\x00" + template.pattern
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, issueSemanticPattern{
					pattern:  template.pattern,
					language: language,
					pathGlob: astGrepGlobForLanguage(language),
					reason:   template.reason + " near " + samplePath,
				})
				if len(out) >= 8 {
					return out
				}
			}
		}
	}
	return out
}

type semanticPatternTemplate struct {
	pattern string
	reason  string
}

func semanticPatternsForName(language string, name string) []semanticPatternTemplate {
	if name == "" {
		return nil
	}
	isExported := strings.ToUpper(name[:1]) == name[:1]
	switch language {
	case "python":
		if isExported {
			return []semanticPatternTemplate{{pattern: "class " + name + ": $$$BODY", reason: "Semantic class lookup for " + name}}
		}
		return []semanticPatternTemplate{{pattern: "def " + name + "($$$ARGS): $$$BODY", reason: "Semantic function lookup for " + name}}
	case "typescript", "tsx", "javascript":
		if isExported {
			return []semanticPatternTemplate{{pattern: "class " + name + " { $$$BODY }", reason: "Semantic class lookup for " + name}}
		}
		return []semanticPatternTemplate{
			{pattern: "function " + name + "($$$ARGS) { $$$BODY }", reason: "Semantic function lookup for " + name},
			{pattern: "const " + name + " = ($$$ARGS) => $$$BODY", reason: "Semantic arrow-function lookup for " + name},
		}
	case "go":
		if isExported {
			return []semanticPatternTemplate{{pattern: "type " + name + " struct { $$$BODY }", reason: "Semantic type lookup for " + name}}
		}
		return []semanticPatternTemplate{{pattern: "func " + name + "($$$ARGS) { $$$BODY }", reason: "Semantic function lookup for " + name}}
	case "rust":
		if isExported {
			return []semanticPatternTemplate{{pattern: "struct " + name + " { $$$BODY }", reason: "Semantic type lookup for " + name}}
		}
		return []semanticPatternTemplate{{pattern: "fn " + name + "($$$ARGS) { $$$BODY }", reason: "Semantic function lookup for " + name}}
	default:
		return nil
	}
}

func detectAstGrepLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js":
		return "javascript"
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	default:
		return ""
	}
}

func astGrepGlobForLanguage(language string) string {
	switch language {
	case "python":
		return "**/*.py"
	case "typescript":
		return "**/*.ts"
	case "tsx":
		return "**/*.tsx"
	case "javascript":
		return "**/*.js"
	case "go":
		return "**/*.go"
	case "rust":
		return "**/*.rs"
	default:
		return ""
	}
}

func sortSemanticMatches(items []SemanticPatternMatchRecord) {
	slices.SortFunc(items, func(a, b SemanticPatternMatchRecord) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if a.Path != b.Path {
			if a.Path < b.Path {
				return -1
			}
			return 1
		}
		if intPtrValue(a.LineStart) < intPtrValue(b.LineStart) {
			return -1
		}
		if intPtrValue(a.LineStart) > intPtrValue(b.LineStart) {
			return 1
		}
		return 0
	})
}

func sortSemanticMatchRows(items []SemanticMatchMaterializationRecord) {
	slices.SortFunc(items, func(a, b SemanticMatchMaterializationRecord) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if a.Path != b.Path {
			if a.Path < b.Path {
				return -1
			}
			return 1
		}
		if intPtrValue(a.LineStart) < intPtrValue(b.LineStart) {
			return -1
		}
		if intPtrValue(a.LineStart) > intPtrValue(b.LineStart) {
			return 1
		}
		return 0
	})
}

func buildDynamicContext(
	dataDir string,
	workspace workspaceRecord,
	issue issueRecord,
	treeFocus []string,
	ticketContexts []TicketContextRecord,
	threatModels []ThreatModelRecord,
	browserDumps []BrowserDumpRecord,
	vulnerabilityFindings []VulnerabilityFindingRecord,
	recentFixes []FixRecord,
	recentActivity []activityRecord,
	relatedPaths []string,
	repoMap *rustcore.RepoMapSummary,
) *DynamicContextBundle {
	tokens := contextTokens(issue, ticketContexts, vulnerabilityFindings)
	symbols := extractSymbolContext(dataDir, workspace.WorkspaceID, workspace.RootPath, append(append([]string{}, treeFocus...), relatedPaths...), tokens)
	semanticMatches, semanticQueries, semanticMatchRows := collectIssueSemanticMatches(workspace.WorkspaceID, workspace.RootPath, issue, tokens, append(append([]string{}, treeFocus...), relatedPaths...), repoMap)
	related := rankRelatedArtifacts(ticketContexts, threatModels, browserDumps, vulnerabilityFindings, recentFixes, recentActivity, tokens)
	if len(symbols) == 0 && len(semanticMatches) == 0 && len(semanticQueries) == 0 && len(semanticMatchRows) == 0 && len(related) == 0 {
		return nil
	}
	return &DynamicContextBundle{
		SymbolContext:     symbols,
		SemanticMatches:   semanticMatches,
		SemanticQueries:   semanticQueries,
		SemanticMatchRows: semanticMatchRows,
		RelatedContext:    related,
	}
}

func buildContextRetrievalLedger(
	issue issueRecord,
	treeFocus []string,
	evidenceBundle []evidenceRef,
	relatedPaths []string,
	guidance []RepoGuidanceRecord,
	dynamicContext *DynamicContextBundle,
	matchedPathInstructions []RepoPathInstructionMatch,
) []ContextRetrievalLedgerEntry {
	tokens := contextTokens(issue, nil, nil)
	entries := []ContextRetrievalLedgerEntry{}
	seen := map[string]struct{}{}
	focusSet := map[string]struct{}{}
	ptr := func(value string) *string {
		return &value
	}
	atLeastOne := func(value int) int {
		if value < 1 {
			return 1
		}
		return value
	}
	for _, path := range treeFocus {
		focusSet[path] = struct{}{}
	}

	matchTerms := func(parts ...string) []string {
		haystack := strings.ToLower(strings.Join(parts, " "))
		matches := []string{}
		for _, token := range tokens[:min(len(tokens), 12)] {
			if strings.Contains(haystack, token) {
				matches = append(matches, token)
			}
		}
		return matches[:min(len(matches), 4)]
	}
	push := func(entry ContextRetrievalLedgerEntry) {
		key := entry.SourceType + "\x00" + entry.SourceID
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		entries = append(entries, entry)
	}

	for idx, evidence := range evidenceBundle[:min(len(evidenceBundle), 8)] {
		path := evidence.Path
		if evidence.NormalizedPath != nil && strings.TrimSpace(*evidence.NormalizedPath) != "" {
			path = *evidence.NormalizedPath
		}
		ref := path
		if evidence.Line != nil {
			ref = fmt.Sprintf("%s:%d", path, *evidence.Line)
		}
		excerpt := ""
		if evidence.Excerpt != nil {
			excerpt = *evidence.Excerpt
		}
		push(ContextRetrievalLedgerEntry{
			EntryID:      fmt.Sprintf("evidence:%d:%s", idx, path),
			SourceType:   "evidence",
			SourceID:     ref,
			Title:        ref,
			Path:         ptr(path),
			Reason:       "Direct evidence attached to the issue.",
			MatchedTerms: matchTerms(path, excerpt),
			Score:        12 - idx,
		})
	}

	for idx, path := range relatedPaths[:min(len(relatedPaths), 8)] {
		matches := matchTerms(path)
		reason := "Ranked from repo-map paths and issue terms."
		if _, ok := focusSet[path]; ok {
			reason = "Direct evidence path."
		}
		push(ContextRetrievalLedgerEntry{
			EntryID:      "related_path:" + path,
			SourceType:   "related_path",
			SourceID:     path,
			Title:        path,
			Path:         ptr(path),
			Reason:       reason,
			MatchedTerms: matches,
			Score:        atLeastOne(10 - idx + len(matches)),
		})
	}

	if dynamicContext != nil {
		for _, symbol := range dynamicContext.SymbolContext[:min(len(dynamicContext.SymbolContext), 8)] {
			line := 0
			if symbol.LineStart != nil {
				line = *symbol.LineStart
			}
			sourceID := fmt.Sprintf("%s:%s:%d", symbol.Path, symbol.Symbol, line)
			reason := "Ranked from symbol names near issue-related files."
			if symbol.Reason != nil && strings.TrimSpace(*symbol.Reason) != "" {
				reason = *symbol.Reason
			}
			scope := ""
			if symbol.EnclosingScope != nil {
				scope = *symbol.EnclosingScope
			}
			sourceType := "on_demand_symbol"
			if symbol.EvidenceSource == "stored_semantic" {
				sourceType = "stored_symbol"
			}
			push(ContextRetrievalLedgerEntry{
				EntryID:      "symbol:" + sourceID,
				SourceType:   sourceType,
				SourceID:     sourceID,
				Title:        symbol.Kind + " " + symbol.Symbol,
				Path:         ptr(symbol.Path),
				Reason:       reason,
				MatchedTerms: matchTerms(symbol.Path, symbol.Symbol, scope),
				Score:        symbol.Score,
			})
		}
		for _, match := range dynamicContext.SemanticMatches[:min(len(dynamicContext.SemanticMatches), 8)] {
			lineStart := intPtrValue(match.LineStart)
			columnStart := intPtrValue(match.ColumnStart)
			sum := sha1.Sum([]byte(match.MatchedText))
			sourceID := fmt.Sprintf("%s:%d:%d:%s", match.Path, lineStart, columnStart, hex.EncodeToString(sum[:])[:10])
			reason := "Matched a semantic pattern derived from issue terms."
			if match.Reason != nil && strings.TrimSpace(*match.Reason) != "" {
				reason = *match.Reason
			}
			title := match.MatchedText
			if len(title) > 72 {
				title = title[:72] + "..."
			}
			push(ContextRetrievalLedgerEntry{
				EntryID:      "semantic_match:" + sourceID,
				SourceType:   "semantic_match",
				SourceID:     sourceID,
				Title:        title,
				Path:         ptr(match.Path),
				Reason:       reason,
				MatchedTerms: matchTerms(match.Path, match.MatchedText, firstNonEmptyPtr(match.ContextLines)),
				Score:        match.Score,
			})
		}
		for _, artifact := range dynamicContext.RelatedContext[:min(len(dynamicContext.RelatedContext), 8)] {
			reason := "Ranked from related operational artifacts."
			if artifact.Reason != nil && strings.TrimSpace(*artifact.Reason) != "" {
				reason = *artifact.Reason
			}
			sourceID := artifact.ArtifactType + ":" + artifact.ArtifactID
			push(ContextRetrievalLedgerEntry{
				EntryID:      "artifact:" + sourceID,
				SourceType:   "artifact",
				SourceID:     sourceID,
				Title:        artifact.Title,
				Path:         artifact.Path,
				Reason:       reason,
				MatchedTerms: artifact.MatchedTerms,
				Score:        artifact.Score,
			})
		}
	}

	for idx, item := range guidance[:min(len(guidance), replayGuidanceLimit)] {
		push(ContextRetrievalLedgerEntry{
			EntryID:      "guidance:" + item.Path,
			SourceType:   "guidance",
			SourceID:     item.Path,
			Title:        firstNonEmpty(item.Title, item.Path),
			Path:         ptr(item.Path),
			Reason:       "Repository guidance selected for the issue prompt.",
			MatchedTerms: matchTerms(item.Path, item.Title, item.Summary),
			Score:        atLeastOne(6 - idx),
		})
	}

	for idx, item := range matchedPathInstructions[:min(len(matchedPathInstructions), 6)] {
		title := item.Path
		if item.Title != nil && strings.TrimSpace(*item.Title) != "" {
			title = *item.Title
		}
		push(ContextRetrievalLedgerEntry{
			EntryID:      "path_instruction:" + item.InstructionID,
			SourceType:   "path_instruction",
			SourceID:     item.InstructionID,
			Title:        title,
			Path:         ptr(item.SourcePath),
			Reason:       "Path-specific instruction matched ranked issue files.",
			MatchedTerms: matchTerms(item.Path, strings.Join(item.MatchedPaths, " "), item.Instructions),
			Score:        atLeastOne(8 - idx),
		})
	}

	slices.SortFunc(entries, func(a, b ContextRetrievalLedgerEntry) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if a.SourceType != b.SourceType {
			if a.SourceType < b.SourceType {
				return -1
			}
			return 1
		}
		if strings.ToLower(a.Title) < strings.ToLower(b.Title) {
			return -1
		}
		if strings.ToLower(a.Title) > strings.ToLower(b.Title) {
			return 1
		}
		return 0
	})
	return entries[:min(len(entries), 32)]
}

func extractSymbolContext(dataDir string, workspaceID string, root string, candidatePaths []string, tokens []string) []RepoMapSymbolRecord {
	results := []RepoMapSymbolRecord{}
	seen := map[string]struct{}{}
	deduped := dedupeText(candidatePaths)
	for _, relPath := range deduped[:min(len(deduped), 10)] {
		absPath := filepath.Join(root, relPath)
		info, err := os.Stat(absPath)
		if err != nil || info.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(relPath))
		if !slices.Contains([]string{".py", ".ts", ".tsx", ".js", ".jsx", ".go", ".rs", ".java"}, ext) {
			continue
		}
		pathSymbols, err := ReadPathSymbols(dataDir, workspaceID, relPath)
		if err != nil {
			continue
		}
		for _, record := range pathSymbols.Symbols[:min(len(pathSymbols.Symbols), 20)] {
			key := fmt.Sprintf("%s::%s::%d::%s", record.Path, record.Symbol, intPtrValue(record.LineStart), record.Kind)
			if _, ok := seen[key]; ok {
				continue
			}
			lowered := strings.ToLower(record.Path + " " + record.Symbol + " " + firstNonEmptyPtr(record.EnclosingScope))
			score := 0
			matches := []string{}
			for _, token := range tokens[:min(len(tokens), 12)] {
				if strings.Contains(lowered, token) {
					score += 2
					matches = append(matches, token)
				}
			}
			for _, focusPath := range candidatePaths[:min(len(candidatePaths), 4)] {
				if relPath == focusPath {
					score += 2
					break
				}
			}
			if score <= 0 {
				continue
			}
			seen[key] = struct{}{}
			var reason *string
			if len(matches) > 0 {
				value := "Matches " + strings.Join(matches[:min(len(matches), 3)], ", ") + "; " + strings.ToLower(pathSymbols.SelectionReason)
				reason = &value
			} else {
				value := pathSymbols.SelectionReason
				reason = &value
			}
			results = append(results, RepoMapSymbolRecord{
				Path:           record.Path,
				Symbol:         record.Symbol,
				Kind:           record.Kind,
				LineStart:      record.LineStart,
				LineEnd:        record.LineEnd,
				EnclosingScope: record.EnclosingScope,
				Reason:         reason,
				EvidenceSource: pathSymbols.EvidenceSource,
				Score:          score,
			})
		}
	}
	slices.SortFunc(results, func(a, b RepoMapSymbolRecord) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if a.Path != b.Path {
			if a.Path < b.Path {
				return -1
			}
			return 1
		}
		if a.Symbol < b.Symbol {
			return -1
		}
		if a.Symbol > b.Symbol {
			return 1
		}
		return 0
	})
	return results[:min(len(results), 8)]
}

func intPtrValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func rankRelatedArtifacts(
	ticketContexts []TicketContextRecord,
	threatModels []ThreatModelRecord,
	browserDumps []BrowserDumpRecord,
	vulnerabilityFindings []VulnerabilityFindingRecord,
	recentFixes []FixRecord,
	recentActivity []activityRecord,
	tokens []string,
) []RelatedContextRecord {
	termSet := map[string]struct{}{}
	for _, token := range tokens[:min(len(tokens), 12)] {
		termSet[token] = struct{}{}
	}
	matchTerms := func(parts ...string) []string {
		haystack := strings.ToLower(strings.Join(parts, " "))
		matches := []string{}
		for token := range termSet {
			if strings.Contains(haystack, token) {
				matches = append(matches, token)
			}
		}
		slices.Sort(matches)
		return matches[:min(len(matches), 4)]
	}
	records := []RelatedContextRecord{}
	for _, item := range ticketContexts[:min(len(ticketContexts), 4)] {
		matches := matchTerms(item.Title, item.Summary, strings.Join(item.AcceptanceCriteria, " "))
		if len(matches) == 0 {
			continue
		}
		reason := "Shares acceptance-criteria or ticket language with the current issue."
		path := "ticket_contexts.json"
		records = append(records, RelatedContextRecord{ArtifactType: "ticket_context", ArtifactID: item.ContextID, Title: item.Title, Path: &path, Reason: &reason, MatchedTerms: matches, Score: len(matches)*2 + 2})
	}
	for _, item := range threatModels[:min(len(threatModels), 3)] {
		matches := matchTerms(item.Title, item.Summary, strings.Join(item.Assets, " "), strings.Join(item.AbuseCases, " "))
		if len(matches) == 0 {
			continue
		}
		reason := "Touches the same assets or abuse language as the issue context."
		path := "threat_models.json"
		records = append(records, RelatedContextRecord{ArtifactType: "threat_model", ArtifactID: item.ThreatModelID, Title: item.Title, Path: &path, Reason: &reason, MatchedTerms: matches, Score: len(matches)*2 + 1})
	}
	for _, item := range browserDumps[:min(len(browserDumps), 3)] {
		pageURL := ""
		if item.PageURL != nil {
			pageURL = *item.PageURL
		}
		pageTitle := ""
		if item.PageTitle != nil {
			pageTitle = *item.PageTitle
		}
		matches := matchTerms(item.Label, item.Summary, pageTitle, pageURL, item.DOMSnapshot)
		if len(matches) == 0 {
			continue
		}
		reason := "Captures a browser-state repro that overlaps with the current issue terms."
		path := "browser_dumps.json"
		records = append(records, RelatedContextRecord{ArtifactType: "browser_dump", ArtifactID: item.DumpID, Title: item.Label, Path: &path, Reason: &reason, MatchedTerms: matches, Score: len(matches)*2 + 1})
	}
	for _, item := range vulnerabilityFindings[:min(len(vulnerabilityFindings), 4)] {
		matches := matchTerms(item.Title, item.Summary, item.Scanner, firstNonEmptyPtr(item.RuleID), firstNonEmptyPtr(item.LocationPath), strings.Join(item.CWEIDs, " "), strings.Join(item.CVEIDs, " "), strings.Join(item.Evidence, " "))
		if len(matches) == 0 {
			continue
		}
		reason := "Scanner evidence overlaps with the current issue language or files."
		path := "vulnerability_findings.json"
		records = append(records, RelatedContextRecord{ArtifactType: "vulnerability_finding", ArtifactID: item.FindingID, Title: item.Title, Path: &path, Reason: &reason, MatchedTerms: matches, Score: len(matches)*2 + 2})
	}
	for _, item := range recentFixes[:min(len(recentFixes), 4)] {
		matches := matchTerms(item.Summary, strings.Join(item.ChangedFiles, " "))
		if len(matches) == 0 {
			continue
		}
		reason := "Previous fix history overlaps with this issue's files or terms."
		path := "fix_records.json"
		records = append(records, RelatedContextRecord{ArtifactType: "fix_record", ArtifactID: item.FixID, Title: item.Summary, Path: &path, Reason: &reason, MatchedTerms: matches, Score: len(matches)*2 + 1})
	}
	for _, item := range recentActivity[:min(len(recentActivity), 4)] {
		matches := matchTerms(item.Summary)
		if len(matches) == 0 {
			continue
		}
		reason := "Recent issue activity mentions the same issue language."
		path := "activity.jsonl"
		records = append(records, RelatedContextRecord{ArtifactType: "activity", ArtifactID: item.ActivityID, Title: item.Summary, Path: &path, Reason: &reason, MatchedTerms: matches, Score: len(matches) * 2})
	}
	slices.SortFunc(records, func(a, b RelatedContextRecord) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if a.ArtifactType != b.ArtifactType {
			if a.ArtifactType < b.ArtifactType {
				return -1
			}
			return 1
		}
		if strings.ToLower(a.Title) < strings.ToLower(b.Title) {
			return -1
		}
		if strings.ToLower(a.Title) > strings.ToLower(b.Title) {
			return 1
		}
		return 0
	})
	return records[:min(len(records), 8)]
}

func buildIssueContextPrompt(
	workspace workspaceRecord,
	issue issueRecord,
	treeFocus []string,
	recentFixes []FixRecord,
	recentActivity []activityRecord,
	guidance []RepoGuidanceRecord,
	verificationProfiles []rustcore.VerificationProfileInput,
	ticketContexts []TicketContextRecord,
	threatModels []ThreatModelRecord,
	browserDumps []BrowserDumpRecord,
	vulnerabilityFindings []VulnerabilityFindingRecord,
	relatedPaths []string,
	repoMap *rustcore.RepoMapSummary,
	dynamicContext *DynamicContextBundle,
	semanticStatus *SemanticIndexStatus,
	retrievalLedger []ContextRetrievalLedgerEntry,
	repoConfig *RepoConfigRecord,
	matchedPathInstructions []RepoPathInstructionMatch,
) string {
	evidence := append(append([]evidenceRef{}, issue.Evidence[:min(len(issue.Evidence), 8)]...), issue.VerificationEvidence[:min(len(issue.VerificationEvidence), 8)]...)
	threatTitles := map[string]string{}
	for _, item := range threatModels {
		threatTitles[item.ThreatModelID] = item.Title
	}
	var topDirs []rustcore.RepoMapDirectoryRecord
	if repoMap != nil {
		topDirs = repoMap.TopDirectories
	}
	dynamic := DynamicContextBundle{}
	if dynamicContext != nil {
		dynamic = *dynamicContext
	}
	identity := func(s string) string { return s }
	sections := []struct {
		title string
		lines []string
	}{
		{"Evidence references", bullets(evidence, len(evidence), "None listed.", evidenceRefLine)},
		{"Recent issue history", bullets(recentActivity, 6, "No recent issue history.", historyLine)},
		{"Prior fix history", bullets(recentFixes, 4, "No prior fixes recorded.", fixLine)},
		{"Ticket context", bullets(ticketContexts, 4, "No linked ticket context recorded.", ticketLine)},
		{"Threat model", bullets(threatModels, 3, "No threat model recorded yet.", threatLine)},
		{"Vulnerability findings", bullets(vulnerabilityFindings, 4, "No vulnerability findings recorded yet.", func(item VulnerabilityFindingRecord) string {
			return vulnerabilityLine(item, threatTitles)
		})},
		{"Browser context", bullets(browserDumps, 3, "No browser dumps recorded yet.", browserLine)},
		{"Repo config", repoConfigLines(repoConfig)},
		{"Path-specific guidance", bullets(matchedPathInstructions, 6, "No path-specific instructions matched the current issue paths.", pathInstructionLine)},
		{"Semantic freshness", semanticStatusLines(semanticStatus)},
		{"Structural context", bullets(topDirs, 5, "No repo map available.", func(item rustcore.RepoMapDirectoryRecord) string {
			return item.Path + ": " + toText(item.SourceFileCount) + " source files, " + toText(item.TestFileCount) + " test files"
		})},
		{"Ranked related paths", bullets(relatedPaths, 8, "No related paths ranked yet.", identity)},
		{"Symbol context", bullets(dynamic.SymbolContext, 6, "No symbol context ranked yet.", symbolLine)},
		{"Semantic matches", bullets(dynamic.SemanticMatches, 6, "No semantic matches ranked yet.", semanticMatchLine)},
		{"Related artifacts", bullets(dynamic.RelatedContext, 6, "No related artifacts ranked yet.", relatedArtifactLine)},
		{"Retrieval ledger", bullets(retrievalLedger, 10, "No retrieval ledger entries recorded yet.", retrievalLine)},
		{"Repository guidance", bullets(guidance, replayGuidanceLimit, "No repository guidance files were found.", guidanceLine)},
		{"Known verification profiles", bullets(verificationProfiles, 4, "No verification profiles configured yet.", verificationLine)},
		{"Priority files", bullets(treeFocus, 12, "Inspect the workspace tree around the bug.", identity)},
	}

	var b strings.Builder
	b.WriteString("You are fixing bug " + issue.BugID + " in workspace " + workspace.RootPath + ".\n" +
		"Title: " + issue.Title + "\n" +
		"Severity: " + issue.Severity + "\n" +
		"Doc status: " + issue.DocStatus + "\n" +
		"Code status: " + issue.CodeStatus + "\n" +
		"Tracker source: " + issue.Source + "\n" +
		"Summary: " + fallbackString(firstNonEmptyPtr(issue.Summary), "No summary supplied.") + "\n" +
		"Impact: " + fallbackString(firstNonEmptyPtr(issue.Impact), "No impact supplied.") + "\n\n")
	for _, sec := range sections {
		b.WriteString(sec.title + ":\n" + strings.Join(sec.lines, "\n") + "\n\n")
	}
	b.WriteString("Required workflow:\n" +
		"1. Reproduce or validate the bug against the current code.\n" +
		"2. Make the minimal safe fix.\n" +
		"3. Add or update tests.\n" +
		"4. Record exact files changed, tests run, and how the fix works back into the tracker.\n" +
		"Return a concise engineering result, not a conversation.")
	return b.String()
}

// bullets renders the first limit items as "- " lines, or the fallback line when
// there are none.
func bullets[T any](items []T, limit int, fallback string, line func(T) string) []string {
	items = items[:min(len(items), limit)]
	if len(items) == 0 {
		return []string{"- " + fallback}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, "- "+line(item))
	}
	return out
}

// present is *value when it holds more than whitespace, else "".
func present(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return ""
	}
	return *value
}

// joinFirst joins the first n items.
func joinFirst(items []string, n int, sep string) string {
	return strings.Join(items[:min(len(items), n)], sep)
}

// joinFirstOr joins the first n items, or returns empty when there are none.
func joinFirstOr(items []string, n int, sep, empty string) string {
	if len(items) == 0 {
		return empty
	}
	return joinFirst(items, n, sep)
}

func evidenceRefLine(evidence evidenceRef) string {
	if evidence.Line != nil {
		return fmt.Sprintf("%s:%d", evidence.Path, *evidence.Line)
	}
	return evidence.Path
}

func historyLine(entry activityRecord) string {
	beforeAfter, ok := entry.Details["before_after"].(map[string]any)
	if !ok || len(beforeAfter) == 0 {
		return entry.CreatedAt + ": " + entry.Summary
	}
	fragments := make([]string, 0, len(beforeAfter))
	for _, field := range slices.Sorted(maps.Keys(beforeAfter)) {
		if diff, ok := beforeAfter[field].(map[string]any); ok {
			fragments = append(fragments, field+" "+toText(diff["from"])+" -> "+toText(diff["to"]))
		} else {
			fragments = append(fragments, field+" "+toText(beforeAfter[field]))
		}
	}
	return entry.CreatedAt + ": " + strings.Join(fragments, ", ")
}

func fixLine(fix FixRecord) string {
	actorLabel := fallbackString(fix.Actor.Label, fix.Actor.Name)
	changed := joinFirstOr(fix.ChangedFiles, 4, ", ", "no files recorded")
	return fix.FixID + " [" + fix.Status + "] by " + actorLabel + ": " + fix.Summary + " (" + changed + ")"
}

func guidanceLine(item RepoGuidanceRecord) string {
	mode := "optional"
	if item.AlwaysOn {
		mode = "always-on"
	}
	return item.Path + " [" + item.Kind + ", " + mode + "]: " + fallbackString(item.Summary, item.Title)
}

func verificationLine(profile rustcore.VerificationProfileInput) string {
	var coverageBits []string
	if command := present(profile.CoverageCommand); command != "" {
		coverageBits = append(coverageBits, "coverage command: "+command)
	}
	if report := present(profile.CoverageReportPath); report != "" {
		coverageBits = append(coverageBits, "report: "+report)
	}
	if profile.CoverageFormat != "" && profile.CoverageFormat != "unknown" {
		coverageBits = append(coverageBits, "format: "+profile.CoverageFormat)
	}
	coverageSummary := ""
	if len(coverageBits) > 0 {
		coverageSummary = " (" + strings.Join(coverageBits, "; ") + ")"
	}
	return profile.Name + ": " + profile.TestCommand + coverageSummary
}

// orNoSummary is summary, or "No summary." when it is blank.
func orNoSummary(summary string) string {
	if strings.TrimSpace(summary) == "" {
		return "No summary."
	}
	return summary
}

func ticketLine(item TicketContextRecord) string {
	headerBits := []string{item.Provider}
	for _, bit := range []string{present(item.ExternalID), present(item.Status)} {
		if bit != "" {
			headerBits = append(headerBits, bit)
		}
	}
	criteria := joinFirstOr(item.AcceptanceCriteria, 3, "; ", "No acceptance criteria recorded.")
	return item.Title + " [" + strings.Join(headerBits, " / ") + "]: " + orNoSummary(item.Summary) + " Acceptance criteria: " + criteria
}

func threatLine(item ThreatModelRecord) string {
	assets := joinFirstOr(item.Assets, 3, ", ", "No assets listed.")
	abuseCases := joinFirstOr(item.AbuseCases, 2, "; ", "No abuse cases listed.")
	mitigations := joinFirstOr(item.Mitigations, 2, "; ", "No mitigations listed.")
	return item.Title + " [" + item.Methodology + " / " + item.Status + "]: " + orNoSummary(item.Summary) +
		" Assets: " + assets + ". Abuse cases: " + abuseCases + ". Mitigations: " + mitigations
}

func vulnerabilityLine(item VulnerabilityFindingRecord, threatTitles map[string]string) string {
	location := fallbackString(present(item.LocationPath), "No file location recorded.")
	if item.LocationLine != nil {
		location += fmt.Sprintf(":%d", *item.LocationLine)
	}
	var ruleBits []string
	if ruleID := present(item.RuleID); ruleID != "" {
		ruleBits = append(ruleBits, ruleID)
	}
	if len(item.CWEIDs) > 0 {
		ruleBits = append(ruleBits, joinFirst(item.CWEIDs, 3, ", "))
	}
	if len(item.CVEIDs) > 0 {
		ruleBits = append(ruleBits, joinFirst(item.CVEIDs, 2, ", "))
	}
	ruleSummary := joinFirstOr(ruleBits, len(ruleBits), " | ", "No rule or taxonomy ids recorded.")
	evidenceSummary := joinFirstOr(item.Evidence, 2, "; ", "No scanner evidence recorded.")
	var linked []string
	for _, threatModelID := range item.ThreatModelIDs {
		if title, ok := threatTitles[threatModelID]; ok {
			linked = append(linked, title)
		}
	}
	linkageSummary := ""
	if len(linked) > 0 {
		linkageSummary = " Linked threat models: " + strings.Join(linked, ", ") + "."
	}
	return item.Title + " [" + item.Scanner + " / " + item.Source + " / " + item.Severity + " / " + item.Status + "]: " + orNoSummary(item.Summary) +
		" Location: " + location + ". IDs: " + ruleSummary + ". Evidence: " + evidenceSummary + "." + linkageSummary
}

func browserLine(item BrowserDumpRecord) string {
	var pageBits []string
	for _, bit := range []string{present(item.PageTitle), present(item.PageURL)} {
		if bit != "" {
			pageBits = append(pageBits, bit)
		}
	}
	pageSummary := joinFirstOr(pageBits, len(pageBits), " — ", "No page metadata recorded.")
	consoleExcerpt := joinFirstOr(item.ConsoleMessages, 2, "; ", "No console messages recorded.")
	networkExcerpt := joinFirstOr(item.NetworkRequests, 2, "; ", "No network requests recorded.")
	domExcerpt := "No DOM snapshot recorded."
	if strings.TrimSpace(item.DOMSnapshot) != "" {
		domExcerpt = strings.TrimSpace(strings.ReplaceAll(item.DOMSnapshot[:min(len(item.DOMSnapshot), 220)], "\n", " "))
	}
	summary := item.Summary
	if strings.TrimSpace(summary) == "" {
		summary = pageSummary
	}
	return item.Label + " [" + item.Source + "]: " + summary + ". Page: " + pageSummary + ". Console: " + consoleExcerpt +
		". Network: " + networkExcerpt + ". DOM: " + domExcerpt
}

func repoConfigLines(repoConfig *RepoConfigRecord) []string {
	var lines []string
	if repoConfig != nil {
		if strings.TrimSpace(repoConfig.Description) != "" {
			lines = append(lines, "- Description: "+repoConfig.Description)
		}
		if len(repoConfig.CodeGuidelines) > 0 {
			lines = append(lines, "- Code guidelines: "+joinFirst(repoConfig.CodeGuidelines, 6, ", "))
		}
		if len(repoConfig.PathFilters) > 0 {
			lines = append(lines, "- Path filters: "+joinFirst(repoConfig.PathFilters, 6, ", "))
		}
		for _, item := range repoConfig.MCPServers[:min(len(repoConfig.MCPServers), 3)] {
			lines = append(lines, "- MCP "+item.Name+": "+firstNonEmptyRepoConfig(item.Description, item.Usage, "Configured MCP context source."))
		}
	}
	if len(lines) == 0 {
		return []string{"- No .xmustard config loaded."}
	}
	return lines
}

func semanticStatusLines(status *SemanticIndexStatus) []string {
	if status == nil {
		return []string{"- No semantic index freshness status available."}
	}
	lines := []string{"- status: " + status.Status}
	for _, reason := range status.StaleReasons[:min(len(status.StaleReasons), 3)] {
		lines = append(lines, "- reason: "+reason)
	}
	for _, warning := range status.Warnings[:min(len(status.Warnings), 3)] {
		lines = append(lines, "- warning: "+warning)
	}
	return lines
}

// atLine is path, or path:line when a line is known.
func atLine(path string, line *int) string {
	if line != nil {
		return fmt.Sprintf("%s:%d", path, *line)
	}
	return path
}

// wrapPresent is prefix+value+suffix when value holds more than whitespace, else "".
func wrapPresent(prefix string, value *string, suffix string) string {
	if v := present(value); v != "" {
		return prefix + v + suffix
	}
	return ""
}

func symbolLine(item RepoMapSymbolRecord) string {
	return item.Kind + " " + item.Symbol + wrapPresent(" in ", item.EnclosingScope, "") + " @ " + atLine(item.Path, item.LineStart) + wrapPresent(" (", item.Reason, ")")
}

func semanticMatchLine(item SemanticPatternMatchRecord) string {
	language := fallbackString(present(item.Language), "unknown")
	matchedText := item.MatchedText
	if len(matchedText) > 120 {
		matchedText = matchedText[:120]
	}
	return atLine(item.Path, item.LineStart) + " [" + language + "]" + wrapPresent(" (", item.Reason, ")") + ": " + matchedText
}

func relatedArtifactLine(item RelatedContextRecord) string {
	matched := ""
	if len(item.MatchedTerms) > 0 {
		matched = " matches " + joinFirst(item.MatchedTerms, 3, ", ")
	}
	reason := ""
	if r := present(item.Reason); r != "" {
		reason = " " + strings.TrimSpace(r)
	}
	line := item.ArtifactType + " " + item.Title + wrapPresent(" [", item.Path, "]") + ":" + reason + matched
	return strings.TrimRight(line, ":")
}

func retrievalLine(item ContextRetrievalLedgerEntry) string {
	matched := ""
	if len(item.MatchedTerms) > 0 {
		matched = " matches " + joinFirst(item.MatchedTerms, 3, ", ")
	}
	return fmt.Sprintf("%s %s%s: %s%s (score %d)", item.SourceType, item.Title, wrapPresent(" [", item.Path, "]"), item.Reason, matched, item.Score)
}

func pathInstructionLine(item RepoPathInstructionMatch) string {
	label := fallbackString(present(item.Title), item.Path)
	return label + " [" + joinFirst(item.MatchedPaths, 4, ", ") + "]: " + item.Instructions
}

func readWorktreeStatus(root string) *WorktreeStatus {
	result := &WorktreeStatus{
		Available:  false,
		IsGitRepo:  false,
		DirtyPaths: []string{},
	}
	output, err := exec.Command("git", "-C", root, "status", "--branch", "--porcelain=v2").CombinedOutput()
	if err != nil {
		if _, lookErr := exec.LookPath("git"); lookErr != nil {
			return result
		}
		noteGitSpawn()
		result.Available = true
		return result
	}

	noteGitSpawn()
	result.Available = true
	result.IsGitRepo = true
	for _, line := range strings.Split(string(output), "\n") {
		result.addPorcelainV2Line(line)
	}
	if len(result.DirtyPaths) > 20 {
		result.DirtyPaths = result.DirtyPaths[:20]
	}
	return result
}

// addPorcelainV2Line folds one line of `git status --branch --porcelain=v2` into the
// status, by its record type: a branch header, an untracked path, or a changed entry
// (ordinary, renamed or unmerged). Ignored ("!") and unknown lines are skipped.
func (w *WorktreeStatus) addPorcelainV2Line(line string) {
	kind, rest, ok := strings.Cut(line, " ")
	if !ok {
		return
	}
	switch kind {
	case "#":
		w.addBranchHeader(rest)
	case "?":
		w.UntrackedFiles++
		w.DirtyFiles++
		w.DirtyPaths = append(w.DirtyPaths, strings.TrimSpace(rest))
	case "1", "2", "u":
		parts := strings.Fields(line)
		if len(parts) < 3 {
			return
		}
		xy := parts[1]
		w.DirtyFiles++
		w.DirtyPaths = append(w.DirtyPaths, parts[len(parts)-1])
		if len(xy) > 0 && xy[0] != '.' {
			w.StagedFiles++
		}
	}
}

// addBranchHeader reads a "# branch.<key> <value>" header.
func (w *WorktreeStatus) addBranchHeader(header string) {
	key, value, ok := strings.Cut(header, " ")
	if !ok {
		return
	}
	value = strings.TrimSpace(value)
	switch key {
	case "branch.head":
		if value != "" {
			w.Branch = &value
		}
	case "branch.oid":
		if value != "" && value != "(initial)" {
			w.HeadSHA = &value
		}
	case "branch.ab":
		for _, part := range strings.Fields(value) {
			switch {
			case strings.HasPrefix(part, "+"):
				w.Ahead = atoiSafe(strings.TrimPrefix(part, "+"))
			case strings.HasPrefix(part, "-"):
				w.Behind = atoiSafe(strings.TrimPrefix(part, "-"))
			}
		}
	}
}

func atoiSafe(value string) int {
	total := 0
	for _, r := range strings.TrimSpace(value) {
		if r < '0' || r > '9' {
			return 0
		}
		total = total*10 + int(r-'0')
	}
	return total
}

func toText(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

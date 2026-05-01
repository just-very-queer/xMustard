package workspaceops

import (
	"fmt"
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
	SymbolContext  []RepoMapSymbolRecord  `json:"symbol_context"`
	RelatedContext []RelatedContextRecord `json:"related_context"`
}

type IssueContextPacket struct {
	Issue                         issueRecord                         `json:"issue"`
	Workspace                     workspaceRecord                     `json:"workspace"`
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
	repoConfig, err := ReadWorkspaceRepoConfig(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	repoMap, err := loadOrBuildRepoMap(dataDir, workspaceID, snapshot.Workspace.RootPath)
	if err != nil {
		return nil, err
	}

	relatedPaths := rankRelatedPathsForIssue(*issue, treeFocus, ticketContexts, repoMap)
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
	dynamicContext := buildDynamicContext(snapshot.Workspace, *issue, treeFocus, ticketContexts, threatModels, browserDumps, recentFixes, recentActivity, relatedPaths)
	retrievalLedger := buildContextRetrievalLedger(*issue, treeFocus, evidenceBundle, relatedPaths, guidance, dynamicContext, matchedPathInstructions)

	packet := &IssueContextPacket{
		Issue:                         *issue,
		Workspace:                     snapshot.Workspace,
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
		packet.RelatedPaths,
		packet.RepoMap,
		packet.DynamicContext,
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
		candidatePath := filepath.Join(workspace.RootPath, item.Path)
		content, err := os.ReadFile(candidatePath)
		if err != nil {
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

func contextTokens(issue issueRecord, ticketContexts []TicketContextRecord) []string {
	textParts := []string{
		issue.Title,
		firstNonEmptyPtr(issue.Summary),
		firstNonEmptyPtr(issue.Impact),
		strings.Join(issue.Labels, " "),
	}
	for _, item := range ticketContexts[:min(len(ticketContexts), 4)] {
		textParts = append(textParts, item.Title, item.Summary, strings.Join(item.Labels, " "), strings.Join(item.AcceptanceCriteria, " "))
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
	tokens := contextTokens(issue, ticketContexts)
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

func buildDynamicContext(
	workspace workspaceRecord,
	issue issueRecord,
	treeFocus []string,
	ticketContexts []TicketContextRecord,
	threatModels []ThreatModelRecord,
	browserDumps []BrowserDumpRecord,
	recentFixes []FixRecord,
	recentActivity []activityRecord,
	relatedPaths []string,
) *DynamicContextBundle {
	tokens := contextTokens(issue, ticketContexts)
	symbols := extractSymbolContext(workspace.RootPath, append(append([]string{}, treeFocus...), relatedPaths...), tokens)
	related := rankRelatedArtifacts(ticketContexts, threatModels, browserDumps, recentFixes, recentActivity, tokens)
	if len(symbols) == 0 && len(related) == 0 {
		return nil
	}
	return &DynamicContextBundle{
		SymbolContext:  symbols,
		RelatedContext: related,
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
	tokens := contextTokens(issue, nil)
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
			Path:         stringPtr(path),
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
			push(ContextRetrievalLedgerEntry{
				EntryID:      "symbol:" + sourceID,
				SourceType:   "symbol",
				SourceID:     sourceID,
				Title:        symbol.Kind + " " + symbol.Symbol,
				Path:         ptr(symbol.Path),
				Reason:       reason,
				MatchedTerms: matchTerms(symbol.Path, symbol.Symbol, scope),
				Score:        symbol.Score,
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

func extractSymbolContext(root string, candidatePaths []string, tokens []string) []RepoMapSymbolRecord {
	results := []RepoMapSymbolRecord{}
	seen := map[string]struct{}{}
	deduped := dedupeText(candidatePaths)
	classRe := regexp.MustCompile(`^class\s+([A-Za-z_][A-Za-z0-9_]*)`)
	defRe := regexp.MustCompile(`^def\s+([A-Za-z_][A-Za-z0-9_]*)`)
	goFuncRe := regexp.MustCompile(`^func\s+(?:\([^)]+\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`)
	rustFnRe := regexp.MustCompile(`^fn\s+([A-Za-z_][A-Za-z0-9_]*)`)
	jsFuncRe := regexp.MustCompile(`^(?:export\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)`)
	varFuncRe := regexp.MustCompile(`^(?:const|let|var)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:async\s*)?\(`)
	structRe := regexp.MustCompile(`^struct\s+([A-Za-z_][A-Za-z0-9_]*)`)
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
		content, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		var currentScope *string
		for index, raw := range lines {
			line := strings.TrimSpace(raw)
			kind := ""
			symbol := ""
			switch {
			case classRe.MatchString(line):
				symbol = classRe.FindStringSubmatch(line)[1]
				kind = "class"
				value := symbol
				currentScope = &value
			case defRe.MatchString(line):
				symbol = defRe.FindStringSubmatch(line)[1]
				kind = "function"
				if currentScope != nil {
					kind = "method"
				}
			case goFuncRe.MatchString(line):
				symbol = goFuncRe.FindStringSubmatch(line)[1]
				kind = "function"
			case rustFnRe.MatchString(line):
				symbol = rustFnRe.FindStringSubmatch(line)[1]
				kind = "function"
			case jsFuncRe.MatchString(line):
				symbol = jsFuncRe.FindStringSubmatch(line)[1]
				kind = "function"
			case varFuncRe.MatchString(line):
				symbol = varFuncRe.FindStringSubmatch(line)[1]
				kind = "function"
			case structRe.MatchString(line):
				symbol = structRe.FindStringSubmatch(line)[1]
				kind = "type"
			}
			if symbol == "" || kind == "" {
				continue
			}
			key := relPath + "::" + symbol
			if _, ok := seen[key]; ok {
				continue
			}
			lowered := strings.ToLower(relPath + " " + symbol)
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
			lineNumber := index + 1
			var reason *string
			if len(matches) > 0 {
				value := "Matches " + strings.Join(matches[:min(len(matches), 3)], ", ")
				reason = &value
			} else {
				value := "Near ranked focus files."
				reason = &value
			}
			results = append(results, RepoMapSymbolRecord{
				Path:           relPath,
				Symbol:         symbol,
				Kind:           kind,
				LineStart:      &lineNumber,
				EnclosingScope: currentScope,
				Reason:         reason,
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

func rankRelatedArtifacts(
	ticketContexts []TicketContextRecord,
	threatModels []ThreatModelRecord,
	browserDumps []BrowserDumpRecord,
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
	relatedPaths []string,
	repoMap *rustcore.RepoMapSummary,
	dynamicContext *DynamicContextBundle,
	retrievalLedger []ContextRetrievalLedgerEntry,
	repoConfig *RepoConfigRecord,
	matchedPathInstructions []RepoPathInstructionMatch,
) string {
	evidenceLines := make([]string, 0, min(len(issue.Evidence)+len(issue.VerificationEvidence), 16))
	for _, evidence := range append(append([]evidenceRef{}, issue.Evidence[:min(len(issue.Evidence), 8)]...), issue.VerificationEvidence[:min(len(issue.VerificationEvidence), 8)]...) {
		ref := evidence.Path
		if evidence.Line != nil {
			ref += fmt.Sprintf(":%d", *evidence.Line)
		}
		evidenceLines = append(evidenceLines, "- "+ref)
	}
	if len(evidenceLines) == 0 {
		evidenceLines = append(evidenceLines, "- None listed.")
	}

	focusLines := "- Inspect the workspace tree around the bug."
	if len(treeFocus) > 0 {
		items := make([]string, 0, min(len(treeFocus), 12))
		for _, path := range treeFocus[:min(len(treeFocus), 12)] {
			items = append(items, "- "+path)
		}
		focusLines = strings.Join(items, "\n")
	}

	fixLines := []string{}
	for _, fix := range recentFixes[:min(len(recentFixes), 4)] {
		actorLabel := fix.Actor.Label
		if actorLabel == "" {
			actorLabel = fix.Actor.Name
		}
		changed := "no files recorded"
		if len(fix.ChangedFiles) > 0 {
			changed = strings.Join(fix.ChangedFiles[:min(len(fix.ChangedFiles), 4)], ", ")
		}
		fixLines = append(fixLines, "- "+fix.FixID+" ["+fix.Status+"] by "+actorLabel+": "+fix.Summary+" ("+changed+")")
	}
	if len(fixLines) == 0 {
		fixLines = append(fixLines, "- No prior fixes recorded.")
	}

	historyLines := []string{}
	for _, entry := range recentActivity[:min(len(recentActivity), 6)] {
		beforeAfter, ok := entry.Details["before_after"].(map[string]any)
		if ok && len(beforeAfter) > 0 {
			keys := make([]string, 0, len(beforeAfter))
			for field := range beforeAfter {
				keys = append(keys, field)
			}
			slices.Sort(keys)
			fragments := make([]string, 0, len(keys))
			for _, field := range keys {
				diff, ok := beforeAfter[field].(map[string]any)
				if ok {
					fragments = append(fragments, field+" "+toText(diff["from"])+" -> "+toText(diff["to"]))
				} else {
					fragments = append(fragments, field+" "+toText(beforeAfter[field]))
				}
			}
			historyLines = append(historyLines, "- "+entry.CreatedAt+": "+strings.Join(fragments, ", "))
			continue
		}
		historyLines = append(historyLines, "- "+entry.CreatedAt+": "+entry.Summary)
	}
	if len(historyLines) == 0 {
		historyLines = append(historyLines, "- No recent issue history.")
	}

	guidanceLines := []string{}
	for _, item := range guidance[:min(len(guidance), replayGuidanceLimit)] {
		mode := "optional"
		if item.AlwaysOn {
			mode = "always-on"
		}
		guidanceLines = append(guidanceLines, "- "+item.Path+" ["+item.Kind+", "+mode+"]: "+fallbackString(item.Summary, item.Title))
	}
	if len(guidanceLines) == 0 {
		guidanceLines = append(guidanceLines, "- No repository guidance files were found.")
	}

	verificationLines := []string{}
	for _, profile := range verificationProfiles[:min(len(verificationProfiles), 4)] {
		coverageBits := []string{}
		if profile.CoverageCommand != nil && strings.TrimSpace(*profile.CoverageCommand) != "" {
			coverageBits = append(coverageBits, "coverage command: "+*profile.CoverageCommand)
		}
		if profile.CoverageReportPath != nil && strings.TrimSpace(*profile.CoverageReportPath) != "" {
			coverageBits = append(coverageBits, "report: "+*profile.CoverageReportPath)
		}
		if profile.CoverageFormat != "" && profile.CoverageFormat != "unknown" {
			coverageBits = append(coverageBits, "format: "+profile.CoverageFormat)
		}
		coverageSummary := ""
		if len(coverageBits) > 0 {
			coverageSummary = " (" + strings.Join(coverageBits, "; ") + ")"
		}
		verificationLines = append(verificationLines, "- "+profile.Name+": "+profile.TestCommand+coverageSummary)
	}
	if len(verificationLines) == 0 {
		verificationLines = append(verificationLines, "- No verification profiles configured yet.")
	}

	ticketLines := []string{}
	for _, item := range ticketContexts[:min(len(ticketContexts), 4)] {
		headerBits := []string{item.Provider}
		if item.ExternalID != nil && strings.TrimSpace(*item.ExternalID) != "" {
			headerBits = append(headerBits, *item.ExternalID)
		}
		if item.Status != nil && strings.TrimSpace(*item.Status) != "" {
			headerBits = append(headerBits, *item.Status)
		}
		criteria := "No acceptance criteria recorded."
		if len(item.AcceptanceCriteria) > 0 {
			criteria = strings.Join(item.AcceptanceCriteria[:min(len(item.AcceptanceCriteria), 3)], "; ")
		}
		summary := item.Summary
		if strings.TrimSpace(summary) == "" {
			summary = "No summary."
		}
		ticketLines = append(ticketLines, "- "+item.Title+" ["+strings.Join(headerBits, " / ")+"]: "+summary+" Acceptance criteria: "+criteria)
	}
	if len(ticketLines) == 0 {
		ticketLines = append(ticketLines, "- No linked ticket context recorded.")
	}

	threatLines := []string{}
	for _, item := range threatModels[:min(len(threatModels), 3)] {
		assets := "No assets listed."
		if len(item.Assets) > 0 {
			assets = strings.Join(item.Assets[:min(len(item.Assets), 3)], ", ")
		}
		abuseCases := "No abuse cases listed."
		if len(item.AbuseCases) > 0 {
			abuseCases = strings.Join(item.AbuseCases[:min(len(item.AbuseCases), 2)], "; ")
		}
		mitigations := "No mitigations listed."
		if len(item.Mitigations) > 0 {
			mitigations = strings.Join(item.Mitigations[:min(len(item.Mitigations), 2)], "; ")
		}
		summary := item.Summary
		if strings.TrimSpace(summary) == "" {
			summary = "No summary."
		}
		threatLines = append(threatLines, "- "+item.Title+" ["+item.Methodology+" / "+item.Status+"]: "+summary+" Assets: "+assets+". Abuse cases: "+abuseCases+". Mitigations: "+mitigations)
	}
	if len(threatLines) == 0 {
		threatLines = append(threatLines, "- No threat model recorded yet.")
	}

	browserLines := []string{}
	for _, item := range browserDumps[:min(len(browserDumps), 3)] {
		pageBits := []string{}
		if item.PageTitle != nil && strings.TrimSpace(*item.PageTitle) != "" {
			pageBits = append(pageBits, *item.PageTitle)
		}
		if item.PageURL != nil && strings.TrimSpace(*item.PageURL) != "" {
			pageBits = append(pageBits, *item.PageURL)
		}
		pageSummary := "No page metadata recorded."
		if len(pageBits) > 0 {
			pageSummary = strings.Join(pageBits, " — ")
		}
		consoleExcerpt := "No console messages recorded."
		if len(item.ConsoleMessages) > 0 {
			consoleExcerpt = strings.Join(item.ConsoleMessages[:min(len(item.ConsoleMessages), 2)], "; ")
		}
		networkExcerpt := "No network requests recorded."
		if len(item.NetworkRequests) > 0 {
			networkExcerpt = strings.Join(item.NetworkRequests[:min(len(item.NetworkRequests), 2)], "; ")
		}
		domExcerpt := "No DOM snapshot recorded."
		if strings.TrimSpace(item.DOMSnapshot) != "" {
			domExcerpt = strings.TrimSpace(strings.ReplaceAll(item.DOMSnapshot[:min(len(item.DOMSnapshot), 220)], "\n", " "))
		}
		summary := item.Summary
		if strings.TrimSpace(summary) == "" {
			summary = pageSummary
		}
		browserLines = append(browserLines, "- "+item.Label+" ["+item.Source+"]: "+summary+". Page: "+pageSummary+". Console: "+consoleExcerpt+". Network: "+networkExcerpt+". DOM: "+domExcerpt)
	}
	if len(browserLines) == 0 {
		browserLines = append(browserLines, "- No browser dumps recorded yet.")
	}

	repoConfigLines := []string{}
	if repoConfig != nil {
		if strings.TrimSpace(repoConfig.Description) != "" {
			repoConfigLines = append(repoConfigLines, "- Description: "+repoConfig.Description)
		}
		if len(repoConfig.CodeGuidelines) > 0 {
			repoConfigLines = append(repoConfigLines, "- Code guidelines: "+strings.Join(repoConfig.CodeGuidelines[:min(len(repoConfig.CodeGuidelines), 6)], ", "))
		}
		if len(repoConfig.PathFilters) > 0 {
			repoConfigLines = append(repoConfigLines, "- Path filters: "+strings.Join(repoConfig.PathFilters[:min(len(repoConfig.PathFilters), 6)], ", "))
		}
		for _, item := range repoConfig.MCPServers[:min(len(repoConfig.MCPServers), 3)] {
			detail := firstNonEmptyRepoConfig(item.Description, item.Usage, "Configured MCP context source.")
			repoConfigLines = append(repoConfigLines, "- MCP "+item.Name+": "+detail)
		}
	}
	if len(repoConfigLines) == 0 {
		repoConfigLines = append(repoConfigLines, "- No .xmustard config loaded.")
	}

	repoDirLines := []string{}
	if repoMap != nil {
		for _, item := range repoMap.TopDirectories[:min(len(repoMap.TopDirectories), 5)] {
			repoDirLines = append(repoDirLines, "- "+item.Path+": "+toText(item.SourceFileCount)+" source files, "+toText(item.TestFileCount)+" test files")
		}
	}
	if len(repoDirLines) == 0 {
		repoDirLines = append(repoDirLines, "- No repo map available.")
	}

	relatedLines := "- No related paths ranked yet."
	if len(relatedPaths) > 0 {
		items := make([]string, 0, min(len(relatedPaths), 8))
		for _, path := range relatedPaths[:min(len(relatedPaths), 8)] {
			items = append(items, "- "+path)
		}
		relatedLines = strings.Join(items, "\n")
	}

	symbolLines := []string{}
	relatedArtifactLines := []string{}
	if dynamicContext != nil {
		for _, item := range dynamicContext.SymbolContext[:min(len(dynamicContext.SymbolContext), 6)] {
			location := item.Path
			if item.LineStart != nil {
				location = fmt.Sprintf("%s:%d", item.Path, *item.LineStart)
			}
			scope := ""
			if item.EnclosingScope != nil && strings.TrimSpace(*item.EnclosingScope) != "" {
				scope = " in " + *item.EnclosingScope
			}
			reason := ""
			if item.Reason != nil && strings.TrimSpace(*item.Reason) != "" {
				reason = " (" + *item.Reason + ")"
			}
			symbolLines = append(symbolLines, "- "+item.Kind+" "+item.Symbol+scope+" @ "+location+reason)
		}
		for _, item := range dynamicContext.RelatedContext[:min(len(dynamicContext.RelatedContext), 6)] {
			matched := ""
			if len(item.MatchedTerms) > 0 {
				matched = " matches " + strings.Join(item.MatchedTerms[:min(len(item.MatchedTerms), 3)], ", ")
			}
			reason := ""
			if item.Reason != nil && strings.TrimSpace(*item.Reason) != "" {
				reason = " " + strings.TrimSpace(*item.Reason)
			}
			path := ""
			if item.Path != nil && strings.TrimSpace(*item.Path) != "" {
				path = " [" + *item.Path + "]"
			}
			line := "- " + item.ArtifactType + " " + item.Title + path + ":" + reason + matched
			relatedArtifactLines = append(relatedArtifactLines, strings.TrimRight(line, ":"))
		}
	}
	if len(symbolLines) == 0 {
		symbolLines = append(symbolLines, "- No symbol context ranked yet.")
	}
	if len(relatedArtifactLines) == 0 {
		relatedArtifactLines = append(relatedArtifactLines, "- No related artifacts ranked yet.")
	}
	retrievalLines := []string{}
	for _, item := range retrievalLedger[:min(len(retrievalLedger), 10)] {
		path := ""
		if item.Path != nil && strings.TrimSpace(*item.Path) != "" {
			path = " [" + *item.Path + "]"
		}
		matched := ""
		if len(item.MatchedTerms) > 0 {
			matched = " matches " + strings.Join(item.MatchedTerms[:min(len(item.MatchedTerms), 3)], ", ")
		}
		retrievalLines = append(
			retrievalLines,
			fmt.Sprintf("- %s %s%s: %s%s (score %d)", item.SourceType, item.Title, path, item.Reason, matched, item.Score),
		)
	}
	if len(retrievalLines) == 0 {
		retrievalLines = append(retrievalLines, "- No retrieval ledger entries recorded yet.")
	}

	pathInstructionLines := []string{}
	for _, item := range matchedPathInstructions[:min(len(matchedPathInstructions), 6)] {
		label := item.Path
		if item.Title != nil && strings.TrimSpace(*item.Title) != "" {
			label = *item.Title
		}
		pathInstructionLines = append(pathInstructionLines, "- "+label+" ["+strings.Join(item.MatchedPaths[:min(len(item.MatchedPaths), 4)], ", ")+"]: "+item.Instructions)
	}
	if len(pathInstructionLines) == 0 {
		pathInstructionLines = append(pathInstructionLines, "- No path-specific instructions matched the current issue paths.")
	}

	summary := firstNonEmptyPtr(issue.Summary)
	if summary == "" {
		summary = "No summary supplied."
	}
	impact := firstNonEmptyPtr(issue.Impact)
	if impact == "" {
		impact = "No impact supplied."
	}

	return "You are fixing bug " + issue.BugID + " in workspace " + workspace.RootPath + ".\n" +
		"Title: " + issue.Title + "\n" +
		"Severity: " + issue.Severity + "\n" +
		"Doc status: " + issue.DocStatus + "\n" +
		"Code status: " + issue.CodeStatus + "\n" +
		"Tracker source: " + issue.Source + "\n" +
		"Summary: " + summary + "\n" +
		"Impact: " + impact + "\n\n" +
		"Evidence references:\n" + strings.Join(evidenceLines, "\n") + "\n\n" +
		"Recent issue history:\n" + strings.Join(historyLines, "\n") + "\n\n" +
		"Prior fix history:\n" + strings.Join(fixLines, "\n") + "\n\n" +
		"Ticket context:\n" + strings.Join(ticketLines, "\n") + "\n\n" +
		"Threat model:\n" + strings.Join(threatLines, "\n") + "\n\n" +
		"Browser context:\n" + strings.Join(browserLines, "\n") + "\n\n" +
		"Repo config:\n" + strings.Join(repoConfigLines, "\n") + "\n\n" +
		"Path-specific guidance:\n" + strings.Join(pathInstructionLines, "\n") + "\n\n" +
		"Structural context:\n" + strings.Join(repoDirLines, "\n") + "\n\n" +
		"Ranked related paths:\n" + relatedLines + "\n\n" +
		"Symbol context:\n" + strings.Join(symbolLines, "\n") + "\n\n" +
		"Related artifacts:\n" + strings.Join(relatedArtifactLines, "\n") + "\n\n" +
		"Retrieval ledger:\n" + strings.Join(retrievalLines, "\n") + "\n\n" +
		"Repository guidance:\n" + strings.Join(guidanceLines, "\n") + "\n\n" +
		"Known verification profiles:\n" + strings.Join(verificationLines, "\n") + "\n\n" +
		"Priority files:\n" + focusLines + "\n\n" +
		"Required workflow:\n" +
		"1. Reproduce or validate the bug against the current code.\n" +
		"2. Make the minimal safe fix.\n" +
		"3. Add or update tests.\n" +
		"4. Record exact files changed, tests run, and how the fix works back into the tracker.\n" +
		"Return a concise engineering result, not a conversation."
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
		result.Available = true
		return result
	}

	result.Available = true
	result.IsGitRepo = true
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "# branch.head ") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			if value != "" {
				result.Branch = &value
			}
			continue
		}
		if strings.HasPrefix(line, "# branch.oid ") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "# branch.oid "))
			if value != "" && value != "(initial)" {
				result.HeadSHA = &value
			}
			continue
		}
		if strings.HasPrefix(line, "# branch.ab ") {
			parts := strings.Fields(line)
			for _, part := range parts {
				if strings.HasPrefix(part, "+") {
					result.Ahead = atoiSafe(strings.TrimPrefix(part, "+"))
				}
				if strings.HasPrefix(part, "-") {
					result.Behind = atoiSafe(strings.TrimPrefix(part, "-"))
				}
			}
			continue
		}
		if strings.HasPrefix(line, "? ") {
			result.UntrackedFiles++
			result.DirtyFiles++
			result.DirtyPaths = append(result.DirtyPaths, strings.TrimSpace(strings.TrimPrefix(line, "? ")))
			continue
		}
		if strings.HasPrefix(line, "1 ") || strings.HasPrefix(line, "2 ") || strings.HasPrefix(line, "u ") {
			parts := strings.Fields(line)
			if len(parts) < 3 {
				continue
			}
			xy := parts[1]
			path := parts[len(parts)-1]
			result.DirtyFiles++
			result.DirtyPaths = append(result.DirtyPaths, path)
			if len(xy) > 0 && xy[0] != '.' {
				result.StagedFiles++
			}
		}
	}
	if len(result.DirtyPaths) > 20 {
		result.DirtyPaths = result.DirtyPaths[:20]
	}
	return result
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

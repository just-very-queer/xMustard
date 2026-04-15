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
	repoMap, err := loadOrBuildRepoMap(dataDir, workspaceID, snapshot.Workspace.RootPath)
	if err != nil {
		return nil, err
	}

	relatedPaths := rankRelatedPathsForIssue(*issue, treeFocus, ticketContexts, repoMap)
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
		"Structural context:\n" + strings.Join(repoDirLines, "\n") + "\n\n" +
		"Ranked related paths:\n" + relatedLines + "\n\n" +
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

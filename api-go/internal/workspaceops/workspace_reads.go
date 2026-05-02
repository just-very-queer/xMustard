package workspaceops

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"xmustard/api-go/internal/rustcore"
)

type sourceRecord struct {
	SourceID    string  `json:"source_id"`
	Kind        string  `json:"kind"`
	Label       string  `json:"label"`
	Path        string  `json:"path"`
	RecordCount int     `json:"record_count"`
	ModifiedAt  *string `json:"modified_at,omitempty"`
	Notes       *string `json:"notes,omitempty"`
}

type discoverySignal struct {
	SignalID      string        `json:"signal_id"`
	Kind          string        `json:"kind"`
	Severity      string        `json:"severity"`
	Title         string        `json:"title"`
	Summary       string        `json:"summary"`
	FilePath      string        `json:"file_path"`
	Line          int           `json:"line"`
	Evidence      []evidenceRef `json:"evidence"`
	Tags          []string      `json:"tags"`
	Fingerprint   *string       `json:"fingerprint"`
	PromotedBugID *string       `json:"promoted_bug_id,omitempty"`
	CreatedAt     *string       `json:"created_at,omitempty"`
}

type runtimeModel struct {
	Runtime string `json:"runtime"`
	ID      string `json:"id"`
}

type runtimeCapabilities struct {
	Runtime    string         `json:"runtime"`
	Available  bool           `json:"available"`
	BinaryPath *string        `json:"binary_path,omitempty"`
	Models     []runtimeModel `json:"models"`
	Notes      *string        `json:"notes,omitempty"`
}

type activityRollupItem struct {
	Key        string  `json:"key"`
	Label      string  `json:"label"`
	Count      int     `json:"count"`
	ActorKey   *string `json:"actor_key,omitempty"`
	Action     *string `json:"action,omitempty"`
	EntityType *string `json:"entity_type,omitempty"`
}

type activityOverview struct {
	TotalEvents        int                  `json:"total_events"`
	UniqueActors       int                  `json:"unique_actors"`
	UniqueActions      int                  `json:"unique_actions"`
	OperatorEvents     int                  `json:"operator_events"`
	AgentEvents        int                  `json:"agent_events"`
	SystemEvents       int                  `json:"system_events"`
	IssuesTouched      int                  `json:"issues_touched"`
	FixesTouched       int                  `json:"fixes_touched"`
	RunsTouched        int                  `json:"runs_touched"`
	ViewsTouched       int                  `json:"views_touched"`
	CountsByEntityType map[string]int       `json:"counts_by_entity_type"`
	TopActors          []activityRollupItem `json:"top_actors"`
	TopActions         []activityRollupItem `json:"top_actions"`
	TopEntities        []activityRollupItem `json:"top_entities"`
	MostRecentAt       *string              `json:"most_recent_at,omitempty"`
}

type treeNode struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	NodeType    string `json:"node_type"`
	HasChildren bool   `json:"has_children"`
	SizeBytes   *int64 `json:"size_bytes,omitempty"`
}

type RepoChangeRecord struct {
	Path         string  `json:"path"`
	Status       string  `json:"status"`
	Scope        string  `json:"scope"`
	PreviousPath *string `json:"previous_path,omitempty"`
	Staged       bool    `json:"staged"`
	Unstaged     bool    `json:"unstaged"`
}

type ChangedSymbolRecord struct {
	Path            string   `json:"path"`
	Symbol          string   `json:"symbol"`
	Kind            string   `json:"kind"`
	LineStart       *int     `json:"line_start,omitempty"`
	LineEnd         *int     `json:"line_end,omitempty"`
	EvidenceSource  string   `json:"evidence_source"`
	SemanticStatus  *string  `json:"semantic_status,omitempty"`
	SelectionReason string   `json:"selection_reason"`
	ChangeScopes    []string `json:"change_scopes"`
	ChangeStatuses  []string `json:"change_statuses"`
}

type PathSymbolRecord struct {
	Path           string  `json:"path"`
	Symbol         string  `json:"symbol"`
	Kind           string  `json:"kind"`
	LineStart      *int    `json:"line_start,omitempty"`
	LineEnd        *int    `json:"line_end,omitempty"`
	EnclosingScope *string `json:"enclosing_scope,omitempty"`
	EvidenceSource string  `json:"evidence_source"`
	Reason         *string `json:"reason,omitempty"`
	Score          int     `json:"score"`
}

type PathSymbolsResult struct {
	WorkspaceID     string             `json:"workspace_id"`
	Path            string             `json:"path"`
	SymbolSource    string             `json:"symbol_source"`
	ParserLanguage  *string            `json:"parser_language,omitempty"`
	EvidenceSource  string             `json:"evidence_source"`
	SelectionReason string             `json:"selection_reason"`
	Symbols         []PathSymbolRecord `json:"symbols"`
	Warnings        []string           `json:"warnings"`
	GeneratedAt     string             `json:"generated_at"`
}

type CodeExplainerResult struct {
	WorkspaceID     string   `json:"workspace_id"`
	Path            string   `json:"path"`
	Role            string   `json:"role"`
	LineCount       int      `json:"line_count"`
	ImportCount     int      `json:"import_count"`
	DetectedSymbols []string `json:"detected_symbols"`
	SymbolSource    string   `json:"symbol_source"`
	ParserLanguage  *string  `json:"parser_language,omitempty"`
	EvidenceSource  string   `json:"evidence_source"`
	SelectionReason string   `json:"selection_reason"`
	Summary         string   `json:"summary"`
	Hints           []string `json:"hints"`
	Warnings        []string `json:"warnings"`
	GeneratedAt     string   `json:"generated_at"`
}

type ImpactPathRecord struct {
	Path             string `json:"path"`
	Reason           string `json:"reason"`
	DerivationSource string `json:"derivation_source"`
	Score            int    `json:"score"`
}

type ImpactReport struct {
	WorkspaceID         string                `json:"workspace_id"`
	BaseRef             string                `json:"base_ref"`
	SemanticStatus      any                   `json:"semantic_status,omitempty"`
	ChangedFiles        []RepoChangeRecord    `json:"changed_files"`
	ChangedSymbols      []ChangedSymbolRecord `json:"changed_symbols"`
	LikelyAffectedFiles []ImpactPathRecord    `json:"likely_affected_files"`
	LikelyAffectedTests []ImpactPathRecord    `json:"likely_affected_tests"`
	DerivationSummary   string                `json:"derivation_summary"`
	Confidence          string                `json:"confidence"`
	Warnings            []string              `json:"warnings"`
	GeneratedAt         string                `json:"generated_at"`
}

type RetrievalSearchHit struct {
	Path         string   `json:"path"`
	SourceType   string   `json:"source_type"`
	Title        string   `json:"title"`
	Reason       string   `json:"reason"`
	MatchedTerms []string `json:"matched_terms"`
	Score        int      `json:"score"`
}

type RetrievalSearchResult struct {
	WorkspaceID     string                        `json:"workspace_id"`
	Query           string                        `json:"query"`
	Hits            []RetrievalSearchHit          `json:"hits"`
	RetrievalLedger []ContextRetrievalLedgerEntry `json:"retrieval_ledger"`
	Warnings        []string                      `json:"warnings"`
	GeneratedAt     string                        `json:"generated_at"`
}

type RepoContextTargetLink struct {
	Target any    `json:"target"`
	Reason string `json:"reason"`
	Score  int    `json:"score"`
}

type RepoContextPlanLink struct {
	RunID         string   `json:"run_id"`
	IssueID       string   `json:"issue_id"`
	Status        string   `json:"status"`
	Phase         *string  `json:"phase,omitempty"`
	OwnershipMode *string  `json:"ownership_mode,omitempty"`
	OwnerLabel    *string  `json:"owner_label,omitempty"`
	AttachedFiles []string `json:"attached_files"`
	Reason        string   `json:"reason"`
	Score         int      `json:"score"`
}

type RepoContextActivityLink struct {
	Action    string  `json:"action"`
	Summary   string  `json:"summary"`
	IssueID   *string `json:"issue_id,omitempty"`
	RunID     *string `json:"run_id,omitempty"`
	CreatedAt string  `json:"created_at"`
	Reason    string  `json:"reason"`
	Score     int     `json:"score"`
}

type RepoContextFixLink struct {
	FixID        string   `json:"fix_id"`
	IssueID      string   `json:"issue_id"`
	RunID        *string  `json:"run_id,omitempty"`
	Summary      string   `json:"summary"`
	ChangedFiles []string `json:"changed_files"`
	TestsRun     []string `json:"tests_run"`
	RecordedAt   string   `json:"recorded_at"`
	Reason       string   `json:"reason"`
}

type RepoContextRecord struct {
	WorkspaceID       string                        `json:"workspace_id"`
	BaseRef           string                        `json:"base_ref"`
	SemanticStatus    any                           `json:"semantic_status,omitempty"`
	Impact            *ImpactReport                 `json:"impact"`
	RunTargets        []RepoContextTargetLink       `json:"run_targets"`
	VerifyTargets     []RepoContextTargetLink       `json:"verify_targets"`
	PlanLinks         []RepoContextPlanLink         `json:"plan_links"`
	RecentActivity    []RepoContextActivityLink     `json:"recent_activity"`
	LatestAcceptedFix *RepoContextFixLink           `json:"latest_accepted_fix,omitempty"`
	RetrievalLedger   []ContextRetrievalLedgerEntry `json:"retrieval_ledger"`
	GeneratedAt       string                        `json:"generated_at"`
}

type runRecord struct {
	RunID             string          `json:"run_id"`
	WorkspaceID       string          `json:"workspace_id"`
	IssueID           string          `json:"issue_id"`
	Runtime           string          `json:"runtime"`
	Model             string          `json:"model"`
	Status            string          `json:"status"`
	Title             string          `json:"title"`
	Prompt            string          `json:"prompt"`
	Command           []string        `json:"command"`
	CommandPreview    string          `json:"command_preview"`
	LogPath           string          `json:"log_path"`
	OutputPath        string          `json:"output_path"`
	CreatedAt         string          `json:"created_at"`
	StartedAt         *string         `json:"started_at,omitempty"`
	CompletedAt       *string         `json:"completed_at,omitempty"`
	ExitCode          *int            `json:"exit_code,omitempty"`
	PID               *int            `json:"pid,omitempty"`
	Error             *string         `json:"error,omitempty"`
	RunbookID         *string         `json:"runbook_id,omitempty"`
	EvalScenarioID    *string         `json:"eval_scenario_id,omitempty"`
	EvalReplayBatchID *string         `json:"eval_replay_batch_id,omitempty"`
	Worktree          *WorktreeStatus `json:"worktree,omitempty"`
	GuidancePaths     []string        `json:"guidance_paths"`
	Summary           map[string]any  `json:"summary,omitempty"`
	Plan              *RunPlan        `json:"plan,omitempty"`
}

func ReadImpact(dataDir string, workspaceID string, baseRef string) (*ImpactReport, error) {
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD"
	}
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	changes, warnings := readGoChangeRecords(snapshot.Workspace.RootPath, baseRef)
	changedPaths := changePaths(changes, false)
	semanticImpact, semanticWarnings, err := readRustSemanticImpact(workspaceID, snapshot.Workspace.RootPath, changes)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, semanticWarnings...)
	confidence := "low"
	if len(semanticImpact.ChangedSymbols) > 0 && (len(semanticImpact.LikelyAffectedFiles) > 0 || len(semanticImpact.LikelyAffectedTests) > 0) {
		confidence = "high"
	} else if len(changedPaths) > 0 {
		confidence = "medium"
	}
	if len(changedPaths) == 0 {
		warnings = append(warnings, "No changed files were detected from the current git comparison surface.")
	} else if len(semanticImpact.ChangedSymbols) == 0 {
		warnings = append(warnings, "Changed files were detected, but Rust semantic core did not derive symbol-level impact.")
	}
	return &ImpactReport{
		WorkspaceID:         workspaceID,
		BaseRef:             baseRef,
		ChangedFiles:        changes,
		ChangedSymbols:      convertRustChangedSymbols(semanticImpact.ChangedSymbols),
		LikelyAffectedFiles: convertRustImpactPaths(semanticImpact.LikelyAffectedFiles),
		LikelyAffectedTests: convertRustImpactPaths(semanticImpact.LikelyAffectedTests),
		DerivationSummary:   impactSummary(changedPaths, semanticImpact),
		Confidence:          confidence,
		Warnings:            dedupeStrings(warnings, 24),
		GeneratedAt:         nowUTC(),
	}, nil
}

func ReadRepoContext(dataDir string, workspaceID string, baseRef string) (*RepoContextRecord, error) {
	impact, err := ReadImpact(dataDir, workspaceID, baseRef)
	if err != nil {
		return nil, err
	}
	referencePaths := map[string]struct{}{}
	for _, path := range changePaths(impact.ChangedFiles, true) {
		referencePaths[path] = struct{}{}
	}
	for _, item := range impact.LikelyAffectedFiles {
		referencePaths[item.Path] = struct{}{}
	}
	for _, item := range impact.LikelyAffectedTests {
		referencePaths[item.Path] = struct{}{}
	}
	planLinks := buildGoPlanLinks(dataDir, workspaceID, referencePaths)
	activityLinks := buildGoActivityLinks(dataDir, workspaceID, referencePaths)
	latestFix := buildGoLatestFix(dataDir, workspaceID, referencePaths)
	ledger := buildRepoContextLedger(impact, planLinks, activityLinks, latestFix)
	return &RepoContextRecord{
		WorkspaceID:       workspaceID,
		BaseRef:           impact.BaseRef,
		Impact:            impact,
		RunTargets:        []RepoContextTargetLink{},
		VerifyTargets:     []RepoContextTargetLink{},
		PlanLinks:         planLinks,
		RecentActivity:    activityLinks,
		LatestAcceptedFix: latestFix,
		RetrievalLedger:   ledger,
		GeneratedAt:       nowUTC(),
	}, nil
}

func SearchRetrieval(dataDir string, workspaceID string, query string, limit int) (*RetrievalSearchResult, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 12
	}
	tokens := queryTokens(query)
	hits := []RetrievalSearchHit{}
	seen := map[string]struct{}{}
	push := func(hit RetrievalSearchHit) {
		key := hit.SourceType + "\x00" + hit.Path + "\x00" + hit.Title
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		hits = append(hits, hit)
	}
	repoMap, err := loadOrBuildRepoMap(dataDir, workspaceID, snapshot.Workspace.RootPath)
	if err == nil && repoMap != nil {
		for _, file := range repoMap.KeyFiles {
			matched := matchedTerms(strings.ToLower(file.Path+" "+file.Role), tokens)
			if len(matched) == 0 {
				continue
			}
			push(RetrievalSearchHit{Path: file.Path, SourceType: "lexical_hit", Title: file.Path, Reason: "Key repo-map path matched query terms: " + strings.Join(matched[:min(len(matched), 4)], ", ") + ".", MatchedTerms: matched, Score: 6 + len(matched)})
		}
		for _, file := range repoMap.KeyFiles {
			if len(hits) >= limit {
				break
			}
			pushContentHit(snapshot.Workspace.RootPath, file.Path, tokens, push)
		}
	}
	for _, path := range candidateSourcePaths(snapshot.Workspace.RootPath, 80) {
		if len(hits) >= limit*3 {
			break
		}
		pushContentHit(snapshot.Workspace.RootPath, path, tokens, push)
	}
	structuralChanges := []RepoChangeRecord{}
	for _, path := range dedupeStrings(hitPaths(hits), 24) {
		structuralChanges = append(structuralChanges, RepoChangeRecord{Path: path, Status: "context", Scope: "retrieval_search"})
	}
	if len(structuralChanges) > 0 {
		if semanticImpact, _, err := readRustSemanticImpact(workspaceID, snapshot.Workspace.RootPath, structuralChanges); err == nil {
			for _, symbol := range convertRustChangedSymbols(semanticImpact.ChangedSymbols) {
				matched := matchedTerms(strings.ToLower(symbol.Symbol+" "+symbol.Kind), tokens)
				if len(matched) == 0 {
					continue
				}
				push(RetrievalSearchHit{Path: symbol.Path, SourceType: "structural_hit", Title: symbol.Kind + " " + symbol.Symbol, Reason: "Rust semantic-core symbol matched query terms: " + strings.Join(matched[:min(len(matched), 4)], ", ") + ".", MatchedTerms: matched, Score: 9 + len(matched)})
			}
		}
	}
	slices.SortFunc(hits, func(a, b RetrievalSearchHit) int {
		if a.Score != b.Score {
			return b.Score - a.Score
		}
		if a.SourceType != b.SourceType {
			return strings.Compare(a.SourceType, b.SourceType)
		}
		return strings.Compare(a.Path+a.Title, b.Path+b.Title)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return &RetrievalSearchResult{
		WorkspaceID:     workspaceID,
		Query:           query,
		Hits:            hits,
		RetrievalLedger: retrievalLedgerFromHits(hits),
		Warnings:        []string{"Go delivery owns this read path; structural hits are derived through the Rust semantic-core contract."},
		GeneratedAt:     nowUTC(),
	}, nil
}

func ReadPathSymbols(dataDir string, workspaceID string, relativePath string) (*PathSymbolsResult, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeWorkspaceFile(snapshot.Workspace.RootPath, relativePath)
	if err != nil {
		return nil, err
	}
	rustResult, err := readRustPathSymbols(workspaceID, snapshot.Workspace.RootPath, normalized)
	if err != nil {
		return nil, err
	}
	return &PathSymbolsResult{
		WorkspaceID:     workspaceID,
		Path:            rustResult.Path,
		SymbolSource:    rustResult.SymbolSource,
		ParserLanguage:  rustResult.ParserLanguage,
		EvidenceSource:  rustResult.EvidenceSource,
		SelectionReason: rustResult.SelectionReason,
		Symbols:         convertRustPathSymbols(rustResult.Symbols),
		Warnings:        rustResult.Warnings,
		GeneratedAt:     rustResult.GeneratedAt,
	}, nil
}

func ExplainPath(dataDir string, workspaceID string, relativePath string) (*CodeExplainerResult, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeWorkspaceFile(snapshot.Workspace.RootPath, relativePath)
	if err != nil {
		return nil, err
	}
	rustResult, err := readRustPathExplanation(workspaceID, snapshot.Workspace.RootPath, normalized)
	if err != nil {
		return nil, err
	}
	return &CodeExplainerResult{
		WorkspaceID:     rustResult.WorkspaceID,
		Path:            rustResult.Path,
		Role:            rustResult.Role,
		LineCount:       rustResult.LineCount,
		ImportCount:     rustResult.ImportCount,
		DetectedSymbols: rustResult.DetectedSymbols,
		SymbolSource:    rustResult.SymbolSource,
		ParserLanguage:  rustResult.ParserLanguage,
		EvidenceSource:  rustResult.EvidenceSource,
		SelectionReason: rustResult.SelectionReason,
		Summary:         rustResult.Summary,
		Hints:           rustResult.Hints,
		Warnings:        rustResult.Warnings,
		GeneratedAt:     rustResult.GeneratedAt,
	}, nil
}

func ReadWorkspaceSnapshot(dataDir string, workspaceID string) (*workspaceSnapshot, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func ListWorkspaceActivity(dataDir string, workspaceID string, issueID string, runID string, limit int) ([]activityRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	handle, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []activityRecord{}, nil
		}
		return nil, err
	}
	defer handle.Close()

	items := []activityRecord{}
	scanner := bufio.NewScanner(handle)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item activityRecord
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		if issueID != "" && (item.IssueID == nil || *item.IssueID != issueID) {
			continue
		}
		if runID != "" && (item.RunID == nil || *item.RunID != runID) {
			continue
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(items, func(a, b activityRecord) int {
		if a.CreatedAt > b.CreatedAt {
			return -1
		}
		if a.CreatedAt < b.CreatedAt {
			return 1
		}
		return 0
	})
	if limit <= 0 {
		limit = 100
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func ReadActivityOverview(dataDir string, workspaceID string, limit int) (*activityOverview, error) {
	activity, err := ListWorkspaceActivity(dataDir, workspaceID, "", "", limit)
	if err != nil {
		return nil, err
	}

	actorCounts := map[string]int{}
	actionCounts := map[string]int{}
	entityCounts := map[string]int{}
	actorLabels := map[string]string{}
	issuesTouched := map[string]struct{}{}
	fixesTouched := map[string]struct{}{}
	runsTouched := map[string]struct{}{}
	viewsTouched := map[string]struct{}{}

	for _, item := range activity {
		actorCounts[item.Actor.Key]++
		actionCounts[item.Action]++
		entityCounts[item.EntityType]++
		actorLabels[item.Actor.Key] = item.Actor.Label
		if item.IssueID != nil && *item.IssueID != "" {
			issuesTouched[*item.IssueID] = struct{}{}
		} else if item.EntityType == "issue" {
			issuesTouched[item.EntityID] = struct{}{}
		}
		if item.EntityType == "fix" {
			fixesTouched[item.EntityID] = struct{}{}
		}
		if item.RunID != nil && *item.RunID != "" {
			runsTouched[*item.RunID] = struct{}{}
		} else if item.EntityType == "run" {
			runsTouched[item.EntityID] = struct{}{}
		}
		if item.EntityType == "view" {
			viewsTouched[item.EntityID] = struct{}{}
		}
	}

	topActors := buildTopActors(actorCounts, actorLabels)
	topActions := buildTopActions(actionCounts)
	topEntities := buildTopEntities(entityCounts)
	countsByEntityType := map[string]int{}
	for key, value := range entityCounts {
		countsByEntityType[key] = value
	}

	var mostRecentAt *string
	if len(activity) > 0 {
		mostRecentAt = &activity[0].CreatedAt
	}

	return &activityOverview{
		TotalEvents:        len(activity),
		UniqueActors:       len(actorCounts),
		UniqueActions:      len(actionCounts),
		OperatorEvents:     countByActorKind(activity, "operator"),
		AgentEvents:        countByActorKind(activity, "agent"),
		SystemEvents:       countByActorKind(activity, "system"),
		IssuesTouched:      len(issuesTouched),
		FixesTouched:       len(fixesTouched),
		RunsTouched:        len(runsTouched),
		ViewsTouched:       len(viewsTouched),
		CountsByEntityType: sortCountMap(countsByEntityType),
		TopActors:          topActors,
		TopActions:         topActions,
		TopEntities:        topEntities,
		MostRecentAt:       mostRecentAt,
	}, nil
}

func ReadSources(dataDir string, workspaceID string) ([]sourceRecord, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	return snapshot.Sources, nil
}

func ListWorkspaceGuidanceRecords(dataDir string, workspaceID string) ([]RepoGuidanceRecord, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	return collectWorkspaceGuidance(snapshot.Workspace.RootPath, workspaceID)
}

func ReadWorkspaceRepoMap(dataDir string, workspaceID string) (*rustcore.RepoMapSummary, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	return loadOrBuildRepoMap(dataDir, workspaceID, snapshot.Workspace.RootPath)
}

func ListWorkspaceTree(dataDir string, workspaceID string, relativePath string) ([]treeNode, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	rootPath := filepath.Clean(snapshot.Workspace.RootPath)
	target := filepath.Clean(filepath.Join(rootPath, relativePath))
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		leftDir := a.IsDir()
		rightDir := b.IsDir()
		if leftDir != rightDir {
			if leftDir {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Name())
		right := strings.ToLower(b.Name())
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
		return 0
	})
	if len(entries) > 80 {
		entries = entries[:80]
	}

	nodes := make([]treeNode, 0, len(entries))
	for _, entry := range entries {
		childPath := filepath.Join(target, entry.Name())
		relative, err := filepath.Rel(rootPath, childPath)
		if err != nil {
			relative = entry.Name()
		}
		node := treeNode{
			Path:        filepath.ToSlash(relative),
			Name:        entry.Name(),
			NodeType:    "file",
			HasChildren: false,
		}
		if entry.IsDir() {
			node.NodeType = "directory"
			children, err := os.ReadDir(childPath)
			node.HasChildren = err == nil && len(children) > 0
		} else {
			info, err := entry.Info()
			if err == nil {
				size := info.Size()
				node.SizeBytes = &size
			}
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func buildTopActors(counts map[string]int, labels map[string]string) []activityRollupItem {
	keys := sortedCountKeys(counts)
	items := make([]activityRollupItem, 0, min(len(keys), 5))
	for _, key := range keys[:min(len(keys), 5)] {
		actorKey := key
		items = append(items, activityRollupItem{
			Key:      key,
			ActorKey: &actorKey,
			Label:    fallbackString(labels[key], key),
			Count:    counts[key],
		})
	}
	return items
}

func buildTopActions(counts map[string]int) []activityRollupItem {
	keys := sortedCountKeys(counts)
	items := make([]activityRollupItem, 0, min(len(keys), 5))
	for _, key := range keys[:min(len(keys), 5)] {
		action := key
		items = append(items, activityRollupItem{
			Key:    key,
			Action: &action,
			Label:  key,
			Count:  counts[key],
		})
	}
	return items
}

func buildTopEntities(counts map[string]int) []activityRollupItem {
	entityLabels := map[string]string{
		"issue":     "Issues",
		"fix":       "Fixes",
		"run":       "Runs",
		"view":      "Views",
		"signal":    "Signals",
		"workspace": "Workspace",
		"settings":  "Settings",
	}
	keys := sortedCountKeys(counts)
	items := make([]activityRollupItem, 0, min(len(keys), 5))
	for _, key := range keys[:min(len(keys), 5)] {
		entityType := key
		items = append(items, activityRollupItem{
			Key:        key,
			EntityType: &entityType,
			Label:      fallbackString(entityLabels[key], key),
			Count:      counts[key],
		})
	}
	return items
}

func sortedCountKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if counts[a] != counts[b] {
			if counts[a] > counts[b] {
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
	return keys
}

func sortCountMap(counts map[string]int) map[string]int {
	keys := sortedCountKeys(counts)
	ordered := make(map[string]int, len(counts))
	for _, key := range keys {
		ordered[key] = counts[key]
	}
	return ordered
}

func countByActorKind(activity []activityRecord, kind string) int {
	total := 0
	for _, item := range activity {
		if item.Actor.Kind == kind {
			total++
		}
	}
	return total
}

func loadRun(dataDir string, workspaceID string, runID string) (*runRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "runs", runID+".json")
	var run runRecord
	if err := readJSON(path, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func listRuns(dataDir string, workspaceID string) ([]runRecord, error) {
	runsDir := filepath.Join(dataDir, "workspaces", workspaceID, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []runRecord{}, nil
		}
		return nil, err
	}
	runs := []runRecord{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".out.json") {
			continue
		}
		var run runRecord
		if err := readJSON(filepath.Join(runsDir, entry.Name()), &run); err != nil {
			continue
		}
		runs = append(runs, run)
	}
	slices.SortFunc(runs, func(a, b runRecord) int {
		if a.CreatedAt > b.CreatedAt {
			return -1
		}
		if a.CreatedAt < b.CreatedAt {
			return 1
		}
		return 0
	})
	return runs, nil
}

func readGoChangeRecords(rootPath string, baseRef string) ([]RepoChangeRecord, []string) {
	warnings := []string{}
	if _, err := os.Stat(filepath.Join(rootPath, ".git")); err != nil {
		return []RepoChangeRecord{}, []string{"Workspace is not a git repository; Go impact reads cannot derive changed files."}
	}
	records := map[string]RepoChangeRecord{}
	diffCmd := exec.Command("git", "-C", rootPath, "diff", "--name-status", baseRef+"...HEAD")
	if output, err := diffCmd.Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}
			status := gitStatusName(parts[0])
			path := parts[len(parts)-1]
			var previous *string
			if strings.HasPrefix(parts[0], "R") && len(parts) >= 3 {
				value := parts[1]
				previous = &value
			}
			records["since_ref:"+path] = RepoChangeRecord{Path: path, Status: status, Scope: "since_ref", PreviousPath: previous}
		}
	} else {
		warnings = append(warnings, "Git base-ref comparison failed for "+baseRef+"; working-tree changes are still reported.")
	}
	statusCmd := exec.Command("git", "-C", rootPath, "status", "--porcelain=v1")
	if output, err := statusCmd.Output(); err == nil {
		for _, raw := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
			if len(raw) < 4 {
				continue
			}
			code := raw[:2]
			path := strings.TrimSpace(raw[3:])
			if strings.Contains(path, " -> ") {
				path = strings.TrimSpace(path[strings.LastIndex(path, " -> ")+4:])
			}
			status := gitStatusName(strings.TrimSpace(code))
			if strings.HasPrefix(code, "??") {
				status = "untracked"
			}
			records["working_tree:"+path] = RepoChangeRecord{
				Path:     path,
				Status:   status,
				Scope:    "working_tree",
				Staged:   code[0] != ' ' && code[0] != '?',
				Unstaged: code[1] != ' ',
			}
		}
	} else {
		warnings = append(warnings, "Git working-tree status failed; Go impact reads may be incomplete.")
	}
	items := make([]RepoChangeRecord, 0, len(records))
	for _, item := range records {
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b RepoChangeRecord) int {
		return strings.Compare(a.Path+a.Scope+a.Status, b.Path+b.Scope+b.Status)
	})
	return items, warnings
}

func gitStatusName(code string) string {
	switch {
	case strings.HasPrefix(code, "A"):
		return "added"
	case strings.HasPrefix(code, "D"):
		return "deleted"
	case strings.HasPrefix(code, "R"):
		return "renamed"
	case strings.HasPrefix(code, "C"):
		return "copied"
	case strings.Contains(code, "M"):
		return "modified"
	case strings.Contains(code, "?"):
		return "untracked"
	default:
		return "unknown"
	}
}

func readRustSemanticImpact(workspaceID string, rootPath string, changes []RepoChangeRecord) (*rustcore.SemanticImpactReport, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rustChanges := make([]rustcore.RepoChangeRecord, 0, len(changes))
	for _, change := range changes {
		rustChanges = append(rustChanges, rustcore.RepoChangeRecord{
			Path:         change.Path,
			Status:       change.Status,
			Scope:        change.Scope,
			PreviousPath: change.PreviousPath,
			Staged:       change.Staged,
			Unstaged:     change.Unstaged,
		})
	}
	report, err := rustcore.BuildSemanticImpact(ctx, workspaceID, rootPath, rustChanges)
	if err != nil {
		return nil, nil, err
	}
	return report, report.Warnings, nil
}

func readRustPathSymbols(workspaceID string, rootPath string, relativePath string) (*rustcore.PathSymbolsResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return rustcore.ExtractPathSymbols(ctx, workspaceID, rootPath, relativePath)
}

func readRustPathExplanation(workspaceID string, rootPath string, relativePath string) (*rustcore.CodeExplainerResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return rustcore.ExplainPath(ctx, workspaceID, rootPath, relativePath)
}

func convertRustChangedSymbols(items []rustcore.ChangedSymbolRecord) []ChangedSymbolRecord {
	out := make([]ChangedSymbolRecord, 0, len(items))
	for _, item := range items {
		out = append(out, ChangedSymbolRecord{
			Path:            item.Path,
			Symbol:          item.Symbol,
			Kind:            item.Kind,
			LineStart:       item.LineStart,
			LineEnd:         item.LineEnd,
			EvidenceSource:  item.EvidenceSource,
			SemanticStatus:  item.SemanticStatus,
			SelectionReason: item.SelectionReason,
			ChangeScopes:    item.ChangeScopes,
			ChangeStatuses:  item.ChangeStatuses,
		})
	}
	return out
}

func convertRustPathSymbols(items []rustcore.PathSymbolRecord) []PathSymbolRecord {
	out := make([]PathSymbolRecord, 0, len(items))
	for _, item := range items {
		out = append(out, PathSymbolRecord{
			Path:           item.Path,
			Symbol:         item.Symbol,
			Kind:           item.Kind,
			LineStart:      item.LineStart,
			LineEnd:        item.LineEnd,
			EnclosingScope: item.EnclosingScope,
			EvidenceSource: item.EvidenceSource,
			Reason:         item.Reason,
			Score:          item.Score,
		})
	}
	return out
}

func convertRustImpactPaths(items []rustcore.ImpactPathRecord) []ImpactPathRecord {
	out := make([]ImpactPathRecord, 0, len(items))
	for _, item := range items {
		out = append(out, ImpactPathRecord{
			Path:             item.Path,
			Reason:           item.Reason,
			DerivationSource: item.DerivationSource,
			Score:            item.Score,
		})
	}
	return out
}

func normalizeWorkspaceFile(rootPath string, relativePath string) (string, error) {
	normalized := strings.TrimPrefix(strings.TrimSpace(relativePath), "./")
	if normalized == "" {
		return "", fmt.Errorf("path is required")
	}
	root := filepath.Clean(rootPath)
	target := filepath.Clean(filepath.Join(root, normalized))
	rel, err := filepath.Rel(root, target)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes workspace root")
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path explainer expects a file path, not a directory")
	}
	return filepath.ToSlash(rel), nil
}

func inferGoFileRole(path string) string {
	lower := strings.ToLower(path)
	base := filepath.Base(lower)
	switch {
	case base == "readme.md" || base == "agents.md" || strings.HasSuffix(lower, ".md"):
		return "guide"
	case strings.Contains(lower, "test") || strings.HasSuffix(lower, "_test.go") || strings.HasSuffix(lower, ".spec.ts"):
		return "test"
	case strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".toml") || strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml"):
		return "config"
	case strings.Contains(lower, "main.") || strings.Contains(lower, "app."):
		return "entry"
	case strings.HasSuffix(lower, ".py") || strings.HasSuffix(lower, ".go") || strings.HasSuffix(lower, ".rs") || strings.HasSuffix(lower, ".ts") || strings.HasSuffix(lower, ".tsx") || strings.HasSuffix(lower, ".js") || strings.HasSuffix(lower, ".jsx"):
		return "source"
	default:
		return "unknown"
	}
}

func countGoImports(content string) int {
	count := 0
	inBlock := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "import (") || strings.HasPrefix(line, "use ") {
			inBlock = strings.HasPrefix(line, "import (")
			count++
			continue
		}
		if inBlock {
			if line == ")" {
				inBlock = false
				continue
			}
			if line != "" {
				count++
			}
			continue
		}
		if strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "from ") || strings.HasPrefix(line, "require(") {
			count++
		}
	}
	return count
}

func buildGoPathSummary(path string, role string, lineCount int, importCount int, symbols []string) string {
	summary := fmt.Sprintf("%s is a %s file with %d line(s) and %d import/use statement(s).", path, role, lineCount, importCount)
	if len(symbols) > 0 {
		summary += " Rust semantic-core detected symbols: " + strings.Join(symbols[:min(len(symbols), 6)], ", ") + "."
	}
	return summary
}

func buildGoPathHints(path string, role string) []string {
	hints := []string{"Go delivered this path explanation from Rust semantic-core path symbols."}
	if role == "test" {
		hints = append(hints, "Treat this as verification evidence before changing nearby source paths.")
	}
	if strings.Contains(path, "service") || strings.Contains(path, "workspace") {
		hints = append(hints, "Check callers before changing this operational surface.")
	}
	return hints
}

func changePaths(changes []RepoChangeRecord, includeDeleted bool) []string {
	paths := []string{}
	for _, item := range changes {
		if !includeDeleted && item.Status == "deleted" {
			continue
		}
		paths = append(paths, item.Path)
	}
	return dedupeStrings(paths, 128)
}

func impactSummary(paths []string, semanticImpact *rustcore.SemanticImpactReport) string {
	return fmt.Sprintf("Go delivery consumed Rust semantic-core output for %d changed file(s), %d changed symbol(s), %d likely affected file(s), and %d likely affected test(s).", len(paths), len(semanticImpact.ChangedSymbols), len(semanticImpact.LikelyAffectedFiles), len(semanticImpact.LikelyAffectedTests))
}

func buildGoPlanLinks(dataDir string, workspaceID string, referencePaths map[string]struct{}) []RepoContextPlanLink {
	runs, err := listRuns(dataDir, workspaceID)
	if err != nil {
		return []RepoContextPlanLink{}
	}
	out := []RepoContextPlanLink{}
	for _, run := range runs {
		if run.Plan == nil {
			continue
		}
		attached := []string{}
		for _, step := range run.Plan.Steps {
			for _, path := range step.FilesAffected {
				if pathMatchesReference(path, referencePaths) {
					attached = append(attached, path)
				}
			}
		}
		attached = dedupeStrings(attached, 24)
		if len(attached) == 0 {
			continue
		}
		phase := run.Plan.Phase
		out = append(out, RepoContextPlanLink{RunID: run.RunID, IssueID: run.IssueID, Status: run.Status, Phase: &phase, AttachedFiles: attached, Reason: "Run plan references changed or affected paths.", Score: 8 + len(attached)})
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func buildGoActivityLinks(dataDir string, workspaceID string, referencePaths map[string]struct{}) []RepoContextActivityLink {
	activity, err := ListWorkspaceActivity(dataDir, workspaceID, "", "", 80)
	if err != nil {
		return []RepoContextActivityLink{}
	}
	out := []RepoContextActivityLink{}
	for _, item := range activity {
		haystack := strings.ToLower(item.Summary + " " + joinDetails(item.Details))
		if !textMatchesReferences(haystack, referencePaths) {
			continue
		}
		out = append(out, RepoContextActivityLink{Action: item.Action, Summary: item.Summary, IssueID: item.IssueID, RunID: item.RunID, CreatedAt: item.CreatedAt, Reason: "Activity mentions a changed or affected path.", Score: 5})
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func buildGoLatestFix(dataDir string, workspaceID string, referencePaths map[string]struct{}) *RepoContextFixLink {
	fixes, err := loadFixRecords(dataDir, workspaceID, "")
	if err != nil {
		return nil
	}
	for _, fix := range fixes {
		for _, path := range fix.ChangedFiles {
			if !pathMatchesReference(path, referencePaths) {
				continue
			}
			return &RepoContextFixLink{FixID: fix.FixID, IssueID: fix.IssueID, RunID: fix.RunID, Summary: fix.Summary, ChangedFiles: fix.ChangedFiles, TestsRun: fix.TestsRun, RecordedAt: fix.RecordedAt, Reason: "Latest accepted fix touched changed or affected paths."}
		}
	}
	return nil
}

func buildRepoContextLedger(impact *ImpactReport, plans []RepoContextPlanLink, activity []RepoContextActivityLink, fix *RepoContextFixLink) []ContextRetrievalLedgerEntry {
	out := []ContextRetrievalLedgerEntry{}
	for index, change := range impact.ChangedFiles[:min(len(impact.ChangedFiles), 10)] {
		path := change.Path
		out = append(out, ContextRetrievalLedgerEntry{EntryID: fmt.Sprintf("go_repo_context:change:%d:%s", index, shortHash(path)), SourceType: "lexical_hit", SourceID: "change:" + path, Title: change.Status + " " + path, Path: &path, Reason: "Changed file included by Go impact delivery.", Score: 8})
	}
	for index, symbol := range impact.ChangedSymbols[:min(len(impact.ChangedSymbols), 10)] {
		path := symbol.Path
		out = append(out, ContextRetrievalLedgerEntry{EntryID: fmt.Sprintf("go_repo_context:symbol:%d:%s", index, shortHash(symbol.Symbol)), SourceType: "structural_hit", SourceID: "symbol:" + path + ":" + symbol.Symbol, Title: symbol.Kind + " " + symbol.Symbol, Path: &path, Reason: "Changed symbol included by Go impact delivery.", MatchedTerms: []string{strings.ToLower(symbol.Symbol)}, Score: 10})
	}
	for index, plan := range plans[:min(len(plans), 5)] {
		out = append(out, ContextRetrievalLedgerEntry{EntryID: fmt.Sprintf("go_repo_context:plan:%d:%s", index, plan.RunID), SourceType: "artifact", SourceID: "run_plan:" + plan.RunID, Title: "Run plan " + plan.RunID, Reason: plan.Reason, MatchedTerms: plan.AttachedFiles, Score: plan.Score})
	}
	for index, item := range activity[:min(len(activity), 5)] {
		out = append(out, ContextRetrievalLedgerEntry{EntryID: fmt.Sprintf("go_repo_context:activity:%d:%s", index, shortHash(item.Summary)), SourceType: "artifact", SourceID: "activity:" + item.Action, Title: item.Summary, Reason: item.Reason, Score: item.Score})
	}
	if fix != nil {
		out = append(out, ContextRetrievalLedgerEntry{EntryID: "go_repo_context:fix:" + fix.FixID, SourceType: "artifact", SourceID: "fix:" + fix.FixID, Title: fix.Summary, Reason: fix.Reason, MatchedTerms: fix.ChangedFiles, Score: 9})
	}
	return out
}

func queryTokens(query string) []string {
	re := regexp.MustCompile(`[A-Za-z0-9_]{3,}`)
	raw := re.FindAllString(strings.ToLower(query), -1)
	out := []string{}
	stop := map[string]struct{}{"the": {}, "and": {}, "for": {}, "with": {}, "from": {}, "that": {}, "this": {}}
	for _, item := range raw {
		if _, found := stop[item]; !found {
			out = append(out, item)
		}
	}
	deduped := dedupeStrings(out, 16)
	return deduped[:min(len(deduped), 16)]
}

func candidateSourcePaths(rootPath string, limit int) []string {
	out := []string{}
	_ = filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil || len(out) >= limit {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "target" || name == "dist" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if !isTextSource(name) {
			return nil
		}
		rel, err := filepath.Rel(rootPath, path)
		if err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func isTextSource(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".rs", ".md", ".json", ".yaml", ".yml", ".toml":
		return true
	default:
		return false
	}
}

func pushContentHit(rootPath string, relativePath string, tokens []string, push func(RetrievalSearchHit)) {
	content, err := os.ReadFile(filepath.Join(rootPath, filepath.FromSlash(relativePath)))
	if err != nil {
		return
	}
	haystack := strings.ToLower(relativePath + " " + string(content[:min(len(content), 30000)]))
	matched := matchedTerms(haystack, tokens)
	if len(matched) == 0 {
		return
	}
	push(RetrievalSearchHit{Path: relativePath, SourceType: "lexical_hit", Title: relativePath, Reason: "File path or content matched query terms: " + strings.Join(matched[:min(len(matched), 4)], ", ") + ".", MatchedTerms: matched, Score: 4 + len(matched)})
}

func retrievalLedgerFromHits(hits []RetrievalSearchHit) []ContextRetrievalLedgerEntry {
	out := make([]ContextRetrievalLedgerEntry, 0, len(hits))
	for index, hit := range hits {
		path := hit.Path
		out = append(out, ContextRetrievalLedgerEntry{EntryID: fmt.Sprintf("go_retrieval:%d:%s:%s", index, hit.SourceType, shortHash(hit.Title)), SourceType: hit.SourceType, SourceID: hit.SourceType + ":" + hit.Path + ":" + hit.Title, Title: hit.Title, Path: &path, Reason: hit.Reason, MatchedTerms: hit.MatchedTerms, Score: hit.Score})
	}
	return out
}

func matchedTerms(haystack string, tokens []string) []string {
	out := []string{}
	for _, token := range tokens {
		if strings.Contains(haystack, token) {
			out = append(out, token)
		}
	}
	return out
}

func hitPaths(hits []RetrievalSearchHit) []string {
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Path)
	}
	return out
}

func pathMatchesReference(path string, references map[string]struct{}) bool {
	lower := strings.ToLower(path)
	for reference := range references {
		ref := strings.ToLower(reference)
		if lower == ref || strings.Contains(lower, ref) || strings.Contains(ref, lower) {
			return true
		}
	}
	return false
}

func textMatchesReferences(text string, references map[string]struct{}) bool {
	for reference := range references {
		if strings.Contains(text, strings.ToLower(reference)) {
			return true
		}
	}
	return false
}

func joinDetails(details map[string]any) string {
	parts := []string{}
	for key, value := range details {
		parts = append(parts, key, fmt.Sprint(value))
	}
	return strings.Join(parts, " ")
}

func shortHash(value string) string {
	sum := sha1.Sum([]byte(value))
	return fmt.Sprintf("%x", sum)[:8]
}

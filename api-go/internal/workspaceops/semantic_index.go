package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

type SemanticIndexRequest struct {
	Surface  string   `json:"surface"`
	Strategy string   `json:"strategy"`
	Paths    []string `json:"paths"`
	Limit    int      `json:"limit"`
	DSN      *string  `json:"dsn,omitempty"`
	Schema   *string  `json:"schema,omitempty"`
	DryRun   bool     `json:"dry_run"`
}

type SemanticIndexPathSelection struct {
	Path   string  `json:"path"`
	Role   string  `json:"role"`
	Score  int     `json:"score"`
	Reason string  `json:"reason"`
	SHA256 *string `json:"sha256,omitempty"`
}

type SemanticIndexPlan struct {
	WorkspaceID         string                        `json:"workspace_id"`
	RootPath            string                        `json:"root_path"`
	Surface             string                        `json:"surface"`
	Strategy            string                        `json:"strategy"`
	RequestedPaths      []string                      `json:"requested_paths"`
	SelectedPaths       []string                      `json:"selected_paths"`
	SelectedPathDetails []SemanticIndexPathSelection  `json:"selected_path_details"`
	HeadSHA             *string                       `json:"head_sha,omitempty"`
	DirtyFiles          int                           `json:"dirty_files"`
	WorktreeDirty       bool                          `json:"worktree_dirty"`
	IndexFingerprint    *string                       `json:"index_fingerprint,omitempty"`
	PostgresConfigured  bool                          `json:"postgres_configured"`
	PostgresSchema      string                        `json:"postgres_schema"`
	TreeSitterAvailable bool                          `json:"tree_sitter_available"`
	AstGrepAvailable    bool                          `json:"ast_grep_available"`
	RunTargetCount      int                           `json:"run_target_count"`
	VerifyTargetCount   int                           `json:"verify_target_count"`
	RetrievalLedger     []ContextRetrievalLedgerEntry `json:"retrieval_ledger"`
	Blockers            []string                      `json:"blockers"`
	Warnings            []string                      `json:"warnings"`
	NextActions         []string                      `json:"next_actions"`
	CanRun              bool                          `json:"can_run"`
	GeneratedAt         string                        `json:"generated_at"`
}

type SemanticIndexRunResult struct {
	WorkspaceID     string                                          `json:"workspace_id"`
	Surface         string                                          `json:"surface"`
	DryRun          bool                                            `json:"dry_run"`
	Plan            *SemanticIndexPlan                              `json:"plan"`
	Materialization *PostgresWorkspaceSemanticMaterializationResult `json:"materialization,omitempty"`
	Message         string                                          `json:"message"`
	GeneratedAt     string                                          `json:"generated_at"`
}

type SemanticIndexBaselineRecord struct {
	IndexRunID          string                       `json:"index_run_id"`
	WorkspaceID         string                       `json:"workspace_id"`
	Surface             string                       `json:"surface"`
	Strategy            string                       `json:"strategy"`
	IndexFingerprint    string                       `json:"index_fingerprint"`
	HeadSHA             *string                      `json:"head_sha,omitempty"`
	DirtyFiles          int                          `json:"dirty_files"`
	WorktreeDirty       bool                         `json:"worktree_dirty"`
	SelectedPaths       []string                     `json:"selected_paths"`
	SelectedPathDetails []SemanticIndexPathSelection `json:"selected_path_details"`
	MaterializedPaths   []string                     `json:"materialized_paths"`
	FileRows            int                          `json:"file_rows"`
	SymbolRows          int                          `json:"symbol_rows"`
	SummaryRows         int                          `json:"summary_rows"`
	PostgresSchema      string                       `json:"postgres_schema"`
	TreeSitterAvailable bool                         `json:"tree_sitter_available"`
	AstGrepAvailable    bool                         `json:"ast_grep_available"`
	CreatedAt           string                       `json:"created_at"`
}

type SemanticIndexStatus struct {
	WorkspaceID        string                       `json:"workspace_id"`
	Surface            string                       `json:"surface"`
	Status             string                       `json:"status"`
	PostgresConfigured bool                         `json:"postgres_configured"`
	PostgresSchema     string                       `json:"postgres_schema"`
	CurrentFingerprint *string                      `json:"current_fingerprint,omitempty"`
	CurrentHeadSHA     *string                      `json:"current_head_sha,omitempty"`
	CurrentDirtyFiles  int                          `json:"current_dirty_files"`
	Baseline           *SemanticIndexBaselineRecord `json:"baseline,omitempty"`
	FingerprintMatch   bool                         `json:"fingerprint_match"`
	StaleReasons       []string                     `json:"stale_reasons"`
	Warnings           []string                     `json:"warnings"`
	GeneratedAt        string                       `json:"generated_at"`
}

type semanticRepoTarget struct {
	Kind       string
	Label      string
	Command    string
	SourcePath string
	Confidence int
}

func PlanSemanticIndex(dataDir string, workspaceID string, request SemanticIndexRequest) (*SemanticIndexPlan, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	surface, err := normalizeSemanticSurface(request.Surface)
	if err != nil {
		return nil, err
	}
	strategy := semanticStrategyOrDefault(request.Strategy)
	limit := max(1, min(request.Limit, 100))
	blockers := []string{}
	warnings := []string{}
	selectedPaths, pathErr := semanticIndexPaths(dataDir, workspaceID, workspace.RootPath, surface, strategy, request.Paths, limit)
	if pathErr != nil {
		blockers = append(blockers, pathErr.Error())
	}
	targetDSN := strings.TrimSpace(firstConfiguredString(request.DSN, settings.PostgresDSN))
	targetSchema := strings.TrimSpace(firstConfiguredString(request.Schema, &settings.PostgresSchema))
	if targetSchema == "" {
		targetSchema = "xmustard"
	}
	if targetDSN == "" {
		blockers = append(blockers, "Postgres DSN is not configured; run with --dsn or save a Postgres setting before applying.")
	}
	if len(selectedPaths) == 0 {
		blockers = append(blockers, "No semantic index paths were selected.")
	}
	astGrepAvailable := astGrepBinary() != ""
	if !astGrepAvailable {
		warnings = append(warnings, "ast-grep binary is unavailable; symbol indexing can run, but semantic pattern matches remain blocked.")
	}

	runTargets := semanticDiscoverTargets(workspace.RootPath, false)
	verifyTargets := semanticDiscoverTargets(workspace.RootPath, true)
	worktree := readWorktreeStatus(workspace.RootPath)
	selections := make([]SemanticIndexPathSelection, 0, len(selectedPaths))
	for _, item := range selectedPaths {
		selections = append(selections, semanticIndexPathSelection(workspace.RootPath, item, surface))
	}
	var fingerprint *string
	if len(selections) > 0 {
		value := semanticIndexFingerprint(workspaceID, surface, selections, worktree.HeadSHA)
		fingerprint = &value
	}
	if worktree.DirtyFiles > 0 {
		warnings = append(warnings, "Worktree has dirty files; this semantic index baseline should be treated as provisional.")
	}
	retrievalLedger := make([]ContextRetrievalLedgerEntry, 0, len(selections))
	for _, item := range selections {
		pathValue := item.Path
		retrievalLedger = append(retrievalLedger, ContextRetrievalLedgerEntry{
			EntryID:      "semantic_index_path:" + item.Path,
			SourceType:   "semantic_index_path",
			SourceID:     item.Path,
			Title:        item.Path,
			Path:         &pathValue,
			Reason:       item.Reason,
			MatchedTerms: semanticIndexMatchedTerms(item.Path, item.Reason, surface),
			Score:        item.Score,
		})
	}
	nextActions := []string{
		"Run semantic-index run after reviewing selected_paths.",
		"Use --path for exact files when the surface selector is too broad.",
		"Install sg before expecting semantic-search match materialization.",
	}
	if targetDSN == "" {
		nextActions = append([]string{"Configure Postgres or pass --dsn for this run."}, nextActions...)
	}

	return &SemanticIndexPlan{
		WorkspaceID:         workspaceID,
		RootPath:            workspace.RootPath,
		Surface:             surface,
		Strategy:            strategy,
		RequestedPaths:      dedupeOrderedSemanticPaths(request.Paths, limit),
		SelectedPaths:       selectedPaths,
		SelectedPathDetails: selections,
		HeadSHA:             worktree.HeadSHA,
		DirtyFiles:          worktree.DirtyFiles,
		WorktreeDirty:       worktree.DirtyFiles > 0,
		IndexFingerprint:    fingerprint,
		PostgresConfigured:  targetDSN != "",
		PostgresSchema:      targetSchema,
		TreeSitterAvailable: false,
		AstGrepAvailable:    astGrepAvailable,
		RunTargetCount:      len(runTargets),
		VerifyTargetCount:   len(verifyTargets),
		RetrievalLedger:     retrievalLedger,
		Blockers:            dedupeSemanticStrings(blockers),
		Warnings:            dedupeSemanticStrings(warnings),
		NextActions:         nextActions,
		CanRun:              len(blockers) == 0,
		GeneratedAt:         nowUTC(),
	}, nil
}

func RunSemanticIndex(dataDir string, workspaceID string, request SemanticIndexRequest) (*SemanticIndexRunResult, error) {
	plan, err := PlanSemanticIndex(dataDir, workspaceID, request)
	if err != nil {
		return nil, err
	}
	if request.DryRun {
		return &SemanticIndexRunResult{
			WorkspaceID: workspaceID,
			Surface:     plan.Surface,
			DryRun:      true,
			Plan:        plan,
			Message:     fmt.Sprintf("Semantic index dry run selected %d paths for surface '%s'.", len(plan.SelectedPaths), plan.Surface),
			GeneratedAt: nowUTC(),
		}, nil
	}
	if !plan.CanRun {
		return nil, fmt.Errorf("semantic index run is blocked: %s", strings.Join(plan.Blockers, "; "))
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := trimOptional(request.DSN)
	if targetDSN == nil {
		targetDSN = settings.PostgresDSN
	}
	targetSchema := firstConfiguredString(request.Schema, &settings.PostgresSchema)
	if strings.TrimSpace(targetSchema) == "" {
		targetSchema = "xmustard"
	}
	materialization, err := MaterializeWorkspaceSymbolsToPostgres(dataDir, workspaceID, PostgresWorkspaceSemanticMaterializationRequest{
		Strategy:   "paths",
		Paths:      plan.SelectedPaths,
		Limit:      max(1, min(request.Limit, 100)),
		DSN:        targetDSN,
		SchemaName: optionalString(targetSchema),
	})
	if err != nil {
		return nil, err
	}
	if plan.IndexFingerprint != nil {
		workspace, workspaceErr := getWorkspaceRecord(dataDir, workspaceID)
		if workspaceErr != nil {
			return nil, workspaceErr
		}
		baseline := &SemanticIndexBaselineRecord{
			IndexRunID:          "semidx_" + hashID(workspaceID, plan.Surface, *plan.IndexFingerprint, nowUTC()),
			WorkspaceID:         workspaceID,
			Surface:             plan.Surface,
			Strategy:            plan.Strategy,
			IndexFingerprint:    *plan.IndexFingerprint,
			HeadSHA:             plan.HeadSHA,
			DirtyFiles:          plan.DirtyFiles,
			WorktreeDirty:       plan.WorktreeDirty,
			SelectedPaths:       append([]string{}, plan.SelectedPaths...),
			SelectedPathDetails: append([]SemanticIndexPathSelection{}, plan.SelectedPathDetails...),
			MaterializedPaths:   append([]string{}, materialization.MaterializedPaths...),
			FileRows:            materialization.FileRows,
			SymbolRows:          materialization.SymbolRows,
			SummaryRows:         materialization.SummaryRows,
			PostgresSchema:      targetSchema,
			TreeSitterAvailable: plan.TreeSitterAvailable,
			AstGrepAvailable:    plan.AstGrepAvailable,
			CreatedAt:           nowUTC(),
		}
		if _, err := persistSemanticIndexBaseline(
			targetDSNValue(targetDSN),
			targetSchema,
			baseline,
			workspace.Name,
			workspace.RootPath,
		); err != nil {
			return nil, err
		}
		if err := appendWorkspaceSemanticActivity(
			dataDir,
			workspaceID,
			"semantic_index.baseline.persist",
			"Persisted semantic index baseline for surface '"+plan.Surface+"'",
			map[string]any{
				"index_run_id":       baseline.IndexRunID,
				"surface":            baseline.Surface,
				"strategy":           baseline.Strategy,
				"index_fingerprint":  baseline.IndexFingerprint,
				"materialized_paths": baseline.MaterializedPaths,
				"symbol_rows":        baseline.SymbolRows,
				"summary_rows":       baseline.SummaryRows,
				"schema_name":        baseline.PostgresSchema,
			},
		); err != nil {
			return nil, err
		}
	}
	return &SemanticIndexRunResult{
		WorkspaceID:     workspaceID,
		Surface:         plan.Surface,
		DryRun:          false,
		Plan:            plan,
		Materialization: materialization,
		Message:         fmt.Sprintf("Semantic index materialized %d paths for surface '%s'.", len(materialization.MaterializedPaths), plan.Surface),
		GeneratedAt:     nowUTC(),
	}, nil
}

func ReadSemanticIndexStatus(dataDir string, workspaceID string, request SemanticIndexRequest) (*SemanticIndexStatus, error) {
	plan, err := PlanSemanticIndex(dataDir, workspaceID, request)
	if err != nil {
		return nil, err
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	targetDSN := strings.TrimSpace(firstConfiguredString(request.DSN, settings.PostgresDSN))
	targetSchema := strings.TrimSpace(firstConfiguredString(request.Schema, &settings.PostgresSchema))
	if targetSchema == "" {
		targetSchema = "xmustard"
	}
	if targetDSN == "" {
		return &SemanticIndexStatus{
			WorkspaceID:        workspaceID,
			Surface:            plan.Surface,
			Status:             "blocked",
			PostgresConfigured: false,
			PostgresSchema:     targetSchema,
			CurrentFingerprint: plan.IndexFingerprint,
			CurrentHeadSHA:     plan.HeadSHA,
			CurrentDirtyFiles:  plan.DirtyFiles,
			StaleReasons:       []string{"Postgres DSN is not configured; no semantic baseline can be read."},
			Warnings:           append([]string{}, plan.Warnings...),
			GeneratedAt:        nowUTC(),
		}, nil
	}
	baseline, err := readLatestSemanticIndexBaseline(targetDSN, targetSchema, workspaceID, plan.Surface, plan.Strategy)
	if err != nil {
		return &SemanticIndexStatus{
			WorkspaceID:        workspaceID,
			Surface:            plan.Surface,
			Status:             "blocked",
			PostgresConfigured: true,
			PostgresSchema:     targetSchema,
			CurrentFingerprint: plan.IndexFingerprint,
			CurrentHeadSHA:     plan.HeadSHA,
			CurrentDirtyFiles:  plan.DirtyFiles,
			StaleReasons:       []string{err.Error()},
			Warnings:           append([]string{}, plan.Warnings...),
			GeneratedAt:        nowUTC(),
		}, nil
	}
	if baseline == nil {
		return &SemanticIndexStatus{
			WorkspaceID:        workspaceID,
			Surface:            plan.Surface,
			Status:             "no_baseline",
			PostgresConfigured: true,
			PostgresSchema:     targetSchema,
			CurrentFingerprint: plan.IndexFingerprint,
			CurrentHeadSHA:     plan.HeadSHA,
			CurrentDirtyFiles:  plan.DirtyFiles,
			StaleReasons:       []string{"No stored semantic index baseline exists for this workspace and surface."},
			Warnings:           append([]string{}, plan.Warnings...),
			GeneratedAt:        nowUTC(),
		}, nil
	}
	fingerprintMatch := plan.IndexFingerprint != nil && *plan.IndexFingerprint == baseline.IndexFingerprint
	semanticInputsMatch := fingerprintMatch || semanticBaselineCoversPlan(plan, baseline)
	staleReasons := []string{}
	if !strings.EqualFold(firstNonEmptyPtr(plan.HeadSHA), firstNonEmptyPtr(baseline.HeadSHA)) {
		staleReasons = append(staleReasons, "HEAD SHA differs from the stored semantic baseline.")
	}
	if !semanticInputsMatch {
		staleReasons = append(staleReasons, "Selected path hashes or baseline inputs differ from the stored semantic baseline.")
	}
	if plan.DirtyFiles > 0 {
		staleReasons = append(staleReasons, "Worktree has dirty files, so freshness is provisional until the tree is clean or re-indexed.")
	}
	status := "stale"
	if semanticInputsMatch && plan.DirtyFiles == 0 {
		status = "fresh"
	}
	if semanticInputsMatch && plan.DirtyFiles > 0 {
		status = "dirty_provisional"
	}
	return &SemanticIndexStatus{
		WorkspaceID:        workspaceID,
		Surface:            plan.Surface,
		Status:             status,
		PostgresConfigured: true,
		PostgresSchema:     targetSchema,
		CurrentFingerprint: plan.IndexFingerprint,
		CurrentHeadSHA:     plan.HeadSHA,
		CurrentDirtyFiles:  plan.DirtyFiles,
		Baseline:           baseline,
		FingerprintMatch:   semanticInputsMatch,
		StaleReasons:       dedupeSemanticStrings(staleReasons),
		Warnings:           append([]string{}, plan.Warnings...),
		GeneratedAt:        nowUTC(),
	}, nil
}

func semanticIndexPaths(dataDir string, workspaceID string, repoRoot string, surface string, strategy string, paths []string, limit int) ([]string, error) {
	if strategy == "paths" {
		return dedupeOrderedSemanticPaths(paths, max(1, min(limit, 100))), nil
	}
	repoMap, err := loadOrBuildRepoMap(dataDir, workspaceID, repoRoot)
	if err != nil {
		return nil, err
	}
	prioritized := []string{}
	for _, item := range repoMap.KeyFiles {
		if item.Role == "test" {
			continue
		}
		if !semanticMaterializationIsIndexableSource(item.Path) {
			continue
		}
		prioritized = append(prioritized, item.Path)
	}
	prioritized = append(prioritized, semanticIndexTargetSeeds(repoRoot, workspaceID, surface)...)
	prioritized = append(prioritized, semanticMaterializationSourceCandidates(repoRoot, max(limit*6, 40))...)
	ranked := []string{}
	for _, item := range dedupeOrderedSemanticPaths(prioritized, max(len(prioritized), limit*8)) {
		if !semanticMaterializationIsIndexableSource(item) {
			continue
		}
		if info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(item))); err != nil || info.IsDir() {
			continue
		}
		ranked = append(ranked, item)
	}
	slices.SortFunc(ranked, func(a, b string) int {
		left := semanticMaterializationPathScoreForSurface(a, surface)
		right := semanticMaterializationPathScoreForSurface(b, surface)
		if left == right {
			if a < b {
				return -1
			}
			if a > b {
				return 1
			}
			return 0
		}
		if left > right {
			return -1
		}
		return 1
	})
	return ranked[:min(len(ranked), limit)], nil
}

func normalizeSemanticSurface(surface string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(surface))
	if normalized == "" {
		normalized = "cli"
	}
	if normalized != "cli" && normalized != "web" && normalized != "all" {
		return "", fmt.Errorf("unknown semantic index surface: %s", surface)
	}
	return normalized, nil
}

func semanticIndexTargetSeeds(repoRoot string, workspaceID string, surface string) []string {
	if surface == "web" {
		return []string{}
	}
	seeds := []string{}
	for _, target := range append(semanticDiscoverTargets(repoRoot, false), semanticDiscoverTargets(repoRoot, true)...) {
		if target.SourcePath != "" {
			seeds = append(seeds, target.SourcePath)
		}
		command := strings.ToLower(target.Command)
		if strings.Contains(command, "python") {
			seeds = append(seeds, "main.py", "cli.py", "__main__.py")
		}
		if strings.Contains(command, "typer") || strings.Contains(command, "click") {
			seeds = append(seeds, "cli.py", "commands.py")
		}
	}
	return seeds
}

func semanticDiscoverTargets(repoRoot string, includeVerify bool) []semanticRepoTarget {
	targets := []semanticRepoTarget{}
	targets = append(targets, semanticDiscoverMakeTargets(repoRoot, includeVerify)...)
	targets = append(targets, semanticDiscoverPackageTargets(repoRoot, includeVerify)...)
	targets = append(targets, semanticDiscoverDockerTargets(repoRoot)...)
	return dedupeSemanticTargets(targets)
}

func semanticDiscoverMakeTargets(repoRoot string, includeVerify bool) []semanticRepoTarget {
	makefile := filepath.Join(repoRoot, "Makefile")
	content, err := os.ReadFile(makefile)
	if err != nil {
		return []semanticRepoTarget{}
	}
	targets := []semanticRepoTarget{}
	pattern := regexp.MustCompile(`^([A-Za-z0-9_.-]+):`)
	for _, raw := range strings.Split(string(content), "\n") {
		match := pattern.FindStringSubmatch(raw)
		if len(match) < 2 {
			continue
		}
		name := match[1]
		if strings.HasPrefix(name, ".") {
			continue
		}
		kind := categorizeSemanticTargetName(name)
		if kind == "verify" && !includeVerify {
			continue
		}
		if !includeVerify && (kind == "test" || kind == "lint" || kind == "verify") {
			continue
		}
		if includeVerify && kind != "test" && kind != "lint" && kind != "verify" && kind != "build" {
			continue
		}
		targets = append(targets, semanticRepoTarget{
			Kind:       kind,
			Label:      "make " + name,
			Command:    "make " + name,
			SourcePath: "Makefile",
			Confidence: 80,
		})
	}
	return targets
}

func semanticDiscoverPackageTargets(repoRoot string, includeVerify bool) []semanticRepoTarget {
	type packagePayload struct {
		Scripts map[string]any `json:"scripts"`
	}
	targets := []semanticRepoTarget{}
	for _, manifest := range candidatePackageJSONFiles(repoRoot) {
		content, err := os.ReadFile(manifest)
		if err != nil {
			continue
		}
		var payload packagePayload
		if err := json.Unmarshal(content, &payload); err != nil {
			continue
		}
		prefix := filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(filepath.Dir(manifest), repoRoot), string(filepath.Separator)))
		runPrefix := ""
		if prefix != "" && prefix != "." {
			runPrefix = "cd " + prefix + " && "
		}
		for name, rawCommand := range payload.Scripts {
			command, ok := rawCommand.(string)
			if !ok {
				continue
			}
			kind := categorizeSemanticTargetName(name)
			if kind == "verify" && !includeVerify {
				continue
			}
			if !includeVerify && (kind == "test" || kind == "lint" || kind == "verify") {
				continue
			}
			if includeVerify && kind != "test" && kind != "lint" && kind != "verify" && kind != "build" {
				continue
			}
			labelPrefix := prefix
			if labelPrefix == "" || labelPrefix == "." {
				labelPrefix = filepath.Base(manifest)
			}
			targets = append(targets, semanticRepoTarget{
				Kind:       kind,
				Label:      labelPrefix + ":" + name,
				Command:    runPrefix + "npm run " + name,
				SourcePath: filepath.ToSlash(strings.TrimPrefix(manifest, repoRoot+string(filepath.Separator))),
				Confidence: 85,
			})
			_ = command
		}
	}
	return targets
}

func semanticDiscoverDockerTargets(repoRoot string) []semanticRepoTarget {
	targets := []semanticRepoTarget{}
	for _, candidate := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		path := filepath.Join(repoRoot, candidate)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		targets = append(targets, semanticRepoTarget{
			Kind:       "service",
			Label:      "docker compose up (" + candidate + ")",
			Command:    "docker compose -f " + candidate + " up",
			SourcePath: candidate,
			Confidence: 75,
		})
	}
	return targets
}

func candidatePackageJSONFiles(repoRoot string) []string {
	candidates := []string{}
	queue := []string{repoRoot}
	maxDepth := 2
	excluded := map[string]struct{}{
		".git": {}, "node_modules": {}, "dist": {}, "build": {}, "coverage": {}, "research": {}, "__pycache__": {}, ".venv": {}, "venv": {},
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		relative, _ := filepath.Rel(repoRoot, current)
		depth := 0
		if relative != "." {
			depth = len(strings.Split(filepath.ToSlash(relative), "/"))
		}
		packageJSON := filepath.Join(current, "package.json")
		if _, err := os.Stat(packageJSON); err == nil {
			candidates = append(candidates, packageJSON)
		}
		if depth >= maxDepth {
			continue
		}
		entries, err := os.ReadDir(current)
		if err != nil {
			continue
		}
		slices.SortFunc(entries, func(a, b os.DirEntry) int {
			if a.Name() < b.Name() {
				return -1
			}
			if a.Name() > b.Name() {
				return 1
			}
			return 0
		})
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, skip := excluded[entry.Name()]; skip {
				continue
			}
			queue = append(queue, filepath.Join(current, entry.Name()))
		}
	}
	return candidates
}

func dedupeSemanticTargets(targets []semanticRepoTarget) []semanticRepoTarget {
	deduped := map[string]semanticRepoTarget{}
	order := []string{}
	for _, target := range targets {
		key := target.Kind + "|" + target.Command
		existing, ok := deduped[key]
		if !ok {
			deduped[key] = target
			order = append(order, key)
			continue
		}
		if target.Confidence > existing.Confidence {
			deduped[key] = target
		}
	}
	out := make([]semanticRepoTarget, 0, len(order))
	for _, key := range order {
		out = append(out, deduped[key])
	}
	return out
}

func categorizeSemanticTargetName(name string) string {
	lowered := strings.ToLower(name)
	switch {
	case strings.Contains(lowered, "test") || strings.Contains(lowered, "pytest") || strings.Contains(lowered, "spec") || strings.Contains(lowered, "check"):
		return "test"
	case strings.Contains(lowered, "lint") || strings.Contains(lowered, "format") || lowered == "fmt":
		return "lint"
	case strings.Contains(lowered, "build") || strings.Contains(lowered, "compile"):
		return "build"
	case strings.Contains(lowered, "dev") || strings.Contains(lowered, "serve") || strings.Contains(lowered, "start") || strings.Contains(lowered, "run") || strings.Contains(lowered, "backend") || strings.Contains(lowered, "frontend"):
		return "dev"
	case strings.Contains(lowered, "verify") || strings.Contains(lowered, "validate"):
		return "verify"
	default:
		return "other"
	}
}

func semanticMaterializationPathScoreForSurface(relativePath string, surface string) int {
	score := semanticMaterializationPathScore(relativePath)
	lowered := strings.ToLower(relativePath)
	if (surface == "cli" || surface == "all") && (strings.Contains(lowered, "cli") || strings.Contains(lowered, "terminal") || strings.Contains(lowered, "runtime") || strings.Contains(lowered, "server") || strings.Contains(lowered, "agent") || strings.Contains(lowered, "command")) {
		score += 12
	}
	if surface == "web" && (strings.Contains(lowered, "frontend/") || strings.Contains(lowered, "web/") || strings.Contains(lowered, "ui/") || strings.Contains(lowered, "src/app") || strings.Contains(lowered, "src/pages") || strings.Contains(lowered, "src/components")) {
		score += 32
	}
	return score
}

func semanticIndexPathSelection(repoRoot string, relativePath string, surface string) SemanticIndexPathSelection {
	var shaValue *string
	if content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relativePath))); err == nil {
		sum := sha256.Sum256(content)
		value := hex.EncodeToString(sum[:])
		shaValue = &value
	}
	role := inferGoFileRole(relativePath)
	score := semanticMaterializationPathScoreForSurface(relativePath, surface)
	reasons := []string{role + " file", fmt.Sprintf("score %d", score)}
	lowered := strings.ToLower(relativePath)
	if (surface == "cli" || surface == "all") && (strings.Contains(lowered, "cli") || strings.Contains(lowered, "runtime") || strings.Contains(lowered, "server") || strings.Contains(lowered, "agent") || strings.Contains(lowered, "command") || strings.Contains(lowered, "terminal")) {
		reasons = append(reasons, "matches CLI/runtime surface markers")
	}
	if surface == "web" && (strings.Contains(lowered, "frontend/") || strings.Contains(lowered, "web/") || strings.Contains(lowered, "ui/") || strings.Contains(lowered, "src/app") || strings.Contains(lowered, "src/pages") || strings.Contains(lowered, "src/components")) {
		reasons = append(reasons, "matches web surface markers")
	}
	if strings.Contains(lowered, ".test.") || strings.Contains(lowered, ".spec.") || strings.Contains("/"+lowered+"/", "/tests/") {
		reasons = append(reasons, "test-like path received a ranking penalty")
	}
	return SemanticIndexPathSelection{
		Path:   relativePath,
		Role:   role,
		Score:  score,
		Reason: strings.Join(reasons, "; "),
		SHA256: shaValue,
	}
}

func semanticIndexMatchedTerms(relativePath string, reason string, surface string) []string {
	haystack := strings.ToLower(relativePath + " " + reason)
	terms := []string{"cli", "runtime", "server", "agent", "command", "test", "config", "entry", "source"}
	if surface == "web" {
		terms = append(terms, "frontend", "web", "ui")
	}
	matched := []string{}
	for _, term := range terms {
		if strings.Contains(haystack, term) {
			matched = append(matched, term)
		}
		if len(matched) >= 6 {
			break
		}
	}
	return matched
}

func semanticIndexFingerprint(workspaceID string, surface string, selections []SemanticIndexPathSelection, headSHA *string) string {
	type fingerprintPath struct {
		Path   string  `json:"path"`
		SHA256 *string `json:"sha256,omitempty"`
	}
	payload := struct {
		WorkspaceID string            `json:"workspace_id"`
		Surface     string            `json:"surface"`
		HeadSHA     *string           `json:"head_sha,omitempty"`
		Paths       []fingerprintPath `json:"paths"`
	}{
		WorkspaceID: workspaceID,
		Surface:     surface,
		HeadSHA:     headSHA,
		Paths:       []fingerprintPath{},
	}
	for _, item := range selections {
		payload.Paths = append(payload.Paths, fingerprintPath{Path: item.Path, SHA256: item.SHA256})
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func semanticBaselineCoversPlan(plan *SemanticIndexPlan, baseline *SemanticIndexBaselineRecord) bool {
	if baseline == nil || !strings.EqualFold(firstNonEmptyPtr(plan.HeadSHA), firstNonEmptyPtr(baseline.HeadSHA)) {
		return false
	}
	if len(plan.SelectedPathDetails) == 0 {
		return false
	}
	baselineHashes := map[string]string{}
	for _, item := range baseline.SelectedPathDetails {
		if item.SHA256 != nil {
			baselineHashes[item.Path] = *item.SHA256
		}
	}
	if len(baselineHashes) == 0 {
		return false
	}
	for _, item := range plan.SelectedPathDetails {
		if item.SHA256 == nil || baselineHashes[item.Path] != *item.SHA256 {
			return false
		}
	}
	return true
}

func persistSemanticIndexBaseline(dsn string, schema string, baseline *SemanticIndexBaselineRecord, workspaceName string, workspaceRoot string) (*SemanticIndexBaselineRecord, error) {
	normalizedSchema, err := validatePostgresSchemaName(schema)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("Postgres DSN is required for semantic index baseline persistence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	connection, err := connectSemanticPostgres(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect Postgres: %w", err)
	}
	defer connection.Close(context.Background())
	if err := upsertSemanticIndexWorkspace(ctx, connection, normalizedSchema, baseline.WorkspaceID, workspaceName, workspaceRoot); err != nil {
		return nil, err
	}
	if _, err := connection.Exec(
		ctx,
		fmt.Sprintf(`
			insert into %s.semantic_index_runs (
				index_run_id, workspace_id, surface, strategy, index_fingerprint, head_sha, dirty_files,
				worktree_dirty, selected_paths_json, selected_path_details_json, materialized_paths_json,
				file_rows, symbol_rows, summary_rows, postgres_schema, tree_sitter_available, ast_grep_available
			) values ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, $12, $13, $14, $15, $16, $17)
		`, normalizedSchema),
		baseline.IndexRunID,
		baseline.WorkspaceID,
		baseline.Surface,
		baseline.Strategy,
		baseline.IndexFingerprint,
		baseline.HeadSHA,
		baseline.DirtyFiles,
		baseline.WorktreeDirty,
		mustSemanticJSON(baseline.SelectedPaths),
		mustSemanticJSON(baseline.SelectedPathDetails),
		mustSemanticJSON(baseline.MaterializedPaths),
		baseline.FileRows,
		baseline.SymbolRows,
		baseline.SummaryRows,
		baseline.PostgresSchema,
		baseline.TreeSitterAvailable,
		baseline.AstGrepAvailable,
	); err != nil {
		return nil, fmt.Errorf("persist semantic index baseline: %w", err)
	}
	return baseline, nil
}

func readLatestSemanticIndexBaseline(dsn string, schema string, workspaceID string, surface string, strategy string) (*SemanticIndexBaselineRecord, error) {
	normalizedSchema, err := validatePostgresSchemaName(schema)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("Postgres DSN is required for semantic index baseline reads")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := connectSemanticPostgres(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect Postgres: %w", err)
	}
	defer connection.Close(context.Background())
	var (
		indexRunID              string
		rowWorkspaceID          string
		rowSurface              string
		rowStrategy             string
		indexFingerprint        string
		headSHA                 *string
		dirtyFiles              int
		worktreeDirty           bool
		selectedPathsJSON       []byte
		selectedPathDetailsJSON []byte
		materializedPathsJSON   []byte
		fileRows                int
		symbolRows              int
		summaryRows             int
		postgresSchema          string
		treeSitterAvailable     bool
		astGrepAvailable        bool
		createdAt               string
	)
	query := fmt.Sprintf(`
		select index_run_id, workspace_id, surface, strategy, index_fingerprint, head_sha, dirty_files,
			worktree_dirty, selected_paths_json, selected_path_details_json, materialized_paths_json,
			file_rows, symbol_rows, summary_rows, postgres_schema, tree_sitter_available, ast_grep_available,
			to_char(created_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
		from %s.semantic_index_runs
		where workspace_id = $1 and surface = $2 and strategy = $3
		order by created_at desc
		limit 1
	`, normalizedSchema)
	err = connection.QueryRow(ctx, query, workspaceID, surface, strategy).Scan(
		&indexRunID,
		&rowWorkspaceID,
		&rowSurface,
		&rowStrategy,
		&indexFingerprint,
		&headSHA,
		&dirtyFiles,
		&worktreeDirty,
		&selectedPathsJSON,
		&selectedPathDetailsJSON,
		&materializedPathsJSON,
		&fileRows,
		&symbolRows,
		&summaryRows,
		&postgresSchema,
		&treeSitterAvailable,
		&astGrepAvailable,
		&createdAt,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no rows") {
			return nil, nil
		}
		return nil, fmt.Errorf("read semantic index baseline: %w", err)
	}
	selectedPaths := []string{}
	if len(selectedPathsJSON) > 0 {
		_ = json.Unmarshal(selectedPathsJSON, &selectedPaths)
	}
	selectedPathDetails := []SemanticIndexPathSelection{}
	if len(selectedPathDetailsJSON) > 0 {
		_ = json.Unmarshal(selectedPathDetailsJSON, &selectedPathDetails)
	}
	materializedPaths := []string{}
	if len(materializedPathsJSON) > 0 {
		_ = json.Unmarshal(materializedPathsJSON, &materializedPaths)
	}
	return &SemanticIndexBaselineRecord{
		IndexRunID:          indexRunID,
		WorkspaceID:         rowWorkspaceID,
		Surface:             normalizeSemanticSurfaceValue(rowSurface),
		Strategy:            semanticStrategyOrDefault(rowStrategy),
		IndexFingerprint:    indexFingerprint,
		HeadSHA:             headSHA,
		DirtyFiles:          dirtyFiles,
		WorktreeDirty:       worktreeDirty,
		SelectedPaths:       selectedPaths,
		SelectedPathDetails: selectedPathDetails,
		MaterializedPaths:   materializedPaths,
		FileRows:            fileRows,
		SymbolRows:          symbolRows,
		SummaryRows:         summaryRows,
		PostgresSchema:      defaultString(postgresSchema, "xmustard"),
		TreeSitterAvailable: treeSitterAvailable,
		AstGrepAvailable:    astGrepAvailable,
		CreatedAt:           createdAt,
	}, nil
}

func upsertSemanticIndexWorkspace(ctx context.Context, connection semanticMaterializationConn, schema string, workspaceID string, workspaceName string, workspaceRoot string) error {
	if _, err := connection.Exec(
		ctx,
		fmt.Sprintf(`
			insert into %s.workspaces (workspace_id, name, root_path)
			values ($1, $2, $3)
			on conflict (workspace_id) do update set
				name = excluded.name,
				root_path = excluded.root_path,
				updated_at = now()
		`, schema),
		workspaceID,
		workspaceName,
		workspaceRoot,
	); err != nil {
		return fmt.Errorf("upsert workspace: %w", err)
	}
	return nil
}

func targetDSNValue(dsn *string) string {
	if dsn == nil {
		return ""
	}
	return strings.TrimSpace(*dsn)
}

func normalizeSemanticSurfaceValue(surface string) string {
	normalized, err := normalizeSemanticSurface(surface)
	if err != nil {
		return "cli"
	}
	return normalized
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func dedupeSemanticStrings(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func mustSemanticJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

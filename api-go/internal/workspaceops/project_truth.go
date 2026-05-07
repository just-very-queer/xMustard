package workspaceops

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"xmustard/api-go/internal/rustcore"
)

const guidancePlaceholderMarker = "TODO(xmustard)"

type RepoToolState struct {
	Workspace        workspaceRecord          `json:"workspace"`
	SnapshotSummary  map[string]int           `json:"snapshot_summary"`
	Worktree         WorktreeStatus           `json:"worktree"`
	RepoMap          *rustcore.RepoMapSummary `json:"repo_map,omitempty"`
	ActivityOverview *activityOverview        `json:"activity_overview,omitempty"`
	RecentActivity   []activityRecord         `json:"recent_activity"`
	RepoConfigHealth *RepoConfigHealth        `json:"repo_config_health,omitempty"`
	GuidanceHealth   *RepoGuidanceHealth      `json:"guidance_health,omitempty"`
	GeneratedAt      string                   `json:"generated_at"`
}

type GuidanceStarterRecord struct {
	TemplateID  string `json:"template_id"`
	Title       string `json:"title"`
	Path        string `json:"path"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended"`
	Exists      bool   `json:"exists"`
	Stale       bool   `json:"stale"`
}

type RepoGuidanceHealth struct {
	WorkspaceID      string                  `json:"workspace_id"`
	Status           string                  `json:"status"`
	Summary          string                  `json:"summary"`
	GuidanceCount    int                     `json:"guidance_count"`
	AlwaysOnCount    int                     `json:"always_on_count"`
	InstructionCount int                     `json:"instruction_count"`
	PresentFiles     []string                `json:"present_files"`
	MissingFiles     []string                `json:"missing_files"`
	StaleFiles       []string                `json:"stale_files"`
	RecommendedFiles []string                `json:"recommended_files"`
	Starters         []GuidanceStarterRecord `json:"starters"`
	GeneratedAt      string                  `json:"generated_at"`
}

type IngestionDependencyRecord struct {
	DependencyID string  `json:"dependency_id"`
	Kind         string  `json:"kind"`
	Label        string  `json:"label"`
	Satisfied    bool    `json:"satisfied"`
	Detail       *string `json:"detail,omitempty"`
}

type IngestionPhaseRecord struct {
	PhaseID             string                      `json:"phase_id"`
	Label               string                      `json:"label"`
	Description         string                      `json:"description"`
	ImplementationState string                      `json:"implementation_state"`
	DeliveryState       string                      `json:"delivery_state"`
	Dependencies        []IngestionDependencyRecord `json:"dependencies"`
	Blockers            []string                    `json:"blockers"`
	Outputs             []string                    `json:"outputs"`
	Evidence            []string                    `json:"evidence"`
}

type IngestionPipelinePlan struct {
	WorkspaceID         string                 `json:"workspace_id"`
	RootPath            string                 `json:"root_path"`
	PostgresConfigured  bool                   `json:"postgres_configured"`
	PostgresSchema      string                 `json:"postgres_schema"`
	Phases              []IngestionPhaseRecord `json:"phases"`
	CompletedPhaseCount int                    `json:"completed_phase_count"`
	ReadyPhaseIDs       []string               `json:"ready_phase_ids"`
	BlockedPhaseIDs     []string               `json:"blocked_phase_ids"`
	NextPhaseID         *string                `json:"next_phase_id,omitempty"`
	GeneratedAt         string                 `json:"generated_at"`
}

func ReadRepoToolState(dataDir string, workspaceID string) (*RepoToolState, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	var snapshot *workspaceSnapshot
	if loaded, loadErr := loadSnapshot(dataDir, workspaceID); loadErr == nil {
		snapshot = loaded
	} else if !os.IsNotExist(loadErr) {
		return nil, loadErr
	}

	worktree, err := ReadWorktreeStatus(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	repoState := &RepoToolState{
		Workspace:       workspace,
		SnapshotSummary: map[string]int{},
		Worktree: WorktreeStatus{
			DirtyPaths: []string{},
		},
		RecentActivity: []activityRecord{},
		GeneratedAt:    nowUTC(),
	}
	if worktree != nil {
		repoState.Worktree = *worktree
	}
	if snapshot == nil {
		return repoState, nil
	}

	repoState.SnapshotSummary = cloneSummary(snapshot.Summary)

	repoMap, err := loadRepoMapIfPresent(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	repoState.RepoMap = repoMap

	activityOverview, err := ReadActivityOverview(dataDir, workspaceID, 200)
	if err != nil {
		return nil, err
	}
	repoState.ActivityOverview = activityOverview

	recentActivity, err := ListWorkspaceActivity(dataDir, workspaceID, "", "", 10)
	if err != nil {
		return nil, err
	}
	repoState.RecentActivity = recentActivity

	repoConfigHealth, err := GetWorkspaceRepoConfigHealth(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	repoState.RepoConfigHealth = repoConfigHealth

	guidanceHealth, err := GetWorkspaceGuidanceHealth(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	repoState.GuidanceHealth = guidanceHealth

	return repoState, nil
}

func ReadIngestionPlan(dataDir string, workspaceID string) (*IngestionPipelinePlan, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	var snapshot *workspaceSnapshot
	if loaded, loadErr := loadSnapshot(dataDir, workspaceID); loadErr == nil {
		snapshot = loaded
	} else if !os.IsNotExist(loadErr) {
		return nil, loadErr
	}

	repoMap, err := loadRepoMapIfPresent(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	settings, err := GetSettings(dataDir)
	if err != nil {
		return nil, err
	}

	postgresConfigured := strings.TrimSpace(firstNonEmptyPtr(settings.PostgresDSN)) != ""
	postgresSchema := fallbackString(strings.TrimSpace(settings.PostgresSchema), "xmustard")
	treeSitterRuntimeAvailable := cargoAvailable()
	astGrepIsAvailable := astGrepAvailable()
	treeSitterOnDemandAvailable := treeSitterRuntimeAvailable && repoMap != nil
	astGrepOnDemandAvailable := repoMap != nil

	runTargets := []RepoTargetRecord{}
	verifyTargets := []RepoTargetRecord{}
	if snapshot != nil {
		runTargets, err = ReadRunTargets(dataDir, workspaceID)
		if err != nil {
			return nil, err
		}
		verifyTargets, err = ReadVerifyTargets(dataDir, workspaceID)
		if err != nil {
			return nil, err
		}
	}

	phases := []IngestionPhaseRecord{}

	repoScanDependencies := []IngestionDependencyRecord{
		newIngestionDependency(
			"workspace_loaded",
			"workspace",
			"Workspace snapshot exists",
			snapshot != nil,
			"A scanned workspace snapshot is required before semantic ingestion can build on repo state.",
		),
	}
	phases = append(phases, IngestionPhaseRecord{
		PhaseID:             "repo_scan",
		Label:               "Repository scan",
		Description:         "Load the workspace snapshot, issue records, signals, worktree, and high-level tree summary.",
		ImplementationState: "implemented",
		DeliveryState:       conditionalString(snapshot != nil, "complete", "blocked"),
		Dependencies:        repoScanDependencies,
		Blockers:            conditionalStrings(snapshot == nil, "Run a workspace scan before semantic ingestion phases can build on repo state."),
		Outputs:             []string{"workspace snapshot", "issue inventory", "signal inventory", "worktree status"},
		Evidence: []string{
			conditionalString(snapshot != nil, "issues_total="+phaseIntString(snapshot.Summary["issues_total"]), "snapshot missing"),
			conditionalString(snapshot != nil, "scanner_version="+phaseIntString(snapshot.ScannerVersion), "scanner unavailable"),
		},
	})

	repoMapDependencies := []IngestionDependencyRecord{
		newIngestionDependency(
			"repo_scan_output",
			"artifact",
			"Repo scan output",
			snapshot != nil,
			"Repo map generation depends on a scanned workspace snapshot.",
		),
	}
	phases = append(phases, IngestionPhaseRecord{
		PhaseID:             "repo_map",
		Label:               "Repo map",
		Description:         "Materialize key files, top directories, and source-aware repo shape for downstream indexing.",
		ImplementationState: "implemented",
		DeliveryState:       conditionalString(repoMap != nil, "complete", "blocked"),
		Dependencies:        repoMapDependencies,
		Blockers:            conditionalStrings(repoMap == nil, "Repo map has not been generated for this workspace yet."),
		Outputs:             []string{"key files", "top directories", "source extension counts"},
		Evidence: []string{
			conditionalString(repoMap != nil, "total_files="+phaseIntString(repoMap.TotalFiles), "repo map missing"),
			conditionalString(repoMap != nil, "source_files="+phaseIntString(repoMap.SourceFiles), "source totals unavailable"),
		},
	})

	runtimeDependencies := []IngestionDependencyRecord{
		newIngestionDependency(
			"repo_scan_for_targets",
			"artifact",
			"Workspace scan",
			snapshot != nil,
			"Run and verify target discovery is attached to the scanned workspace state.",
		),
	}
	phases = append(phases, IngestionPhaseRecord{
		PhaseID:             "runtime_discovery",
		Label:               "Runtime discovery",
		Description:         "Discover run, build, test, lint, and verification targets that explain how to execute the repo.",
		ImplementationState: "implemented",
		DeliveryState:       conditionalString(snapshot != nil, "complete", "blocked"),
		Dependencies:        runtimeDependencies,
		Blockers:            conditionalStrings(snapshot == nil, "Workspace scan must complete before runtime discovery can report targets."),
		Outputs:             []string{"run targets", "verify targets"},
		Evidence: []string{
			"run_targets=" + phaseIntString(len(runTargets)),
			"verify_targets=" + phaseIntString(len(verifyTargets)),
		},
	})

	treeSitterDependencies := []IngestionDependencyRecord{
		newIngestionDependency(
			"repo_map_available",
			"artifact",
			"Repo map available",
			repoMap != nil,
			"Parser-backed indexing should start from the cleaned repo-map boundary.",
		),
		newIngestionDependency(
			"tree_sitter_on_demand_surface",
			"implementation",
			"On-demand symbol extraction surface implemented",
			true,
			"path-symbols, code-explainer, and issue-context symbol ranking already use parser-backed extraction when the runtime is available.",
		),
		newIngestionDependency(
			"tree_sitter_runtime",
			"tool",
			"tree-sitter runtime available",
			treeSitterRuntimeAvailable,
			"Parser-backed extraction requires the Rust semantic toolchain to be runnable locally.",
		),
		newIngestionDependency(
			"postgres_ready_for_materialization",
			"setting",
			"Postgres configured",
			postgresConfigured,
			"Durable parser-backed symbol rows should be materialized into Postgres once the semantic index tranche runs.",
		),
	}
	treeSitterDeliveryState := "blocked"
	switch {
	case repoMap == nil:
		treeSitterDeliveryState = "blocked"
	case !treeSitterRuntimeAvailable:
		treeSitterDeliveryState = "ready"
	case postgresConfigured:
		treeSitterDeliveryState = "complete"
	default:
		treeSitterDeliveryState = "ready"
	}
	treeSitterBlockers := []string{}
	if repoMap == nil {
		treeSitterBlockers = append(treeSitterBlockers, "Generate the repo map before durable parser-backed indexing.")
	}
	if !treeSitterRuntimeAvailable {
		treeSitterBlockers = append(treeSitterBlockers, "Install or enable the Rust semantic toolchain so parser-backed symbol extraction can run.")
	}
	if !postgresConfigured {
		treeSitterBlockers = append(treeSitterBlockers, "Configure Postgres to persist parser-backed symbol rows.")
	}
	phases = append(phases, IngestionPhaseRecord{
		PhaseID:             "tree_sitter_index",
		Label:               "tree-sitter index",
		Description:         "Use parser-backed extraction to materialize symbols, scopes, and file summaries.",
		ImplementationState: "implemented",
		DeliveryState:       treeSitterDeliveryState,
		Dependencies:        treeSitterDependencies,
		Blockers:            treeSitterBlockers,
		Outputs:             []string{"symbol rows", "file summaries", "scope-aware repo context"},
		Evidence: []string{
			conditionalString(treeSitterOnDemandAvailable, "On-demand parser-backed symbol extraction already feeds path-symbols and code-explainer.", "On-demand parser-backed extraction is not yet available because the repo map is missing."),
			"postgres_schema=" + postgresSchema,
		},
	})

	astGrepDependencies := []IngestionDependencyRecord{
		newIngestionDependency(
			"repo_map_for_patterns",
			"artifact",
			"Repo map available",
			repoMap != nil,
			"Semantic pattern scans should use the repo-map boundary and source filters.",
		),
		newIngestionDependency(
			"ast_grep_runtime",
			"tool",
			"ast-grep available",
			astGrepIsAvailable,
			"Semantic pattern queries require the sg or ast-grep binary to be installed locally.",
		),
		newIngestionDependency(
			"ast_grep_surface",
			"implementation",
			"Semantic pattern surface implemented",
			true,
			"semantic-search tool surface implemented and issue-context aware.",
		),
		newIngestionDependency(
			"postgres_ready_for_pattern_materialization",
			"setting",
			"Postgres configured",
			postgresConfigured,
			"Durable query and match rows should be materialized into Postgres when semantic rule capture is enabled.",
		),
	}
	astGrepDeliveryState := "blocked"
	switch {
	case repoMap == nil:
		astGrepDeliveryState = "blocked"
	case !astGrepIsAvailable:
		astGrepDeliveryState = "ready"
	case postgresConfigured:
		astGrepDeliveryState = "complete"
	default:
		astGrepDeliveryState = "ready"
	}
	astGrepBlockers := []string{}
	if repoMap == nil {
		astGrepBlockers = append(astGrepBlockers, "Generate the repo map before semantic pattern scans.")
	}
	if !astGrepIsAvailable {
		astGrepBlockers = append(astGrepBlockers, "Install ast-grep so semantic pattern scans can execute.")
	}
	if !postgresConfigured {
		astGrepBlockers = append(astGrepBlockers, "Configure Postgres to persist semantic pattern queries and matches.")
	}
	phases = append(phases, IngestionPhaseRecord{
		PhaseID:             "ast_grep_rules",
		Label:               "ast-grep rules",
		Description:         "Run semantic pattern queries for issue-shaped checks and durable rule-backed evidence.",
		ImplementationState: "implemented",
		DeliveryState:       astGrepDeliveryState,
		Dependencies:        astGrepDependencies,
		Blockers:            astGrepBlockers,
		Outputs:             []string{"semantic matches", "rule-backed issue evidence", "durable query rows"},
		Evidence: []string{
			conditionalString(astGrepOnDemandAvailable, "Storage-ready semantic query and match row previews can now be generated on-demand.", "Semantic pattern scans are blocked until repo-map inputs exist."),
			conditionalString(astGrepIsAvailable, "ast-grep available", "ast-grep unavailable"),
			"Durable semantic query and match materialization is still pending.",
		},
	})

	semanticDependencies := []IngestionDependencyRecord{
		newIngestionDependency(
			"tree_sitter_index_complete",
			"implementation",
			"tree-sitter index landed",
			treeSitterRuntimeAvailable,
			"Durable semantic graph materialization depends on parser-backed symbol rows being produced by the Rust semantic toolchain.",
		),
		newIngestionDependency(
			"postgres_ready_for_semantic_graph",
			"setting",
			"Postgres configured",
			postgresConfigured,
			"Materialized semantic rows need Postgres as the durable backing store.",
		),
	}
	semanticDeliveryState := "blocked"
	if treeSitterRuntimeAvailable {
		semanticDeliveryState = conditionalString(postgresConfigured, "ready", "blocked")
	}
	semanticBlockers := []string{}
	if !treeSitterRuntimeAvailable {
		semanticBlockers = append(semanticBlockers, "Enable the Rust semantic toolchain so parser-backed symbol rows can materialize first.")
	}
	if !postgresConfigured {
		semanticBlockers = append(semanticBlockers, "Configure Postgres before materializing durable semantic graph rows.")
	}
	phases = append(phases, IngestionPhaseRecord{
		PhaseID:             "semantic_materialization",
		Label:               "Semantic materialization",
		Description:         "Persist symbol, edge, and query-ready semantic rows into Postgres for durable repo intelligence.",
		ImplementationState: "implemented",
		DeliveryState:       semanticDeliveryState,
		Dependencies:        semanticDependencies,
		Blockers:            semanticBlockers,
		Outputs:             []string{"symbol rows", "semantic graph edges", "workspace semantic materializations"},
		Evidence: []string{
			"Path-symbol and semantic-search Postgres materialization routes are implemented behind api-go.",
			"Workspace semantic index materialization is available through Go-owned delivery.",
		},
	})

	lspDependencies := []IngestionDependencyRecord{
		newIngestionDependency(
			"semantic_core_first",
			"implementation",
			"tree-sitter and ast-grep tranche landed",
			true,
			"The structural semantic tranche already landed, so live LSP reads and durable diagnostics build on that floor instead of bypassing it.",
		),
		newIngestionDependency(
			"postgres_configured_for_lsp",
			"setting",
			"Postgres configured",
			postgresConfigured,
			"LSP diagnostics and symbol links should be persisted alongside the semantic graph.",
		),
	}
	phases = append(phases, IngestionPhaseRecord{
		PhaseID:             "lsp_enrichment",
		Label:               "LSP enrichment",
		Description:         "Attach live definitions, references, workspace symbols, and diagnostics through a workspace LSP manager.",
		ImplementationState: "implemented",
		DeliveryState:       conditionalString(postgresConfigured, "complete", "blocked"),
		Dependencies:        lspDependencies,
		Blockers:            conditionalStrings(!postgresConfigured, "Configure Postgres so diagnostics baselines can persist and link to durable repo/run state."),
		Outputs:             []string{"definitions", "references", "diagnostics", "workspace symbols"},
		Evidence: []string{
			"Go-owned workspace LSP routes now serve go-to-definition, references, document symbols, workspace symbols, and live diagnostics.",
			"Diagnostics baselines now persist run linkage, semantic baseline anchors, replay payload provenance, and durable symbol-link context.",
			"Rust owns diagnostics normalization and conservative diagnostic-to-symbol link decisions behind the Go delivery surface.",
		},
	})

	searchDependencies := []IngestionDependencyRecord{
		newIngestionDependency(
			"postgres_configured_for_search",
			"setting",
			"Postgres configured",
			postgresConfigured,
			"Hybrid retrieval starts from Postgres-backed lexical and artifact indexes.",
		),
		newIngestionDependency(
			"symbol_materialization",
			"implementation",
			"Symbol and edge materialization landed",
			false,
			"Search quality depends on symbols and structural edges being materialized first.",
		),
	}
	searchBlockers := []string{"Depends on symbol and edge materialization before hybrid retrieval can be trusted."}
	if !postgresConfigured {
		searchBlockers = append([]string{"Postgres configured"}, searchBlockers...)
	}
	phases = append(phases, IngestionPhaseRecord{
		PhaseID:             "search_materialization",
		Label:               "Search materialization",
		Description:         "Build lexical, structural, and later semantic retrieval surfaces over repo and artifact state.",
		ImplementationState: "planned",
		DeliveryState:       "blocked",
		Dependencies:        searchDependencies,
		Blockers:            searchBlockers,
		Outputs:             []string{"search indexes", "retrieval ledger inputs", "impact retrieval seeds"},
		Evidence:            []string{"postgres_schema=" + postgresSchema},
	})

	readyPhaseIDs := []string{}
	blockedPhaseIDs := []string{}
	completedPhaseCount := 0
	for _, phase := range phases {
		switch phase.DeliveryState {
		case "ready":
			readyPhaseIDs = append(readyPhaseIDs, phase.PhaseID)
		case "blocked":
			blockedPhaseIDs = append(blockedPhaseIDs, phase.PhaseID)
		case "complete":
			completedPhaseCount++
		}
	}
	var nextPhaseID *string
	if len(readyPhaseIDs) > 0 {
		nextPhaseID = &readyPhaseIDs[0]
	}

	return &IngestionPipelinePlan{
		WorkspaceID:         workspaceID,
		RootPath:            workspace.RootPath,
		PostgresConfigured:  postgresConfigured,
		PostgresSchema:      postgresSchema,
		Phases:              phases,
		CompletedPhaseCount: completedPhaseCount,
		ReadyPhaseIDs:       readyPhaseIDs,
		BlockedPhaseIDs:     blockedPhaseIDs,
		NextPhaseID:         nextPhaseID,
		GeneratedAt:         nowUTC(),
	}, nil
}

func GetWorkspaceGuidanceHealth(dataDir string, workspaceID string) (*RepoGuidanceHealth, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	rootPath := snapshot.Workspace.RootPath
	guidance, err := collectWorkspaceGuidance(rootPath, workspaceID)
	if err != nil {
		return nil, err
	}

	presentFiles := make([]string, 0, len(guidance))
	staleFiles := []string{}
	missingFiles := []string{}
	starters := []GuidanceStarterRecord{}
	for _, item := range guidance {
		presentFiles = append(presentFiles, item.Path)
	}
	for _, starter := range guidanceStarterSpecs() {
		candidatePath := filepath.Join(rootPath, starter.Path)
		info, err := os.Stat(candidatePath)
		exists := err == nil && !info.IsDir()
		stale := exists && guidanceFileIsStale(candidatePath)
		starters = append(starters, GuidanceStarterRecord{
			TemplateID:  starter.TemplateID,
			Title:       starter.Title,
			Path:        starter.Path,
			Description: starter.Description,
			Recommended: starter.Recommended,
			Exists:      exists,
			Stale:       stale,
		})
		if stale {
			staleFiles = append(staleFiles, starter.Path)
		}
		if starter.Recommended && !exists {
			missingFiles = append(missingFiles, starter.Path)
		}
	}

	instructionCount := 0
	alwaysOnCount := 0
	for _, item := range guidance {
		if item.Kind == "agent_instructions" || item.Kind == "conventions" {
			instructionCount++
		}
		if item.AlwaysOn {
			alwaysOnCount++
		}
	}

	recommendedFiles := []string{}
	for _, starter := range starters {
		if starter.Recommended {
			recommendedFiles = append(recommendedFiles, starter.Path)
		}
	}

	status := "healthy"
	summary := "Repository guidance is present and ready to shape issue context and runs."
	switch {
	case len(staleFiles) > 0:
		status = "stale"
		summary = "Starter guidance exists but still contains xMustard placeholders that should be customized."
	case len(guidance) == 0:
		status = "missing"
		summary = "No repository guidance files were found. Generate a starter AGENTS.md to ground issue context and runs."
	case len(missingFiles) > 0:
		status = "partial"
		summary = "Guidance is present, but the recommended starter set is incomplete."
	}

	slices.Sort(presentFiles)
	slices.Sort(missingFiles)
	slices.Sort(staleFiles)
	slices.Sort(recommendedFiles)

	return &RepoGuidanceHealth{
		WorkspaceID:      workspaceID,
		Status:           status,
		Summary:          summary,
		GuidanceCount:    len(guidance),
		AlwaysOnCount:    alwaysOnCount,
		InstructionCount: instructionCount,
		PresentFiles:     presentFiles,
		MissingFiles:     missingFiles,
		StaleFiles:       staleFiles,
		RecommendedFiles: recommendedFiles,
		Starters:         starters,
		GeneratedAt:      nowUTC(),
	}, nil
}

type guidanceStarterSpec struct {
	TemplateID  string
	Title       string
	Path        string
	Description string
	Recommended bool
}

func guidanceStarterSpecs() []guidanceStarterSpec {
	return []guidanceStarterSpec{
		{
			TemplateID:  "agents",
			Title:       "AGENTS.md",
			Path:        "AGENTS.md",
			Description: "Always-on repo instructions for local agents and issue-context prompts.",
			Recommended: true,
		},
		{
			TemplateID:  "openhands_repo",
			Title:       "OpenHands repo microagent",
			Path:        ".openhands/microagents/repo.md",
			Description: "Short repo-scoped microagent instructions for planning and execution flows.",
			Recommended: true,
		},
		{
			TemplateID:  "conventions",
			Title:       "CONVENTIONS.md",
			Path:        "CONVENTIONS.md",
			Description: "Shared engineering conventions for style, structure, and review defaults.",
			Recommended: false,
		},
	}
}

func guidanceFileIsStale(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(content), guidancePlaceholderMarker)
}

func loadRepoMapIfPresent(dataDir string, workspaceID string) (*rustcore.RepoMapSummary, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "repo_map.json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var summary rustcore.RepoMapSummary
	if err := readJSON(path, &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

func cloneSummary(input map[string]int) map[string]int {
	if len(input) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cargoAvailable() bool {
	_, err := exec.LookPath("cargo")
	return err == nil
}

func astGrepAvailable() bool {
	if _, err := exec.LookPath("sg"); err == nil {
		return true
	}
	_, err := exec.LookPath("ast-grep")
	return err == nil
}

func newIngestionDependency(id string, kind string, label string, satisfied bool, detail string) IngestionDependencyRecord {
	return IngestionDependencyRecord{
		DependencyID: id,
		Kind:         kind,
		Label:        label,
		Satisfied:    satisfied,
		Detail:       optionalString(detail),
	}
}

func conditionalString(condition bool, whenTrue string, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func conditionalStrings(condition bool, items ...string) []string {
	if !condition {
		return []string{}
	}
	return append([]string{}, items...)
}

func phaseIntString(value int) string {
	return strconv.Itoa(value)
}

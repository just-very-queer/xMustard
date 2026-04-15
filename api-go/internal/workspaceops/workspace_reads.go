package workspaceops

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

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

type runRecord struct {
	RunID          string          `json:"run_id"`
	WorkspaceID    string          `json:"workspace_id"`
	IssueID        string          `json:"issue_id"`
	Runtime        string          `json:"runtime"`
	Model          string          `json:"model"`
	Status         string          `json:"status"`
	Title          string          `json:"title"`
	Prompt         string          `json:"prompt"`
	Command        []string        `json:"command"`
	CommandPreview string          `json:"command_preview"`
	LogPath        string          `json:"log_path"`
	OutputPath     string          `json:"output_path"`
	CreatedAt      string          `json:"created_at"`
	StartedAt      *string         `json:"started_at,omitempty"`
	CompletedAt    *string         `json:"completed_at,omitempty"`
	ExitCode       *int            `json:"exit_code,omitempty"`
	PID            *int            `json:"pid,omitempty"`
	Error          *string         `json:"error,omitempty"`
	RunbookID      *string         `json:"runbook_id,omitempty"`
	Worktree       *WorktreeStatus `json:"worktree,omitempty"`
	GuidancePaths  []string        `json:"guidance_paths"`
	Summary        map[string]any  `json:"summary,omitempty"`
	Plan           *RunPlan        `json:"plan,omitempty"`
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

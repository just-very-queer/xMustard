package workspaceops

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"xmustard/api-go/internal/rustcore"
)

const (
	maxCachedSnapshotBytes = 25 * 1024 * 1024
	scannerVersion         = 2
)

type WorkspaceLoadRequest struct {
	RootPath             string  `json:"root_path"`
	Name                 *string `json:"name"`
	AutoScan             bool    `json:"auto_scan"`
	PreferCachedSnapshot bool    `json:"prefer_cached_snapshot"`
}

type ExportBundle struct {
	Workspace            workspaceRecord             `json:"workspace"`
	Snapshot             workspaceSnapshot           `json:"snapshot"`
	RepoMap              *rustcore.RepoMapSummary    `json:"repo_map,omitempty"`
	Runs                 []runRecord                 `json:"runs"`
	Fixes                []FixRecord                 `json:"fixes"`
	RunReviews           []RunReviewRecord           `json:"run_reviews"`
	Runbooks             []RunbookRecord             `json:"runbooks"`
	VerificationProfiles []verificationProfileRecord `json:"verification_profiles"`
	TicketContexts       []TicketContextRecord       `json:"ticket_contexts"`
	ThreatModels         []ThreatModelRecord         `json:"threat_models"`
	BrowserDumps         []BrowserDumpRecord         `json:"browser_dumps"`
	ContextReplays       []IssueContextReplayRecord  `json:"context_replays"`
	Verifications        []verificationRecord        `json:"verifications"`
	Activity             []activityRecord            `json:"activity"`
	ExportedAt           string                      `json:"exported_at"`
}

func ListWorkspaces(dataDir string) ([]workspaceRecord, error) {
	path := filepath.Join(dataDir, "workspaces.json")
	var items []workspaceRecord
	if err := readJSON(path, &items); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []workspaceRecord{}, nil
		}
		return nil, err
	}
	slices.SortFunc(items, func(a, b workspaceRecord) int {
		left := strings.ToLower(a.Name)
		right := strings.ToLower(b.Name)
		if left == right {
			if a.WorkspaceID < b.WorkspaceID {
				return -1
			}
			if a.WorkspaceID > b.WorkspaceID {
				return 1
			}
			return 0
		}
		if left < right {
			return -1
		}
		return 1
	})
	return items, nil
}

func LoadWorkspace(dataDir string, request WorkspaceLoadRequest) (*workspaceSnapshot, error) {
	rootPath, err := filepath.Abs(strings.TrimSpace(request.RootPath))
	if err != nil {
		return nil, err
	}
	if rootPath == "" {
		return nil, fmt.Errorf("root_path is required")
	}
	now := nowUTC()
	name := strings.TrimSpace(firstNonEmptyPtr(request.Name))
	if name == "" {
		name = filepath.Base(rootPath)
	}

	workspaces, err := ListWorkspaces(dataDir)
	if err != nil {
		return nil, err
	}
	workspaceID := workspaceIDForPath(rootPath)
	for _, item := range workspaces {
		if filepath.Clean(item.RootPath) == rootPath {
			workspaceID = item.WorkspaceID
			break
		}
	}

	workspace := workspaceRecord{
		WorkspaceID: workspaceID,
		Name:        name,
		RootPath:    rootPath,
		CreatedAt:   ptr(now),
		UpdatedAt:   ptr(now),
	}
	for _, item := range workspaces {
		if item.WorkspaceID == workspaceID {
			workspace = item
			workspace.Name = name
			workspace.RootPath = rootPath
			workspace.UpdatedAt = ptr(now)
			break
		}
	}
	if err := saveWorkspaceRecord(dataDir, workspace); err != nil {
		return nil, err
	}

	snapshotPath := filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json")
	snapshotIsOversized := false
	if info, statErr := os.Stat(snapshotPath); statErr == nil {
		snapshotIsOversized = info.Size() > maxCachedSnapshotBytes
	}

	var cached *workspaceSnapshot
	if !snapshotIsOversized {
		cached, err = loadSnapshot(dataDir, workspaceID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if cached != nil && cached.ScannerVersion != scannerVersion {
		cached = nil
	}
	if cached != nil && request.PreferCachedSnapshot && !snapshotIsOversized {
		cached.Workspace.WorkspaceID = workspace.WorkspaceID
		cached.Workspace.Name = workspace.Name
		cached.Workspace.RootPath = workspace.RootPath
		cached.Workspace.CreatedAt = workspace.CreatedAt
		cached.Workspace.UpdatedAt = workspace.UpdatedAt
		if cached.Workspace.LatestScanAt == nil {
			cached.Workspace.LatestScanAt = workspace.LatestScanAt
		}
		return cached, nil
	}

	if request.AutoScan {
		return ScanWorkspace(dataDir, workspaceID)
	}
	if cached != nil {
		cached.Workspace.WorkspaceID = workspace.WorkspaceID
		cached.Workspace.Name = workspace.Name
		cached.Workspace.RootPath = workspace.RootPath
		cached.Workspace.CreatedAt = workspace.CreatedAt
		cached.Workspace.UpdatedAt = workspace.UpdatedAt
		if cached.Workspace.LatestScanAt == nil {
			cached.Workspace.LatestScanAt = workspace.LatestScanAt
		}
		return cached, nil
	}
	return nil, os.ErrNotExist
}

func ReadWorktreeStatus(dataDir string, workspaceID string) (*WorktreeStatus, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	return readWorktreeStatus(workspace.RootPath), nil
}

func ExportWorkspace(dataDir string, workspaceID string) (*ExportBundle, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	repoMap, err := loadOrBuildRepoMap(dataDir, workspaceID, snapshot.Workspace.RootPath)
	if err != nil {
		return nil, err
	}
	runs, err := listRuns(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	fixes, err := loadFixRecords(dataDir, workspaceID, "")
	if err != nil {
		return nil, err
	}
	runReviews, err := loadRunReviews(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	runbooks, err := listRunbooks(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	verificationProfiles, err := ListVerificationProfiles(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	ticketContexts, err := loadTicketContexts(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	threatModels, err := loadThreatModels(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	browserDumps, err := loadBrowserDumps(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	contextReplays, err := loadContextReplays(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	verifications, err := ListVerifications(dataDir, workspaceID, "")
	if err != nil {
		return nil, err
	}
	activity, err := loadAllWorkspaceActivity(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	return &ExportBundle{
		Workspace:            workspace,
		Snapshot:             *snapshot,
		RepoMap:              repoMap,
		Runs:                 runs,
		Fixes:                fixes,
		RunReviews:           runReviews,
		Runbooks:             runbooks,
		VerificationProfiles: verificationProfiles,
		TicketContexts:       ticketContexts,
		ThreatModels:         threatModels,
		BrowserDumps:         browserDumps,
		ContextReplays:       contextReplays,
		Verifications:        verifications,
		Activity:             activity,
		ExportedAt:           nowUTC(),
	}, nil
}

func getWorkspaceRecord(dataDir string, workspaceID string) (workspaceRecord, error) {
	workspaces, err := ListWorkspaces(dataDir)
	if err != nil {
		return workspaceRecord{}, err
	}
	for _, item := range workspaces {
		if item.WorkspaceID == workspaceID {
			return item, nil
		}
	}
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err == nil && snapshot != nil {
		return snapshot.Workspace, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return workspaceRecord{}, os.ErrNotExist
	}
	return workspaceRecord{}, err
}

func saveWorkspaceRecord(dataDir string, workspace workspaceRecord) error {
	workspaces, err := ListWorkspaces(dataDir)
	if err != nil {
		return err
	}
	filtered := workspaces[:0]
	for _, item := range workspaces {
		if item.WorkspaceID != workspace.WorkspaceID {
			filtered = append(filtered, item)
		}
	}
	workspaces = append(filtered, workspace)
	slices.SortFunc(workspaces, func(a, b workspaceRecord) int {
		left := strings.ToLower(a.Name)
		right := strings.ToLower(b.Name)
		if left == right {
			if a.WorkspaceID < b.WorkspaceID {
				return -1
			}
			if a.WorkspaceID > b.WorkspaceID {
				return 1
			}
			return 0
		}
		if left < right {
			return -1
		}
		return 1
	})
	return writeJSON(filepath.Join(dataDir, "workspaces.json"), workspaces)
}

func workspaceIDForPath(rootPath string) string {
	normalized := filepath.Clean(rootPath)
	digest := sha1.Sum([]byte(normalized))
	stem := strings.ToLower(filepath.Base(normalized))
	stem = strings.ReplaceAll(stem, " ", "-")
	stem = strings.ReplaceAll(stem, "_", "-")
	builder := strings.Builder{}
	for _, ch := range stem {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			builder.WriteRune(ch)
		}
	}
	value := strings.Trim(builder.String(), "-")
	if value == "" {
		value = "workspace"
	}
	return value + "-" + hex.EncodeToString(digest[:])[:10]
}

func loadAllWorkspaceActivity(dataDir string, workspaceID string) ([]activityRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	handle, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
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
	return items, nil
}

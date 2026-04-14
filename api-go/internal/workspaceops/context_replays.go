package workspaceops

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"xmustard/api-go/internal/rustcore"
)

type IssueContextReplayRequest struct {
	Label *string `json:"label"`
}

type IssueContextReplayRecord struct {
	ReplayID               string   `json:"replay_id"`
	WorkspaceID            string   `json:"workspace_id"`
	IssueID                string   `json:"issue_id"`
	Label                  string   `json:"label"`
	Prompt                 string   `json:"prompt"`
	TreeFocus              []string `json:"tree_focus"`
	GuidancePaths          []string `json:"guidance_paths"`
	VerificationProfileIDs []string `json:"verification_profile_ids"`
	TicketContextIDs       []string `json:"ticket_context_ids"`
	CreatedAt              string   `json:"created_at"`
}

const replayGuidanceLimit = 6

func ListIssueContextReplays(dataDir string, workspaceID string, issueID string) ([]IssueContextReplayRecord, error) {
	if _, err := requireIssue(dataDir, workspaceID, issueID); err != nil {
		return nil, err
	}
	items, err := loadContextReplays(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	filtered := make([]IssueContextReplayRecord, 0, len(items))
	for _, item := range items {
		if item.IssueID == issueID {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func CaptureIssueContextReplay(dataDir string, workspaceID string, issueID string, request IssueContextReplayRequest) (*IssueContextReplayRecord, error) {
	packet, err := BuildIssueContextPacket(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}

	label := strings.TrimSpace(issueID + " context replay")
	if request.Label != nil && strings.TrimSpace(*request.Label) != "" {
		label = strings.TrimSpace(*request.Label)
	}

	record := IssueContextReplayRecord{
		ReplayID:               newReplayID(),
		WorkspaceID:            workspaceID,
		IssueID:                issueID,
		Label:                  label,
		Prompt:                 packet.Prompt,
		TreeFocus:              append([]string{}, packet.TreeFocus[:min(len(packet.TreeFocus), 12)]...),
		GuidancePaths:          guidancePaths(packet.Guidance),
		VerificationProfileIDs: verificationProfileIDs(packet.AvailableVerificationProfiles),
		TicketContextIDs:       ticketContextIDs(packet.TicketContexts),
		CreatedAt:              nowUTC(),
	}

	items, err := loadContextReplays(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	items = append(items, record)
	if err := saveContextReplays(dataDir, workspaceID, items); err != nil {
		return nil, err
	}
	if err := appendIssueActivity(
		dataDir,
		workspaceID,
		issueID,
		"",
		"context_replay.captured",
		"Captured issue context replay "+record.Label,
		map[string]any{"replay_id": record.ReplayID},
	); err != nil {
		return nil, err
	}
	return &record, nil
}

func loadContextReplays(dataDir string, workspaceID string) ([]IssueContextReplayRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "context_replays.json")
	var items []IssueContextReplayRecord
	if err := readJSON(path, &items); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []IssueContextReplayRecord{}, nil
		}
		return nil, err
	}
	slices.SortFunc(items, func(a, b IssueContextReplayRecord) int {
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

func saveContextReplays(dataDir string, workspaceID string, items []IssueContextReplayRecord) error {
	slices.SortFunc(items, func(a, b IssueContextReplayRecord) int {
		if a.CreatedAt > b.CreatedAt {
			return -1
		}
		if a.CreatedAt < b.CreatedAt {
			return 1
		}
		return 0
	})
	path := filepath.Join(dataDir, "workspaces", workspaceID, "context_replays.json")
	return writeJSON(path, items)
}

func listReplayActivity(dataDir string, workspaceID string, issueID string, limit int) ([]activityRecord, error) {
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
		var record activityRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record.EntityType == "issue" && record.IssueID != nil && *record.IssueID == issueID {
			items = append(items, record)
		}
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
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func collectWorkspaceGuidance(root string, workspaceID string) ([]RepoGuidanceRecord, error) {
	rootPath := filepath.Clean(root)
	candidates := []struct {
		path     string
		kind     string
		alwaysOn bool
		priority int
	}{
		{path: "AGENTS.md", kind: "agent_instructions", alwaysOn: true, priority: 10},
		{path: "agents.md", kind: "agent_instructions", alwaysOn: true, priority: 10},
		{path: "CLAUDE.md", kind: "agent_instructions", alwaysOn: true, priority: 12},
		{path: "GEMINI.md", kind: "agent_instructions", alwaysOn: true, priority: 12},
		{path: ".openhands/microagents/repo.md", kind: "agent_instructions", alwaysOn: true, priority: 14},
		{path: "CONVENTIONS.md", kind: "conventions", alwaysOn: true, priority: 20},
		{path: ".clinerules", kind: "conventions", alwaysOn: true, priority: 22},
		{path: ".devin/wiki.json", kind: "repo_index", alwaysOn: true, priority: 25},
		{path: "README.md", kind: "workspace_overview", alwaysOn: false, priority: 60},
	}
	walkRoots := []struct {
		path     string
		kind     string
		alwaysOn bool
		priority int
	}{
		{path: ".openhands/microagents", kind: "agent_instructions", alwaysOn: true, priority: 28},
		{path: ".openhands/skills", kind: "skill", alwaysOn: false, priority: 30},
		{path: ".agents/skills", kind: "skill", alwaysOn: false, priority: 32},
		{path: ".cursor/rules", kind: "conventions", alwaysOn: false, priority: 34},
	}

	seen := map[string]struct{}{}
	items := make([]RepoGuidanceRecord, 0, 12)
	addCandidate := func(path string, kind string, alwaysOn bool, priority int) error {
		resolved, err := filepath.Abs(path)
		if err != nil {
			resolved = path
		}
		key := strings.ToLower(resolved)
		if _, exists := seen[key]; exists {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return nil
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		summary, excerpt, triggerKeywords := summarizeGuidanceText(path, kind, string(text), info)
		title := guidanceTitleFromText(path, string(text))
		rel, err := filepath.Rel(rootPath, path)
		if err != nil {
			rel = filepath.Base(path)
		}
		seen[key] = struct{}{}
		items = append(items, RepoGuidanceRecord{
			GuidanceID:      hashID(workspaceID, rel, kind),
			WorkspaceID:     workspaceID,
			Path:            rel,
			Kind:            kind,
			AlwaysOn:        alwaysOn,
			Priority:        priority,
			Title:           title,
			Summary:         summary,
			Excerpt:         excerpt,
			TriggerKeywords: triggerKeywords,
			UpdatedAt:       ptr(fileModTime(info)),
		})
		return nil
	}

	for _, candidate := range candidates {
		if err := addCandidate(filepath.Join(rootPath, candidate.path), candidate.kind, candidate.alwaysOn, candidate.priority); err != nil {
			return nil, err
		}
	}

	for _, walkRoot := range walkRoots {
		root := filepath.Join(rootPath, walkRoot.path)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".md", ".mdc", ".json":
				return addCandidate(path, walkRoot.kind, walkRoot.alwaysOn, walkRoot.priority)
			default:
				return nil
			}
		})
	}

	slices.SortFunc(items, func(a, b RepoGuidanceRecord) int {
		if a.Priority != b.Priority {
			if a.Priority < b.Priority {
				return -1
			}
			return 1
		}
		left := strings.ToLower(a.Path)
		right := strings.ToLower(b.Path)
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
		return 0
	})
	if len(items) > replayGuidanceLimit {
		items = items[:replayGuidanceLimit]
	}
	return items, nil
}

func summarizeGuidanceText(path string, kind string, text string, info os.FileInfo) (string, *string, []string) {
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return summarizeGuidanceJSON(text)
	}

	lines := []string{}
	triggerKeywords := []string{}
	inFrontMatter := false
	inCodeFence := false
	seenFrontMatter := false

	for idx, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if idx == 0 && line == "---" {
			inFrontMatter = true
			seenFrontMatter = true
			continue
		}
		if inFrontMatter && line == "---" {
			inFrontMatter = false
			continue
		}
		if strings.HasPrefix(line, "```") {
			inCodeFence = !inCodeFence
			continue
		}
		if inFrontMatter {
			if strings.HasPrefix(strings.ToLower(line), "keywords:") {
				triggerKeywords = append(triggerKeywords, extractInlineKeywords(line)...)
			}
			continue
		}
		if inCodeFence || strings.HasPrefix(line, "#") {
			continue
		}
		normalized := strings.TrimSpace(strings.TrimLeft(line, "-*"))
		if normalized == "" {
			continue
		}
		normalized = regexpNumberPrefix.ReplaceAllString(normalized, "")
		if normalized != "" && (len(lines) == 0 || lines[len(lines)-1] != normalized) {
			lines = append(lines, normalized)
		}
		if len(lines) >= 4 {
			break
		}
	}
	if kind == "skill" && seenFrontMatter && len(triggerKeywords) == 0 {
		triggerKeywords = extractKeywordBlock(text)
	}

	summary := "Repository guidance from " + filepath.Base(path)
	if len(lines) > 0 {
		summary = strings.Join(lines[:min(len(lines), 3)], " ")
	}
	excerptValue := strings.Join(lines[:min(len(lines), 4)], "\n")
	if excerptValue == "" {
		excerptValue = summary
	}
	return summary, ptr(excerptValue), trimStringList(triggerKeywords, 8)
}

func summarizeGuidanceJSON(text string) (string, *string, []string) {
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		compact := strings.Join(strings.Fields(text), " ")
		if len(compact) > 280 {
			compact = compact[:280]
		}
		if compact == "" {
			compact = "Repository index configuration"
		}
		return compact, ptr(compact), []string{}
	}
	dict, ok := payload.(map[string]any)
	if !ok {
		compact, _ := json.Marshal(payload)
		excerpt := string(compact)
		return excerpt, ptr(excerpt), []string{}
	}
	parts := []string{}
	if desc, ok := dict["description"].(string); ok && strings.TrimSpace(desc) != "" {
		parts = append(parts, strings.TrimSpace(desc))
	}
	if include, ok := dict["include"].([]any); ok && len(include) > 0 {
		parts = append(parts, "Includes "+joinAny(include[:min(len(include), 4)]))
	}
	if exclude, ok := dict["exclude"].([]any); ok && len(exclude) > 0 {
		parts = append(parts, "Excludes "+joinAny(exclude[:min(len(exclude), 4)]))
	}
	summary := strings.Join(parts, ". ")
	if summary == "" {
		compact, _ := json.Marshal(dict)
		summary = string(compact)
	}
	excerptBytes, _ := json.MarshalIndent(dict, "", "  ")
	excerpt := string(excerptBytes)
	if len(excerpt) > 600 {
		excerpt = excerpt[:600]
	}
	return summary, ptr(excerpt), []string{}
}

var regexpNumberPrefix = regexp.MustCompile(`^\d+\.\s*`)

func extractInlineKeywords(line string) []string {
	start := strings.Index(line, "[")
	end := strings.Index(line, "]")
	if start == -1 || end == -1 || end <= start {
		return []string{}
	}
	items := strings.Split(line[start+1:end], ",")
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(strings.Trim(item, `'"`))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func extractKeywordBlock(text string) []string {
	match := regexp.MustCompile(`(?is)keywords:\s*\[(.*?)\]`).FindStringSubmatch(text)
	if len(match) < 2 {
		return []string{}
	}
	items := strings.Split(match[1], ",")
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(strings.Trim(item, `'"`))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func guidanceTitleFromText(path string, text string) string {
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return filepath.Base(path)
}

func fileModTime(info os.FileInfo) string {
	if info == nil {
		return nowUTC()
	}
	return info.ModTime().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
}

func joinAny(items []any) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprint(item))
	}
	return strings.Join(parts, ", ")
}

func loadOrBuildRepoMap(dataDir string, workspaceID string, repoRoot string) (*rustcore.RepoMapSummary, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "repo_map.json")
	if _, err := os.Stat(path); err == nil {
		var summary rustcore.RepoMapSummary
		if err := readJSON(path, &summary); err != nil {
			return nil, err
		}
		return &summary, nil
	}
	summary, err := rustcore.BuildRepoMap(context.Background(), workspaceID, repoRoot)
	if err != nil {
		return nil, err
	}
	if err := writeJSON(path, summary); err != nil {
		return nil, err
	}
	return summary, nil
}

func guidancePaths(items []RepoGuidanceRecord) []string {
	paths := make([]string, 0, len(items))
	for _, item := range items {
		paths = append(paths, item.Path)
	}
	return paths
}

func verificationProfileIDs(items []rustcore.VerificationProfileInput) []string {
	ids := make([]string, 0, min(len(items), 8))
	for _, item := range items {
		ids = append(ids, item.ProfileID)
		if len(ids) >= 8 {
			break
		}
	}
	return ids
}

func ticketContextIDs(items []TicketContextRecord) []string {
	ids := make([]string, 0, min(len(items), 8))
	for _, item := range items {
		ids = append(ids, item.ContextID)
		if len(ids) >= 8 {
			break
		}
	}
	return ids
}

func newReplayID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err == nil {
		return "ctx_" + hex.EncodeToString(buf)
	}
	return "ctx_" + hashID(nowUTC(), os.Getenv("USER"), os.Getenv("HOSTNAME"))
}

func ptr[T any](value T) *T {
	return &value
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

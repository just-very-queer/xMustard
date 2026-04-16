package workspaceops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

type RepoMCPServerRecord struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
}

type RepoPathInstructionRecord struct {
	InstructionID string  `json:"instruction_id"`
	Path          string  `json:"path"`
	Instructions  string  `json:"instructions"`
	Title         *string `json:"title,omitempty"`
	SourcePath    string  `json:"source_path"`
}

type RepoPathInstructionMatch struct {
	InstructionID string   `json:"instruction_id"`
	Path          string   `json:"path"`
	Title         *string  `json:"title,omitempty"`
	Instructions  string   `json:"instructions"`
	SourcePath    string   `json:"source_path"`
	MatchedPaths  []string `json:"matched_paths"`
}

type RepoConfigRecord struct {
	WorkspaceID      string                      `json:"workspace_id"`
	SourcePath       *string                     `json:"source_path,omitempty"`
	Description      string                      `json:"description"`
	PathFilters      []string                    `json:"path_filters"`
	PathInstructions []RepoPathInstructionRecord `json:"path_instructions"`
	CodeGuidelines   []string                    `json:"code_guidelines"`
	MCPServers       []RepoMCPServerRecord       `json:"mcp_servers"`
	LoadedAt         string                      `json:"loaded_at"`
}

type RepoConfigHealth struct {
	WorkspaceID          string  `json:"workspace_id"`
	Status               string  `json:"status"`
	SourcePath           *string `json:"source_path,omitempty"`
	Summary              string  `json:"summary"`
	PathInstructionCount int     `json:"path_instruction_count"`
	PathFilterCount      int     `json:"path_filter_count"`
	CodeGuidelineCount   int     `json:"code_guideline_count"`
	MCPServerCount       int     `json:"mcp_server_count"`
	LoadedAt             string  `json:"loaded_at"`
}

type rawRepoConfig struct {
	Description    string               `json:"description" yaml:"description"`
	Repository     string               `json:"repository" yaml:"repository"`
	CodeGuidelines any                  `json:"code_guidelines" yaml:"code_guidelines"`
	Guidance       any                  `json:"guidance" yaml:"guidance"`
	MCPServers     []rawRepoMCPServer   `json:"mcp_servers" yaml:"mcp_servers"`
	Reviews        rawRepoConfigReviews `json:"reviews" yaml:"reviews"`
}

type rawRepoConfigReviews struct {
	PathInstructions []rawRepoPathInstruction `json:"path_instructions" yaml:"path_instructions"`
	PathFilters      []string                 `json:"path_filters" yaml:"path_filters"`
}

type rawRepoPathInstruction struct {
	Path         string `json:"path" yaml:"path"`
	Instructions string `json:"instructions" yaml:"instructions"`
	Title        string `json:"title" yaml:"title"`
}

type rawRepoMCPServer struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Usage       string `json:"usage" yaml:"usage"`
}

var repoConfigCandidates = []string{
	".xmustard.yaml",
	".xmustard.yml",
	".xmustard.json",
}

func ReadWorkspaceRepoConfig(dataDir string, workspaceID string) (*RepoConfigRecord, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	return loadRepoConfigForRoot(workspaceID, snapshot.Workspace.RootPath)
}

func GetWorkspaceRepoConfigHealth(dataDir string, workspaceID string) (*RepoConfigHealth, error) {
	config, err := ReadWorkspaceRepoConfig(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if config.SourcePath == nil || strings.TrimSpace(*config.SourcePath) == "" {
		return &RepoConfigHealth{
			WorkspaceID: workspaceID,
			Status:      "missing",
			Summary:     "No .xmustard config was found. Add one to define path-specific instructions, filters, and MCP/browser context guidance.",
			LoadedAt:    nowUTC(),
		}, nil
	}
	summaryParts := []string{"Loaded " + *config.SourcePath}
	if len(config.PathInstructions) > 0 {
		summaryParts = append(summaryParts, pluralSummary(len(config.PathInstructions), "path instruction"))
	}
	if len(config.PathFilters) > 0 {
		summaryParts = append(summaryParts, pluralSummary(len(config.PathFilters), "path filter"))
	}
	if len(config.MCPServers) > 0 {
		summaryParts = append(summaryParts, pluralSummary(len(config.MCPServers), "MCP server hint"))
	}
	return &RepoConfigHealth{
		WorkspaceID:          workspaceID,
		Status:               "configured",
		SourcePath:           config.SourcePath,
		Summary:              strings.Join(summaryParts, ". ") + ".",
		PathInstructionCount: len(config.PathInstructions),
		PathFilterCount:      len(config.PathFilters),
		CodeGuidelineCount:   len(config.CodeGuidelines),
		MCPServerCount:       len(config.MCPServers),
		LoadedAt:             nowUTC(),
	}, nil
}

func matchRepoPathInstructions(config *RepoConfigRecord, candidatePaths []string) []RepoPathInstructionMatch {
	if config == nil || len(config.PathInstructions) == 0 {
		return []RepoPathInstructionMatch{}
	}
	orderedPaths := dedupeStrings(candidatePaths, 24)
	matches := make([]RepoPathInstructionMatch, 0, len(config.PathInstructions))
	for _, item := range config.PathInstructions {
		matchedPaths := make([]string, 0, 4)
		for _, candidate := range orderedPaths {
			ok, err := doublestar.Match(item.Path, filepath.ToSlash(candidate))
			if err != nil || !ok {
				continue
			}
			matchedPaths = append(matchedPaths, candidate)
			if len(matchedPaths) >= 6 {
				break
			}
		}
		if len(matchedPaths) == 0 {
			continue
		}
		matches = append(matches, RepoPathInstructionMatch{
			InstructionID: item.InstructionID,
			Path:          item.Path,
			Title:         item.Title,
			Instructions:  item.Instructions,
			SourcePath:    item.SourcePath,
			MatchedPaths:  matchedPaths,
		})
		if len(matches) >= 6 {
			break
		}
	}
	return matches
}

func loadRepoConfigForRoot(workspaceID string, root string) (*RepoConfigRecord, error) {
	for _, relativePath := range repoConfigCandidates {
		candidate := filepath.Join(root, relativePath)
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		return loadRepoConfigFromPath(workspaceID, root, candidate)
	}
	return &RepoConfigRecord{
		WorkspaceID:      workspaceID,
		PathFilters:      []string{},
		PathInstructions: []RepoPathInstructionRecord{},
		CodeGuidelines:   []string{},
		MCPServers:       []RepoMCPServerRecord{},
		LoadedAt:         nowUTC(),
	}, nil
}

func loadRepoConfigFromPath(workspaceID string, root string, path string) (*RepoConfigRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload rawRepoConfig
	if strings.EqualFold(filepath.Ext(path), ".json") {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
	} else {
		if err := yaml.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
	}
	sourcePath, err := filepath.Rel(root, path)
	if err != nil {
		sourcePath = filepath.Base(path)
	}
	sourcePath = filepath.ToSlash(sourcePath)
	config := &RepoConfigRecord{
		WorkspaceID:      workspaceID,
		SourcePath:       ptr(sourcePath),
		Description:      strings.TrimSpace(firstNonEmptyRepoConfig(payload.Description, payload.Repository)),
		PathFilters:      trimStringList(payload.Reviews.PathFilters, 24),
		PathInstructions: []RepoPathInstructionRecord{},
		CodeGuidelines:   normalizeGuidelineList(payload.CodeGuidelines, payload.Guidance),
		MCPServers:       []RepoMCPServerRecord{},
		LoadedAt:         nowUTC(),
	}
	for index, item := range payload.Reviews.PathInstructions {
		pattern := strings.TrimSpace(item.Path)
		instructions := strings.TrimSpace(item.Instructions)
		if pattern == "" || instructions == "" {
			continue
		}
		title := optionalString(strings.TrimSpace(item.Title))
		config.PathInstructions = append(config.PathInstructions, RepoPathInstructionRecord{
			InstructionID: "cfg_" + hashID(workspaceID, sourcePath, toText(index), pattern),
			Path:          filepath.ToSlash(pattern),
			Instructions:  instructions,
			Title:         title,
			SourcePath:    sourcePath,
		})
	}
	for _, item := range payload.MCPServers {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		config.MCPServers = append(config.MCPServers, RepoMCPServerRecord{
			Name:        name,
			Description: strings.TrimSpace(item.Description),
			Usage:       strings.TrimSpace(item.Usage),
		})
	}
	return config, nil
}

func normalizeGuidelineList(primary any, fallback any) []string {
	if values := normalizeStringListValue(primary); len(values) > 0 {
		return values
	}
	return normalizeStringListValue(fallback)
}

func normalizeStringListValue(value any) []string {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return []string{}
		}
		return []string{strings.TrimSpace(typed)}
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(toText(item))
			if text != "" {
				items = append(items, text)
			}
		}
		return dedupeStrings(items, 24)
	case []string:
		return dedupeStrings(typed, 24)
	default:
		return []string{}
	}
}

func dedupeStrings(items []string, limit int) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, min(len(items), limit))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func pluralSummary(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return toText(count) + " " + noun + "s"
}

func firstNonEmptyRepoConfig(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

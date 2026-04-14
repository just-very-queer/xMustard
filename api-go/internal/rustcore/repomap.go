package rustcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

type RepoMapDirectoryRecord struct {
	Path            string `json:"path"`
	FileCount       int    `json:"file_count"`
	SourceFileCount int    `json:"source_file_count"`
	TestFileCount   int    `json:"test_file_count"`
}

type RepoMapFileRecord struct {
	Path      string `json:"path"`
	Role      string `json:"role"`
	SizeBytes *int64 `json:"size_bytes"`
}

type RepoMapSummary struct {
	WorkspaceID    string                   `json:"workspace_id"`
	RootPath       string                   `json:"root_path"`
	TotalFiles     int                      `json:"total_files"`
	SourceFiles    int                      `json:"source_files"`
	TestFiles      int                      `json:"test_files"`
	TopExtensions  map[string]int           `json:"top_extensions"`
	TopDirectories []RepoMapDirectoryRecord `json:"top_directories"`
	KeyFiles       []RepoMapFileRecord      `json:"key_files"`
	GeneratedAt    string                   `json:"generated_at"`
}

func BuildRepoMap(ctx context.Context, workspaceID string, repoRoot string) (*RepoMapSummary, error) {
	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"build-repo-map",
		workspaceID,
		repoRoot,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core build-repo-map failed: %w: %s", err, stderr.String())
	}

	var summary RepoMapSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		return nil, fmt.Errorf("decode rust-core repo-map: %w", err)
	}
	return &summary, nil
}

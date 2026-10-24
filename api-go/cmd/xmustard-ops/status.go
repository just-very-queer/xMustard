package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"xmustard/api-go/internal/rustcore"
)

func runRepositoryPathCommand(args []string) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(os.Stderr, "usage: xmustard-ops <status|is-ignored> <path>")
		return 2
	}
	output, err := repositoryPathStatus(args[0], rustcore.RunIndex)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, string(output))
	return 0
}

func repositoryPathStatus(path string, runIndex func(context.Context, ...string) ([]byte, error)) ([]byte, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	path = filepath.Join(parent, filepath.Base(path))
	root, err := repositoryRoot(parent)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("path must be inside a Git worktree")
	}
	return runIndex(context.Background(), "path-status", root, "--path", filepath.ToSlash(rel))
}

func repositoryRoot(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", fmt.Errorf("Git returned an empty worktree root")
	}
	return filepath.EvalSymlinks(root)
}

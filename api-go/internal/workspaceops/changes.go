package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"xmustard/api-go/internal/rustcore"
)

// Change-state delivery: resolves a workspace's repo root + absolute data dir and
// delegates gitnexus-style change tracking to the Rust core. Feeds the cockpit
// UI's "change state" pane and the MCP `changed_since` tool.

func resolveChangeRoot(dataDir, workspaceID string) (root string, absData string, err error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return "", "", err
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve data dir: %w", err)
	}
	return snapshot.Workspace.RootPath, abs, nil
}

// WorkspaceFingerprint returns the current repo fingerprint (head/remote/content hash).
func WorkspaceFingerprint(dataDir, workspaceID string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(context.Background(), "fingerprint", root)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// IndexWorkspace builds + persists the index baseline for the workspace repo.
func IndexWorkspace(dataDir, workspaceID string) (json.RawMessage, error) {
	root, absData, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(context.Background(), "index", absData, root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// WorkspaceDrift reports stale-index / sibling-clone drift vs the baseline.
func WorkspaceDrift(dataDir, workspaceID string) (json.RawMessage, error) {
	return WorkspaceDriftCtx(context.Background(), dataDir, workspaceID)
}

// WorkspaceDriftCtx is the request-scoped variant: cancelling ctx kills its Rust/tool children.
func WorkspaceDriftCtx(ctx context.Context, dataDir, workspaceID string) (json.RawMessage, error) {
	root, absData, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(ctx, "drift", absData, root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// WorkspaceChangesSinceIndex returns files + dirty symbols changed since the baseline.
func WorkspaceChangesSinceIndex(dataDir, workspaceID string) (json.RawMessage, error) {
	return WorkspaceChangesSinceIndexCtx(context.Background(), dataDir, workspaceID)
}

// WorkspaceChangesSinceIndexCtx is the request-scoped variant: cancelling ctx kills its Rust/tool children.
func WorkspaceChangesSinceIndexCtx(ctx context.Context, dataDir, workspaceID string) (json.RawMessage, error) {
	root, absData, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(ctx, "changed-since", absData, root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// WorkspaceWorkingChanges returns uncommitted working-tree changes + dirty symbols,
// including contract-break flags (vs the index baseline) on modified symbols.
func WorkspaceWorkingChanges(dataDir, workspaceID string) (json.RawMessage, error) {
	return WorkspaceWorkingChangesCtx(context.Background(), dataDir, workspaceID)
}

// WorkspaceWorkingChangesCtx is the request-scoped variant: cancelling ctx kills its Rust/tool children.
func WorkspaceWorkingChangesCtx(ctx context.Context, dataDir, workspaceID string) (json.RawMessage, error) {
	root, absData, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(ctx, "working-changes", absData, root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

package workspaceops

import (
	"context"
	"encoding/json"
	"strconv"

	"xmustard/api-go/internal/rustcore"
)

// Build and query the workspace symbol graph via rust-core.

func WorkspaceSymbolGraph(dataDir, workspaceID string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunSymbolgraph(context.Background(), "build", root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func WorkspaceHotspots(dataDir, workspaceID string, limit int) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	out, err := rustcore.RunSymbolgraph(context.Background(), "hotspots", root, workspaceID, strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func SymbolBlastRadius(dataDir, workspaceID, symbol string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunSymbolgraph(context.Background(), "blast-radius", root, workspaceID, symbol)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// SymbolImpactCtx is impact v2 (WS-35) on a symbol, UID or path: its dependents or
// dependencies by depth tier, with risk and the completeness envelope, over the code
// index's resolved graph (the legacy graph when the root has no index). params are the
// ImpactBlast parameters (ImpactFlags). Cancelling ctx cancels the Rust work.
func SymbolImpactCtx(ctx context.Context, dataDir, workspaceID, symbol string, params map[string]string) (json.RawMessage, error) {
	flags, err := ImpactFlags(ImpactBlast, params)
	if err != nil {
		return nil, err
	}
	return indexedRead(ctx, dataDir, workspaceID, func(root string, read codeIndexRead) ([]byte, error) {
		args := []string{"impact"}
		args = append(append(append(args, read.flags()...), flags...), root, workspaceID, symbol)
		return rustcore.RunSymbolgraph(ctx, args...)
	})
}

// TraceSymbolsCtx is the shortest path from one symbol to another with per-hop edges
// (WS-35); params are the ImpactTrace parameters. Cancelling ctx cancels the Rust work.
func TraceSymbolsCtx(ctx context.Context, dataDir, workspaceID, from, to string, params map[string]string) (json.RawMessage, error) {
	flags, err := ImpactFlags(ImpactTrace, params)
	if err != nil {
		return nil, err
	}
	return indexedRead(ctx, dataDir, workspaceID, func(root string, read codeIndexRead) ([]byte, error) {
		args := []string{"trace"}
		args = append(append(append(args, read.flags()...), flags...), root, workspaceID, from, to)
		return rustcore.RunSymbolgraph(ctx, args...)
	})
}

// DetectChangesCtx maps a scoped diff onto symbols (WS-35, PAR-IMP-06): the changed
// symbols with contract breaks, and their dependents. params are the ImpactChanges
// parameters, scope among them. The core parses the changed files in a one-shot
// process. Cancelling ctx cancels the Rust work.
func DetectChangesCtx(ctx context.Context, dataDir, workspaceID string, params map[string]string) (json.RawMessage, error) {
	flags, err := ImpactFlags(ImpactChanges, params)
	if err != nil {
		return nil, err
	}
	return indexedRead(ctx, dataDir, workspaceID, func(root string, read codeIndexRead) ([]byte, error) {
		args := []string{"changes"}
		args = append(append(append(args, read.flags()...), flags...), root, workspaceID)
		return rustcore.RunSymbolgraph(ctx, args...)
	})
}

// indexedRead brings the workspace's code index to its observed identity, runs one
// graph read and reports the refresh it waited for in the result's coverage.
func indexedRead(ctx context.Context, dataDir, workspaceID string, run func(root string, read codeIndexRead) ([]byte, error)) (json.RawMessage, error) {
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	read := ensureCodeIndex(ctx, root)
	out, err := run(root, read)
	if err != nil {
		return nil, err
	}
	return read.annotate(out), nil
}

// FileCluster mirrors rust-core's community-cluster output.
type FileCluster struct {
	ClusterID int      `json:"cluster_id"`
	Label     string   `json:"label"`
	Files     []string `json:"files"`
	Size      int      `json:"size"`
}

// WorkspaceClustersCtx returns the file communities (label-propagation clusters) for
// the workspace's symbol graph. Cancelling ctx cancels the clustering (see
// rustcore.runCoreCtx).
func WorkspaceClustersCtx(ctx context.Context, dataDir, workspaceID string) ([]FileCluster, error) {
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunSymbolgraph(ctx, "clusters", root, workspaceID)
	if err != nil {
		return nil, err
	}
	var clusters []FileCluster
	if err := json.Unmarshal(out, &clusters); err != nil {
		return nil, err
	}
	return clusters, nil
}

// PathGraph is where a file sits in the code graph, for `explain`: its community
// cluster (its functional neighbourhood; nil when the file is in none) and the
// freshness and coverage of the graph that answered.
type PathGraph struct {
	Cluster   *FileCluster    `json:"cluster"`
	Freshness json.RawMessage `json:"freshness,omitempty"`
	Coverage  json.RawMessage `json:"coverage,omitempty"`
}

// PathGraphCtx reads path's cluster from the code index (refreshed first, see
// ensureCodeIndex; the core takes the observed identity after the subcommand) or the
// legacy graph. Cancelling ctx cancels the Rust work.
func PathGraphCtx(ctx context.Context, dataDir, workspaceID, path string) (*PathGraph, error) {
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	read := ensureCodeIndex(ctx, root)
	args := []string{"cluster-of"}
	args = append(append(args, read.flags()...), root, workspaceID, path)
	out, err := rustcore.RunSymbolgraph(ctx, args...)
	if err != nil {
		return nil, err
	}
	var g PathGraph
	if err := json.Unmarshal(read.annotate(out), &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// LiveDocumentSymbols runs a live LSP session (rust-core spawns the language
// server, runs the handshake, and returns normalized document symbols). Servers
// that aren't installed degrade gracefully to {"available": false, ...}.
func LiveDocumentSymbols(dataDir, workspaceID, path string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunLspDocumentSymbols(workspaceID, root, path)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

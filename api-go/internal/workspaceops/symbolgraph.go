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

// SymbolImpact returns the true blast radius of a symbol — every file that
// transitively references it (bounded BFS over the precomputed reference graph).
func SymbolImpact(dataDir, workspaceID, symbol string, maxDepth int) (json.RawMessage, error) {
	return SymbolImpactCtx(context.Background(), dataDir, workspaceID, symbol, maxDepth)
}

// SymbolImpactCtx is the request-scoped variant: cancelling ctx cancels its Rust/tool work (see rustcore.runCoreCtx).
func SymbolImpactCtx(ctx context.Context, dataDir, workspaceID, symbol string, maxDepth int) (json.RawMessage, error) {
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if maxDepth <= 0 {
		maxDepth = 4
	}
	read := ensureCodeIndex(ctx, root)
	args := []string{"impact"}
	args = append(append(args, read.flags()...), root, workspaceID, symbol, strconv.Itoa(maxDepth))
	out, err := rustcore.RunSymbolgraph(ctx, args...)
	if err != nil {
		return nil, err
	}
	return read.annotate(out), nil
}

// TraceSymbols returns the shortest dependency path between two symbols.
func TraceSymbols(dataDir, workspaceID, from, to string) (json.RawMessage, error) {
	return TraceSymbolsCtx(context.Background(), dataDir, workspaceID, from, to)
}

// TraceSymbolsCtx is the request-scoped variant: cancelling ctx cancels its Rust/tool work (see rustcore.runCoreCtx).
func TraceSymbolsCtx(ctx context.Context, dataDir, workspaceID, from, to string) (json.RawMessage, error) {
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	read := ensureCodeIndex(ctx, root)
	args := []string{"trace"}
	args = append(append(args, read.flags()...), root, workspaceID, from, to)
	out, err := rustcore.RunSymbolgraph(ctx, args...)
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

// WorkspaceClusters returns the file communities (label-propagation clusters) for
// the workspace's symbol graph.
func WorkspaceClusters(dataDir, workspaceID string) ([]FileCluster, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunSymbolgraph(context.Background(), "clusters", root, workspaceID)
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

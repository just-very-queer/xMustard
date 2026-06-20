package workspaceops

import (
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
	out, err := rustcore.RunSymbolgraph("build", root, workspaceID)
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
	out, err := rustcore.RunSymbolgraph("hotspots", root, workspaceID, strconv.Itoa(limit))
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
	out, err := rustcore.RunSymbolgraph("blast-radius", root, workspaceID, symbol)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
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
	out, err := rustcore.RunSymbolgraph("clusters", root, workspaceID)
	if err != nil {
		return nil, err
	}
	var clusters []FileCluster
	if err := json.Unmarshal(out, &clusters); err != nil {
		return nil, err
	}
	return clusters, nil
}

// PathCluster returns the cluster a file belongs to (its functional neighbourhood),
// for enriching `explain` with where the file sits in the repo's communities.
func PathCluster(dataDir, workspaceID, path string) (*FileCluster, error) {
	clusters, err := WorkspaceClusters(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range clusters {
		for _, f := range clusters[i].Files {
			if f == path {
				return &clusters[i], nil
			}
		}
	}
	return nil, nil
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

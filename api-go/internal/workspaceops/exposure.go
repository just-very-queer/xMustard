package workspaceops

import (
	"path/filepath"
	"strings"
)

// IsSafeID reports whether id is a plain identifier: letters, digits, "_", "." and
// "-", with no "..". Wildcards and delimiters ("*", "?", "/", ",", ":" ...) never
// pass, so an id can't widen a scope or address another store (PAR-SEC-05).
func IsSafeID(id string) bool { return validateSafeID("id", id) == nil }

// WorkspaceIDForRoot returns the workspace id LoadWorkspace would use for rootPath:
// the id of an already registered workspace with that root, else the id derived from
// the path. It lets the API check a workspace allowlist before registering a root.
func WorkspaceIDForRoot(dataDir, rootPath string) (string, error) {
	root, err := filepath.Abs(strings.TrimSpace(rootPath))
	if err != nil {
		return "", err
	}
	if items, err := ListWorkspaces(dataDir); err == nil {
		for _, it := range items {
			if filepath.Clean(it.RootPath) == root {
				return it.WorkspaceID, nil
			}
		}
	}
	return workspaceIDForPath(root), nil
}

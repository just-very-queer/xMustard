package workspaceops

import (
	"os"
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
	found, err := LookupWorkspaceRoot(dataDir, rootPath)
	return found.ID, err
}

// WorkspaceRootLookup is what the registry holds for a root before it is loaded.
type WorkspaceRootLookup struct {
	ID         string // the registered id, else the id LoadWorkspace would derive
	Name       string // the registered name ("" when not registered)
	RootPath   string // the root as registered, else the root as given (absolute)
	Registered bool
}

// LookupWorkspaceRoot finds the registered workspace whose root is rootPath,
// compared as LoadWorkspace stores it (absolute and cleaned, symlinks kept).
func LookupWorkspaceRoot(dataDir, rootPath string) (WorkspaceRootLookup, error) {
	return lookupWorkspace(dataDir, rootPath, false)
}

// LookupWorkspaceDir is LookupWorkspaceRoot, but when no registered root is spelled
// like rootPath it also matches a registered root that is the same directory
// (os.SameFile): one reached through a symlink, or spelled in another case on a
// case-insensitive filesystem. A non-admin load uses it, so that one directory
// never becomes several workspaces. A root a non-admin registered matches only as
// the directory it is, not through a symlink swapped in for it later.
func LookupWorkspaceDir(dataDir, rootPath string) (WorkspaceRootLookup, error) {
	return lookupWorkspace(dataDir, rootPath, true)
}

func lookupWorkspace(dataDir, rootPath string, sameDir bool) (WorkspaceRootLookup, error) {
	root, err := filepath.Abs(strings.TrimSpace(rootPath))
	if err != nil {
		return WorkspaceRootLookup{}, err
	}
	found := WorkspaceRootLookup{ID: workspaceIDForPath(root), RootPath: root}
	items, err := ListWorkspaces(dataDir)
	if err != nil {
		return found, nil
	}
	registered := func(it workspaceRecord) WorkspaceRootLookup {
		return WorkspaceRootLookup{ID: it.WorkspaceID, Name: it.Name, RootPath: it.RootPath, Registered: true}
	}
	for _, it := range items {
		if filepath.Clean(it.RootPath) == root {
			return registered(it), nil
		}
	}
	want, err := os.Stat(root)
	if !sameDir || err != nil {
		return found, nil
	}
	for _, it := range items {
		stat := os.Stat
		if it.RegisterRoot != "" {
			stat = os.Lstat
		}
		if info, err := stat(it.RootPath); err == nil && os.SameFile(want, info) {
			return registered(it), nil
		}
	}
	return found, nil
}

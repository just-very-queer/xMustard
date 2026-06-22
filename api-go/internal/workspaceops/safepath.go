package workspaceops

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
)

// Workspace path confinement. Several surfaces accept caller-controlled relative
// paths (governed-memory references, file explain/symbols, provider image input)
// and read them with server privileges. Without confinement, `../` traversal,
// absolute paths, or a symlink inside the repo pointing out turn those surfaces
// into an arbitrary host-file read/hash oracle. resolveWorkspacePath is the single
// choke point: it rejects escapes lexically AND after symlink resolution, and the
// read helpers add a regular-file requirement plus a byte cap.

// maxRefFileBytes caps a referenced file read (hash baseline, etc.) so a memory or
// image reference can't force unbounded I/O / RSS by pointing at a huge file.
const maxRefFileBytes = 8 << 20 // 8 MiB

var (
	errNoRoot     = errors.New("no workspace root")
	errEmptyPath  = errors.New("empty or invalid path")
	errAbsPath    = errors.New("absolute path not allowed")
	errEscape     = errors.New("path escapes workspace root")
	errNotRegular = errors.New("not a regular file")
)

// escapesRoot reports whether `target` lexically escapes `root`.
func escapesRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveWorkspacePath confines a caller-supplied relative path to `root`. It
// rejects absolute paths and `..` traversal, and — when the target (or any prefix)
// exists — resolves symlinks and re-checks containment so a symlink inside the repo
// cannot point outside it. The file need not exist (the lexically-contained join is
// returned so callers can represent "missing" themselves).
func resolveWorkspacePath(root, rel string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errNoRoot
	}
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", errEmptyPath
	}
	if filepath.IsAbs(rel) {
		return "", errAbsPath
	}
	// canonicalize the root so symlinked roots compare correctly.
	croot, err := filepath.EvalSymlinks(root)
	if err != nil {
		croot = filepath.Clean(root)
	}
	joined := filepath.Join(croot, rel)
	if escapesRoot(croot, joined) {
		return "", errEscape
	}
	// If the target (or its existing parent prefix) resolves through symlinks to a
	// location outside the root, reject. EvalSymlinks needs an existing path, so try
	// the target first, then walk up to the nearest existing ancestor.
	probe := joined
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if probe == joined {
				if escapesRoot(croot, resolved) {
					return "", errEscape
				}
				return resolved, nil
			}
			// an ancestor resolved; ensure that ancestor is still inside root, then
			// return the lexically-contained join for the (not-yet-existing) target.
			if escapesRoot(croot, resolved) {
				return "", errEscape
			}
			return joined, nil
		}
		parent := filepath.Dir(probe)
		if parent == probe || escapesRoot(croot, parent) {
			// reached the root (or above) without a symlink escape.
			return joined, nil
		}
		probe = parent
	}
}

// readWorkspaceRegularFile confines rel beneath root WITHOUT a check-to-open race,
// requires a regular file, and reads at most maxRefFileBytes. Returns (data, true)
// on success; (nil, false) for missing/unreadable/escaping/non-regular/oversized —
// callers treat false as "not a trustworthy in-repo file". The file is opened via a
// symlink-refusing fd-walk and then stat+read happen on THAT SAME fd, so an agent
// swapping the path for a symlink after validation can't redirect the read
// (XM-PRO-007).
func readWorkspaceRegularFile(root, rel string) ([]byte, bool) {
	f, err := openWorkspaceFileBeneath(root, rel)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat() // fstat on the held fd — not a re-stat by path
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	if info.Size() > maxRefFileBytes {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRefFileBytes+1))
	if err != nil || int64(len(data)) > maxRefFileBytes {
		return nil, false
	}
	return data, true
}

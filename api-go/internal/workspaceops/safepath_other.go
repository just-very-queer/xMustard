//go:build !unix

package workspaceops

import (
	"os"
	"path/filepath"
)

// openWorkspaceFileBeneath fallback for non-unix platforms (no openat/O_NOFOLLOW):
// retains the lexical + symlink-resolution confinement of resolveWorkspacePath, but
// not the race-free fd-walk. xMustard's runtime targets are unix; this exists so the
// package still builds elsewhere.
func openWorkspaceFileBeneath(root, rel string) (*os.File, error) {
	abs, err := resolveWorkspacePath(root, rel)
	if err != nil {
		return nil, err
	}
	return os.Open(abs)
}

// createWorkspaceFileBeneath fallback (non-unix): lexical/symlink-resolution confinement,
// no race-free fd-walk. Honors overwrite and reports prior existence.
func createWorkspaceFileBeneath(root, rel string, overwrite bool) (*os.File, bool, error) {
	abs, err := resolveWorkspacePath(root, rel)
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, false, err
	}
	_, statErr := os.Lstat(abs)
	existed := statErr == nil
	if existed && !overwrite {
		return nil, true, nil
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(abs, flags, 0o644)
	if err != nil {
		return nil, existed, err
	}
	return f, existed && overwrite, nil
}

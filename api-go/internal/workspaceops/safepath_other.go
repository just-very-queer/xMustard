//go:build !unix

package workspaceops

import "os"

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

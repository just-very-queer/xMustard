//go:build unix

package workspaceops

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openWorkspaceFileBeneath opens rel relative to root WITHOUT following a symlink at
// ANY path component: it opens the root directory, then walks each component with
// openat()+O_NOFOLLOW. Because the returned fd is reached purely through
// directory-relative opens that refuse to traverse symlinks, the file a caller
// reads/stats from that fd is exactly the validated in-repo file — a concurrent
// agent cannot swap a validated regular file for a symlink between check and open
// (the check-to-open TOCTOU, XM-PRO-007). It rejects absolute paths, "..", NUL, and
// any symlink component (ELOOP -> errEscape). Symlinks are refused outright (the
// race-free policy); legitimate in-repo symlinks are rare in source trees and the
// confinement guarantee is worth more than following them.
func openWorkspaceFileBeneath(root, rel string) (*os.File, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errNoRoot
	}
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.ContainsRune(rel, 0) {
		return nil, errEmptyPath
	}
	if filepath.IsAbs(rel) {
		return nil, errAbsPath
	}

	rootFd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errNoRoot
	}
	cur := rootFd
	closeCur := func() {
		if cur >= 0 {
			_ = unix.Close(cur)
			cur = -1
		}
	}

	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			// filepath.Clean already collapses interior "..", so a leading ".." here is
			// a genuine escape attempt.
			closeCur()
			return nil, errEscape
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(cur, part, flags, 0)
		closeCur() // close the parent fd whether or not the child opened
		if err != nil {
			if err == unix.ELOOP {
				return nil, errEscape // O_NOFOLLOW hit a symlink component
			}
			return nil, err
		}
		cur = next
	}

	if cur == rootFd {
		// rel cleaned to "." — that's the root directory, not a file.
		closeCur()
		return nil, errEmptyPath
	}
	return os.NewFile(uintptr(cur), filepath.Join(root, rel)), nil
}

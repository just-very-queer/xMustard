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
	// opened tracks whether any component was opened. Comparing cur with rootFd is not
	// enough: once rootFd is closed, a later openat may reuse its number.
	opened := false
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
		opened = true
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

	if !opened {
		// rel cleaned to "." — that's the root directory, not a file.
		closeCur()
		return nil, errEmptyPath
	}
	return os.NewFile(uintptr(cur), filepath.Join(root, rel)), nil
}

// createWorkspaceFileBeneath opens rel under root FOR WRITING without following a symlink
// at ANY component — the write counterpart of openWorkspaceFileBeneath. It walks (and, if
// missing, mkdirat-creates) intermediate directories no-follow, then opens the final file
// O_NOFOLLOW|O_CREAT (plus O_EXCL when overwrite is false). The O_NOFOLLOW on the final
// open means a symlink an agent planted at the target path is REFUSED (ELOOP) instead of
// being followed — so generating a starter file can never become an arbitrary host-file
// write. Returns the fd, whether the file already existed, and an error. When the file
// exists and overwrite is false, returns (nil, true, nil) — caller skips.
func createWorkspaceFileBeneath(root, rel string, overwrite bool) (f *os.File, existed bool, err error) {
	if strings.TrimSpace(root) == "" {
		return nil, false, errNoRoot
	}
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.ContainsRune(rel, 0) {
		return nil, false, errEmptyPath
	}
	if filepath.IsAbs(rel) {
		return nil, false, errAbsPath
	}

	rootFd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, errNoRoot
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
			closeCur()
			return nil, false, errEscape
		}
		if i < len(parts)-1 {
			// intermediate directory: create it if missing (no-follow), then descend.
			if err := unix.Mkdirat(cur, part, 0o755); err != nil && err != unix.EEXIST {
				closeCur()
				return nil, false, err
			}
			next, err := unix.Openat(cur, part, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			closeCur()
			if err != nil {
				if err == unix.ELOOP {
					return nil, false, errEscape
				}
				return nil, false, err
			}
			cur = next
			continue
		}
		// final component: open for writing, never following a symlink at the target.
		flags := unix.O_WRONLY | unix.O_NOFOLLOW | unix.O_CREAT | unix.O_CLOEXEC
		if !overwrite {
			flags |= unix.O_EXCL
		} else {
			flags |= unix.O_TRUNC
		}
		fd, oerr := unix.Openat(cur, part, flags, 0o644)
		if oerr == nil {
			// O_EXCL success => newly created; O_TRUNC success => existed (overwrite).
			closeCur2 := cur
			cur = -1
			_ = unix.Close(closeCur2)
			return os.NewFile(uintptr(fd), filepath.Join(root, rel)), overwrite, nil
		}
		if oerr == unix.EEXIST && !overwrite {
			closeCur()
			return nil, true, nil // exists and not overwriting: caller skips
		}
		closeCur()
		if oerr == unix.ELOOP {
			return nil, false, errEscape // a symlink was planted at the target — refused
		}
		return nil, false, oerr
	}
	closeCur()
	return nil, false, errEmptyPath
}

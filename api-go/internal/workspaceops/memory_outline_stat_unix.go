//go:build unix

package workspaceops

import (
	"os"
	"syscall"
)

// fileMarkFromInfo is a fileMark for an already-open descriptor's FileInfo, the same
// identity statMark reads by path. It exists so a caller holding an fd (the workspace
// reader's symlink-refusing open) can key a cache entry on what it actually read.
//
// os.FileInfo.Sys() on unix returns *syscall.Stat_t, which is a distinct type from
// x/sys/unix.Stat_t even where they carry the same fields, so the assertion must be to
// syscall.Stat_t. Asserting to unix.Stat_t silently always fails, and the mark degrades to
// size and mtime with no inode or ctime, which is what an atomic same-size replacement
// preserves.
func fileMarkFromInfo(info os.FileInfo) fileMark {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileMark{exists: true, size: info.Size(), mtimeNs: info.ModTime().UnixNano()}
	}
	return fileMark{exists: true, size: st.Size, mtimeNs: st.Mtim.Nano(), ctimeNs: st.Ctim.Nano(),
		ino: uint64(st.Ino), dev: uint64(st.Dev)}
}

//go:build unix

package workspaceops

import (
	"os"

	"golang.org/x/sys/unix"
)

// fileMarkFromInfo is a fileMark for an already-open descriptor's FileInfo, the same
// identity statMark reads by path. It exists so a caller holding an fd (the workspace
// reader's symlink-refusing open) can key a cache entry on what it actually read.
func fileMarkFromInfo(info os.FileInfo) fileMark {
	st, ok := info.Sys().(*unix.Stat_t)
	if !ok {
		return fileMark{exists: true, size: info.Size(), mtimeNs: info.ModTime().UnixNano()}
	}
	return fileMark{exists: true, size: st.Size, mtimeNs: st.Mtim.Nano(), ctimeNs: st.Ctim.Nano(),
		ino: uint64(st.Ino), dev: uint64(st.Dev)}
}

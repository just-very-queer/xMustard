//go:build unix

package workspaceops

import (
	"os"

	"golang.org/x/sys/unix"
)

// fileMarkFromFile is a fileMark for the descriptor the workspace reader opened, so cache
// identity includes the inode and ctime on every Unix platform.
func fileMarkFromFile(f *os.File, info os.FileInfo) fileMark {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return fileMark{exists: true, size: info.Size(), mtimeNs: info.ModTime().UnixNano()}
	}
	return fileMark{exists: true, size: st.Size, mtimeNs: st.Mtim.Nano(), ctimeNs: st.Ctim.Nano(),
		ino: uint64(st.Ino), dev: uint64(st.Dev)}
}

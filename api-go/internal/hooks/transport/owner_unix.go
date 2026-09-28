//go:build unix

package transport

import (
	"os"
	"syscall"
)

// ownedByMe reports whether this process's user owns the file.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

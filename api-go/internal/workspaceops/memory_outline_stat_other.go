//go:build !unix

package workspaceops

import "os"

// fileMarkFromFile is a fileMark for the opened descriptor's FileInfo. Off unix there is no
// inode or ctime to read, which is the same limitation statMark has there.
func fileMarkFromFile(_ *os.File, info os.FileInfo) fileMark {
	return fileMark{exists: true, size: info.Size(), mtimeNs: info.ModTime().UnixNano()}
}

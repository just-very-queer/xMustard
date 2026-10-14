//go:build !linux && !darwin

package workspaceops

import "os"

// fileMarkFromInfo is a fileMark for an already-open descriptor's FileInfo. Off unix
// there is no inode or ctime to read, which is the same limitation statMark has there.
func fileMarkFromInfo(info os.FileInfo) fileMark {
	return fileMark{exists: true, size: info.Size(), mtimeNs: info.ModTime().UnixNano()}
}

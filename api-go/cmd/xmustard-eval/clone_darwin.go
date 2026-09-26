package main

import "golang.org/x/sys/unix"

// cloneDir clones the directory src to dst (which must not exist) in one clonefile
// call: copy-on-write on APFS, with symbolic links cloned as links.
func cloneDir(src, dst string) error {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW|unix.CLONE_NOOWNERCOPY)
}

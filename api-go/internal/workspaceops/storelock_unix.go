//go:build unix

package workspaceops

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive flock on path, creating it (and its directory) if
// needed, and returns the release function. With wait it blocks until the lock is
// free; without, a held lock fails at once with errStoreBusy.
func lockFile(path string, wait bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	fd, how := int(f.Fd()), unix.LOCK_EX
	if !wait {
		how |= unix.LOCK_NB
	}
	for {
		err = unix.Flock(fd, how)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		err = errStoreBusy
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = f.Close()
	}, nil
}

// syncDir flushes a directory's entries, so a rename into it survives a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

//go:build unix

package workspaceops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// flockWaitStep is how long a cancellable lockFile sleeps between its flock attempts. It
// bounds how long after ctx ends the wait notices, at the cost of one flock syscall per
// step per waiter — a waiter exists only for a call already in flight, and there is one
// per store key per request, so the rate is bounded by in-flight requests and not by
// repository size.
const flockWaitStep = 20 * time.Millisecond

// lockFile takes an exclusive flock on path, creating it (and its directory) if
// needed, and returns the release function. With wait it blocks until the lock is free,
// or until ctx ends. A cancellable wait is a non-blocking flock and a sleep between
// attempts, because unix.Flock has no cancellable form and a blocking one parks in the
// kernel where no ctx can reach it; a wait that gives up closes its descriptor without
// having taken the lock. A nil ctx waits unconditionally, which is what the
// context-free callers get.
func lockFile(ctx context.Context, path string, wait bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	fd, how := int(f.Fd()), unix.LOCK_EX
	if !wait || ctx != nil {
		how |= unix.LOCK_NB
	}
	for {
		if err = flockRetry(fd, how); !errors.Is(err, unix.EWOULDBLOCK) || ctx == nil {
			break
		}
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		time.Sleep(flockWaitStep)
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

// flockRetry is unix.Flock with its EINTR retry, the one wait that has no cancellable
// form because the caller asked to block.
func flockRetry(fd, how int) error {
	for {
		err := unix.Flock(fd, how)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
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

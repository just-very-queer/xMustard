//go:build unix

package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"xmustard/api-go/internal/budget"
)

// openRegularNoFollow opens an operator-supplied absolute path read-only, refusing a
// symlink at the final component (O_NOFOLLOW); the caller fstat-checks regularity.
func openRegularNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if err == unix.ELOOP {
			return nil, errEscape
		}
		if err == unix.ENOENT {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// lockDiagnosticsStore takes an exclusive cross-process flock on the lock file,
// retrying LOCK_NB for at most wait. Contention wraps budget.ErrOverloaded (503).
// The kernel drops the lock if the holder dies, so no stale-lock guessing is needed.
func lockDiagnosticsStore(ctx context.Context, lockFile *os.File, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, fmt.Errorf("lock diagnostics store: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w (another diagnostics import holds this workspace's store)", budget.ErrOverloaded)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

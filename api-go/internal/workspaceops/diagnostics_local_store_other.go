//go:build !unix

package workspaceops

import (
	"context"
	"os"
	"time"
)

func openRegularNoFollow(path string) (*os.File, error) {
	return nil, ErrDiagnosticsStoreUnsupported
}

// lockDiagnosticsStore fails closed: without flock there is no safe cross-process
// lock, and guessing stale-lock age could let two writers publish at once.
func lockDiagnosticsStore(ctx context.Context, lockFile *os.File, wait time.Duration) (func(), error) {
	return nil, ErrDiagnosticsStoreUnsupported
}

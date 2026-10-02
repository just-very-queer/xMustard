//go:build !unix

package workspaceops

import "context"

// lockFile has no cross-process lock on non-unix platforms; lockStore's in-process
// gate still applies and is cancellable, so a ctx that ends stops the wait there.
// xMustard's runtime targets are unix; this keeps the package building elsewhere.
func lockFile(context.Context, string, bool) (func(), error) { return func() {}, nil }

// syncDir is a no-op where directories cannot be opened for fsync.
func syncDir(string) error { return nil }

//go:build !unix

package workspaceops

// lockFile has no cross-process lock on non-unix platforms; lockStore's in-process
// mutex still applies. xMustard's runtime targets are unix; this keeps the package
// building elsewhere.
func lockFile(string, bool) (func(), error) { return func() {}, nil }

// syncDir is a no-op where directories cannot be opened for fsync.
func syncDir(string) error { return nil }

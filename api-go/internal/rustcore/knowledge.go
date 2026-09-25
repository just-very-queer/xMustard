package rustcore

import "context"

// runCore routes through the shared hardened bridge runner (timeout, bounded
// output, sanitized errors, server-side logs, caller cancellation — see runCoreCtx).
func runCore(ctx context.Context, sub string, args ...string) ([]byte, error) {
	return runCoreCtx(ctx, sub, args...)
}

// RunSearch runs hybrid repo search; RunWiki generates the repo wiki.
func RunSearch(ctx context.Context, args ...string) ([]byte, error) {
	return runCore(ctx, "search", args...)
}
func RunWiki(ctx context.Context, args ...string) ([]byte, error) {
	return runCore(ctx, "wiki", args...)
}

// RunOwnership runs the ownership/subsystem model commands.
func RunOwnership(ctx context.Context, args ...string) ([]byte, error) {
	return runCore(ctx, "ownership", args...)
}

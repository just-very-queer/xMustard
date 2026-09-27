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

// RunIndex runs `xmustard-core index <build|update|stats> <root> [flags]`, the code
// index worker. It is one-shot by design: its parse and write peak runs in a transient
// process under the heavy slot, never in the resident worker, which only reads the
// graph segment the update writes.
func RunIndex(ctx context.Context, args ...string) ([]byte, error) {
	return runCore(ctx, "index", args...)
}

// RunOwnership runs the ownership/subsystem model commands.
func RunOwnership(ctx context.Context, args ...string) ([]byte, error) {
	return runCore(ctx, "ownership", args...)
}

package rustcore

import "context"

// RunChangetrack runs `xmustard-core changetrack <args...>` and returns stdout.
// The Rust core owns gitnexus-style change tracking; this is the delivery bridge
// (timeout + bounded output + sanitized errors + caller cancellation via runCoreCtx).
func RunChangetrack(ctx context.Context, args ...string) ([]byte, error) {
	return runCoreCtx(ctx, "changetrack", args...)
}

// RunRepoKey runs `xmustard-core repo-key <root>`: the repository identity key used to
// label evidence freshness (see workspaceops.RepoIdentity for the JSON contract).
func RunRepoKey(ctx context.Context, root string) ([]byte, error) {
	return runCoreCtx(ctx, "repo-key", root)
}

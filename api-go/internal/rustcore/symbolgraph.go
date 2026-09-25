package rustcore

import "context"

// RunSymbolgraph runs `xmustard-core symbolgraph <args...>` (symbol graph,
// hotspots, blast radius) via the shared hardened bridge runner. Cancelling ctx
// kills the child.
func RunSymbolgraph(ctx context.Context, args ...string) ([]byte, error) {
	return runCoreCtx(ctx, "symbolgraph", args...)
}

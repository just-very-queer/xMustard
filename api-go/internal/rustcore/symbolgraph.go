package rustcore

import "context"

// RunSymbolgraph runs `xmustard-core symbolgraph <args...>` (symbol graph,
// hotspots, blast radius) via the shared hardened bridge runner. Cancelling ctx
// kills the child. impact, impact-file, trace, clusters, cluster-of and hotspots read
// the code index's snapshot when the root has an index (resident in the worker) and
// the legacy graph otherwise; impact, impact-file, trace and cluster-of carry
// freshness and coverage.
func RunSymbolgraph(ctx context.Context, args ...string) ([]byte, error) {
	return runCoreCtx(ctx, "symbolgraph", args...)
}

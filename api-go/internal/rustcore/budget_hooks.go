package rustcore

import "xmustard/api-go/internal/budget"

// Budget hooks for bridge children (PAR-RT-04): per-call caps derived from the
// transient pool.

// requestReplyHeadroom is left in the pool after a request-scoped call's output and its
// two decode copies, for the response built from it (the evidence envelope holds two
// copies of a projection of at most 1 MiB); a quarter of a smaller pool.
const requestReplyHeadroom = 3 << 20

// coreStdoutCap is one call's stdout cap: maxCoreStdout, lowered to what the scope's
// pool could ever admit, so output that can never be held takes the permanent "output
// too large" path instead of a retryable overload. A request-scoped call holds its
// output plus two more copies until the response is written, so it gets a third of the
// pool after stderr and reply headroom; a call that owns its scope gets the pool less
// stderr.
func coreStdoutCap(scope *budget.Scope, requestScoped bool) int {
	limit := int64(maxCoreStdout)
	if pool := scope.PoolMax(); pool > 0 {
		room := pool - maxCoreStderr
		if requestScoped {
			room = (room - min(requestReplyHeadroom, pool/4)) / 3
		}
		limit = min(limit, max(room, 0))
	}
	return int(limit)
}

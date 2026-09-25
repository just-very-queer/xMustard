package rustcore

import (
	"os/exec"
	"strings"

	"xmustard/api-go/internal/budget"
)

// Budget hooks for bridge children (PAR-EVAL-04, PAR-RT-04, PAR-RT-05). They live here,
// not in children.go, so the generic tracking there (TrackChild, startTracked) stays
// free of accounting: each spawn is counted where its kind is known.

// noteCoreSpawn counts cmd as a rust-core spawn when it actually started (a missing
// binary starts nothing).
func noteCoreSpawn(cmd *exec.Cmd) {
	if cmd.Process != nil {
		budget.NoteSpawn(budget.SpawnCore)
	}
}

// childEnv is budget.ChildEnv; a variable so tests can supply the Linux answer on any
// platform.
var childEnv = budget.ChildEnv

// coreChildEnv is the environment for a rust-core child running sub: nil (inherit)
// unless budget.ChildEnv adds the Linux allocator cap. Subcommands that run external
// programs never get it: the core hands its environment unchanged to what it spawns,
// and a language server or the user's verification and managed commands are external
// processes (PARITY_REQUIREMENTS §7.6) that must run in the operator's environment.
func coreChildEnv(sub string) []string {
	if runsExternalPrograms(sub) {
		return nil
	}
	return childEnv()
}

// runsExternalPrograms reports the subcommands that start programs outside xMustard:
// live LSP sessions (lsp-*) and verification and managed commands (run-*).
func runsExternalPrograms(sub string) bool {
	return strings.HasPrefix(sub, "lsp-") || strings.HasPrefix(sub, "run-")
}

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

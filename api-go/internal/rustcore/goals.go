package rustcore

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// goalCommandTimeout bounds a goal CLI invocation so a hung Rust child can't wedge
// the HTTP handler indefinitely.
const goalCommandTimeout = 60 * time.Second

// ErrGoalNotFound corresponds to the Rust goal CLI exit code 4 (goal or
// workspace not found), so callers can map it onto a 404.
var ErrGoalNotFound = errors.New("rust-core goal: not found")

// RunGoalCommand executes `xmustard-core goal <args...>` against the Rust core
// and returns raw stdout. The Rust runtime is the single owner of goal logic;
// this is the delivery shim. Exit code 4 is surfaced as ErrGoalNotFound; all
// other non-zero exits (validation, slop, or completion-gate refusals) carry
// the Rust stderr message.
func RunGoalCommand(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), goalCommandTimeout)
	defer cancel()
	cmd := coreCommandContext(ctx, "goal", args...)

	stdout, stderr, over, err := runBoundedCmd(cmd)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 4 {
			return nil, ErrGoalNotFound
		}
		// keep the goal runtime's own message (validation/slop/gate text is
		// user-facing) but bound it so a huge child stderr can't be echoed wholesale.
		return nil, fmt.Errorf("rust-core goal %v: %w: %s", args, err, truncateForError(stderr))
	}
	if over {
		return nil, fmt.Errorf("rust-core goal %v: output too large", args)
	}
	return stdout, nil
}

// truncateForError bounds an error fragment so large child output isn't echoed.
func truncateForError(s string) string {
	const max = 2000
	if len(s) > max {
		return s[:max] + "…(truncated)"
	}
	return s
}

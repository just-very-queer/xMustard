package rustcore

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
)

// ErrGoalNotFound corresponds to the Rust goal CLI exit code 4 (goal or
// workspace not found), so callers can map it onto a 404.
var ErrGoalNotFound = errors.New("rust-core goal: not found")

// RunGoalCommand executes `xmustard-core goal <args...>` against the Rust core
// and returns raw stdout. The Rust runtime is the single owner of goal logic;
// this is the delivery shim. Exit code 4 is surfaced as ErrGoalNotFound; all
// other non-zero exits (validation, slop, or completion-gate refusals) carry
// the Rust stderr message.
func RunGoalCommand(args ...string) ([]byte, error) {
	full := append([]string{"run", "--quiet", "--bin", "xmustard-core", "--", "goal"}, args...)
	cmd := exec.Command("cargo", full...)
	cmd.Dir = rustCoreDir()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 4 {
			return nil, ErrGoalNotFound
		}
		return nil, fmt.Errorf("rust-core goal %v: %w: %s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

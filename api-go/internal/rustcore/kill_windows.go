//go:build windows

package rustcore

import "os/exec"

// IsolateProcessTree is a no-op on Windows: cancellation kills only the immediate
// child (exec.CommandContext's default). Descendants are not tracked there.
func IsolateProcessTree(cmd *exec.Cmd) {}

// KillProcessTree kills the immediate child only (see IsolateProcessTree).
func KillProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func isolateProcessGroup(cmd *exec.Cmd) {}

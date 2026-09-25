//go:build !darwin && !linux

package workspaceops

import (
	"os/exec"
	"time"
)

// killTerminalSession kills the shell. Terminals cannot open on these platforms
// (see terminal_pty_stub.go), so no session exists to sweep.
func killTerminalSession(cmd *exec.Cmd, grace time.Duration) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

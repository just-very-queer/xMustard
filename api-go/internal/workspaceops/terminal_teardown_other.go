//go:build !darwin && !linux

package workspaceops

// Terminals cannot open on these platforms (see terminal_pty_stub.go), so there is
// no session to sweep.

func endTerminalSession(session *terminalSession, mode terminalTeardown) {
	if mode == terminalKill && session.process != nil && session.process.Process != nil {
		_ = session.process.Process.Kill()
	}
}

func terminalProcStart(pid int) (uint64, bool) { return 0, false }

func waitTerminalShellExit(pid int) bool { return false }

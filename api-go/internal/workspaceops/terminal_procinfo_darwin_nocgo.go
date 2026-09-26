//go:build darwin && !cgo

package workspaceops

// Without cgo terminals cannot open (terminal_pty_stub.go), so no replica exists
// to look for.

func terminalTTYHolders(ttyPath string, procs []terminalProc) map[int]bool { return nil }

func terminalProcCwd(pid int) (string, bool) { return "", false }

//go:build unix

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// startGroup starts cmd as the leader of a new process group, so the whole tree the
// client spawns (MCP servers, shells, test runners) can be signalled together.
func startGroup(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return cmd.Start()
}

// killGroup sends SIGTERM to the process group led by pid, waits up to grace for it
// to empty, then sends SIGKILL. It is safe to call after the leader has exited.
func killGroup(pid int, grace time.Duration) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && errors.Is(err, syscall.ESRCH) {
		return
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// groupAlive reports whether any process of the group led by pid is still running.
func groupAlive(pid int) bool {
	return pid > 0 && syscall.Kill(-pid, 0) == nil
}

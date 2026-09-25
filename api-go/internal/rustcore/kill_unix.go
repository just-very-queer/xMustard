//go:build !windows

package rustcore

import (
	"os/exec"
	"syscall"
)

// IsolateProcessTree starts cmd as the leader of a new process group and makes
// context cancellation kill that whole group, so helpers the child spawns (ast-grep,
// language servers) end with it instead of outliving the request.
// cmd must come from exec.CommandContext (Go rejects Cancel otherwise); use
// isolateProcessGroup for a plain exec.Command.
func IsolateProcessTree(cmd *exec.Cmd) {
	isolateProcessGroup(cmd)
	cmd.Cancel = func() error {
		KillProcessTree(cmd)
		return nil
	}
}

// KillProcessTree kills a started command, including its process group when the
// command was started as a group leader. Safe to call after exit: while any group
// member survives the group id cannot be reused, and an empty group is ESRCH.
func KillProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
}

// isolateProcessGroup starts cmd as a new process-group leader without touching
// cmd.Cancel, for commands built with exec.Command.
func isolateProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

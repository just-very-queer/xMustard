//go:build !unix

package main

import (
	"os"
	"os/exec"
	"time"
)

// Process groups are a unix facility; elsewhere only the direct child is signalled.
func startGroup(cmd *exec.Cmd) error { return cmd.Start() }

func killGroup(pid int, _ time.Duration) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func groupAlive(int) bool { return false }

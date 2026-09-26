package main

import "syscall"

// groupAlive reports whether any process of the group led by pid is still running.
func groupAlive(pid int) bool {
	return pid > 0 && syscall.Kill(-pid, 0) == nil
}

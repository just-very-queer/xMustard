//go:build darwin

package main

import "golang.org/x/sys/unix"

func procStartTime(pid int) (int64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return 0, false
	}
	t := kp.Proc.P_starttime
	return int64(t.Sec)*1e6 + int64(t.Usec), true
}

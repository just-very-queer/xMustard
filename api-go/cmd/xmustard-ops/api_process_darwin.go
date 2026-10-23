//go:build darwin

package main

import "golang.org/x/sys/unix"

const darwinZombieState = 5 // SZOMB in Darwin's sys/proc.h.

func apiProcessIdentity(pid int) (int64, string, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) || kp.Proc.P_stat == darwinZombieState {
		return 0, "", false
	}
	t := kp.Proc.P_starttime
	return int64(t.Sec)*1e6 + int64(t.Usec), unix.ByteSliceToString(kp.Proc.P_comm[:]), true
}

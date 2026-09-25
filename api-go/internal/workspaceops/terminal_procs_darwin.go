//go:build darwin

package workspaceops

import "golang.org/x/sys/unix"

// sZOMB is p_stat for a zombie in <sys/proc.h>.
const sZOMB = 5

// listTerminalProcs reads the process table with sysctl kern.proc.all. The session
// id is not in kinfo_proc (only a kernel pointer), so it comes from getsid(2).
// Processes that exit while the table is being read are skipped.
func listTerminalProcs() ([]terminalProc, error) {
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	procs := make([]terminalProc, 0, len(infos))
	for i := range infos {
		info := &infos[i]
		pid := int(info.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		exited := info.Proc.P_stat == sZOMB
		sid := 0
		if !exited {
			if sid, err = unix.Getsid(pid); err != nil {
				continue
			}
		}
		start := info.Proc.P_starttime
		procs = append(procs, terminalProc{
			pid:    pid,
			ppid:   int(info.Eproc.Ppid),
			pgid:   int(info.Eproc.Pgid),
			sid:    sid,
			start:  uint64(start.Sec)*1_000_000 + uint64(start.Usec),
			exited: exited,
		})
	}
	return procs, nil
}

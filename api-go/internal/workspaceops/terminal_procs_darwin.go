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
		procs = append(procs, terminalProc{
			pid:    pid,
			ppid:   int(info.Eproc.Ppid),
			pgid:   int(info.Eproc.Pgid),
			sid:    sid,
			start:  kinfoStart(info),
			exited: exited,
		})
	}
	return procs, nil
}

func kinfoStart(info *unix.KinfoProc) uint64 {
	start := info.Proc.P_starttime
	return uint64(start.Sec)*1_000_000 + uint64(start.Usec)
}

// terminalProcStart returns the start time of pid as listTerminalProcs reports it.
func terminalProcStart(pid int) (uint64, bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(info.Proc.P_pid) != pid {
		return 0, false
	}
	return kinfoStart(info), true
}

// waitTerminalShellExit blocks until the child pid has exited and reports whether
// it has, without reaping it: a kqueue NOTE_EXIT event fires on exit and leaves
// the zombie for wait(2).
func waitTerminalShellExit(pid int) bool {
	kq, err := unix.Kqueue()
	if err != nil {
		return false
	}
	defer unix.Close(kq)
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
	events := make([]unix.Kevent_t, 1)
	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		// ESRCH: the process has already exited; it stays a zombie until reaped
		return err == unix.ESRCH
	}
	for {
		n, err := unix.Kevent(kq, nil, events, nil)
		if err == unix.EINTR {
			continue
		}
		return err == nil && n == 1 && events[0].Fflags&unix.NOTE_EXIT != 0
	}
}

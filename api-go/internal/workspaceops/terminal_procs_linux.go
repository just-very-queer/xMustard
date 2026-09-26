//go:build linux

package workspaceops

import (
	"bytes"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// listTerminalProcs reads the process table from /proc. Processes that exit while
// the table is being read are skipped. One buffer serves every stat file, since
// teardown rereads the table while it waits.
func listTerminalProcs() ([]terminalProc, error) {
	dir, err := os.Open("/proc")
	if err != nil {
		return nil, err
	}
	names, err := dir.Readdirnames(-1)
	_ = dir.Close()
	if err != nil {
		return nil, err
	}
	procs := make([]terminalProc, 0, len(names))
	buf := make([]byte, 2048)
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		fd, err := unix.Open("/proc/"+name+"/stat", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		n, err := unix.Read(fd, buf)
		_ = unix.Close(fd)
		if err != nil || n <= 0 {
			continue
		}
		if proc, ok := parseProcStat(pid, buf[:n]); ok {
			procs = append(procs, proc)
		}
	}
	return procs, nil
}

// terminalProcStart returns the start time of pid as listTerminalProcs reports it.
func terminalProcStart(pid int) (uint64, bool) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	proc, ok := parseProcStat(pid, stat)
	return proc.start, ok
}

// waitTerminalShellExit blocks until the child pid has exited and reports whether
// it has, without reaping it (waitid with WNOWAIT).
func waitTerminalShellExit(pid int) bool {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err == nil
		}
	}
}

// terminalTTYHolders returns the live processes with a descriptor on ttyPath,
// read from /proc/<pid>/fd. Descriptors of other users' processes are unreadable,
// and those processes could not be signalled anyway. readlink never touches the
// file a descriptor names, so a hung network mount cannot block the scan.
func terminalTTYHolders(ttyPath string, procs []terminalProc) map[int]bool {
	holders := make(map[int]bool)
	link := make([]byte, len(ttyPath)+1)
	for _, proc := range procs {
		if proc.exited || proc.pid <= 1 {
			continue
		}
		dir := "/proc/" + strconv.Itoa(proc.pid) + "/fd/"
		handle, err := os.Open(dir)
		if err != nil {
			continue
		}
		names, _ := handle.Readdirnames(-1)
		_ = handle.Close()
		for _, name := range names {
			if n, err := unix.Readlink(dir+name, link); err == nil && string(link[:n]) == ttyPath {
				holders[proc.pid] = true
				break
			}
		}
	}
	return holders
}

// parseProcStat parses /proc/<pid>/stat (proc(5)). The command name is
// parenthesised and may itself contain spaces or ')', so fields are counted from
// the last ')'.
func parseProcStat(pid int, stat []byte) (terminalProc, bool) {
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return terminalProc{}, false
	}
	// fields after the name, 0-based: state ppid pgrp session ... starttime (19)
	var fields [20][]byte
	rest := stat[end+1:]
	for i := range fields {
		rest = bytes.TrimLeft(rest, " ")
		cut := bytes.IndexAny(rest, " \n")
		if cut < 0 {
			cut = len(rest)
		}
		if cut == 0 {
			return terminalProc{}, false
		}
		fields[i], rest = rest[:cut], rest[cut:]
	}
	ppid, ok1 := parseProcStatNumber(fields[1])
	pgid, ok2 := parseProcStatNumber(fields[2])
	sid, ok3 := parseProcStatNumber(fields[3])
	start, ok4 := parseProcStatNumber(fields[19])
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return terminalProc{}, false
	}
	state := fields[0]
	return terminalProc{
		pid:    pid,
		ppid:   int(ppid),
		pgid:   int(pgid),
		sid:    int(sid),
		start:  start,
		exited: len(state) == 1 && (state[0] == 'Z' || state[0] == 'X'),
	}, true
}

// parseProcStatNumber parses a non-negative decimal field without allocating.
func parseProcStatNumber(token []byte) (uint64, bool) {
	var value uint64
	for _, c := range token {
		if c < '0' || c > '9' {
			return 0, false
		}
		value = value*10 + uint64(c-'0')
	}
	return value, true
}

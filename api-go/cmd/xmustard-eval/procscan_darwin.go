package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// procStartTime identifies a process instance: its start time in microseconds, so a
// recorded pid that the system has since reused is not mistaken for the original.
func procStartTime(pid int) (int64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return 0, false
	}
	tv := kp.Proc.P_starttime
	return int64(tv.Sec)*1e6 + int64(tv.Usec), true
}

// pidsWithEnv lists processes whose environment contains the exact entry kv, from
// `ps -E`. Recent macOS releases no longer expose other processes' environments
// (kern.procargs2 returns argv only), so this finds nothing there; the sweep then
// relies on the sampled process tree and working directories.
func pidsWithEnv(kv string) ([]int, error) {
	out, err := exec.Command("ps", "-E", "-ww", "-A", "-o", "pid=", "-o", "command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps -E: %w", err)
	}
	var pids []int
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || !slices.Contains(f[1:], kv) {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, sc.Err()
}

// pidsWithCwdIn lists this user's processes whose working directory is one of dirs
// or below one, from one lsof call.
func pidsWithCwdIn(dirs []string) ([]int, error) {
	cmd := exec.Command("lsof", "-w", "-n", "-P", "-a", "-d", "cwd", "-u", strconv.Itoa(os.Getuid()), "-F", "pn")
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("lsof: %w", err)
	}
	var pids []int
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if pid > 0 && cwdWithin(line[1:], dirs) {
				pids = append(pids, pid)
			}
		}
	}
	return pids, nil
}

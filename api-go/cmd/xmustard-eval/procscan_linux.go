package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procStartTime identifies a process instance: its start time in clock ticks since
// boot (/proc/<pid>/stat field 22), so a reused pid is not mistaken for the original.
func procStartTime(pid int) (int64, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	// the command name (field 2) may contain spaces; fields resume after its ')'
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return 0, false
	}
	v, err := strconv.ParseInt(f[19], 10, 64)
	return v, err == nil
}

// pidsWithEnv lists processes whose environment contains the exact entry kv, from
// /proc/<pid>/environ (readable for processes of the same user).
func pidsWithEnv(kv string) ([]int, error) {
	return scanProc(func(pid string) bool {
		b, err := os.ReadFile(filepath.Join("/proc", pid, "environ"))
		if err != nil {
			return false
		}
		for _, e := range bytes.Split(b, []byte{0}) {
			if string(e) == kv {
				return true
			}
		}
		return false
	})
}

// pidsWithCwdIn lists processes whose working directory is one of dirs or below one.
func pidsWithCwdIn(dirs []string) ([]int, error) {
	return scanProc(func(pid string) bool {
		cwd, err := os.Readlink(filepath.Join("/proc", pid, "cwd"))
		return err == nil && cwdWithin(cwd, dirs)
	})
}

func scanProc(match func(pid string) bool) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if match(e.Name()) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

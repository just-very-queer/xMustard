//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
)

func apiProcessIdentity(pid int) (int64, string, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, "", false
	}
	return parseLinuxProcStat(b)
}

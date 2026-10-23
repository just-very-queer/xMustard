//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func procStartTime(pid int) (int64, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(string(b[i+1:]))
	if len(fields) < 20 {
		return 0, false
	}
	start, err := strconv.ParseInt(fields[19], 10, 64)
	return start, err == nil
}

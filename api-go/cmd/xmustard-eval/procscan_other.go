//go:build !darwin && !linux

package main

import (
	"errors"
	"runtime"
)

var errProcScan = errors.New("finding a run's processes is supported on macOS and Linux only, not " + runtime.GOOS)

func procStartTime(int) (int64, bool)       { return 0, false }
func pidsWithEnv(string) ([]int, error)     { return nil, errProcScan }
func pidsWithCwdIn([]string) ([]int, error) { return nil, errProcScan }

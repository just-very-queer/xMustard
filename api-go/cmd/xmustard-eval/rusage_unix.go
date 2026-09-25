//go:build unix

package main

import (
	"runtime"
	"syscall"
)

// selfMaxRSSBytes is the executor's own peak RSS (getrusage RUSAGE_SELF). It is
// reported on its own line: the executor is outside the measured xMustard tree.
func selfMaxRSSBytes() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) // bytes on macOS
	}
	return int64(ru.Maxrss) * 1024 // KiB elsewhere
}

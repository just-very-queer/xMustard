//go:build unix

package main

import (
	"runtime"
	"syscall"
)

// peakRSS is the process's peak resident set in bytes (getrusage reports KiB on Linux,
// bytes on macOS), 0 when unknown.
func peakRSS() int64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) << 10
}

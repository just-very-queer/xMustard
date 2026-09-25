//go:build !windows

package main

import (
	"fmt"
	"runtime"
	"syscall"
)

// childHighWater reports getrusage high-water marks and CPU for this process and its
// waited-for children, plus Go allocation totals. ru_maxrss is bytes on macOS and KiB
// on Linux; children_maxrss is the largest single child, never an aggregate concurrent
// process-tree peak.
func childHighWater() string {
	unit := "KiB"
	if runtime.GOOS == "darwin" {
		unit = "bytes"
	}
	var self, kids syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
	_ = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &kids)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	cpu := func(r syscall.Rusage) float64 {
		return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	}
	return fmt.Sprintf("rusage self_maxrss=%d children_maxrss=%d unit=%s self_cpu_s=%.3f children_cpu_s=%.3f go_total_alloc_bytes=%d go_mallocs=%d go_heap_sys_bytes=%d",
		self.Maxrss, kids.Maxrss, unit, cpu(self), cpu(kids), ms.TotalAlloc, ms.Mallocs, ms.HeapSys)
}

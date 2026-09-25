package budget

import (
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"sync"
)

// Runtime memory hygiene (PAR-RT-05).
//
// DefaultMemoryLimitBytes is the Go daemon's peak line in the default budget. The limit
// is a soft GC target, not a cap: GOGC keeps its normal pacing below it, and the
// runtime's GC CPU limiter bounds collection work if the live heap ever grows past it.
// A higher limit would let the heap grow past the daemon's line before the collector
// pushes back.
const DefaultMemoryLimitBytes int64 = 28 << 20

// Memory-limit sources reported by health.
const (
	MemoryLimitDefault = "xmustard_default" // applied by ApplyMemoryLimit
	MemoryLimitEnv     = "env"              // GOMEMLIMIT set by the operator
	MemoryLimitUnset   = "unset"            // ApplyMemoryLimit not called (tests, tools)
)

var memoryLimitSource = struct {
	sync.Mutex
	v string
}{v: MemoryLimitUnset}

// ApplyMemoryLimit sets the Go soft memory limit to DefaultMemoryLimitBytes unless the
// operator set GOMEMLIMIT, which the runtime has already applied. It returns the source
// and the limit now in force.
func ApplyMemoryLimit() (source string, limit int64) {
	return applyMemoryLimit(os.Getenv, debug.SetMemoryLimit)
}

func applyMemoryLimit(getenv func(string) string, set func(int64) int64) (string, int64) {
	source := MemoryLimitEnv
	if strings.TrimSpace(getenv("GOMEMLIMIT")) == "" {
		set(DefaultMemoryLimitBytes)
		source = MemoryLimitDefault
	}
	memoryLimitSource.Lock()
	memoryLimitSource.v = source
	memoryLimitSource.Unlock()
	return source, set(-1) // a negative value reads the limit without changing it
}

// ChildEnv returns the environment for a helper child: nil (inherit unchanged) unless
// an allocator setting must be added. On Linux, glibc gives each thread that contends
// its own malloc arena; MALLOC_ARENA_MAX=2 keeps a Rust child's arenas at two unless
// the operator chose a value.
func ChildEnv() []string { return childEnv(runtime.GOOS, os.Environ) }

func childEnv(goos string, environ func() []string) []string {
	if goos != "linux" {
		return nil
	}
	env := environ()
	for _, kv := range env {
		if strings.HasPrefix(kv, "MALLOC_ARENA_MAX=") {
			return nil
		}
	}
	return append(env, "MALLOC_ARENA_MAX=2")
}

// RuntimeStats are the Go GC and heap figures health reports, read from runtime/metrics
// (no stop-the-world).
type RuntimeStats struct {
	MemoryLimitBytes     int64    `json:"gomemlimit_bytes"`
	MemoryLimitSource    string   `json:"gomemlimit_source"`
	GOGCPercent          int64    `json:"gogc_percent"`
	HeapObjectsBytes     int64    `json:"heap_objects_bytes"`
	HeapGoalBytes        int64    `json:"heap_goal_bytes"`
	HeapReleasedBytes    int64    `json:"heap_released_bytes"`
	StackBytes           int64    `json:"stack_bytes"`
	GoMappedBytes        int64    `json:"go_mapped_bytes"`
	TotalAllocBytes      int64    `json:"total_alloc_bytes"`
	GCCycles             int64    `json:"gc_cycles"`
	GCLimiterLastEnabled int64    `json:"gc_limiter_last_enabled_cycle"`
	GCCPUSeconds         float64  `json:"gc_cpu_seconds"`
	Goroutines           int64    `json:"goroutines"`
	UnsupportedMetrics   []string `json:"unsupported_metrics,omitempty"`
}

var runtimeMetricNames = []string{
	"/gc/gomemlimit:bytes",
	"/gc/gogc:percent",
	"/memory/classes/heap/objects:bytes",
	"/gc/heap/goal:bytes",
	"/memory/classes/heap/released:bytes",
	"/memory/classes/heap/stacks:bytes",
	"/memory/classes/total:bytes",
	"/gc/heap/allocs:bytes",
	"/gc/cycles/total:gc-cycles",
	"/gc/limiter/last-enabled:gc-cycle",
	"/cpu/classes/gc/total:cpu-seconds",
	"/sched/goroutines:goroutines",
}

// ReadRuntimeStats reads the Go runtime figures.
func ReadRuntimeStats() RuntimeStats {
	samples := make([]metrics.Sample, len(runtimeMetricNames))
	for i, n := range runtimeMetricNames {
		samples[i].Name = n
	}
	metrics.Read(samples)
	var r RuntimeStats
	u := func(i int) int64 {
		switch samples[i].Value.Kind() {
		case metrics.KindUint64:
			return int64(samples[i].Value.Uint64())
		case metrics.KindFloat64:
			return int64(samples[i].Value.Float64())
		default:
			r.UnsupportedMetrics = append(r.UnsupportedMetrics, samples[i].Name)
			return 0
		}
	}
	r.MemoryLimitBytes = u(0)
	r.GOGCPercent = u(1)
	r.HeapObjectsBytes = u(2)
	r.HeapGoalBytes = u(3)
	r.HeapReleasedBytes = u(4)
	r.StackBytes = u(5)
	r.GoMappedBytes = u(6)
	r.TotalAllocBytes = u(7)
	r.GCCycles = u(8)
	r.GCLimiterLastEnabled = u(9)
	if samples[10].Value.Kind() == metrics.KindFloat64 {
		r.GCCPUSeconds = samples[10].Value.Float64()
	} else {
		r.UnsupportedMetrics = append(r.UnsupportedMetrics, samples[10].Name)
	}
	r.Goroutines = u(11)
	memoryLimitSource.Lock()
	r.MemoryLimitSource = memoryLimitSource.v
	memoryLimitSource.Unlock()
	return r
}

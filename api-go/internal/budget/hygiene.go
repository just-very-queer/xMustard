package budget

import (
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"sync"
	"sync/atomic"
)

// Runtime memory hygiene (PAR-RT-05).
//
// DefaultMemoryLimitBytes is the Go daemon's peak line in the default budget. The limit
// is a soft GC target, not a cap: GOGC keeps its normal pacing below it. On its own it
// would let the collector run back to back once the live heap nears it, and the
// transient pool's admitted bytes (decoded bodies, captured core output, provider
// responses: up to 24 MiB) live in the same heap. So the default limit comes with a
// GOGC floor: after every collection the limit is lifted, when needed, so the heap may
// still grow by at least DefaultGOGCFloorPercent of the live heap before the next one
// (never below the daemon's line). Within the line the limit holds the heap to it;
// past it, collection cost stays bounded as with GOGC=50 instead of thrashing.
const DefaultMemoryLimitBytes int64 = 28 << 20

// DefaultGOGCFloorPercent is the least growth, as a percentage of the live heap, the
// default memory limit leaves the heap before the next collection.
const DefaultGOGCFloorPercent int64 = 50

// Memory-limit sources reported by health.
const (
	MemoryLimitDefault = "xmustard_default" // applied by ApplyMemoryLimit
	MemoryLimitEnv     = "env"              // GOMEMLIMIT set by the operator
	MemoryLimitUnset   = "unset"            // ApplyMemoryLimit not called (tests, tools)
)

var memoryLimitSource = struct {
	sync.Mutex
	v     string
	floor *gcFloor // the running GOGC floor, when the default limit applies
}{v: MemoryLimitUnset}

// ApplyMemoryLimit sets the Go soft memory limit to DefaultMemoryLimitBytes, with the
// GOGC floor, unless the operator set GOMEMLIMIT, which the runtime has already applied
// and which is then left alone. It returns the source and the limit now in force.
func ApplyMemoryLimit() (source string, limit int64) {
	return applyMemoryLimit(os.Getenv, debug.SetMemoryLimit)
}

func applyMemoryLimit(getenv func(string) string, set func(int64) int64) (string, int64) {
	source := MemoryLimitEnv
	var floor *gcFloor
	if strings.TrimSpace(getenv("GOMEMLIMIT")) == "" {
		set(DefaultMemoryLimitBytes)
		source = MemoryLimitDefault
		floor = newGCFloor(DefaultMemoryLimitBytes, DefaultGOGCFloorPercent, set, readHeapForFloor)
	}
	memoryLimitSource.Lock()
	if memoryLimitSource.floor != nil {
		memoryLimitSource.floor.stop()
	}
	memoryLimitSource.v, memoryLimitSource.floor = source, floor
	memoryLimitSource.Unlock()
	if floor != nil {
		floor.start()
	}
	return source, set(-1) // a negative value reads the limit without changing it
}

// gcFloor lifts the memory limit after each collection so the next one comes no sooner
// than the heap growing by percent of what is live. It runs from a finalizer re-armed
// every GC cycle: no goroutine, no timer, one small allocation per cycle.
type gcFloor struct {
	base, percent int64
	set           func(int64) int64
	read          func() (live, overhead int64)
	stopped       atomic.Bool
	applied       atomic.Int64 // limit last set by the floor (base until lifted)
	lifts         atomic.Int64 // cycles that ended with the limit above base
}

func newGCFloor(base, percent int64, set func(int64) int64, read func() (int64, int64)) *gcFloor {
	f := &gcFloor{base: base, percent: percent, set: set, read: read}
	f.applied.Store(base)
	return f
}

// target is the limit that leaves percent of the live heap to grow into, on top of the
// runtime's non-heap memory, and never less than base.
func (f *gcFloor) target() int64 {
	live, overhead := f.read()
	return max(f.base, overhead+live+live*f.percent/100)
}

// tune applies the target after a collection.
func (f *gcFloor) tune() {
	t := f.target()
	if t != f.applied.Load() {
		f.set(t)
		f.applied.Store(t)
	}
	if t > f.base {
		f.lifts.Add(1)
	}
}

// gcSentinel is unreachable as soon as it is armed, so the next collection finds it and
// queues its finalizer. It holds a pointer, so it is never tiny-allocated (a tiny
// object's finalizer may never run).
type gcSentinel struct{ f *gcFloor }

func (f *gcFloor) start() { armGCFloor(f) }

func armGCFloor(f *gcFloor) {
	runtime.SetFinalizer(&gcSentinel{f: f}, func(s *gcSentinel) {
		if s.f.stopped.Load() {
			return
		}
		s.f.tune()
		armGCFloor(s.f)
	})
}

func (f *gcFloor) stop() { f.stopped.Store(true) }

var floorMetricNames = []string{
	"/gc/heap/live:bytes",
	"/memory/classes/total:bytes",
	"/memory/classes/heap/released:bytes",
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/heap/unused:bytes",
	"/memory/classes/heap/free:bytes",
}

// readHeapForFloor returns the live heap marked by the last collection and the runtime's
// non-heap memory (stacks, GC metadata, other runtime structures), which the memory
// limit also counts.
func readHeapForFloor() (live, overhead int64) {
	s := make([]metrics.Sample, len(floorMetricNames))
	for i, n := range floorMetricNames {
		s[i].Name = n
	}
	metrics.Read(s)
	v := func(i int) int64 {
		if s[i].Value.Kind() == metrics.KindUint64 {
			return int64(s[i].Value.Uint64())
		}
		return 0
	}
	live = v(0)
	overhead = v(1) - v(2) - v(3) - v(4) - v(5)
	return live, max(overhead, 0)
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
	MemoryLimitBaseBytes int64    `json:"gomemlimit_base_bytes"`
	GOGCFloorPercent     int64    `json:"gogc_floor_percent"`
	GOGCFloorLifts       int64    `json:"gogc_floor_lifts"`
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
	if f := memoryLimitSource.floor; f != nil {
		r.MemoryLimitBaseBytes, r.GOGCFloorPercent, r.GOGCFloorLifts = f.base, f.percent, f.lifts.Load()
	}
	memoryLimitSource.Unlock()
	return r
}

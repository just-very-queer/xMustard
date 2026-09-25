package budget

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The Go memory limit is applied only when the operator did not set GOMEMLIMIT.
func TestMemoryLimitAppliedOnlyWhenGOMEMLIMITUnset(t *testing.T) {
	prevSource := ReadRuntimeStats().MemoryLimitSource
	t.Cleanup(func() {
		memoryLimitSource.Lock()
		memoryLimitSource.v = prevSource
		memoryLimitSource.Unlock()
	})
	var mu sync.Mutex
	var set []int64
	fakeSet := func(n int64) int64 {
		mu.Lock()
		defer mu.Unlock()
		if n >= 0 {
			set = append(set, n)
			return 0
		}
		if len(set) > 0 {
			return set[len(set)-1]
		}
		return 1 << 62
	}
	src, limit := applyMemoryLimit(func(string) string { return "" }, fakeSet)
	memoryLimitSource.Lock()
	floor := memoryLimitSource.floor
	memoryLimitSource.Unlock()
	if floor == nil {
		t.Fatal("the default limit must come with the GOGC floor")
	}
	floor.stop() // collections from here on must not reach fakeSet
	mu.Lock()
	if src != MemoryLimitDefault || len(set) == 0 || set[0] != DefaultMemoryLimitBytes || limit != DefaultMemoryLimitBytes {
		t.Fatalf("unset GOMEMLIMIT: source=%s limit=%d set=%v", src, limit, set)
	}
	set = nil
	mu.Unlock()
	if r := ReadRuntimeStats(); r.MemoryLimitSource != MemoryLimitDefault || r.MemoryLimitBaseBytes != DefaultMemoryLimitBytes || r.GOGCFloorPercent != DefaultGOGCFloorPercent {
		t.Fatalf("health must report the default source, base and floor: %+v", r)
	}
	src, _ = applyMemoryLimit(func(k string) string {
		if k == "GOMEMLIMIT" {
			return "64MiB"
		}
		return ""
	}, fakeSet)
	mu.Lock()
	if src != MemoryLimitEnv || len(set) != 0 {
		t.Fatalf("GOMEMLIMIT set: the limit must be left alone, source=%s set=%v", src, set)
	}
	mu.Unlock()
	if r := ReadRuntimeStats(); r.MemoryLimitSource != MemoryLimitEnv || r.GOGCFloorPercent != 0 {
		t.Fatalf("health must report the env source and no floor: %+v", r)
	}
	if DefaultMemoryLimitBytes > 28<<20 {
		t.Fatalf("the default limit %d must not exceed the daemon's 28 MiB line", DefaultMemoryLimitBytes)
	}
}

func TestRuntimeStatsAreRead(t *testing.T) {
	r := ReadRuntimeStats()
	if len(r.UnsupportedMetrics) != 0 {
		t.Fatalf("unsupported runtime metrics: %v", r.UnsupportedMetrics)
	}
	if r.GoMappedBytes <= 0 || r.HeapGoalBytes <= 0 || r.Goroutines <= 0 || r.MemoryLimitBytes <= 0 || r.GOGCPercent == 0 {
		t.Fatalf("runtime stats: %+v", r)
	}
}

// On Linux a Rust child gets MALLOC_ARENA_MAX=2 unless the operator chose a value;
// elsewhere the environment is inherited unchanged.
func TestChildEnvCapsGlibcArenasOnLinuxOnly(t *testing.T) {
	base := func() []string { return []string{"PATH=/bin", "HOME=/h"} }
	if env := childEnv("darwin", base); env != nil {
		t.Fatalf("darwin must inherit: %v", env)
	}
	env := childEnv("linux", base)
	if len(env) != 3 || env[2] != "MALLOC_ARENA_MAX=2" || env[0] != "PATH=/bin" {
		t.Fatalf("linux env: %v", env)
	}
	if env := childEnv("linux", func() []string { return []string{"MALLOC_ARENA_MAX=8"} }); env != nil {
		t.Fatalf("an operator value must be kept: %v", env)
	}
}

// PAR-RT-05 GOGC floor: the default limit is lifted, when needed, so the heap can grow
// by at least the floor's share of the live heap (plus the runtime's non-heap memory)
// before the next collection, and it returns to the daemon's line when the live heap
// shrinks.
func TestGCFloorTargetKeepsHeadroomOverLiveHeap(t *testing.T) {
	var live, overhead atomic.Int64
	var sets []int64
	f := newGCFloor(28<<20, 50, func(n int64) int64 { sets = append(sets, n); return 0 },
		func() (int64, int64) { return live.Load(), overhead.Load() })
	live.Store(4 << 20)
	overhead.Store(3 << 20)
	f.tune()
	if len(sets) != 0 || f.lifts.Load() != 0 {
		t.Fatalf("4 MiB live stays under the 28 MiB line: sets %v", sets)
	}
	live.Store(30 << 20) // e.g. a full transient pool held live
	f.tune()
	f.tune()
	if len(sets) != 1 || sets[0] != 3<<20+45<<20 || f.lifts.Load() != 2 {
		t.Fatalf("30 MiB live: want one lift to 48 MiB, got sets %v lifts %d", sets, f.lifts.Load())
	}
	live.Store(2 << 20)
	f.tune()
	if len(sets) != 2 || sets[1] != 28<<20 {
		t.Fatalf("back under the line: want the limit restored to 28 MiB, got %v", sets)
	}
}

// The floor runs after collections (finalizer re-armed each cycle) and stops cleanly.
func TestGCFloorRunsAfterEachCollection(t *testing.T) {
	var calls atomic.Int64
	f := newGCFloor(1<<20, 50, func(n int64) int64 { calls.Add(1); return 0 },
		func() (int64, int64) { return int64(calls.Load()+1) << 30, 0 }) // a new target every cycle
	f.start()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the floor ran %d times over repeated collections", calls.Load())
		}
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
	}
	f.stop()
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	after := calls.Load()
	for i := 0; i < 3; i++ {
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != after {
		t.Fatalf("a stopped floor kept tuning: %d -> %d", after, calls.Load())
	}
}

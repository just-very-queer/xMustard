package budget

import "testing"

// The Go memory limit is applied only when the operator did not set GOMEMLIMIT.
func TestMemoryLimitAppliedOnlyWhenGOMEMLIMITUnset(t *testing.T) {
	prevSource := ReadRuntimeStats().MemoryLimitSource
	t.Cleanup(func() {
		memoryLimitSource.Lock()
		memoryLimitSource.v = prevSource
		memoryLimitSource.Unlock()
	})
	var set []int64
	fakeSet := func(n int64) int64 {
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
	if src != MemoryLimitDefault || len(set) != 1 || set[0] != DefaultMemoryLimitBytes || limit != DefaultMemoryLimitBytes {
		t.Fatalf("unset GOMEMLIMIT: source=%s limit=%d set=%v", src, limit, set)
	}
	if ReadRuntimeStats().MemoryLimitSource != MemoryLimitDefault {
		t.Fatal("health must report the default source")
	}
	set = nil
	src, _ = applyMemoryLimit(func(k string) string {
		if k == "GOMEMLIMIT" {
			return "64MiB"
		}
		return ""
	}, fakeSet)
	if src != MemoryLimitEnv || len(set) != 0 {
		t.Fatalf("GOMEMLIMIT set: the limit must be left alone, source=%s set=%v", src, set)
	}
	if ReadRuntimeStats().MemoryLimitSource != MemoryLimitEnv {
		t.Fatal("health must report the env source")
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

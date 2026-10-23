package shims

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestUpdateSlopesResetsBaselineWhenProcessIdentityChangesOrCPUDecreases(t *testing.T) {
	slopes.Lock()
	slopes.samples = make(map[int]sample)
	slopes.Unlock()
	base := time.Unix(300, 0)
	first := []Process{{PID: 70005, UID: 501, StartTime: 10, Comm: ProcessName, CPUAt: base}}
	updateSlopes(first, base)
	replacement := []Process{{PID: 70005, UID: 501, StartTime: 20, Comm: ProcessName, CPUS: 900 * time.Millisecond, CPUAt: base.Add(time.Second)}}
	updateSlopes(replacement, base.Add(time.Second))
	if replacement[0].CPUSlope != 0 || Classify(replacement[0]) != Live {
		t.Fatalf("replacement inherited old identity slope: %+v", replacement[0])
	}
	next := []Process{{PID: 70005, UID: 501, StartTime: 20, Comm: ProcessName, CPUS: 1800 * time.Millisecond, CPUAt: base.Add(2 * time.Second)}}
	updateSlopes(next, base.Add(2*time.Second))
	if next[0].CPUSlope < .8 || Classify(next[0]) != Runaway {
		t.Fatalf("replacement's own runaway slope not detected: %+v", next[0])
	}
	reset := []Process{{PID: 70005, UID: 501, StartTime: 20, Comm: ProcessName, CPUS: time.Second, CPUAt: base.Add(3 * time.Second)}}
	updateSlopes(reset, base.Add(3*time.Second))
	if reset[0].CPUSlope != 0 || Classify(reset[0]) != Live {
		t.Fatalf("decreasing counter retained stale slope: %+v", reset[0])
	}
}

func TestUpdateSlopesRetainsLastValidSlopeAcrossShortIntervals(t *testing.T) {
	slopes.Lock()
	slopes.samples = make(map[int]sample)
	slopes.Unlock()
	base := time.Unix(400, 0)
	first := []Process{{PID: 70006, UID: 501, StartTime: 30, Comm: ProcessName, CPUAt: base}}
	updateSlopes(first, base)
	measured := []Process{{PID: 70006, UID: 501, StartTime: 30, Comm: ProcessName, CPUS: 900 * time.Millisecond, CPUAt: base.Add(time.Second)}}
	updateSlopes(measured, base.Add(time.Second))
	for i := 1; i <= 3; i++ {
		short := []Process{{PID: 70006, UID: 501, StartTime: 30, Comm: ProcessName, CPUS: 900 * time.Millisecond, CPUAt: base.Add(time.Second + time.Duration(i)*100*time.Millisecond)}}
		updateSlopes(short, base.Add(time.Second+time.Duration(i)*100*time.Millisecond))
		if short[0].CPUSlope < .8 || Classify(short[0]) != Runaway {
			t.Fatalf("short poll %d discarded valid runaway slope: %+v", i, short[0])
		}
	}
}

func TestUpdateSlopesKeepsBaselineAcrossShortQuantizedIntervals(t *testing.T) {
	slopes.Lock()
	slopes.samples = make(map[int]sample)
	slopes.Unlock()
	base := time.Unix(200, 0)
	first := []Process{{PID: 70003, Comm: ProcessName, CPUAt: base}}
	updateSlopes(first, base.Add(10*time.Second))
	for i := 1; i <= 5; i++ {
		short := []Process{{PID: 70003, Comm: ProcessName, CPUS: 10 * time.Millisecond, CPUAt: base.Add(time.Duration(i) * 10 * time.Millisecond)}}
		updateSlopes(short, base.Add(20*time.Second))
		if short[0].CPUSlope != 0 || Classify(short[0]) != Live {
			t.Fatalf("short quantized sample %d classified as %s at slope %v", i, Classify(short[0]), short[0].CPUSlope)
		}
	}
	idle := []Process{{PID: 70003, Comm: ProcessName, CPUS: 10 * time.Millisecond, CPUAt: base.Add(time.Second)}}
	updateSlopes(idle, base.Add(30*time.Second))
	if idle[0].CPUSlope >= .8 || Classify(idle[0]) != Live {
		t.Fatalf("quantized idle sample = %+v", idle[0])
	}

	baseline := []Process{{PID: 70004, Comm: ProcessName, CPUAt: base}}
	updateSlopes(baseline, base.Add(40*time.Second))
	runaway := []Process{{PID: 70004, Comm: ProcessName, CPUS: 900 * time.Millisecond, CPUAt: base.Add(time.Second)}}
	updateSlopes(runaway, base.Add(50*time.Second))
	if runaway[0].CPUSlope < .8 || Classify(runaway[0]) != Runaway {
		t.Fatalf("sustained runaway sample = %+v", runaway[0])
	}
}

func TestUpdateSlopesUsesSecondsAndRejectsStaleSamples(t *testing.T) {
	slopes.Lock()
	slopes.samples = make(map[int]sample)
	slopes.Unlock()
	base := time.Unix(100, 0)
	low := []Process{{PID: 70001, Comm: ProcessName, CPUS: 10 * time.Millisecond}}
	updateSlopes(low, base)
	low = []Process{{PID: 70001, Comm: ProcessName, CPUS: 20 * time.Millisecond}}
	updateSlopes(low, base.Add(time.Second))
	if low[0].CPUSlope != .01 {
		t.Fatalf("low CPU slope = %v, want .01", low[0].CPUSlope)
	}
	runaway := []Process{{PID: 70001, Comm: ProcessName, CPUS: 920 * time.Millisecond}}
	updateSlopes(runaway, base.Add(2*time.Second))
	if runaway[0].CPUSlope < .8 || Classify(runaway[0]) != Runaway {
		t.Fatalf("runaway sample = %+v", runaway[0])
	}
	stale := []Process{{PID: 70001, Comm: ProcessName, CPUS: 5 * time.Second}}
	updateSlopes(stale, base.Add(1500*time.Millisecond))
	next := []Process{{PID: 70001, Comm: ProcessName, CPUS: time.Second}}
	updateSlopes(next, base.Add(3*time.Second))
	if next[0].CPUSlope >= .8 {
		t.Fatalf("stale sample inflated later slope: %v", next[0].CPUSlope)
	}
}

func TestDarwinRechecksIdentityAtSignalBoundary(t *testing.T) {
	expected := Process{PID: 99, UID: 10, StartTime: 100, Comm: ProcessName}
	replacement := Process{PID: 99, UID: 10, StartTime: 200, Comm: ProcessName}
	checks := 0
	signaled := false
	err := signalIfIdentity(expected, func(int) (Process, error) {
		checks++
		if checks == 1 {
			return expected, nil
		}
		return replacement, nil
	}, func(int) error {
		signaled = true
		return nil
	})
	if err != ErrProcessChanged || signaled || checks != 2 {
		t.Fatalf("replacement at signal boundary: err=%v signaled=%v checks=%d", err, signaled, checks)
	}
}

func TestUpdateSlopesUsesObservationTimes(t *testing.T) {
	slopes.Lock()
	slopes.samples = make(map[int]sample)
	slopes.Unlock()
	base := time.Unix(100, 0)
	first := []Process{{PID: 70002, Comm: ProcessName, CPUS: 10 * time.Millisecond, CPUAt: base}}
	updateSlopes(first, base.Add(10*time.Second))
	second := []Process{{PID: 70002, Comm: ProcessName, CPUS: 110 * time.Millisecond, CPUAt: base.Add(2 * time.Second)}}
	updateSlopes(second, base.Add(11*time.Second))
	if second[0].CPUSlope < .049 || second[0].CPUSlope > .051 {
		t.Fatalf("slope used collection completion timestamps: %v", second[0].CPUSlope)
	}
}

func TestClassifyOrphanRequiresKnownAge(t *testing.T) {
	p := Process{Comm: ProcessName, PPID: 1, Age: time.Hour}
	if got := Classify(p); got != Live {
		t.Fatalf("unknown age classified %q, want live", got)
	}
}

func TestReapDoesNotSignalOrphanWithUnknownAge(t *testing.T) {
	called := false
	err := Reap([]Process{{PID: 32, Comm: ProcessName, PPID: 1, Age: time.Hour}}, func(Process) error {
		called = true
		return nil
	})
	if err != nil || called {
		t.Fatalf("unknown-age orphan: err=%v signaled=%v", err, called)
	}
}

func TestReapRefusesReusedPID(t *testing.T) {
	original := Process{PID: 99, UID: 10, StartTime: 100, Comm: ProcessName, CPUS: 301 * time.Second}
	replacement := Process{PID: 99, UID: 10, StartTime: 200, Comm: "unrelated"}
	signaled := false
	err := Reap([]Process{original}, func(expected Process) error {
		return signalIfIdentity(expected, func(int) (Process, error) {
			return replacement, nil
		}, func(int) error {
			signaled = true
			return nil
		})
	})
	if err != ErrProcessChanged || signaled {
		t.Fatalf("reused pid: err=%v signaled=%v", err, signaled)
	}
}

func TestListSerializesSamplingAndBaselinePublication(t *testing.T) {
	previous := enumerateProcesses
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls int
	enumerateProcesses = func() ([]Process, error) {
		calls++
		entered <- struct{}{}
		if calls == 1 {
			<-release
		}
		return []Process{{PID: 80001, Comm: ProcessName, CPUS: time.Duration(calls) * time.Second}}, nil
	}
	t.Cleanup(func() { enumerateProcesses = previous })
	done := make(chan error, 2)
	go func() { _, err := List(); done <- err }()
	<-entered
	started := make(chan struct{})
	go func() { close(started); _, err := List(); done <- err }()
	<-started
	select {
	case <-entered:
		t.Fatal("second sample entered before the first published its baseline")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("sampler calls=%d, want 2", calls)
	}
}

func TestClassifyShims(t *testing.T) {
	cases := []struct {
		name    string
		process Process
		want    Class
	}{
		{"high cumulative CPU", Process{Comm: ProcessName, CPUS: 1200 * time.Second}, Runaway},
		{"high CPU slope", Process{Comm: ProcessName, CPUSlope: .97}, Runaway},
		{"old orphan", Process{Comm: ProcessName, PPID: 1, Age: 11 * time.Minute, AgeKnown: true}, Orphaned},
		{"new orphan", Process{Comm: ProcessName, PPID: 1, Age: 2 * time.Minute, AgeKnown: true}, Live},
		{"ordinary live", Process{Comm: ProcessName, PPID: 4242, CPUS: 3 * time.Second}, Live},
		{"other command", Process{Comm: "not-xmustard-mcp", PPID: 1, Age: time.Hour, CPUS: time.Hour}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.process); got != tc.want {
				t.Fatalf("Classify(%+v) = %q, want %q", tc.process, got, tc.want)
			}
		})
	}
}

func TestReapEachPreservesPerPIDOutcomes(t *testing.T) {
	processes := []Process{
		{PID: 10, Comm: ProcessName, CPUS: 301 * time.Second},
		{PID: 11, Comm: ProcessName, CPUS: 302 * time.Second},
	}
	outcomes := ReapEach(processes, func(p Process) error {
		if p.PID == 10 {
			return errors.New("process exited before signal")
		}
		return nil
	})
	if len(outcomes) != 2 || outcomes[0].PID != 10 || outcomes[0].Result != "failed" || outcomes[1].PID != 11 || outcomes[1].Result != "signaled" {
		t.Fatalf("reap outcomes = %+v", outcomes)
	}
}

func TestReapSelectsOnlyRunawayAndOrphaned(t *testing.T) {
	processes := []Process{
		{PID: 1, Comm: ProcessName, CPUS: 301 * time.Second},
		{PID: 2, Comm: ProcessName, PPID: 1, Age: 11 * time.Minute, AgeKnown: true},
		{PID: 3, Comm: ProcessName, PPID: 1, Age: 2 * time.Minute, AgeKnown: true},
		{PID: 4, Comm: ProcessName, PPID: 4242, CPUS: 3 * time.Second},
		{PID: 5, Comm: "other", CPUS: time.Hour},
	}
	var got []int
	outcomes := ReapEach(processes, func(p Process) error {
		got = append(got, p.PID)
		return nil
	})
	if want := []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("killed pids %v, want %v", got, want)
	}
	if len(outcomes) != len(got) {
		t.Fatalf("reap outcomes = %+v, want one for each selected PID", outcomes)
	}
}

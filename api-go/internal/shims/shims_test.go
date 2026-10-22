package shims

import (
	"reflect"
	"testing"
	"time"
)

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
		{"old orphan", Process{Comm: ProcessName, PPID: 1, Age: 11 * time.Minute}, Orphaned},
		{"new orphan", Process{Comm: ProcessName, PPID: 1, Age: 2 * time.Minute}, Live},
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

func TestReapSelectsOnlyRunawayAndOrphaned(t *testing.T) {
	processes := []Process{
		{PID: 1, Comm: ProcessName, CPUS: 301 * time.Second},
		{PID: 2, Comm: ProcessName, PPID: 1, Age: 11 * time.Minute},
		{PID: 3, Comm: ProcessName, PPID: 1, Age: 2 * time.Minute},
		{PID: 4, Comm: ProcessName, PPID: 4242, CPUS: 3 * time.Second},
		{PID: 5, Comm: "other", CPUS: time.Hour},
	}
	var got []int
	err := Reap(processes, func(p Process) error {
		got = append(got, p.PID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("killed pids %v, want %v", got, want)
	}
}

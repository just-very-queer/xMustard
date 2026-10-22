package shims

import (
	"os"
	"reflect"
	"testing"
	"time"
)

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
	err := Reap(processes, func(pid int, sig os.Signal) error {
		if sig != os.Kill {
			t.Errorf("signal=%v, want KILL", sig)
		}
		got = append(got, pid)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("killed pids %v, want %v", got, want)
	}
}

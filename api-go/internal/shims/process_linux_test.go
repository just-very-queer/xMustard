//go:build linux

package shims

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPidfdFailureAndReplacementNeverSignal(t *testing.T) {
	expected := Process{PID: 123, UID: 10, StartTime: 456, Comm: ProcessName}
	signaled := false
	openFailure := signalWithPidfd(expected, func(int, int) (int, error) { return -1, syscall.EMFILE }, func(int) error { return nil }, nil, func(int) error { signaled = true; return nil })
	if openFailure != ErrProcessChanged || signaled {
		t.Fatalf("pidfd failure: err=%v signaled=%v", openFailure, signaled)
	}
	replacement := Process{PID: expected.PID, UID: expected.UID, StartTime: expected.StartTime + 1, Comm: ProcessName}
	current := expected
	pidfdTarget := Process{}
	deliveredTarget := Process{}
	err := signalWithPidfd(expected, func(int, int) (int, error) {
		pidfdTarget = current
		return 8, nil
	}, func(int) error { return nil }, func(int) (Process, error) {
		return current, nil
	}, func(fd int) error {
		if fd != 8 {
			t.Fatalf("signal used fd %d, want pidfd 8", fd)
		}
		current = replacement // PID reuse after validation cannot change the pidfd target.
		deliveredTarget = pidfdTarget
		return nil
	})
	if err != nil || deliveredTarget != expected || current != replacement {
		t.Fatalf("pidfd delivery: err=%v target=%+v current=%+v", err, deliveredTarget, current)
	}
}

func TestTicksDurationDoesNotOverflowAtLongUptime(t *testing.T) {
	const ticks = uint64(10_000_000_000)
	got := ticksDuration(ticks)
	want := 100_000_000 * time.Second
	if got != want {
		t.Fatalf("ticksDuration(%d) = %v, want %v", ticks, got, want)
	}
	boot := time.Now().Unix() - 1
	age := time.Since(time.Unix(boot, 0).Add(ticksDuration(50)))
	if age < 0 || age > time.Minute {
		t.Fatalf("synthetic recent process age = %v", age)
	}
	if got := Classify(Process{Comm: ProcessName, PPID: 1, Age: age, AgeKnown: true}); got != Live {
		t.Fatalf("recent orphan classified %q, want live", got)
	}
}

func TestLinuxBootTimeRejectsMissingOrMalformedData(t *testing.T) {
	if _, err := readLinuxBootTime(func(string) ([]byte, error) { return nil, os.ErrNotExist }); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing /proc/stat error = %v", err)
	}
	for _, input := range []string{"cpu 1 2 3\n", "btime nope\n"} {
		if _, err := parseLinuxBootTime([]byte(input)); err == nil {
			t.Fatalf("parseLinuxBootTime(%q) succeeded", input)
		}
	}
	if got, err := parseLinuxBootTime([]byte("cpu 1\nbtime 1700000000\n")); err != nil || got != 1_700_000_000 {
		t.Fatalf("valid boot time = %d, %v", got, err)
	}
}

func TestIdleShimFixtureIsEnumeratedLive(t *testing.T) {
	binary := filepath.Join(t.TempDir(), ProcessName)
	build := exec.Command("cc", "-x", "c", "-o", binary, "-")
	build.Stdin = strings.NewReader("#include <unistd.h>\nint main(void) { for (;;) pause(); }\n")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build idle shim fixture: %v: %s", err, output)
	}
	cmd := exec.Command(binary)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	processes, err := ListForOperator()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range processes {
		if p.PID == cmd.Process.Pid {
			if p.Class != Live || p.CPUSlope >= .8 {
				t.Fatalf("idle fixture classification: %+v", p)
			}
			return
		}
	}
	t.Fatalf("real process enumeration did not list fixture pid %d: %+v", cmd.Process.Pid, processes)
}

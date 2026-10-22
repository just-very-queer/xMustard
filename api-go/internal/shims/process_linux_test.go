//go:build linux

package shims

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

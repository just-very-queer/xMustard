//go:build darwin || linux

package workspaceops

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Output the shell writes just before it exits is in the log by the time a read
// reports EOF. A Linux PTY buffers several KiB, so closing the master as soon as
// the shell exited dropped the tail. exec makes printf the session leader, so the
// last write is followed at once by the leader's exit.
func TestTerminalLogKeepsOutputWrittenJustBeforeExit(t *testing.T) {
	const size = 64 << 10
	body := strings.Repeat("x", size)
	var lost []string
	for run := 0; run < 10; run++ {
		dataDir, workspaceID, record := newTerminalTestSession(t, 80, 24)
		live := liveTerminalSession(t, record.TerminalID)
		// printf builds the marker, so the echoed command line cannot contain it
		command := fmt.Sprintf("s=$(head -c %d /dev/zero | tr '\\0' x); exec printf '%%s\\n__T%%sL__\\n' \"$s\" AI\n", size)
		if err := WriteTerminal(workspaceID, record.TerminalID, command); err != nil {
			t.Fatalf("write command: %v", err)
		}
		var offset int64
		content, _ := waitForTerminal(t, dataDir, workspaceID, record.TerminalID, &offset, func(read *TerminalReadResult, content string) bool {
			return read.EOF
		})
		requireTerminalTornDown(t, live)
		if !strings.Contains(content, body) || !strings.Contains(content, "__TAIL__") {
			lost = append(lost, fmt.Sprintf("run %d: %d of %d bytes, marker %v", run, strings.Count(content, "x"), size, strings.Contains(content, "__TAIL__")))
		}
	}
	if len(lost) > 0 {
		t.Fatalf("EOF was reported before the shell's last output reached the log:\n%s", strings.Join(lost, "\n"))
	}
}

// A shell that exits on its own is a hang-up, not a close: what it left running
// gets SIGHUP and no more. A nohup'd job with its stdio off the terminal outlives
// it, and so does a job that ignores SIGHUP while still holding the terminal; that
// one must not keep the pump, the master or the log open, and EOF still comes.
func TestTerminalNaturalExitOnlyHangsUp(t *testing.T) {
	dataDir, workspaceID, record := newTerminalTestSession(t, 80, 24)
	live := liveTerminalSession(t, record.TerminalID)
	jobs := strings.Join([]string{
		// disowned, so the shell itself sends none of them SIGHUP on exit
		"nohup sleep 40 </dev/null >/dev/null 2>&1 & printf '__PID_NOHUP__:%s:\\n' $!; disown",
		"sh -c 'trap \"\" HUP; exec sleep 41' & printf '__PID_HOLDER__:%s:\\n' $!; disown",
		"sleep 42 & printf '__PID_PLAIN__:%s:\\n' $!; disown",
	}, "\n") + "\n"
	if err := WriteTerminal(workspaceID, record.TerminalID, jobs); err != nil {
		t.Fatalf("write jobs: %v", err)
	}
	var offset int64
	pids := waitForTerminalPIDs(t, dataDir, workspaceID, record.TerminalID, &offset, "NOHUP", "HOLDER", "PLAIN")
	survivors := []int{pids["NOHUP"], pids["HOLDER"]}
	for _, pid := range survivors {
		t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
	}

	if err := WriteTerminal(workspaceID, record.TerminalID, "exit\n"); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	waitForTerminal(t, dataDir, workspaceID, record.TerminalID, &offset, func(read *TerminalReadResult, content string) bool {
		return read.EOF
	})
	requireTerminalTornDownExcept(t, live, survivors, pids["PLAIN"])
	for _, name := range []string{"NOHUP", "HOLDER"} {
		if !terminalProcessAlive(t, pids[name]) {
			t.Fatalf("%s job %d did not outlive the shell's exit", name, pids[name])
		}
	}
}

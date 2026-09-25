//go:build darwin || linux

package workspaceops

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openpty(3) leaves both descriptors inheritable. Before they were marked, every
// later shell held the primary side of every open terminal, its own included, so
// closing ours never hung the PTY up and closed terminals' shells lived on.
func TestTerminalPTYDescriptorsAreCloseOnExec(t *testing.T) {
	primary, replica, err := openTerminalPTY(80, 24)
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer primary.Close()
	defer replica.Close()
	for _, handle := range []*os.File{primary, replica} {
		flags, err := unix.FcntlInt(handle.Fd(), unix.F_GETFD, 0)
		if err != nil {
			t.Fatalf("fcntl %s: %v", handle.Name(), err)
		}
		if flags&unix.FD_CLOEXEC == 0 {
			t.Fatalf("%s descriptor is inheritable: every later child would hold it open", handle.Name())
		}
	}
}

// A descriptor the server holds without FD_CLOEXEC, like one inherited from its own
// parent, must not reach the shell. The shell holding a pipe's write end is visible
// as a missing EOF once our copy is closed.
func TestTerminalShellInheritsNoStrayDescriptors(t *testing.T) {
	// the fixture may start helper processes; only the shell must see the pipe
	dataDir, workspaceID := newTerminalTestWorkspace(t)
	var fds [2]int
	if err := unix.Pipe(fds[:]); err != nil {
		t.Fatalf("pipe: %v", err)
	}
	for _, fd := range fds {
		if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil || flags&unix.FD_CLOEXEC != 0 {
			t.Fatalf("test needs an inheritable descriptor; fd %d flags %d err %v", fd, flags, err)
		}
	}
	if err := unix.SetNonblock(fds[0], true); err != nil {
		t.Fatalf("nonblock: %v", err)
	}
	reader := os.NewFile(uintptr(fds[0]), "stray-read")
	defer reader.Close()
	writer := os.NewFile(uintptr(fds[1]), "stray-write")

	record := openTerminalTestSession(t, dataDir, workspaceID, 80, 24)
	live := liveTerminalSession(t, record.TerminalID)
	var offset int64
	runTerminalCommand(t, dataDir, workspaceID, record.TerminalID, &offset, "__READY__")

	if err := writer.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}
	_ = reader.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("shell %d still holds the write end (read err %v)", live.process.Process.Pid, err)
	}
}

// Closing one terminal while another is open ends the first shell, even though an
// interactive shell ignores SIGTERM and the second shell was started after the
// first PTY existed. The second terminal keeps working.
func TestCloseTerminalWhileAnotherTerminalIsOpen(t *testing.T) {
	dataDirA, workspaceA, first := newTerminalTestSession(t, 80, 24)
	liveA := liveTerminalSession(t, first.TerminalID)
	dataDirB, workspaceB, second := newTerminalTestSession(t, 80, 24)
	liveB := liveTerminalSession(t, second.TerminalID)
	var offsetA, offsetB int64
	runTerminalCommand(t, dataDirA, workspaceA, first.TerminalID, &offsetA, "__A_READY__")
	runTerminalCommand(t, dataDirB, workspaceB, second.TerminalID, &offsetB, "__B_READY__")

	if err := CloseTerminal(workspaceA, first.TerminalID); err != nil {
		t.Fatalf("close first terminal: %v", err)
	}
	requireTerminalTornDown(t, liveA)

	runTerminalCommand(t, dataDirB, workspaceB, second.TerminalID, &offsetB, "__B_STILL_UP__")
	if err := CloseTerminal(workspaceB, second.TerminalID); err != nil {
		t.Fatalf("close second terminal: %v", err)
	}
	requireTerminalTornDown(t, liveB)
}

// A child that left the shell's session with setsid(2) is still found through its
// parent and ended with the session.
func TestCloseTerminalEndsSetsidDescendant(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl is needed to call setsid(2) from the shell")
	}
	dataDir, workspaceID, record := newTerminalTestSession(t, 80, 24)
	live := liveTerminalSession(t, record.TerminalID)

	var offset int64
	command := `perl -MPOSIX -e 'my $p = fork; if ($p) { waitpid($p, 0); exit } POSIX::setsid(); print "__PID_ESCAPED__:$$:\n"; sleep 60' &` + "\n"
	if err := WriteTerminal(workspaceID, record.TerminalID, command); err != nil {
		t.Fatalf("write setsid command: %v", err)
	}
	pids := waitForTerminalPIDs(t, dataDir, workspaceID, record.TerminalID, &offset, "ESCAPED")
	escaped := pids["ESCAPED"]
	if sid, err := unix.Getsid(escaped); err != nil || sid == live.process.Process.Pid {
		t.Fatalf("escaped child %d should lead its own session, got sid %d err %v", escaped, sid, err)
	}

	if err := CloseTerminal(workspaceID, record.TerminalID); err != nil {
		t.Fatalf("close terminal: %v", err)
	}
	requireTerminalTornDown(t, live, escaped)
}

// The idle reaper and server shutdown tear sessions down the same way CloseTerminal
// does.
func TestReapAndShutdownTearDownTerminalSessions(t *testing.T) {
	dataDir, workspaceID, idle := newTerminalTestSession(t, 80, 24)
	liveIdle := liveTerminalSession(t, idle.TerminalID)
	var offset int64
	runTerminalCommand(t, dataDir, workspaceID, idle.TerminalID, &offset, "__IDLE_READY__")

	liveIdle.mu.Lock()
	liveIdle.lastActivity = time.Now().Add(-2 * terminalIdleTTL)
	liveIdle.mu.Unlock()
	reapIdleTerminals()
	requireTerminalTornDown(t, liveIdle)
	if _, ok := terminalSessions.Load(idle.TerminalID); ok {
		t.Fatal("reaped terminal must leave the session map")
	}

	_, _, first := newTerminalTestSession(t, 80, 24)
	liveA := liveTerminalSession(t, first.TerminalID)
	_, _, second := newTerminalTestSession(t, 80, 24)
	liveB := liveTerminalSession(t, second.TerminalID)
	closeAllTerminals()
	requireTerminalTornDown(t, liveA)
	requireTerminalTornDown(t, liveB)
	for _, id := range []string{first.TerminalID, second.TerminalID} {
		if _, ok := terminalSessions.Load(id); ok {
			t.Fatalf("shutdown must clear terminal %s from the session map", id)
		}
	}
}

// Rejecting a duplicate live terminal id must not leave the shell it had already
// started.
func TestOpenTerminalDuplicateIDLeavesNoShell(t *testing.T) {
	dataDir, workspaceID, record := newTerminalTestSession(t, 80, 24)
	live := liveTerminalSession(t, record.TerminalID)
	before := childSessionLeaders(t)

	duplicate := record.TerminalID
	if _, err := OpenTerminal(dataDir, TerminalOpenRequest{WorkspaceID: workspaceID, TerminalID: &duplicate}); err == nil {
		t.Fatal("a duplicate live terminal id must be rejected")
	}
	if after := childSessionLeaders(t); len(after) != len(before) {
		t.Fatalf("duplicate open left a shell behind: session leaders %v before, %v after", before, after)
	}
	if err := CloseTerminal(workspaceID, record.TerminalID); err != nil {
		t.Fatalf("close terminal: %v", err)
	}
	requireTerminalTornDown(t, live)
}

func TestListTerminalProcsSeesSelf(t *testing.T) {
	procs, err := listTerminalProcs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	sid, err := unix.Getsid(0)
	if err != nil {
		t.Fatalf("getsid: %v", err)
	}
	want := terminalProc{pid: os.Getpid(), ppid: os.Getppid(), pgid: unix.Getpgrp(), sid: sid}
	for _, proc := range procs {
		if proc.pid == want.pid {
			if proc.start == 0 {
				t.Fatalf("own row has no start time: %+v", proc)
			}
			proc.start = 0
			if proc != want {
				t.Fatalf("own row %+v, want %+v", proc, want)
			}
			return
		}
	}
	t.Fatal("own process missing from the table")
}

func liveTerminalSession(t *testing.T, terminalID string) *terminalSession {
	t.Helper()
	value, ok := terminalSessions.Load(terminalID)
	if !ok {
		t.Fatalf("terminal %s is not open", terminalID)
	}
	return value.(*terminalSession)
}

// runTerminalCommand prints marker from the shell and waits for the output, which
// proves the shell is up and reading.
func runTerminalCommand(t *testing.T, dataDir, workspaceID, terminalID string, offset *int64, marker string) {
	t.Helper()
	if err := WriteTerminal(workspaceID, terminalID, "printf '%s\\n' "+marker+"\n"); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	waitForTerminal(t, dataDir, workspaceID, terminalID, offset, func(read *TerminalReadResult, content string) bool {
		return regexp.MustCompile(`(?m)^` + marker + `\r?$`).MatchString(content)
	})
}

var terminalPIDPattern = regexp.MustCompile(`__PID_(\w+)__:(\d+):`)

// waitForTerminalPIDs waits for "__PID_<NAME>__:<pid>:" output for every name.
func waitForTerminalPIDs(t *testing.T, dataDir, workspaceID, terminalID string, offset *int64, names ...string) map[string]int {
	t.Helper()
	pids := map[string]int{}
	waitForTerminal(t, dataDir, workspaceID, terminalID, offset, func(read *TerminalReadResult, content string) bool {
		for _, match := range terminalPIDPattern.FindAllStringSubmatch(content, -1) {
			if pid, err := strconv.Atoi(match[2]); err == nil {
				pids[match[1]] = pid
			}
		}
		for _, name := range names {
			if pids[name] == 0 {
				return false
			}
		}
		return true
	})
	return pids
}

// requireTerminalTornDown asserts that shutdown finished with the PTY released, and
// that neither the shell, nor any of pids, nor any process in the shell's session
// or below it is still alive.
func requireTerminalTornDown(t *testing.T, session *terminalSession, pids ...int) {
	t.Helper()
	select {
	case <-session.tornDown:
	case <-time.After(10 * time.Second):
		t.Fatalf("terminal %s: teardown did not finish", session.terminalID)
	}
	select {
	case <-session.pumpDone:
	default:
		t.Fatalf("terminal %s: PTY pump still running after teardown", session.terminalID)
	}
	leader := session.process.Process.Pid
	pids = append(pids, leader)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var alive []int
		for _, pid := range pids {
			if terminalProcessAlive(t, pid) {
				alive = append(alive, pid)
			}
		}
		sweep := terminalSessionSweep{leader: leader, tracked: map[int]uint64{}}
		members, err := sweep.members()
		if err != nil {
			t.Fatalf("list processes: %v", err)
		}
		if len(alive) == 0 && len(members) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal %s left processes running: pids %v, session members %+v", session.terminalID, alive, members)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// terminalProcessAlive reports whether pid names a process that has not exited; a
// zombie waiting to be reaped counts as exited.
func terminalProcessAlive(t *testing.T, pid int) bool {
	t.Helper()
	if err := unix.Kill(pid, 0); err != nil {
		return false
	}
	procs, err := listTerminalProcs()
	if err != nil {
		t.Fatalf("list processes: %v", err)
	}
	for _, proc := range procs {
		if proc.pid == pid {
			return !proc.exited
		}
	}
	return false
}

// childSessionLeaders lists this process's live children that lead their own
// session: the terminal shells.
func childSessionLeaders(t *testing.T) []int {
	t.Helper()
	procs, err := listTerminalProcs()
	if err != nil {
		t.Fatalf("list processes: %v", err)
	}
	var leaders []int
	for _, proc := range procs {
		if proc.ppid == os.Getpid() && proc.sid == proc.pid && !proc.exited {
			leaders = append(leaders, proc.pid)
		}
	}
	return leaders
}

//go:build darwin || linux

package workspaceops

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
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

// A process that left the session and has no parent in it is still found while it
// holds the terminal: a daemon that forked, let its parent exit and called
// setsid(2), and, where util-linux provides it, the everyday `setsid cmd &`, which
// forks because a job leads its process group. Neither may keep the pump, the
// master or the log open either.
func TestCloseTerminalEndsDetachedProcessesHoldingThePTY(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl is needed to call setsid(2) from the shell")
	}
	dataDir, workspaceID, record := newTerminalTestSession(t, 80, 24)
	live := liveTerminalSession(t, record.TerminalID)

	names := []string{"DAEMON"}
	command := `perl -MPOSIX -e 'if (fork) { exit } POSIX::setsid(); print "__PID_DAEMON__:$$:\n"; sleep 60' &` + "\n"
	if _, err := exec.LookPath("setsid"); err == nil && runtime.GOOS == "linux" {
		names = append(names, "DETACHED")
		command += `setsid sh -c 'printf "__PID_DETACHED__:%s:\n" $$; exec sleep 61' &` + "\n"
	}
	if err := WriteTerminal(workspaceID, record.TerminalID, command); err != nil {
		t.Fatalf("write detached commands: %v", err)
	}
	var offset int64
	found := waitForTerminalPIDs(t, dataDir, workspaceID, record.TerminalID, &offset, names...)
	var pids []int
	for _, name := range names {
		pid := found[name]
		t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
		pids = append(pids, pid)
		requireOutsideSession(t, pid, live.process.Process.Pid)
	}

	if err := CloseTerminal(workspaceID, record.TerminalID); err != nil {
		t.Fatalf("close terminal: %v", err)
	}
	requireTerminalTornDown(t, live, pids...)
}

// A job that treats SIGHUP as a reload still gets to shut down cleanly: teardown
// follows SIGHUP with SIGTERM well before SIGKILL.
func TestCloseTerminalSendsSIGTERMAfterSIGHUP(t *testing.T) {
	dataDir, workspaceID, record := newTerminalTestSession(t, 80, 24)
	live := liveTerminalSession(t, record.TerminalID)
	marker := filepath.Join(t.TempDir(), "clean-exit")

	job := fmt.Sprintf(`sh -c 'trap "echo reload" HUP; trap "echo clean > \"\$1\"; exit 0" TERM; printf "__PID_JOB__:%%s:\n" $$; while :; do sleep 0.1; done' _ %s`, shellQuote(marker))
	if err := WriteTerminal(workspaceID, record.TerminalID, job+"\n"); err != nil {
		t.Fatalf("write job: %v", err)
	}
	var offset int64
	pid := waitForTerminalPIDs(t, dataDir, workspaceID, record.TerminalID, &offset, "JOB")["JOB"]

	if err := CloseTerminal(workspaceID, record.TerminalID); err != nil {
		t.Fatalf("close terminal: %v", err)
	}
	requireTerminalTornDown(t, live, pid)
	// macOS /bin/sh (bash) may flush the HUP trap's echo, stranded by the revoked
	// terminal, into the marker ahead of the TERM trap's line.
	if content, err := os.ReadFile(marker); err != nil || !strings.HasSuffix(string(content), "clean\n") {
		t.Fatalf("the job never got SIGTERM: marker %q, err %v", content, err)
	}
}

// While a close is still tearing the session down, a read does not report EOF and
// a write fails; once the close has returned, a read reports EOF.
func TestReadTerminalReportsNoEOFWhileCloseTearsDown(t *testing.T) {
	dataDir, workspaceID, record := newTerminalTestSession(t, 80, 24)
	live := liveTerminalSession(t, record.TerminalID)
	// Ignores SIGHUP and SIGTERM, so teardown waits out the grace period. A
	// non-interactive sh sets the dispositions: an interactive zsh already ignores
	// SIGTERM itself and restores it for exec.
	if err := WriteTerminal(workspaceID, record.TerminalID, "sh -c 'trap \"\" HUP TERM; exec sleep 60' & printf '__PID_STUBBORN__:%s:\\n' $!\n"); err != nil {
		t.Fatalf("write job: %v", err)
	}
	var offset int64
	pid := waitForTerminalPIDs(t, dataDir, workspaceID, record.TerminalID, &offset, "STUBBORN")["STUBBORN"]

	closed := make(chan error, 1)
	go func() { closed <- CloseTerminal(workspaceID, record.TerminalID) }()
	for deadline := time.Now().Add(5 * time.Second); !live.isClosed(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("close never started")
		}
	}
	read, err := ReadTerminal(dataDir, workspaceID, record.TerminalID, offset)
	if err != nil {
		t.Fatalf("read during close: %v", err)
	}
	if read.EOF {
		t.Fatal("a read during teardown reported EOF before the session was down")
	}
	if err := WriteTerminal(workspaceID, record.TerminalID, "true\n"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("a write during teardown must fail as closed, got %v", err)
	}

	if err := <-closed; err != nil {
		t.Fatalf("close terminal: %v", err)
	}
	requireTerminalTornDown(t, live, pid)
	read, err = ReadTerminal(dataDir, workspaceID, record.TerminalID, offset)
	if err != nil {
		t.Fatalf("read after close: %v", err)
	}
	if !read.EOF {
		t.Fatal("a read after the close returned must report EOF")
	}
}

// Known gap: a daemon that forks, lets its parent exit, calls setsid(2) and moves
// its stdio off the terminal keeps no link to the terminal that teardown can find.
// Only something like a cgroup per terminal would; the body shows the leak the
// independent working-directory check reports.
func TestCloseTerminalFullyDetachedDaemonIsNotFound(t *testing.T) {
	t.Skip("known gap: a daemon with no parent, session or descriptor in the terminal survives teardown")
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl is needed to call setsid(2) from the shell")
	}
	dataDir, workspaceID, record := newTerminalTestSession(t, 80, 24)
	live := liveTerminalSession(t, record.TerminalID)
	command := `perl -MPOSIX -e 'if (fork) { exit } POSIX::setsid(); print "__PID_GONE__:$$:\n"; open STDIN, "</dev/null"; open STDOUT, ">/dev/null"; open STDERR, ">/dev/null"; sleep 60' &` + "\n"
	if err := WriteTerminal(workspaceID, record.TerminalID, command); err != nil {
		t.Fatalf("write daemon command: %v", err)
	}
	var offset int64
	pid := waitForTerminalPIDs(t, dataDir, workspaceID, record.TerminalID, &offset, "GONE")["GONE"]
	t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
	if err := CloseTerminal(workspaceID, record.TerminalID); err != nil {
		t.Fatalf("close terminal: %v", err)
	}
	requireTerminalTornDown(t, live, pid)
}

// Only the shell's own session counts as the terminal's: a process that does not
// lead a session contributes nothing through its pid, and once the shell's pid
// shows another start time, a session with that id is not the terminal's.
func TestTerminalSessionSweepChecksTheLeader(t *testing.T) {
	notLeader := startSweepFixture(t, &syscall.SysProcAttr{Setpgid: true})
	sweep := terminalSessionSweep{leader: notLeader, descendants: true, tracked: map[int]uint64{}}
	if members := sweepMemberPIDs(t, &sweep); len(members) != 0 {
		t.Fatalf("pid %d leads no session, yet the sweep returned %v", notLeader, members)
	}

	leader := startSweepFixture(t, &syscall.SysProcAttr{Setsid: true})
	start, ok := terminalProcStart(leader)
	if !ok {
		t.Fatalf("no start time for %d", leader)
	}
	sweep = terminalSessionSweep{leader: leader, leaderStart: start + 1, descendants: true, tracked: map[int]uint64{}}
	if members := sweepMemberPIDs(t, &sweep); len(members) != 0 {
		t.Fatalf("a reused pid %d must not name the session, yet the sweep returned %v", leader, members)
	}
	sweep = terminalSessionSweep{leader: leader, leaderStart: start, descendants: true, tracked: map[int]uint64{}}
	if members := sweepMemberPIDs(t, &sweep); len(members) != 2 || !slices.Contains(members, leader) {
		t.Fatalf("session %d has the leader and its sleep, got %v", leader, members)
	}
}

// startSweepFixture starts `sh -c 'sleep 30 & wait'` and returns once the sleep
// child exists. The whole process group is killed at cleanup.
func startSweepFixture(t *testing.T, attr *syscall.SysProcAttr) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = unix.Kill(-pid, unix.SIGKILL)
		_ = cmd.Wait()
	})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		procs, err := listTerminalProcs()
		if err != nil {
			t.Fatalf("list processes: %v", err)
		}
		if slices.ContainsFunc(procs, func(proc terminalProc) bool { return proc.ppid == pid && !proc.exited }) {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture %d never started its child", pid)
		}
	}
}

func sweepMemberPIDs(t *testing.T, sweep *terminalSessionSweep) []int {
	t.Helper()
	members, err := sweep.members(false)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	var pids []int
	for _, member := range members {
		pids = append(pids, member.pid)
	}
	return pids
}

// requireOutsideSession asserts that pid neither is in the shell's session nor
// descends from a process that is, so only its hold on the terminal can find it.
func requireOutsideSession(t *testing.T, pid int, leader int) {
	t.Helper()
	procs, err := listTerminalProcs()
	if err != nil {
		t.Fatalf("list processes: %v", err)
	}
	parent := map[int]terminalProc{}
	for _, proc := range procs {
		parent[proc.pid] = proc
	}
	for at, hops := pid, 0; at > 1 && hops < 64; hops++ {
		proc, ok := parent[at]
		if !ok {
			return
		}
		if proc.sid == leader {
			t.Fatalf("pid %d is not detached: %d in its ancestry is in the shell's session", pid, at)
		}
		at = proc.ppid
	}
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

// requireTerminalTornDown asserts that shutdown finished with the pump done and
// that no process of the terminal is left: not the shell, none of pids, nothing in
// the shell's session and nothing working in the workspace. The last two read the
// process table directly rather than through the sweep teardown uses, so a process
// teardown fails to find still fails the test.
func requireTerminalTornDown(t *testing.T, session *terminalSession, pids ...int) {
	t.Helper()
	requireTerminalTornDownExcept(t, session, nil, pids...)
}

// requireTerminalTornDownExcept is requireTerminalTornDown for a terminal whose
// survivors are expected to outlive it.
func requireTerminalTornDownExcept(t *testing.T, session *terminalSession, survivors []int, pids ...int) {
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
	root, err := filepath.EvalSymlinks(session.process.Dir)
	if err != nil {
		t.Fatalf("resolve workspace root: %v", err)
	}
	leader := session.process.Process.Pid
	pids = append(pids, leader)
	deadline := time.Now().Add(5 * time.Second)
	for {
		left := terminalLeftovers(t, leader, root, pids, survivors)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal %s left processes running:\n%s", session.terminalID, strings.Join(left, "\n"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// terminalLeftovers describes each live process, other than survivors, that is
// one of pids, is in the shell's session, or works under root.
func terminalLeftovers(t *testing.T, leader int, root string, pids []int, survivors []int) []string {
	t.Helper()
	procs, err := listTerminalProcs()
	if err != nil {
		t.Fatalf("list processes: %v", err)
	}
	var left []string
	for _, proc := range procs {
		if proc.exited || proc.pid == os.Getpid() || slices.Contains(survivors, proc.pid) {
			continue
		}
		cwd, _ := terminalProcCwd(proc.pid)
		switch {
		case slices.Contains(pids, proc.pid):
			left = append(left, fmt.Sprintf("pid %d", proc.pid))
		case proc.sid == leader:
			left = append(left, fmt.Sprintf("pid %d in the shell's session", proc.pid))
		case cwd == root || strings.HasPrefix(cwd, root+"/"):
			left = append(left, fmt.Sprintf("pid %d working in %s", proc.pid, cwd))
		}
	}
	return left
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

//go:build darwin || linux

package workspaceops

import (
	"log"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

// terminalProc is one process-table row, reduced to what teardown needs.
type terminalProc struct {
	pid    int
	ppid   int
	pgid   int
	sid    int
	start  uint64 // start time in platform units; (pid, start) survives pid reuse
	exited bool   // zombie: already dead, only waiting to be reaped
}

const (
	// the process table is reread at terminalTeardownPoll, backing off to
	// terminalTeardownPollMax while stragglers wait out the grace period
	terminalTeardownPoll    = 20 * time.Millisecond
	terminalTeardownPollMax = 100 * time.Millisecond
	// terminalKillSettle bounds the wait after SIGKILL. Only a process stuck in an
	// uninterruptible sleep outlives it.
	terminalKillSettle = 2 * time.Second
)

// killTerminalSession ends every process the terminal's shell started and returns
// once they are gone. The shell runs with Setsid, so it leads a session whose id is
// its pid. Job control gives each background job its own process group inside that
// session, so signalling the shell's group alone missed them. An interactive shell
// ignores SIGTERM, so that signal never ended the shell itself.
//
// Members get SIGHUP, what a real terminal hang-up delivers, and SIGCONT so stopped
// jobs act on it. Members still alive after grace get SIGKILL.
func killTerminalSession(cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 1 {
		return
	}
	sweep := terminalSessionSweep{leader: cmd.Process.Pid, tracked: map[int]uint64{}}
	members, err := sweep.members()
	if err != nil {
		// Without a process table only the shell's own group is reachable. Signal
		// fails once the shell is reaped, which keeps a reused pid safe.
		log.Printf("terminal: list processes to tear down session %d: %v", sweep.leader, err)
		if cmd.Process.Signal(unix.SIGKILL) == nil {
			_ = unix.Kill(-sweep.leader, unix.SIGKILL)
		}
		return
	}
	if len(members) == 0 {
		return
	}
	signalTerminalMembers(members, unix.SIGHUP)
	signalTerminalMembers(members, unix.SIGCONT)
	poll := terminalTeardownPoll
	for deadline := time.Now().Add(grace); time.Now().Before(deadline); {
		time.Sleep(poll)
		if poll *= 2; poll > terminalTeardownPollMax {
			poll = terminalTeardownPollMax
		}
		if members, err = sweep.members(); err != nil || len(members) == 0 {
			return
		}
	}
	for settle := time.Now().Add(terminalKillSettle); time.Now().Before(settle); {
		signalTerminalMembers(members, unix.SIGKILL)
		time.Sleep(terminalTeardownPoll)
		if members, err = sweep.members(); err != nil || len(members) == 0 {
			return
		}
	}
	log.Printf("terminal: %d process(es) of session %d survived SIGKILL", len(members), sweep.leader)
}

// terminalSessionSweep finds the live processes of one terminal session.
type terminalSessionSweep struct {
	leader  int
	tracked map[int]uint64 // pid -> start of every process seen as a member
}

// members returns the live processes in the leader's session plus the descendants
// of the leader and of each member. The descendant walk catches a child that left
// the session with setsid(2) while its parent still runs. A process seen once stays
// a member by (pid, start) after it is reparented, and a reused pid never matches.
func (sweep *terminalSessionSweep) members() ([]terminalProc, error) {
	procs, err := listTerminalProcs()
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	byPID := make(map[int]int, len(procs))
	children := make(map[int][]int)
	isMember := make(map[int]bool)
	queue := []int{sweep.leader}
	for i, proc := range procs {
		if proc.exited || proc.pid <= 1 || proc.pid == self {
			continue
		}
		byPID[proc.pid] = i
		children[proc.ppid] = append(children[proc.ppid], proc.pid)
		start, seen := sweep.tracked[proc.pid]
		if proc.sid == sweep.leader || (seen && start == proc.start) {
			isMember[proc.pid] = true
			queue = append(queue, proc.pid)
		}
	}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if !isMember[child] {
				isMember[child] = true
				queue = append(queue, child)
			}
		}
	}
	members := make([]terminalProc, 0, len(isMember))
	for pid := range isMember {
		proc := procs[byPID[pid]]
		sweep.tracked[pid] = proc.start
		members = append(members, proc)
	}
	return members, nil
}

// signalTerminalMembers signals each member and each member's process group; the
// group signal also reaches a process forked after the table was read. The caller's
// own process and group are never signalled.
func signalTerminalMembers(members []terminalProc, sig unix.Signal) {
	self, selfGroup := os.Getpid(), unix.Getpgrp()
	signalled := make(map[int]bool)
	for _, proc := range members {
		if proc.pgid > 1 && proc.pgid != selfGroup && !signalled[proc.pgid] {
			signalled[proc.pgid] = true
			_ = unix.Kill(-proc.pgid, sig)
		}
		if proc.pid != self {
			_ = unix.Kill(proc.pid, sig)
		}
	}
}

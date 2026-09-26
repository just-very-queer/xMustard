//go:build darwin || linux

package workspaceops

import (
	"log"
	"os"
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
	// terminalTeardownPollMax while stragglers wait out a grace period
	terminalTeardownPoll    = 20 * time.Millisecond
	terminalTeardownPollMax = 100 * time.Millisecond
	// terminalKillSettle bounds the wait after SIGKILL. Only a process stuck in an
	// uninterruptible sleep outlives it.
	terminalKillSettle = 2 * time.Second
)

// endTerminalSession ends the processes a terminal's shell started, as mode says.
// The shell runs with Setsid, so it leads a session whose id is its pid, and job
// control gives each background job its own process group inside that session.
// A process that left the session is found as a descendant of a member, or by
// holding the terminal's replica.
//
// Members first get SIGHUP, what a real terminal hang-up delivers, and SIGCONT so
// stopped jobs act on it. For terminalHangUp that is all. For terminalKill, members
// still alive after terminalTermDelay also get SIGTERM, which programs that treat
// SIGHUP as a reload shut down on, and those alive after terminalKillGrace get
// SIGKILL; it returns once none is left.
func endTerminalSession(session *terminalSession, mode terminalTeardown) {
	cmd := session.process
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 1 {
		return
	}
	sweep := &terminalSessionSweep{
		leader:      cmd.Process.Pid,
		leaderStart: session.shellStart,
		ttyPath:     session.ttyPath,
		descendants: mode == terminalKill,
		tracked:     map[int]uint64{},
	}
	members, err := sweep.members(true)
	if err != nil {
		// Without a process table only the shell's own group is reachable. The
		// shell is not reaped before teardown ends, so its pid and group are ours.
		log.Printf("terminal: list processes to tear down session %d: %v", sweep.leader, err)
		if mode == terminalKill && cmd.Process.Signal(unix.SIGKILL) == nil {
			_ = unix.Kill(-sweep.leader, unix.SIGKILL)
		}
		return
	}
	if len(members) == 0 {
		return
	}
	signalTerminalMembers(members, unix.SIGHUP, unix.SIGCONT)
	if mode == terminalHangUp {
		return
	}
	hungUp := time.Now()
	if members = sweep.waitGone(hungUp.Add(terminalTermDelay)); len(members) == 0 {
		return
	}
	signalTerminalMembers(members, unix.SIGTERM, unix.SIGCONT)
	if members = sweep.waitGone(hungUp.Add(terminalKillGrace)); len(members) == 0 {
		return
	}
	for settle := time.Now().Add(terminalKillSettle); time.Now().Before(settle); {
		signalTerminalMembers(members, unix.SIGKILL)
		if members = sweep.waitGone(time.Now().Add(terminalTeardownPoll)); len(members) == 0 {
			return
		}
	}
	log.Printf("terminal: %d process(es) of session %d survived SIGKILL", len(members), sweep.leader)
}

// terminalSessionSweep finds the live processes of one terminal session.
type terminalSessionSweep struct {
	leader      int
	leaderStart uint64 // the shell's start time; 0 until known
	leaderLost  bool   // the shell's pid no longer names it, so its sid no longer names the session
	ttyPath     string // processes holding this replica are members
	descendants bool   // also take every descendant of a member
	tracked     map[int]uint64
}

// terminalMember is a process teardown signals.
type terminalMember struct {
	terminalProc
	// inSession: the process is in the shell's session, so its process group is
	// too, and the group may be signalled as well
	inSession bool
}

// waitGone rereads the process table until no member is left or deadline passes,
// and returns the members still alive. An empty quick scan is confirmed by a full
// one, which also looks for processes holding the replica.
func (sweep *terminalSessionSweep) waitGone(deadline time.Time) []terminalMember {
	for poll := terminalTeardownPoll; ; {
		wait := time.Until(deadline)
		if wait <= 0 {
			return sweep.mustMembers(true)
		}
		if wait > poll {
			wait = poll
		}
		time.Sleep(wait)
		if poll *= 2; poll > terminalTeardownPollMax {
			poll = terminalTeardownPollMax
		}
		if len(sweep.mustMembers(false)) == 0 && len(sweep.mustMembers(true)) == 0 {
			return nil
		}
	}
}

// mustMembers is members with a failed table read logged and taken as no members:
// nothing further can be found to signal.
func (sweep *terminalSessionSweep) mustMembers(scanTTY bool) []terminalMember {
	members, err := sweep.members(scanTTY)
	if err != nil {
		log.Printf("terminal: list processes of session %d: %v", sweep.leader, err)
		return nil
	}
	return members
}

// members returns the live processes of the terminal: those in the shell's session
// while the shell's pid still names it, those seen as members before (by pid and
// start, so a reused pid never matches), with scanTTY those holding the replica,
// and, if the sweep takes descendants, every descendant of these. The descendant
// walk catches a child that left the session with setsid(2) while its parent runs.
func (sweep *terminalSessionSweep) members(scanTTY bool) ([]terminalMember, error) {
	procs, err := listTerminalProcs()
	if err != nil {
		return nil, err
	}
	inSession := sweep.sessionIDValid(procs)
	var holders map[int]bool
	if scanTTY && sweep.ttyPath != "" {
		holders = terminalTTYHolders(sweep.ttyPath, procs)
	}
	self := os.Getpid()
	byPID := make(map[int]int, len(procs))
	children := make(map[int][]int)
	isMember := make(map[int]bool)
	var queue []int
	for i, proc := range procs {
		if proc.exited || proc.pid <= 1 || proc.pid == self {
			continue
		}
		byPID[proc.pid] = i
		children[proc.ppid] = append(children[proc.ppid], proc.pid)
		start, seen := sweep.tracked[proc.pid]
		if (inSession && proc.sid == sweep.leader) || (seen && start == proc.start) || holders[proc.pid] {
			isMember[proc.pid] = true
			queue = append(queue, proc.pid)
		}
	}
	for sweep.descendants && len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if !isMember[child] {
				isMember[child] = true
				queue = append(queue, child)
			}
		}
	}
	members := make([]terminalMember, 0, len(isMember))
	for pid := range isMember {
		proc := procs[byPID[pid]]
		sweep.tracked[pid] = proc.start
		members = append(members, terminalMember{terminalProc: proc, inSession: inSession && proc.sid == sweep.leader})
	}
	return members, nil
}

// sessionIDValid reports whether the shell's pid, and so the session id, still
// names this terminal's session: the shell's row, zombie or not, still shows the
// start time recorded when it started (or, if that was not recorded, on the first
// scan). Once the row is gone or shows another start time, the pid may have passed
// to another process and never names the session again; members are then only the
// processes already tracked, their descendants and holders of the replica.
func (sweep *terminalSessionSweep) sessionIDValid(procs []terminalProc) bool {
	if sweep.leaderLost {
		return false
	}
	for _, proc := range procs {
		if proc.pid != sweep.leader {
			continue
		}
		if sweep.leaderStart == 0 {
			sweep.leaderStart = proc.start
		}
		if proc.start == sweep.leaderStart {
			return true
		}
		break
	}
	sweep.leaderLost = true
	return false
}

// signalTerminalMembers sends each signal to every member, and to the process group
// of each member inside the shell's session; the group signal also reaches a
// process forked after the table was read. A process group never spans sessions,
// so such a group holds only the terminal's processes. The group of a process
// found only by holding the replica may not, so only the process is signalled.
// The caller's own process and group are never signalled.
func signalTerminalMembers(members []terminalMember, sigs ...unix.Signal) {
	self, selfGroup := os.Getpid(), unix.Getpgrp()
	for _, sig := range sigs {
		signalled := make(map[int]bool)
		for _, proc := range members {
			if proc.inSession && proc.pgid > 1 && proc.pgid != selfGroup && !signalled[proc.pgid] {
				signalled[proc.pgid] = true
				_ = unix.Kill(-proc.pgid, sig)
			}
			if proc.pid != self {
				_ = unix.Kill(proc.pid, sig)
			}
		}
	}
}

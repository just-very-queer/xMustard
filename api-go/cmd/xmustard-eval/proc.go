package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// startGroup starts cmd as the leader of a new process group, so the whole tree the
// client spawns (MCP servers, shells, test runners) can be signalled together. The
// group is recorded for the watchdog while Execute runs.
func startGroup(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if err := cmd.Start(); err != nil {
		return err
	}
	trackGroup(cmd.Process.Pid, true)
	return nil
}

// killGroup sends SIGTERM to the process group led by pid, waits up to grace for it
// to empty, then sends SIGKILL. It is safe to call after the leader has exited.
func killGroup(pid int, grace time.Duration) {
	if pid <= 0 {
		return
	}
	defer trackGroup(pid, false)
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && errors.Is(err, syscall.ESRCH) {
		return
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// ---- run marker and survivor sweep ----

// runMarkerVar is set, with a fresh random value per run, in the environment of every
// process the harness starts for that run (client, setup, verify, oracle, xMustard
// API). A process that leaves the client's process group (setsid, as Pi's bash tool
// does for every command) keeps the marker, so it can still be found and killed.
const runMarkerVar = "XMEVAL_RUN"

// sweepRunProcesses kills every process of the run that is still alive: one seen in
// the tree of a command the run started while it ran (tracked: pid -> start time, so
// a reused pid is left alone), one that carries marker in its environment (Linux;
// macOS no longer exposes environments), and one whose working directory is inside
// one of dirs. It repeats until none is found and returns how many it killed and any
// still alive after the last attempt. A process that detached and left dirs before a
// sampler saw it, with no readable environment, is not found: on macOS it keeps
// running under the profile of the command that started it, which hides every other
// run's directories and the oracle's judge copy; on Linux it dies with bwrap's pid
// namespace.
func sweepRunProcesses(marker string, dirs []string, tracked map[int]int64) (killed int, alive []int, err error) {
	for attempt := 0; attempt < 10; attempt++ {
		pids, err := findRunProcesses(marker, dirs, tracked)
		if err != nil {
			return killed, nil, err
		}
		if len(pids) == 0 {
			return killed, nil, nil
		}
		for _, p := range pids {
			if syscall.Kill(p, syscall.SIGKILL) == nil {
				killed++
			}
		}
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	alive, err = findRunProcesses(marker, dirs, tracked)
	return killed, alive, err
}

// findRunProcesses lists this user's processes (other than the executor) that are
// tracked and still the same process, carry marker in their environment, or have a
// working directory at or below one of dirs.
func findRunProcesses(marker string, dirs []string, tracked map[int]int64) ([]int, error) {
	var realDirs []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r
		}
		realDirs = append(realDirs, d)
	}
	var (
		mu    sync.Mutex
		found = map[int]bool{}
		errs  []error
		wg    sync.WaitGroup
	)
	add := func(pids []int, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, err)
		}
		for _, p := range pids {
			if p != os.Getpid() && p > 1 {
				found[p] = true
			}
		}
	}
	if marker != "" {
		wg.Add(1)
		go func() { defer wg.Done(); add(pidsWithEnv(marker)) }()
	}
	if len(realDirs) > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); add(pidsWithCwdIn(realDirs)) }()
	}
	var same []int
	for pid, start := range tracked {
		if now, ok := procStartTime(pid); ok && now == start {
			same = append(same, pid)
		}
	}
	add(same, nil)
	wg.Wait()
	out := make([]int, 0, len(found))
	for p := range found {
		out = append(out, p)
	}
	return out, errors.Join(errs...)
}

// cwdWithin reports whether a working directory is one of dirs or below one.
func cwdWithin(cwd string, dirs []string) bool {
	for _, d := range dirs {
		if cwd == d || isWithin(cwd, d) {
			return true
		}
	}
	return false
}

// procSet is every process seen in the trees of the commands a run started (setup,
// the client, verify, the oracle): pid -> start time. A process that left its
// command's session is reparented away from the tree, so the sweep uses this record
// to find it again.
type procSet struct {
	mu sync.Mutex
	m  map[int]int64
}

func newProcSet() *procSet { return &procSet{m: map[int]int64{}} }

func (s *procSet) merge(m map[int]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for pid, start := range m {
		s.m[pid] = start
	}
}

func (s *procSet) snapshot() map[int]int64 {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int]int64, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}

// watchTree records every process under root into set, sampling every interval
// until the returned stop function is called.
func watchTree(root int, set *procSet, interval time.Duration) (stop func()) {
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		seen := map[int]int64{}
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			procs, err := psSnapshot(ctx)
			cancel()
			if err == nil {
				_, _, tree := splitTrees(procs, nil, root, nil)
				for _, pid := range tree {
					if _, ok := seen[pid]; !ok {
						if start, ok := procStartTime(pid); ok {
							seen[pid] = start
						}
					}
				}
			}
			select {
			case <-quit:
				set.merge(seen)
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(quit); <-done }
}

// newRunMarker returns a fresh "XMEVAL_RUN=<random>" environment entry.
func newRunMarker() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s=%x", runMarkerVar, b), nil
}

// ---- watchdog ----

// The watchdog outlives an executor that dies without cleaning up (SIGKILL, an OOM
// kill, a test timeout). Execute starts it with a pipe on its stdin and a state file
// naming the live process groups and the current run's marker and worktree. On a
// normal exit the executor writes "done"; if the pipe closes first, the executor is
// gone, and the watchdog kills those groups, sweeps the run's escaped processes and
// removes the work root. It is a child of the executor, outside both measured trees.

type groupTracker struct {
	mu     sync.Mutex
	path   string
	groups map[int]bool
	marker string
	dirs   []string
}

var (
	trackerMu sync.Mutex
	tracker   *groupTracker
)

func trackGroup(pid int, live bool) {
	trackerMu.Lock()
	t := tracker
	trackerMu.Unlock()
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if live {
		t.groups[pid] = true
	} else {
		delete(t.groups, pid)
	}
	t.writeLocked()
}

// trackRun records the current run's marker and directories for the watchdog.
func trackRun(marker string, dirs ...string) {
	trackerMu.Lock()
	t := tracker
	trackerMu.Unlock()
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.marker, t.dirs = marker, slices.Clone(dirs)
	t.writeLocked()
}

func (t *groupTracker) writeLocked() {
	var b strings.Builder
	if t.marker != "" {
		fmt.Fprintf(&b, "marker %s\n", t.marker)
	}
	for _, d := range t.dirs {
		fmt.Fprintf(&b, "dir %s\n", d)
	}
	for _, g := range sortedKeys(intKeys(t.groups)) {
		fmt.Fprintf(&b, "group %s\n", g)
	}
	tmp := t.path + ".tmp"
	if os.WriteFile(tmp, []byte(b.String()), 0o600) == nil {
		_ = os.Rename(tmp, t.path)
	}
}

func intKeys(m map[int]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[strconv.Itoa(k)] = true
	}
	return out
}

type watchdog struct {
	cmd *exec.Cmd
	in  io.WriteCloser
}

// startWatchdog installs the group tracker and starts the watchdog process.
func startWatchdog(self, statePath, removeDir string) (*watchdog, error) {
	t := &groupTracker{path: statePath, groups: map[int]bool{}}
	t.mu.Lock()
	t.writeLocked()
	t.mu.Unlock()
	args := []string{"watchdog", "--state", statePath}
	if removeDir != "" {
		args = append(args, "--remove", removeDir)
	}
	cmd := exec.Command(self, args...)
	cmd.Env = append(scrubbedEnv(), envAsMain+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // terminal signals go to the executor only
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	trackerMu.Lock()
	tracker = t
	trackerMu.Unlock()
	return &watchdog{cmd: cmd, in: in}, nil
}

// stop tells the watchdog the executor finished cleanly and uninstalls the tracker.
func (w *watchdog) stop() {
	trackerMu.Lock()
	tracker = nil
	trackerMu.Unlock()
	_, _ = io.WriteString(w.in, "done\n")
	_ = w.in.Close()
	done := make(chan struct{})
	go func() { _ = w.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = w.cmd.Process.Kill()
		<-done
	}
}

// runWatchdog is `xmustard-eval watchdog --state FILE [--remove DIR]`.
func runWatchdog(args []string, stdin io.Reader) int {
	fs := flag.NewFlagSet("watchdog", flag.ContinueOnError)
	state := fs.String("state", "", "state file the executor maintains")
	remove := fs.String("remove", "", "work root to remove if the executor dies")
	if err := fs.Parse(args); err != nil || *state == "" {
		return 2
	}
	signal.Ignore(os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		if sc.Text() == "done" {
			return 0
		}
	}
	cleanupAfterExecutor(*state, *remove)
	return 0
}

// cleanupAfterExecutor is what the watchdog does when the executor died.
func cleanupAfterExecutor(statePath, removeDir string) {
	b, _ := os.ReadFile(statePath)
	marker, dirs := "", []string(nil)
	for _, line := range strings.Split(string(b), "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "group":
			if pid, err := strconv.Atoi(val); err == nil && pid > 1 {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		case "marker":
			marker = val
		case "dir":
			dirs = append(dirs, val)
		}
	}
	if marker != "" || len(dirs) > 0 {
		_, _, _ = sweepRunProcesses(marker, dirs, nil)
	}
	if removeDir != "" {
		_ = os.RemoveAll(removeDir)
	}
}

package main

import (
	"bufio"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
// the client's process tree while it ran (tracked: pid -> start time, so a reused pid
// is left alone), one that carries marker in its environment (Linux; macOS no longer
// exposes environments), and one whose working directory is inside dir. It repeats
// until none is found and returns how many it killed and any still alive after the
// last attempt. A process that detached and left the worktree before the sampler saw
// it, with no readable environment, is not found; it stays inside the run's
// containment (and on Linux dies with bwrap's pid namespace).
func sweepRunProcesses(marker, dir string, tracked map[int]int64) (killed int, alive []int, err error) {
	for attempt := 0; attempt < 10; attempt++ {
		pids, err := findRunProcesses(marker, dir, tracked)
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
	alive, err = findRunProcesses(marker, dir, tracked)
	return killed, alive, err
}

// findRunProcesses lists this user's processes (other than the executor) that are
// tracked and still the same process, carry marker in their environment, or have a
// working directory at or below dir.
func findRunProcesses(marker, dir string, tracked map[int]int64) ([]int, error) {
	realDir := ""
	if dir != "" {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			realDir = r
		} else {
			realDir = dir
		}
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
	if realDir != "" {
		wg.Add(1)
		go func() { defer wg.Done(); add(pidsWithCwdIn(realDir)) }()
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
	dir    string
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

func trackRun(marker, dir string) {
	trackerMu.Lock()
	t := tracker
	trackerMu.Unlock()
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.marker, t.dir = marker, dir
	t.writeLocked()
}

func (t *groupTracker) writeLocked() {
	var b strings.Builder
	if t.marker != "" {
		fmt.Fprintf(&b, "marker %s\n", t.marker)
	}
	if t.dir != "" {
		fmt.Fprintf(&b, "dir %s\n", t.dir)
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
	marker, dir := "", ""
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
			dir = val
		}
	}
	if marker != "" || dir != "" {
		_, _, _ = sweepRunProcesses(marker, dir, nil)
	}
	if removeDir != "" {
		_ = os.RemoveAll(removeDir)
	}
}

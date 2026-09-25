package rustcore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
)

func withPool(t *testing.T, max int64) *budget.ByteBudget {
	t.Helper()
	prev := budget.TransientBytes
	b := budget.NewByteBudget(max)
	budget.TransientBytes = b
	t.Cleanup(func() { budget.TransientBytes = prev })
	return b
}

// busyPool installs a pool of max bytes with held of them reserved by another request,
// so a capture that would fit the pool on its own is refused for now: the retryable
// overload path, which the audit tests below guard.
func busyPool(t *testing.T, max, held int64) *budget.ByteBudget {
	t.Helper()
	pool := withPool(t, max)
	other := budget.NewScope(pool)
	if err := other.Acquire(held); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	return pool
}

const busyMax, busyHeld = 5 << 20, 4 << 20

func floodingCore(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "xmustard-core")
	script := "#!/bin/sh\nhead -c 4194304 /dev/zero | tr '\\0' 'a'\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Audit Go #5: runCoreCtx captured up to 64 MiB without charging the shared pool. With
// 1 MiB of a 5 MiB pool free, a 4 MiB capture (which would fit an idle pool) is refused
// as a retryable overload and never drives the pool past its ceiling.
func TestRunCoreCaptureNeverExceedsPool(t *testing.T) {
	pool := busyPool(t, busyMax, busyHeld)
	t.Setenv("XMUSTARD_CORE_BIN", floodingCore(t))
	out, err := runCoreCtx(context.Background(), "search", "x")
	if !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("4 MiB capture with 1 MiB of the pool free: want ErrOverloaded, got %d bytes, %v", len(out), err)
	}
	if pool.Peak() > pool.Max() {
		t.Fatalf("pool peak %d exceeded max %d", pool.Peak(), pool.Max())
	}
	if pool.InUse() != busyHeld {
		t.Fatalf("reservation leaked: %d in use after return", pool.InUse()-busyHeld)
	}
}

// Audit Go #5: runBoundedCmd charged a 64 MiB reservation unconditionally, driving the
// pool past its ceiling instead of refusing.
func TestRunBoundedCmdNeverExceedsPool(t *testing.T) {
	pool := busyPool(t, busyMax, busyHeld)
	_, _, _, err := runBoundedCmd(exec.Command(floodingCore(t)))
	if !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("4 MiB bounded capture with 1 MiB of the pool free: want ErrOverloaded, got %v", err)
	}
	if pool.Peak() > pool.Max() {
		t.Fatalf("pool peak %d exceeded max %d", pool.Peak(), pool.Max())
	}
	if pool.InUse() != busyHeld {
		t.Fatalf("reservation leaked: %d", pool.InUse()-busyHeld)
	}
}

// sizedCore prints n bytes of output.
func sizedCore(t *testing.T, n int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "xmustard-core")
	script := "#!/bin/sh\nhead -c " + strconv.Itoa(n) + " /dev/zero | tr '\\0' 'a'\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Output the pool could never hold is the permanent "output too large" error, never a
// retryable overload that an idle server answers the same way forever. Under the default
// 24 MiB pool a request-scoped call (output plus two decode copies held to the end)
// takes about a third of the pool and an owned call about all of it.
func TestCoreOutputThePoolCanNeverHoldIsPermanent(t *testing.T) {
	pool := withPool(t, budget.DefaultTransientBudgetBytes)
	request := func() (context.Context, func()) {
		s := budget.NewScope(nil)
		return budget.WithScope(context.Background(), s), s.Close
	}
	for _, tc := range []struct {
		name   string
		bytes  int
		scoped bool
		ok     bool
	}{
		{"5 MiB in a request", 5 << 20, true, true},
		{"10 MiB in a request", 10 << 20, true, false},
		{"10 MiB owned", 10 << 20, false, true},
		{"30 MiB owned", 30 << 20, false, false},
	} {
		t.Setenv("XMUSTARD_CORE_BIN", sizedCore(t, tc.bytes))
		ctx, done := context.Background(), func() {}
		if tc.scoped {
			ctx, done = request()
		}
		out, err := runCoreCtx(ctx, "search", "x")
		done()
		if tc.ok && (err != nil || len(out) != tc.bytes) {
			t.Fatalf("%s: %d bytes, %v", tc.name, len(out), err)
		}
		if !tc.ok && (err == nil || errors.Is(err, budget.ErrOverloaded) || !strings.Contains(err.Error(), "output too large")) {
			t.Fatalf("%s: want the permanent output-too-large error, got %v", tc.name, err)
		}
		if pool.InUse() != 0 {
			t.Fatalf("%s: %d bytes leaked", tc.name, pool.InUse())
		}
	}
	// the bounded runner reports it as over the cap, not as a refusal
	withPool(t, 1<<20)
	_, _, over, err := runBoundedCmd(exec.Command(floodingCore(t)))
	if !over || errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("4 MiB under an idle 1 MiB pool: want over=true, got over=%v err=%v", over, err)
	}
}

// floodThenSleep floods 4 MiB, then keeps the pipe open while sleeping, so the
// child only exits if the bridge actively kills it on refusal. It records its PID.
func floodThenSleep(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	p := filepath.Join(dir, "xmustard-core")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nhead -c 4194304 /dev/zero | tr '\\0' 'a'\nexec sleep 30\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p, pidFile
}

func assertKilledPromptly(t *testing.T, run func() error, pidFile string) {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- run() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("flood under a 1 MiB pool succeeded")
		}
	case <-time.After(5 * time.Second):
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		<-done
		t.Fatalf("refused capture did not stop the child: call blocked %v until the child was killed externally", time.Since(start))
	}
	raw, _ := os.ReadFile(pidFile)
	if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid > 0 && syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("child %d still alive after refusal", pid)
	}
}

// Root review: returning an error from the capture writer only stops pipe copying; a
// child that floods then sleeps keeps running. Refusal must kill the child.
func TestRunCoreRefusalKillsFloodThenSleepChild(t *testing.T) {
	pool := busyPool(t, busyMax, busyHeld)
	core, pidFile := floodThenSleep(t)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	assertKilledPromptly(t, func() error {
		_, err := runCoreCtx(context.Background(), "search", "x")
		if !errors.Is(err, budget.ErrOverloaded) {
			t.Errorf("want ErrOverloaded from a started, refused child; got %v", err)
		}
		return err
	}, pidFile)
	if pool.InUse() != busyHeld {
		t.Fatalf("reservation leaked: %d", pool.InUse()-busyHeld)
	}
}

// Output past the pool-derived cap kills the child just as a refusal does.
func TestRunCoreOverCapKillsFloodThenSleepChild(t *testing.T) {
	pool := withPool(t, 1<<20)
	core, pidFile := floodThenSleep(t)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	assertKilledPromptly(t, func() error {
		_, err := runCoreCtx(context.Background(), "search", "x")
		if err == nil || errors.Is(err, budget.ErrOverloaded) {
			t.Errorf("want the permanent output-too-large error; got %v", err)
		}
		return err
	}, pidFile)
	if pool.InUse() != 0 {
		t.Fatalf("reservation leaked: %d", pool.InUse())
	}
}

func TestRunBoundedCmdRefusalKillsFloodThenSleepChild(t *testing.T) {
	pool := busyPool(t, busyMax, busyHeld)
	core, pidFile := floodThenSleep(t)
	assertKilledPromptly(t, func() error {
		_, _, _, err := runBoundedCmd(exec.Command(core))
		if !errors.Is(err, budget.ErrOverloaded) {
			t.Errorf("want ErrOverloaded from a started, refused child; got %v", err)
		}
		return err
	}, pidFile)
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("child never started: %v", err)
	}
	if pool.InUse() != busyHeld {
		t.Fatalf("reservation leaked: %d", pool.InUse()-busyHeld)
	}
}

// treeCore starts a background grandchild (both record PIDs) and then either sleeps
// or floods-then-waits, so only killing the whole owned tree ends both processes.
func treeCore(t *testing.T, flood bool) (core, childPID, grandPID string) {
	t.Helper()
	dir := t.TempDir()
	childPID, grandPID = filepath.Join(dir, "child.pid"), filepath.Join(dir, "grand.pid")
	body := "#!/bin/sh\necho $$ > " + childPID + "\nsh -c 'echo $$ > " + grandPID + "; exec sleep 30' &\n"
	if flood {
		body += "while [ ! -s " + grandPID + " ]; do sleep 0.01; done\nhead -c 4194304 /dev/zero | tr '\\0' 'a'\nwait\n"
	} else {
		body += "wait\n"
	}
	core = filepath.Join(dir, "xmustard-core")
	if err := os.WriteFile(core, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return core, childPID, grandPID
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(raw), "\n") {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

func assertTreeGone(t *testing.T, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for _, pid := range pids {
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if syscall.Kill(pid, 0) == nil {
			for _, p := range pids {
				_ = syscall.Kill(p, syscall.SIGKILL)
			}
			t.Fatalf("owned process %d survived (tree %v)", pid, pids)
		}
	}
}

// Root follow-up: cancellation must end the Rust child AND its descendants (Rust
// spawns ast-grep/LSP), not only the immediate child.
func TestRunCoreCancelKillsChildAndGrandchild(t *testing.T) {
	core, cp, gp := treeCore(t, false)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := runCoreCtx(ctx, "search", "x"); done <- err }()
	child, grand := readPID(t, cp), readPID(t, gp)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(child, syscall.SIGKILL)
		_ = syscall.Kill(grand, syscall.SIGKILL)
		t.Fatal("cancelled call did not return")
	}
	assertTreeGone(t, child, grand)
}

// Root follow-up: a capture refusal must also end the whole owned tree.
func TestRunCoreRefusalKillsChildAndGrandchild(t *testing.T) {
	busyPool(t, busyMax, busyHeld)
	core, cp, gp := treeCore(t, true)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	done := make(chan error, 1)
	go func() { _, err := runCoreCtx(context.Background(), "search", "x"); done <- err }()
	child, grand := readPID(t, cp), readPID(t, gp)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("flood under a 1 MiB pool succeeded")
		}
	case <-time.After(8 * time.Second):
		_ = syscall.Kill(child, syscall.SIGKILL)
		_ = syscall.Kill(grand, syscall.SIGKILL)
		t.Fatal("refused call did not return")
	}
	assertTreeGone(t, child, grand)
}

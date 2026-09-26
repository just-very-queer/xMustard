package rustcore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
)

// The whole-repository builds take the heavy slot: they show as its owner while they
// run, a busy slot makes them wait its bound and then ErrOverloaded without running,
// a hook or capture path is refused at once, and queries never touch the slot.
func TestHeavyCoreBuildsTakeTheHeavySlot(t *testing.T) {
	logPath := useFakeWorker(t, "ok")
	g := quietGovernor(t, budget.GovernorConfig{HeavyWait: 400 * time.Millisecond})
	ctx := context.Background()
	before := g.Snapshot().HeavySlot

	t.Setenv("XMUSTARD_FAKE_ONESHOT_SLEEP_MS", "300")
	done := make(chan error, 1)
	go func() {
		_, err := runCoreCtx(ctx, "symbolgraph", "build", "/r", "ws")
		done <- err
	}()
	waitFor(t, "the build to hold the slot", 3*time.Second, func() bool { return g.Snapshot().HeavySlot.Busy })
	if h := g.Snapshot().HeavySlot; h.Owner != "rust:symbolgraph/build" || h.DeclaredBytes != heavyBuildBytes {
		t.Fatalf("heavy slot while the build runs: %+v", h)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_FAKE_ONESHOT_SLEEP_MS", "")
	out, err := runCoreCtx(ctx, "changetrack", "index", "/d", "/r", "ws")
	if err != nil || strings.TrimSpace(string(out)) != `{"oneshot":"changetrack"}` {
		t.Fatalf("changetrack index: %s %v", out, err)
	}
	for _, call := range [][]string{{"symbolgraph", "impact", "/r", "ws", "X", "2"}, {"symbolgraph", "blast-radius", "/r", "ws", "X"}, {"changetrack", "drift", "/d", "/r", "ws"}, {"echo", "q"}} {
		if _, err := runCoreCtx(ctx, call[0], call[1:]...); err != nil {
			t.Fatalf("%v: %v", call, err)
		}
	}
	after := g.Snapshot().HeavySlot
	if after.Acquired-before.Acquired != 2 || after.Released-before.Released != 2 || after.FreeOSMemoryCalls-before.FreeOSMemoryCalls != 2 {
		t.Fatalf("two builds, two holds: before %+v after %+v", before, after)
	}

	release, err := g.AcquireHeavy(ctx, "test_holder", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	execs := len(fakeLog(t, logPath, "oneshot symbolgraph"))
	start := time.Now()
	_, err = runCoreCtx(ctx, "symbolgraph", "build", "/r", "ws")
	if !errors.Is(err, budget.ErrOverloaded) || !strings.Contains(err.Error(), "test_holder") {
		t.Fatalf("a build behind a busy slot: want ErrOverloaded naming the holder, got %v", err)
	}
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("refused after %s, before the 400ms wait bound", d)
	}
	start = time.Now()
	if _, err := runCoreCtx(budget.WithoutHeavyWait(ctx), "changetrack", "index", "/d", "/r", "ws"); !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("a hot path: want ErrOverloaded at once, got %v", err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("a hot path waited %s for the heavy slot", d)
	}
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := runCoreCtx(cctx, "symbolgraph", "build", "/r", "ws"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a caller that gives up while waiting: want its context error, got %v", err)
	}
	if n := len(fakeLog(t, logPath, "oneshot symbolgraph")); n != execs {
		t.Fatalf("refused builds must not run: %d -> %d execs", execs, n)
	}
	if n := len(fakeLog(t, logPath, "oneshot changetrack")); n != 2 { // index, then drift
		t.Fatalf("changetrack execs: %d", n)
	}
}

// A build whose declaration the soft ceiling can never admit is refused permanently
// (ErrTooLarge, not a retryable overload), without running.
func TestHeavyCoreBuildThatCanNeverFitIsPermanent(t *testing.T) {
	logPath := useFakeWorker(t, "ok")
	quietGovernor(t, budget.GovernorConfig{SoftCeilingBytes: 40 << 20}) // 40 - 22 MiB daemon < 44 MiB
	_, err := runCoreCtx(context.Background(), "symbolgraph", "build", "/r", "ws")
	if !errors.Is(err, budget.ErrTooLarge) || errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if n := len(fakeLog(t, logPath, "oneshot ")); n != 0 {
		t.Fatalf("a refused build ran: %d execs", n)
	}
}

// The heavy table: whole-repository builds and WS-07's index writes are heavy with
// their declarations; reads and queries of the same families are not.
func TestHeavyCoreOpClassification(t *testing.T) {
	for _, tc := range []struct {
		call     []string
		owner    string
		declared int64
	}{
		{[]string{"symbolgraph", "build", "/r", "ws"}, "rust:symbolgraph/build", heavyBuildBytes},
		{[]string{"changetrack", "index", "/d", "/r", "ws"}, "rust:changetrack/index", heavyBuildBytes},
		{[]string{"index", "build", "/r"}, "rust:index/build", heavyIndexBytes},
		{[]string{"index", "update", "/r", "--paths", "a.go"}, "rust:index/update", heavyIndexBytes},
		{[]string{"index", "stats", "/r"}, "", 0},
		{[]string{"symbolgraph", "impact", "/r", "ws", "X", "2"}, "", 0},
		{[]string{"changetrack", "drift", "/d", "/r", "ws"}, "", 0},
		{[]string{"index"}, "", 0},
	} {
		owner, declared, ok := heavyCoreOp(tc.call[0], tc.call[1:])
		if owner != tc.owner || declared != tc.declared || ok != (tc.owner != "") {
			t.Errorf("%v: got %q %d %v, want %q %d", tc.call, owner, declared, ok, tc.owner, tc.declared)
		}
	}
}

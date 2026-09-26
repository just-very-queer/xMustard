package rustcore

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
)

// quietGovernor swaps in a process governor for one test. Unless cfg brings its own
// sampler, the tree measures a steady 10 MiB, so admission does not depend on this test
// binary's memory.
func quietGovernor(t *testing.T, cfg budget.GovernorConfig) *budget.Governor {
	t.Helper()
	if cfg.Sampler == nil {
		cfg.Sampler = func() (budget.TreeSample, error) {
			return budget.TreeSample{At: time.Now(), Supported: true, Basis: "test", Processes: 1, RSSBytes: 10 << 20, SelfRSSBytes: 10 << 20}, nil
		}
	}
	if cfg.FreeOSMemory == nil {
		cfg.FreeOSMemory = func() {}
	}
	prev := budget.Gov
	g := budget.NewProcessGovernor(cfg)
	budget.Gov = g
	t.Cleanup(func() { budget.Gov = prev })
	return g
}

// workerTree is a sampler: base bytes, plus worker bytes for each tracked worker pid
// that has not exited yet (a retired worker counts until it is gone).
type workerTree struct {
	base, worker atomic.Int64
	mu           sync.Mutex
	pids         []int
}

func newWorkerTree(base, worker int64) *workerTree {
	w := &workerTree{}
	w.base.Store(base)
	w.worker.Store(worker)
	return w
}

func (w *workerTree) track(pid int) {
	w.mu.Lock()
	w.pids = append(w.pids, pid)
	w.mu.Unlock()
}

func (w *workerTree) sample() (budget.TreeSample, error) {
	rss := w.base.Load()
	w.mu.Lock()
	for _, pid := range w.pids {
		if !processGone(pid) {
			rss += w.worker.Load()
		}
	}
	w.mu.Unlock()
	return budget.TreeSample{At: time.Now(), Supported: true, Basis: "test", Processes: 1, RSSBytes: rss, SelfRSSBytes: w.base.Load()}, nil
}

func component(t *testing.T, s budget.Snapshot, name string) budget.ComponentStatus {
	t.Helper()
	for _, c := range s.Reservations.Components {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("component %q missing: %+v", name, s.Reservations.Components)
	return budget.ComponentStatus{}
}

func measurable() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "linux" }

func activeCalls() int {
	coreWorker.mu.Lock()
	defer coreWorker.mu.Unlock()
	if coreWorker.proc == nil {
		return -1
	}
	return coreWorker.proc.active
}

// The worker is a governed component on every process governor: it declares its line
// while XMUSTARD_CORE_WORKER is on (and reserves nothing while it is off), reports the
// running worker's RSS, is not counted a second time under helper_children, and, being
// reclaimable, does not lower the largest heavy declaration that can ever be admitted.
func TestWorkerIsAGovernedComponent(t *testing.T) {
	useFakeWorker(t, "ok")
	const children = int64(1 << 30)
	g := quietGovernor(t, budget.GovernorConfig{Sampler: func() (budget.TreeSample, error) {
		return budget.TreeSample{At: time.Now(), Supported: true, Basis: "test", Processes: 2, RSSBytes: 10<<20 + children, SelfRSSBytes: 10 << 20}, nil
	}})
	s := g.Snapshot()
	w := component(t, s, "rust_worker")
	if !w.Enabled || !w.Reclaimable || w.Kind != budget.ComponentResident || w.ReservedSteadyBytes != workerSteadyBytes ||
		w.ReservedPeakBytes != workerPeakBytes || w.UsedBytes == nil || *w.UsedBytes != 0 || w.UsedBasis != "ps_rss" {
		t.Fatalf("a stopped, enabled worker: %+v", w)
	}
	daemon := component(t, s, "go_daemon")
	if s.HeavySlot.MaxAdmissible != s.SoftCeilingBytes-daemon.ReservedSteadyBytes {
		t.Fatalf("a reclaimable worker must not lower the permanent heavy bound: max %d, soft %d, daemon %d",
			s.HeavySlot.MaxAdmissible, s.SoftCeilingBytes, daemon.ReservedSteadyBytes)
	}

	mustEcho(t, "a")
	s = g.Snapshot()
	w, helper := component(t, s, "rust_worker"), component(t, s, "helper_children")
	if measurable() {
		if w.UsedBytes == nil || *w.UsedBytes <= 0 || helper.UsedBytes == nil {
			t.Fatalf("the running worker's RSS must be measured: worker %+v helper %+v", w, helper)
		}
		// measured a moment apart, so allow for drift
		if sum := *w.UsedBytes + *helper.UsedBytes; sum < children-(4<<20) || sum > children+(4<<20) {
			t.Fatalf("helper_children must leave the worker out: worker %d + helper %d, children %d", *w.UsedBytes, *helper.UsedBytes, children)
		}
	}
	h := CoreWorkerHealth()
	if !h.Running || h.PID == 0 || h.Policy == "" || h.ReservedPeakBytes != workerPeakBytes || h.IdleExitMS <= 0 {
		t.Fatalf("worker health: %+v", h)
	}
	if measurable() && (h.RSSBytes == nil || *h.RSSBytes <= 0 || h.FootprintBytes == nil) {
		t.Fatalf("worker health must carry its memory: %+v", h)
	}

	t.Setenv("XMUSTARD_CORE_WORKER", "0")
	off := g.Snapshot()
	if w := component(t, off, "rust_worker"); w.Enabled || off.Reservations.SteadyTotalBytes != s.Reservations.SteadyTotalBytes-workerSteadyBytes {
		t.Fatalf("a disabled worker reserves nothing: %+v, steady %d -> %d", w, s.Reservations.SteadyTotalBytes, off.Reservations.SteadyTotalBytes)
	}
}

// Heavy work refused for memory asks the worker for it: an idle worker is retired at
// once, the governor waits for its exit, measures again, and admits the work.
func TestHeavyAdmissionRecyclesAnIdleWorkerAndWaitsForItsExit(t *testing.T) {
	logPath := useFakeWorker(t, "ok")
	tree := newWorkerTree(20<<20, 60<<20)
	g := quietGovernor(t, budget.GovernorConfig{SoftCeilingBytes: 100 << 20, Sampler: tree.sample})
	mustEcho(t, "warm")
	pid := CoreWorkerStats().PID
	tree.track(pid)

	release, err := g.AcquireHeavy(context.Background(), "test_index", 30<<20)
	if err != nil {
		t.Fatalf("20 + 60 (worker) + 30 MiB over 100: the idle worker must be recycled and the work admitted: %v", err)
	}
	release()
	if !processGone(pid) {
		t.Fatal("heavy work was admitted before the recycled worker exited")
	}
	if n := CoreWorkerStats().Recycles[budget.PressureHeavyAdmission]; n != 1 {
		t.Fatalf("recycles: %v", CoreWorkerStats().Recycles)
	}
	rc := g.Snapshot().Reclaim
	if rc.Requests[budget.PressureHeavyAdmission] != 1 || rc.Released != 1 || rc.Waits != 1 || rc.AdmittedAfterReclaim != 1 {
		t.Fatalf("reclaim stats: %+v", rc)
	}
	mustEcho(t, "fresh")
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("the next call must start a fresh worker: %d starts", n)
	}
}

// A busy worker is not killed and no second worker starts beside it: it is marked,
// keeps serving, and retires when its last call ends. The heavy work is refused for now
// (it does not wait for calls that may run for minutes) and admitted once the memory
// is back.
func TestHeavyAdmissionRetiresABusyWorkerWhenItsCallsEnd(t *testing.T) {
	logPath := useFakeWorker(t, "ok")
	tree := newWorkerTree(20<<20, 60<<20)
	g := quietGovernor(t, budget.GovernorConfig{SoftCeilingBytes: 100 << 20, Sampler: tree.sample})
	mustEcho(t, "warm")
	pid := CoreWorkerStats().PID
	tree.track(pid)
	done := make(chan error, 1)
	go func() {
		out, err := runCoreCtx(context.Background(), "sleep", "500")
		if err == nil && string(out) != `"slept"` {
			err = fmt.Errorf("unexpected output %s", out)
		}
		done <- err
	}()
	waitFor(t, "the call to start", 3*time.Second, func() bool { return activeCalls() == 1 })

	start := time.Now()
	if _, err := g.AcquireHeavy(context.Background(), "test_index", 30<<20); !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("with the worker busy: want ErrOverloaded for now, got %v", err)
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("heavy admission waited %s on a busy worker's calls", d)
	}
	if st := CoreWorkerStats(); !st.RecyclePending || st.PID != pid {
		t.Fatalf("the busy worker must be marked, not replaced: %+v", st)
	}
	mustEcho(t, "still-served") // a call while marked runs on the same worker
	if n := len(fakeLog(t, logPath, "serve ")); n != 1 {
		t.Fatalf("a second worker started beside the marked one: %d starts", n)
	}
	if err := <-done; err != nil {
		t.Fatalf("the in-flight call must complete: %v", err)
	}
	waitFor(t, "the marked worker to retire", 3*time.Second, func() bool { return processGone(pid) })
	if n := CoreWorkerStats().Recycles[budget.PressureHeavyAdmission]; n != 1 {
		t.Fatalf("recycles: %v", CoreWorkerStats().Recycles)
	}
	release, err := g.AcquireHeavy(context.Background(), "test_index", 30<<20)
	if err != nil {
		t.Fatalf("with the memory back the work fits: %v", err)
	}
	release()
	mustEcho(t, "fresh")
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("want one fresh worker after the retire: %d starts", n)
	}
}

// A hook or capture path never waits for reclaimed memory: it is refused at once, and
// the worker is still asked, so a retry finds the memory back.
func TestHotPathHeavyAdmissionAsksWithoutWaiting(t *testing.T) {
	useFakeWorker(t, "ok")
	tree := newWorkerTree(20<<20, 60<<20)
	g := quietGovernor(t, budget.GovernorConfig{SoftCeilingBytes: 100 << 20, Sampler: tree.sample})
	mustEcho(t, "warm")
	pid := CoreWorkerStats().PID
	tree.track(pid)
	if _, err := g.AcquireHeavy(budget.WithoutHeavyWait(context.Background()), "identity_refresh", 30<<20); !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("a hot path must be refused rather than wait for reclaimed memory: %v", err)
	}
	waitFor(t, "the asked worker to exit", 3*time.Second, func() bool { return processGone(pid) })
	if rc := g.Snapshot().Reclaim; rc.Waits != 0 || rc.Requests[budget.PressureHeavyAdmission] != 1 {
		t.Fatalf("reclaim stats: %+v", rc)
	}
	release, err := g.AcquireHeavy(budget.WithoutHeavyWait(context.Background()), "identity_refresh", 30<<20)
	if err != nil {
		t.Fatalf("the retry must find the memory back: %v", err)
	}
	release()
}

// A sample over the soft ceiling asks the worker for its memory, at most once per
// pressure interval.
func TestSampleOverTheSoftCeilingRecyclesTheWorker(t *testing.T) {
	useFakeWorker(t, "ok")
	tree := newWorkerTree(20<<20, 90<<20)
	g := quietGovernor(t, budget.GovernorConfig{SoftCeilingBytes: 100 << 20, Sampler: tree.sample, PressureInterval: time.Hour})
	mustEcho(t, "warm")
	first := CoreWorkerStats().PID
	tree.track(first)
	if _, err := g.Sample(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the worker to be recycled", 3*time.Second, func() bool { return processGone(first) })
	if n := CoreWorkerStats().Recycles[budget.PressureOverSoftCeiling]; n != 1 {
		t.Fatalf("recycles: %v", CoreWorkerStats().Recycles)
	}
	mustEcho(t, "again")
	second := CoreWorkerStats().PID
	tree.track(second)
	if _, err := g.Sample(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if processGone(second) || g.Snapshot().Reclaim.Requests[budget.PressureOverSoftCeiling] != 1 {
		t.Fatalf("a second sample inside the interval must not ask again: %+v", g.Snapshot().Reclaim)
	}
}

// Going idle is a sampling point: with the tree over the soft ceiling, the worker is
// recycled after its call without any health poll or heavy work, and only once per
// pressure interval however many calls go idle.
func TestWorkerGoingIdleOverTheSoftCeilingIsRecycled(t *testing.T) {
	logPath := useFakeWorker(t, "ok")
	tree := newWorkerTree(20<<20, 90<<20)
	g := quietGovernor(t, budget.GovernorConfig{SoftCeilingBytes: 100 << 20, Sampler: tree.sample, PressureInterval: time.Hour})
	// the worker's pid is known once it has started; track it from the fake's log
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if starts := fakeLog(t, logPath, "serve "); len(starts) > 0 {
				pid, _ := strconv.Atoi(strings.TrimPrefix(starts[0], "serve "))
				tree.track(pid)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	out, err := runCoreCtx(context.Background(), "sleep", "300") // long enough to be tracked
	if err != nil || string(out) != `"slept"` {
		t.Fatalf("call: %s %v", out, err)
	}
	first, _ := strconv.Atoi(strings.TrimPrefix(fakeLog(t, logPath, "serve ")[0], "serve "))
	waitFor(t, "the idle worker over the soft ceiling to be recycled", 3*time.Second, func() bool { return processGone(first) })
	if n := CoreWorkerStats().Recycles[budget.PressureOverSoftCeiling]; n != 1 {
		t.Fatalf("recycles: %v", CoreWorkerStats().Recycles)
	}
	for i := 0; i < 3; i++ {
		mustEcho(t, "again")
	}
	time.Sleep(300 * time.Millisecond)
	if rc := g.Snapshot().Reclaim; rc.Requests[budget.PressureOverSoftCeiling] != 1 {
		t.Fatalf("the governor must ask at most once per interval: %+v", rc)
	}
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("starts: %d", n)
	}
}

// A worker above the runaway line is recycled as soon as it goes idle; one within it
// stays.
func TestRunawayWorkerIsRecycledWhenIdle(t *testing.T) {
	if !measurable() {
		t.Skip("no per-process memory measurement on " + runtime.GOOS)
	}
	logPath := useFakeWorker(t, "ok")
	quietGovernor(t, budget.GovernorConfig{})
	workerRunawayLine = 1
	mustEcho(t, "a")
	// the worker may be retired before its pid can be read from the supervisor
	first, _ := strconv.Atoi(strings.TrimPrefix(fakeLog(t, logPath, "serve ")[0], "serve "))
	waitFor(t, "the runaway worker to be recycled", 3*time.Second, func() bool { return processGone(first) })
	if n := CoreWorkerStats().Recycles[reasonRunaway]; n != 1 {
		t.Fatalf("recycles: %v", CoreWorkerStats().Recycles)
	}
	workerRunawayLine = 1 << 40
	mustEcho(t, "b")
	second := CoreWorkerStats().PID
	time.Sleep(200 * time.Millisecond)
	if second == first || processGone(second) {
		t.Fatalf("a worker within the runaway line stays: first %d second %d", first, second)
	}
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("starts: %d", n)
	}
}

// Above its reserved peak, the worker still serves a burst of calls (it goes idle
// between them), and is recycled only once it has been idle for its trim period.
func TestWorkerAboveItsPeakIsRecycledAfterItsTrimPeriod(t *testing.T) {
	if !measurable() {
		t.Skip("no per-process memory measurement on " + runtime.GOOS)
	}
	t.Setenv("XMUSTARD_CORE_WORKER_TRIM_MS", "300")
	logPath := useFakeWorker(t, "ok")
	quietGovernor(t, budget.GovernorConfig{}) // level normal
	workerPeakLine = 1
	for i := 0; i < 5; i++ {
		mustEcho(t, "burst")
		time.Sleep(20 * time.Millisecond)
	}
	first := CoreWorkerStats().PID
	if n := len(fakeLog(t, logPath, "serve ")); n != 1 || first == 0 {
		t.Fatalf("a burst of calls must stay on one worker: %d starts", n)
	}
	waitFor(t, "the idle worker above its peak to be recycled", 3*time.Second, func() bool { return processGone(first) })
	if n := CoreWorkerStats().Recycles[reasonIdleOverLine]; n != 1 {
		t.Fatalf("recycles: %v", CoreWorkerStats().Recycles)
	}
	// within its peak, the check repeats and the worker stays warm
	workerPeakLine = 1 << 40
	mustEcho(t, "b")
	second := CoreWorkerStats().PID
	time.Sleep(800 * time.Millisecond) // two idle checks
	if processGone(second) || CoreWorkerStats().Recycles[reasonIdleOverLine] != 1 {
		t.Fatalf("a worker within its peak stays warm: %+v", CoreWorkerStats())
	}
}

// After its trim period idle, the worker is recycled when the level is tight (less room
// under the soft ceiling than the heavy line) and kept warm otherwise.
func TestIdleWorkerIsRecycledOnlyWhenMemoryIsTight(t *testing.T) {
	t.Setenv("XMUSTARD_CORE_WORKER_TRIM_MS", "100")
	useFakeWorker(t, "ok")
	var tight atomic.Bool
	g := quietGovernor(t, budget.GovernorConfig{SoftCeilingBytes: 100 << 20, HeavyLineBytes: 25 << 20, Sampler: func() (budget.TreeSample, error) {
		rss := int64(50 << 20)
		if tight.Load() {
			rss = 80 << 20 // over 100 - 25 MiB, under 100
		}
		return budget.TreeSample{At: time.Now(), Supported: true, Basis: "test", Processes: 1, RSSBytes: rss, SelfRSSBytes: rss}, nil
	}})
	mustEcho(t, "a")
	first := CoreWorkerStats().PID
	time.Sleep(500 * time.Millisecond)
	if processGone(first) || CoreWorkerStats().Recycles[reasonIdleTight] != 0 {
		t.Fatalf("with room for heavy work the idle worker stays warm: %+v", CoreWorkerStats())
	}
	tight.Store(true)
	waitFor(t, "the level to turn tight", 3*time.Second, func() bool { return g.Level() == budget.LevelTight })
	mustEcho(t, "b") // same worker; re-arms its idle check
	if CoreWorkerStats().PID != first {
		t.Fatal("the worker was replaced")
	}
	waitFor(t, "the idle worker to be recycled under tight memory", 3*time.Second, func() bool { return processGone(first) })
	if n := CoreWorkerStats().Recycles[reasonIdleTight]; n != 1 {
		t.Fatalf("recycles: %v", CoreWorkerStats().Recycles)
	}
}

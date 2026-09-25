package budget

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTree is a sampler whose tree memory the test sets.
type fakeTree struct {
	rss, footprint atomic.Int64
	fail           atomic.Bool
	calls          atomic.Int64
}

func (f *fakeTree) sample() (TreeSample, error) {
	f.calls.Add(1)
	if f.fail.Load() {
		return TreeSample{}, errNoRSSSampler
	}
	r, fp := f.rss.Load(), f.footprint.Load()
	return TreeSample{At: time.Now(), Supported: true, Basis: "fake", Processes: 1, RSSBytes: r, FootprintBytes: fp, SelfRSSBytes: r, SelfFootprintBytes: fp}, nil
}

type govFixture struct {
	g    *Governor
	tree *fakeTree
	free *atomic.Int64
}

func newGov(t *testing.T, cfg GovernorConfig) govFixture {
	t.Helper()
	tree := &fakeTree{}
	tree.rss.Store(10 << 20)
	free := &atomic.Int64{}
	cfg.Sampler = tree.sample
	cfg.FreeOSMemory = func() { free.Add(1) }
	if cfg.SoftCeilingBytes == 0 {
		cfg.SoftCeilingBytes = 100 << 20
	}
	return govFixture{g: NewGovernor(cfg), tree: tree, free: free}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// Heavy slot exclusivity: while one holder runs, a second holder is queued (visible with
// its owner label), waits the configured bound, and is then refused with ErrOverloaded.
func TestHeavySlotSecondHolderWaitsThenOverloads(t *testing.T) {
	f := newGov(t, GovernorConfig{HeavyWait: 150 * time.Millisecond})
	release, err := f.g.AcquireHeavy(context.Background(), "index_build", 8<<20)
	if err != nil {
		t.Fatalf("first holder: %v", err)
	}
	defer release()

	type result struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		r, err := f.g.AcquireHeavy(context.Background(), "bulk_import", 1<<20)
		if r != nil {
			r()
		}
		done <- result{err, time.Since(start)}
	}()
	waitFor(t, "second holder queued", func() bool { return f.g.Snapshot().HeavySlot.QueueLen == 1 })
	snap := f.g.Snapshot().HeavySlot
	if !snap.Busy || snap.Owner != "index_build" || snap.DeclaredBytes != 8<<20 || len(snap.Queue) != 1 || snap.Queue[0].Owner != "bulk_import" {
		t.Fatalf("snapshot while contended: %+v", snap)
	}
	res := <-done
	if !errors.Is(res.err, ErrOverloaded) || !strings.Contains(res.err.Error(), "index_build") {
		t.Fatalf("second holder: want ErrOverloaded naming the holder, got %v", res.err)
	}
	if res.elapsed < 150*time.Millisecond {
		t.Fatalf("second holder refused after %s, before the 150ms wait bound", res.elapsed)
	}
	snap = f.g.Snapshot().HeavySlot
	if snap.QueueLen != 0 || snap.RefusedBusy != 1 || snap.Acquired != 1 || !snap.Busy {
		t.Fatalf("after the refusal: %+v", snap)
	}
}

// A queued holder gets the slot as soon as the current holder releases within the bound.
func TestHeavySlotWaiterGetsSlotOnRelease(t *testing.T) {
	f := newGov(t, GovernorConfig{HeavyWait: 5 * time.Second})
	release, err := f.g.AcquireHeavy(context.Background(), "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		r, err := f.g.AcquireHeavy(context.Background(), "b", 0)
		if err == nil {
			if f.g.Snapshot().HeavySlot.Owner != "b" {
				err = errors.New("slot owner is not the new holder")
			}
			r()
		}
		got <- err
	}()
	waitFor(t, "waiter queued", func() bool { return f.g.Snapshot().HeavySlot.QueueLen == 1 })
	release()
	if err := <-got; err != nil {
		t.Fatalf("waiter after release: %v", err)
	}
	if s := f.g.Snapshot().HeavySlot; s.Busy || s.Acquired != 2 || s.Released != 2 {
		t.Fatalf("after both holders: %+v", s)
	}
}

// A waiter whose context ends leaves the queue with the context's error.
func TestHeavySlotWaitEndsWithContext(t *testing.T) {
	f := newGov(t, GovernorConfig{HeavyWait: 5 * time.Second})
	release, _ := f.g.AcquireHeavy(context.Background(), "a", 0)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		_, err := f.g.AcquireHeavy(ctx, "b", 0)
		got <- err
	}()
	waitFor(t, "waiter queued", func() bool { return f.g.Snapshot().HeavySlot.QueueLen == 1 })
	cancel()
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if s := f.g.Snapshot().HeavySlot; s.QueueLen != 0 || s.Cancelled != 1 {
		t.Fatalf("cancelled waiter must leave the queue: %+v", s)
	}
}

// Hook and capture paths never wait for the heavy slot: with the slot held and a long
// wait bound, a WithoutHeavyWait context is refused at once and never queued.
func TestHotPathNeverWaitsForHeavySlot(t *testing.T) {
	f := newGov(t, GovernorConfig{HeavyWait: 10 * time.Second})
	release, _ := f.g.AcquireHeavy(context.Background(), "index_writer", 0)
	defer release()
	start := time.Now()
	_, err := f.g.AcquireHeavy(WithoutHeavyWait(context.Background()), "capture", 0)
	if !errors.Is(err, ErrOverloaded) {
		t.Fatalf("hot path with the slot busy: want ErrOverloaded, got %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("hot path waited %s for the heavy slot", d)
	}
	if s := f.g.Snapshot().HeavySlot; s.QueueLen != 0 || s.RefusedHotPath != 1 || s.RefusedBusy != 0 {
		t.Fatalf("hot path must not queue: %+v", s)
	}
}

// The watchdog refuses new heavy work when the simulated tree memory plus the declared
// bytes would pass the soft ceiling, using the larger of ps-RSS and footprint. A refusal
// frees the slot.
func TestWatchdogRefusesHeavyWorkNearSoftCeiling(t *testing.T) {
	f := newGov(t, GovernorConfig{SoftCeilingBytes: 100 << 20})
	f.tree.rss.Store(50 << 20)
	r, err := f.g.AcquireHeavy(context.Background(), "index_build", 40<<20)
	if err != nil {
		t.Fatalf("50 + 40 MiB under a 100 MiB ceiling must be admitted: %v", err)
	}
	r()

	f.tree.rss.Store(70 << 20)
	if _, err := f.g.AcquireHeavy(context.Background(), "index_build", 40<<20); !errors.Is(err, ErrOverloaded) || !strings.Contains(err.Error(), "soft ceiling") {
		t.Fatalf("70 + 40 MiB over a 100 MiB ceiling: want ErrOverloaded (soft ceiling), got %v", err)
	}
	f.tree.rss.Store(30 << 20)
	f.tree.footprint.Store(80 << 20)
	if _, err := f.g.AcquireHeavy(context.Background(), "index_build", 40<<20); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("footprint 80 + 40 MiB must be refused even though RSS is 30: %v", err)
	}
	f.tree.footprint.Store(0)
	f.tree.rss.Store(101 << 20)
	if _, err := f.g.AcquireHeavy(context.Background(), "tiny", 0); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("tree already over the ceiling must refuse even zero declared bytes: %v", err)
	}
	f.tree.rss.Store(10 << 20)
	if _, err := f.g.AcquireHeavy(context.Background(), "huge", 1<<62); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("an overflowing declaration must be refused: %v", err)
	}
	r, err = f.g.AcquireHeavy(context.Background(), "index_build", 1<<20)
	if err != nil {
		t.Fatalf("refusals must free the slot: %v", err)
	}
	r()
	s := f.g.Snapshot()
	if s.HeavySlot.RefusedRSS != 4 || s.HeavySlot.Acquired != 2 {
		t.Fatalf("stats: %+v", s.HeavySlot)
	}
	if s.Watchdog.PeakRSSBytes != 101<<20 || s.Watchdog.PeakFootprintBytes != 80<<20 || s.Watchdog.OverSoftCeilingSamples != 1 {
		t.Fatalf("watchdog peaks: %+v", s.Watchdog)
	}
}

// Without a tree sampler the admission projects from the steady reservations.
func TestWatchdogWithoutSamplerProjectsFromReservations(t *testing.T) {
	f := newGov(t, GovernorConfig{SoftCeilingBytes: 100 << 20})
	f.tree.fail.Store(true)
	f.g.Reserve(Component{Name: "daemon", SteadyBytes: 40 << 20, PeakBytes: 50 << 20})
	f.g.Reserve(Component{Name: "index", SteadyBytes: 20 << 20, PeakBytes: 20 << 20})
	r, err := f.g.AcquireHeavy(context.Background(), "a", 30<<20)
	if err != nil {
		t.Fatalf("60 reserved + 30 declared under 100: %v", err)
	}
	r()
	if _, err := f.g.AcquireHeavy(context.Background(), "a", 50<<20); !errors.Is(err, ErrOverloaded) || !strings.Contains(err.Error(), "reserved steady") {
		t.Fatalf("60 reserved + 50 declared over 100: want refusal from reservations, got %v", err)
	}
	if last := f.g.Snapshot().Watchdog.Last; last.Supported || last.Error == "" {
		t.Fatalf("an unsupported sampler must show in the snapshot: %+v", last)
	}
}

// Releasing heavy work runs the trim hooks and FreeOSMemory exactly once, even when a
// hook panics, and frees the slot.
func TestHeavyReleaseTrimsAndFreesOSMemoryOnce(t *testing.T) {
	f := newGov(t, GovernorConfig{})
	var trims atomic.Int64
	f.g.OnHeavyRelease(func() { trims.Add(1) })
	f.g.OnHeavyRelease(func() { panic("trim failed") })
	r, err := f.g.AcquireHeavy(context.Background(), "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.free.Load() != 0 || trims.Load() != 0 {
		t.Fatal("nothing is trimmed before release")
	}
	r()
	r()
	if f.free.Load() != 1 || trims.Load() != 1 {
		t.Fatalf("release must trim and free once: free=%d trims=%d", f.free.Load(), trims.Load())
	}
	if s := f.g.Snapshot().HeavySlot; s.Busy || s.Owner != "" || s.FreeOSMemoryCalls != 1 || s.Released != 1 {
		t.Fatalf("after release: %+v", s)
	}
	r2, err := f.g.AcquireHeavy(context.Background(), "b", 0)
	if err != nil {
		t.Fatalf("slot must be free after release: %v", err)
	}
	r2()
}

// The watchdog samples on its interval while heavy work is active and stops after.
func TestWatchdogSamplesWhileHeavyWorkIsActive(t *testing.T) {
	f := newGov(t, GovernorConfig{WatchInterval: 5 * time.Millisecond})
	r, err := f.g.AcquireHeavy(context.Background(), "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	before := f.tree.calls.Load()
	waitFor(t, "periodic samples", func() bool { return f.tree.calls.Load() >= before+3 })
	if !f.g.Snapshot().Watchdog.SamplingActive {
		t.Fatal("sampling must be active while heavy work runs")
	}
	r()
	if f.g.Snapshot().Watchdog.SamplingActive {
		t.Fatal("sampling must stop with the heavy work")
	}
	after := f.tree.calls.Load()
	time.Sleep(30 * time.Millisecond)
	if n := f.tree.calls.Load(); n != after {
		t.Fatalf("sampler still running after release: %d -> %d calls", after, n)
	}
}

// Owner labels are shown on the public health endpoint, so they are kept short and inert.
func TestHeavyOwnerLabelIsSanitized(t *testing.T) {
	cases := map[string]string{
		"index_build":            "index_build",
		"":                       "unlabeled",
		"index build\n<script>":  "index_build__script_",
		"ws:abc/index-update.v2": "ws:abc/index-update.v2",
		strings.Repeat("x", 100): strings.Repeat("x", 64),
		"café":                   "caf__",
	}
	for in, want := range cases {
		if got := heavyLabel(in); got != want {
			t.Errorf("heavyLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// Reservations are totalled per component; unknown usage is reported as null.
func TestSnapshotReportsReservations(t *testing.T) {
	f := newGov(t, GovernorConfig{})
	f.g.Reserve(Component{Name: "svc", Kind: ComponentResident, SteadyBytes: 3 << 20, PeakBytes: 4 << 20,
		UsedBasis: "ps_rss", Used: func() (int64, bool) { return 2 << 20, true }})
	f.g.Reserve(Component{Name: "cold", SteadyBytes: 1 << 20, PeakBytes: 1 << 20, Used: func() (int64, bool) { return 0, false }})
	f.g.Reserve(Component{Name: "svc", Kind: ComponentResident, SteadyBytes: 5 << 20, PeakBytes: 6 << 20})
	f.g.Reserve(Component{Name: " "})
	s := f.g.Snapshot()
	if len(s.Reservations.Components) != 2 || s.Reservations.SteadyTotalBytes != 6<<20 || s.Reservations.PeakTotalBytes != 7<<20 {
		t.Fatalf("reservations: %+v", s.Reservations)
	}
	if c := s.Reservations.Components[0]; c.Name != "svc" || c.ReservedSteadyBytes != 5<<20 || c.UsedBytes != nil {
		t.Fatalf("a re-registered component replaces the old one: %+v", c)
	}
	if c := s.Reservations.Components[1]; c.Kind != ComponentResident || c.UsedBytes != nil {
		t.Fatalf("unknown usage must be null: %+v", c)
	}
	if s.GateBytes != 100_000_000 || s.SoftCeilingBytes != 100<<20 || s.Version != 1 {
		t.Fatalf("header: %+v", s)
	}
}

// The process-wide governor declares the components every API process has.
func TestDefaultGovernorDeclaresBaseComponents(t *testing.T) {
	s := Gov.Snapshot()
	byName := map[string]ComponentStatus{}
	for _, c := range s.Reservations.Components {
		byName[c.Name] = c
	}
	for _, name := range []string{"go_daemon", "transient_pool", "heavy_slot", "helper_children"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing default component %q in %+v", name, s.Reservations.Components)
		}
	}
	if c := byName["go_daemon"]; c.Kind != ComponentResident || c.ReservedSteadyBytes != daemonSteadyBytes || c.ReservedPeakBytes != daemonPeakBytes {
		t.Fatalf("go_daemon line: %+v", c)
	}
	if c := byName["heavy_slot"]; c.ReservedPeakBytes != DefaultHeavyLineBytes || c.UsedBytes == nil || *c.UsedBytes != 0 {
		t.Fatalf("idle heavy slot: %+v", c)
	}
	if s.HeavySlot.Capacity != 1 || s.HeavySlot.WaitBoundMS != DefaultHeavyWait.Milliseconds() {
		t.Fatalf("heavy slot defaults: %+v", s.HeavySlot)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if c := byName["go_daemon"]; c.UsedBytes == nil || *c.UsedBytes <= 0 {
			t.Fatalf("go_daemon usage must be measured on %s: %+v (last sample %+v)", runtime.GOOS, c, s.Watchdog.Last)
		}
	}
}

func TestProcfsParsers(t *testing.T) {
	smaps := []byte("55d0c0a00000-7ffd1a3f1000 ---p 00000000 00:00 0                          [rollup]\n" +
		"Rss:               12345 kB\nPss:                6789 kB\nPss_Anon:           4000 kB\nShared_Clean:       100 kB\n")
	rss, pss, ok := parseSmapsRollup(smaps)
	if !ok || rss != 12345<<10 || pss != 6789<<10 {
		t.Fatalf("smaps_rollup: rss=%d pss=%d ok=%v", rss, pss, ok)
	}
	if _, _, ok := parseSmapsRollup([]byte("Pss: 1 kB\n")); ok {
		t.Fatal("no Rss line must not parse")
	}
	if pages, ok := parseStatmResident([]byte("1000 250 30 5 0 90 0\n")); !ok || pages != 250 {
		t.Fatalf("statm: %d %v", pages, ok)
	}
	if ppid, ok := parseStatPPID([]byte("4242 (weird) name)) S 17 4242 4242 0 -1")); !ok || ppid != 17 {
		t.Fatalf("stat ppid: %d %v", ppid, ok)
	}
	if _, ok := parseStatPPID([]byte("garbage")); ok {
		t.Fatal("garbage stat must not parse")
	}
	if got := parsePIDList([]byte("12 34 x 0 56\n")); len(got) != 3 || got[0] != 12 || got[2] != 56 {
		t.Fatalf("children list: %v", got)
	}
}

// The tree walk is breadth-first, skips vanished processes, tolerates cycles, reports
// external subtrees on their own line and stops at maxTreeProcesses.
func TestWalkTreeIsBounded(t *testing.T) {
	kids := map[int][]int{1: {2, 3, 6}, 2: {4, 1}, 3: {5}, 6: {7}, 7: {8}}
	names := map[int]string{1: "xmustard-api", 2: "xmustard-core", 3: "git", 4: "git", 6: "gopls", 7: "xmustard-core", 8: "sh"}
	s := walkTree(1, func(p int) []int { return kids[p] }, func(p int) (procMem, bool) {
		if p == 5 {
			return procMem{}, false
		}
		return procMem{rss: int64(p) << 20, footprint: int64(p) << 10, name: names[p]}, true
	})
	if s.Processes != 4 || s.RSSBytes != (1+2+3+4)<<20 || s.SelfRSSBytes != 1<<20 || s.ChildrenRSSBytes() != 9<<20 || s.Truncated {
		t.Fatalf("owned walk: %+v", s)
	}
	// gopls is external, and so is everything under it, whatever its name
	if s.ExternalProcesses != 3 || s.ExternalRSSBytes != (6+7+8)<<20 {
		t.Fatalf("external line: %+v", s)
	}
	chain := walkTree(1, func(p int) []int { return []int{p + 1} }, func(int) (procMem, bool) { return procMem{rss: 1, footprint: 1, name: "xmustard-core"}, true })
	if chain.Processes != maxTreeProcesses || !chain.Truncated {
		t.Fatalf("unbounded chain must be truncated at %d: %+v", maxTreeProcesses, chain)
	}
}

func TestOwnedProcessNames(t *testing.T) {
	for name, want := range map[string]bool{
		"xmustard-core": true, "xmustard-api": true, "git": true, "git-remote-http": true, "ast-grep": true, "sg": true,
		"sh": false, "gopls": false, "node": false, "claude": false, "opencode": false, "": false, "gitk2": false,
	} {
		if got := ownedProcessName(name); got != want {
			t.Errorf("ownedProcessName(%q) = %v, want %v", name, got, want)
		}
	}
}

// The platform sampler sees this process, counts a child named like the Rust core in
// the owned tree, and puts any other child on the external line.
func TestOwnTreeSamplerSeesChildren(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no tree sampler on " + runtime.GOOS)
	}
	self, err := sampleOwnTree()
	if err != nil || !self.Supported || self.SelfRSSBytes <= 0 || self.RSSBytes < self.SelfRSSBytes {
		t.Fatalf("own sample: %+v err=%v", self, err)
	}
	if runtime.GOOS == "darwin" && self.SelfFootprintBytes <= 0 {
		t.Fatalf("darwin must report phys_footprint: %+v", self)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	raw, err := os.ReadFile(sleep)
	if err != nil {
		t.Skipf("cannot copy sleep: %v", err)
	}
	owned := filepath.Join(t.TempDir(), "xmustard-core")
	if err := os.WriteFile(owned, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	start := func(bin string) *exec.Cmd {
		cmd := exec.Command(bin, "5")
		if err := cmd.Start(); err != nil {
			t.Skipf("cannot start %s: %v", bin, err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	start(owned)
	start(sleep)
	var s TreeSample
	waitFor(t, "owned and external children in the sample", func() bool {
		s, err = sampleOwnTree()
		return err == nil && s.Processes >= self.Processes+1 && s.ChildrenRSSBytes() > 0 && s.ExternalProcesses >= 1 && s.ExternalRSSBytes > 0
	})
}

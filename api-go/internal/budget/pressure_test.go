package budget

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// reclaimable is a component whose Reclaim hook the test scripts.
type reclaimable struct {
	calls   atomic.Int64
	reasons chan string
	onAsk   func(Pressure) <-chan struct{}
}

func (r *reclaimable) component(name string, steady int64) Component {
	return Component{Name: name, Kind: ComponentResident, SteadyBytes: steady, PeakBytes: steady,
		Reclaim: func(p Pressure) <-chan struct{} {
			r.calls.Add(1)
			if r.reasons != nil {
				r.reasons <- p.Reason
			}
			if r.onAsk == nil {
				return nil
			}
			return r.onAsk(p)
		}}
}

// Heavy work refused for memory asks the reclaimable components, waits for what they
// release, measures again and is admitted when the memory came back.
func TestHeavyAdmissionReclaimsThenAdmits(t *testing.T) {
	f := newGov(t, GovernorConfig{SoftCeilingBytes: 100 << 20})
	f.tree.rss.Store(80 << 20)
	var got Pressure
	r := &reclaimable{onAsk: func(p Pressure) <-chan struct{} {
		got = p
		done := make(chan struct{})
		go func() {
			time.Sleep(50 * time.Millisecond)
			f.tree.rss.Store(30 << 20) // the component's process exited
			close(done)
		}()
		return done
	}}
	f.g.Reserve(r.component("svc", 20<<20))
	release, err := f.g.AcquireHeavy(context.Background(), "index_build", 40<<20)
	if err != nil {
		t.Fatalf("80 + 40 over 100, then 30 + 40 after the reclaim: %v", err)
	}
	release()
	if got.Reason != PressureHeavyAdmission || got.TreeBytes != 80<<20 || got.DeclaredBytes != 40<<20 || got.SoftCeilingBytes != 100<<20 {
		t.Fatalf("pressure sent: %+v", got)
	}
	s := f.g.Snapshot()
	if s.Reclaim.Requests[PressureHeavyAdmission] != 1 || s.Reclaim.Released != 1 || s.Reclaim.Waits != 1 || s.Reclaim.AdmittedAfterReclaim != 1 {
		t.Fatalf("reclaim stats: %+v", s.Reclaim)
	}
	if s.HeavySlot.RefusedRSS != 0 || s.HeavySlot.Acquired != 1 {
		t.Fatalf("heavy stats: %+v", s.HeavySlot)
	}
}

// A component that releases nothing now leaves the refusal immediate; one that never
// finishes releasing is waited for at most ReclaimWait; a caller that gives up while
// waiting gets its context error.
func TestHeavyAdmissionReclaimIsBounded(t *testing.T) {
	f := newGov(t, GovernorConfig{SoftCeilingBytes: 100 << 20, ReclaimWait: 150 * time.Millisecond})
	f.tree.rss.Store(80 << 20)
	r := &reclaimable{}
	f.g.Reserve(r.component("svc", 20<<20))

	start := time.Now()
	if _, err := f.g.AcquireHeavy(context.Background(), "a", 40<<20); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("nothing released: want ErrOverloaded, got %v", err)
	}
	if d := time.Since(start); d > 100*time.Millisecond || f.g.Snapshot().Reclaim.Waits != 0 {
		t.Fatalf("nothing released, so nothing to wait for: %s, %+v", d, f.g.Snapshot().Reclaim)
	}

	never := make(chan struct{})
	r.onAsk = func(Pressure) <-chan struct{} { return never }
	start = time.Now()
	if _, err := f.g.AcquireHeavy(context.Background(), "a", 40<<20); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("release never finished: want ErrOverloaded, got %v", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 2*time.Second {
		t.Fatalf("the reclaim wait must be bounded by ReclaimWait (150ms): %s", d)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := f.g.AcquireHeavy(ctx, "a", 40<<20); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a caller that gives up while waiting: want its context error, got %v", err)
	}
	s := f.g.Snapshot()
	if s.HeavySlot.RefusedRSS != 2 || s.HeavySlot.Cancelled != 1 || s.HeavySlot.Busy || s.Reclaim.Waits != 2 || r.calls.Load() != 3 {
		t.Fatalf("stats: heavy %+v reclaim %+v calls %d", s.HeavySlot, s.Reclaim, r.calls.Load())
	}
	// every refusal freed the slot
	f.tree.rss.Store(10 << 20)
	release, err := f.g.AcquireHeavy(context.Background(), "a", 40<<20)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// A hook or capture path asks for memory but never waits for it.
func TestHotPathAsksForMemoryWithoutWaiting(t *testing.T) {
	f := newGov(t, GovernorConfig{SoftCeilingBytes: 100 << 20})
	f.tree.rss.Store(80 << 20)
	asked := make(chan struct{})
	r := &reclaimable{onAsk: func(Pressure) <-chan struct{} {
		close(asked)
		return make(chan struct{}) // never finishes
	}}
	f.g.Reserve(r.component("svc", 0))
	start := time.Now()
	if _, err := f.g.AcquireHeavy(WithoutHeavyWait(context.Background()), "identity", 40<<20); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("want ErrOverloaded, got %v", err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("a hot path waited %s for reclaimed memory", d)
	}
	select {
	case <-asked:
	case <-time.After(3 * time.Second):
		t.Fatal("the hot path's refusal must still ask for memory")
	}
	if rc := f.g.Snapshot().Reclaim; rc.Waits != 0 {
		t.Fatalf("reclaim stats: %+v", rc)
	}
}

// A sample over the soft ceiling asks for memory, at most once per interval; a sample
// under it does not. Admission's own samples leave it to the admission.
func TestSampleOverSoftCeilingAsksOncePerInterval(t *testing.T) {
	f := newGov(t, GovernorConfig{SoftCeilingBytes: 100 << 20, PressureInterval: 200 * time.Millisecond})
	r := &reclaimable{reasons: make(chan string, 8)}
	f.g.Reserve(r.component("svc", 0))
	f.tree.rss.Store(90 << 20)
	_, _ = f.g.Sample()
	f.tree.rss.Store(110 << 20)
	_, _ = f.g.Sample()
	_, _ = f.g.Sample()
	select {
	case reason := <-r.reasons:
		if reason != PressureOverSoftCeiling {
			t.Fatalf("reason %q", reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a sample over the soft ceiling must ask for memory")
	}
	time.Sleep(50 * time.Millisecond)
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("asked %d times inside one interval", n)
	}
	time.Sleep(200 * time.Millisecond)
	_, _ = f.g.Sample()
	waitFor(t, "a second request after the interval", func() bool { return r.calls.Load() == 2 })
	if got := f.g.Snapshot().Reclaim.Requests[PressureOverSoftCeiling]; got != 2 {
		t.Fatalf("requests: %d", got)
	}
}

// Level classifies the measured tree plus shims against the soft ceiling and the heavy
// line.
func TestPressureLevels(t *testing.T) {
	f := newGov(t, GovernorConfig{SoftCeilingBytes: 100 << 20, HeavyLineBytes: 25 << 20})
	for _, tc := range []struct {
		rss, footprint int64
		want           string
	}{
		{50 << 20, 0, LevelNormal},
		{75 << 20, 0, LevelNormal},
		{76 << 20, 0, LevelTight},
		{40 << 20, 90 << 20, LevelTight}, // footprint counts too
		{101 << 20, 0, LevelOver},
	} {
		f.tree.rss.Store(tc.rss)
		f.tree.footprint.Store(tc.footprint)
		time.Sleep(healthSampleMaxAge) // past the cached sample
		if got := f.g.Level(); got != tc.want {
			t.Fatalf("rss %d footprint %d: level %s, want %s", tc.rss>>20, tc.footprint>>20, got, tc.want)
		}
		if got := f.g.Snapshot().Reclaim.Level; got != tc.want {
			t.Fatalf("snapshot level %s, want %s", got, tc.want)
		}
	}
	f.tree.fail.Store(true)
	time.Sleep(healthSampleMaxAge)
	if got := f.g.Level(); got != LevelUnknown {
		t.Fatalf("no sampler: level %s", got)
	}
	if tight := f.g.Snapshot().Reclaim.TightAboveBytes; tight != 75<<20 {
		t.Fatalf("tight above %d", tight)
	}
}

// A disabled component is listed, reserves nothing and is never asked; a reclaimable
// one does not lower the permanent heavy bound; a descendant is left out of
// helper_children; a panicking hook is skipped.
func TestComponentFlags(t *testing.T) {
	f := newGov(t, GovernorConfig{SoftCeilingBytes: 100 << 20})
	f.g.registerDefaultComponents()
	var on atomic.Bool
	off := &reclaimable{}
	svc := off.component("svc", 30<<20)
	svc.Enabled = on.Load
	svc.Descendant = true
	svc.Used = func() (int64, bool) { return 3 << 20, true }
	f.g.Reserve(svc)
	panicky := Component{Name: "panicky", Reclaim: func(Pressure) <-chan struct{} { panic("boom") }}
	f.g.Reserve(panicky)
	f.g.cfg.Sampler = func() (TreeSample, error) { // self 10 MiB, owned children 8 MiB
		return TreeSample{At: time.Now(), Supported: true, Processes: 3, RSSBytes: 18 << 20, SelfRSSBytes: 10 << 20}, nil
	}

	s := f.g.Snapshot()
	byName := map[string]ComponentStatus{}
	for _, c := range s.Reservations.Components {
		byName[c.Name] = c
	}
	if c := byName["svc"]; c.Enabled || !c.Reclaimable || c.ReservedSteadyBytes != 30<<20 {
		t.Fatalf("disabled svc: %+v", c)
	}
	base := s.Reservations.SteadyTotalBytes
	if base != daemonSteadyBytes {
		t.Fatalf("a disabled component must not reserve: steady total %d", base)
	}
	if c := byName["helper_children"]; c.UsedBytes == nil || *c.UsedBytes != 8<<20 {
		t.Fatalf("a disabled descendant is not subtracted: %+v", c)
	}
	on.Store(true)
	s = f.g.Snapshot()
	for _, c := range s.Reservations.Components {
		byName[c.Name] = c
	}
	if !byName["svc"].Enabled || s.Reservations.SteadyTotalBytes != base+30<<20 {
		t.Fatalf("enabled svc: %+v total %d", byName["svc"], s.Reservations.SteadyTotalBytes)
	}
	if c := byName["helper_children"]; c.UsedBytes == nil || *c.UsedBytes != 5<<20 {
		t.Fatalf("helper_children must leave the descendant's 3 MiB out of 8: %+v", c)
	}
	if s.HeavySlot.MaxAdmissible != 100<<20-daemonSteadyBytes {
		t.Fatalf("a reclaimable component must not lower the permanent bound: %d", s.HeavySlot.MaxAdmissible)
	}
	if got := f.g.askReclaim(Pressure{Reason: PressureOverSoftCeiling}); len(got) != 0 || off.calls.Load() != 1 {
		t.Fatalf("both hooks asked, the panic skipped: %d released, svc asked %d", len(got), off.calls.Load())
	}
	on.Store(false)
	f.g.askReclaim(Pressure{Reason: PressureOverSoftCeiling})
	if off.calls.Load() != 1 {
		t.Fatal("a disabled component must never be asked")
	}
}

// RegisterProcessComponent declares the component on the process-wide governor and on
// every governor NewProcessGovernor builds afterwards.
func TestRegisterProcessComponent(t *testing.T) {
	processComponents.Lock()
	saved := append([]Component(nil), processComponents.list...)
	processComponents.Unlock()
	t.Cleanup(func() {
		processComponents.Lock()
		processComponents.list = saved
		processComponents.Unlock()
	})
	prev := Gov
	Gov = NewProcessGovernor(GovernorConfig{})
	t.Cleanup(func() { Gov = prev })
	RegisterProcessComponent(Component{Name: "test_service", SteadyBytes: 1 << 20})
	for _, g := range []*Governor{Gov, NewProcessGovernor(GovernorConfig{})} {
		found := false
		for _, c := range g.Snapshot().Reservations.Components {
			found = found || c.Name == "test_service"
		}
		if !found {
			t.Fatal("a process component must be on the process governor and on new ones")
		}
	}
}

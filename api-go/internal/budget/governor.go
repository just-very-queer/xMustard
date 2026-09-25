package budget

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The governor (PAR-RT-04) adds three things to byte and child admission:
//
//   - static component reservations: each resident component declares its steady and
//     peak allocation, so health can show reserved against used per component;
//   - one heavy slot: index builds and updates, bulk imports, the index writer, the
//     helper model and the ONNX sidecar run one at a time. A caller waits for the slot up
//     to a bound, then gets ErrOverloaded (HTTP 503 + Retry-After, MCP -32000). Hook and
//     capture paths never wait for it (WithoutHeavyWait); captures stream to the spool
//     in O(window) memory and do not use it at all;
//   - an RSS watchdog over this process and its descendants that refuses new heavy work
//     when measured memory plus the work's declared bytes would pass a soft ceiling. It
//     samples every WatchInterval while heavy work runs, and on demand otherwise.
//
// Releasing the heavy slot runs the registered cache-trim hooks and debug.FreeOSMemory
// before the next holder is admitted, so its admission sees the returned memory.

// GateBytes is the budget gate for the xMustard-owned process tree: 1e8 bytes, 95.4 MiB.
const GateBytes int64 = 100_000_000

const (
	// DefaultSoftCeilingBytes leaves about 5 MiB of margin under the gate.
	DefaultSoftCeilingBytes int64 = 90 << 20
	// DefaultHeavyLineBytes is the heavy slot's peak line in the default budget.
	DefaultHeavyLineBytes int64 = 25 << 20
	// DefaultHeavyWait bounds how long a caller waits for a busy heavy slot.
	DefaultHeavyWait = 10 * time.Second
	// DefaultWatchInterval is the sampling period while heavy work is active.
	DefaultWatchInterval = 250 * time.Millisecond
	// daemonSteadyBytes / daemonPeakBytes are the Go daemon's line in the default budget.
	daemonSteadyBytes int64 = 22 << 20
	daemonPeakBytes   int64 = 28 << 20
	// healthSampleMaxAge lets frequent health polls reuse a recent sample.
	healthSampleMaxAge = 250 * time.Millisecond
	// maxQueueShown bounds the waiters listed in a snapshot (queue_len is exact).
	maxQueueShown = 16
)

// Component kinds.
const (
	ComponentResident  = "resident"
	ComponentTransient = "transient"
)

// Component is one static reservation. Used, when set, reports live usage on the basis
// named by UsedBasis; ok=false means unknown right now.
type Component struct {
	Name        string
	Kind        string
	SteadyBytes int64
	PeakBytes   int64
	Used        func() (int64, bool)
	UsedBasis   string
}

// GovernorConfig configures a Governor. Zero fields take the defaults.
type GovernorConfig struct {
	SoftCeilingBytes int64
	HeavyLineBytes   int64
	HeavyWait        time.Duration
	WatchInterval    time.Duration
	// Sampler measures this process tree; FreeOSMemory returns freed memory to the OS.
	Sampler      func() (TreeSample, error)
	FreeOSMemory func()
}

type heavyHolder struct {
	owner    string
	since    time.Time
	declared int64
}

type heavyWaiter struct {
	owner string
	since time.Time
}

type heavyStats struct {
	acquired, released, refusedBusy, refusedRSS, refusedHotPath, cancelled, freeOSMemory int64
}

// Governor owns the reservations, the heavy slot and the watchdog.
type Governor struct {
	cfg GovernorConfig

	mu         sync.Mutex
	components []Component
	holder     *heavyHolder
	queue      []*heavyWaiter
	stats      heavyStats
	onRelease  []func()
	slot       chan struct{}

	wmu       sync.Mutex
	last      TreeSample
	hasLast   bool
	peakRSS   int64
	peakFP    int64
	samples   int64
	overSoft  int64
	watching  bool
	warnedFor time.Time
}

// NewGovernor builds a governor with no components registered.
func NewGovernor(cfg GovernorConfig) *Governor {
	if cfg.SoftCeilingBytes <= 0 {
		cfg.SoftCeilingBytes = DefaultSoftCeilingBytes
	}
	if cfg.HeavyLineBytes <= 0 {
		cfg.HeavyLineBytes = DefaultHeavyLineBytes
	}
	if cfg.HeavyWait <= 0 {
		cfg.HeavyWait = DefaultHeavyWait
	}
	if cfg.WatchInterval <= 0 {
		cfg.WatchInterval = DefaultWatchInterval
	}
	if cfg.Sampler == nil {
		cfg.Sampler = sampleOwnTree
	}
	if cfg.FreeOSMemory == nil {
		cfg.FreeOSMemory = debug.FreeOSMemory
	}
	return &Governor{cfg: cfg, slot: make(chan struct{}, 1)}
}

// Gov is the process-wide governor. XMUSTARD_RSS_SOFT_CEILING_BYTES and
// XMUSTARD_HEAVY_WAIT_MS override the soft ceiling and the heavy-slot wait bound.
var Gov = NewProcessGovernor(GovernorConfig{
	SoftCeilingBytes: envInt64("XMUSTARD_RSS_SOFT_CEILING_BYTES", DefaultSoftCeilingBytes),
	HeavyWait:        time.Duration(envInt("XMUSTARD_HEAVY_WAIT_MS", int(DefaultHeavyWait/time.Millisecond))) * time.Millisecond,
})

// NewProcessGovernor builds a governor with the components every API process declares.
func NewProcessGovernor(cfg GovernorConfig) *Governor {
	g := NewGovernor(cfg)
	g.registerDefaultComponents()
	return g
}

// registerDefaultComponents declares what exists in every API process today. Later
// resident components (the Rust index service, the governance store) call Reserve.
func (g *Governor) registerDefaultComponents() {
	g.Reserve(Component{Name: "go_daemon", Kind: ComponentResident, SteadyBytes: daemonSteadyBytes, PeakBytes: daemonPeakBytes,
		UsedBasis: "ps_rss", Used: func() (int64, bool) {
			s, err := g.cachedSample(healthSampleMaxAge)
			return s.SelfRSSBytes, err == nil && s.Supported
		}})
	g.Reserve(Component{Name: "transient_pool", Kind: ComponentTransient, PeakBytes: TransientBytes.Max(),
		UsedBasis: "admitted_bytes", Used: func() (int64, bool) { return TransientBytes.InUse(), true }})
	g.Reserve(Component{Name: "heavy_slot", Kind: ComponentTransient, PeakBytes: g.cfg.HeavyLineBytes,
		UsedBasis: "declared_bytes", Used: func() (int64, bool) {
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.holder == nil {
				return 0, true
			}
			return g.holder.declared, true
		}})
	// Per-call Rust cores and git children are unreserved until the resident service
	// replaces them; their measured memory is still shown.
	g.Reserve(Component{Name: "helper_children", Kind: ComponentTransient,
		UsedBasis: "ps_rss", Used: func() (int64, bool) {
			s, err := g.cachedSample(healthSampleMaxAge)
			return s.ChildrenRSSBytes(), err == nil && s.Supported
		}})
}

// Reserve declares (or replaces, by name) a component's static reservation.
func (g *Governor) Reserve(c Component) {
	if strings.TrimSpace(c.Name) == "" {
		return
	}
	if c.Kind == "" {
		c.Kind = ComponentResident
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range g.components {
		if g.components[i].Name == c.Name {
			g.components[i] = c
			return
		}
	}
	g.components = append(g.components, c)
}

// Reserve declares a component on the process-wide governor.
func Reserve(c Component) { Gov.Reserve(c) }

// OnHeavyRelease registers a cache-trim hook run each time the heavy slot is released,
// before FreeOSMemory. Hooks must be quick; a panicking hook is logged and skipped.
func (g *Governor) OnHeavyRelease(fn func()) {
	if fn == nil {
		return
	}
	g.mu.Lock()
	g.onRelease = append(g.onRelease, fn)
	g.mu.Unlock()
}

type noHeavyWaitKey struct{}

// WithoutHeavyWait marks ctx as a hook or capture path. Heavy admission from such a
// context never waits: when the slot is busy it fails at once with ErrOverloaded.
func WithoutHeavyWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, noHeavyWaitKey{}, true)
}

func heavyWaitForbidden(ctx context.Context) bool {
	v, _ := ctx.Value(noHeavyWaitKey{}).(bool)
	return v
}

// AcquireHeavy takes the heavy slot on the process-wide governor.
func AcquireHeavy(ctx context.Context, owner string, declaredBytes int64) (func(), error) {
	return Gov.AcquireHeavy(ctx, owner, declaredBytes)
}

// AcquireHeavy takes the single heavy slot for owner, a short code-chosen label such as
// "index_build" (shown on the public health endpoint, so never user data). It waits at
// most the configured bound for a busy slot, then admits the work only if measured tree
// memory plus declaredBytes stays within the soft ceiling. The returned release is
// idempotent and must be called when the work ends.
func (g *Governor) AcquireHeavy(ctx context.Context, owner string, declaredBytes int64) (func(), error) {
	owner = heavyLabel(owner)
	declaredBytes = max(0, declaredBytes)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case g.slot <- struct{}{}:
	default:
		if heavyWaitForbidden(ctx) {
			g.mu.Lock()
			g.stats.refusedHotPath++
			held := g.holderLabelLocked()
			g.mu.Unlock()
			return nil, fmt.Errorf("%w (heavy slot held by %s; hook and capture paths never wait for it)", ErrOverloaded, held)
		}
		if err := g.waitForSlot(ctx, owner); err != nil {
			return nil, err
		}
	}
	// With the slot held nothing else heavy is running, so the sample shows the memory
	// this work would add to.
	if err := g.admitMemory(declaredBytes); err != nil {
		<-g.slot
		g.mu.Lock()
		g.stats.refusedRSS++
		g.mu.Unlock()
		return nil, err
	}
	g.mu.Lock()
	g.holder = &heavyHolder{owner: owner, since: time.Now(), declared: declaredBytes}
	g.stats.acquired++
	g.mu.Unlock()
	stopWatch := g.watchWhileHeld()
	var once sync.Once
	return func() { once.Do(func() { g.releaseHeavy(stopWatch) }) }, nil
}

func (g *Governor) waitForSlot(ctx context.Context, owner string) error {
	w := &heavyWaiter{owner: owner, since: time.Now()}
	g.mu.Lock()
	g.queue = append(g.queue, w)
	g.mu.Unlock()
	timer := time.NewTimer(g.cfg.HeavyWait)
	defer timer.Stop()
	defer g.dequeue(w)
	select {
	case g.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		g.stats.cancelled++
		g.mu.Unlock()
		return ctx.Err()
	case <-timer.C:
		g.mu.Lock()
		g.stats.refusedBusy++
		held := g.holderLabelLocked()
		g.mu.Unlock()
		return fmt.Errorf("%w (heavy slot held by %s; waited %s)", ErrOverloaded, held, g.cfg.HeavyWait)
	}
}

func (g *Governor) dequeue(w *heavyWaiter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, q := range g.queue {
		if q == w {
			g.queue = append(g.queue[:i], g.queue[i+1:]...)
			return
		}
	}
}

func (g *Governor) holderLabelLocked() string {
	if g.holder == nil {
		return "another holder"
	}
	return g.holder.owner
}

// admitMemory refuses when measured tree memory plus declared would pass the soft
// ceiling. Without a sampler it projects from the steady reservations instead.
func (g *Governor) admitMemory(declared int64) error {
	s, err := g.Sample()
	base, basis := s.admissionBytes(), "measured tree"
	if err != nil || !s.Supported {
		base, basis = g.steadyReserved(), "reserved steady"
	}
	soft := g.cfg.SoftCeilingBytes
	if base > soft || declared > soft-base {
		return fmt.Errorf("%w (memory near the soft ceiling: %s %d + declared %d > %d bytes)", ErrOverloaded, basis, base, declared, soft)
	}
	return nil
}

func (g *Governor) steadyReserved() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	var n int64
	for _, c := range g.components {
		n += c.SteadyBytes
	}
	return n
}

func (g *Governor) releaseHeavy(stopWatch func()) {
	stopWatch()
	g.mu.Lock()
	hooks := append([]func(){}, g.onRelease...)
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.holder = nil
		g.stats.released++
		g.mu.Unlock()
		<-g.slot
	}()
	for _, h := range hooks {
		runTrimHook(h)
	}
	g.cfg.FreeOSMemory()
	g.mu.Lock()
	g.stats.freeOSMemory++
	g.mu.Unlock()
}

func runTrimHook(h func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("budget: heavy-release trim hook panicked: %v", r)
		}
	}()
	h()
}

// watchWhileHeld samples every WatchInterval until the returned stop is called.
func (g *Governor) watchWhileHeld() (stop func()) {
	done := make(chan struct{})
	exited := make(chan struct{})
	g.wmu.Lock()
	g.watching = true
	g.wmu.Unlock()
	go func() {
		defer close(exited)
		t := time.NewTicker(g.cfg.WatchInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_, _ = g.Sample()
			}
		}
	}()
	return func() {
		close(done)
		<-exited
		g.wmu.Lock()
		g.watching = false
		g.wmu.Unlock()
	}
}

// Sample measures the process tree now and records it.
func (g *Governor) Sample() (TreeSample, error) {
	s, err := g.cfg.Sampler()
	if err != nil {
		s.Supported = false
		s.Error = err.Error()
	}
	if s.At.IsZero() {
		s.At = time.Now()
	}
	g.wmu.Lock()
	defer g.wmu.Unlock()
	g.samples++
	g.last, g.hasLast = s, true
	if err == nil && s.Supported {
		g.peakRSS = max(g.peakRSS, s.RSSBytes)
		g.peakFP = max(g.peakFP, s.FootprintBytes)
		if s.admissionBytes() > g.cfg.SoftCeilingBytes {
			g.overSoft++
			if g.watching && time.Since(g.warnedFor) > time.Minute {
				g.warnedFor = time.Now()
				log.Printf("budget: process tree at %d bytes (rss %d, footprint %d) is over the %d-byte soft ceiling during heavy work",
					s.admissionBytes(), s.RSSBytes, s.FootprintBytes, g.cfg.SoftCeilingBytes)
			}
		}
	}
	return s, err
}

func (g *Governor) cachedSample(maxAge time.Duration) (TreeSample, error) {
	g.wmu.Lock()
	if g.hasLast && time.Since(g.last.At) < maxAge {
		s := g.last
		g.wmu.Unlock()
		if !s.Supported {
			return s, errNoRSSSampler
		}
		return s, nil
	}
	g.wmu.Unlock()
	return g.Sample()
}

// ComponentStatus is one reservation in the health view. UsedBytes is null when unknown.
type ComponentStatus struct {
	Name                string `json:"name"`
	Kind                string `json:"kind"`
	ReservedSteadyBytes int64  `json:"reserved_steady_bytes"`
	ReservedPeakBytes   int64  `json:"reserved_peak_bytes"`
	UsedBytes           *int64 `json:"used_bytes"`
	UsedBasis           string `json:"used_basis,omitempty"`
}

// ReservationStatus totals the static reservations.
type ReservationStatus struct {
	SteadyTotalBytes int64             `json:"steady_total_bytes"`
	PeakTotalBytes   int64             `json:"peak_total_bytes"`
	Components       []ComponentStatus `json:"components"`
}

// HeavyWaiterStatus is one queued heavy request.
type HeavyWaiterStatus struct {
	Owner    string `json:"owner"`
	WaitedMS int64  `json:"waited_ms"`
}

// HeavySlotStatus is the heavy slot in the health view.
type HeavySlotStatus struct {
	Capacity          int                 `json:"capacity"`
	Busy              bool                `json:"busy"`
	Owner             string              `json:"owner"`
	HeldMS            int64               `json:"held_ms"`
	DeclaredBytes     int64               `json:"declared_bytes"`
	WaitBoundMS       int64               `json:"wait_bound_ms"`
	QueueLen          int                 `json:"queue_len"`
	Queue             []HeavyWaiterStatus `json:"queue"`
	Acquired          int64               `json:"acquired"`
	Released          int64               `json:"released"`
	RefusedBusy       int64               `json:"refused_busy"`
	RefusedRSS        int64               `json:"refused_rss"`
	RefusedHotPath    int64               `json:"refused_hot_path"`
	Cancelled         int64               `json:"cancelled"`
	FreeOSMemoryCalls int64               `json:"free_os_memory_calls"`
}

// WatchdogStatus is the RSS watchdog in the health view.
type WatchdogStatus struct {
	Scope                  string     `json:"scope"`
	IntervalMS             int64      `json:"interval_ms"`
	SamplingActive         bool       `json:"sampling_active"`
	Samples                int64      `json:"samples"`
	OverSoftCeilingSamples int64      `json:"over_soft_ceiling_samples"`
	PeakRSSBytes           int64      `json:"peak_rss_bytes"`
	PeakFootprintBytes     int64      `json:"peak_footprint_bytes"`
	Last                   TreeSample `json:"last"`
}

// PoolStatus / ChildStatus repeat the byte pool and child limit for the block.
type PoolStatus struct {
	Max   int64 `json:"max"`
	InUse int64 `json:"in_use"`
	Peak  int64 `json:"peak"`
}

type ChildStatus struct {
	Cap   int `json:"cap"`
	InUse int `json:"in_use"`
	Peak  int `json:"peak"`
}

// Snapshot is the /api/health budget block.
type Snapshot struct {
	Version          int               `json:"version"`
	GateBytes        int64             `json:"gate_bytes"`
	SoftCeilingBytes int64             `json:"soft_ceiling_bytes"`
	Reservations     ReservationStatus `json:"reservations"`
	HeavySlot        HeavySlotStatus   `json:"heavy_slot"`
	Watchdog         WatchdogStatus    `json:"watchdog"`
	TransientPool    PoolStatus        `json:"transient_pool"`
	Children         ChildStatus       `json:"children"`
	Runtime          RuntimeStats      `json:"runtime"`
	Counters         CounterSnapshot   `json:"counters"`
}

// Status is the process-wide governor's snapshot.
func Status() Snapshot { return Gov.Snapshot() }

// Snapshot reports reservations, the heavy slot, the watchdog, the byte pool, the
// child limit, Go runtime stats and the data-movement counters.
func (g *Governor) Snapshot() Snapshot {
	_, _ = g.cachedSample(healthSampleMaxAge)
	s := Snapshot{Version: 1, GateBytes: GateBytes, SoftCeilingBytes: g.cfg.SoftCeilingBytes}

	g.mu.Lock()
	comps := append([]Component{}, g.components...)
	h := HeavySlotStatus{Capacity: cap(g.slot), WaitBoundMS: g.cfg.HeavyWait.Milliseconds(), QueueLen: len(g.queue),
		Queue: []HeavyWaiterStatus{}, Acquired: g.stats.acquired, Released: g.stats.released, RefusedBusy: g.stats.refusedBusy,
		RefusedRSS: g.stats.refusedRSS, RefusedHotPath: g.stats.refusedHotPath, Cancelled: g.stats.cancelled,
		FreeOSMemoryCalls: g.stats.freeOSMemory}
	if g.holder != nil {
		h.Busy, h.Owner, h.DeclaredBytes = true, g.holder.owner, g.holder.declared
		h.HeldMS = time.Since(g.holder.since).Milliseconds()
	}
	for i, w := range g.queue {
		if i == maxQueueShown {
			break
		}
		h.Queue = append(h.Queue, HeavyWaiterStatus{Owner: w.owner, WaitedMS: time.Since(w.since).Milliseconds()})
	}
	g.mu.Unlock()
	s.HeavySlot = h

	s.Reservations.Components = make([]ComponentStatus, 0, len(comps))
	for _, c := range comps {
		cs := ComponentStatus{Name: c.Name, Kind: c.Kind, ReservedSteadyBytes: c.SteadyBytes, ReservedPeakBytes: c.PeakBytes, UsedBasis: c.UsedBasis}
		if c.Used != nil {
			if v, ok := c.Used(); ok {
				cs.UsedBytes = &v
			}
		}
		s.Reservations.SteadyTotalBytes += c.SteadyBytes
		s.Reservations.PeakTotalBytes += c.PeakBytes
		s.Reservations.Components = append(s.Reservations.Components, cs)
	}

	g.wmu.Lock()
	s.Watchdog = WatchdogStatus{Scope: "this process and its xMustard-owned descendants (Rust core, git, ast-grep); other descendants are external and reported separately; per-agent stdio shims are launched by clients and are outside it",
		IntervalMS: g.cfg.WatchInterval.Milliseconds(), SamplingActive: g.watching, Samples: g.samples,
		OverSoftCeilingSamples: g.overSoft, PeakRSSBytes: g.peakRSS, PeakFootprintBytes: g.peakFP, Last: g.last}
	g.wmu.Unlock()

	s.TransientPool = PoolStatus{Max: TransientBytes.Max(), InUse: TransientBytes.InUse(), Peak: TransientBytes.Peak()}
	s.Children = ChildStatus{Cap: Children.Cap(), InUse: Children.InUse(), Peak: Children.Peak()}
	s.Runtime = ReadRuntimeStats()
	s.Counters = Counters()
	return s
}

// heavyLabel keeps an owner label short and inert: [A-Za-z0-9_.:/-], at most 64 bytes.
func heavyLabel(s string) string {
	b := make([]byte, 0, min(len(s), 64))
	for i := 0; i < len(s) && len(b) < 64; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.IndexByte("_.:/-", c) >= 0:
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	if len(b) == 0 {
		return "unlabeled"
	}
	return string(b)
}

func envInt64(name string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

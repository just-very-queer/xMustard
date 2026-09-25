package budget

import (
	"context"
	"log"
	"sync"
	"time"
)

// Memory pressure and reclaim (WS-06B). A resident component that can give memory back
// declares a Reclaim hook with its reservation. The Rust worker is one: its allocator
// keeps freed memory until the process exits, so recycling it is how its memory goes
// back to the OS. The governor asks reclaimable components for memory in two cases:
//
//   - Heavy admission. Heavy work refused for memory first asks every reclaimable
//     component, waits up to ReclaimWait for the memory to come back, and measures the
//     tree once more before refusing. A hook or capture path (WithoutHeavyWait) never
//     waits for this: the components are asked, and the path is refused at once.
//   - Over the soft ceiling. A sample over the soft ceiling asks every reclaimable
//     component, at most once per PressureInterval. Samples come from heavy work (every
//     WatchInterval), health polls and CheckPressure.
//
// Components apply their own idle policy through Level. The worker gives its memory back
// when it goes idle above its runaway line, or when it has been idle for its trim
// period while the level is tight or over, or while it is above its reserved peak
// (rustcore/worker_governor.go).

// Reasons the governor asks a component to give memory back.
const (
	PressureHeavyAdmission  = "heavy_admission"
	PressureOverSoftCeiling = "over_soft_ceiling"
)

// Pressure levels, from the latest sample of the tree plus the stdio shims.
const (
	// LevelNormal leaves room under the soft ceiling for heavy work of the heavy line.
	LevelNormal = "normal"
	// LevelTight leaves less room than the heavy line, so heavy work of that size
	// would be refused.
	LevelTight = "tight"
	// LevelOver is past the soft ceiling.
	LevelOver = "over"
	// LevelUnknown means the tree cannot be measured on this platform.
	LevelUnknown = "unknown"
)

const (
	// DefaultReclaimWait bounds how long refused heavy work waits for reclaimed memory.
	// The Rust worker exits within its 2 s kill grace after it is retired.
	DefaultReclaimWait = 3 * time.Second
	// DefaultPressureInterval spaces the reclaim requests a tree over the soft ceiling
	// sends. It keeps a tree that stays over from recycling a component on every sample.
	DefaultPressureInterval = 5 * time.Second
)

// Pressure is one request to give memory back.
type Pressure struct {
	Reason string
	// TreeBytes is the admission measure (owned tree plus stdio shims, the larger of the
	// two metrics) that caused the request.
	TreeBytes        int64
	SoftCeilingBytes int64
	// DeclaredBytes is the refused heavy work's declaration (heavy_admission only).
	DeclaredBytes int64
}

type reclaimStats struct {
	mu                   sync.Mutex
	requests             map[string]int64
	released             int64
	waits                int64
	admittedAfterReclaim int64
	lastDispatch         time.Time
}

// ReclaimStatus is the reclaim part of the health view.
type ReclaimStatus struct {
	// Level is from the latest sample: normal, tight, over or unknown.
	Level string `json:"level"`
	// TightAboveBytes is where the level turns tight: the soft ceiling less the heavy
	// line.
	TightAboveBytes int64 `json:"tight_above_bytes"`
	IntervalMS      int64 `json:"interval_ms"`
	WaitBoundMS     int64 `json:"wait_bound_ms"`
	// Requests counts the requests sent to reclaimable components, by reason.
	Requests map[string]int64 `json:"requests"`
	// Released counts the requests a component answered by releasing memory at once.
	Released int64 `json:"released"`
	// Waits counts refused heavy work that waited for reclaimed memory, and
	// AdmittedAfterReclaim how much of it was then admitted.
	Waits                int64 `json:"waits"`
	AdmittedAfterReclaim int64 `json:"admitted_after_reclaim"`
}

// levelOf classifies an admission measure against the soft ceiling.
func (g *Governor) levelOf(treeBytes int64) string {
	switch soft := g.cfg.SoftCeilingBytes; {
	case treeBytes > soft:
		return LevelOver
	case treeBytes > soft-g.cfg.HeavyLineBytes:
		return LevelTight
	}
	return LevelNormal
}

// CheckPressure samples the tree unless a sample younger than the health cache exists.
// A sample over the soft ceiling asks the reclaimable components for memory, at most
// once per PressureInterval. Components call it at their own quiet moments (the Rust
// worker when it goes idle), so the soft ceiling is enforced between heavy work and
// health polls too.
func (g *Governor) CheckPressure() { _, _ = g.cachedSample(healthSampleMaxAge) }

// Level measures the tree (reusing a sample younger than the health cache) and
// classifies it.
func (g *Governor) Level() string {
	s, err := g.cachedSample(healthSampleMaxAge)
	if err != nil || !s.Supported {
		return LevelUnknown
	}
	return g.levelOf(s.admissionBytes())
}

// reclaimers returns the enabled components that can give memory back.
func (g *Governor) reclaimers() []Component {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []Component
	for _, c := range g.components {
		if c.Reclaim != nil && c.enabled() {
			out = append(out, c)
		}
	}
	return out
}

// askReclaim sends p to every reclaimable component and returns the channels of those
// that released memory now. A panicking hook is logged and skipped.
func (g *Governor) askReclaim(p Pressure) []<-chan struct{} {
	var released []<-chan struct{}
	for _, c := range g.reclaimers() {
		g.reclaim.mu.Lock()
		g.reclaim.requests[p.Reason]++
		g.reclaim.mu.Unlock()
		if ch := runReclaimHook(c, p); ch != nil {
			released = append(released, ch)
		}
	}
	if len(released) > 0 {
		g.reclaim.mu.Lock()
		g.reclaim.released += int64(len(released))
		g.reclaim.mu.Unlock()
	}
	return released
}

func runReclaimHook(c Component, p Pressure) (ch <-chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("budget: reclaim hook of %s panicked: %v", c.Name, r)
			ch = nil
		}
	}()
	return c.Reclaim(p)
}

// reclaimForHeavy asks for memory after heavy work was refused for it and waits, up to
// ReclaimWait and while ctx lasts, for what was released. It reports whether anything
// was released, so the caller measures again.
func (g *Governor) reclaimForHeavy(ctx context.Context, p Pressure) bool {
	released := g.askReclaim(p)
	if len(released) == 0 {
		return false
	}
	g.reclaim.mu.Lock()
	g.reclaim.waits++
	g.reclaim.mu.Unlock()
	timer := time.NewTimer(g.cfg.ReclaimWait)
	defer timer.Stop()
	for _, ch := range released {
		select {
		case <-ch:
		case <-timer.C:
			return true
		case <-ctx.Done():
			return true
		}
	}
	return true
}

// notePressure runs after each supported sample: a tree over the soft ceiling asks the
// reclaimable components for memory, at most once per PressureInterval, without
// waiting for them.
func (g *Governor) notePressure(treeBytes int64) {
	if treeBytes <= g.cfg.SoftCeilingBytes {
		return
	}
	g.reclaim.mu.Lock()
	if !g.reclaim.lastDispatch.IsZero() && time.Since(g.reclaim.lastDispatch) < g.cfg.PressureInterval {
		g.reclaim.mu.Unlock()
		return
	}
	g.reclaim.lastDispatch = time.Now()
	g.reclaim.mu.Unlock()
	go g.askReclaim(Pressure{Reason: PressureOverSoftCeiling, TreeBytes: treeBytes, SoftCeilingBytes: g.cfg.SoftCeilingBytes})
}

func (g *Governor) reclaimStatus(level string) ReclaimStatus {
	g.reclaim.mu.Lock()
	defer g.reclaim.mu.Unlock()
	st := ReclaimStatus{Level: level, TightAboveBytes: g.cfg.SoftCeilingBytes - g.cfg.HeavyLineBytes,
		IntervalMS: g.cfg.PressureInterval.Milliseconds(), WaitBoundMS: g.cfg.ReclaimWait.Milliseconds(),
		Requests: make(map[string]int64, len(g.reclaim.requests)), Released: g.reclaim.released,
		Waits: g.reclaim.waits, AdmittedAfterReclaim: g.reclaim.admittedAfterReclaim}
	for k, v := range g.reclaim.requests {
		st.Requests[k] = v
	}
	return st
}

// Process components: resident components that packages above budget own (budget is a
// leaf package, so it cannot declare them itself).
var processComponents struct {
	sync.Mutex
	list []Component
}

// RegisterProcessComponent declares c on the process-wide governor and on every
// governor NewProcessGovernor builds later. A package that owns a resident component
// calls it from init.
func RegisterProcessComponent(c Component) {
	processComponents.Lock()
	processComponents.list = append(processComponents.list, c)
	processComponents.Unlock()
	Gov.Reserve(c)
}

func (g *Governor) registerProcessComponents() {
	processComponents.Lock()
	list := append([]Component(nil), processComponents.list...)
	processComponents.Unlock()
	for _, c := range list {
		g.Reserve(c)
	}
}

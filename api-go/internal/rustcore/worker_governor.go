package rustcore

import (
	"log"
	"time"

	"xmustard/api-go/internal/budget"
)

// The resident worker under the budget governor (WS-06B, PAR-RT-01, PAR-RT-04).
//
// The worker is a governed component: it declares its line with the governor, reports
// its measured RSS on /api/health, and gives its memory back when the governor asks.
// Its allocator keeps freed memory until the process exits (the idle trim returned
// 0-3.5 of 19-23 MiB on pi-mono), so giving memory back means recycling: the worker is
// retired, its calls in flight finish, it exits, and the next call starts a fresh one
// (one exec and a graph load from the on-disk cache).
//
// It is recycled when:
//   - heavy work is refused for memory (budget.PressureHeavyAdmission), or a sample of
//     the tree is over the soft ceiling (budget.PressureOverSoftCeiling). An idle
//     worker is retired at once and the governor waits for its exit; a busy one is
//     marked and retired when its last call finishes, so no second worker starts
//     beside it while memory is short. Going idle is itself a sampling point
//     (checkPressureAtIdle), so query traffic cannot keep the tree over the soft
//     ceiling between heavy work and health polls; the governor asks at most once per
//     pressure interval;
//   - it goes idle above the runaway line (reasonRunaway);
//   - it has been idle for its trim period while the governor's level is tight or over
//     (reasonIdleTight), or while it is still above its reserved peak
//     (reasonIdleOverLine). Otherwise it stays warm until its idle exit.
//
// An agent's calls leave the worker idle between them, and its working set passes the
// peak line during ordinary queries, so the peak line is checked only after the trim
// period: recycling at every idle moment would restart the worker between the calls
// of one burst.

// The worker's lines, on the ps-RSS basis (see the WS-06B record in
// docs/plans/2026-09-25-parity-build-plan.md). Steady is WS-02's measured plateau for a
// mixed query sequence on pi-mono (22.5-22.6 MiB), which the WS-06B runs also reached
// when idle (19.8-22.3 MiB). Peak is a budget line: with the API at its 28 MiB line
// and two stdio shims (about 22 MiB measured), a worker at 32 MiB keeps the tree under
// the 90 MiB soft ceiling. The measured working set passes it during queries (35-41 MiB
// on pi-mono, up to 55.8 MiB on cline, one resident JSON symbol graph), so a worker
// above it is recycled once it has been idle for its trim period. The runaway line is
// above every measured working set. All three are over the design line (a 3 MiB
// service base plus a 12 MiB CSR graph) until WS-14 replaces the JSON snapshot.
const (
	workerSteadyBytes  int64 = 24 << 20
	workerPeakBytes    int64 = 32 << 20
	workerRunawayBytes int64 = 64 << 20
)

// Recycle reasons besides the governor's pressure reasons.
const (
	reasonRunaway      = "runaway"
	reasonIdleOverLine = "idle_over_line"
	reasonIdleTight    = "idle_tight"
	reasonRequested    = "requested"
)

// workerPeakLine and workerRunawayLine are variables for tests.
var (
	workerPeakLine    = workerPeakBytes
	workerRunawayLine = workerRunawayBytes
)

func init() {
	budget.RegisterProcessComponent(budget.Component{
		Name:        "rust_worker",
		Kind:        budget.ComponentResident,
		SteadyBytes: workerSteadyBytes,
		PeakBytes:   workerPeakBytes,
		UsedBasis:   "ps_rss",
		Used:        coreWorkerUsed,
		Enabled:     workerEnabled,
		Descendant:  true,
		Reclaim:     reclaimCoreWorker,
	})
}

// coreWorkerUsed is the running worker's RSS; a stopped worker uses nothing.
func coreWorkerUsed() (int64, bool) {
	pid := coreWorker.runningPID()
	if pid == 0 {
		return 0, true
	}
	rss, _, ok := budget.ProcessMemory(pid)
	return rss, ok
}

func (s *workerSupervisor) runningPID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil {
		return 0
	}
	return s.proc.pid
}

// reclaimCoreWorker is the worker's budget.Component.Reclaim hook. An idle worker is
// retired now, and its done channel closes when it has exited. A busy one is marked,
// and retired when its last call finishes (release). A worker retired a moment ago
// that is still exiting is reported by its done channel, so a heavy admission waits
// for it instead of refusing.
func reclaimCoreWorker(pr budget.Pressure) <-chan struct{} {
	s := coreWorker
	s.mu.Lock()
	p := s.proc
	if p == nil {
		last := s.lastRecycled
		s.mu.Unlock()
		if last != nil {
			select {
			case <-last.done:
			default:
				return last.done
			}
		}
		return nil
	}
	if p.active > 0 {
		if !p.recycleAtIdle {
			p.recycleAtIdle, p.recycleReason = true, pr.Reason
		}
		s.mu.Unlock()
		return nil
	}
	s.proc, p.retiring = nil, true
	s.mu.Unlock()
	s.noteRecycle(p, pr.Reason)
	p.retire()
	return p.done
}

// recycleIdle retires p for reason if it is still the current, idle worker.
func (s *workerSupervisor) recycleIdle(p *workerProc, reason string) bool {
	s.mu.Lock()
	if s.proc != p || p.active != 0 || p.retiring {
		s.mu.Unlock()
		return false
	}
	s.proc, p.retiring = nil, true
	s.mu.Unlock()
	s.noteRecycle(p, reason)
	p.retire()
	return true
}

// noteRecycle counts a recycle and remembers p until it has exited.
func (s *workerSupervisor) noteRecycle(p *workerProc, reason string) {
	s.mu.Lock()
	if s.recycles == nil {
		s.recycles = map[string]int64{}
	}
	s.recycles[reason]++
	s.lastRecycled = p
	s.mu.Unlock()
	log.Printf("rust-core worker %d recycled (%s)", p.pid, reason)
}

// checkRunaway runs when p goes idle: a worker above the runaway line is recycled.
func (s *workerSupervisor) checkRunaway(p *workerProc) {
	if rss, _, ok := budget.ProcessMemory(p.pid); ok && rss > workerRunawayLine {
		s.recycleIdle(p, reasonRunaway)
	}
}

// checkPressureAtIdle has the governor sample the tree, off the caller's path, when the
// worker goes idle: over the soft ceiling it asks for the worker's memory (reclaim-
// CoreWorker). One check runs at a time, and the governor reuses a sample younger
// than its health cache, so a burst of calls costs at most a few tree walks a second.
func (s *workerSupervisor) checkPressureAtIdle(g *budget.Governor) {
	if !s.pressureCheck.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.pressureCheck.Store(false)
		g.CheckPressure()
	}()
}

// armIdlePressureCheck schedules the idle check at p's trim period (while its idle exit
// is later), against governor g. The caller holds s.mu.
func (s *workerSupervisor) armIdlePressureCheck(p *workerProc, g *budget.Governor) {
	if p.trimAfter <= 0 || (p.idleAfter > 0 && p.trimAfter >= p.idleAfter) {
		return
	}
	if p.pressureTimer != nil {
		p.pressureTimer.Stop()
	}
	p.pressureTimer = time.AfterFunc(p.trimAfter, func() { s.idlePressureCheck(p, g) })
}

// idlePressureCheck recycles p when it has been idle for its trim period and the
// governor's level is tight or over, or p is still above its reserved peak. The trim
// has dropped its snapshots by then, but the allocator kept most of the memory; only
// an exit returns it. Otherwise the check repeats every trim period until the idle
// exit.
func (s *workerSupervisor) idlePressureCheck(p *workerProc, g *budget.Governor) {
	s.mu.Lock()
	idle := s.proc == p && p.active == 0 && time.Since(p.lastUsed) >= p.trimAfter
	s.mu.Unlock()
	if !idle {
		return
	}
	switch g.Level() {
	case budget.LevelTight, budget.LevelOver:
		s.recycleIdle(p, reasonIdleTight)
		return
	}
	if rss, _, ok := budget.ProcessMemory(p.pid); ok && rss > workerPeakLine {
		s.recycleIdle(p, reasonIdleOverLine)
		return
	}
	s.mu.Lock()
	if s.proc == p && p.active == 0 {
		s.armIdlePressureCheck(p, g)
	}
	s.mu.Unlock()
}

// WorkerHealth is the resident worker in the /api/health budget block.
type WorkerHealth struct {
	WorkerStats
	Running bool `json:"running"`
	// RSSBytes and FootprintBytes measure the running worker (null when it is not
	// running or cannot be measured).
	RSSBytes            *int64 `json:"rss_bytes"`
	FootprintBytes      *int64 `json:"footprint_bytes"`
	ReservedSteadyBytes int64  `json:"reserved_steady_bytes"`
	ReservedPeakBytes   int64  `json:"reserved_peak_bytes"`
	RunawayBytes        int64  `json:"runaway_bytes"`
	IdleExitMS          int64  `json:"idle_exit_ms"`
	IdleCheckMS         int64  `json:"idle_check_ms"`
	Policy              string `json:"policy"`
}

const workerPolicy = "recycled (retired once its calls finish; the next call starts a fresh worker) when heavy work is refused for memory, when a sample is over the soft ceiling, when it goes idle above runaway_bytes, or when it has been idle for idle_check_ms while the level is tight or over or while it is above reserved_peak_bytes; it exits after idle_exit_ms without calls"

// CoreWorkerHealth reports the worker's counters, its measured memory and its policy.
func CoreWorkerHealth() WorkerHealth {
	set := readWorkerSettings()
	h := WorkerHealth{WorkerStats: CoreWorkerStats(), ReservedSteadyBytes: workerSteadyBytes, ReservedPeakBytes: workerPeakBytes,
		RunawayBytes: workerRunawayBytes, IdleExitMS: set.idle.Milliseconds(), IdleCheckMS: set.trim.Milliseconds(), Policy: workerPolicy}
	if h.PID != 0 {
		h.Running = true
		if rss, fp, ok := budget.ProcessMemory(h.PID); ok {
			h.RSSBytes, h.FootprintBytes = &rss, &fp
		}
	}
	return h
}

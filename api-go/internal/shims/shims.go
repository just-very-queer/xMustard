package shims

import (
	"errors"
	"sync"
	"time"
)

const ProcessName = "xmustard-mcp"

var ErrProcessChanged = errors.New("stdio shim process identity changed or is unverifiable")

type Class string

const (
	Live     Class = "live"
	Orphaned Class = "orphaned"
	Runaway  Class = "runaway"
)

type Process struct {
	PID        int           `json:"pid"`
	PPID       int           `json:"ppid"`
	UID        int           `json:"uid"`
	StartTime  uint64        `json:"-"`
	Age        time.Duration `json:"-"`
	AgeKnown   bool          `json:"-"`
	AgeSeconds int64         `json:"age_seconds"`
	CPUS       time.Duration `json:"-"`
	CPUAt      time.Time     `json:"-"`
	CPUSeconds float64       `json:"cpu_seconds"`
	RSSBytes   int64         `json:"rss_bytes"`
	Comm       string        `json:"comm"`
	CPUSlope   float64       `json:"cpu_slope"`
	Class      Class         `json:"class"`
}

type Summary struct {
	Live     int   `json:"live"`
	Orphaned int   `json:"orphaned"`
	Runaway  int   `json:"runaway"`
	PIDs     []int `json:"pids"`
}

func Classify(p Process) Class {
	if p.Comm != ProcessName {
		return ""
	}
	if p.CPUS > 300*time.Second || p.CPUSlope >= .8 {
		return Runaway
	}
	if p.PPID == 1 && p.AgeKnown && p.Age > 10*time.Minute {
		return Orphaned
	}
	return Live
}

type ReapOutcome struct {
	PID    int    `json:"pid"`
	Class  Class  `json:"class"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
	err    error
}

func Reap(processes []Process, signal func(Process) error) error {
	for _, outcome := range ReapEach(processes, signal) {
		if outcome.err != nil {
			return outcome.err
		}
	}
	return nil
}

func ReapEach(processes []Process, signal func(Process) error) []ReapOutcome {
	outcomes := make([]ReapOutcome, 0)
	for _, p := range processes {
		class := Classify(p)
		if class != Runaway && class != Orphaned {
			continue
		}
		outcome := ReapOutcome{PID: p.PID, Class: class, Result: "signaled"}
		if err := signal(p); err != nil {
			outcome.Result = "failed"
			outcome.Error = err.Error()
			outcome.err = err
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

var sampleCollection sync.Mutex
var enumerateProcesses = listPlatform

func list() ([]Process, error) {
	sampleCollection.Lock()
	defer sampleCollection.Unlock()
	ps, err := enumerateProcesses()
	if err != nil {
		return nil, err
	}
	updateSlopes(ps, time.Now())
	for i := range ps {
		ps[i].Class = Classify(ps[i])
	}
	return ps, nil
}

func List() ([]Process, error) {
	return list()
}

func sameIdentity(before, after Process) bool {
	return before.PID == after.PID && before.UID == after.UID && before.StartTime == after.StartTime && before.Comm == after.Comm
}

func signalIfIdentity(expected Process, identify func(int) (Process, error), send func(int) error) error {
	for range 2 {
		current, err := identify(expected.PID)
		if err != nil || current.PID != expected.PID || current.UID != expected.UID || current.StartTime != expected.StartTime || current.Comm != ProcessName {
			return ErrProcessChanged
		}
	}
	return send(expected.PID)
}

func ReapCurrent(processes []Process) []ReapOutcome {
	return ReapEach(processes, signalProcess)
}

var slopes = struct {
	sync.Mutex
	samples map[int]sample
}{samples: make(map[int]sample)}

type sample struct {
	at        time.Time
	cpu       time.Duration
	uid       int
	startTime uint64
	comm      string
	slope     float64
}

const minimumSlopeInterval = time.Second

func updateSlopes(ps []Process, now time.Time) {
	slopes.Lock()
	defer slopes.Unlock()
	seen := make(map[int]bool, len(ps))
	for i := range ps {
		p := &ps[i]
		if p.Comm != ProcessName {
			continue
		}
		seen[p.PID] = true
		if p.CPUAt.IsZero() {
			p.CPUAt = now
		}
		old, ok := slopes.samples[p.PID]
		if !ok || old.uid != p.UID || old.startTime != p.StartTime || old.comm != p.Comm || p.CPUS < old.cpu {
			slopes.samples[p.PID] = sample{at: p.CPUAt, cpu: p.CPUS, uid: p.UID, startTime: p.StartTime, comm: p.Comm}
			continue
		}
		p.CPUSlope = old.slope
		if elapsed := p.CPUAt.Sub(old.at); elapsed >= minimumSlopeInterval {
			p.CPUSlope = (p.CPUS - old.cpu).Seconds() / elapsed.Seconds()
			slopes.samples[p.PID] = sample{at: p.CPUAt, cpu: p.CPUS, uid: p.UID, startTime: p.StartTime, comm: p.Comm, slope: p.CPUSlope}
		}
	}
	for pid := range slopes.samples {
		if !seen[pid] {
			delete(slopes.samples, pid)
		}
	}
}

func ListForOperator() ([]Process, error) {
	ps, err := List()
	if err != nil || len(ps) == 0 {
		return ps, err
	}
	time.Sleep(time.Second)
	return List()
}

func Count(ps []Process) Summary {
	s := Summary{PIDs: make([]int, 0, len(ps))}
	for _, p := range ps {
		s.PIDs = append(s.PIDs, p.PID)
		switch Classify(p) {
		case Live:
			s.Live++
		case Orphaned:
			s.Orphaned++
		case Runaway:
			s.Runaway++
		}
	}
	return s
}

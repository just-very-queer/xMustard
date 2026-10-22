package shims

import (
	"os"
	"sync"
	"time"
)

const ProcessName = "xmustard-mcp"

type Class string

const (
	Live     Class = "live"
	Orphaned Class = "orphaned"
	Runaway  Class = "runaway"
)

type Process struct {
	PID        int           `json:"pid"`
	PPID       int           `json:"ppid"`
	Age        time.Duration `json:"-"`
	AgeSeconds int64         `json:"age_seconds"`
	CPUS       time.Duration `json:"-"`
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
	if p.PPID == 1 && p.Age > 10*time.Minute {
		return Orphaned
	}
	return Live
}

func Reap(processes []Process, kill func(int, os.Signal) error) error {
	var first error
	for _, p := range processes {
		class := Classify(p)
		if class == Runaway || class == Orphaned {
			if err := kill(p.PID, os.Kill); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

var slopes = struct {
	sync.Mutex
	samples map[int]sample
}{samples: make(map[int]sample)}

type sample struct {
	at  time.Time
	cpu time.Duration
}

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
		if old, ok := slopes.samples[p.PID]; ok && now.After(old.at) && p.CPUS >= old.cpu {
			p.CPUSlope = float64(p.CPUS-old.cpu) / now.Sub(old.at).Seconds()
		}
		slopes.samples[p.PID] = sample{now, p.CPUS}
	}
	for pid := range slopes.samples {
		if !seen[pid] {
			delete(slopes.samples, pid)
		}
	}
}

func List() ([]Process, error) {
	ps, err := listPlatform()
	if err != nil {
		return nil, err
	}
	updateSlopes(ps, time.Now())
	for i := range ps {
		ps[i].Class = Classify(ps[i])
	}
	return ps, nil
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

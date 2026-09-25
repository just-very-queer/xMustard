package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GateBytes is the xMustard-owned process-tree limit (1e8 bytes = 95.4 MiB).
const GateBytes = 100_000_000

// RSS sampling follows the frozen v1 bench (scripts/bench/rss_bench.py): one `ps`
// snapshot every 100 ms, RSS summed over every xMustard-owned process in that single
// snapshot. The xMustard tree is the registered API process(es) with all their
// descendants, plus any xmustard-api/-mcp/-core process under the agent (the stdio
// shim the client launches) with its descendants. The agent's other processes are an
// external line and never count toward the gate. The eval executor itself is outside
// both trees. Sampling can miss short peaks, so these are SAMPLED peaks; footprint and
// PSS (WS-10's gate v2) are not measured here.

var xmustardProcNames = []string{"xmustard-api", "xmustard-mcp", "xmustard-core"}

type psProc struct {
	pid, ppid int
	rssKiB    int64
	comm      string
}

// psSnapshot runs one ps over all processes.
func psSnapshot(ctx context.Context) ([]psProc, error) {
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,rss=,comm=").Output()
	if err != nil {
		return nil, err
	}
	var procs []psProc
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		rss, e3 := strconv.ParseInt(f[2], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		// macOS prints an exiting process's name in parentheses; its pages still count.
		comm := strings.Join(f[3:], " ")
		if strings.HasPrefix(comm, "(") && strings.HasSuffix(comm, ")") {
			comm = comm[1 : len(comm)-1]
		}
		procs = append(procs, psProc{pid, ppid, rss, filepath.Base(comm)})
	}
	return procs, sc.Err()
}

// RoleKiB is one role's RSS in a sample.
type RoleKiB struct {
	Role   string `json:"role"`
	RSSKiB int64  `json:"rss_kib"`
	Procs  int    `json:"procs"`
}

// RSSSummary is what a run records.
type RSSSummary struct {
	Method          string    `json:"method"`
	IntervalMS      int       `json:"interval_ms"`
	Samples         int       `json:"samples"`
	PSFailures      int       `json:"ps_failures"`
	XmPeakKiB       int64     `json:"xmustard_peak_kib"`
	XmPeakPhase     string    `json:"xmustard_peak_phase,omitempty"`
	XmPeakRoles     []RoleKiB `json:"xmustard_peak_roles,omitempty"`
	XmPeakByPhase   []RoleKiB `json:"xmustard_peak_by_phase,omitempty"` // Role holds the phase name
	AgentPeakKiB    int64     `json:"agent_peak_kib"`                   // external line
	XmWithinGate    *bool     `json:"xmustard_within_gate,omitempty"`
	GateBytes       int64     `json:"gate_bytes"`
	XmRolesObserved []string  `json:"xmustard_roles_observed,omitempty"`
	Note            string    `json:"note,omitempty"`
}

// rssSampler samples in the background until stopped.
type rssSampler struct {
	interval time.Duration
	out      *os.File

	mu        sync.Mutex
	phase     string
	xmRoots   []int
	agentRoot int
	sum       RSSSummary
	seenRoles map[string]bool
	stop      chan struct{}
	done      chan struct{}
}

func startRSSSampler(interval time.Duration, samplesPath string) *rssSampler {
	s := &rssSampler{interval: interval, phase: "setup", seenRoles: map[string]bool{},
		stop: make(chan struct{}), done: make(chan struct{})}
	s.sum = RSSSummary{Method: "ps -A -o pid,ppid,rss,comm (sampled ps-RSS, v1 method)", IntervalMS: int(interval / time.Millisecond), GateBytes: GateBytes}
	if samplesPath != "" {
		s.out, _ = os.Create(samplesPath)
	}
	go s.loop()
	return s
}

func (s *rssSampler) setPhase(p string) {
	s.mu.Lock()
	s.phase = p
	s.mu.Unlock()
}

// setXmRoots replaces the registered xMustard root processes (the API restarts
// between setup and the agent phase).
func (s *rssSampler) setXmRoots(pids ...int) {
	s.mu.Lock()
	s.xmRoots = slices.Clone(pids)
	s.mu.Unlock()
}

func (s *rssSampler) setAgentRoot(pid int) {
	s.mu.Lock()
	s.agentRoot = pid
	s.mu.Unlock()
}

func (s *rssSampler) loop() {
	defer close(s.done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		s.sampleOnce()
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
	}
}

func (s *rssSampler) sampleOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	procs, err := psSnapshot(ctx)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.sum.PSFailures++
		return
	}
	xm, agent := attribute(procs, s.xmRoots, s.agentRoot)
	s.sum.Samples++
	var xmTotal, agentTotal int64
	roles := map[string]*RoleKiB{}
	for _, p := range xm {
		xmTotal += p.rssKiB
		role := p.comm
		if !slices.Contains(xmustardProcNames, role) {
			role = "child:" + role
		}
		s.seenRoles[role] = true
		r := roles[role]
		if r == nil {
			r = &RoleKiB{Role: role}
			roles[role] = r
		}
		r.RSSKiB += p.rssKiB
		r.Procs++
	}
	for _, p := range agent {
		agentTotal += p.rssKiB
	}
	if xmTotal > s.sum.XmPeakKiB {
		s.sum.XmPeakKiB = xmTotal
		s.sum.XmPeakPhase = s.phase
		s.sum.XmPeakRoles = s.sum.XmPeakRoles[:0]
		for _, k := range sortedKeys(roles) {
			s.sum.XmPeakRoles = append(s.sum.XmPeakRoles, *roles[k])
		}
	}
	found := false
	for i := range s.sum.XmPeakByPhase {
		if s.sum.XmPeakByPhase[i].Role == s.phase {
			found = true
			if xmTotal > s.sum.XmPeakByPhase[i].RSSKiB {
				s.sum.XmPeakByPhase[i].RSSKiB = xmTotal
				s.sum.XmPeakByPhase[i].Procs = len(xm)
			}
		}
	}
	if !found {
		s.sum.XmPeakByPhase = append(s.sum.XmPeakByPhase, RoleKiB{Role: s.phase, RSSKiB: xmTotal, Procs: len(xm)})
	}
	s.sum.AgentPeakKiB = max(s.sum.AgentPeakKiB, agentTotal)
	if s.out != nil {
		line, _ := json.Marshal(map[string]any{"phase": s.phase, "xmustard_kib": xmTotal, "agent_kib": agentTotal, "xmustard_procs": len(xm), "agent_procs": len(agent)})
		_, _ = s.out.Write(append(line, '\n'))
	}
}

// attribute splits a snapshot into the xMustard-owned tree and the agent's external
// tree.
func attribute(procs []psProc, xmRoots []int, agentRoot int) (xm, agent []psProc) {
	byPID := make(map[int]psProc, len(procs))
	children := map[int][]int{}
	for _, p := range procs {
		byPID[p.pid] = p
		children[p.ppid] = append(children[p.ppid], p.pid)
	}
	inXm := map[int]bool{}
	var mark func(pid int)
	mark = func(pid int) {
		if inXm[pid] {
			return
		}
		if _, ok := byPID[pid]; !ok {
			return
		}
		inXm[pid] = true
		for _, c := range children[pid] {
			mark(c)
		}
	}
	for _, r := range xmRoots {
		mark(r)
	}
	var agentTree []int
	if agentRoot > 0 {
		seen := map[int]bool{}
		stack := []int{agentRoot}
		for len(stack) > 0 {
			pid := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[pid] {
				continue
			}
			seen[pid] = true
			if _, ok := byPID[pid]; !ok {
				continue
			}
			agentTree = append(agentTree, pid)
			stack = append(stack, children[pid]...)
		}
		for _, pid := range agentTree {
			if slices.Contains(xmustardProcNames, byPID[pid].comm) {
				mark(pid)
			}
		}
	}
	for pid := range inXm {
		xm = append(xm, byPID[pid])
	}
	for _, pid := range agentTree {
		if !inXm[pid] {
			agent = append(agent, byPID[pid])
		}
	}
	return xm, agent
}

// finish stops sampling and returns the summary.
func (s *rssSampler) finish() RSSSummary {
	close(s.stop)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.out != nil {
		_ = s.out.Close()
	}
	sum := s.sum
	sum.XmPeakRoles = slices.Clone(sum.XmPeakRoles)
	sum.XmPeakByPhase = slices.Clone(sum.XmPeakByPhase)
	sum.XmRolesObserved = sortedKeys(s.seenRoles)
	if sum.Samples > 0 && sum.PSFailures == 0 && len(sum.XmRolesObserved) > 0 {
		ok := sum.XmPeakKiB*1024 <= GateBytes
		sum.XmWithinGate = &ok
	} else {
		sum.Note = "no gate verdict: no xMustard process sampled or ps failed"
	}
	return sum
}

// Localization is edit localization against the task's gold files (PAR-EVAL-09):
// which gold files the agent's diff touched.
type Localization struct {
	Gold      int     `json:"gold"`
	Touched   int     `json:"touched"`
	Hits      int     `json:"hits"`
	Recall    float64 `json:"recall"`
	Precision float64 `json:"precision"`
}

func editLocalization(gold, touched []string) *Localization {
	if len(gold) == 0 {
		return nil
	}
	l := &Localization{Gold: len(gold), Touched: len(touched)}
	for _, g := range gold {
		if slices.Contains(touched, g) {
			l.Hits++
		}
	}
	l.Recall = float64(l.Hits) / float64(l.Gold)
	if l.Touched > 0 {
		l.Precision = float64(l.Hits) / float64(l.Touched)
	}
	return l
}

// Price is a per-million-token price list entry for clients that report no cost.
type Price struct {
	InputPerMTok       float64 `yaml:"input_per_mtok" json:"input_per_mtok"`
	CachedInputPerMTok float64 `yaml:"cached_input_per_mtok" json:"cached_input_per_mtok"`
	CacheWritePerMTok  float64 `yaml:"cache_write_per_mtok" json:"cache_write_per_mtok"`
	OutputPerMTok      float64 `yaml:"output_per_mtok" json:"output_per_mtok"`
}

// priceUsage fills in cost from the price table when the client reported none.
func priceUsage(t *Transcript, model string, pricing map[string]Price) {
	if t.CostUSD != nil {
		return
	}
	p, ok := pricing[model]
	if !ok {
		t.CostSource = CostUnpriced
		return
	}
	c := (float64(t.Usage.Input)*p.InputPerMTok + float64(t.Usage.CacheRead)*p.CachedInputPerMTok +
		float64(t.Usage.CacheWrite)*p.CacheWritePerMTok + float64(t.Usage.Output)*p.OutputPerMTok) / 1e6
	c = math.Round(c*1e8) / 1e8
	t.CostUSD, t.CostSource = &c, CostPriceTable
}

// estTokens is the documented bytes/4 heuristic, used only for xMustard tool-result
// volume (tokens per recall); it is always labelled as an estimate.
func estTokens(bytes int) int {
	return (bytes + 3) / 4
}

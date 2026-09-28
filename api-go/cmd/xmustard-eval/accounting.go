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
// descendants, plus any xMustard process under the agent (the stdio shim the client
// launches) with its descendants. xMustard processes are recognised by name: the
// fixed names below plus the basenames of the configured stack binaries, so a renamed
// shim or relay still counts toward the gate; each run records the names it matched.
// The agent's other processes are an external line and never count toward the gate.
// The eval executor, its watchdog and the containment wrapper (bwrap) are outside the
// tree. Sampling can miss short peaks, so these are SAMPLED peaks; footprint and PSS
// (WS-10's gate v2) are not measured here.

var xmustardProcNames = []string{"xmustard-api", "xmustard-mcp", "xmustard-core"}

// containmentWrappers are harness processes that may sit between a registered root
// and the xMustard process; they are walked through but not counted.
var containmentWrappers = []string{"bwrap", "sandbox-exec"}

// procNameMatches reports whether a ps comm is one of names. Linux truncates comm to
// 15 bytes, so a 15-byte comm also matches a longer name it prefixes.
func procNameMatches(comm string, names []string) bool {
	for _, n := range names {
		if comm == n || (len(comm) == 15 && strings.HasPrefix(n, comm)) {
			return true
		}
	}
	return false
}

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

// RSSPart is the RSS of one role (xmustard-api, child:git, ...) or one phase.
type RSSPart struct {
	Name   string `json:"name"`
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
	XmPeakRoles     []RSSPart `json:"xmustard_peak_roles,omitempty"`
	XmPeakByPhase   []RSSPart `json:"xmustard_peak_by_phase,omitempty"`
	AgentPeakKiB    int64     `json:"agent_peak_kib"` // external line
	XmWithinGate    *bool     `json:"xmustard_within_gate,omitempty"`
	GateBytes       int64     `json:"gate_bytes"`
	XmRolesObserved []string  `json:"xmustard_roles_observed,omitempty"`
	XmNames         []string  `json:"xmustard_names,omitempty"` // names attributed to the xMustard tree
	Note            string    `json:"note,omitempty"`
}

// rssSampler samples in the background until stopped.
type rssSampler struct {
	interval time.Duration
	out      *os.File
	names    []string

	mu        sync.Mutex
	phase     string
	xmRoots   []int
	agentRoot int
	sum       RSSSummary
	seenRoles map[string]bool
	// agentProcs is every process seen in the client's tree: pid -> start time. A
	// process the client detached (setsid) is reparented away from the tree, so the
	// sweep uses this record to find it again.
	agentProcs map[int]int64
	stop       chan struct{}
	done       chan struct{}
}

func startRSSSampler(interval time.Duration, samplesPath string, names []string) *rssSampler {
	if len(names) == 0 {
		names = xmustardProcNames
	}
	s := &rssSampler{interval: interval, phase: "setup", seenRoles: map[string]bool{}, names: names, agentProcs: map[int]int64{},
		stop: make(chan struct{}), done: make(chan struct{})}
	s.sum = RSSSummary{Method: "ps -A -o pid,ppid,rss,comm (sampled ps-RSS, v1 method)", IntervalMS: int(interval / time.Millisecond), GateBytes: GateBytes,
		XmNames: slices.Clone(names)}
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
	// The phase (and roots) in effect when ps starts own the snapshot, not the ones
	// in effect when it returns ~100 ms later.
	s.mu.Lock()
	phase, xmRoots, agentRoot := s.phase, s.xmRoots, s.agentRoot
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	procs, err := psSnapshot(ctx)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.sum.PSFailures++
		return
	}
	xm, agent, agentAll := splitTrees(procs, xmRoots, agentRoot, s.names)
	for _, pid := range agentAll {
		if _, seen := s.agentProcs[pid]; !seen {
			if start, ok := procStartTime(pid); ok {
				s.agentProcs[pid] = start
			}
		}
	}
	s.sum.Samples++
	var xmTotal, agentTotal int64
	roles := map[string]*RSSPart{}
	for _, p := range xm {
		xmTotal += p.rssKiB
		role := p.comm
		if !procNameMatches(role, s.names) {
			role = "child:" + role
		}
		s.seenRoles[role] = true
		r := roles[role]
		if r == nil {
			r = &RSSPart{Name: role}
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
		s.sum.XmPeakPhase = phase
		s.sum.XmPeakRoles = s.sum.XmPeakRoles[:0]
		for _, k := range sortedKeys(roles) {
			s.sum.XmPeakRoles = append(s.sum.XmPeakRoles, *roles[k])
		}
	}
	found := false
	for i := range s.sum.XmPeakByPhase {
		if s.sum.XmPeakByPhase[i].Name == phase {
			found = true
			if xmTotal > s.sum.XmPeakByPhase[i].RSSKiB {
				s.sum.XmPeakByPhase[i].RSSKiB = xmTotal
				s.sum.XmPeakByPhase[i].Procs = len(xm)
			}
		}
	}
	if !found {
		s.sum.XmPeakByPhase = append(s.sum.XmPeakByPhase, RSSPart{Name: phase, RSSKiB: xmTotal, Procs: len(xm)})
	}
	s.sum.AgentPeakKiB = max(s.sum.AgentPeakKiB, agentTotal)
	if s.out != nil {
		line, _ := json.Marshal(map[string]any{"phase": phase, "xmustard_kib": xmTotal, "agent_kib": agentTotal, "xmustard_procs": len(xm), "agent_procs": len(agent)})
		_, _ = s.out.Write(append(line, '\n'))
	}
}

// splitTrees splits a snapshot into the xMustard-owned tree and the agent's external
// tree. Containment wrappers are in neither. agentAll is every pid under the agent
// root, whichever tree it counts toward.
func splitTrees(procs []psProc, xmRoots []int, agentRoot int, names []string) (xm, agent []psProc, agentAll []int) {
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
			if procNameMatches(byPID[pid].comm, names) {
				mark(pid)
			}
		}
	}
	for pid := range inXm {
		if !slices.Contains(containmentWrappers, byPID[pid].comm) {
			xm = append(xm, byPID[pid])
		}
	}
	for _, pid := range agentTree {
		if !inXm[pid] && !slices.Contains(containmentWrappers, byPID[pid].comm) {
			agent = append(agent, byPID[pid])
		}
	}
	return xm, agent, agentTree
}

// agentProcesses returns the processes seen in the client's tree (pid -> start time).
func (s *rssSampler) agentProcesses() map[int]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int]int64, len(s.agentProcs))
	for k, v := range s.agentProcs {
		out[k] = v
	}
	return out
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

// Price is a per-million-token price list entry for clients that report no cost. An
// unset cache_write_per_mtok bills cache writes at the input rate, as OpenAI does.
type Price struct {
	InputPerMTok       float64 `yaml:"input_per_mtok" json:"input_per_mtok"`
	CachedInputPerMTok float64 `yaml:"cached_input_per_mtok" json:"cached_input_per_mtok"`
	CacheWritePerMTok  float64 `yaml:"cache_write_per_mtok" json:"cache_write_per_mtok"`
	OutputPerMTok      float64 `yaml:"output_per_mtok" json:"output_per_mtok"`
}

// priceUsage fills in cost from the price table when the client reported none. It
// prices only usage the client reported for the whole session: pricing partial or
// missing usage would record a truncated run as cheap.
func priceUsage(t *Transcript, model string, pricing map[string]Price) {
	if t.CostUSD != nil {
		return
	}
	if !t.UsageReported {
		t.CostSource = CostUnpriced
		return
	}
	p, ok := pricing[model]
	if !ok {
		t.CostSource = CostUnpriced
		return
	}
	cacheWrite := p.CacheWritePerMTok
	if cacheWrite == 0 {
		cacheWrite = p.InputPerMTok
	}
	c := (float64(t.Usage.Input)*p.InputPerMTok + float64(t.Usage.CacheRead)*p.CachedInputPerMTok +
		float64(t.Usage.CacheWrite)*cacheWrite + float64(t.Usage.Output)*p.OutputPerMTok) / 1e6
	c = math.Round(c*1e8) / 1e8
	t.CostUSD, t.CostSource = &c, CostPriceTable
}

// estTokens is the documented bytes/4 heuristic, used only for xMustard tool-result
// volume (tokens per recall); it is always labelled as an estimate.
func estTokens(bytes int) int {
	return (bytes + 3) / 4
}

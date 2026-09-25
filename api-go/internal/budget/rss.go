package budget

import (
	"bufio"
	"bytes"
	"errors"
	"strconv"
	"strings"
	"time"
)

// TreeSample is one measurement of this process and its descendants. RSSBytes is on the
// ps-RSS basis the budget gate uses; FootprintBytes is the second metric (macOS
// phys_footprint, Linux PSS), which leaves out clean file-backed pages that ps-RSS
// counts. Both sum the xMustard-owned processes only: this process and descendants
// named as xMustard helpers (ownedProcessName). Every other descendant, and everything
// below it, is external (agent runs, LSP servers, terminals, test commands a
// verification run starts) and is reported on its own line, never in the owned sums
// (PARITY_REQUIREMENTS §7.6).
type TreeSample struct {
	At                 time.Time `json:"at"`
	Supported          bool      `json:"supported"`
	Basis              string    `json:"basis"`
	Processes          int       `json:"processes"`
	Truncated          bool      `json:"truncated,omitempty"`
	RSSBytes           int64     `json:"rss_bytes"`
	FootprintBytes     int64     `json:"footprint_bytes"`
	SelfRSSBytes       int64     `json:"self_rss_bytes"`
	SelfFootprintBytes int64     `json:"self_footprint_bytes"`
	ExternalProcesses  int       `json:"external_processes"`
	ExternalRSSBytes   int64     `json:"external_rss_bytes"`
	Error              string    `json:"error,omitempty"`
}

// ChildrenRSSBytes is the ps-RSS of the owned descendants alone.
func (s TreeSample) ChildrenRSSBytes() int64 { return max(0, s.RSSBytes-s.SelfRSSBytes) }

// procMem is one process's memory and short command name.
type procMem struct {
	rss, footprint int64
	name           string
}

// ownedProcessName reports whether a descendant with this command name is part of the
// xMustard-owned tree: xMustard binaries (the Rust core), git, and the ast-grep helper.
// Names are the kernel's short command name (16 bytes on macOS, 15 on Linux).
func ownedProcessName(name string) bool {
	switch {
	case strings.HasPrefix(name, "xmustard"):
		return true
	case name == "git", strings.HasPrefix(name, "git-"):
		return true
	case name == "ast-grep", name == "sg":
		return true
	}
	return false
}

// admissionBytes is the conservative size used for admission: the larger of the two
// metrics, so neither file-backed pages (ps-RSS) nor compressed or swapped memory
// (footprint) can hide growth.
func (s TreeSample) admissionBytes() int64 { return max(s.RSSBytes, s.FootprintBytes) }

// maxTreeProcesses bounds one tree walk; a larger tree is reported as truncated.
const maxTreeProcesses = 256

// errNoRSSSampler is returned by platforms without a tree sampler.
var errNoRSSSampler = errors.New("process-tree memory sampling is not supported on this platform")

// walkTree visits root and its descendants breadth-first, at most maxTreeProcesses of
// them. children lists a pid's direct children; measure returns ok=false for a process
// that is gone or unreadable, which is skipped with its subtree.
func walkTree(root int, children func(int) []int, measure func(int) (procMem, bool)) TreeSample {
	type item struct {
		pid      int
		external bool
	}
	s := TreeSample{At: time.Now(), Supported: true}
	queue := []item{{pid: root}}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if s.Processes+s.ExternalProcesses >= maxTreeProcesses {
			s.Truncated = true
			break
		}
		m, ok := measure(it.pid)
		if !ok {
			continue
		}
		external := it.external || (it.pid != root && !ownedProcessName(m.name))
		if external {
			s.ExternalProcesses++
			s.ExternalRSSBytes += m.rss
		} else {
			s.Processes++
			s.RSSBytes += m.rss
			s.FootprintBytes += m.footprint
			if it.pid == root {
				s.SelfRSSBytes, s.SelfFootprintBytes = m.rss, m.footprint
			}
		}
		for _, c := range children(it.pid) {
			if c > 0 && !seen[c] {
				seen[c] = true
				queue = append(queue, item{pid: c, external: external})
			}
		}
	}
	return s
}

// parseSmapsRollup reads Rss and Pss (kB lines) from /proc/<pid>/smaps_rollup text and
// returns them in bytes. ok is false when Rss is absent.
func parseSmapsRollup(data []byte) (rss, pss int64, ok bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case bytes.HasPrefix(line, []byte("Rss:")):
			if v, good := kbField(line[len("Rss:"):]); good {
				rss, ok = v, true
			}
		case bytes.HasPrefix(line, []byte("Pss:")):
			if v, good := kbField(line[len("Pss:"):]); good {
				pss = v
			}
		}
	}
	return rss, pss, ok
}

// kbField parses "   1234 kB" into bytes.
func kbField(b []byte) (int64, bool) {
	f := bytes.Fields(b)
	if len(f) == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(string(f[0]), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n << 10, true
}

// parseStatmResident returns the resident page count (second field) of /proc/<pid>/statm.
func parseStatmResident(data []byte) (int64, bool) {
	f := bytes.Fields(data)
	if len(f) < 2 {
		return 0, false
	}
	n, err := strconv.ParseInt(string(f[1]), 10, 64)
	return n, err == nil && n >= 0
}

// parseStatPPID returns the parent pid from /proc/<pid>/stat. The command name is in
// parentheses and may itself contain spaces or parentheses, so fields are read after
// the last ')'.
func parseStatPPID(data []byte) (int, bool) {
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, false
	}
	f := bytes.Fields(data[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(string(f[1]))
	return n, err == nil
}

// parsePIDList parses a whitespace-separated pid list (/proc/<pid>/task/<tid>/children).
func parsePIDList(data []byte) []int {
	var out []int
	for _, f := range bytes.Fields(data) {
		if n, err := strconv.Atoi(string(f)); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

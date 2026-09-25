//go:build linux

package budget

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Linux sampling reads procfs: Rss (the ps-RSS basis) and Pss from smaps_rollup, with
// statm as the Rss fallback on kernels without smaps_rollup (Pss is then 0), and the
// command name from comm. Children
// come from /proc/<pid>/task/<tid>/children, or from a scan of every /proc/<pid>/stat
// when that file is unavailable (kernels built without CONFIG_PROC_CHILDREN).

const treeBasis = "ps_rss+pss"

var pageSize = int64(os.Getpagesize())

func sampleOwnTree() (TreeSample, error) {
	children := linuxTaskChildren
	if !linuxHasTaskChildren() {
		byParent := linuxScanParents()
		children = func(pid int) []int { return byParent[pid] }
	}
	s := walkTree(os.Getpid(), children, linuxProcMem)
	s.Basis = treeBasis
	if s.Processes == 0 {
		return TreeSample{At: s.At, Basis: treeBasis}, errNoRSSSampler
	}
	return s, nil
}

func linuxProcMem(pid int) (procMem, bool) {
	base := "/proc/" + strconv.Itoa(pid)
	var m procMem
	if comm, err := os.ReadFile(base + "/comm"); err == nil {
		m.name = strings.TrimSpace(string(comm))
	}
	if data, err := os.ReadFile(base + "/smaps_rollup"); err == nil {
		if rss, pss, ok := parseSmapsRollup(data); ok {
			m.rss, m.footprint = rss, pss
			return m, true
		}
	}
	data, err := os.ReadFile(base + "/statm")
	if err != nil {
		return procMem{}, false
	}
	pages, ok := parseStatmResident(data)
	if !ok {
		return procMem{}, false
	}
	m.rss = pages * pageSize
	return m, true
}

func linuxHasTaskChildren() bool {
	self := strconv.Itoa(os.Getpid())
	_, err := os.Stat("/proc/" + self + "/task/" + self + "/children")
	return err == nil
}

// linuxTaskChildren gathers the children of every thread of pid: a child is listed
// under the thread that forked it.
func linuxTaskChildren(pid int) []int {
	tasks, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/task")
	if err != nil {
		return nil
	}
	var out []int
	for _, t := range tasks {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "task", t.Name(), "children"))
		if err != nil {
			continue
		}
		out = append(out, parsePIDList(data)...)
	}
	return out
}

// linuxScanParents maps parent pid to children from every /proc/<pid>/stat.
func linuxScanParents() map[int][]int {
	byParent := map[int][]int{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return byParent
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue // exited since the listing
		}
		if ppid, ok := parseStatPPID(data); ok {
			byParent[ppid] = append(byParent[ppid], pid)
		}
	}
	return byParent
}

//go:build darwin

package budget

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS sampling uses the proc_info system call behind libproc, without cgo:
// proc_listchildpids for the tree and proc_pid_rusage (RUSAGE_INFO_V0) for
// ri_resident_size (what ps reports as RSS) and ri_phys_footprint. The command name
// comes from sysctl kern.proc.pid (p_comm).
const (
	procInfoCallListPIDs  = 1 // PROC_INFO_CALL_LISTPIDS
	procInfoCallPIDRusage = 9 // PROC_INFO_CALL_PIDRUSAGE
	procPPIDOnly          = 6 // PROC_PPID_ONLY
	rusageInfoV0          = 0 // RUSAGE_INFO_V0
)

// rusageInfoV0Struct mirrors struct rusage_info_v0 from <sys/resource.h>.
type rusageInfoV0Struct struct {
	UUID             [16]byte
	UserTime         uint64
	SystemTime       uint64
	PkgIdleWakeups   uint64
	InterruptWakeups uint64
	Pageins          uint64
	WiredSize        uint64
	ResidentSize     uint64
	PhysFootprint    uint64
	ProcStartAbstime uint64
	ProcExitAbstime  uint64
}

const treeBasis = "ps_rss+phys_footprint"

func sampleOwnTree() (TreeSample, error) {
	buf := make([]int32, 128)
	s, seen := walkTreeSeen(os.Getpid(), func(pid int) []int { return darwinChildPIDs(pid, buf) }, darwinProcMem)
	s.Basis = treeBasis
	if s.Processes == 0 {
		return TreeSample{At: s.At, Basis: treeBasis}, errNoRSSSampler
	}
	addShims(&s, seen, cachedShimPIDs(darwinShimPIDs), darwinProcMem)
	return s, nil
}

// darwinShimPIDs lists this user's processes named like the stdio shim (one sysctl).
func darwinShimPIDs() []int {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil
	}
	var out []int
	for i := range procs {
		if unix.ByteSliceToString(procs[i].Proc.P_comm[:]) == shimProcessName {
			out = append(out, int(procs[i].Proc.P_pid))
		}
	}
	return out
}

func darwinProcMem(pid int) (procMem, bool) {
	var ri rusageInfoV0Struct
	_, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, procInfoCallPIDRusage, uintptr(pid), rusageInfoV0, 0, uintptr(unsafe.Pointer(&ri)), 0)
	if errno != 0 {
		return procMem{}, false
	}
	m := procMem{rss: int64(ri.ResidentSize), footprint: int64(ri.PhysFootprint)}
	if kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid); err == nil {
		m.name = unix.ByteSliceToString(kp.Proc.P_comm[:])
	}
	return m, true
}

func darwinChildPIDs(pid int, buf []int32) []int {
	n, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, procInfoCallListPIDs, procPPIDOnly, uintptr(pid), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*4))
	if errno != 0 {
		return nil
	}
	count := min(int(n)/4, len(buf))
	out := make([]int, 0, count)
	for _, c := range buf[:count] {
		if c > 0 {
			out = append(out, int(c))
		}
	}
	return out
}

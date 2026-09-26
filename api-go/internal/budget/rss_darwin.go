//go:build darwin

package budget

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS sampling uses the proc_info system call behind libproc, without cgo:
// proc_listchildpids for the tree, proc_listpids(PROC_UID_ONLY) for the shims,
// proc_pid_rusage (RUSAGE_INFO_V0) for ri_resident_size (what ps reports as RSS) and
// ri_phys_footprint, and proc_pidinfo(PROC_PIDT_SHORTBSDINFO) for the command name
// (p_comm). Names are read into fixed buffers, so a sample allocates almost nothing.
const (
	procInfoCallListPIDs  = 1  // PROC_INFO_CALL_LISTPIDS
	procInfoCallPIDInfo   = 2  // PROC_INFO_CALL_PIDINFO
	procInfoCallPIDRusage = 9  // PROC_INFO_CALL_PIDRUSAGE
	procUIDOnly           = 4  // PROC_UID_ONLY
	procPPIDOnly          = 6  // PROC_PPID_ONLY
	procPIDTShortBSDInfo  = 13 // PROC_PIDT_SHORTBSDINFO
	rusageInfoV0          = 0  // RUSAGE_INFO_V0
)

// procBSDShortInfo mirrors struct proc_bsdshortinfo from <sys/proc_info.h>.
type procBSDShortInfo struct {
	PID, PPID, PGID, Status uint32
	Comm                    [16]byte // MAXCOMLEN
	Flags                   uint32
	UID, GID, RUID, RGID    uint32
	SVUID, SVGID, RFU       uint32
}

// darwinComm reads pid's short command name into c without allocating.
func darwinComm(pid int, c *procBSDShortInfo) bool {
	n, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, procInfoCallPIDInfo, uintptr(pid), procPIDTShortBSDInfo, 0,
		uintptr(unsafe.Pointer(c)), unsafe.Sizeof(*c))
	return errno == 0 && n == unsafe.Sizeof(*c)
}

func commString(c *procBSDShortInfo) string { return unix.ByteSliceToString(c.Comm[:]) }

// commIs compares a short name without converting it to a string.
func commIs(c *procBSDShortInfo, name string) bool {
	n := bytes.IndexByte(c.Comm[:], 0)
	if n < 0 {
		n = len(c.Comm)
	}
	return string(c.Comm[:n]) == name // no allocation: the conversion is only compared
}

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

// uidPIDs is the reusable buffer for this user's pid list (guarded by shimList).
var uidPIDs = make([]int32, 2048)

// darwinShimPIDs lists this user's processes named like the stdio shim: one
// proc_listpids for the user's pids, then one short-name read per pid.
func darwinShimPIDs() []int {
	var n int
	for {
		r, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, procInfoCallListPIDs, procUIDOnly, uintptr(os.Getuid()), 0,
			uintptr(unsafe.Pointer(&uidPIDs[0])), uintptr(len(uidPIDs)*4))
		if errno != 0 {
			return nil
		}
		n = min(int(r)/4, len(uidPIDs))
		if n < len(uidPIDs) || len(uidPIDs) >= 1<<16 {
			break
		}
		uidPIDs = make([]int32, 2*len(uidPIDs)) // the list filled the buffer: it may be cut short
	}
	var out []int
	var c procBSDShortInfo
	for _, pid := range uidPIDs[:n] {
		if pid > 0 && darwinComm(int(pid), &c) && commIs(&c, shimProcessName) {
			out = append(out, int(pid))
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
	var c procBSDShortInfo
	if darwinComm(pid, &c) {
		m.name = commString(&c)
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

func measureProcess(pid int) (procMem, bool) { return darwinProcMem(pid) }

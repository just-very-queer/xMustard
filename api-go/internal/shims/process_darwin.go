//go:build darwin

package shims

import (
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	callListPIDs  = 1
	callPIDInfo   = 2
	callPIDRusage = 9
	uidOnly       = 4
	shortBSDInfo  = 13
	rusageV0      = 0
)

type bsdShort struct {
	PID, PPID, PGID, Status                        uint32
	Comm                                           [16]byte
	Flags, UID, GID, RUID, RGID, SVUID, SVGID, RFU uint32
}
type rusage struct {
	UUID                                                                                       [16]byte
	UserTime, SystemTime, PkgIdle, Interrupt, Pageins, Wired, Resident, Footprint, Start, Exit uint64
}
type procBSD struct {
	Flags, Status, XStatus, PID, PPID, UID, GID, RUID, RGID, SVUID, SVGID, Reserved uint32
	Comm                                                                            [16]byte
	Name                                                                            [32]byte
	NFiles, Pgid, Pjobc, Tdev, Tpgid                                                uint32
	Nice                                                                            int32
	StartSec, StartUsec                                                             uint64
}

func readStableRusage(pid int, identify func(int) (Process, error), readUsage func(int) (rusage, error)) (Process, rusage, bool) {
	var usage rusage
	identity, stable := stableIdentitySample(pid, os.Getuid(), identify, func() error {
		var err error
		usage, err = readUsage(pid)
		return err
	})
	return identity, usage, stable
}

func processIdentity(pid int) (Process, error) {
	var b bsdShort
	got, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, callPIDInfo, uintptr(pid), shortBSDInfo, 0, uintptr(unsafe.Pointer(&b)), unsafe.Sizeof(b))
	if errno != 0 || got != unsafe.Sizeof(b) {
		if errno != 0 {
			return Process{}, errno
		}
		return Process{}, ErrProcessChanged
	}
	var info procBSD
	got, _, errno = syscall.Syscall6(unix.SYS_PROC_INFO, callPIDInfo, uintptr(pid), 3, 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if errno != 0 || got != unsafe.Sizeof(info) {
		if errno != 0 {
			return Process{}, errno
		}
		return Process{}, ErrProcessChanged
	}
	return Process{PID: pid, UID: int(b.UID), StartTime: info.StartSec*1_000_000 + info.StartUsec, Comm: unix.ByteSliceToString(b.Comm[:])}, nil
}

func signalProcess(expected Process) error {
	return signalIfIdentity(expected, processIdentity, func(pid int) error {
		// Darwin has no identity-bound kill; signalIfIdentity re-reads immediately before delivery.
		return syscall.Kill(pid, syscall.SIGKILL)
	})
}

func listPlatform() ([]Process, error) {
	pids := make([]int32, 4096)
	var n int
	for {
		count, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, callListPIDs, uidOnly, uintptr(os.Getuid()), 0, uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)*4))
		if errno != 0 {
			return nil, errno
		}
		n = min(int(count)/4, len(pids))
		if n < len(pids) || len(pids) >= 1<<16 {
			break
		}
		pids = make([]int32, len(pids)*2)
	}
	var out []Process
	for _, raw := range pids[:n] {
		if raw <= 0 {
			continue
		}
		pid := int(raw)
		var b bsdShort
		got, _, e := syscall.Syscall6(unix.SYS_PROC_INFO, callPIDInfo, uintptr(pid), shortBSDInfo, 0, uintptr(unsafe.Pointer(&b)), unsafe.Sizeof(b))
		if e != 0 || got != unsafe.Sizeof(b) || unix.ByteSliceToString(b.Comm[:]) != ProcessName {
			continue
		}
		var cpuAt time.Time
		after, r, stable := readStableRusage(pid, processIdentity, func(pid int) (rusage, error) {
			var usage rusage
			_, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, callPIDRusage, uintptr(pid), rusageV0, 0, uintptr(unsafe.Pointer(&usage)), 0)
			if errno != 0 {
				return rusage{}, errno
			}
			cpuAt = time.Now()
			return usage, nil
		})
		if !stable {
			continue
		}
		var finalBSD bsdShort
		got, _, e = syscall.Syscall6(unix.SYS_PROC_INFO, callPIDInfo, uintptr(pid), shortBSDInfo, 0, uintptr(unsafe.Pointer(&finalBSD)), unsafe.Sizeof(finalBSD))
		if e != 0 || got != unsafe.Sizeof(finalBSD) || int(finalBSD.UID) != after.UID || unix.ByteSliceToString(finalBSD.Comm[:]) != after.Comm {
			continue
		}
		start := time.Unix(int64(after.StartTime/1_000_000), int64(after.StartTime%1_000_000)*1000)
		age := time.Since(start)
		cpu := time.Duration(r.UserTime + r.SystemTime)
		out = append(out, Process{PID: pid, PPID: int(finalBSD.PPID), UID: after.UID, StartTime: after.StartTime, Age: age, AgeKnown: true, AgeSeconds: int64(age.Seconds()), CPUS: cpu, CPUAt: cpuAt, CPUSeconds: cpu.Seconds(), RSSBytes: int64(r.Resident), Comm: after.Comm})
	}
	return out, nil
}

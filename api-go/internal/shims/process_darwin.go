//go:build darwin

package shims

import (
	"errors"
	"fmt"
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

func stableRusageSample(pid int, identify func(int) (Process, error), readUsage func(int) (rusage, error)) (Process, rusage, error) {
	before, err := identify(pid)
	if err != nil {
		return Process{}, rusage{}, identitySampleError(pid, err)
	}
	if before.UID != os.Getuid() || before.Comm != ProcessName {
		return Process{}, rusage{}, ErrProcessChanged
	}
	usage, readErr := readUsage(pid)
	if readErr != nil {
		after, identifyErr := identify(pid)
		if identifyErr != nil {
			return Process{}, rusage{}, identitySampleError(pid, identifyErr)
		}
		if !sameIdentity(before, after) {
			return Process{}, rusage{}, ErrProcessChanged
		}
		return Process{}, rusage{}, fmt.Errorf("sample stdio shim %d: %w", pid, readErr)
	}
	after, err := identify(pid)
	if err != nil {
		return Process{}, rusage{}, identitySampleError(pid, err)
	}
	if !sameIdentity(before, after) {
		return Process{}, rusage{}, ErrProcessChanged
	}
	return after, usage, nil
}

func identitySampleError(pid int, err error) error {
	if errors.Is(err, ErrProcessChanged) || errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT) {
		return ErrProcessChanged
	}
	return fmt.Errorf("identify stdio shim %d: %w", pid, err)
}

func readProcessInfo(pid int, flavor uint32) (Process, error) {
	if flavor == shortBSDInfo {
		var info bsdShort
		got, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, callPIDInfo, uintptr(pid), uintptr(flavor), 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
		if errno != 0 {
			return Process{}, errno
		}
		if got != unsafe.Sizeof(info) || int(info.PID) != pid {
			return Process{}, ErrProcessChanged
		}
		return Process{PID: pid, PPID: int(info.PPID), UID: int(info.UID), Comm: unix.ByteSliceToString(info.Comm[:])}, nil
	}
	var info procBSD
	got, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, callPIDInfo, uintptr(pid), uintptr(flavor), 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if errno != 0 {
		return Process{}, errno
	}
	if got != unsafe.Sizeof(info) || int(info.PID) != pid {
		return Process{}, ErrProcessChanged
	}
	return Process{PID: pid, PPID: int(info.PPID), UID: int(info.UID), StartTime: info.StartSec*1_000_000 + info.StartUsec, Comm: unix.ByteSliceToString(info.Comm[:])}, nil
}

var readBSDProcessInfo = readProcessInfo

func processIdentity(pid int) (Process, error) {
	return readBSDProcessInfo(pid, 3)
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
	return listDarwinProcesses(pids[:n], readRusage, time.Now)
}

func readRusage(pid int) (rusage, error) {
	var usage rusage
	_, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, callPIDRusage, uintptr(pid), rusageV0, 0, uintptr(unsafe.Pointer(&usage)), 0)
	if errno != 0 {
		return rusage{}, errno
	}
	return usage, nil
}

func listDarwinProcesses(pids []int32, readUsage func(int) (rusage, error), now func() time.Time) ([]Process, error) {
	var out []Process
	for _, raw := range pids {
		if raw <= 0 {
			continue
		}
		pid := int(raw)
		short, err := readBSDProcessInfo(pid, shortBSDInfo)
		if err != nil || short.Comm != ProcessName {
			continue
		}
		cpuAt := time.Time{}
		after, usage, sampleErr := stableRusageSample(pid, processIdentity, func(pid int) (rusage, error) {
			r, err := readUsage(pid)
			if err == nil {
				cpuAt = now()
			}
			return r, err
		})
		if errors.Is(sampleErr, ErrProcessChanged) {
			continue
		}
		if sampleErr != nil {
			return nil, sampleErr
		}
		start := time.Unix(int64(after.StartTime/1_000_000), int64(after.StartTime%1_000_000)*1000)
		age := now().Sub(start)
		cpu := time.Duration(usage.UserTime + usage.SystemTime)
		out = append(out, Process{PID: pid, PPID: after.PPID, UID: after.UID, StartTime: after.StartTime, Age: age, AgeKnown: true, AgeSeconds: int64(age.Seconds()), CPUS: cpu, CPUAt: cpuAt, CPUSeconds: cpu.Seconds(), RSSBytes: int64(usage.Resident), Comm: after.Comm})
	}
	return out, nil
}

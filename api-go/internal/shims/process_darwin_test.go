//go:build darwin

package shims

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestDarwinRusageFailureForStableShimIsUnavailable(t *testing.T) {
	identity := Process{PID: 43, UID: os.Getuid(), StartTime: 100, PPID: 7, Comm: ProcessName}
	got, _, err := stableRusageSample(identity.PID, func(int) (Process, error) {
		return identity, nil
	}, func(int) (rusage, error) {
		return rusage{}, syscall.EPERM
	})
	if !errors.Is(err, syscall.EPERM) || errors.Is(err, ErrProcessChanged) || got.PID != 0 {
		t.Fatalf("stable shim sampling failure = identity %+v, err %v", got, err)
	}
}

func TestDarwinRusageFailureAfterDisappearanceIsIdentityChange(t *testing.T) {
	identity := Process{PID: 44, UID: os.Getuid(), StartTime: 100, Comm: ProcessName}
	calls := 0
	_, _, err := stableRusageSample(identity.PID, func(int) (Process, error) {
		calls++
		if calls == 1 {
			return identity, nil
		}
		return Process{}, syscall.ESRCH
	}, func(int) (rusage, error) {
		return rusage{}, syscall.EPERM
	})
	if !errors.Is(err, ErrProcessChanged) || errors.Is(err, syscall.EPERM) {
		t.Fatalf("disappearance classification = %v, want ErrProcessChanged only", err)
	}
}

func TestDarwinIdentitySamplingFailureIsUnavailable(t *testing.T) {
	identity := Process{PID: 46, UID: os.Getuid(), StartTime: 100, Comm: ProcessName}
	calls := 0
	_, _, err := stableRusageSample(identity.PID, func(int) (Process, error) {
		calls++
		if calls == 1 {
			return identity, nil
		}
		return Process{}, syscall.EPERM
	}, func(int) (rusage, error) {
		return rusage{}, nil
	})
	if !errors.Is(err, syscall.EPERM) || errors.Is(err, ErrProcessChanged) {
		t.Fatalf("identity sampling failure = %v, want unavailable EPERM", err)
	}
}

func TestDarwinEnumerationRejectsReplacementAtFinalBSDRead(t *testing.T) {
	original := Process{PID: 45, UID: os.Getuid(), StartTime: 100_000_000, PPID: 7, Comm: ProcessName}
	replacement := Process{PID: 45, UID: os.Getuid(), StartTime: 200_000_000, PPID: 9, Comm: ProcessName}
	reads := 0
	previous := readBSDProcessInfo
	readBSDProcessInfo = func(_ int, flavor uint32) (Process, error) {
		if flavor == shortBSDInfo {
			return original, nil
		}
		reads++
		if reads < 3 {
			return original, nil
		}
		return replacement, nil
	}
	t.Cleanup(func() { readBSDProcessInfo = previous })
	got, err := listDarwinProcesses([]int32{45}, func(int) (rusage, error) {
		return rusage{}, nil
	}, func() time.Time { return time.Unix(300, 0) })
	if err != nil || len(got) != 1 {
		t.Fatalf("enumeration = %+v, err %v", got, err)
	}
	if got[0].PPID != original.PPID || got[0].StartTime != original.StartTime || got[0].PPID == 0 {
		t.Fatalf("replacement at final BSD read mixed identities: %+v", got[0])
	}
}

func TestDarwinEnumerationUsesFinalCheckedBSDRecord(t *testing.T) {
	checked := Process{PID: 47, UID: os.Getuid(), StartTime: 100_000_000, PPID: 17, Comm: ProcessName}
	previous := readBSDProcessInfo
	readBSDProcessInfo = func(_ int, _ uint32) (Process, error) { return checked, nil }
	t.Cleanup(func() { readBSDProcessInfo = previous })
	got, err := listDarwinProcesses([]int32{47}, func(int) (rusage, error) {
		return rusage{}, nil
	}, func() time.Time { return time.Unix(300, 0) })
	if err != nil || len(got) != 1 {
		t.Fatalf("enumeration = %+v, err %v", got, err)
	}
	if got[0].PPID != checked.PPID || got[0].StartTime != checked.StartTime || got[0].PPID == 0 {
		t.Fatalf("PPID/start time did not come from the same checked BSD record: %+v", got[0])
	}
}

func TestDarwinEnumerationRejectsPIDReuseDuringRusage(t *testing.T) {
	original := Process{PID: 42, UID: os.Getuid(), StartTime: 100, Comm: ProcessName}
	replacement := Process{PID: 42, UID: os.Getuid(), StartTime: 200, Comm: ProcessName}
	identities := 0
	got, _, err := stableRusageSample(original.PID, func(int) (Process, error) {
		identities++
		if identities == 1 {
			return original, nil
		}
		return replacement, nil
	}, func(int) (rusage, error) {
		return rusage{UserTime: uint64(301 * time.Second)}, nil
	})
	if !errors.Is(err, ErrProcessChanged) || got.PID != 0 {
		t.Fatalf("reused PID sample accepted: err=%v identity=%+v", err, got)
	}
}

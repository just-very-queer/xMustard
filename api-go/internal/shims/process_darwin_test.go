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

func TestDarwinStableRusageReturnsPPIDFromCheckedIdentity(t *testing.T) {
	before := Process{PID: 45, UID: os.Getuid(), StartTime: 100, PPID: 7, Comm: ProcessName}
	after := before
	after.PPID = 9
	calls := 0
	got, _, err := stableRusageSample(before.PID, func(int) (Process, error) {
		calls++
		if calls == 1 {
			return before, nil
		}
		return after, nil
	}, func(int) (rusage, error) {
		return rusage{}, nil
	})
	if err != nil || got.PPID != after.PPID {
		t.Fatalf("identity-stable PPID snapshot = %+v, err %v", got, err)
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

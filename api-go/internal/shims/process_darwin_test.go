//go:build darwin

package shims

import (
	"os"
	"testing"
	"time"
)

func TestDarwinEnumerationRejectsPIDReuseDuringRusage(t *testing.T) {
	original := Process{PID: 42, UID: os.Getuid(), StartTime: 100, Comm: ProcessName}
	replacement := Process{PID: 42, UID: os.Getuid(), StartTime: 200, Comm: ProcessName}
	identities := 0
	got, _, stable := readStableRusage(original.PID, func(int) (Process, error) {
		identities++
		if identities == 1 {
			return original, nil
		}
		return replacement, nil
	}, func(int) (rusage, error) {
		return rusage{UserTime: uint64(301 * time.Second)}, nil
	})
	if stable || got.PID != 0 {
		t.Fatalf("reused PID sample accepted: stable=%v identity=%+v", stable, got)
	}
}

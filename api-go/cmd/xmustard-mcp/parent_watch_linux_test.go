//go:build linux

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestParentThreadDeathSignalDoesNotStopLiveProcess(t *testing.T) {
	called := make(chan struct{}, 1)
	stop := watchParentDeathSignal(os.Getppid(), func() { called <- struct{}{} })
	defer stop()
	defer syscall.Syscall6(syscall.SYS_PRCTL, 1, 0, 0, 0, 0, 0)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
		t.Fatal("thread-death notification stopped a process whose parent is still alive")
	case <-time.After(100 * time.Millisecond):
	}
}

//go:build linux

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// watchParentDeathSignal asks the kernel to deliver SIGTERM if the starting parent
// exits. The caller also runs the bounded ppid poll as a fallback and to cover the
// race where the parent exits before PR_SET_PDEATHSIG takes effect.
func watchParentDeathSignal(startPPID int, onDeath func()) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 1, uintptr(syscall.SIGTERM), 0, 0, 0, 0)
	if errno != 0 {
		signal.Stop(signals)
		return func() {}
	}
	if os.Getppid() != startPPID {
		onDeath()
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-signals:
			onDeath()
		case <-done:
		}
	}()
	return func() {
		close(done)
		signal.Stop(signals)
	}
}

//go:build linux

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// watchParentDeathSignal asks the kernel for a notification when the creating thread
// exits. A signal is only treated as parent death after confirming process-level reparenting;
// the bounded ppid poll covers thread-only notifications and the install race.
func watchParentDeathSignal(startPPID int, onDeath func()) func() {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGUSR1)
	_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 1, uintptr(syscall.SIGUSR1), 0, 0, 0, 0)
	useDeathSignal := errno == 0
	if useDeathSignal && os.Getppid() != startPPID {
		onDeath()
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-signals:
				if sig == syscall.SIGTERM || (useDeathSignal && os.Getppid() != startPPID) {
					onDeath()
					return
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		signal.Stop(signals)
	}
}

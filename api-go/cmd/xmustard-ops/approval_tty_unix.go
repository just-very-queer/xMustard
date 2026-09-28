//go:build darwin || linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// readTerminalSecret prompts on the controlling terminal and reads one line with echo
// off, so the token does not have to sit in a file or the environment. That is all it
// proves: a process that knows the token can type it into a pseudo-terminal too, which
// is why only a presence-only token, refused everywhere else, records user_presence.
// Without a controlling terminal it fails: pass --token-file or XMUSTARD_APPROVER_TOKEN
// instead (both advisory). An interrupt, quit, hangup or terminate signal while the
// prompt is open restores the terminal before the process exits.
func readTerminalSecret(label string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("no controlling terminal to type the approver token (%v); pass --token-file or XMUSTARD_APPROVER_TOKEN, which are advisory", err)
	}
	defer tty.Close()
	fd := int(tty.Fd())
	saved, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return "", fmt.Errorf("read the terminal mode: %w", err)
	}
	var once sync.Once
	restore := func() {
		once.Do(func() {
			_ = unix.IoctlSetTermios(fd, ioctlSetTermios, saved)
			_, _ = tty.WriteString("\n")
		})
	}
	stop := restoreOnSignal(restore)
	defer stop()
	quiet := *saved
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &quiet); err != nil {
		return "", fmt.Errorf("turn terminal echo off: %w", err)
	}
	defer restore()
	if _, err := tty.WriteString(label); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(io.LimitReader(tty, maxTokenFileBytes)).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no approver token was typed")
	}
	return strings.TrimSpace(line), nil
}

// restoreOnSignal runs restore and exits 130 when a terminating signal arrives before
// the returned stop is called; Go's default handling would exit without running defers
// and leave the terminal with echo off.
func restoreOnSignal(restore func()) (stop func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT, syscall.SIGHUP, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			restore()
			os.Exit(130)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sigs)
		close(done)
	}
}

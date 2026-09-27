//go:build darwin || linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// readTerminalSecret prompts on the controlling terminal and reads one line with echo
// off, so the token is typed by the human and never stored where an agent process
// could read it. Without a controlling terminal it fails: pass --token-file or
// XMUSTARD_APPROVER_TOKEN instead (both advisory).
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
	quiet := *saved
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &quiet); err != nil {
		return "", fmt.Errorf("turn terminal echo off: %w", err)
	}
	defer func() {
		_ = unix.IoctlSetTermios(fd, ioctlSetTermios, saved)
		_, _ = tty.WriteString("\n")
	}()
	if _, err := tty.WriteString(label); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(io.LimitReader(tty, maxTokenFileBytes)).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no approver token was typed")
	}
	return strings.TrimSpace(line), nil
}

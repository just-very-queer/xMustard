//go:build cgo && (darwin || linux)

package workspaceops

/*
#cgo linux LDFLAGS: -lutil
#if defined(__APPLE__)
#include <util.h>
#elif defined(__linux__)
#include <pty.h>
#endif
*/
import "C"

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func openTerminalPTY(cols int, rows int) (*os.File, *os.File, error) {
	var primaryFD C.int
	var replicaFD C.int
	windowSize := C.struct_winsize{
		ws_col: C.ushort(normalizeTerminalDimension(cols, 80)),
		ws_row: C.ushort(normalizeTerminalDimension(rows, 24)),
	}
	if rv, err := C.openpty(&primaryFD, &replicaFD, nil, nil, &windowSize); rv != 0 {
		return nil, nil, err
	}
	primary := os.NewFile(uintptr(primaryFD), "pty-primary")
	replica := os.NewFile(uintptr(replicaFD), "pty-replica")
	if primary == nil || replica == nil {
		if primary != nil {
			_ = primary.Close()
		}
		if replica != nil {
			_ = replica.Close()
		}
		return nil, nil, errors.New("openpty returned invalid file handles")
	}
	return primary, replica, nil
}

func configureTerminalCommand(cmd *exec.Cmd, replica *os.File) {
	cmd.Stdin = replica
	cmd.Stdout = replica
	cmd.Stderr = replica
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
}

func resizeTerminalPTY(handle *os.File, cols int, rows int) error {
	if handle == nil {
		return os.ErrClosed
	}
	return unix.IoctlSetWinsize(
		int(handle.Fd()),
		unix.TIOCSWINSZ,
		&unix.Winsize{
			Col: uint16(normalizeTerminalDimension(cols, 80)),
			Row: uint16(normalizeTerminalDimension(rows, 24)),
		},
	)
}

func terminateTerminalProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	for _, childPID := range collectTerminalChildPIDs(cmd.Process.Pid) {
		if pgid, err := unix.Getpgid(childPID); err == nil {
			_ = unix.Kill(-pgid, unix.SIGTERM)
		}
		_ = unix.Kill(childPID, unix.SIGTERM)
	}
	pgid, err := unix.Getpgid(cmd.Process.Pid)
	if err == nil {
		_ = unix.Kill(-pgid, unix.SIGTERM)
		return
	}
	if errors.Is(err, unix.ESRCH) {
		return
	}
	_ = cmd.Process.Signal(unix.SIGTERM)
}

func collectTerminalChildPIDs(parentPID int) []int {
	output, err := exec.Command("pgrep", "-P", strconv.Itoa(parentPID)).Output()
	if err != nil {
		return nil
	}
	pids := []int{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			continue
		}
		pids = append(pids, collectTerminalChildPIDs(pid)...)
		pids = append(pids, pid)
	}
	return pids
}

func normalizeTerminalDimension(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

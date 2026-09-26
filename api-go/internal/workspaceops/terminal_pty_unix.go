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
	"runtime"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// openTerminalPTY returns the master and the replica. The replica's name is its
// device path, which teardown uses to find processes still holding it.
func openTerminalPTY(cols int, rows int) (*os.File, *os.File, error) {
	var primaryFD C.int
	var replicaFD C.int
	// openpty(3) copies the replica's path into name unbounded; device paths such
	// as /dev/pts/12 or /dev/ttys012 are far shorter.
	var name [256]C.char
	windowSize := C.struct_winsize{
		ws_col: C.ushort(normalizeTerminalDimension(cols, 80)),
		ws_row: C.ushort(normalizeTerminalDimension(rows, 24)),
	}
	// openpty(3) creates both descriptors without FD_CLOEXEC, and fork/exec passes
	// every unmarked descriptor to the child. Unmarked, each later shell inherited
	// the primary side of every open terminal, including its own, so closing our
	// copy never hung up the PTY and closed terminals' shells outlived the process.
	// ForkLock keeps any fork out of the gap between creating and marking them.
	syscall.ForkLock.RLock()
	rv, err := C.openpty(&primaryFD, &replicaFD, &name[0], nil, &windowSize)
	if rv == 0 {
		unix.CloseOnExec(int(primaryFD))
		unix.CloseOnExec(int(replicaFD))
	}
	syscall.ForkLock.RUnlock()
	if rv != 0 {
		return nil, nil, err
	}
	// On Linux a non-blocking master joins Go's poller, so closing it ends a read
	// that a process outside the session would otherwise keep blocked. On darwin
	// the session leader's exit revokes the terminal, which already ends the read,
	// so the master keeps its blocking descriptor there.
	if runtime.GOOS == "linux" {
		if err := unix.SetNonblock(int(primaryFD), true); err != nil {
			_ = unix.Close(int(primaryFD))
			_ = unix.Close(int(replicaFD))
			return nil, nil, err
		}
	}
	primary := os.NewFile(uintptr(primaryFD), "pty-primary")
	replica := os.NewFile(uintptr(replicaFD), C.GoString(&name[0]))
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

// configureTerminalCommand gives the shell the PTY as descriptors 0-2 and nothing
// else, and makes it the leader of a new session with the PTY as its controlling
// terminal, so teardown can find everything it starts by session id.
func configureTerminalCommand(cmd *exec.Cmd, replica *os.File) {
	cmd.Stdin = replica
	cmd.Stdout = replica
	cmd.Stderr = replica
	cmd.ExtraFiles = nil
	markInheritedDescriptorsCloseOnExec()
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
	// through SyscallConn, so a concurrent close cannot free the descriptor number
	// for reuse while the ioctl runs
	conn, err := handle.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlErr error
	err = conn.Control(func(fd uintptr) {
		ioctlErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{
			Col: uint16(normalizeTerminalDimension(cols, 80)),
			Row: uint16(normalizeTerminalDimension(rows, 24)),
		})
	})
	if err != nil {
		return err
	}
	return ioctlErr
}

// markInheritedDescriptorsCloseOnExec sets FD_CLOEXEC on every open descriptor
// above stderr that lacks it. Go opens its own descriptors close-on-exec, so these
// came from whatever started this process (or from cgo), and fork/exec would hand
// them to every child. ExtraFiles still works: dup2 into the child clears the flag.
func markInheritedDescriptorsCloseOnExec() {
	dir := "/dev/fd"
	if runtime.GOOS == "linux" {
		dir = "/proc/self/fd"
	}
	// Readdirnames, not ReadDir: ReadDir stats each entry, which fails on macOS's
	// fdesc for the listing's own descriptor and cuts the list short.
	handle, err := os.Open(dir)
	if err != nil {
		return
	}
	names, _ := handle.Readdirnames(-1)
	_ = handle.Close()
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil || fd <= 2 {
			continue
		}
		if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil && flags&unix.FD_CLOEXEC == 0 {
			unix.CloseOnExec(fd)
		}
	}
}

func normalizeTerminalDimension(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

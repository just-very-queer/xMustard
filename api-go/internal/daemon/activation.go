package daemon

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

// listenFDsStart is the first file descriptor systemd passes (SD_LISTEN_FDS_START).
const listenFDsStart = 3

// Activated returns the listening socket systemd passed this process (socket
// activation: LISTEN_PID names this process and LISTEN_FDS counts the sockets), or nil
// when there is none. It clears the LISTEN_* variables either way, so no child takes
// them for its own, and accepts exactly one TCP socket: the caller must still judge
// its address, which the socket unit chose, with the same startup rules as a bind.
func Activated() (net.Listener, error) {
	return activated(os.Getenv, os.Unsetenv, os.Getpid(), listenFDsStart)
}

func activated(getenv func(string) string, unsetenv func(string) error, pid, fd int) (net.Listener, error) {
	owner, count := getenv("LISTEN_PID"), getenv("LISTEN_FDS")
	for _, k := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		_ = unsetenv(k)
	}
	if owner == "" || owner != strconv.Itoa(pid) {
		return nil, nil // no activation, or one meant for another process
	}
	if count != "1" {
		return nil, fmt.Errorf("socket activation passed LISTEN_FDS=%q; the socket unit must listen on exactly one address", count)
	}
	f := os.NewFile(uintptr(fd), "systemd-socket")
	defer f.Close() // FileListener holds its own duplicate
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("socket activation: fd %d is not a listening socket: %w", fd, err)
	}
	if _, ok := ln.Addr().(*net.TCPAddr); !ok {
		_ = ln.Close()
		return nil, fmt.Errorf("socket activation passed a %s socket; the daemon serves TCP only", ln.Addr().Network())
	}
	return ln, nil
}

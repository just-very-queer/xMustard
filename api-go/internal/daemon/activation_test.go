//go:build darwin || linux

package daemon

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestActivatedAdoptsOnlyItsOwnSocket(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	unsetenv := func(k string) error { delete(env, k); return nil }
	set := func(kv ...string) {
		for i := 0; i < len(kv); i += 2 {
			env[kv[i]] = kv[i+1]
		}
	}

	if ln, err := activated(getenv, unsetenv, 100, 99); ln != nil || err != nil {
		t.Fatalf("no activation: %v %v", ln, err)
	}
	set("LISTEN_PID", "7", "LISTEN_FDS", "1", "LISTEN_FDNAMES", "x")
	if ln, err := activated(getenv, unsetenv, 100, 99); ln != nil || err != nil || len(env) != 0 {
		t.Fatalf("another process's activation must be ignored and cleared: %v %v %v", ln, err, env)
	}
	set("LISTEN_PID", "100", "LISTEN_FDS", "2")
	if _, err := activated(getenv, unsetenv, 100, 99); err == nil {
		t.Fatal("two sockets accepted")
	}

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	set("LISTEN_PID", "100", "LISTEN_FDS", "1")
	ln, err := activated(getenv, unsetenv, 100, passedFD(t, tcp))
	if err != nil || ln.Addr().String() != tcp.Addr().String() || len(env) != 0 {
		t.Fatalf("activated = %v %v (env %v), want the socket on %s", ln, err, env, tcp.Addr())
	}
	go func() {
		if c, err := net.Dial("tcp", tcp.Addr().String()); err == nil {
			_ = c.Close()
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept on the adopted socket: %v", err)
	}
	_ = c.Close()
	_ = ln.Close()

	unix, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Skip("no unix sockets here")
	}
	defer unix.Close()
	set("LISTEN_PID", "100", "LISTEN_FDS", "1")
	if _, err := activated(getenv, unsetenv, 100, passedFD(t, unix)); err == nil || !strings.Contains(err.Error(), "TCP only") {
		t.Fatalf("a unix socket: %v", err)
	}
}

// passedFD is a descriptor of ln's socket that only the code under test owns and
// closes, as systemd's fd 3 is.
func passedFD(t *testing.T, ln net.Listener) int {
	t.Helper()
	f, err := ln.(interface{ File() (*os.File, error) }).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

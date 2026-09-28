//go:build darwin || linux

package daemon

import (
	"os"

	"golang.org/x/sys/unix"
)

// redirectStdio points file descriptors 1 and 2 at f, so what the runtime itself writes
// there (a panic, a fatal error) goes to the current log.
func redirectStdio(f *os.File) error {
	for _, fd := range []int{1, 2} {
		if err := unix.Dup2(int(f.Fd()), fd); err != nil {
			return err
		}
	}
	return nil
}

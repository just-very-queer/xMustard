//go:build !cgo || (!darwin && !linux)

package workspaceops

import (
	"errors"
	"os"
	"os/exec"
)

var errTerminalPTYUnsupported = errors.New("pty terminals require cgo on darwin or linux")

func openTerminalPTY(cols int, rows int) (*os.File, *os.File, error) {
	return nil, nil, errTerminalPTYUnsupported
}

func configureTerminalCommand(cmd *exec.Cmd, replica *os.File) {}

func resizeTerminalPTY(handle *os.File, cols int, rows int) error {
	return errTerminalPTYUnsupported
}

func terminateTerminalProcess(cmd *exec.Cmd) {}

//go:build !darwin && !linux

package shims

import "errors"

func processIdentity(pid int) (Process, error) {
	return Process{}, errors.New("stdio shim process identity is unsupported on this platform")
}

func signalProcess(expected Process) error {
	return errors.New("stdio shim process signaling is unsupported on this platform")
}

func listPlatform() ([]Process, error) {
	return nil, errors.New("stdio shim process listing is unsupported on this platform")
}

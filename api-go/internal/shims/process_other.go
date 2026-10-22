//go:build !darwin && !linux

package shims

import "errors"

func listPlatform() ([]Process, error) {
	return nil, errors.New("stdio shim process listing is unsupported on this platform")
}

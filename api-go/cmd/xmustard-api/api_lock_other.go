//go:build !unix

package main

import (
	"errors"
	"path/filepath"
)

var errAPIAlreadyRunning = errors.New("another instance owns this data directory")

func canonicalDataDir(dataDir string) (string, error) {
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	resolvedDataDir, err := filepath.EvalSymlinks(absDataDir)
	if err != nil {
		return absDataDir, nil
	}
	return resolvedDataDir, nil
}

func lockAPIDataDir(string) (func(int) error, func(), error) {
	return func(int) error { return nil }, func() {}, nil
}

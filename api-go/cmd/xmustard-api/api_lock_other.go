//go:build !unix

package main

import "errors"

var errAPIAlreadyRunning = errors.New("another instance owns this data directory")

func lockAPIDataDir(string) (func(), error) { return func() {}, nil }

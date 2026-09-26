//go:build !darwin

package main

import "errors"

// cloneDir is macOS only; elsewhere the judge copy is made file by file.
func cloneDir(string, string) error { return errors.ErrUnsupported }

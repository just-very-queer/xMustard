//go:build !linux

package main

func watchParentDeathSignal(_ int, _ func()) func() { return func() {} }

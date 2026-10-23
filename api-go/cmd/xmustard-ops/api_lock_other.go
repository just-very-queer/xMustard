//go:build !unix

package main

func dataDirLockHeld(string) bool { return false }

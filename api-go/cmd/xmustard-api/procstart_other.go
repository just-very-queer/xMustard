//go:build !linux && !darwin

package main

func procStartTime(int) (int64, bool) { return 1, true }

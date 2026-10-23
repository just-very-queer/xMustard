//go:build !linux && !darwin

package main

func apiProcessIdentity(int) (int64, string, bool) { return 0, "", false }

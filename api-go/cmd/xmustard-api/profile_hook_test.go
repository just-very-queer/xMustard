//go:build profile

package main

import "testing"

func TestProfileHookAcceptsOnlyLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:6060": true, "[::1]:6060": true,
		"0.0.0.0:6060": false, ":6060": false, "localhost:6060": false, "192.168.1.2:6060": false, "garbage": false,
	} {
		if got := loopbackAddr(addr); got != want {
			t.Errorf("loopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

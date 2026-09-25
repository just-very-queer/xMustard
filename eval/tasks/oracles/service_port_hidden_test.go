package server

import "testing"

func TestHiddenAgreedPort(t *testing.T) {
	if DefaultPort != 8081 {
		t.Fatalf("DefaultPort = %d, want the agreed 8081", DefaultPort)
	}
	if got := Addr(0); got != ":8081" {
		t.Fatalf("Addr(0) = %q, want :8081", got)
	}
}

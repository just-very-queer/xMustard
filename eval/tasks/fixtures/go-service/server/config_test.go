package server

import "testing"

func TestAddrUsesDefault(t *testing.T) {
	if got := Addr(0); got != ":"+itoa(DefaultPort) {
		t.Fatalf("Addr(0) = %q", got)
	}
	if got := Addr(9000); got != ":9000" {
		t.Fatalf("Addr(9000) = %q", got)
	}
}

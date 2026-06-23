package main

import (
	"os"
	"strings"
	"testing"
)

// The wording-coupled HTTP-status classifiers — strings.Contains(err.Error(), ...) —
// are being migrated onto the typed DomainError + respondError mapper (XM-PRO-013).
// This guard asserts the count does NOT grow: a new handler must use a typed
// DomainError + respondError, not substring matching on an error message. The ceiling
// ratchets down as the remaining sites are migrated.
func TestErrorClassifierCountDoesNotGrow(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count(string(src), "strings.Contains(strings.ToLower(err.Error())")
	const ceiling = 22
	if n > ceiling {
		t.Fatalf("err.Error() substring status-classifiers grew to %d (ceiling %d) — "+
			"new handlers must use a typed DomainError + respondError, not substring matching", n, ceiling)
	}
}

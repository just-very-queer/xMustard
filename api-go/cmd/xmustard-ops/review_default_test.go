//go:build !review

package main

import (
	"strings"
	"testing"
)

// Review anchoring (WS-65) is built only with the review build tag; the default build
// has no `review anchor`.
func TestReviewAnchorIsOffInTheDefaultBuild(t *testing.T) {
	if _, ok := reviewCommands["anchor"]; ok {
		t.Fatal("review anchor is registered without the review build tag")
	}
	e, _, errOut := testEnv(nil, "")
	if code := runOps(e, "review", "anchor", "ws", "--base", "main", "--findings", "f"); code != exitUsage ||
		!strings.Contains(errOut.String(), "<approve|gate|revoke>") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

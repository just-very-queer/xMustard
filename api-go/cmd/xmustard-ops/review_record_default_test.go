//go:build !review

package main

import (
	"strings"
	"testing"
)

// Review records (WS-66) are built only with the review build tag: the default build has
// no `review record`, `review show` or `review triage`.
func TestReviewRecordIsOffInTheDefaultBuild(t *testing.T) {
	for _, name := range []string{"record", "show", "triage"} {
		if _, ok := reviewCommands[name]; ok {
			t.Fatalf("review %s is registered without the review build tag", name)
		}
	}
	e, _, errOut := testEnv(nil, "")
	if code := runOps(e, "review", "record", "ws", "--base", "main"); code != exitUsage ||
		!strings.Contains(errOut.String(), "<approve|gate|revoke>") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

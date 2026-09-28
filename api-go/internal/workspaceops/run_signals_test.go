package workspaceops

import (
	"reflect"
	"strings"
	"testing"
)

func TestDetectPatchIssuesSignals(t *testing.T) {
	one, zero := 1, 0
	long := strings.Repeat("e", 250)
	blank := "   "
	cases := []struct {
		name   string
		run    runRecord
		output string
		want   []string
	}{
		{"clean", runRecord{ExitCode: &zero}, "patched 3 files", []string{}},
		{"empty output", runRecord{}, " \n\t", []string{"Empty output - no changes generated"}},
		{"exit and error", runRecord{ExitCode: &one, Error: &long}, "done", []string{
			"Run exited with non-zero code: 1", "Run had error: " + long[:200]}},
		{"blank error", runRecord{Error: &blank}, "done", []string{}},
		{"every marker", runRecord{}, "Traceback ...\nValueError exception\nPANIC: x\nSegmentation fault (core dumped)", []string{
			"Python traceback found in output", "Uncaught exception detected in output", "Panic detected in output", "Segmentation fault detected"}},
		{"caught exception", runRecord{}, "Exception was caught and logged", []string{}},
		{"segfault spelling", runRecord{}, "process segfault at 0x0", []string{"Segmentation fault detected"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectPatchIssues(&c.run, c.output); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

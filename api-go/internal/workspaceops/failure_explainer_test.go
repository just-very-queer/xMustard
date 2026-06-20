package workspaceops

import (
	"strings"
	"testing"
)

func TestSalientErrorLinesAndPathIntersection(t *testing.T) {
	output := strings.Join([]string{
		"compiling project",
		"api-go/internal/foo.go:42: undefined: Bar",
		"note: just a note",
		"FAILED build",
	}, "\n")

	lines := salientErrorLines(output, 8)
	if len(lines) != 2 {
		t.Fatalf("expected 2 salient error lines, got %d: %v", len(lines), lines)
	}

	// the changed file named in the output is implicated; the other is not.
	hits := intersectMentionedPaths(output, []string{"api-go/internal/foo.go", "unrelated/baz.go"})
	if len(hits) != 1 || hits[0] != "api-go/internal/foo.go" {
		t.Fatalf("expected foo.go implicated, got %v", hits)
	}
}

func TestSummarizeFailureMentionsImplicatedPath(t *testing.T) {
	code := 1
	exp := &FailureExplanation{
		Failed:          true,
		ExitCode:        &code,
		Signals:         []string{"Run exited with non-zero code: 1"},
		ImplicatedPaths: []string{"api-go/internal/foo.go"},
	}
	s := summarizeFailure(exp)
	if !strings.Contains(s, "foo.go") || !strings.Contains(s, "exit 1") {
		t.Fatalf("summary should name the exit and the implicated file: %q", s)
	}
}

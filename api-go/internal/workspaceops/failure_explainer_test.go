package workspaceops

import (
	"slices"
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
	hits := implicatedBy(mentionedPaths(output), "", []string{"api-go/internal/foo.go", "unrelated/baz.go"})
	if len(hits) != 1 || hits[0] != "api-go/internal/foo.go" {
		t.Fatalf("expected foo.go implicated, got %v", hits)
	}
}

// A path is implicated by its whole repo-relative form, a trailing part of it (a
// package-relative name, a bare file name), or an absolute path under the root.
func TestImplicatedByMatchesTrailingAndAbsolutePaths(t *testing.T) {
	changed := []string{"api-go/internal/foo.go", "web/src/app.ts", "docs/readme.md"}
	output := "internal/foo.go:3: undefined: X\n/repo/web/src/app.ts(4,2): error TS2304\nsee https://example.com/readme.md\n"
	hits := implicatedBy(mentionedPaths(output), "/repo", changed)
	if want := []string{"api-go/internal/foo.go", "web/src/app.ts"}; !slices.Equal(hits, want) {
		t.Fatalf("implicated = %v, want %v (a URL is not a path)", hits, want)
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

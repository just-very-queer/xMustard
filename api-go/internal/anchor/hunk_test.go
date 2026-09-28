// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors
// Copyright 2026 xMustard contributors
//
// Translated from open-code-review@486022d internal/diff/hunk_test.go and the ParseHunks
// and splitDiffLines cases of internal/diff/crlf_test.go. Changes by xMustard
// contributors: table form; the CRLF hunk has a well-formed header and no longer ends
// with an empty context line; a case for text after a complete hunk.

package anchor

import (
	"slices"
	"testing"
)

func TestParseHunks(t *testing.T) {
	kinds := func(h Hunk) []LineKind {
		var out []LineKind
		for _, l := range h.Lines {
			out = append(out, l.Kind)
		}
		return out
	}
	contents := func(h Hunk) []string {
		var out []string
		for _, l := range h.Lines {
			out = append(out, l.Content)
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		diff  string
		heads [][4]int // old start, old count, new start, new count per hunk
		kinds [][]LineKind
		lines [][]string
	}{
		{
			name: "single hunk",
			diff: "diff --git a/pkg/example/handler.go b/pkg/example/handler.go\n--- a/pkg/example/handler.go\n+++ b/pkg/example/handler.go\n" +
				"@@ -10,7 +10,7 @@ func HandleRequest(w http.ResponseWriter, r *http.Request) {\n" +
				"     ctx := r.Context()\n-    log.Print(\"handling request\")\n+    log.Printf(\"handling request: %s\", r.URL.Path)\n     err := process(ctx)",
			heads: [][4]int{{10, 7, 10, 7}},
			kinds: [][]LineKind{{Context, Deleted, Added, Context}},
		},
		{
			name: "multiple hunks",
			diff: "@@ -10,3 +10,3 @@ func foo() {\n     a := 1\n-    b := 2\n+    b := 3\n     c := 4\n" +
				"@@ -25,6 +25,8 @@ func bar() {\n     if err != nil {\n         return err\n     }\n+    log.Print(\"ok\")\n+    log.Print(\"done\")\n     return nil",
			heads: [][4]int{{10, 3, 10, 3}, {25, 6, 25, 8}},
		},
		{
			name:  "no-newline marker is skipped",
			diff:  "@@ -1,2 +1,2 @@\n-    old line\n\\ No newline at end of file\n+    new line",
			heads: [][4]int{{1, 2, 1, 2}},
			kinds: [][]LineKind{{Deleted, Added}},
		},
		{name: "empty input", diff: ""},
		{
			name:  "new file, all additions",
			diff:  "diff --git a/pkg/new.go b/pkg/new.go\nnew file mode 100644\n--- /dev/null\n+++ b/pkg/new.go\n@@ -0,0 +1,3 @@\n+package pkg\n+\n+func New() {}",
			heads: [][4]int{{0, 0, 1, 3}},
			kinds: [][]LineKind{{Added, Added, Added}},
		},
		{
			name:  "CRLF line content is clean",
			diff:  "diff --git a/x.go b/x.go\r\n--- a/x.go\r\n+++ b/x.go\r\n@@ -1,2 +1,2 @@\r\n kept\r\n+added\r\n-removed\r\n",
			heads: [][4]int{{1, 2, 1, 2}},
			lines: [][]string{{"kept", "added", "removed"}},
		},
		{
			name:  "text after a complete hunk is not read",
			diff:  "@@ -1 +1 @@\n-a\n+b\n\nnot a hunk line\n",
			heads: [][4]int{{1, 1, 1, 1}},
			lines: [][]string{{"a", "b"}},
		},
		{
			name:  "parsing stops at the next file",
			diff:  "@@ -1,2 +1,2 @@\n-a\n+b\ndiff --git a/y b/y\n+c",
			heads: [][4]int{{1, 2, 1, 2}},
			lines: [][]string{{"a", "b"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hunks := ParseHunks(tc.diff)
			if len(hunks) != len(tc.heads) {
				t.Fatalf("got %d hunks, want %d: %+v", len(hunks), len(tc.heads), hunks)
			}
			for i, h := range hunks {
				if got := [4]int{h.OldStart, h.OldCount, h.NewStart, h.NewCount}; got != tc.heads[i] {
					t.Errorf("hunk %d header = %v, want %v", i, got, tc.heads[i])
				}
				if tc.kinds != nil && !slices.Equal(kinds(h), tc.kinds[i]) {
					t.Errorf("hunk %d kinds = %v, want %v", i, kinds(h), tc.kinds[i])
				}
				if tc.lines != nil && !slices.Equal(contents(h), tc.lines[i]) {
					t.Errorf("hunk %d lines = %q, want %q", i, contents(h), tc.lines[i])
				}
			}
		})
	}
}

// LF text is untouched, and only a line's final CR is a terminator.
func TestSplitDiffLinesLeavesLFTextAlone(t *testing.T) {
	if got := splitDiffLines("alpha\nbeta\n"); !slices.Equal(got, []string{"alpha", "beta", ""}) {
		t.Errorf("got %q", got)
	}
	if got := splitDiffLines("a\rb\n"); got[0] != "a\rb" {
		t.Errorf("interior CR was stripped: %q", got[0])
	}
}

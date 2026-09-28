// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors
// Copyright 2026 xMustard contributors
//
// Translated from open-code-review@486022d internal/diff/resolver_test.go. Changes by
// xMustard contributors: table form over File.Resolve and Set.Place; expectations carry
// the status and side; "first match wins" is now ambiguous (nothing is guessed); an
// all-blank or absent snippet is refused by NewSnippet; the extractSideLines and
// matchConsecutive cases test File's sides and find.

package anchor

import (
	"slices"
	"testing"
)

const handlerDiff = "diff --git a/pkg/example/handler.go b/pkg/example/handler.go\n--- a/pkg/example/handler.go\n+++ b/pkg/example/handler.go\n" +
	"@@ -10,7 +10,7 @@ func HandleRequest(w http.ResponseWriter, r *http.Request) {\n" +
	"     ctx := r.Context()\n-    log.Print(\"handling request\")\n+    log.Printf(\"handling request: %s\", r.URL.Path)\n     err := process(ctx)"

// oneFile is a change of one file, test.go, with an optional head content.
func oneFile(diff, head string) []*File {
	return []*File{NewFile("test.go", "test.go", diff, func() string { return head })}
}

func mustSnippet(t *testing.T, code string) Snippet {
	t.Helper()
	s, err := NewSnippet(code)
	if err != nil {
		t.Fatalf("NewSnippet(%q): %v", code, err)
	}
	return s
}

// at is an anchored result on test.go.
func at(status Status, side Side, start, end int) Anchor {
	return Anchor{Path: "test.go", StartLine: start, EndLine: end, Side: side, Status: status}
}

func TestResolve(t *testing.T) {
	const blankHunk = "@@ -1,2 +1,2 @@\n-old\n+new"
	for _, tc := range []struct {
		name  string
		files []*File
		path  string
		code  string
		want  Anchor
	}{
		{"deleted line on the old side", []*File{NewFile("h.go", "h.go", handlerDiff, nil)}, "h.go", `    log.Print("handling request")`,
			Anchor{Path: "h.go", StartLine: 11, EndLine: 11, Side: Old, Status: ExactOld}},
		{"whitespace tolerant", []*File{NewFile("h.go", "h.go", handlerDiff, nil)}, "h.go", `log.Print("handling request")`,
			Anchor{Path: "h.go", StartLine: 11, EndLine: 11, Side: Old, Status: ExactOld}},
		{"multi-line on the old side", oneFile("@@ -5,4 +5,4 @@ import \"fmt\"\n func foo() {\n-    x := 1\n-    y := 2\n+    x := 10\n+    y := 20\n }", ""),
			"test.go", "    x := 1\n    y := 2", at(ExactOld, Old, 6, 7)},
		{"lines on the new side before the file", oneFile("@@ -1,3 +1,4 @@\n package main\n+import \"fmt\"\n func foo() {}", "package main\nimport \"fmt\"\nfunc foo() {}"),
			"test.go", "package main\nimport \"fmt\"", at(ExactNew, New, 1, 2)},
		{"file content across blank lines", oneFile(blankHunk, "package main\n\nfunc foo() {\n\n\treturn 1\n}"),
			"test.go", "func foo() {\n\treturn 1\n}", at(InFile, New, 3, 6)},
		{"file content across several blank lines", oneFile(blankHunk, "a\n\n\nb\n\nc\n"), "test.go", "a\nb\nc", at(InFile, New, 1, 6)},
		{"file content after leading blanks", oneFile(blankHunk, "\n\nfoo\nbar\n"), "test.go", "foo\nbar", at(InFile, New, 3, 4)},
		{"CRLF file content", oneFile(blankHunk, "alpha\r\nbeta\r\ngamma\r\n"), "test.go", "beta\ngamma", at(InFile, New, 2, 3)},
		{"two places are ambiguous, not the first", oneFile(blankHunk, "x\ny\nx\ny\n"), "test.go", "x\ny",
			Anchor{Path: "test.go", Status: Unanchored, Reason: ReasonAmbiguous, Candidates: 2}},
		{"no match", oneFile(handlerDiff, ""), "test.go", "totally unrelated code", Unplaced("test.go", ReasonNotFound)},
		{"path outside the change", oneFile(handlerDiff, ""), "missing.go", "some code", Unplaced("missing.go", ReasonNotFound)},
		{"added lines", oneFile("@@ -3,3 +3,5 @@\n func main() {\n+    x := 1\n+    y := 2\n     fmt.Println(\"hello\")\n }", ""),
			"test.go", "    x := 1\n    y := 2", at(ExactNew, New, 4, 5)},
		{"old side across an added line", oneFile("@@ -5,3 +5,4 @@\n     x := 1\n+    z := 99\n     y := 2\n }", ""),
			"test.go", "    x := 1\n    y := 2", at(ExactOld, Old, 5, 6)},
		{"context line", oneFile("@@ -3,3 +3,4 @@\n func main() {\n     fmt.Println(\"hello\")\n+    fmt.Println(\"world\")\n }", ""),
			"test.go", `    fmt.Println("hello")`, at(ExactNew, New, 4, 4)},
		{"single added line", oneFile("@@ -1,2 +1,3 @@\n package main\n+import \"fmt\"\n func main() {}", ""), "test.go", `import "fmt"`, at(ExactNew, New, 2, 2)},
		{"new side wins with new-file numbers", oneFile("@@ -5,3 +8,4 @@\n func main() {\n     fmt.Println(\"hello\")\n+    fmt.Println(\"world\")\n }", ""),
			"test.go", `    fmt.Println("hello")`, at(ExactNew, New, 9, 9)},
		{"second hunk", oneFile("@@ -2,3 +2,3 @@\n func foo() {\n-    old1()\n+    new1()\n }\n@@ -20,3 +20,4 @@\n func bar() {\n+    added_in_bar()\n     existing()\n }", ""),
			"test.go", "    added_in_bar()", at(ExactNew, New, 21, 21)},
		{"added lines with context", oneFile("@@ -10,3 +10,5 @@\n func process() {\n+    validate()\n+    transform()\n     save()\n }", ""),
			"test.go", "    validate()\n    transform()\n    save()", at(ExactNew, New, 11, 13)},
		{"new side across a deleted line", oneFile("@@ -5,4 +5,3 @@\n     a := 1\n-    unused := 0\n     b := 2\n }", ""),
			"test.go", "    a := 1\n    b := 2", at(ExactNew, New, 5, 6)},
		{"old path of a rename", []*File{NewFile("old_name.go", "new_name.go", "@@ -1,3 +1,3 @@\n package main\n-func oldFunc() {}\n+func newFunc() {}", nil)},
			"old_name.go", "func oldFunc() {}", Anchor{Path: "old_name.go", StartLine: 2, EndLine: 2, Side: Old, Status: ExactOld}},
		{"diff marker in the snippet", oneFile("@@ -1,2 +1,3 @@\n x := 1\n+y := 2\n z := 3", ""), "test.go", "+y := 2", at(ExactNew, New, 2, 2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewSet(tc.files).Place(tc.path, mustSnippet(t, tc.code)); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestResolveSeveralSnippetsOnOneFile(t *testing.T) {
	set := NewSet(oneFile("@@ -1,4 +1,6 @@\n package main\n+import \"fmt\"\n+import \"os\"\n func main() {\n-    old()\n+    new()\n }", ""))
	for code, want := range map[string]Anchor{
		`import "fmt"`: at(ExactNew, New, 2, 2),
		`import "os"`:  at(ExactNew, New, 3, 3),
		"    old()":    at(ExactOld, Old, 3, 3),
	} {
		if got := set.Place("test.go", mustSnippet(t, code)); got != want {
			t.Errorf("%q: got %+v, want %+v", code, got, want)
		}
	}
}

func TestResolveMixedTiers(t *testing.T) {
	set := NewSet(oneFile("@@ -5,3 +5,4 @@\n func foo() {\n+    newLine()\n     bar()\n }",
		"package main\nimport \"fmt\"\n\nfunc helper() {}\nfunc foo() {\n    newLine()\n    bar()\n}"))
	for code, want := range map[string]Anchor{
		"    newLine()":                  at(ExactNew, New, 6, 6),
		"func helper() {}":               at(InFile, New, 4, 4),
		"this_does_not_exist_anywhere()": Unplaced("test.go", ReasonNotFound),
	} {
		if got := set.Place("test.go", mustSnippet(t, code)); got != want {
			t.Errorf("%q: got %+v, want %+v", code, got, want)
		}
	}
}

func TestNewSnippetRefusesBlankCode(t *testing.T) {
	for code, want := range map[string]error{
		"":        ErrNoSnippet,
		"\n\n\n":  ErrNoSnippet,
		" \t\r\n": ErrNoSnippet,
	} {
		if _, err := NewSnippet(code); err != want {
			t.Errorf("NewSnippet(%q) = %v, want %v", code, err, want)
		}
	}
}

func TestNormalizeLine(t *testing.T) {
	for in, want := range map[string]string{
		"  hello  ":     "hello",
		"+added line":   "added line",
		"-deleted line": "deleted line",
		"\tindented\t":  "indented",
		"":              "",
		"crlf\r":        "crlf",
		"+ -both":       "-both",
		"-+minus first": "+minus first",
	} {
		if got := normalizeLine(in); got != want {
			t.Errorf("normalizeLine(%q) = %q, want %q", in, got, want)
		}
	}
	if got := mustSnippet(t, "line1\n\nline2").lines; !slices.Equal(got, []string{"line1", "line2"}) {
		t.Errorf("snippet lines = %q", got)
	}
}

func TestFileSides(t *testing.T) {
	sideOf := func(segs [][]line) []line { return slices.Concat(segs...) }
	for _, tc := range []struct {
		name     string
		diff     string
		new, old []line
	}{
		{"both sides", "@@ -10,3 +10,3 @@\n     ctx := r.Context()\n-    log.Print(\"old\")\n+    log.Printf(\"new: %s\", r.URL)\n     err := process(ctx)",
			[]line{{10, "ctx := r.Context()"}, {11, `log.Printf("new: %s", r.URL)`}, {12, "err := process(ctx)"}},
			[]line{{10, "ctx := r.Context()"}, {11, `log.Print("old")`}, {12, "err := process(ctx)"}}},
		{"divergent starts", "@@ -5,2 +8,3 @@\n A\n+B\n C", []line{{8, "A"}, {9, "B"}, {10, "C"}}, []line{{5, "A"}, {6, "C"}}},
		{"only added", "@@ -1,0 +1,2 @@\n+line1\n+line2", []line{{1, "line1"}, {2, "line2"}}, nil},
		{"only deleted", "@@ -3,2 +3,0 @@\n-old1\n-old2", nil, []line{{3, "old1"}, {4, "old2"}}},
		{"blank lines are skipped but numbered", "@@ -1,3 +1,3 @@\n a\n \n b", []line{{1, "a"}, {3, "b"}}, []line{{1, "a"}, {3, "b"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFile("f", "f", tc.diff, nil)
			if got := sideOf(f.newSide); !slices.Equal(got, tc.new) {
				t.Errorf("new side = %v, want %v", got, tc.new)
			}
			if got := sideOf(f.oldSide); !slices.Equal(got, tc.old) {
				t.Errorf("old side = %v, want %v", got, tc.old)
			}
		})
	}
}

func TestFind(t *testing.T) {
	seg := func(pairs ...any) [][]line {
		var ls []line
		for i := 0; i < len(pairs); i += 2 {
			ls = append(ls, line{pairs[i].(int), pairs[i+1].(string)})
		}
		return [][]line{ls}
	}
	for _, tc := range []struct {
		name  string
		segs  [][]line
		want  []string
		first span
		n     int
	}{
		{"single line", seg(5, "hello", 6, "world", 7, "foo"), []string{"world"}, span{6, 6}, 1},
		{"multi-line", seg(1, "a", 2, "b", 3, "c", 4, "d"), []string{"b", "c"}, span{2, 3}, 1},
		{"no match", seg(1, "a", 2, "b"), []string{"x"}, span{}, 0},
		{"every match is counted", seg(10, "x", 11, "y", 20, "x", 21, "y"), []string{"x", "y"}, span{10, 11}, 2},
		{"longer than the side", seg(1, "a"), []string{"a", "b"}, span{}, 0},
		{"empty side", nil, []string{"a"}, span{}, 0},
		{"at the end", seg(1, "a", 2, "b", 3, "c"), []string{"b", "c"}, span{2, 3}, 1},
		{"at the start", seg(1, "a", 2, "b", 3, "c"), []string{"a", "b"}, span{1, 2}, 1},
		{"the whole side", seg(1, "a", 2, "b"), []string{"a", "b"}, span{1, 2}, 1},
		{"never across segments", append(seg(1, "a"), seg(2, "b")...), []string{"a", "b"}, span{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, _, n := find(tc.segs, tc.want, 0)
			if first != tc.first || n != tc.n {
				t.Errorf("find = %v, %d; want %v, %d", first, n, tc.first, tc.n)
			}
		})
	}
}

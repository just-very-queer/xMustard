// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors
// Copyright 2026 xMustard contributors
//
// Translated from open-code-review@486022d internal/diff/relocate_across_files_test.go.
// Changes by xMustard contributors: Set.Place replaces RelocateAcrossFiles on a comment;
// the result is an Anchor (a decline is unanchored with its reason, never a mutated
// comment); a case where the other file holds the excerpt twice.

package anchor

import "testing"

// headerDiff declares the function; sourceDiff implements it. A reviewer reads the
// source while reviewing the header and files a comment about the body against the
// header.
const headerDiff = "diff --git a/src/span.h b/src/span.h\n--- a/src/span.h\n+++ b/src/span.h\n" +
	"@@ -10,3 +10,4 @@\n void span_set_text(span_t * span, const char * text);\n+void span_set_text_fmt(span_t * span, const char * fmt, ...);\n"

const sourceDiff = "diff --git a/src/span.c b/src/span.c\n--- a/src/span.c\n+++ b/src/span.c\n" +
	"@@ -40,4 +40,7 @@\n void span_set_text_fmt(span_t * span, const char * fmt, ...)\n {\n" +
	"+\tchar * text = span_vfmt(fmt, args);\n+\tif(text == NULL) return;\n+\tva_end(args);\n }\n"

const earlyReturn = "\tif(text == NULL) return;\n\tva_end(args);"

func spanFiles(extra ...*File) []*File {
	return append([]*File{NewFile("src/span.h", "src/span.h", headerDiff, nil), NewFile("src/span.c", "src/span.c", sourceDiff, nil)}, extra...)
}

func TestPlaceRefilesToTheImplementation(t *testing.T) {
	got := NewSet(spanFiles()).Place("src/span.h", mustSnippet(t, earlyReturn))
	// The lines move with the path, or the comment is re-filed onto the right file while
	// still pointing at the wrong line.
	want := Anchor{Path: "src/span.c", StartLine: 43, EndLine: 44, Side: New, Status: Relocated, RefiledFrom: "src/span.h"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPlaceDeclinesWhenAmbiguous(t *testing.T) {
	// The same excerpt in two files: re-filing onto either would swap one wrong location
	// for another.
	dup := "diff --git a/src/other.c b/src/other.c\n--- a/src/other.c\n+++ b/src/other.c\n" +
		"@@ -1,2 +1,4 @@\n+\tchar * text = span_vfmt(fmt, args);\n+\tif(text == NULL) return;\n+\tva_end(args);\n }\n"
	got := NewSet(spanFiles(NewFile("src/other.c", "src/other.c", dup, nil))).Place("src/span.h", mustSnippet(t, earlyReturn))
	want := Anchor{Path: "src/span.h", Status: Unanchored, Reason: ReasonAmbiguousAcrossFiles, Candidates: 2}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPlaceDeclinesWhenTheOtherFileHoldsItTwice(t *testing.T) {
	// One other file, but two places in it: still no single home.
	twice := "@@ -1,2 +1,6 @@\n+\tif(text == NULL) return;\n+\tva_end(args);\n x\n+\tif(text == NULL) return;\n+\tva_end(args);\n y\n"
	files := []*File{NewFile("src/span.h", "src/span.h", headerDiff, nil), NewFile("src/twice.c", "src/twice.c", twice, nil)}
	got := NewSet(files).Place("src/span.h", mustSnippet(t, earlyReturn))
	if got.Status != Unanchored || got.Reason != ReasonAmbiguousAcrossFiles || got.Candidates != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestPlaceDeclinesWhenAbsent(t *testing.T) {
	if got := NewSet(spanFiles()).Place("src/span.h", mustSnippet(t, "int nothing_here = 0;")); got != Unplaced("src/span.h", ReasonNotFound) {
		t.Fatalf("got %+v", got)
	}
}

func TestPlaceKeepsItsOwnFile(t *testing.T) {
	// A snippet its own file holds is never re-filed, even when another file holds it
	// too, and an empty snippet or an empty change anchors nothing.
	got := NewSet(spanFiles()).Place("src/span.c", mustSnippet(t, "\tva_end(args);"))
	if got.Status != ExactNew || got.Path != "src/span.c" || got.RefiledFrom != "" {
		t.Fatalf("own file: got %+v", got)
	}
	if got := NewSet(spanFiles()).Place("src/span.h", Snippet{}); got != Unplaced("src/span.h", ReasonNoSnippet) {
		t.Fatalf("empty snippet: got %+v", got)
	}
	if got := NewSet(nil).Place("src/span.h", mustSnippet(t, "x")); got != Unplaced("src/span.h", ReasonNotFound) {
		t.Fatalf("empty change: got %+v", got)
	}
}

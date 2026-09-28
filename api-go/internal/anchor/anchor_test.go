// SPDX-License-Identifier: MIT
// Copyright 2026 xMustard contributors

package anchor

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

type fixtureFile struct {
	OldPath string  `json:"old_path"`
	NewPath string  `json:"new_path"`
	Diff    string  `json:"diff"`
	Head    *string `json:"head"`
}

// place anchors code filed against path the way a caller does: a refused snippet is
// unanchored with its reason.
func place(set *Set, path, code string) Anchor {
	s, err := NewSnippet(code)
	if err != nil {
		return Unplaced(path, SnippetReason(err))
	}
	return set.Place(path, s)
}

// TestFixtures runs testdata/fixtures.json: CRLF, ambiguity, every tier, re-filing,
// added and deleted files, and the snippet bounds.
func TestFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			Name    string        `json:"name"`
			Files   []fixtureFile `json:"files"`
			Path    string        `json:"path"`
			Snippet string        `json:"snippet"`
			Want    Anchor        `json:"want"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) < 20 {
		t.Fatalf("only %d fixtures", len(doc.Cases))
	}
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var files []*File
			for _, f := range c.Files {
				var head func() string
				if f.Head != nil {
					head = func() string { return *f.Head }
				}
				files = append(files, NewFile(f.OldPath, f.NewPath, f.Diff, head))
			}
			if got := place(NewSet(files), c.Path, c.Snippet); got != c.Want {
				t.Errorf("got  %+v\nwant %+v", got, c.Want)
			}
		})
	}
}

func TestNewSnippetBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		err  error
	}{
		{"4 KiB", strings.Repeat("x", MaxSnippetBytes), nil},
		{"one byte over 4 KiB", strings.Repeat("x", MaxSnippetBytes+1), ErrSnippetTooLarge},
		{"40 lines", strings.Repeat("x\n", 40), nil},
		{"40 CRLF lines", strings.Repeat("x\r\n", 40), nil},
		{"41 lines", strings.Repeat("x\n", 40) + "x", ErrSnippetTooLarge},
		{"41 lines, the last blank", strings.Repeat("x\n", 40) + " ", ErrSnippetTooLarge},
	} {
		if _, err := NewSnippet(tc.code); err != tc.err {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.err)
		}
	}
	if SnippetReason(ErrSnippetTooLarge) != ReasonTooLarge || SnippetReason(ErrNoSnippet) != ReasonNoSnippet {
		t.Error("SnippetReason")
	}
}

// Locate is a memory's quoted-code anchor (WS-27); Reanchor re-finds it after edits
// (WS-28).
func TestLocateAndReanchor(t *testing.T) {
	const content = "package p\n\nfunc A() {\n\treturn 1\n}\n\nfunc B() {\n\treturn 1\n}\n"
	one, ret := mustSnippet(t, "func B() {\n\treturn 1"), mustSnippet(t, "return 1")
	if got, want := Locate("p.go", content, one), (Anchor{Path: "p.go", StartLine: 7, EndLine: 8, Side: New, Status: InFile}); got != want {
		t.Fatalf("Locate = %+v, want %+v", got, want)
	}
	if got := Locate("p.go", content, ret); got.Reason != ReasonAmbiguous || got.Candidates != 2 {
		t.Fatalf("ambiguous Locate = %+v", got)
	}
	if got := Locate("p.go", content, Snippet{}); got != Unplaced("p.go", ReasonNoSnippet) {
		t.Fatalf("empty Locate = %+v", got)
	}

	prev := Locate("p.go", content, one)
	moved := "package p\n\n// A doc.\nfunc A() {\n\treturn 1\n}\n\nfunc B() {\n\treturn 1\n}\n"
	for _, tc := range []struct {
		name    string
		prev    Anchor
		content string
		s       Snippet
		want    Anchor
	}{
		{"unchanged", prev, content, one, prev},
		{"moved down a line", prev, moved, one, Anchor{Path: "p.go", StartLine: 8, EndLine: 9, Side: New, Status: InFile}},
		{"gone", prev, strings.Replace(content, "func B()", "func C()", 1), one, Unplaced("p.go", ReasonNotFound)},
		{"several, one where it was", Anchor{Path: "p.go", StartLine: 8}, content, ret,
			Anchor{Path: "p.go", StartLine: 8, EndLine: 8, Side: New, Status: InFile}},
		{"several, none where it was", Anchor{Path: "p.go", StartLine: 5}, content, ret,
			Anchor{Path: "p.go", Status: Unanchored, Reason: ReasonAmbiguous, Candidates: 2}},
	} {
		if got := Reanchor(tc.prev, tc.content, tc.s); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// Touches is the in_changed_hunk check: a changed line (blank ones included) inside the
// anchored range, on the anchor's side.
func TestTouches(t *testing.T) {
	diff := "@@ -1,6 +1,7 @@\n ctx1\n-gone\n+came\n+\n ctx2\n ctx3\n ctx4\n ctx5\n"
	set := NewSet([]*File{NewFile("t.go", "t.go", diff, func() string { return "ctx1\ncame\n\nctx2\nctx3\nctx4\nctx5\n" })})
	for _, tc := range []struct {
		code string
		want bool
	}{
		{"came", true},        // an added line
		{"ctx1\ncame", true},  // context plus an added line
		{"ctx2\nctx3", false}, // context only
		{"came\nctx2", true},  // spans the added blank line
		{"ctx1\ngone", true},  // old side, holding the deleted line
		{"ctx4\nctx5", false}, // context lines at the hunk's end
	} {
		a := place(set, "t.go", tc.code)
		if a.Status == Unanchored {
			t.Fatalf("%q unanchored: %+v", tc.code, a)
		}
		if got := set.Touches(a); got != tc.want {
			t.Errorf("Touches(%q at %+v) = %v, want %v", tc.code, a, got, tc.want)
		}
	}
	for _, a := range []Anchor{Unplaced("t.go", ReasonNotFound), {Path: "other.go", StartLine: 1, EndLine: 1, Side: New}} {
		if set.Touches(a) {
			t.Errorf("Touches(%+v) = true", a)
		}
	}
}

// A finding filed against a file outside the change is looked for in that file at head
// (Outside) before it is re-filed; without Outside it is only re-filed.
func TestPlaceTriesAFileOutsideTheChangeFirst(t *testing.T) {
	changed := NewFile("a.go", "a.go", "@@ -1 +1,2 @@\n x\n+shared()\n", nil)
	heads := map[string]string{"out.go": "package p\n\nfunc F() {\n\tshared()\n}\n", "twice.go": "shared()\nshared()\n"}
	var asked []string
	set := NewSet([]*File{changed})
	set.Outside = func(p string) string { asked = append(asked, p); return heads[p] }
	for _, tc := range []struct {
		path, code string
		want       Anchor
	}{
		{"out.go", "shared()", Anchor{Path: "out.go", StartLine: 4, EndLine: 4, Side: New, Status: InFile}},
		{"twice.go", "shared()", Anchor{Path: "twice.go", Status: Unanchored, Reason: ReasonAmbiguous, Candidates: 2}},
		{"missing.go", "shared()", Anchor{Path: "a.go", StartLine: 2, EndLine: 2, Side: New, Status: Relocated, RefiledFrom: "missing.go"}},
		{"a.go", "x", Anchor{Path: "a.go", StartLine: 1, EndLine: 1, Side: New, Status: ExactNew}},
	} {
		if got := place(set, tc.path, tc.code); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.path, got, tc.want)
		}
	}
	if want := []string{"out.go", "twice.go", "missing.go"}; !slices.Equal(asked, want) {
		t.Errorf("Outside asked for %q, want %q (a changed file never goes through it)", asked, want)
	}
	set.Outside = nil
	if got := place(set, "out.go", "shared()"); got.Status != Relocated || got.Path != "a.go" {
		t.Errorf("without Outside: %+v", got)
	}
}

// The head is read once, and only when a snippet reaches the whole-file tier.
func TestHeadIsLoadedLazilyAndOnce(t *testing.T) {
	loads := 0
	f := NewFile("l.go", "l.go", "@@ -1 +1 @@\n-a\n+b\n", func() string { loads++; return "b\nc\n" })
	set := NewSet([]*File{f})
	if place(set, "l.go", "b").Status != ExactNew || loads != 0 {
		t.Fatalf("hunk match loaded the head %d times", loads)
	}
	for range 3 {
		if got := place(set, "l.go", "c"); got.Status != InFile || got.StartLine != 2 {
			t.Fatalf("file tier: %+v", got)
		}
	}
	if loads != 1 {
		t.Fatalf("head loaded %d times, want 1", loads)
	}
}

// BenchmarkPlace parses a 200-file change (each file 300 lines at head) and anchors 50
// snippets against it: most in a hunk, a fifth only in the head content, and a tenth in
// no file, so re-filing reads and searches every file's head.
func BenchmarkPlace(b *testing.B) {
	type spec struct{ path, diff, head string }
	var specs []spec
	for i := range 200 {
		var head, diff strings.Builder
		diff.WriteString("@@ -100,6 +100,8 @@\n")
		for j := range 300 {
			fmt.Fprintf(&head, "\tv%d_%d := compute(%d, %d)\n", i, j, i, j)
		}
		for j := 99; j < 105; j++ {
			fmt.Fprintf(&diff, " \tv%d_%d := compute(%d, %d)\n", i, j, i, j)
		}
		fmt.Fprintf(&diff, "+\tadded%d()\n+\tmore%d()\n", i, i)
		specs = append(specs, spec{fmt.Sprintf("f%d.go", i), diff.String(), head.String()})
	}
	type query struct {
		path string
		s    Snippet
	}
	var qs []query
	for i := range 50 {
		code := fmt.Sprintf("added%d()\nmore%d()", i, i)
		switch {
		case i%10 == 0:
			code = fmt.Sprintf("absent%d()", i)
		case i%5 == 1:
			code = fmt.Sprintf("v%d_%d := compute(%d, %d)", i, 250, i, 250)
		}
		s, err := NewSnippet(code)
		if err != nil {
			b.Fatal(err)
		}
		qs = append(qs, query{fmt.Sprintf("f%d.go", i), s})
	}
	b.ReportAllocs()
	for b.Loop() {
		files := make([]*File, 0, len(specs))
		for _, sp := range specs {
			files = append(files, NewFile(sp.path, sp.path, sp.diff, func() string { return sp.head }))
		}
		set := NewSet(files)
		for _, q := range qs {
			if a := set.Place(q.path, q.s); a.Status == Unanchored && a.Reason != ReasonNotFound {
				b.Fatalf("%s: %+v", q.path, a)
			}
		}
	}
}

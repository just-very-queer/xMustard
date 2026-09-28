//go:build review

package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/anchor"
	"xmustard/api-go/internal/review"
)

// anchorRepo is reviewRepo with more files on main (a 40-line file, a name with a space,
// a non-ASCII name, a file the feature deletes and one it leaves alone) and a feature
// commit that edits and deletes them, rebased so main is the merge base.
func anchorRepo(t testing.TB) (dataDir, ws string) {
	t.Helper()
	dataDir, ws, root, git := reviewRepo(t)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var big strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&big, "\tv%02d := step(%d)\n", i, i)
	}
	git("checkout", "-q", "main")
	write("big.go", big.String())
	write("my file.go", "package a\n\nvar spaced = 1\n")
	write("café.go", "package a\n\nvar accent = 1\n")
	write("gone.go", "package a\n\nfunc Gone() {}\n")
	write("keep.go", "package a\n\nvar kept = 1\n")
	git("add", ".")
	git("commit", "-q", "-m", "more")
	git("checkout", "-q", "feature")
	git("rebase", "-q", "main")
	write("big.go", strings.Replace(big.String(), "v20 := step(20)", "v20 := step(20) + drift", 1))
	write("my file.go", "package a\n\nvar spaced = 2\n")
	write("café.go", "package a\n\nvar accent = 2\n")
	git("rm", "-q", "gone.go")
	git("add", ".")
	git("commit", "-q", "-m", "edit")
	return dataDir, ws
}

func findingsBatch(t testing.TB, fs ...[2]string) *review.Batch {
	t.Helper()
	var items []map[string]string
	for _, f := range fs {
		items = append(items, map[string]string{"path": f[0], "content": "look here", "existing_code": f[1], "category": "bug", "severity": "medium"})
	}
	raw, _ := json.Marshal(items)
	b, err := review.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReviewAnchorBindsTheSealedChange(t *testing.T) {
	dir, ws := anchorRepo(t)
	batch := findingsBatch(t,
		[2]string{"a.go", "func A() int { return 2 }"},
		[2]string{"big.go", "v05 := step(5)\nv06 := step(6)"},
		[2]string{"my file.go", "var spaced = 2"},
		[2]string{"café.go", "var accent = 2"},
		[2]string{"gone.go", "func Gone() {}"},
		[2]string{"b.go", "v20 := step(20) + drift"},
		[2]string{"a.go", "not in this change"},
		[2]string{"keep.go", "var kept = 1"},    // a file outside the change, read at head
		[2]string{"nowhere.go", "var kept = 1"}, // a path head does not hold
	)
	got, err := AnchorReviewFindings(context.Background(), dir, ws, "main", "HEAD", batch)
	if err != nil {
		t.Fatal(err)
	}
	want, err := DiffReviewedChange(context.Background(), dir, ws, "main", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got.Change != want {
		t.Fatalf("anchored against %+v, the attested change is %+v", got.Change, want)
	}
	type row struct {
		path            string
		status          anchor.Status
		lines           [2]int
		inHunk, inScope bool
	}
	wantRows := []row{
		{"a.go", anchor.ExactNew, [2]int{3, 3}, true, true},
		{"big.go", anchor.InFile, [2]int{5, 6}, false, true},
		{"my file.go", anchor.ExactNew, [2]int{3, 3}, true, true},
		{"café.go", anchor.ExactNew, [2]int{3, 3}, true, true},
		{"gone.go", anchor.ExactOld, [2]int{3, 3}, true, true},
		{"big.go", anchor.Relocated, [2]int{20, 20}, true, true},
		{"a.go", anchor.Unanchored, [2]int{}, false, true},
		{"keep.go", anchor.InFile, [2]int{3, 3}, false, false},
		{"nowhere.go", anchor.Unanchored, [2]int{}, false, false},
	}
	if len(got.Findings) != len(wantRows) {
		t.Fatalf("%d findings, want %d", len(got.Findings), len(wantRows))
	}
	for i, f := range got.Findings {
		g := row{f.Anchor.Path, f.Anchor.Status, [2]int{f.Anchor.StartLine, f.Anchor.EndLine}, f.Checks.InChangedHunk, f.Checks.InScope}
		if g != wantRows[i] {
			t.Errorf("finding %d: got %+v, want %+v (%+v)", i, g, wantRows[i], f.Anchor)
		}
	}
	if got.Counts.Supported != 7 || got.Counts.Unsupported != 2 || got.Findings[6].Support != review.Unsupported ||
		got.Findings[7].Support != review.Supported || got.Findings[8].Support != review.Unsupported {
		t.Errorf("counts = %+v", got.Counts)
	}
	// big.go and keep.go needed their heads (a.go's miss reads a.go and b.go too, found
	// nowhere); nowhere.go is not at head.
	if got.HeadReads.Files < 2 || got.HeadReads.Skipped != 0 || got.HeadReads.Missing != 1 || got.Files != 6 || got.Label != ReviewEvidenceLabel {
		t.Errorf("files %d, head reads %+v", got.Files, got.HeadReads)
	}
}

func TestReviewAnchorBounds(t *testing.T) {
	dir, ws := anchorRepo(t)
	batch := findingsBatch(t, [2]string{"big.go", "v05 := step(5)"})

	defer func(v int64) { headFileLimit = v }(headFileLimit)
	headFileLimit = 64
	got, err := AnchorReviewFindings(context.Background(), dir, ws, "main", "HEAD", batch)
	if err != nil {
		t.Fatal(err)
	}
	if a := got.Findings[0].Anchor; a.Status != anchor.Unanchored || !slices.Contains(got.HeadReads.SkippedPaths, "big.go") {
		t.Fatalf("a head over its bound: anchor %+v, reads %+v", a, got.HeadReads)
	}
	headFileLimit = 1 << 20

	defer func(v int) { anchorDiffLimit = v }(anchorDiffLimit)
	anchorDiffLimit = 64
	if _, err := AnchorReviewFindings(context.Background(), dir, ws, "main", "HEAD", batch); !errors.Is(err, errAnchorTooLarge) {
		t.Fatalf("a diff over its bound: %v", err)
	}
}

func TestReviewAnchorRefusesBadInput(t *testing.T) {
	dir, ws := anchorRepo(t)
	batch := findingsBatch(t, [2]string{"a.go", "x"})
	for name, call := range map[string]func() error{
		"unknown base": func() error {
			_, err := AnchorReviewFindings(context.Background(), dir, ws, "no-such-branch", "HEAD", batch)
			return err
		},
		"option-shaped head": func() error {
			_, err := AnchorReviewFindings(context.Background(), dir, ws, "main", "--output=/tmp/x", batch)
			return err
		},
		"unknown workspace": func() error {
			_, err := AnchorReviewFindings(context.Background(), dir, "nope", "main", "HEAD", batch)
			return err
		},
		"no batch": func() error {
			_, err := AnchorReviewFindings(context.Background(), dir, ws, "main", "HEAD", nil)
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// BenchmarkReviewAnchor anchors 50 findings against a change of 200 files of 300 lines,
// two lines edited in each: 30 in the hunks, 10 only in a head (read through cat-file),
// 5 filed against the wrong file and 5 found nowhere, so re-filing reads every head.
func BenchmarkReviewAnchor(b *testing.B) {
	dir, ws, root, git := reviewRepo(b)
	body := func(i int, edited bool) string {
		var s strings.Builder
		for j := range 300 {
			fmt.Fprintf(&s, "\tv%03d_%03d := step(%d, %d)\n", i, j, i, j)
			if edited && (j == 100 || j == 200) {
				fmt.Fprintf(&s, "\tcheck%03d_%03d()\n", i, j)
			}
		}
		return s.String()
	}
	write := func(edited bool) {
		for i := range 200 {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.go", i)), []byte(body(i, edited)), 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	git("checkout", "-q", "main")
	write(false)
	git("add", ".")
	git("commit", "-q", "-m", "files")
	git("checkout", "-q", "feature")
	git("rebase", "-q", "main")
	write(true)
	git("commit", "-q", "-am", "edit")
	var fs [][2]string
	for i := range 50 {
		p, code := fmt.Sprintf("f%03d.go", i), fmt.Sprintf("check%03d_100()", i)
		switch {
		case i%10 == 0:
			code = fmt.Sprintf("absent%03d()", i)
		case i%10 == 1:
			p = fmt.Sprintf("f%03d.go", i+1)
		case i%5 == 2:
			code = fmt.Sprintf("v%03d_%03d := step(%d, %d)", i, 20, i, 20)
		}
		fs = append(fs, [2]string{p, code})
	}
	batch := findingsBatch(b, fs...)
	var res *ReviewAnchoring
	b.ReportAllocs()
	for b.Loop() {
		var err error
		if res, err = AnchorReviewFindings(context.Background(), dir, ws, "main", "HEAD", batch); err != nil {
			b.Fatal(err)
		}
	}
	if res.Counts.Supported != 45 || res.Counts.ByStatus[anchor.Relocated] != 5 {
		b.Fatalf("counts %+v", res.Counts)
	}
	b.ReportMetric(float64(res.Change.DiffBytes), "diff_bytes")
	b.ReportMetric(float64(res.HeadReads.Bytes), "head_bytes")
}

package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/anchor"
)

const oneFinding = `{"path":"a.go","content":"nil check misses err","existing_code":"if x == nil {","category":"bug","severity":"high"}`

func TestDecodeAcceptsEveryShape(t *testing.T) {
	quoted, _ := json.Marshal("[" + oneFinding + "]")
	for _, tc := range []struct {
		name  string
		input string
		notes []string
	}{
		{"bare array", "[" + oneFinding + "]", nil},
		{"findings object", `{"findings":[` + oneFinding + `]}`, nil},
		{"OCR --format json --output", `{"status":"complete","summary":{"files_reviewed":3},"manifest":{"x":1},"comments":[` +
			strings.Replace(oneFinding, `"category"`, `"start_line":4,"end_line":4,"thinking":"because","category"`, 1) + `]}`,
			[]string{"thinking dropped from 1 findings"}},
		{"array sent as a JSON string", string(quoted), []string{"the findings arrived as a JSON string and were decoded"}},
		{"findings member sent as a JSON string", `{"findings":` + string(quoted) + `}`, []string{"the findings arrived as a JSON string and were decoded"}},
		{"surrounding whitespace", "\n  [" + oneFinding + "]\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Decode([]byte(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			if len(b.Findings) != 1 || b.Findings[0].Path != "a.go" || b.Findings[0].Severity != "high" {
				t.Fatalf("findings = %+v", b.Findings)
			}
			if !slices.Equal(b.Normalized, tc.notes) {
				t.Errorf("notes = %q, want %q", b.Normalized, tc.notes)
			}
			if b.Source.Bytes != len(tc.input) || len(b.Source.SHA256) != 64 {
				t.Errorf("source = %+v", b.Source)
			}
		})
	}
}

func TestDecodeRefusesMalformedInput(t *testing.T) {
	many := "[" + strings.Repeat(oneFinding+",", MaxFindings) + oneFinding + "]"
	huge := `{"findings":[` + oneFinding + `],"pad":"` + strings.Repeat("x", MaxFindingsBytes) + `"}`
	withField := func(k, v string) string {
		return "[" + strings.Replace(oneFinding, `"path"`, `"`+k+`":`+v+`,"path"`, 1) + "]"
	}
	for name, input := range map[string]string{
		"not JSON":                "nope",
		"a number":                "42",
		"an unknown member":       withField("confidence", "0.9"),
		"a wrong type":            withField("start_line", `"4"`),
		"no path":                 `[{"content":"x"}]`,
		"no content":              `[{"path":"a.go","content":"  "}]`,
		"an absolute path":        `[{"path":"/etc/passwd","content":"x"}]`,
		"a path escaping":         `[{"path":"../x.go","content":"x"}]`,
		"a path that is the root": `[{"path":"./","content":"x"}]`,
		"a NUL in the path":       `[{"path":"a\u0000b","content":"x"}]`,
		"both arrays":             `{"findings":[],"comments":[` + oneFinding + `]}`,
		"no array":                `{"status":"complete"}`,
		"a findings object":       `{"findings":{"path":"a.go"}}`,
		"a string of an object":   `"{}"`,
		"a string of a string":    `"\"[]\""`,
		"too many findings":       many,
		"over the byte bound":     huge,
		"an item that is a list":  `[[]]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(input)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	if b, err := Decode([]byte(strings.Replace(many, oneFinding+",", "", 1))); err != nil || len(b.Findings) != MaxFindings {
		t.Fatalf("exactly %d findings: %v", MaxFindings, err)
	}
}

func TestDecodeNormalizesAndBounds(t *testing.T) {
	long := strings.Repeat("é", MaxContentRunes+5)
	big := strings.Repeat("x := 1\n", anchor.MaxSnippetLines+1)
	input, _ := json.Marshal([]map[string]any{
		{"path": "./pkg//a.go", "content": long, "existing_code": big, "suggestion_code": big, "category": "Security", "severity": "urgent"},
		{"path": "b.go", "content": "fine", "category": "", "severity": "LOW"},
	})
	b, err := Decode(input)
	if err != nil {
		t.Fatal(err)
	}
	f := b.Findings[0]
	if f.Path != "pkg/a.go" || f.Category != "security" || f.Severity != "low" || f.ExistingCode != "" || f.SuggestionCode != "" {
		t.Fatalf("finding = %+v", f)
	}
	if n := len([]rune(f.Content)); n != MaxContentRunes || !strings.HasPrefix(long, f.Content) {
		t.Fatalf("content has %d runes", n)
	}
	for _, want := range []string{"content cut from 2005 to 2000 characters", `severity "urgent" is not a known value; recorded as low`,
		"existing_code dropped", "suggestion_code dropped"} {
		if !slices.ContainsFunc(f.notes, func(n string) bool { return strings.HasPrefix(n, want) }) {
			t.Errorf("no note %q in %q", want, f.notes)
		}
	}
	if g := b.Findings[1]; g.Category != "other" || g.Severity != "low" || !slices.Equal(g.notes, []string{"category missing; recorded as other"}) {
		t.Fatalf("second finding = %+v", g)
	}
}

func TestSplitDiff(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/new.go b/new.go\nnew file mode 100644\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1 @@\n+n\n" +
		"diff --git a/old.go b/old.go\ndeleted file mode 100644\n--- a/old.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-o\n" +
		"diff --git \"a/caf\\303\\251.go\" \"b/caf\\303\\251.go\"\n--- \"a/caf\\303\\251.go\"\n+++ \"b/caf\\303\\251.go\"\n@@ -1 +1 @@\n-x\n+y\n" +
		"diff --git a/my file.go b/my file.go\n--- a/my file.go\t\n+++ b/my file.go\t\n@@ -1 +1 @@\n-p\n+q\n" +
		"diff --git a/img.png b/img.png\nnew file mode 100644\nindex 0..1\nGIT binary patch\nliteral 3\nKcmZQzU|;|M00aO5\n\nliteral 0\nHcmV?d00001\n\n" +
		"diff --git \"a/tab\\there.bin\" \"b/tab\\there.bin\"\nold mode 100644\nnew mode 100755\n" +
		"diff --git a/content diff --git b/c b/content diff --git b/c\n--- a/content diff --git b/c\n+++ b/content diff --git b/c\n@@ -1 +1 @@\n-diff --git a/x b/x\n+z\n"
	type pair struct{ old, new string }
	var got []pair
	for _, s := range SplitDiff(diff) {
		got = append(got, pair{s.OldPath, s.NewPath})
		if !strings.HasPrefix(s.Diff, gitHeader) {
			t.Errorf("section of %s does not start at its header", s.NewPath)
		}
	}
	want := []pair{{"a.go", "a.go"}, {"", "new.go"}, {"old.go", ""}, {"café.go", "café.go"}, {"my file.go", "my file.go"},
		{"", "img.png"}, {"tab\there.bin", "tab\there.bin"}, {"content diff --git b/c", "content diff --git b/c"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if SplitDiff("") != nil || SplitDiff("no diff here\n") != nil {
		t.Fatal("text without a diff --git line split into files")
	}
	if got := SplitDiff("diff --git a/x b/y\nold mode 100644\nnew mode 100755\n"); got != nil {
		t.Fatalf("a renamed header without ---/+++ lines has no readable path: %+v", got)
	}
}

// change is a set over split diff text with head contents by path.
func change(diff string, heads map[string]string) *anchor.Set {
	var files []*anchor.File
	for _, s := range SplitDiff(diff) {
		h, ok := heads[s.NewPath]
		var head func() string
		if ok {
			head = func() string { return h }
		}
		files = append(files, anchor.NewFile(s.OldPath, s.NewPath, s.Diff, head))
	}
	return anchor.NewSet(files)
}

func TestAnchorChecksEveryFinding(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,4 +1,5 @@\n func A() {\n-\tif x == nil {\n+\tif x == nil || err != nil {\n+\t\tlog()\n \t\treturn\n \t}\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,2 +1,3 @@\n func B() {\n+\tpanic(\"todo\")\n }\n"
	set := change(diff, map[string]string{"a.go": "func A() {\n\tif x == nil || err != nil {\n\t\tlog()\n\t\treturn\n\t}\n}\n\nfunc helper() {}\n"})
	finding := func(p, code string) string {
		b, _ := json.Marshal(map[string]any{"path": p, "content": "c", "existing_code": code, "category": "bug", "severity": "low", "start_line": 1, "end_line": 1})
		return string(b)
	}
	b, err := Decode([]byte("[" + strings.Join([]string{
		finding("a.go", "if x == nil || err != nil {\n\t\tlog()"), // added lines
		finding("a.go", "\t\treturn\n\t}"),                        // context only
		finding("a.go", "if x == nil {"),                          // deleted line
		finding("a.go", "func helper() {}"),                       // head only
		finding("a.go", "panic(\"todo\")"),                        // filed against the wrong file
		finding("a.go", "not in the change"),                      // unsupported
		finding("c.go", "func helper() {}"),                       // outside the change, re-filed
		finding("c.go", ""),                                       // no quoted code
	}, ",") + "]"))
	if err != nil {
		t.Fatal(err)
	}
	got, counts := Anchor(b.Findings, set)
	type row struct {
		status          anchor.Status
		path            string
		lines           [2]int
		present, inHunk bool
		inScope         bool
		support         string
	}
	want := []row{
		{anchor.ExactNew, "a.go", [2]int{2, 3}, true, true, true, Supported},
		{anchor.ExactNew, "a.go", [2]int{4, 5}, true, false, true, Supported},
		{anchor.ExactOld, "a.go", [2]int{2, 2}, true, true, true, Supported},
		{anchor.InFile, "a.go", [2]int{8, 8}, true, false, true, Supported},
		{anchor.Relocated, "b.go", [2]int{2, 2}, true, true, true, Supported},
		{anchor.Unanchored, "a.go", [2]int{}, false, false, true, Unsupported},
		{anchor.Relocated, "a.go", [2]int{8, 8}, true, false, true, Supported},
		{anchor.Unanchored, "c.go", [2]int{}, false, false, false, Unsupported},
	}
	for i, r := range got {
		g := row{r.Anchor.Status, r.Anchor.Path, [2]int{r.Anchor.StartLine, r.Anchor.EndLine}, r.Checks.CodePresent,
			r.Checks.InChangedHunk, r.Checks.InScope, r.Support}
		if g != want[i] {
			t.Errorf("finding %d: got %+v, want %+v (%+v)", i, g, want[i], r.Anchor)
		}
		if r.Checks.SymbolResolved != "unknown" {
			t.Errorf("finding %d: symbol_resolved = %q", i, r.Checks.SymbolResolved)
		}
	}
	if got[7].Anchor.Reason != anchor.ReasonNoSnippet || got[4].Anchor.RefiledFrom != "a.go" {
		t.Errorf("reasons: %+v, %+v", got[7].Anchor, got[4].Anchor)
	}
	if !slices.Contains(got[0].Normalized, "the producer's lines 1-1 were replaced by the anchor") {
		t.Errorf("claimed lines: %q", got[0].Normalized)
	}
	wantCounts := Counts{Findings: 8, Supported: 6, Unsupported: 2, InChangedHunk: 3,
		ByStatus: map[anchor.Status]int{anchor.ExactNew: 2, anchor.ExactOld: 1, anchor.InFile: 1, anchor.Relocated: 2, anchor.Unanchored: 2}}
	if fmt.Sprint(counts) != fmt.Sprint(wantCounts) {
		t.Errorf("counts = %+v, want %+v", counts, wantCounts)
	}
	out, err := json.Marshal(got[4])
	if err != nil || !strings.Contains(string(out), `"anchor":{"path":"b.go","start_line":2,"end_line":2,"side":"new","anchor_status":"relocated","refiled_from":"a.go"}`) {
		t.Fatalf("JSON: %s %v", out, err)
	}
}

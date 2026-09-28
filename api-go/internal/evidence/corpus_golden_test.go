package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reducer corpus pins every family reducer's whole result (projection text,
// per-section parts, structured projection, facts and omission ranges) over a fixed
// set of tool outputs at two targets. Each case is kept as a digest line, so a
// behavior-preserving refactor of the reducers must reproduce every projection byte
// for byte; XM_CORPUS_DUMP=<dir> writes each case's full result for diffing.

type corpusCase struct {
	name     string
	raw      []byte
	sel      Selector
	secs     []Section // nil: one "output" section
	ct       string    // content type; "" is text/plain
	noFamily bool      // xm-reduce/1 without a registry hook
}

const corpusGolden = "reducer_corpus"

func TestReducerCorpusGolden(t *testing.T) {
	var listing strings.Builder
	listing.WriteString("# case target bytes sha256 first-line\n")
	dump := os.Getenv("XM_CORPUS_DUMP")
	full := map[string]string{}
	for _, c := range reducerCorpus() {
		for _, target := range []int{2 << 10, 8 << 10} {
			key := fmt.Sprintf("%s@%d", c.name, target)
			out := runCorpusCase(c, target)
			sum := sha256.Sum256([]byte(out))
			first, _, _ := strings.Cut(out, "\n")
			fmt.Fprintf(&listing, "%s %d %s %.100s\n", key, len(out), hex.EncodeToString(sum[:8]), first)
			full[key] = out
			if dump != "" {
				name := strings.NewReplacer("/", "_", " ", "_").Replace(key) + ".txt"
				if err := os.WriteFile(filepath.Join(dump, name), []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	path := filepath.Join("testdata", corpusGolden+".golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(listing.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run with -update)", path, err)
	}
	wantLines := map[string]string{}
	for _, l := range strings.Split(string(want), "\n") {
		if k, _, ok := strings.Cut(l, " "); ok && !strings.HasPrefix(l, "#") {
			wantLines[k] = l
		}
	}
	for _, l := range strings.Split(strings.TrimSuffix(listing.String(), "\n"), "\n")[1:] {
		k, _, _ := strings.Cut(l, " ")
		if wantLines[k] != l {
			t.Errorf("%s changed:\n want %s\n  got %s\n--- got (first 3000 bytes) ---\n%.3000s", k, wantLines[k], l, full[k])
		}
		delete(wantLines, k)
	}
	for k := range wantLines {
		t.Errorf("%s: in the golden but no longer produced", k)
	}
}

// runCorpusCase reduces one case and renders everything the reducer decided.
func runCorpusCase(c corpusCase, target int) string {
	ct := c.ct
	if ct == "" {
		ct = "text/plain"
	}
	ctx := context.Background()
	var h *reduceHook
	if !c.noFamily {
		red, argv0 := DefaultRegistry().Select(c.sel)
		h = &reduceHook{reducer: red, argv0: argv0, in: Input{Sel: c.sel, Target: target, Sections: c.secs}}
		ctx = withReduceHook(ctx, h)
	}
	text, rec, err := Reduce(ctx, bytes.NewReader(c.raw), int64(len(c.raw)), ct, target, 1<<20)
	if err != nil {
		return "error: " + err.Error() + "\n"
	}
	result := struct {
		Record     Record            `json:"record"`
		Parts      map[string]string `json:"parts,omitempty"`
		Structured any               `json:"structured,omitempty"`
		Facts      *Facts            `json:"facts,omitempty"`
	}{Record: rec}
	if h != nil && h.out != nil {
		result.Parts, result.Structured, result.Facts = h.out.Parts, h.out.Structured, &h.out.Facts
	}
	meta, err := json.MarshalIndent(result, "", " ")
	if err != nil {
		return "marshal: " + err.Error() + "\n"
	}
	return text + "\n=== result\n" + string(meta) + "\n"
}

func bash(cmd string) Selector { return Selector{Client: "claude", Tool: "Bash", Command: cmd} }

// lint forces the lint family (these commands alone select build).
func lint(cmd string) Selector {
	s := bash(cmd)
	s.Family = FamilyLint
	return s
}

func exitCode(n int) *int { return &n }

// sections concatenates named outputs into one original and returns their ranges.
func sections(named ...any) ([]byte, []Section) {
	var b bytes.Buffer
	var secs []Section
	for i := 0; i+1 < len(named); i += 2 {
		start := int64(b.Len())
		b.Write(named[i+1].([]byte))
		name := named[i].(string)
		array := strings.HasSuffix(name, "[]")
		secs = append(secs, Section{Name: strings.TrimSuffix(name, "[]"), Start: start, End: int64(b.Len()), Array: array})
	}
	return b.Bytes(), secs
}

func reducerCorpus() []corpusCase {
	var cases []corpusCase
	add := func(c corpusCase) { cases = append(cases, c) }

	// grep family (reduceGrouped, parseGrepLine)
	add(corpusCase{name: "grep/numbered", raw: grepOutput(30, 4), sel: bash("rg -n needle src")})
	add(corpusCase{name: "grep/heading", raw: rgHeading(40, 3), sel: bash("rg --heading -n -C1 needle")})
	add(corpusCase{name: "grep/count", raw: grepCount(700), sel: bash("grep -rc needle src")})
	add(corpusCase{name: "grep/files", raw: grepFiles(900), sel: bash("grep -rl needle src")})
	add(corpusCase{name: "grep/unnumbered", raw: grepUnnumbered(300), sel: bash("grep -r -C1 needle src")})
	add(corpusCase{name: "grep/notices", raw: grepNotices(60), sel: Selector{Client: "pi", Tool: "grep"}})
	add(corpusCase{name: "grep/no-matches", raw: noisyShell(), sel: bash("rg needle")})
	arr, arrSecs := sections("filenames[]", grepFiles(700))
	add(corpusCase{name: "grep/array", raw: arr, sel: Selector{Client: "claude", Tool: "Grep"}, secs: arrSecs})
	multi, multiSecs := sections("stdout", grepOutput(20, 6), "stderr", permissionDenied(80))
	add(corpusCase{name: "grep/sections", raw: multi, sel: bash("grep -rn needle /"), secs: multiSecs})

	// lint family (reduceGrouped, parseLintLine) and its build fallback
	add(corpusCase{name: "lint/diagnostics", raw: lintOutput(12, 40), sel: bash("ruff check .")})
	add(corpusCase{name: "lint/stylish", raw: eslintStylish(40, 12), sel: bash("npx eslint src")})
	add(corpusCase{name: "lint/tsc-and-vet", raw: tscAndVet(300), sel: lint("go vet ./... && tsc --noEmit")})
	add(corpusCase{name: "lint/fallback", raw: clippyOutput(150), sel: lint("cargo clippy")})

	// diff family (diffReducer.Reduce, diffScanner.kind)
	add(corpusCase{name: "diff/many-files", raw: diffOutput(80, 3), sel: bash("git diff")})
	add(corpusCase{name: "diff/log-p", raw: gitLogP(40), sel: bash("git log -p")})
	add(corpusCase{name: "diff/unified", raw: unifiedDiff(50), sel: bash("diff -u a b")})
	add(corpusCase{name: "diff/meta", raw: metaDiff(40), sel: bash("git show HEAD")})
	add(corpusCase{name: "diff/no-hunks", raw: diffStat(600), sel: bash("git diff --stat")})
	add(corpusCase{name: "diff/big-hunk", raw: bigHunk(2000), sel: bash("git diff")})
	add(corpusCase{name: "diff/mangled", raw: mangledDiff(60), sel: bash("git diff")})

	// line engine (planLines, emitLines) over every line family
	add(corpusCase{name: "test/unittest", raw: unittestRun(3000, 60), sel: Selector{Tool: "Bash", Command: "python -m unittest -v", ExitCode: exitCode(1)}})
	add(corpusCase{name: "test/go", raw: goTestRun(3000), sel: bash("go test -v ./...")})
	add(corpusCase{name: "test/jest", raw: jestRun(3000), sel: bash("npx jest")})
	add(corpusCase{name: "test/cargo", raw: cargoRun(3000), sel: bash("cargo test")})
	add(corpusCase{name: "test/go-panic", raw: goPanic(2000), sel: bash("go test ./...")})
	add(corpusCase{name: "shell/noisy", raw: noisyShell(), sel: bash("./build.sh")})
	add(corpusCase{name: "shell/traceback", raw: pythonTraceback(1500), sel: bash("python app.py")})
	add(corpusCase{name: "shell/exit-text", raw: exitStatusRun(900), sel: bash("./run.sh")})
	sh, shSecs := sections("stdout", logLines(1500, 700), "stderr", permissionDenied(300))
	add(corpusCase{name: "shell/sections", raw: sh, sel: bash("./run.sh"), secs: shSecs})
	add(corpusCase{name: "build/make", raw: makeBuild(1200), sel: bash("make -j8")})
	add(corpusCase{name: "log/app", raw: logLines(4000, 2600), sel: bash("tail -n 5000 app.log")})
	add(corpusCase{name: "git/status", raw: gitStatus(800), sel: bash("git status")})

	// read, list and glob
	add(corpusCase{name: "read/source", raw: sourceFile(1500), sel: Selector{Client: "claude", Tool: "Read", Path: "big.go", StartLine: 20}})
	add(corpusCase{name: "list/ls", raw: lsOutput(1500), sel: bash("ls -la")})
	add(corpusCase{name: "glob/paths", raw: globOutput(2000), sel: Selector{Client: "claude", Tool: "Glob"}})

	// structured family and xm-reduce/1 (reduceObject, reduceArray, reduceText)
	add(corpusCase{name: "structured/status", raw: structuredDoc(900), sel: Selector{Client: "claude", Tool: "mcp__ci__runs"}, ct: "application/json"})
	add(corpusCase{name: "json/salient-array", raw: salientArray(1500), noFamily: true, ct: "application/json"})
	add(corpusCase{name: "json/nested", raw: nestedDoc(300), noFamily: true, ct: "application/json"})
	add(corpusCase{name: "json/big-first", raw: bigFirstElement(), noFamily: true, ct: "application/json"})
	add(corpusCase{name: "text/log", raw: logLines(3000, 1800), noFamily: true})
	add(corpusCase{name: "text/long-lines", raw: longLines(), noFamily: true})
	add(corpusCase{name: "text/traceback", raw: pythonTraceback(1200), noFamily: true})
	return cases
}

func rgHeading(files, per int) []byte {
	var b bytes.Buffer
	for f := 0; f < files; f++ {
		fmt.Fprintf(&b, "src/mod%02d/file_%03d.rs\n", f%5, f)
		for i := 0; i < per*(1+f%4); i++ {
			if i%3 == 1 {
				fmt.Fprintf(&b, "%d:%d:    let needle_%d = compute(%d);\n", 10+i*4, 5+i%7, i, i)
			} else {
				fmt.Fprintf(&b, "%d:    let needle_%d = compute(%d);\n", 10+i*4, i, i)
			}
			if i%2 == 0 {
				fmt.Fprintf(&b, "%d-    // context for needle %d\n", 11+i*4, i)
			}
		}
		if f%3 == 0 {
			b.WriteString("--\n")
		}
		b.WriteString("\n")
	}
	return b.Bytes()
}

func grepCount(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "src/dir%02d/file_%04d.go:%d\n", i%9, i, (i*7)%13)
	}
	return b.Bytes()
}

func grepFiles(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "src/dir%02d/file_%04d.py\n", i%11, i)
	}
	return b.Bytes()
}

func grepUnnumbered(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("docs/part%02d/page_%03d.md", i%6, i/3)
		fmt.Fprintf(&b, "%s-context before match %d\n", p, i)
		fmt.Fprintf(&b, "%s:the needle appears in sentence %d of this page\n", p, i)
		if i%10 == 0 {
			fmt.Fprintf(&b, "https://example.com:8080/path/%d is not a path\n", i)
		}
	}
	return b.Bytes()
}

func grepNotices(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		for k := 0; k < 1+i%5; k++ {
			fmt.Fprintf(&b, "lib/pkg%02d/unit_%03d.ts:%d: const needle%d = load(%d)\n", i%8, i, 3+k*11, k, i)
		}
	}
	b.WriteString("[100 matches limit reached. Use limit=200 for more]\n")
	b.WriteString("Found 4096 matches in 311 files\n")
	b.WriteString("[output truncated: 50KB limit reached]\n")
	return b.Bytes()
}

func permissionDenied(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "grep: /proc/%d/task: Permission denied\n", 1000+i)
	}
	return b.Bytes()
}

func eslintStylish(files, per int) []byte {
	var b bytes.Buffer
	for f := 0; f < files; f++ {
		fmt.Fprintf(&b, "/home/dev/app/src/component_%03d.js\n", f)
		for i := 0; i < per; i++ {
			sev := "warning"
			if (i+f)%4 == 0 {
				sev = "error"
			}
			fmt.Fprintf(&b, "  %d:%d  %s  'value%d' is assigned a value but never used  no-unused-vars\n", 10+i*3, 5+i%9, sev, i)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "✖ %d problems (%d errors, %d warnings)\n", files*per, files*per/4, files*per*3/4)
	return b.Bytes()
}

func tscAndVet(n int) []byte {
	var b bytes.Buffer
	b.WriteString("# example.com/app/internal/store\n")
	for i := 0; i < n; i++ {
		switch i % 3 {
		case 0:
			fmt.Fprintf(&b, "internal/store/db_%02d.go:%d:%d: printf: fmt.Sprintf format %%d has arg of wrong type\n", i%17, 20+i, 3)
		case 1:
			fmt.Fprintf(&b, "src/ui/view_%02d.ts(%d,%d): error TS2322: Type 'string' is not assignable to type 'number'.\n", i%13, 40+i, 9)
		default:
			fmt.Fprintf(&b, "src/ui/view_%02d.ts(%d,%d): warning TS6133: 'x' is declared but its value is never read.\n", i%13, 41+i, 2)
		}
	}
	b.WriteString("Found 200 errors in 13 files.\n")
	return b.Bytes()
}

func clippyOutput(n int) []byte {
	var b bytes.Buffer
	b.WriteString("    Checking app v0.1.0 (/home/dev/app)\n")
	for i := 0; i < n; i++ {
		if i%5 == 0 {
			fmt.Fprintf(&b, "error[E0308]: mismatched types in call %d\n", i)
		} else {
			fmt.Fprintf(&b, "warning: unused variable: `v%d`\n", i)
		}
		fmt.Fprintf(&b, "  --> src/module_%02d.rs:%d:9\n   |\n%d |     let v%d = compute();\n   |         ^^ help: prefix it with an underscore\n\n", i%7, 30+i, 30+i, i)
	}
	b.WriteString("error: could not compile `app` (lib) due to 30 previous errors; 120 warnings emitted\n")
	return b.Bytes()
}

func unifiedDiff(files int) []byte {
	var b bytes.Buffer
	for f := 0; f < files; f++ {
		old := fmt.Sprintf("a/lib/f%03d.c", f)
		if f%7 == 0 {
			old = "/dev/null"
		}
		fmt.Fprintf(&b, "--- %s\t2026-09-25 12:00:00\n+++ b/lib/f%03d.c\t2026-09-25 12:01:00\n", old, f)
		for h := 0; h < 2+f%3; h++ {
			fmt.Fprintf(&b, "@@ -%d,4 +%d,5 @@\n int a%d;\n-int b%d;\n+int c%d;\n+int d%d;\n int e%d;\n int f%d;\n", 5+h*30, 5+h*30, h, h, h, h, h, h)
		}
	}
	return b.Bytes()
}

func metaDiff(files int) []byte {
	var b bytes.Buffer
	b.WriteString("commit 0123456789abcdef0123456789abcdef01234567\nAuthor: Dev <dev@example.com>\nDate:   Thu Sep 25 12:00:00 2026 +0000\n\n    refactor: move the parser\n\n    Line two of the message.\n    Line three.\n    Line four is past the kept lines.\n\n")
	for f := 0; f < files; f++ {
		switch f % 4 {
		case 0:
			fmt.Fprintf(&b, "diff --git a/old/p%03d.go b/new/p%03d.go\nsimilarity index 90%%\nrename from old/p%03d.go\nrename to new/p%03d.go\nindex 1..2 100644\n--- a/old/p%03d.go\n+++ b/new/p%03d.go\n", f, f, f, f, f, f)
		case 1:
			fmt.Fprintf(&b, "diff --git a/new/n%03d.go b/new/n%03d.go\nnew file mode 100644\nindex 0000000..3\n--- /dev/null\n+++ b/new/n%03d.go\n", f, f, f)
		case 2:
			fmt.Fprintf(&b, "diff --git a/img/i%03d.png b/img/i%03d.png\nBinary files a/img/i%03d.png and b/img/i%03d.png differ\n", f, f, f, f)
			continue
		default:
			fmt.Fprintf(&b, "diff --git a/sql/s%03d.sql b/sql/s%03d.sql\ndeleted file mode 100644\nindex 4..0000000\n--- a/sql/s%03d.sql\n+++ /dev/null\n", f, f, f)
		}
		fmt.Fprintf(&b, "@@ -1,4 +1,4 @@\n-- a comment line\n--- deleted sql comment %d\n+++ added line that looks like a header %d\n context\n\\ No newline at end of file\n", f, f)
	}
	return b.Bytes()
}

func diffStat(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, " src/pkg%02d/file_%04d.go | %3d ++++++++-----\n", i%9, i, 3+i%40)
	}
	fmt.Fprintf(&b, " %d files changed, 9120 insertions(+), 4431 deletions(-)\n", n)
	return b.Bytes()
}

func bigHunk(lines int) []byte {
	var b bytes.Buffer
	b.WriteString("diff --git a/big.txt b/big.txt\nindex 1..2 100644\n--- a/big.txt\n+++ b/big.txt\n")
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", lines, lines+lines/4)
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, " unchanged line %d\n", i)
		if i%4 == 0 {
			fmt.Fprintf(&b, "+inserted line %d\n", i)
		}
	}
	b.WriteString("diff --git a/small.txt b/small.txt\nindex 3..4 100644\n--- a/small.txt\n+++ b/small.txt\n@@ -1,2 +1,2 @@\n-old\n+new\n ctx\n")
	return b.Bytes()
}

func mangledDiff(files int) []byte {
	var b bytes.Buffer
	for f := 0; f < files; f++ {
		fmt.Fprintf(&b, "diff --git a/m%03d.txt b/m%03d.txt\nindex 1..2 100644\n--- a/m%03d.txt\n+++ b/m%03d.txt\n", f, f, f, f)
		// the header announces more lines than follow, then prose interrupts
		fmt.Fprintf(&b, "@@ -1,9 +1,9 @@\n ctx\n-old %d\n+new %d\nplain prose line %d\n+later add %d\n", f, f, f, f)
		fmt.Fprintf(&b, "@@@ -1,2 -1,2 +1,3 @@@\n  merged ctx\n ++added in merge %d\n", f)
	}
	return b.Bytes()
}

func goPanic(pass int) []byte {
	var b bytes.Buffer
	for i := 0; i < pass; i++ {
		fmt.Fprintf(&b, "=== RUN   TestCase%04d\n--- PASS: TestCase%04d (0.00s)\n", i, i)
	}
	b.WriteString("=== RUN   TestCrash\npanic: runtime error: index out of range [5] with length 3 [recovered]\n\npanic: runtime error: index out of range [5] with length 3\n\n")
	b.WriteString("goroutine 18 [running]:\n")
	for k := 0; k < 20; k++ {
		fmt.Fprintf(&b, "example.com/pkg.step%d(0xc000012345, 0x%x)\n\t/src/pkg/step.go:%d +0x%x\n", k, k*16, 10+k, 0x40+k)
	}
	b.WriteString("created by testing.(*T).Run in goroutine 1\n\t/usr/local/go/src/testing/testing.go:1742 +0x390\n")
	b.WriteString("FAIL\texample.com/pkg\t0.051s\nFAIL\n")
	return b.Bytes()
}

func pythonTraceback(noise int) []byte {
	var b bytes.Buffer
	for i := 0; i < noise; i++ {
		fmt.Fprintf(&b, "INFO loading shard %d of %d\n", i, noise)
	}
	b.WriteString("Traceback (most recent call last):\n")
	for k := 0; k < 25; k++ {
		fmt.Fprintf(&b, "  File \"/app/svc/layer%d.py\", line %d, in handle%d\n    return handle%d(req)\n", k, 20+k, k, k+1)
	}
	b.WriteString("KeyError: 'user_id'\n\nDuring handling of the above exception, another exception occurred:\n\n")
	b.WriteString("Traceback (most recent call last):\n  File \"/app/main.py\", line 9, in <module>\n    run()\nRuntimeError: request failed\n")
	for i := 0; i < noise/3; i++ {
		fmt.Fprintf(&b, "INFO shutting down worker %d\n", i)
	}
	return b.Bytes()
}

func exitStatusRun(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "step %d: copying asset bundle part %d\n", i, i*3)
		if i%150 == 0 {
			fmt.Fprintf(&b, "\rprogress %d%%\rprogress %d%%\n", i/10, i/10+1)
		}
	}
	b.WriteString("make: *** [Makefile:12: all] Error 2\nProcess finished with exit code 2\n")
	return b.Bytes()
}

func logLines(n, errAt int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "2026-09-25T12:%02d:%02d.%03dZ INFO request id=%08x served in %dms\n", i/60%60, i%60, i%1000, i*2654435761%(1<<32), i%97)
		if i%500 == 250 {
			fmt.Fprintf(&b, "2026-09-25T12:%02d:%02d.000Z WARN slow query took %dms\n", i/60%60, i%60, 900+i)
		}
		if i == errAt {
			b.WriteString("2026-09-25T12:30:00.000Z ERROR database connection refused: dial tcp 10.0.0.5:5432\n")
			b.WriteString("2026-09-25T12:30:00.001Z FATAL giving up after 5 retries\n")
		}
	}
	return b.Bytes()
}

func makeBuild(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "cc -O2 -c src/unit_%04d.c -o build/unit_%04d.o\n", i, i)
		if i%200 == 100 {
			fmt.Fprintf(&b, "src/unit_%04d.c:%d:5: warning: unused variable 'tmp' [-Wunused-variable]\n", i, 10+i%50)
		}
		if i == n*2/3 {
			fmt.Fprintf(&b, "src/unit_%04d.c:42:10: error: 'undeclared_thing' undeclared (first use in this function)\n", i)
			b.WriteString("   42 |   return undeclared_thing + 1;\n      |          ^~~~~~~~~~~~~~~~\n")
		}
	}
	b.WriteString("make: *** [Makefile:30: build/unit.o] Error 1\n1 error generated.\n")
	return b.Bytes()
}

func gitStatus(n int) []byte {
	var b bytes.Buffer
	b.WriteString("On branch feature/x\nYour branch is ahead of 'origin/feature/x' by 3 commits.\n\nChanges not staged for commit:\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\tmodified:   src/area%02d/file_%04d.go\n", i%12, i)
	}
	b.WriteString("\nerror: pathspec 'missing' did not match any file(s) known to git\nCONFLICT (content): Merge conflict in src/a.go\n")
	return b.Bytes()
}

func structuredDoc(n int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"ok":false,"status":"failed","exit_code":2,"runs":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		state := "passed"
		if i == n/2 {
			state = "error: timeout waiting for runner"
		}
		fmt.Fprintf(&b, `{"id":%d,"job":"job-%05d","state":%q,"log":"%s"}`, i, i, state, strings.Repeat("x", 40))
	}
	fmt.Fprintf(&b, `],"summary":{"total":%d,"failed":1,"message":"%s"}}`, n, strings.Repeat("summary words ", 80))
	return b.Bytes()
}

func salientArray(n int) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		switch {
		case i%400 == 399:
			fmt.Fprintf(&b, `{"i":%d,"result":"FAILED: assertion failed at step %d","trace":"%s"}`, i, i, strings.Repeat("frame ", 90))
		case i%97 == 0:
			fmt.Fprintf(&b, `[%d,%d,%d]`, i, i+1, i+2)
		default:
			fmt.Fprintf(&b, `{"i":%d,"result":"ok","note":"item %d is fine"}`, i, i)
		}
	}
	b.WriteByte(']')
	return b.Bytes()
}

func nestedDoc(n int) []byte {
	doc := map[string]any{"status": "degraded", "exit_code": 3}
	var groups []map[string]any
	for g := 0; g < 6; g++ {
		var items []any
		for i := 0; i < n; i++ {
			item := map[string]any{"id": g*1000 + i, "ok": !(g == 4 && i == n-3), "log": strings.Repeat(fmt.Sprintf("entry %d fine; ", i), 3)}
			items = append(items, item)
		}
		groups = append(groups, map[string]any{"name": fmt.Sprintf("group/%d", g), "items": items, "blob": strings.Repeat("b", 3000+g*500)})
	}
	doc["groups"] = groups
	doc["tail~key"] = strings.Repeat("the error is near the end ", 300)
	out, _ := json.Marshal(doc)
	return out
}

func bigFirstElement() []byte {
	return []byte(`[{"payload":"` + strings.Repeat("large ", 4000) + `"},{"small":1},{"small":2}]`)
}

func longLines() []byte {
	var b bytes.Buffer
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "line %d normal output\n", i)
		if i == 150 {
			b.WriteString(strings.Repeat("a", 9000) + " error: buried failure at the end of a long line\n")
		}
		if i == 220 {
			b.WriteString(strings.Repeat("z", 6000) + "\n")
		}
		if i == 300 {
			b.WriteString("invalid bytes \xff\xfe in this error line\n")
		}
	}
	b.WriteString("final line without a newline")
	return b.Bytes()
}

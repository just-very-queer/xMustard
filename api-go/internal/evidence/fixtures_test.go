package evidence

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
)

// Deterministic tool-output fixtures for the family reducers.

// unittestRun: 5,000 passing tests and one failing assertion with a 12-frame trace.
func unittestRun(pass, frames int) []byte {
	var b bytes.Buffer
	failAt := pass / 2
	for i := 0; i <= pass; i++ {
		if i == failAt {
			b.WriteString("test_parse_value (tests.test_mod.TestMod.test_parse_value) ... FAIL\n")
			continue
		}
		fmt.Fprintf(&b, "test_case_%04d (tests.test_mod.TestMod.test_case_%04d) ... ok\n", i, i)
	}
	b.WriteString("\n======================================================================\n")
	b.WriteString("FAIL: test_parse_value (tests.test_mod.TestMod.test_parse_value)\n")
	b.WriteString("----------------------------------------------------------------------\n")
	b.WriteString("Traceback (most recent call last):\n")
	b.WriteString("  File \"/src/tests/test_mod.py\", line 42, in test_parse_value\n")
	b.WriteString("    self.assertEqual(parse(\"3\"), 4)\n")
	for k := 0; k < frames; k++ {
		fmt.Fprintf(&b, "  File \"/src/mod/layer%d.py\", line %d, in step%d\n", k, 10+k, k)
		fmt.Fprintf(&b, "    return step%d(value)\n", k+1)
	}
	b.WriteString("  File \"/src/mod/core.py\", line 88, in convert\n")
	b.WriteString("    return int(s) + 1\n")
	b.WriteString("AssertionError: 3 != 4\n\n")
	b.WriteString("----------------------------------------------------------------------\n")
	fmt.Fprintf(&b, "Ran %d tests in 12.345s\n\n", pass+1)
	b.WriteString("FAILED (failures=1)\n")
	return b.Bytes()
}

// goTestRun: go test -v with n passing tests and one failing test with a panic trace.
func goTestRun(pass int) []byte {
	var b bytes.Buffer
	failAt := pass / 2
	for i := 0; i < pass; i++ {
		if i == failAt {
			b.WriteString("=== RUN   TestParseValue\n")
			b.WriteString("    parse_test.go:17: parse(\"3\") = 3, want 4\n")
			b.WriteString("--- FAIL: TestParseValue (0.00s)\n")
		}
		fmt.Fprintf(&b, "=== RUN   TestCase%04d\n--- PASS: TestCase%04d (0.00s)\n", i, i)
	}
	b.WriteString("FAIL\nFAIL\texample.com/pkg\t0.412s\nFAIL\n")
	return b.Bytes()
}

func reduceWith(t testing.TB, raw []byte, sel Selector, target int) (*Projection, Record) {
	t.Helper()
	reg := DefaultRegistry()
	red, argv0 := reg.Select(sel)
	h := &reduceHook{reducer: red, argv0: argv0, in: Input{Sel: sel, Target: target}}
	ctx := withReduceHook(context.Background(), h)
	_, rec, err := Reduce(ctx, bytes.NewReader(raw), int64(len(raw)), "text/plain", 64<<10, 1<<20)
	if err != nil {
		t.Fatalf("reduce: %v", err)
	}
	return h.out, rec
}

func grepOutput(files, perFile int) []byte {
	var b bytes.Buffer
	for f := 0; f < files; f++ {
		n := perFile
		if f == 3 {
			n = perFile * 20
		}
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "src/pkg%02d/file_%03d.go:%d:\tfoo := callSomething(%d) // matches needle\n", f%7, f, 10+i*3, i)
		}
	}
	return b.Bytes()
}

func sourceFile(lines int) []byte {
	var b bytes.Buffer
	b.WriteString("package big\n\nimport \"fmt\"\n\n")
	for i := 0; b.Len() < lines*40; i++ {
		fmt.Fprintf(&b, "// Func%d does step %d.\nfunc Func%d(x int) int {\n\ty := x * %d\n\tfmt.Println(y)\n\treturn y + %d\n}\n\n", i, i, i, i, i)
	}
	return b.Bytes()
}

func lsOutput(n int) []byte {
	var b bytes.Buffer
	b.WriteString("total 1234\n")
	for i := 0; i < n; i++ {
		ext := []string{".go", ".ts", ".md", ".json"}[i%4]
		if i%9 == 0 {
			fmt.Fprintf(&b, "drwxr-xr-x  5 dev staff   160 Sep 25 12:00 dir_%04d\n", i)
			continue
		}
		fmt.Fprintf(&b, "-rw-r--r--  1 dev staff  %4d Sep 25 12:00 file_%04d%s\n", i*7%9000, i, ext)
	}
	return b.Bytes()
}

func globOutput(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		top := []string{"src", "test", "docs", "scripts"}[i%4]
		fmt.Fprintf(&b, "%s/mod%02d/file_%04d.ts\n", top, i%13, i)
	}
	return b.Bytes()
}

func diffOutput(files, hunks int) []byte {
	var b bytes.Buffer
	for f := 0; f < files; f++ {
		fmt.Fprintf(&b, "diff --git a/src/f%03d.go b/src/f%03d.go\nindex 1111111..2222222 100644\n--- a/src/f%03d.go\n+++ b/src/f%03d.go\n", f, f, f, f)
		for h := 0; h < hunks; h++ {
			fmt.Fprintf(&b, "@@ -%d,7 +%d,8 @@ func F%d() {\n \tctx := 1\n \tctx2 := 2\n-\told := %d\n+\tnew := %d\n+\tadded := %d\n \tctx3 := 3\n \tctx4 := 4\n", 10+h*20, 10+h*20, h, h, h, h)
		}
	}
	return b.Bytes()
}

func lintOutput(files, per int) []byte {
	var b bytes.Buffer
	for f := 0; f < files; f++ {
		for i := 0; i < per; i++ {
			sev := "warning"
			if i%5 == 0 {
				sev = "error"
			}
			fmt.Fprintf(&b, "src/a%02d.py:%d:5: %s: something is wrong here (rule-%d)\n", f, 10+i, sev, i%7)
		}
	}
	b.WriteString("Found 400 errors.\n")
	return b.Bytes()
}

func noisyShell() []byte {
	var b bytes.Buffer
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "Downloading package %d of 2000 [%3d%%]\r", i, i/20)
	}
	b.WriteString("\n")
	for i := 0; i < 3000; i++ {
		b.WriteString("waiting for lock on build directory\n")
	}
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "processing item %d ok\n", i)
	}
	b.WriteString("error: disk quota exceeded while writing /tmp/out.bin\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "cleanup %d\n", i)
	}
	b.WriteString("Exit code 3\n")
	return b.Bytes()
}

func jestRun(pass int) []byte {
	var b bytes.Buffer
	b.WriteString(" FAIL  tests/parser.test.js\n  Parser\n")
	for i := 0; i < pass; i++ {
		fmt.Fprintf(&b, "    ✓ case %04d (1 ms)\n", i)
	}
	b.WriteString("    ✕ parses values (3 ms)\n\n")
	b.WriteString("  ● Parser › parses values\n\n    expect(received).toBe(expected) // Object.is equality\n\n    Expected: 4\n    Received: 3\n\n")
	b.WriteString("      41 |     const v = parse('3');\n    > 42 |     expect(v).toBe(4);\n         |               ^\n\n")
	b.WriteString("      at Object.<anonymous> (tests/parser.test.js:42:15)\n")
	for k := 0; k < 30; k++ {
		fmt.Fprintf(&b, "      at step%d (node_modules/jest-circus/build/run.js:%d:9)\n", k, 100+k)
	}
	b.WriteString("      at processTicksAndRejections (node:internal/process/task_queues:95:5)\n\n")
	fmt.Fprintf(&b, "Test Suites: 1 failed, 1 total\nTests:       1 failed, %d passed, %d total\nSnapshots:   0 total\nTime:        4.2 s\n", pass, pass+1)
	return b.Bytes()
}

func cargoRun(pass int) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "running %d tests\n", pass+1)
	for i := 0; i < pass; i++ {
		fmt.Fprintf(&b, "test tests::case_%04d ... ok\n", i)
	}
	b.WriteString("test tests::parse_value ... FAILED\n\nfailures:\n\n---- tests::parse_value stdout ----\n")
	b.WriteString("thread 'tests::parse_value' panicked at src/lib.rs:42:9:\nassertion `left == right` failed\n  left: 3\n right: 4\n")
	b.WriteString("note: run with `RUST_BACKTRACE=1` environment variable to display a backtrace\n\n\nfailures:\n    tests::parse_value\n\n")
	fmt.Fprintf(&b, "test result: FAILED. %d passed; 1 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.52s\n\nerror: test failed, to rerun pass `--lib`\n", pass)
	return b.Bytes()
}

// repeatReaderAt serves n bytes of a repeating pattern without storing them, so a
// test can reduce a very large original with the reducer's allocations alone.
type repeatReaderAt struct {
	pat []byte
	n   int64
}

func (r repeatReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.n {
		return 0, io.EOF
	}
	k := 0
	for k < len(p) && off+int64(k) < r.n {
		pos := (off + int64(k)) % int64(len(r.pat))
		k += copy(p[k:min(len(p), k+int(int64(len(r.pat))-pos), int(r.n-off))], r.pat[pos:])
	}
	if off+int64(k) >= r.n {
		return k, io.EOF
	}
	return k, nil
}

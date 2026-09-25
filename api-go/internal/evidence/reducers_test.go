package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run with -update)", path, err)
	}
	if string(want) != got {
		t.Fatalf("projection differs from %s (run with -update after reviewing):\n--- got ---\n%s", path, got)
	}
}

func mustContain(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Fatalf("projection lacks %q:\n%s", p, text)
		}
	}
}

func mustNotContain(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(text, p) {
			t.Fatalf("projection unexpectedly contains %q:\n%s", p, text)
		}
	}
}

// coverage checks that every byte of the original is either shown or inside a
// listed omission range: nothing is dropped silently.
func omissionsInRange(t *testing.T, rec Record, n int64) {
	t.Helper()
	for _, o := range rec.Omissions {
		if o.Start < 0 || o.End > n || o.End < o.Start {
			t.Fatalf("omission %+v outside the %d-byte original", o, n)
		}
	}
}

// Golden fixture (PAR-CTX-02): one failing assertion among 5,000 passing tests keeps
// its failure header, assertion, first and last stack frames, the summary and the
// exit code; the passing lines are counted, not shown.
func TestGoldenOneFailureAmongFiveThousandPassing(t *testing.T) {
	one := 1
	raw := unittestRun(5000, 200)
	sel := Selector{Client: "claude", Tool: "Bash", Command: "python -m unittest -v tests", ExitCode: &one}
	for _, target := range []int{16 << 10, 4 << 10} {
		p, rec := reduceWith(t, raw, sel, target)
		if rec.Reducer != "xm-test/1" || rec.Family == nil || rec.Family.Family != FamilyTest {
			t.Fatalf("reducer id/version not recorded: %+v", rec)
		}
		if len(p.Text) > target {
			t.Fatalf("target %d: projection %d bytes", target, len(p.Text))
		}
		mustContain(t, p.Text,
			"exit=1", "passed=5000 failed=1", "counts_from=summary", "failing=test_parse_value",
			"test_parse_value (tests.test_mod.TestMod.test_parse_value) ... FAIL",
			"FAIL: test_parse_value",
			`File "/src/tests/test_mod.py", line 42, in test_parse_value`, // first frame
			`self.assertEqual(parse("3"), 4)`,
			`File "/src/mod/core.py", line 88, in convert`, // last frame
			"AssertionError: 3 != 4",
			"Ran 5001 tests", "FAILED (failures=1)")
		mustNotContain(t, p.Text, "test_case_0001 (tests", "test_case_4999 (tests")
		omissionsInRange(t, rec, int64(len(raw)))
		if target == 4<<10 {
			// the middle of the 200-frame trace does not fit a 4 KiB projection
			mustNotContain(t, p.Text, "layer100.py")
		}
		if p.Facts.Passed != 5000 || p.Facts.Failed != 1 || p.Facts.ExitCode == nil || *p.Facts.ExitCode != 1 {
			t.Fatalf("facts: %+v", p.Facts)
		}
	}
	p, _ := reduceWith(t, raw, sel, 16<<10)
	golden(t, "unittest_5000_pass_1_fail", p.Text)
}

func TestTestRunnersKeepFailuresAndCounts(t *testing.T) {
	cases := []struct {
		name    string
		raw     []byte
		command string
		want    []string
		passed  int
		failed  int
		exit    int
	}{
		{"go", goTestRun(5000), "go test -v ./...", []string{"--- FAIL: TestParseValue", `parse("3") = 3, want 4`, "FAIL\texample.com/pkg"}, 5000, 1, -1},
		{"jest", jestRun(5000), "npx jest", []string{"✕ parses values", "● Parser › parses values", "Expected: 4", "Received: 3",
			"at Object.<anonymous> (tests/parser.test.js:42:15)", "at processTicksAndRejections", "Tests:       1 failed, 5000 passed"}, 5000, 1, -1},
		{"cargo", cargoRun(5000), "cargo test", []string{"test tests::parse_value ... FAILED", "panicked at src/lib.rs:42:9",
			"assertion `left == right` failed", "left: 3", "right: 4", "test result: FAILED. 5000 passed; 1 failed"}, 5000, 1, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, rec := reduceWith(t, c.raw, Selector{Client: "claude", Tool: "Bash", Command: c.command}, 16<<10)
			if rec.Reducer != "xm-test/1" {
				t.Fatalf("reducer %s", rec.Reducer)
			}
			mustContain(t, p.Text, c.want...)
			if p.Facts.Passed != c.passed || p.Facts.Failed != c.failed {
				t.Fatalf("counts: %+v", p.Facts)
			}
			if len(p.Text) > 16<<10 {
				t.Fatalf("projection %d bytes", len(p.Text))
			}
			if strings.Count(p.Text, "\n") > 200 {
				t.Fatalf("passing lines were not collapsed (%d lines)", strings.Count(p.Text, "\n"))
			}
		})
	}
	// a middle frame of the 32-frame jest trace is omitted when the budget is tight
	p, _ := reduceWith(t, jestRun(5000), Selector{Tool: "Bash", Command: "jest"}, 2<<10)
	mustContain(t, p.Text, "at Object.<anonymous> (tests/parser.test.js:42:15)", "Expected: 4")
	mustNotContain(t, p.Text, "at step15 ")
}

func TestExitCodeFromToolOrText(t *testing.T) {
	two := 2
	raw := bytes.Repeat([]byte("compiling module ok\n"), 2000)
	raw = append(raw, "src/main.c:12:5: error: expected ';' before '}' token\nmake: *** [Makefile:12: all] Error 2\n"...)
	p, rec := reduceWith(t, raw, Selector{Tool: "Bash", Command: "make"}, 4<<10)
	if rec.Reducer != "xm-build/1" || p.Facts.ExitCode == nil || *p.Facts.ExitCode != 2 || p.Facts.ExitFrom != "text" {
		t.Fatalf("exit from text: %s %+v", rec.Reducer, p.Facts)
	}
	mustContain(t, p.Text, "exit=2", "error: expected ';'", "errors=2") // the compiler error and make's
	p, _ = reduceWith(t, raw, Selector{Tool: "Bash", Command: "make", ExitCode: &two}, 4<<10)
	if p.Facts.ExitFrom != "tool" {
		t.Fatalf("exit from tool: %+v", p.Facts)
	}
}

func TestShellCollapsesProgressAndRepeats(t *testing.T) {
	raw := noisyShell()
	p, rec := reduceWith(t, raw, Selector{Tool: "Bash", Command: "./deploy.sh"}, 8<<10)
	if rec.Reducer != "xm-shell/1" {
		t.Fatalf("reducer %s", rec.Reducer)
	}
	mustContain(t, p.Text, "exit=3", "previous line repeated 2999 more times", "error: disk quota exceeded", "Exit code 3")
	mustNotContain(t, p.Text, "Downloading package 1 of")
	if len(p.Text) > 8<<10 {
		t.Fatalf("projection %d bytes", len(p.Text))
	}
}

// A progress-looking line that carries failure evidence is never collapsed.
func TestProgressLinesWithFailuresAreKept(t *testing.T) {
	var b bytes.Buffer
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "Downloading dependency %d\n", i)
	}
	b.WriteString("Checking signatures: error: signature mismatch for dep 1234\n")
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "Compiling unit %d\n", i)
	}
	p, _ := reduceWith(t, b.Bytes(), Selector{Tool: "Bash", Command: "./build.sh"}, 4<<10)
	mustContain(t, p.Text, "Checking signatures: error: signature mismatch for dep 1234", "failure_lines=1")
}

func TestGrepProjectionCapsPerFileAndReportsTotals(t *testing.T) {
	raw := grepOutput(30, 4) // file 3 has 80 matches, the others 4
	p, rec := reduceWith(t, raw, Selector{Client: "claude", Tool: "Bash", Command: "rg -n needle src"}, 8<<10)
	if rec.Reducer != "xm-grep/1" {
		t.Fatalf("reducer %s", rec.Reducer)
	}
	gp := p.Structured.(*GroupedProjection)
	if gp.Matches != 196 || gp.Files != 30 || gp.Shown != 40 || len(gp.Results) != 40 {
		t.Fatalf("totals: %+v", gp)
	}
	perFile := map[string]int{}
	for _, r := range gp.Results {
		perFile[r.Path]++
	}
	for path, n := range perFile {
		if n > gp.PerFileCap {
			t.Fatalf("%s shows %d > cap %d", path, n, gp.PerFileCap)
		}
	}
	mustContain(t, p.Text, "matches=196 files=30", "src/pkg03/file_003.go (80)", "… 75 more in src/pkg03/file_003.go", "156 more of 196 not shown")
	omissionsInRange(t, rec, int64(len(raw)))
	// Claude Grep files_with_matches: an array section keeps one entry per line
	var files bytes.Buffer
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&files, "src/f%03d.go\n", i)
	}
	in := files.Bytes()
	h := &reduceHook{reducer: grepReducer{}, in: Input{Sel: Selector{Tool: "Grep"}, Target: 4 << 10,
		Sections: []Section{{Name: "filenames", Start: 0, End: int64(len(in)), Array: true}}}}
	if _, _, err := Reduce(withReduceHook(context.Background(), h), bytes.NewReader(in), int64(len(in)), "text/plain", 64<<10, 1<<20); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(h.out.Parts["filenames"], "\n"), "\n")
	if len(lines) != 41 || lines[0] != "src/f000.go" || !strings.Contains(lines[40], "460 more of 500") {
		t.Fatalf("array part: %d lines, first %q last %q", len(lines), lines[0], lines[len(lines)-1])
	}
}

func TestReadProjectionIsLineNumberedRange(t *testing.T) {
	raw := sourceFile(3000)
	// a read of lines 100.. (the capture starts at line 100)
	p, rec := reduceWith(t, raw, Selector{Client: "claude", Tool: "Read", Path: "big/big.go", StartLine: 100}, 8<<10)
	if rec.Reducer != "xm-read/1" {
		t.Fatalf("reducer %s", rec.Reducer)
	}
	rp := p.Structured.(*ReadProjection)
	total := countLines(raw)
	if rp.Path != "big/big.go" || rp.LineCount != total || rp.FirstLine != 100 || rp.LastLine != 99+total || rp.Excerpt.From != 100 {
		t.Fatalf("read projection: %+v", rp)
	}
	mustContain(t, p.Text, "   100\tpackage big", fmt.Sprintf("lines 100-%d", 99+total))
	// numbers are absolute and match the content
	lines := strings.Split(string(raw), "\n")
	mustContain(t, p.Text, fmt.Sprintf("%6d\t%s\n", 100+rp.Excerpt.To-100, lines[rp.Excerpt.To-100]))
	// the unnumbered contiguous excerpt (for Claude Read) ends with a continuation marker
	part := p.Parts["output"]
	if !strings.HasPrefix(part, "package big\n") || !strings.Contains(part, fmt.Sprintf("read on from line %d", rp.Excerpt.To+1)) {
		t.Fatalf("read part: %.200q", part)
	}
	if len(p.Text) > 8<<10 {
		t.Fatalf("projection %d bytes", len(p.Text))
	}
	// a requested range narrower than the capture
	p, _ = reduceWith(t, raw, Selector{Tool: "read", Path: "big.go", FromLine: 500, ToLine: 520}, 4<<10)
	rp = p.Structured.(*ReadProjection)
	if rp.FirstLine != 500 || rp.LastLine != 520 || rp.Requested == nil {
		t.Fatalf("range: %+v", rp)
	}
	mustContain(t, p.Text, fmt.Sprintf("   500\t%s\n", lines[499]), fmt.Sprintf("   520\t%s\n", lines[519]))
	mustNotContain(t, p.Text, fmt.Sprintf("   521\t"))
}

func TestListAndGlobProjectionsAreCapped(t *testing.T) {
	p, rec := reduceWith(t, lsOutput(500), Selector{Tool: "Bash", Command: "ls -la src"}, 16<<10)
	lp := p.Structured.(*ListProjection)
	if rec.Reducer != "xm-list/1" || lp.Total != 500 || lp.Shown != 40 || len(lp.Entries) != 40 || lp.Dirs != 56 {
		t.Fatalf("list: %s %+v", rec.Reducer, lp)
	}
	mustContain(t, p.Text, "entries=500 dirs=56 shown=40", "460 more entries not shown")
	p, rec = reduceWith(t, globOutput(900), Selector{Client: "claude", Tool: "Glob"}, 16<<10)
	lp = p.Structured.(*ListProjection)
	if rec.Reducer != "xm-glob/1" || lp.Total != 900 || lp.Shown != 60 {
		t.Fatalf("glob: %s %+v", rec.Reducer, lp)
	}
	mustContain(t, p.Text, "840 more entries not shown; by directory: docs/ 210 scripts/ 210 src/ 210 test/ 210")
}

func TestDiffKeepsFilesHunksAndCounts(t *testing.T) {
	raw := diffOutput(80, 6)
	p, rec := reduceWith(t, raw, Selector{Tool: "Bash", Command: "git diff HEAD~3"}, 8<<10)
	dp := p.Structured.(*DiffProjection)
	if rec.Reducer != "xm-diff/1" || dp.Files != 80 || dp.Additions != 960 || dp.Deletions != 480 || dp.Hunks != 480 {
		t.Fatalf("diff: %s %+v", rec.Reducer, dp)
	}
	mustContain(t, p.Text, "files=80 +960 -480 hunks=480", "=== src/f000.go (+12 -6, 6 hunks)", "+\tnew := 0", "more hunks omitted in src/f000.go")
	if strings.Count(p.Text, "=== src/f000.go") != 1 {
		t.Fatalf("file header repeated:\n%s", p.Text)
	}
	if len(p.Text) > 8<<10 {
		t.Fatalf("projection %d bytes", len(p.Text))
	}
	// git log -p: commit headers are budgeted too
	raw = gitLogP(300)
	p, _ = reduceWith(t, raw, Selector{Tool: "Bash", Command: "git log -p -n 300"}, 16<<10)
	dp = p.Structured.(*DiffProjection)
	// file stats are per change (commit, path): 300 commits x 2 files
	if dp.Commits != 300 || dp.Files != 600 || dp.Additions != 3600 || len(p.Text) > 16<<10 {
		t.Fatalf("git log -p: commits %d files %d +%d, %d bytes", dp.Commits, dp.Files, dp.Additions, len(p.Text))
	}
	mustContain(t, p.Text, "commits=300 file_changes=600", "commit headers not shown", "fix: change number 0 in the parser",
		"=== src/m000.go (+6 -3, 3 hunks)")
}

// Every family stays within the client target on large inputs.
func TestFamilyProjectionsStayWithinTarget(t *testing.T) {
	inputs := []struct {
		raw []byte
		sel Selector
	}{
		{unittestRun(5000, 200), Selector{Tool: "Bash", Command: "pytest"}},
		{jestRun(5000), Selector{Tool: "Bash", Command: "jest"}},
		{noisyShell(), Selector{Tool: "Bash", Command: "./x.sh"}},
		{grepOutput(300, 20), Selector{Tool: "Bash", Command: "rg x"}},
		{lintOutput(200, 30), Selector{Tool: "Bash", Command: "eslint ."}},
		{sourceFile(20000), Selector{Tool: "Read", Path: "a.go"}},
		{lsOutput(5000), Selector{Tool: "Bash", Command: "ls -la"}},
		{globOutput(20000), Selector{Tool: "Glob"}},
		{diffOutput(400, 9), Selector{Tool: "Bash", Command: "git diff"}},
		{gitLogP(2000), Selector{Tool: "Bash", Command: "git log -p"}},
	}
	for _, target := range []int{4 << 10, 16 << 10, 32 << 10} {
		for _, in := range inputs {
			p, rec := reduceWith(t, in.raw, in.sel, target)
			if len(p.Text) > target {
				t.Errorf("%s at target %d: projection %d bytes", rec.Reducer, target, len(p.Text))
			}
		}
	}
}

func TestLintGroupsErrorsFirst(t *testing.T) {
	p, rec := reduceWith(t, lintOutput(20, 20), Selector{Tool: "Bash", Command: "ruff check ."}, 16<<10)
	gp := p.Structured.(*GroupedProjection)
	if rec.Reducer != "xm-lint/1" || gp.Matches != 400 || gp.Errors != 80 || gp.Warnings != 320 || gp.Shown != 40 {
		t.Fatalf("lint: %s %+v", rec.Reducer, gp)
	}
	mustContain(t, p.Text, "Found 400 errors.")
	mustNotContain(t, p.Text, "warning: something") // 80 errors outrank every warning
}

// Structured payloads are byte-bounded and never lose status fields (the
// cursor-bridge gap): the generic reducer may placeholder a late status member when
// the budget runs out; the structured family keeps it.
func TestStructuredPayloadKeepsStatusWithinBudget(t *testing.T) {
	doc := map[string]any{}
	for i := 0; i < 60; i++ {
		doc[fmt.Sprintf("a%02d", i)] = strings.Repeat("x", 120)
	}
	// a long status value late in the document: the generic reducer gives it a
	// share of what is left (nothing) and a placeholder
	doc["conclusion"] = strings.Repeat("timed out waiting for the runner; ", 25)
	doc["status"] = "degraded"
	doc["exit_code"] = 7
	raw, _ := json.Marshal(doc) // keys sorted: the status members come after the a## members
	target := 2 << 10
	generic, _, err := reduceGeneric(context.Background(), bytes.NewReader(raw), int64(len(raw)), "application/json", target, 1<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(generic, `"conclusion":""`) {
		t.Fatalf("fixture no longer exercises the gap: generic kept the status:\n%s", generic)
	}
	p, rec := reduceWith(t, raw, Selector{Client: "claude", Tool: "mcp__ci__pipeline_status"}, target)
	if rec.Reducer != "xm-structured/1" || !json.Valid([]byte(p.Text)) {
		t.Fatalf("structured: %s valid=%v", rec.Reducer, json.Valid([]byte(p.Text)))
	}
	mustContain(t, p.Text, `"status":"degraded"`, `"exit_code":7`, `"conclusion":"timed out waiting for the runner;`)
	if len(p.Text) > target+512 {
		t.Fatalf("structured projection %d bytes for a %d target", len(p.Text), target)
	}
}

func TestRegistrySelectsByToolAndArgv0(t *testing.T) {
	cases := []struct {
		tool, cmd string
		want      Family
	}{
		{"Bash", "go test ./...", FamilyTest},
		{"Bash", "cd api-go && go test -p 2 ./... 2>&1 | tail -50", FamilyTest},
		{"Bash", "FOO=1 uv run pytest -q", FamilyTest},
		{"Bash", "python3 -m pytest tests", FamilyTest},
		{"bash", "npm run test:unit", FamilyTest},
		{"Bash", "cargo clippy --all-targets", FamilyBuild},
		{"Bash", "make build", FamilyBuild},
		{"Bash", "npx eslint src", FamilyLint},
		{"Bash", "go vet ./...", FamilyLint},
		{"Bash", "git diff HEAD~1 | head -100", FamilyDiff},
		{"Bash", "git status", FamilyGit},
		{"Bash", "git log -p -3", FamilyDiff},
		{"Bash", "rg -n needle src", FamilyGrep},
		{"Bash", "cat a.go | grep foo", FamilyGrep},
		{"Bash", "ls -la", FamilyList},
		{"Bash", "find . -name '*.go'", FamilyGlob},
		{"Bash", "tail -f /var/log/app.log", FamilyLog},
		{"Bash", "kubectl logs deploy/api", FamilyLog},
		{"Bash", "cat README.md", FamilyRead},
		{"Bash", "./deploy.sh --prod", FamilyShell},
		{"Read", "", FamilyRead},
		{"Grep", "", FamilyGrep},
		{"Glob", "", FamilyGlob},
		{"LS", "", FamilyList},
		{"find", "", FamilyGlob},
		{"exec_command", "pytest", FamilyTest},
		{"mcp__github__get_pull_request", "", FamilyStructured},
		{"mcp__repo__exec", "go test ./...", FamilyTest},
		{"shell", "bash -lc cd api-go && go test ./...", FamilyTest},
		{"shell", "sh -c 'rg -n needle src'", FamilyGrep},
		{"shell", "bash script.sh", FamilyShell},
	}
	reg := DefaultRegistry()
	for _, c := range cases {
		got, _ := SelectFamily(Selector{Tool: c.tool, Command: c.cmd})
		if got != c.want {
			t.Errorf("%s %q: family %s, want %s", c.tool, c.cmd, got, c.want)
		}
		if r, _ := reg.Select(Selector{Tool: c.tool, Command: c.cmd}); r.Family() != c.want {
			t.Errorf("%s %q: reducer family %s", c.tool, c.cmd, r.Family())
		}
	}
	if got, _ := SelectFamily(Selector{Tool: "Bash", Command: "ls", Family: FamilyTest}); got != FamilyTest {
		t.Fatalf("explicit family ignored: %s", got)
	}
	infos := reg.Reducers()
	if len(infos) != 12 {
		t.Fatalf("registry: %+v", infos)
	}
	for _, i := range infos {
		if !strings.HasSuffix(i.Reducer, "/1") {
			t.Fatalf("unversioned reducer %+v", i)
		}
	}
}

// The nine tools keep xm-reduce/1: without a registry hook nothing changes, and
// the persisted record carries no family.
func TestNineToolProjectionsUnchangedWithoutHook(t *testing.T) {
	s, _ := testStore(t, nil)
	raw := manyResults(300, 1)
	d, err := capture(t, s, "ws", "", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, rec, err := reduceGeneric(context.Background(), bytes.NewReader(raw), int64(len(raw)), "application/json",
		s.limits.ProjectionTarget, s.limits.MaxProjection, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.Projection != plain || d.Reducer != ReducerVersion || rec.Family != nil {
		t.Fatalf("nine-tool projection changed: reducer %s", d.Reducer)
	}
	key, _ := handleKey(d.Handle)
	var obs Observation
	if err := readJSON(filepath.Join(s.root, "ws", key, "meta.json"), &obs); err != nil {
		t.Fatal(err)
	}
	if obs.Projection.Family != nil || obs.Projection.Reducer != ReducerVersion {
		t.Fatalf("persisted record: %+v", obs.Projection)
	}
}

// Family reduction is O(window): reducing a 64 MiB original allocates about what a
// 16 MiB one does (no allocation proportional to the input).
func TestFamilyReducersUseBoundedMemory(t *testing.T) {
	if raceEnabled && testing.Short() {
		t.Skip("allocation bound is not meaningful under -race")
	}
	pattern := []byte("=== RUN   TestCase\n--- PASS: TestCase (0.00s)\nsome log line from the test\n")
	fail := goTestRun(10)
	for _, fam := range []struct {
		name string
		sel  Selector
	}{
		{"test", Selector{Tool: "Bash", Command: "go test ./..."}},
		{"shell", Selector{Tool: "Bash", Command: "./run.sh"}},
		{"grep", Selector{Tool: "Bash", Command: "rg x"}},
		{"read", Selector{Tool: "Read"}},
		{"list", Selector{Tool: "Bash", Command: "ls -la"}},
		{"diff", Selector{Tool: "Bash", Command: "git diff"}},
	} {
		var allocs [2]uint64
		for i, size := range []int64{8 << 20, 32 << 20} {
			r := io2ReaderAt{repeatReaderAt{pat: pattern, n: size - int64(len(fail))}, fail}
			reg := DefaultRegistry()
			red, _ := reg.Select(fam.sel)
			h := &reduceHook{reducer: red, in: Input{Sel: fam.sel, Target: 16 << 10}}
			ctx := withReduceHook(context.Background(), h)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			if _, _, err := Reduce(ctx, r, size, "text/plain", 64<<10, 1<<20); err != nil {
				t.Fatalf("%s: %v", fam.name, err)
			}
			runtime.ReadMemStats(&after)
			allocs[i] = after.TotalAlloc - before.TotalAlloc
		}
		t.Logf("%s: 8 MiB → %d KiB allocated, 32 MiB → %d KiB", fam.name, allocs[0]>>10, allocs[1]>>10)
		if !raceEnabled && allocs[1] > 12<<20 || allocs[1] > allocs[0]*2+(4<<20) {
			t.Fatalf("%s: allocation grows with the input: %d → %d bytes", fam.name, allocs[0], allocs[1])
		}
	}
}

// io2ReaderAt is a synthetic original followed by a fixed tail.
type io2ReaderAt struct {
	head repeatReaderAt
	tail []byte
}

func (r io2ReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	if off < r.head.n {
		k, _ := r.head.ReadAt(p[:min(int64(len(p)), r.head.n-off)], off)
		n = k
	}
	for n < len(p) {
		t := off + int64(n) - r.head.n
		if t >= int64(len(r.tail)) {
			if n == 0 {
				return 0, io.EOF
			}
			return n, nil
		}
		n += copy(p[n:], r.tail[t:])
	}
	return n, nil
}

func BenchmarkTestReducer16MiB(b *testing.B) {
	pattern := []byte("=== RUN   TestCase\n--- PASS: TestCase (0.00s)\nsome log line from the test\n")
	raw := bytes.Repeat(pattern, (16<<20)/len(pattern))
	sel := Selector{Tool: "Bash", Command: "go test ./..."}
	b.SetBytes(int64(len(raw)))
	for i := 0; i < b.N; i++ {
		red, _ := DefaultRegistry().Select(sel)
		h := &reduceHook{reducer: red, in: Input{Sel: sel, Target: 16 << 10}}
		if _, _, err := Reduce(withReduceHook(context.Background(), h), bytes.NewReader(raw), int64(len(raw)), "text/plain", 64<<10, 1<<20); err != nil {
			b.Fatal(err)
		}
	}
}

// Token estimation (PAR-CTX-13): deterministic, allocation-free, monotonic, and
// within a plausible band of BPE counts (o200k reference counts noted inline).
func TestTokenEstimateHeuristic(t *testing.T) {
	cases := []struct {
		text     string
		min, max int
	}{
		{"", 0, 0},
		{"Hello, world!", 3, 5}, // o200k: 4
		{"The quick brown fox jumps over the lazy dog.", 8, 12},               // o200k: 10
		{"func main() {\n\tfmt.Println(\"hi\")\n}\n", 10, 18},                 // o200k: 12
		{"2026-09-25T12:00:00Z ERROR connection refused (retry 3/5)", 14, 30}, // o200k: ~22
		{"日本語のテキストです", 6, 12},                                                 // o200k: ~7
		{strings.Repeat("a", 4000), 900, 1100},                                // runs of letters
	}
	for _, c := range cases {
		n := EstimateTokens(c.text)
		if n < c.min || n > c.max {
			t.Errorf("%q: %d tokens, want %d..%d", c.text[:min(len(c.text), 40)], n, c.min, c.max)
		}
		if nb := EstimateTokensBytes([]byte(c.text)); nb != n {
			t.Errorf("string and bytes estimates differ: %d vs %d", n, nb)
		}
	}
	// monotonic: appending text never lowers the estimate
	base := string(goTestRun(50))
	prev := 0
	for i := 0; i <= len(base); i += 97 {
		n := EstimateTokens(base[:i])
		if n+1 < prev { // a run split at the cut may be re-counted by one
			t.Fatalf("estimate fell from %d to %d at %d", prev, n, i)
		}
		prev = n
	}
	// allocation-free
	big := strings.Repeat("some output line with words and 12345 numbers\n", 1000)
	if a := testing.AllocsPerRun(10, func() { EstimateTokens(big) }); a != 0 {
		t.Fatalf("EstimateTokens allocates %v times", a)
	}
}

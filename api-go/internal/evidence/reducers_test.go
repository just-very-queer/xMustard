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
	"regexp"
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
		{gitLogP(600), Selector{Tool: "Bash", Command: "git log -p"}},
		{longPathDiff(), Selector{Tool: "Bash", Command: "git diff"}},
		{longPathGrep(), Selector{Tool: "Bash", Command: "rg x"}},
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

// long paths make closing summaries large: they must still fit the target
func longPathDiff() []byte {
	var b bytes.Buffer
	for f := 0; f < 300; f++ {
		p := fmt.Sprintf("%s/file_%03d.go", strings.Repeat("deeply/nested/directory", 8), f)
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,2 +1,%d @@\n ctx\n", p, p, p, p, 2+f)
		for i := 0; i < f; i++ {
			fmt.Fprintf(&b, "+added line %d\n", i)
		}
	}
	return b.Bytes()
}

func longPathGrep() []byte {
	var b bytes.Buffer
	for f := 0; f < 400; f++ {
		for i := 0; i < 1+f%7; i++ {
			fmt.Fprintf(&b, "%s/f%03d.go:%d:match here\n", strings.Repeat("very/long/path/segment", 10), f, i+1)
		}
	}
	return b.Bytes()
}

// --- review fixes (round 1) ---

// observeRaw captures a raw body with the production projection limits.
func observeRaw(t *testing.T, s *Store, raw []byte, meta CaptureMeta, sel Selector) *ObservationResult {
	t.Helper()
	res, err := s.Observe(context.Background(), nil, ObservationInput{WorkspaceID: "ws", Format: FormatRaw,
		Body: bytes.NewReader(raw), Meta: meta, Sel: sel})
	if err != nil {
		t.Fatalf("observe %s: %v", meta.Tool, err)
	}
	return res
}

func productionStore(t *testing.T) *Store {
	t.Helper()
	s, _ := testStore(t, func(l *Limits) { *l = DefaultLimits() })
	return s
}

// Another server's tool is never reduced as native grep/read/list output just
// because its name ends in search, read or list: JSON output goes to the structured
// family (valid JSON, status kept, bounded); text output keeps the name's family.
func TestNamespacedToolJSONUsesStructuredFamily(t *testing.T) {
	s := productionStore(t)
	var jobs []map[string]any
	for i := 0; i < 3000; i++ {
		jobs = append(jobs, map[string]any{"id": i, "name": fmt.Sprintf("job-%04d", i), "log": "src/app.go:12: something"})
	}
	pretty, _ := json.MarshalIndent(map[string]any{"jobs": jobs, "status": "failed", "ok": false}, "", "  ")
	var results []map[string]any
	for i := 0; i < 500; i++ {
		results = append(results, map[string]any{"path": fmt.Sprintf("src/f%03d.go", i), "line": i, "text": "needle := 1"})
	}
	single, _ := json.Marshal(map[string]any{"status": "partial", "results": results, "total": 500})
	target := PolicyFor("claude").Target
	for _, c := range []struct {
		tool string
		raw  []byte
		keep []string
	}{
		{"mcp__ci__list", pretty, []string{`"failed"`, `"ok"`}},
		{"mcp__github__search", single, []string{`"partial"`}},
		{"mcp__brave__search", single, []string{`"partial"`}},
		{"mcp__xmustard__search", single, []string{`"partial"`}},
		{"mcp__fs__read_file", pretty, []string{`"failed"`}},
		{"github.search", single, []string{`"partial"`}},
		{"MCP:search", single, []string{`"partial"`}},
	} {
		res := observeRaw(t, s, c.raw, CaptureMeta{Client: "claude", Tool: c.tool, ContentType: "application/json"}, Selector{})
		if res.Family != FamilyStructured || !strings.HasPrefix(res.Reducer, "xm-structured/1") || !json.Valid([]byte(res.Projection)) {
			t.Errorf("%s: family %s reducer %s valid=%v", c.tool, res.Family, res.Reducer, json.Valid([]byte(res.Projection)))
			continue
		}
		mustContain(t, res.Projection, c.keep...)
		if len(res.Projection) > target+1024 {
			t.Errorf("%s: projection %d bytes for a %d target", c.tool, len(res.Projection), target)
		}
	}
	// no declared content type: the output itself decides
	if res := observeRaw(t, s, pretty, CaptureMeta{Client: "claude", Tool: "mcp__ci__list"}, Selector{}); res.Family != FamilyStructured ||
		!strings.Contains(res.Projection, `"failed"`) {
		t.Fatalf("undeclared JSON: %s", res.Family)
	}
	// text output of a namespaced tool keeps the family its name suggests
	res := observeRaw(t, s, globOutput(900), CaptureMeta{Client: "claude", Tool: "mcp__fs__find"}, Selector{})
	if res.Family != FamilyGlob || res.Reducer != "xm-glob/1" {
		t.Fatalf("text output of mcp__fs__find: %s %s", res.Family, res.Reducer)
	}
	// native tools keep their family whatever the output looks like
	for _, tool := range []string{"Read", "read", "list", "search"} {
		if NamespacedTool(tool) {
			t.Fatalf("%s is a native tool name", tool)
		}
	}
	for _, tool := range []string{"mcp__a__b", "srv.search", "srv/read", "MCP:x", "mcp_x"} {
		if !NamespacedTool(tool) {
			t.Fatalf("%s is namespaced", tool)
		}
	}
	res = observeRaw(t, s, pretty, CaptureMeta{Client: "claude", Tool: "Read"}, Selector{Path: "jobs.json"})
	if res.Family != FamilyRead {
		t.Fatalf("a native Read of a JSON file is a read: %s", res.Family)
	}
}

// Every projection records the rule set that produced its text: when a family
// reducer delegates, the record and the header name the delegate, and the family
// record keeps the selected reducer.
func TestRecordedReducerMatchesProjectionHeader(t *testing.T) {
	var stat bytes.Buffer
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&stat, " src/file_%04d.go | %d ++--\n", i, i%9+1)
	}
	var plain, none bytes.Buffer
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&plain, "plain text result line %d\n", i)
		none.WriteString("--\n")
	}
	none.WriteString("Found 0 matches\n")
	var cc bytes.Buffer
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&cc, "In file included from x.h; note: step %d\n", i)
	}
	cc.WriteString("fatal error: missing.h not found\n")
	for _, c := range []struct {
		raw      []byte
		sel      Selector
		used     string
		selected string
		header   string
	}{
		{stat.Bytes(), Selector{Tool: "Bash", Command: "git diff --stat HEAD~50"}, "xm-git/1", "xm-diff/1", "[xmustard git] xm-git/1 "},
		{none.Bytes(), Selector{Tool: "Bash", Command: "rg needle"}, "xm-shell/1", "xm-grep/1", "[xmustard shell] xm-shell/1 "},
		{plain.Bytes(), Selector{Tool: "mcp__srv__tool"}, "xm-shell/1", "xm-structured/1", "[xmustard shell] xm-shell/1 "},
		{cc.Bytes(), Selector{Tool: "Bash", Command: "npm run lint"}, "xm-build/1", "xm-lint/1", "[xmustard lint] xm-build/1 "},
		{diffOutput(40, 4), Selector{Tool: "Bash", Command: "git diff"}, "xm-diff/1", "", "[xmustard diff] xm-diff/1 "},
	} {
		p, rec := reduceWith(t, c.raw, c.sel, 8<<10)
		if rec.Reducer != c.used || rec.Family == nil || rec.Family.Reducer != c.used || rec.Family.Selected != c.selected {
			t.Errorf("%s: record %s, family record %+v", c.sel.Command+c.sel.Tool, rec.Reducer, rec.Family)
		}
		if !strings.HasPrefix(p.Text, c.header) {
			t.Errorf("%s: header %.80q, want prefix %q", c.sel.Command+c.sel.Tool, p.Text, c.header)
		}
	}
}

// rg and grep -r write "path:text" (no line number) when not on a tty: those lines
// are matches grouped by file, and one enormous matched line stays within the target.
func TestGrepUnnumberedOutputStaysWithinTarget(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("src/app.go:\tfoo := needle()\n")
	b.WriteString("dist/app.min.js:" + strings.Repeat("var a=needle;", 8000) + "\n")
	b.WriteString("src/app.go:\treturn needle\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "./pkg/mod%02d/file_%03d.go:  x := needle(%d)\n", i%7, i%40, i)
	}
	raw := b.Bytes()
	target := 16 << 10
	p, rec := reduceWith(t, raw, Selector{Client: "claude", Tool: "Bash", Command: "rg needle"}, target)
	gp := p.Structured.(*GroupedProjection)
	// 280 distinct (module, file) pairs, src/app.go and dist/app.min.js
	if rec.Reducer != "xm-grep/1" || gp.Matches != 403 || gp.Files != 282 {
		t.Fatalf("unnumbered grep: %s matches %d files %d", rec.Reducer, gp.Matches, gp.Files)
	}
	if len(p.Text) > target {
		t.Fatalf("projection %d bytes for a %d target", len(p.Text), target)
	}
	mustContain(t, p.Text, "src/app.go (2)\n  foo := needle()\n", "dist/app.min.js (1)", "…[+")
	// the same through a Claude Bash capture: a reduced replacement, not a size error
	s := productionStore(t)
	body := `{"tool_name":"Bash","tool_input":{"command":"rg foo"},"tool_response":{"stdout":` + jsonString(string(raw)) +
		`,"stderr":"","interrupted":false,"isImage":false}}`
	res := observe(t, s, FormatClaude, body, CaptureMeta{Client: "claude"})
	if res.Family != FamilyGrep || res.Shape.Mode != ShapeReplace || len(res.Projection) > PolicyFor("claude").Target {
		t.Fatalf("claude rg: %s %s %d bytes (%s)", res.Family, res.Shape.Mode, len(res.Projection), res.Shape.Reason)
	}
	// grep -h style lines starting with '[' are output, not notices, and are capped
	var ini bytes.Buffer
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&ini, "[section_%05d]\n", i)
	}
	p, _ = reduceWith(t, ini.Bytes(), Selector{Tool: "Bash", Command: "grep -rh '^\\[' ."}, 4<<10)
	if len(p.Text) > 4<<10 {
		t.Fatalf("bracketed grep lines: projection %d bytes", len(p.Text))
	}
}

// Directory names in brackets (Next.js [slug] routes) are entries: capped like any
// other, within the target, with memory independent of the listing's size. Only a
// tool's notice ("[500 entries limit reached...]") is kept as a summary.
func TestListBracketedNamesAreEntries(t *testing.T) {
	gen := func(n int) []byte {
		var b bytes.Buffer
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "[slug%06d]\n", i)
		}
		b.WriteString("\n[500 entries limit reached. Use limit=1000 for more]\n")
		return b.Bytes()
	}
	target := 16 << 10
	var allocs []uint64
	for _, n := range []int{3000, 150000} {
		raw := gen(n)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		p, rec := reduceWith(t, raw, Selector{Tool: "Bash", Command: "ls app"}, target)
		runtime.ReadMemStats(&after)
		allocs = append(allocs, after.TotalAlloc-before.TotalAlloc)
		lp := p.Structured.(*ListProjection)
		if rec.Reducer != "xm-list/1" || lp.Total != n || lp.Shown != maxListEntries || len(p.Text) > target {
			t.Fatalf("%d bracketed entries: %s total %d shown %d, %d bytes", n, rec.Reducer, lp.Total, lp.Shown, len(p.Text))
		}
		mustContain(t, p.Text, "[slug000000]", "[500 entries limit reached. Use limit=1000 for more]")
	}
	t.Logf("allocated %d KiB for 3,000 entries, %d KiB for 150,000", allocs[0]>>10, allocs[1]>>10)
	if !raceEnabled && allocs[1] > allocs[0]+(2<<20) {
		t.Fatalf("list allocation grows with the input: %d → %d bytes", allocs[0], allocs[1])
	}
}

// The closing note of a list/glob projection is carried once, however many
// sections the original has, so a rebuilt payload is no larger than the projection.
func TestListNoteIsCarriedOnce(t *testing.T) {
	s := productionStore(t)
	var blocks []string
	for i := 0; i < 50; i++ {
		blocks = append(blocks, `{"type":"text","text":`+jsonString(string(globOutput(120)))+`}`)
	}
	body := `{"tool_name":"mcp__fs__find","tool_input":{"pattern":"**/*.ts"},"tool_response":[` + strings.Join(blocks, ",") + `]}`
	res := observe(t, s, FormatClaude, body, CaptureMeta{Client: "claude"})
	if res.Family != FamilyGlob || res.Shape.Mode != ShapeReplace {
		t.Fatalf("50 glob blocks: %s %s (%s)", res.Family, res.Shape.Mode, res.Shape.Reason)
	}
	if n := strings.Count(string(res.Shape.Payload), "more entries not shown"); n != 1 {
		t.Fatalf("closing note carried %d times", n)
	}
	if res.Shape.Chars > PolicyFor("claude").MaxChars || len(res.Shape.Payload) > len(res.Projection)+len(res.Footer)+8<<10 {
		t.Fatalf("payload %d bytes (%d chars) for a %d-byte projection", len(res.Shape.Payload), res.Shape.Chars, len(res.Projection))
	}
}

// A deleted line that reads "--- ..." (an SQL, Lua or Haskell comment) is a
// deletion inside a counted hunk, not a file header.
func TestDiffCountsHunkLinesThatLookLikeHeaders(t *testing.T) {
	var b bytes.Buffer
	for f := 0; f < 30; f++ {
		fmt.Fprintf(&b, "diff --git a/db/m%02d.sql b/db/m%02d.sql\nindex 1111111..2222222 100644\n--- a/db/m%02d.sql\n+++ b/db/m%02d.sql\n", f, f, f, f)
		b.WriteString("@@ -1,21 +1,20 @@\n--- old header comment\n")
		for i := 0; i < 20; i++ {
			fmt.Fprintf(&b, "-SELECT %d;\n+SELECT %d + 1;\n", i, i)
		}
	}
	b.WriteString("diff --git a/x.lua b/x.lua\n--- a/x.lua\n+++ b/x.lua\n@@ -1 +1,2 @@\n-x = 1\n+++ counter\n+x = 2\n\\ No newline at end of file\n")
	p, rec := reduceWith(t, b.Bytes(), Selector{Tool: "Bash", Command: "git diff"}, 16<<10)
	dp := p.Structured.(*DiffProjection)
	if rec.Reducer != "xm-diff/1" || dp.Files != 31 || dp.Additions != 602 || dp.Deletions != 631 || dp.Hunks != 31 {
		t.Fatalf("diff counts: %s %+v", rec.Reducer, dp)
	}
	mustContain(t, p.Text, "files=31 +602 -631 hunks=31", "=== db/m00.sql (+20 -21, 1 hunks)", "--- old header comment")
}

// The lint parsers match the patterns they replace, on every line shape.
func TestLintParsersMatchTheirPatterns(tt *testing.T) {
	diagRe := regexp.MustCompile(`^\s*(\S[^:()]*?)(?::(\d+)(?::(\d+))?|\((\d+),(\d+)\)):?\s*(?:-\s*)?(.*)$`)
	stylishRe := regexp.MustCompile(`^\s+(\d+):(\d+)\s+(error|warning|warn|info)\s+(.*)$`)
	lines := []string{
		"src/a.py:12:5: error: bad", "src/a.py:12: warning - x", "a.ts(3,4): error TS2322: y", "  lib/x.go:7:  - msg",
		"C:\\x\\y.cs(1,2): warning CS1: z", "::12: x", ":12: x", "x:", "x:12", "x:12:", "x:12:3", "x(1,2)", "x(1,)", "x(1,2", "x)1",
		"path with space.go:3: m", "\tsrc/b.rs:9:1:msg", "at foo (a.js:1:2)", "", "   ", "noColon", "a:b:c", "a:1:b:2: c", "a(1,2):x",
		"x:1:2:3", "x:-1", "x:1 -", "x:1\t-\tmsg", "\f\rp:1: q", "é/ü.go:4:2: ü", "x:12:5:", "x (1,2): y",
		"  12:5  error  Unexpected any", "  3:1  warning  foo  rule", "  3:1  warn  x", "  3:1  warnings  x", "  3:1  info\tx",
		"3:1  error  x", "  3:1 error", "  3:1  error ", "  a:1  error  x", "  3:  error  x", "  3:1error  x",
	}
	for _, l := range lines {
		t := []byte(l)
		m := diagRe.FindSubmatchIndex(t)
		path, ln, rest, ok := parseDiag(t)
		if (m != nil) != ok {
			tt.Errorf("%q: regexp match %v, parser %v", l, m != nil, ok)
			continue
		}
		if ok {
			var want []byte
			switch {
			case m[4] >= 0 && m[6] >= 0:
				want = t[m[4]:m[7]]
			case m[4] >= 0:
				want = t[m[4]:m[5]]
			default:
				want = t[m[8]:m[11]]
			}
			if string(path) != string(t[m[2]:m[3]]) || string(ln) != string(want) || string(rest) != string(t[m[12]:m[13]]) {
				tt.Errorf("%q: parser (%q %q %q), regexp (%q %q %q)", l, path, ln, rest, t[m[2]:m[3]], want, t[m[12]:m[13]])
			}
		}
		sm := stylishRe.FindSubmatchIndex(t)
		sln, word, sok := parseStylish(t)
		if (sm != nil) != sok || (sok && (string(sln) != string(t[sm[2]:sm[5]]) || string(word) != string(t[sm[6]:sm[7]]))) {
			tt.Errorf("%q: stylish regexp %v, parser %v %q %q", l, sm != nil, sok, sln, word)
		}
	}
}

// scaled grows a fixture to about size bytes.
func scaled(gen func(int) []byte, size int) []byte {
	k := 64
	base := len(gen(k))
	return gen(max(k, k*size/max(base, 1)))
}

// Every family reducer is O(window) on its own output shape, not only on generic
// lines: reducing a ~4x larger original allocates about the same (tracked-file
// tables, rings and line buffers are bounded; lint parses without per-line
// allocation).
func TestFamilyReducersUseBoundedMemoryOnRealShapes(t *testing.T) {
	if raceEnabled && testing.Short() {
		t.Skip("allocation bound is not meaningful under -race")
	}
	jsonDoc := func(n int) []byte {
		var b bytes.Buffer
		b.WriteString(`{"status":"failed","items":[`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":%d,"name":"item-%06d","state":"ok","detail":"nothing to see in item %d"}`, i, i, i)
		}
		b.WriteString(`],"total":1}`)
		return b.Bytes()
	}
	for _, fam := range []struct {
		name   string
		gen    func(int) []byte
		sel    Selector
		prefix string
	}{
		{"diff", func(n int) []byte { return diffOutput(n, 6) }, Selector{Tool: "Bash", Command: "git diff"}, "[xmustard diff] xm-diff/1"},
		{"git log -p", gitLogP, Selector{Tool: "Bash", Command: "git log -p"}, "[xmustard diff] xm-diff/1"},
		{"grep", func(n int) []byte { return grepOutput(n, 4) }, Selector{Tool: "Bash", Command: "rg -n needle"}, "[xmustard grep] xm-grep/1"},
		{"lint", func(n int) []byte { return lintOutput(n, 30) }, Selector{Tool: "Bash", Command: "ruff check ."}, "[xmustard lint] xm-lint/1"},
		{"read", sourceFile, Selector{Tool: "Read", Path: "big.go"}, "[xmustard read] xm-read/1"},
		{"list", lsOutput, Selector{Tool: "Bash", Command: "ls -la"}, "[xmustard list] xm-list/1"},
		{"glob", globOutput, Selector{Tool: "Glob"}, "[xmustard glob] xm-glob/1"},
		{"test (unittest)", func(n int) []byte { return unittestRun(n, 40) }, Selector{Tool: "Bash", Command: "pytest"}, "[xmustard test] xm-test/1"},
		{"test (cargo)", cargoRun, Selector{Tool: "Bash", Command: "cargo test"}, "[xmustard test] xm-test/1"},
		{"test (pytest -v)", func(n int) []byte {
			var b bytes.Buffer
			for i := 0; i < n; i++ {
				fmt.Fprintf(&b, "tests/test_mod.py::test_case_%06d PASSED                  [ 50%%]\n", i)
			}
			b.WriteString("tests/test_mod.py::test_parse FAILED                  [100%]\n")
			fmt.Fprintf(&b, "==== 1 failed, %d passed in 9.87s ====\n", n)
			return b.Bytes()
		}, Selector{Tool: "Bash", Command: "pytest -v"}, "[xmustard test] xm-test/1"},
		{"structured", jsonDoc, Selector{Tool: "mcp__ci__items"}, `{"status":"failed"`},
	} {
		var allocs [2]uint64
		for i, size := range []int{4 << 20, 16 << 20} {
			raw := scaled(fam.gen, size)
			red, argv0 := DefaultRegistry().Select(fam.sel)
			h := &reduceHook{reducer: red, argv0: argv0, in: Input{Sel: fam.sel, Target: 16 << 10}}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			if _, _, err := Reduce(withReduceHook(context.Background(), h), bytes.NewReader(raw), int64(len(raw)), "text/plain", 64<<10, 1<<20); err != nil {
				t.Fatalf("%s: %v", fam.name, err)
			}
			runtime.ReadMemStats(&after)
			allocs[i] = after.TotalAlloc - before.TotalAlloc
			if !strings.HasPrefix(h.out.Text, fam.prefix) {
				t.Fatalf("%s: the fixture no longer exercises the family engine: %.80q", fam.name, h.out.Text)
			}
		}
		t.Logf("%s: ~4 MiB → %d KiB allocated, ~16 MiB → %d KiB", fam.name, allocs[0]>>10, allocs[1]>>10)
		if allocs[1] > allocs[0]*2+(4<<20) {
			t.Errorf("%s: allocation grows with the input: %d → %d bytes", fam.name, allocs[0], allocs[1])
		}
	}
}

// xmReduceFixtures are the originals the xm-reduce/1 goldens pin (generated on the
// baseline b4f7b49, before tool-family reducers existed).
func xmReduceFixtures() map[string][]byte {
	out := map[string][]byte{}
	out["json_results"] = manyResults(300, 137)
	nested := map[string]any{"status": "degraded", "exit_code": 3}
	var items []map[string]any
	for i := 0; i < 400; i++ {
		items = append(items, map[string]any{"id": i, "name": fmt.Sprintf("n-%04d", i), "ok": i != 211,
			"log": strings.Repeat(fmt.Sprintf("entry %d fine; ", i), 6)})
	}
	nested["items"] = items
	nested["summary"] = map[string]any{"passed": 399, "failed": 1, "note": strings.Repeat("summary text ", 200)}
	out["json_nested"], _ = json.Marshal(nested)
	var log bytes.Buffer
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&log, "2026-09-25T12:%02d:%02d info worker %d processed batch %d\n", i/60%60, i%60, i%8, i)
		if i == 2500 {
			log.WriteString("panic: runtime error: invalid memory address or nil pointer dereference\n\tgoroutine 7 [running]:\n")
		}
	}
	out["text_log"] = log.Bytes()
	out["small"] = []byte(`{"ok":true,"n":1}`)
	return out
}

// xmReduceGolden renders one fixture through Reduce without a registry hook.
func xmReduceGolden(raw []byte, contentType string) string {
	proj, rec, err := Reduce(context.Background(), bytes.NewReader(raw), int64(len(raw)), contentType, 8<<10, 1<<20)
	if err != nil {
		return "error: " + err.Error()
	}
	meta, _ := json.Marshal(rec)
	return string(meta) + "\n" + proj
}

// xm-reduce/1 is pinned byte for byte to projections generated on the baseline
// (b4f7b49, before the registry existed): the nine tools' projections, and every
// capture that selects no family reducer, are unchanged by this workstream.
func TestXmReduceProjectionsMatchBaseline(t *testing.T) {
	for name, raw := range xmReduceFixtures() {
		ct := "text/plain"
		if !strings.HasPrefix(name, "text") {
			ct = "application/json"
		}
		want, err := os.ReadFile(filepath.Join("testdata", "xm_reduce_1_"+name+".golden"))
		if err != nil {
			t.Fatal(err)
		}
		if got := xmReduceGolden(raw, ct); got != string(want) {
			t.Errorf("%s: xm-reduce/1 output differs from the baseline golden:\n--- got ---\n%.2000s", name, got)
		}
	}
}

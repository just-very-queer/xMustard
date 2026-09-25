package evidence

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Test-runner family (xm-test/1). Built on the line engine: every failing test's
// header, assertion lines and the first and last frames of its stack trace are kept
// failure-first; passing tests, RUN markers and progress dots are counted and
// collapsed; summary lines are always kept and their counts, when a runner prints
// them, are authoritative over the per-line tallies. Recognized runners: go test,
// pytest, unittest, jest, vitest, mocha, node --test and TAP, cargo test, Maven,
// Gradle, dotnet test and RSpec.

// testSummary holds counts a runner printed itself.
type testSummary struct {
	passed, failed, skipped int
	seen                    bool
	// cargo and dotnet print one summary per test binary/project: those add up
	additive bool
}

var (
	goRun       = regexp.MustCompile(`^\s*=== (RUN|PAUSE|CONT|NAME)\s`)
	goPass      = regexp.MustCompile(`^\s*--- PASS: `)
	goSkip      = regexp.MustCompile(`^\s*--- SKIP: `)
	goFail      = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	goPkg       = regexp.MustCompile(`^(ok|FAIL|\?)\s+\S+(\s+[\d.]+s|\s+\[no test files\]|\s+\(cached\))?`)
	pyItem      = regexp.MustCompile(`^(\S+::\S+)\s+(PASSED|FAILED|SKIPPED|XFAIL|XPASS|ERROR)\b`)
	pyProgress  = regexp.MustCompile(`^\S+\.py [.FEsxX]+\s*(\[\s*\d+%\])?$`)
	pyBlock     = regexp.MustCompile(`^_{3,} (.+?) _{3,}$`)
	pySummary   = regexp.MustCompile(`^={2,} (.*\b(passed|failed|error|errors|skipped|no tests ran)\b.*) ={2,}$`)
	pyShort     = regexp.MustCompile(`^(FAILED|ERROR) (\S+::\S+|\S+\.py\b)`)
	pyE         = regexp.MustCompile(`^E\s{2,}\S`)
	utItem      = regexp.MustCompile(`^(\S+) \(([\w.]+)\)(?: \S.*)? \.\.\. (ok|FAIL|ERROR|skipped.*|expected failure|unexpected success)$`)
	utBlock     = regexp.MustCompile(`^(FAIL|ERROR): (\S+)`)
	utRan       = regexp.MustCompile(`^Ran (\d+) tests? in `)
	utResult    = regexp.MustCompile(`^(OK|FAILED)(?: \((.*)\))?$`)
	jsPass      = regexp.MustCompile(`^\s*[✓✔√]\s`)
	jsFail      = regexp.MustCompile(`^\s*[✕✗×✘]\s+(.+)`)
	jsSkip      = regexp.MustCompile(`^\s*[○↓-]\s+(skipped|todo|\S)`)
	jsBlock     = regexp.MustCompile(`^\s*● (.+)`)
	jsFile      = regexp.MustCompile(`^\s*(PASS|FAIL)\s+\S`)
	jsTests     = regexp.MustCompile(`^\s*Tests:?\s+(.*)$`)
	jsSuites    = regexp.MustCompile(`^\s*(Test Suites|Test Files|Snapshots|Time|Duration):?\s`)
	cargoItem   = regexp.MustCompile(`^test (\S+) \.\.\. (ok|FAILED|ignored)`)
	cargoBlock  = regexp.MustCompile(`^---- (\S+) stdout ----$`)
	cargoResult = regexp.MustCompile(`^test result: \w+\. (\d+) passed; (\d+) failed; (\d+) ignored`)
	tapOK       = regexp.MustCompile(`^\s*ok \d+ `)
	tapNotOK    = regexp.MustCompile(`^\s*not ok \d+ - (.+)`)
	tapCount    = regexp.MustCompile(`^# (pass|fail|skipped|todo|tests|suites|cancelled) (\d+)$`)
	tapSubtest  = regexp.MustCompile(`^\s*# Subtest: `)
	mochaPass   = regexp.MustCompile(`^\s+✔ `)
	mochaFail   = regexp.MustCompile(`^\s+\d+\) (.+)`)
	mochaCount  = regexp.MustCompile(`^\s*(\d+) (passing|failing|pending)\b`)
	mvnCount    = regexp.MustCompile(`Tests run: (\d+), Failures: (\d+), Errors: (\d+), Skipped: (\d+)`)
	mvnFail     = regexp.MustCompile(`<<< (FAILURE|ERROR)!`)
	gradleFail  = regexp.MustCompile(`^(\S+) > (.+) FAILED$`)
	gradleCount = regexp.MustCompile(`(\d+) tests? completed, (\d+) failed(?:, (\d+) skipped)?`)
	dotnetCount = regexp.MustCompile(`(?:Passed|Failed)!\s+-\s+Failed:\s+(\d+),\s+Passed:\s+(\d+),\s+Skipped:\s+(\d+)`)
	dotnetFail  = regexp.MustCompile(`^\s+Failed (\S+) \[`)
	rspecCount  = regexp.MustCompile(`^(\d+) examples?, (\d+) failures?(?:, (\d+) pending)?`)
	rspecDots   = regexp.MustCompile(`^[.*F]{8,}$`)
	countWord   = regexp.MustCompile(`(\d+) (passed|failed|skipped|errors?|xfailed|xpassed|todo|pending)\b`)
	failHint    = regexp.MustCompile(`(?i)^\s*(expected|received|actual|got|want|wanted|left|right|diff)\s*[:=]`)
)

var testRules = &lineRules{
	family: FamilyTest, id: "xm-test", version: 1, lookback: true,
	classify: classifyTestLine,
	header: func(f *Facts) string {
		s := fmt.Sprintf(" passed=%d failed=%d skipped=%d", f.Passed, f.Failed, f.Skipped)
		if f.CountsFrom != "" {
			s += " counts_from=" + f.CountsFrom
		}
		if len(f.FailingTests) > 0 {
			// at most ~400 bytes of names: the header must not eat a small budget
			names, shown := "", 0
			for _, n := range f.FailingTests {
				if len(names)+len(n) > 400 && shown > 0 {
					break
				}
				if shown > 0 {
					names += ","
				}
				names, shown = names+n, shown+1
			}
			s += " failing=" + names
			if rest := max(f.Failed, len(f.FailingTests)) - shown; rest > 0 {
				s += fmt.Sprintf(",+%d", rest)
			}
		}
		return s
	},
}

func (f *Facts) summary() *testSummary {
	if f.sum == nil {
		f.sum = &testSummary{}
	}
	return f.sum
}

func (f *Facts) failing(name string) {
	name = strings.TrimSpace(name)
	if name == "" || len(f.FailingTests) >= maxFailingNames {
		return
	}
	for _, n := range f.FailingTests {
		if n == name {
			return
		}
	}
	if len(name) > 160 {
		name = string(trimRune([]byte(name[:160])))
	}
	f.FailingTests = append(f.FailingTests, name)
}

func atoi(b []byte) int {
	v, _ := strconv.Atoi(string(b))
	return v
}

// classifyTestLine recognizes the runners' per-test, block and summary lines. It
// returns lcPlain for anything else; the engine then applies generic failure
// keywords, stack-frame rules and repeat collapsing.
func classifyTestLine(st *lineState, line []byte) lineClass {
	f := st.facts
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return lcPlain
	}
	// cheap guards keep the runner patterns off lines that cannot match them
	c0 := trimmed[0]
	has := func(sub string) bool { return bytes.Contains(line, []byte(sub)) }
	pre := func(p string) bool { return bytes.HasPrefix(trimmed, []byte(p)) }
	nonASCII := c0 >= 0x80
	switch {
	// go test
	case c0 == '=' && goRun.Match(line):
		return lcBoundary
	case c0 == '-' && goPass.Match(line):
		f.Passed++
		return lcBoundary
	case c0 == '-' && goSkip.Match(line):
		f.Skipped++
		return lcBoundary
	case c0 == '-' && goFail.Match(line):
		f.Failed++
		f.failing(string(goFail.FindSubmatch(line)[1]))
		return lcSalient
	case bytes.Equal(trimmed, []byte("PASS")) || bytes.Equal(trimmed, []byte("FAIL")):
		return lcSummary
	case (bytes.HasPrefix(line, []byte("ok ")) || bytes.HasPrefix(line, []byte("?"))) && goPkg.Match(line):
		return lcCollapse
	case bytes.HasPrefix(line, []byte("FAIL")) && goPkg.Match(line):
		return lcSummary
	case (c0 == 'O' || c0 == 'F') && f.sum != nil && f.sum.seen && utResult.Match(line):
		m := utResult.FindSubmatch(line)
		s := f.summary()
		total := s.passed + s.failed + s.skipped
		s.failed, s.skipped = 0, 0
		for _, kv := range strings.Split(string(m[2]), ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
			n, _ := strconv.Atoi(v)
			switch k {
			case "failures", "errors", "unexpected successes":
				s.failed += n
			case "skipped", "expected failures":
				s.skipped += n
			}
		}
		s.passed = max(0, total-s.failed-s.skipped)
		return lcSummary
	// pytest
	case has("::") && pyItem.Match(line):
		m := pyItem.FindSubmatch(line)
		switch string(m[2]) {
		case "PASSED", "XPASS":
			f.Passed++
			return lcCollapse
		case "SKIPPED", "XFAIL":
			f.Skipped++
			return lcCollapse
		}
		f.Failed++
		f.failing(string(m[1]))
		return lcSalient
	case has(".py ") && pyProgress.Match(line):
		return lcCollapse
	case c0 == '_' && pyBlock.Match(line):
		st.block = maxBlockLines
		return lcSalient
	case c0 == '=' && pySummary.Match(line):
		s := f.summary()
		s.seen, s.passed, s.failed, s.skipped = true, 0, 0, 0
		for _, m := range countWord.FindAllSubmatch(line, -1) {
			n := atoi(m[1])
			switch string(m[2]) {
			case "passed", "xpassed":
				s.passed += n
			case "failed", "error", "errors":
				s.failed += n
			case "skipped", "xfailed":
				s.skipped += n
			}
		}
		return lcSummary
	case (c0 == 'F' || c0 == 'E') && pyShort.Match(line):
		f.failing(string(pyShort.FindSubmatch(line)[2]))
		return lcSalient
	case c0 == 'E' && pyE.Match(line):
		return lcSalient
	// unittest
	case has(" ... ") && utItem.Match(line):
		m := utItem.FindSubmatch(line)
		switch r := string(m[3]); {
		case r == "ok" || r == "unexpected success":
			f.Passed++
			return lcCollapse
		case strings.HasPrefix(r, "skipped") || r == "expected failure":
			f.Skipped++
			return lcCollapse
		}
		f.Failed++
		f.failing(string(m[1]))
		return lcSalient
	case (c0 == 'F' || c0 == 'E') && utBlock.Match(line):
		st.block = maxBlockLines
		f.failing(string(utBlock.FindSubmatch(line)[2]))
		return lcSalient
	case c0 == 'R' && utRan.Match(line):
		s := f.summary()
		s.seen = true
		s.passed = atoi(utRan.FindSubmatch(line)[1]) // total until OK/FAILED refines it
		return lcSummary
	// jest / vitest
	case nonASCII && (jsPass.Match(line) || mochaPass.Match(line)):
		f.Passed++
		return lcCollapse
	case nonASCII && jsFail.Match(line):
		f.Failed++
		f.failing(string(jsFail.FindSubmatch(line)[1]))
		return lcSalient
	case nonASCII && jsBlock.Match(line):
		st.block = maxBlockLines
		f.failing(string(jsBlock.FindSubmatch(line)[1]))
		return lcSalient
	case c0 == 'T' && jsTests.Match(line) && countWord.Match(line):
		s := f.summary()
		s.seen, s.passed, s.failed, s.skipped = true, 0, 0, 0
		for _, m := range countWord.FindAllSubmatch(line, -1) {
			n := atoi(m[1])
			switch string(m[2]) {
			case "passed":
				s.passed += n
			case "failed":
				s.failed += n
			case "skipped", "todo", "pending":
				s.skipped += n
			}
		}
		return lcSummary
	case (c0 == 'T' || c0 == 'S' || c0 == 'D') && jsSuites.Match(line):
		return lcSummary
	case (c0 == 'P' || c0 == 'F') && jsFile.Match(line):
		if bytes.Contains(line, []byte("FAIL")) {
			return lcSalient
		}
		return lcCollapse
	case (nonASCII || c0 == '-') && (has("skipped") || pre("○")) && jsSkip.Match(line):
		f.Skipped++
		return lcCollapse
	// cargo test
	case c0 == 't' && cargoItem.Match(line):
		m := cargoItem.FindSubmatch(line)
		switch string(m[2]) {
		case "ok":
			f.Passed++
			return lcCollapse
		case "ignored":
			f.Skipped++
			return lcCollapse
		}
		f.Failed++
		f.failing(string(m[1]))
		return lcSalient
	case c0 == '-' && cargoBlock.Match(line):
		st.block = maxBlockLines
		return lcSalient
	case c0 == 't' && cargoResult.Match(line):
		m := cargoResult.FindSubmatch(line)
		s := f.summary()
		if !s.additive {
			s.passed, s.failed, s.skipped = 0, 0, 0
		}
		s.seen, s.additive = true, true
		s.passed += atoi(m[1])
		s.failed += atoi(m[2])
		s.skipped += atoi(m[3])
		return lcSummary
	// TAP / node --test
	case c0 == '#' && tapSubtest.Match(line):
		return lcBoundary
	case c0 == 'n' && tapNotOK.Match(line):
		if bytes.Contains(line, []byte("# SKIP")) || bytes.Contains(line, []byte("# TODO")) {
			f.Skipped++
			return lcCollapse
		}
		f.Failed++
		f.failing(string(tapNotOK.FindSubmatch(line)[1]))
		st.block = maxBlockLines
		return lcSalient
	case c0 == 'o' && tapOK.Match(line):
		if bytes.Contains(line, []byte("# SKIP")) {
			f.Skipped++
		} else {
			f.Passed++
		}
		return lcCollapse
	case c0 == '#' && tapCount.Match(line):
		m := tapCount.FindSubmatch(line)
		s := f.summary()
		s.seen = true
		switch string(m[1]) {
		case "pass":
			s.passed = atoi(m[2])
		case "fail":
			s.failed = atoi(m[2])
		case "skipped", "todo":
			s.skipped += atoi(m[2])
		}
		return lcSummary
	// mocha
	case c0 >= '0' && c0 <= '9' && mochaCount.Match(line):
		m := mochaCount.FindSubmatch(line)
		s := f.summary()
		s.seen = true
		switch string(m[2]) {
		case "passing":
			s.passed = atoi(m[1])
		case "failing":
			s.failed = atoi(m[1])
		case "pending":
			s.skipped = atoi(m[1])
		}
		return lcSummary
	case c0 >= '0' && c0 <= '9' && mochaFail.Match(line) && !has(" passing"):
		f.failing(string(mochaFail.FindSubmatch(line)[1]))
		return lcSalient
	// Maven / Gradle / dotnet / RSpec
	case has("Tests run:") && mvnCount.Match(line):
		m := mvnCount.FindSubmatch(line)
		s := f.summary()
		total := atoi(m[1])
		s.seen, s.failed, s.skipped = true, atoi(m[2])+atoi(m[3]), atoi(m[4])
		s.passed = max(0, total-s.failed-s.skipped)
		if s.failed > 0 {
			return lcSalient
		}
		return lcSummary
	case has("<<<") && mvnFail.Match(line):
		return lcSalient
	case has(" > ") && has("FAILED") && gradleFail.Match(line):
		m := gradleFail.FindSubmatch(line)
		f.Failed++
		f.failing(string(m[1]) + " > " + string(m[2]))
		return lcSalient
	case has("completed") && gradleCount.Match(line):
		m := gradleCount.FindSubmatch(line)
		s := f.summary()
		s.seen, s.failed, s.skipped = true, atoi(m[2]), atoi(m[3])
		s.passed = max(0, atoi(m[1])-s.failed-s.skipped)
		return lcSummary
	case has("!") && dotnetCount.Match(line):
		m := dotnetCount.FindSubmatch(line)
		s := f.summary()
		if !s.additive {
			s.passed, s.failed, s.skipped = 0, 0, 0
		}
		s.seen, s.additive = true, true
		s.failed += atoi(m[1])
		s.passed += atoi(m[2])
		s.skipped += atoi(m[3])
		return lcSummary
	case c0 == 'F' && dotnetFail.Match(line):
		f.Failed++
		f.failing(string(dotnetFail.FindSubmatch(line)[1]))
		return lcSalient
	case c0 >= '0' && c0 <= '9' && rspecCount.Match(line):
		m := rspecCount.FindSubmatch(line)
		s := f.summary()
		s.seen, s.failed, s.skipped = true, atoi(m[2]), atoi(m[3])
		s.passed = max(0, atoi(m[1])-s.failed-s.skipped)
		return lcSummary
	case (c0 == '.' || c0 == '*' || c0 == 'F') && rspecDots.Match(trimmed):
		return lcCollapse
	case bytes.ContainsAny(trimmed[:min(len(trimmed), 10)], ":=") && failHint.Match(line):
		return lcSalient
	}
	if bytes.Contains(line, []byte("exit status ")) {
		return lcSummary
	}
	return lcPlain
}

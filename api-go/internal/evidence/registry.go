package evidence

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Tool-family reduction (PAR-CTX-02). The nine xMustard tools keep the generic
// xm-reduce/1 projection. Output captured from any other tool (native shell, read,
// grep, list, MCP tools of other servers) is reduced by a versioned family reducer
// selected from a registry keyed by the client's tool name and, for shell tools, the
// command's argv0. Store.Capture reaches the selected reducer through the registry
// hook in Reduce (reduce.go): the capture pipeline puts a reduceHook in the context,
// so the store's retention logic is shared unchanged.

// Family names one tool family.
type Family string

const (
	FamilyShell      Family = "shell"
	FamilyTest       Family = "test"
	FamilyBuild      Family = "build"
	FamilyLint       Family = "lint"
	FamilyLog        Family = "log"
	FamilyGit        Family = "git"
	FamilyDiff       Family = "diff"
	FamilyGrep       Family = "grep"
	FamilyRead       Family = "read"
	FamilyList       Family = "list"
	FamilyGlob       Family = "glob"
	FamilyStructured Family = "structured"
)

// Selector describes the tool call that produced one output.
type Selector struct {
	Client  string // claude | codex | cursor | pi | opencode | letta | mcp | http
	Tool    string // the client's tool name (Bash, Read, grep, mcp__server__tool, ...)
	Command string // shell command line, when the tool ran one
	Path    string // file or directory the tool addressed
	// StartLine is the line number of the first captured line (a read of a range);
	// 0 means 1. FromLine/ToLine bound the requested range (absolute, inclusive; 0 =
	// open), for reads that captured more than was asked for.
	StartLine, FromLine, ToLine int
	ExitCode                    *int
	// Family forces a family (the caller knows, e.g., that a command ran tests).
	Family Family
}

// Section is a named byte range of one captured original (for example the stdout
// and stderr strings of a decoded hook body). Offsets are absolute.
type Section struct {
	Name  string `json:"name"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	// Array marks a JSON array of strings stored one element per line (Claude
	// Glob/Grep filenames); its projection keeps that form.
	Array bool `json:"array,omitempty"`
}

// Input is one original as a family reducer sees it.
type Input struct {
	R           io.ReaderAt
	N           int64
	ContentType string
	Sel         Selector
	Sections    []Section // nil means one "output" section covering [0,N)
	Target      int       // projection budget in bytes
	Max         int       // hard cap in bytes
}

// Facts are the counts and anchors a family reducer extracted from the whole
// original. They are exact for the scanned bytes, whatever the projection kept.
type Facts struct {
	ExitCode      *int     `json:"exit_code,omitempty"`
	ExitFrom      string   `json:"exit_from,omitempty"` // tool (reported by the client) | text (parsed from the output)
	Lines         int      `json:"lines"`
	Passed        int      `json:"passed,omitempty"`
	Failed        int      `json:"failed,omitempty"`
	Skipped       int      `json:"skipped,omitempty"`
	CountsFrom    string   `json:"counts_from,omitempty"` // summary | lines
	FailingTests  []string `json:"failing_tests,omitempty"`
	FailureLines  int      `json:"failure_lines,omitempty"`
	FailuresShown int      `json:"failures_shown,omitempty"`
	Errors        int      `json:"errors,omitempty"`
	Warnings      int      `json:"warnings,omitempty"`
	Collapsed     int      `json:"collapsed_lines,omitempty"`
	Repeats       int      `json:"repeated_lines,omitempty"`
	Matches       int      `json:"matches,omitempty"`
	Files         int      `json:"files,omitempty"`
	Entries       int      `json:"entries,omitempty"`
	Additions     int      `json:"additions,omitempty"`
	Deletions     int      `json:"deletions,omitempty"`
	Shown         int      `json:"shown,omitempty"`

	sum *testSummary // counts a test runner printed (reduce_testrun.go)
}

// FamilyRecord is persisted with the projection record (Record.Family): which family
// reducer produced it, for which client and output shape, and what it extracted.
type FamilyRecord struct {
	Family   Family    `json:"family"`
	Reducer  string    `json:"reducer"` // id/version, e.g. xm-test/1
	Client   string    `json:"client,omitempty"`
	Tool     string    `json:"tool,omitempty"`
	Argv0    string    `json:"argv0,omitempty"`
	Shape    string    `json:"output_shape,omitempty"`
	Sections []Section `json:"sections,omitempty"`
	Facts    Facts     `json:"facts"`
	// Capture is the capture metadata of a universal capture (capture_meta.go).
	Capture *CaptureMeta `json:"capture,omitempty"`
}

// CaptureMeta is the metadata of a universal capture (PAR-CTX-01, capture_meta.go),
// persisted with the projection record.
type CaptureMeta struct {
	Client           string    `json:"client"`
	Format           string    `json:"format"`
	Tool             string    `json:"tool"`
	ToolVersion      string    `json:"tool_version,omitempty"`
	CallID           string    `json:"call_id,omitempty"`
	SessionID        string    `json:"session_id,omitempty"`
	AgentID          string    `json:"agent_id,omitempty"`
	Principal        string    `json:"principal,omitempty"`
	ArgsDigest       string    `json:"args_digest,omitempty"`
	IsError          bool      `json:"is_error"`
	ExitCode         *int      `json:"exit_code,omitempty"`
	ContentType      string    `json:"content_type"`
	OutputShape      string    `json:"output_shape"`
	HookEvent        string    `json:"hook_event,omitempty"`
	BodySHA256       string    `json:"body_sha256,omitempty"`
	BodyBytes        int64     `json:"body_bytes,omitempty"`
	CapturedIdentity string    `json:"captured_identity"`
	Sections         []Section `json:"sections,omitempty"`
}

// Projection is a family reducer's result.
type Projection struct {
	Text string // the model-facing projection of the whole original
	// Parts holds one projection per section, for shape adapters that rebuild a
	// client payload field by field (Claude Bash stdout/stderr, Read file.content).
	Parts map[string]string
	// Structured is the per-kind projection (read {path, line_count, excerpt}, list
	// entries, search matches) when the family defines one.
	Structured any
	Record     Record
	Facts      Facts
}

// Reducer is one versioned, deterministic projection rule set for a tool family.
type Reducer interface {
	Family() Family
	ID() string
	Version() int
	Reduce(ctx context.Context, in *Input) (*Projection, error)
}

// reducerName is the id/version recorded with every projection.
func reducerName(r Reducer) string { return r.ID() + "/" + strconv.Itoa(r.Version()) }

// Registry maps tool families to their reducers.
type Registry struct {
	byFamily map[Family]Reducer
}

// NewRegistry builds a registry; a later reducer for the same family replaces an
// earlier one.
func NewRegistry(rs ...Reducer) *Registry {
	g := &Registry{byFamily: map[Family]Reducer{}}
	for _, r := range rs {
		g.byFamily[r.Family()] = r
	}
	return g
}

// DefaultRegistry holds every built-in family reducer.
func DefaultRegistry() *Registry {
	return NewRegistry(
		lineReducer{rules: shellRules}, lineReducer{rules: buildRules}, lineReducer{rules: logRules},
		lineReducer{rules: gitRules}, lineReducer{rules: testRules}, lintReducer{}, diffReducer{},
		grepReducer{}, readReducer{}, listReducer{glob: false}, listReducer{glob: true},
		structuredReducer{},
	)
}

// ReducerInfo describes one registered reducer.
type ReducerInfo struct {
	Family  Family `json:"family"`
	Reducer string `json:"reducer"`
}

// Reducers lists the registered reducers in family order.
func (g *Registry) Reducers() []ReducerInfo {
	out := make([]ReducerInfo, 0, len(g.byFamily))
	for f, r := range g.byFamily {
		out = append(out, ReducerInfo{Family: f, Reducer: reducerName(r)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Family < out[j].Family })
	return out
}

// Lookup returns the reducer for a family.
func (g *Registry) Lookup(f Family) (Reducer, bool) {
	r, ok := g.byFamily[f]
	return r, ok
}

// Select picks the reducer for a tool call, deterministically: an explicit family,
// else the client tool name, else (for shell tools) the command's argv0 and
// subcommand. It also returns the argv0 the decision used, if any.
func (g *Registry) Select(sel Selector) (Reducer, string) {
	fam, argv0 := SelectFamily(sel)
	if r, ok := g.byFamily[fam]; ok {
		return r, argv0
	}
	return g.byFamily[FamilyShell], argv0
}

// toolFamilies maps normalized client tool names to families. Shell tools are
// resolved further by their command.
var toolFamilies = map[string]Family{
	"read": FamilyRead, "read_file": FamilyRead, "readfile": FamilyRead, "view": FamilyRead,
	"open_file": FamilyRead, "view_file": FamilyRead, "cat": FamilyRead,
	"grep": FamilyGrep, "rg": FamilyGrep, "ripgrep": FamilyGrep, "search": FamilyGrep,
	"grep_search": FamilyGrep, "search_files": FamilyGrep, "search_file_content": FamilyGrep,
	"glob": FamilyGlob, "find": FamilyGlob, "find_files": FamilyGlob, "file_search": FamilyGlob,
	"ls": FamilyList, "list": FamilyList, "list_dir": FamilyList, "list_directory": FamilyList,
	"listdir": FamilyList, "list_files": FamilyList,
	"git_diff": FamilyDiff, "diff": FamilyDiff,
}

var shellTools = map[string]bool{
	"bash": true, "shell": true, "sh": true, "exec": true, "exec_command": true, "run_terminal_cmd": true,
	"run_shell_command": true, "terminal": true, "execute_command": true, "local_shell": true,
	"powershell": true, "command": true, "run_command": true, "bashoutput": true,
}

// SelectFamily returns the family for a tool call and the argv0 it used.
func SelectFamily(sel Selector) (Family, string) {
	if sel.Family != "" {
		return sel.Family, ""
	}
	tool := strings.ToLower(strings.TrimSpace(sel.Tool))
	// namespaced MCP tools: mcp__server__tool, server.tool, server/tool
	if i := strings.LastIndex(tool, "__"); i >= 0 {
		tool = tool[i+2:]
	}
	if i := strings.LastIndexAny(tool, "./"); i >= 0 {
		tool = tool[i+1:]
	}
	if f, ok := toolFamilies[tool]; ok && (sel.Command == "" || !shellTools[tool]) {
		return f, ""
	}
	if sel.Command != "" {
		return commandFamily(sel.Command)
	}
	if shellTools[tool] {
		return FamilyShell, ""
	}
	return FamilyStructured, ""
}

// commandFamily classifies a shell command line. Every simple command of the line
// (split on ;, &&, || and |) is classified and the most specific family wins, so
// "cd x && go test ./... 2>&1 | tail -50" is a test run and "git diff | head" a diff.
func commandFamily(cmdline string) (Family, string) {
	best, bestArgv0, bestRank := FamilyShell, "", -1
	for _, simple := range splitCommand(cmdline) {
		argv := commandWords(simple)
		if len(argv) == 0 {
			continue
		}
		f := argvFamily(argv)
		if inner, ok := shellWrapped(argv); ok {
			// bash -lc "go test ./..." classifies the wrapped command line
			f, argv = commandFamilyWords(inner)
		}
		if r := familyRank[f]; r > bestRank {
			best, bestArgv0, bestRank = f, path.Base(argv[0]), r
		}
	}
	return best, bestArgv0
}

// shellWrapped returns the command string of `bash -c CMD` / `sh -lc CMD` style argv.
func shellWrapped(argv []string) (string, bool) {
	switch path.Base(argv[0]) {
	case "bash", "sh", "zsh", "dash", "ksh":
	default:
		return "", false
	}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "-") {
			return "", false
		}
		if !strings.HasPrefix(a, "--") && strings.ContainsRune(a[1:], 'c') && i+1 < len(argv) {
			return strings.Join(argv[i+1:], " "), true // -c, -lc, -ic
		}
	}
	return "", false
}

// commandFamilyWords classifies a wrapped command line and returns the argv of its
// most specific part.
func commandFamilyWords(cmdline string) (Family, []string) {
	best, bestArgv, bestRank := FamilyShell, []string{"sh"}, -1
	for _, simple := range splitCommand(cmdline) {
		argv := commandWords(simple)
		if len(argv) == 0 {
			continue
		}
		if f := argvFamily(argv); familyRank[f] > bestRank {
			best, bestArgv, bestRank = f, argv, familyRank[f]
		}
	}
	return best, bestArgv
}

// familyRank orders families by specificity when a command line has several parts.
var familyRank = map[Family]int{
	FamilyShell: 0, FamilyRead: 1, FamilyGit: 2, FamilyList: 3, FamilyGlob: 4, FamilyLog: 5,
	FamilyGrep: 6, FamilyDiff: 7, FamilyLint: 8, FamilyBuild: 9, FamilyTest: 10,
}

// splitCommand splits a command line on ;, &&, || and | outside quotes.
func splitCommand(s string) []string {
	var parts []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(s) {
				cur.WriteByte(c)
				i++
				c = s[i]
			}
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			quote = c
			cur.WriteByte(c)
		case c == ';' || c == '|' || c == '&' || c == '\n':
			if c == '&' && (i+1 >= len(s) || s[i+1] != '&') {
				// a lone & backgrounds a command, and "2>&1" / "&>" are redirections
				cur.WriteByte(c)
				continue
			}
			parts = append(parts, cur.String())
			cur.Reset()
			if i+1 < len(s) && (s[i+1] == '&' || s[i+1] == '|') && c != ';' {
				i++
			}
		default:
			cur.WriteByte(c)
		}
	}
	return append(parts, cur.String())
}

// commandWords returns the words of one simple command with leading environment
// assignments, wrappers (sudo, time, env, nice, timeout N, npx, uv run, poetry run)
// and redirections removed.
func commandWords(simple string) []string {
	var words []string
	for _, w := range strings.Fields(simple) {
		w = strings.Trim(w, `"'()`)
		if w == "" || strings.HasPrefix(w, ">") || strings.HasPrefix(w, "<") || strings.HasPrefix(w, "2>") || strings.HasPrefix(w, "&>") {
			continue
		}
		words = append(words, w)
	}
	for len(words) > 0 {
		w := words[0]
		switch {
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "="):
			words = words[1:] // FOO=bar
		case w == "sudo" || w == "time" || w == "env" || w == "nice" || w == "command" || w == "exec" ||
			w == "npx" || w == "bunx" || w == "pnpx" || w == "xargs" || w == "stdbuf" || w == "unbuffer":
			words = words[1:]
		case w == "timeout" && len(words) > 1:
			words = words[2:]
		case (w == "uv" || w == "poetry" || w == "pipenv" || w == "rye" || w == "hatch" || w == "pdm") && len(words) > 1 && words[1] == "run":
			words = words[2:]
		case (w == "python" || w == "python3" || strings.HasPrefix(w, "python3.")) && len(words) > 2 && words[1] == "-m":
			words = words[2:] // python -m pytest → pytest
		default:
			return words
		}
	}
	return words
}

var (
	testCommands = map[string]bool{
		"pytest": true, "py.test": true, "jest": true, "vitest": true, "mocha": true, "rspec": true,
		"phpunit": true, "ctest": true, "tox": true, "nox": true, "ava": true, "tap": true, "karma": true,
		"unittest": true, "nosetests": true, "playwright": true, "cypress": true,
	}
	buildCommands = map[string]bool{
		"tsc": true, "gcc": true, "g++": true, "clang": true, "clang++": true, "javac": true, "rustc": true,
		"webpack": true, "esbuild": true, "rollup": true, "cmake": true, "ninja": true, "bazel": true,
		"msbuild": true, "xcodebuild": true, "swiftc": true, "kotlinc": true, "scalac": true, "turbo": true,
	}
	lintCommands = map[string]bool{
		"eslint": true, "golangci-lint": true, "ruff": true, "flake8": true, "pylint": true, "mypy": true,
		"pyright": true, "staticcheck": true, "shellcheck": true, "rubocop": true, "stylelint": true,
		"biome": true, "oxlint": true, "hadolint": true, "markdownlint": true, "revive": true, "golint": true,
		"tflint": true, "yamllint": true, "pycodestyle": true, "pydocstyle": true, "bandit": true, "semgrep": true,
	}
	logCommands  = map[string]bool{"tail": true, "journalctl": true, "dmesg": true, "logcat": true, "stern": true}
	grepCommands = map[string]bool{"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true, "ugrep": true}
	listCommands = map[string]bool{"ls": true, "tree": true, "exa": true, "eza": true, "dir": true, "lsd": true}
	globCommands = map[string]bool{"find": true, "fd": true, "fdfind": true, "locate": true}
	readCommands = map[string]bool{"cat": true, "head": true, "nl": true, "bat": true, "less": true, "more": true, "sed": true, "awk": true}
)

// argvFamily classifies one simple command by argv0 and subcommand.
func argvFamily(argv []string) Family {
	a0 := strings.ToLower(path.Base(argv[0]))
	sub := ""
	if len(argv) > 1 {
		sub = strings.ToLower(argv[1])
	}
	rest := strings.ToLower(strings.Join(argv[1:], " "))
	switch {
	case testCommands[a0]:
		return FamilyTest
	case lintCommands[a0]:
		return FamilyLint
	case buildCommands[a0]:
		return FamilyBuild
	case a0 == "go":
		switch sub {
		case "test":
			return FamilyTest
		case "vet":
			return FamilyLint
		case "build", "install", "run", "generate", "mod":
			return FamilyBuild
		}
	case a0 == "cargo":
		switch sub {
		case "test", "nextest":
			return FamilyTest
		case "clippy", "build", "check", "run", "install", "doc", "fmt":
			return FamilyBuild
		}
	case a0 == "npm" || a0 == "yarn" || a0 == "pnpm" || a0 == "bun" || a0 == "deno":
		script := sub
		if sub == "run" && len(argv) > 2 {
			script = strings.ToLower(argv[2])
		}
		switch {
		case strings.HasPrefix(script, "test") || script == "t" || strings.HasPrefix(script, "e2e") || script == "vitest" || script == "jest":
			return FamilyTest
		case strings.HasPrefix(script, "lint") || script == "eslint":
			return FamilyLint
		case strings.HasPrefix(script, "build") || script == "compile" || script == "typecheck" || script == "tsc" || script == "install" || script == "ci":
			return FamilyBuild
		}
	case a0 == "node" && strings.Contains(rest, "--test"):
		return FamilyTest
	case a0 == "make" || a0 == "just" || a0 == "task" || a0 == "rake":
		if strings.Contains(rest, "test") || strings.Contains(rest, "check") {
			return FamilyTest
		}
		if strings.Contains(rest, "lint") {
			return FamilyLint
		}
		return FamilyBuild
	case a0 == "mvn" || a0 == "gradle" || a0 == "gradlew" || a0 == "./gradlew" || a0 == "sbt" || a0 == "dotnet" || a0 == "swift" || a0 == "zig" || a0 == "mix":
		if strings.Contains(rest, "test") {
			return FamilyTest
		}
		return FamilyBuild
	case a0 == "git":
		switch sub {
		case "diff", "show", "format-patch":
			return FamilyDiff
		case "log":
			if strings.Contains(" "+rest+" ", " -p ") || strings.Contains(rest, "--patch") {
				return FamilyDiff
			}
			return FamilyGit
		case "grep":
			return FamilyGrep
		case "ls-files":
			return FamilyGlob
		}
		return FamilyGit
	case a0 == "diff" || a0 == "colordiff":
		return FamilyDiff
	case a0 == "docker" || a0 == "kubectl" || a0 == "podman" || a0 == "oc":
		if strings.Contains(" "+rest+" ", " logs ") || sub == "logs" {
			return FamilyLog
		}
		if sub == "build" {
			return FamilyBuild
		}
	case logCommands[a0]:
		if a0 == "tail" && !strings.Contains(" "+rest+" ", " -f ") && !strings.Contains(rest, "--follow") && !strings.Contains(rest, ".log") {
			return FamilyRead
		}
		return FamilyLog
	case grepCommands[a0]:
		if a0 == "rg" && (strings.Contains(" "+rest+" ", " --files ") || strings.HasSuffix(rest, "--files")) {
			return FamilyGlob
		}
		return FamilyGrep
	case listCommands[a0]:
		return FamilyList
	case globCommands[a0]:
		return FamilyGlob
	case readCommands[a0]:
		return FamilyRead
	}
	return FamilyShell
}

// --- the registry hook (reduce.go) ---

type hookKey struct{}

// reduceHook carries one capture's family selection into Reduce and its result back
// out to the capture pipeline. It is used by one Capture call only.
type reduceHook struct {
	reducer Reducer
	argv0   string
	in      Input // Sel, Sections and the client Target; R, N, ContentType, Max come from Reduce
	shape   string
	meta    *CaptureMeta // persisted with the projection record
	out     *Projection
}

func withReduceHook(ctx context.Context, h *reduceHook) context.Context {
	return context.WithValue(ctx, hookKey{}, h)
}

func reduceHookFrom(ctx context.Context) *reduceHook {
	h, _ := ctx.Value(hookKey{}).(*reduceHook)
	return h
}

// run reduces one original with the selected family reducer. Output at or below the
// target passes through unchanged (nothing omitted, nothing retained), exactly like
// the generic reducer; a family reducer never sees binary or declared-invalid input.
func (h *reduceHook) run(ctx context.Context, r io.ReaderAt, n int64, contentType string, target, max int) (string, Record, error) {
	in := h.in
	in.R, in.N, in.ContentType, in.Max = r, n, contentType, max
	if in.Target <= 0 || in.Target > target {
		in.Target = target
	}
	if in.Target > in.Max {
		in.Target = in.Max
	}
	if len(in.Sections) == 0 {
		in.Sections = []Section{{Name: "output", Start: 0, End: n}}
	}
	name := reducerName(h.reducer)
	rec := Record{Reducer: name, RawBytes: n}
	famRec := func(f Facts) *FamilyRecord {
		return &FamilyRecord{Family: h.reducer.Family(), Reducer: name, Client: in.Sel.Client, Tool: in.Sel.Tool,
			Argv0: h.argv0, Shape: h.shape, Sections: in.Sections, Facts: f, Capture: h.meta}
	}
	if n <= int64(in.Target) {
		buf := make([]byte, n)
		if m, err := r.ReadAt(buf, 0); int64(m) != n || (err != nil && err != io.EOF) {
			return "", rec, ErrCorrupt
		}
		if !utf8.Valid(buf) {
			return "", rec, fmt.Errorf("%w: non-UTF-8 output (%s) cannot be delivered as text", ErrUnsupported, contentType)
		}
		p := &Projection{Text: string(buf), Parts: map[string]string{}}
		for _, s := range in.Sections {
			p.Parts[s.Name] = string(buf[s.Start:s.End])
		}
		p.Facts.ExitCode = in.Sel.ExitCode
		p.Facts.Lines = countLines(buf)
		rec.Mode, rec.ProjectedBytes = "passthrough", len(buf)
		rec.Family = famRec(p.Facts)
		p.Record = rec
		h.out = p
		return p.Text, rec, nil
	}
	if h.reducer.Family() != FamilyStructured && classify(r, n, contentType) == "unsupported" {
		// binary, multimodal or declared-but-invalid structured output: the generic
		// rules decide (labeled passthrough within the cap, or an explicit refusal)
		text, grec, err := reduceGeneric(ctx, r, n, contentType, in.Target, max, false)
		if err != nil {
			return "", grec, err
		}
		grec.Family = famRec(Facts{ExitCode: in.Sel.ExitCode})
		h.out = &Projection{Text: text, Parts: map[string]string{}, Record: grec}
		return text, grec, nil
	}
	p, err := h.reducer.Reduce(ctx, &in)
	if err != nil {
		return "", rec, err
	}
	if len(p.Text) > max {
		return "", rec, fmt.Errorf("%w: %s projection %d bytes exceeds %d", ErrUnsupported, name, len(p.Text), max)
	}
	if !utf8.ValidString(p.Text) {
		return "", rec, fmt.Errorf("%w: %s produced invalid UTF-8", ErrCorrupt, name)
	}
	p.Record.Reducer, p.Record.RawBytes, p.Record.ProjectedBytes = name, n, len(p.Text)
	if p.Record.Mode == "" {
		p.Record.Mode = "text"
	}
	p.Record.Family = famRec(p.Facts)
	h.out = p
	return p.Text, p.Record, nil
}

func countLines(b []byte) int {
	n := bytes.Count(b, []byte("\n"))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// --- structured payloads ---

// statusKeys name the members that report an outcome. The structured family keeps
// their scalar values verbatim in every reduced object: a bounded payload never
// loses its status (the cursor-bridge gap, CB-04).
var statusKeys = map[string]bool{
	"status": true, "state": true, "ok": true, "success": true, "succeeded": true, "error": true,
	"is_error": true, "iserror": true, "exit_code": true, "exitcode": true, "exit_status": true,
	"returncode": true, "return_code": true, "code": true, "conclusion": true, "outcome": true,
	"passed": true, "failed": true, "interrupted": true, "timed_out": true, "timedout": true,
	"cancelled": true, "canceled": true, "truncated": true, "result": true, "status_code": true,
	"statuscode": true, "severity": true, "level": true,
}

func isStatusKey(raw []byte) bool {
	if len(raw) > 24 {
		return false
	}
	return statusKeys[strings.ToLower(string(raw))]
}

// structuredReducer bounds JSON payloads (MCP results of other servers, JSON tool
// output) with the generic shape-preserving reducer, keeping status members. Non-JSON
// text falls back to the shell rules.
type structuredReducer struct{}

func (structuredReducer) Family() Family { return FamilyStructured }
func (structuredReducer) ID() string     { return "xm-structured" }
func (structuredReducer) Version() int   { return 1 }

func (structuredReducer) Reduce(ctx context.Context, in *Input) (*Projection, error) {
	secs := nonEmpty(in.Sections)
	anyJSON := false
	for _, sec := range secs {
		n := sec.End - sec.Start
		anyJSON = anyJSON || classify(io.NewSectionReader(in.R, sec.Start, n), n, "") == "json"
	}
	if !anyJSON {
		return reduceLineSections(ctx, in, shellRules) // plain text blocks: one header
	}
	shares := sectionShares(secs, in.Target-256)
	var out bytes.Buffer
	parts := map[string]string{}
	var oms []Omission
	mode := "json"
	for i, sec := range secs {
		n := sec.End - sec.Start
		sr := io.NewSectionReader(in.R, sec.Start, n)
		var text string
		switch {
		case classify(sr, n, "") == "json":
			t, rec, err := reduceGeneric(ctx, sr, n, "application/json", shares[i], in.Max-out.Len(), true)
			if err != nil {
				return nil, err
			}
			for _, o := range rec.Omissions {
				if o.Start >= 0 {
					o.Start += sec.Start
				}
				o.End += sec.Start
				oms = append(oms, o)
			}
			text = t
		default:
			sub := *in
			sub.Sections, sub.Target = []Section{sec}, shares[i]
			p, err := reduceLineSections(ctx, &sub, shellRules)
			if err != nil {
				return nil, err
			}
			oms = append(oms, p.Record.Omissions...)
			text, mode = p.Parts[sec.Name], "text"
		}
		parts[sec.Name] = text
		if len(secs) > 1 {
			fmt.Fprintf(&out, "[%s]\n", sec.Name)
		}
		out.WriteString(text)
		if len(secs) > 1 && !strings.HasSuffix(text, "\n") {
			out.WriteByte('\n')
		}
	}
	for _, s := range in.Sections {
		if _, ok := parts[s.Name]; !ok {
			parts[s.Name] = ""
		}
	}
	facts := Facts{ExitCode: in.Sel.ExitCode}
	return &Projection{Text: out.String(), Parts: parts, Facts: facts,
		Record: Record{Mode: mode, Reduced: true, Omissions: capOmissions(oms)}}, nil
}

package hooks

import (
	"path"
	"strings"
	"unicode"
)

// Pattern extraction (PAR-HAR-02): what a native search tool call looks for, as a query
// the index and memory can answer. Grep and Glob carry the pattern in their input; a
// Bash command is parsed for an rg or grep invocation, whose pattern is the first
// non-flag argument of at least minPatternToken characters. Flags that take a value
// skip that value too, so `rg -g '*.go' -A 3 handler` looks for "handler", not
// "*.go" or "3". `-e`/`--regexp` names the pattern explicitly.

// minPatternToken is the shortest argument taken as a pattern.
const minPatternToken = 3

// maxQueryWords bounds the query a pattern becomes.
const maxQueryWords = 6

// searcher describes one command-line search program: the flags whose value is the
// pattern, and the flags that take some other value.
type searcher struct {
	patternFlags map[string]bool
	valueFlags   map[string]bool
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// grepFamily is shared by grep and its variants: -e names the pattern; -f reads it from
// a file; -m, -A, -B and -C take counts; --include and --exclude take globs.
var grepFamily = searcher{
	patternFlags: set("-e", "--regexp"),
	valueFlags: set("-f", "--file", "-m", "--max-count", "-A", "--after-context", "-B", "--before-context",
		"-C", "--context", "--include", "--exclude", "--exclude-dir", "--label", "-d", "--directories", "-D", "--devices"),
}

// searchers maps a program (its base name) to its flag table. rg adds -g/--glob and
// -t/-T/--type for file selection and a few value-taking output options.
var searchers = map[string]searcher{
	"grep":  grepFamily,
	"egrep": grepFamily,
	"fgrep": grepFamily,
	"rg": {
		patternFlags: set("-e", "--regexp"),
		valueFlags: set("-f", "--file", "-m", "--max-count", "-A", "--after-context", "-B", "--before-context",
			"-C", "--context", "-g", "--glob", "--iglob", "-t", "--type", "-T", "--type-not", "--type-add",
			"-j", "--threads", "-M", "--max-columns", "--max-depth", "--max-filesize", "-E", "--encoding",
			"-r", "--replace", "--pre", "--pre-glob", "--sort", "--sortr", "--color", "--colors", "--path-separator"),
	},
}

// SearchQuery returns the query a native search call looks for: the words of its
// pattern, or "" when the call is not a search or its pattern has no word of at least
// minPatternToken characters.
func SearchQuery(tool string, in ToolInput) string {
	switch tool {
	case "Grep":
		return patternWords(in.Pattern)
	case "Glob":
		return globWord(in.Pattern)
	case "Bash":
		return patternWords(ShellSearchPattern(in.Command))
	}
	return ""
}

// ShellSearchPattern returns the pattern of the first rg or grep invocation in a shell
// command line (pipelines and lists included), or "".
func ShellSearchPattern(command string) string {
	for _, argv := range shellCommands(command) {
		argv = skipAssignments(argv)
		if len(argv) == 0 {
			continue
		}
		prog := path.Base(argv[0])
		args := argv[1:]
		if prog == "git" && len(args) > 0 && args[0] == "grep" {
			prog, args = "grep", args[1:]
		}
		s, ok := searchers[prog]
		if !ok {
			continue
		}
		if p := s.pattern(args); p != "" {
			return p
		}
	}
	return ""
}

// pattern walks one invocation's arguments.
func (s searcher) pattern(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, inline := strings.Cut(a, "=")
		switch {
		case a == "--":
			return firstLong(args[i+1:])
		case s.patternFlags[name] && inline:
			return value
		case s.patternFlags[a]:
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case s.valueFlags[name] && inline:
		case s.valueFlags[a]:
			i++ // the flag's value is not the pattern
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// a boolean flag, or a cluster such as -rn
		case len([]rune(a)) >= minPatternToken:
			return a
		}
	}
	return ""
}

func firstLong(args []string) string {
	for _, a := range args {
		if len([]rune(a)) >= minPatternToken {
			return a
		}
	}
	return ""
}

// skipAssignments drops leading VAR=value words and the env and command prefixes.
func skipAssignments(argv []string) []string {
	for len(argv) > 0 {
		a := argv[0]
		name, _, isAssign := strings.Cut(a, "=")
		switch {
		case isAssign && name != "" && !strings.ContainsAny(name, "/-"):
		case a == "env" || a == "command" || a == "exec" || a == "time" || a == "nice":
		default:
			return argv
		}
		argv = argv[1:]
	}
	return argv
}

// patternWords turns a regular expression or a literal into query words: escape
// sequences such as \s and \b are dropped, the rest is split on anything that is not
// part of an identifier, and words shorter than minPatternToken are left out.
func patternWords(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			i++ // \s, \w, \., ...
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(pattern[i])
	}
	words := strings.FieldsFunc(b.String(), func(r rune) bool { return !isWordRune(r) })
	out := make([]string, 0, maxQueryWords)
	seen := map[string]bool{}
	for _, w := range words {
		if len([]rune(w)) < minPatternToken || seen[w] {
			continue
		}
		seen[w] = true
		if out = append(out, w); len(out) == maxQueryWords {
			break
		}
	}
	return strings.Join(out, " ")
}

// globWord is the longest word of a glob's last segment, before its extension:
// "src/**/handler*.go" looks for "handler"; "**/*.go" for nothing.
func globWord(glob string) string {
	base := path.Base(strings.ReplaceAll(glob, `\`, "/"))
	if dot := strings.LastIndexByte(base, '.'); dot > 0 {
		base = base[:dot]
	}
	best := ""
	for _, w := range strings.FieldsFunc(base, func(r rune) bool { return !isWordRune(r) }) {
		if len([]rune(w)) >= minPatternToken && len(w) > len(best) {
			best = w
		}
	}
	return best
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// GitHeadMove returns the git subcommand of a command line that moves HEAD or
// rewrites the working tree (commit, merge, rebase, cherry-pick, pull, ...), or "".
func GitHeadMove(command string) string {
	for _, argv := range shellCommands(command) {
		argv = skipAssignments(argv)
		if len(argv) < 2 || path.Base(argv[0]) != "git" {
			continue
		}
		if verb := gitVerb(argv[1:]); headMovers[verb] {
			return verb
		}
	}
	return ""
}

// headMovers are the git subcommands after which the index, drift and memory
// staleness must be re-read.
var headMovers = set("commit", "merge", "rebase", "cherry-pick", "pull", "revert", "reset", "checkout", "switch", "am")

// gitValueFlags are git's global options that take a separate value.
var gitValueFlags = set("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env")

// gitVerb skips git's global options and returns its subcommand.
func gitVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case gitValueFlags[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// shellCommands splits a command line into its simple commands (split on |, ||, &&,
// ; and newlines outside quotes), each as its words with quotes removed. It is a
// reader of intent, not a shell: expansions are kept as written.
func shellCommands(line string) [][]string {
	var cmds [][]string
	var words []string
	var cur strings.Builder
	started := false
	quote := byte(0)
	flushWord := func() {
		if started {
			words = append(words, cur.String())
			cur.Reset()
			started = false
		}
	}
	flushCmd := func() {
		flushWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == '"' && c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case quote != 0:
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			quote, started = c, true
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			started = true
		case c == ' ' || c == '\t':
			flushWord()
		case c == '|' || c == ';' || c == '&' || c == '\n' || c == '(' || c == ')':
			flushCmd()
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	flushCmd()
	return cmds
}

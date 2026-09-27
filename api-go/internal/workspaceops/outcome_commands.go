package workspaceops

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"xmustard/api-go/internal/redact"
	"xmustard/api-go/internal/rustcore"
)

// Commands why_failed runs (WS-21). A command is the caller's argv, checked against a
// closed table of test, build and lint invocations and run through the bounded Rust
// runner: no shell, a working directory inside the workspace root, the daemon's
// environment without its own configuration and secrets, and a timeout at which the
// runner terminates the command's whole process group. One command runs at a time.
// This narrows what a proposer can run; it is not a sandbox: a test or build runs the
// repository's own code (docs/SECURITY.md).

// runCheckCommand is the bounded runner; tests replace it.
var runCheckCommand = rustcore.RunManagedCommandEnv

// lookPath finds a bare program name on the daemon's PATH; tests replace it.
var lookPath = exec.LookPath

// preparedCommand is a checked command, ready for the runner.
type preparedCommand struct {
	argv    []string // argv[0] is the resolved program
	dir     string   // the absolute working directory
	rel     string   // the working directory relative to the root ("" is the root)
	timeout int      // seconds
}

// prepareCommand checks req's command in this order, failing closed: its words, the
// program's rule, the working directory, the arguments' paths, the program's file and
// the timeout.
func prepareCommand(root string, req FailureRequest) (*preparedCommand, error) {
	if strings.TrimSpace(root) == "" {
		return nil, Invalid("the workspace has no root directory to run a command in")
	}
	if err := checkCommandWords(req.Argv); err != nil {
		return nil, err
	}
	if err := checkCommandRule(req.Argv); err != nil {
		return nil, err
	}
	dir, rel, err := confineCommandDir(root, req.Cwd)
	if err != nil {
		return nil, err
	}
	if err := checkArgumentPaths(root, rel, req.Argv[1:]); err != nil {
		return nil, err
	}
	program, err := resolveProgram(root, rel, req.Argv[0])
	if err != nil {
		return nil, err
	}
	timeout := cmp.Or(req.TimeoutSeconds, DefaultWhyFailedTimeout)
	if timeout < 1 || timeout > MaxWhyFailedTimeout {
		return nil, Invalid(fmt.Sprintf("timeout_seconds must be between 1 and %d", MaxWhyFailedTimeout))
	}
	return &preparedCommand{argv: append([]string{program}, req.Argv[1:]...), dir: dir, rel: rel, timeout: timeout}, nil
}

// --- words --------------------------------------------------------------------------

// shellStarts start the words a shell reads as an operator or a redirection (|, ||, &,
// &&, ;, >, >>, <, <<, 2>&1, &>), and shellSubstitutions are what it expands anywhere in
// a word. The command runs without a shell, so they would reach the program as literal
// text: they are refused instead. Other text ($VAR, globs) reaches it unexpanded.
var (
	shellStarts        = []string{"|", "&", ";", ">", "<", "1>", "2>"}
	shellSubstitutions = []string{"$(", "`"}
)

func shellSyntax(word string) bool {
	return slices.ContainsFunc(shellStarts, func(p string) bool { return strings.HasPrefix(word, p) }) ||
		slices.ContainsFunc(shellSubstitutions, func(s string) bool { return strings.Contains(word, s) })
}

func checkCommandWords(argv []string) error {
	total := 0
	for _, a := range argv {
		total += len(a)
		if strings.ContainsRune(a, 0) {
			return Invalid("command arguments may not contain NUL")
		}
		if shellSyntax(a) {
			return Invalid(fmt.Sprintf("command runs as argv without a shell: %q (a pipe, command list, redirection or substitution) is not supported; run it in your shell and pass the output as log", a))
		}
	}
	if len(argv) > maxWhyFailedArgs || total > maxWhyFailedArgv {
		return Invalid(fmt.Sprintf("command is limited to %d arguments and %d bytes", maxWhyFailedArgs, maxWhyFailedArgv))
	}
	if strings.TrimSpace(argv[0]) == "" {
		return Invalid("command has no program")
	}
	return nil
}

// --- the program table ----------------------------------------------------------------

// commandRule admits one program's arguments. A nil args check means the program is
// itself a test runner, linter or type checker and takes any arguments.
type commandRule struct {
	args func(args []string) error
	// refused are flag prefixes, in their one-dash spelling ("--config" is read as
	// "-config"), that choose a program, shell, linker or configuration to run.
	refused []string
}

var (
	directRule = commandRule{}
	scriptRule = commandRule{args: checkScript, refused: []string{"-script-shell", "-node-options", "-onload-script", "-config"}}
	taskRule   = commandRule{args: checkTasks}
)

// whyFailedCommands is the closed table of programs why_failed runs, by the program's
// last path element. Anything else (a shell, an interpreter, curl, a package install,
// go run, cargo install, npm publish, make deploy) is refused.
var whyFailedCommands = map[string]commandRule{
	"go":            {args: verbs("test", "vet", "build"), refused: []string{"-exec", "-toolexec", "-vettool", "-ldflags", "-gccgoflags", "-compiler"}},
	"cargo":         {args: verbs("test", "nextest", "check", "clippy", "build"), refused: []string{"-config", "-Z"}},
	"dotnet":        {args: verbs("test", "build")},
	"swift":         {args: verbs("test", "build")},
	"golangci-lint": {args: verbs("run")},
	"ruff":          {args: verbs("check")},
	"biome":         {args: verbs("check", "lint", "ci")},

	"npm": scriptRule, "pnpm": scriptRule, "yarn": scriptRule, "bun": scriptRule,

	"make": taskRule, "just": taskRule, "task": taskRule, "gradle": taskRule, "gradlew": taskRule, "mvn": taskRule, "mvnw": taskRule,

	"pytest": directRule, "py.test": directRule, "unittest": directRule, "jest": directRule, "vitest": directRule,
	"mocha": directRule, "rspec": directRule, "phpunit": directRule, "ctest": directRule, "tsc": directRule,
	"eslint": directRule, "flake8": directRule, "pylint": directRule, "mypy": directRule, "pyright": directRule,
	"staticcheck": directRule, "shellcheck": directRule, "rubocop": directRule, "stylelint": directRule, "oxlint": directRule,
}

// pythonModules are the table's programs `python -m` may run.
var pythonModules = map[string]bool{"pytest": true, "unittest": true, "mypy": true, "pylint": true, "flake8": true, "ruff": true}

// checkCommandRule applies the program's rule from whyFailedCommands.
func checkCommandRule(argv []string) error {
	name, args := commandName(argv)
	rule, ok := whyFailedCommands[name]
	if !ok {
		return notACheck(argv[0], "is not a test, build or lint program")
	}
	if rule.args != nil {
		if err := rule.args(args); err != nil {
			return notACheck(argv[0], err.Error())
		}
	}
	for _, a := range args {
		flag := a
		if strings.HasPrefix(a, "--") {
			flag = a[1:]
		}
		if slices.ContainsFunc(rule.refused, func(p string) bool { return strings.HasPrefix(flag, p) }) {
			return notACheck(argv[0], "takes no "+a+": it chooses a program, shell or configuration to run")
		}
	}
	return nil
}

func notACheck(program, why string) error {
	return Invalid(fmt.Sprintf("why_failed runs test, build and lint commands only (%s %s); run other commands in your shell and pass the output as log", program, why))
}

// commandName is argv's table key and the arguments its rule reads: the program's last
// path element, or the module of `python -m <module>`.
func commandName(argv []string) (string, []string) {
	name, args := filepath.Base(argv[0]), argv[1:]
	python := name == "python" || name == "python3" || strings.HasPrefix(name, "python3.")
	if python && len(args) > 1 && args[0] == "-m" && pythonModules[args[1]] {
		return args[1], args[2:]
	}
	return name, args
}

// verbs admits a program with subcommands: argv[1] must be one of vs.
func verbs(vs ...string) func([]string) error {
	return func(args []string) error {
		if len(args) == 0 || !slices.Contains(vs, args[0]) {
			return fmt.Errorf("runs only as %s", strings.Join(vs, ", "))
		}
		return nil
	}
}

// checkScript admits a package script runner running a check-named script: npm test,
// yarn lint, pnpm run build:prod, bun test.
func checkScript(args []string) error {
	if len(args) > 1 && (args[0] == "run" || args[0] == "run-script") {
		args = args[1:]
	}
	if len(args) == 0 || !isCheckName(args[0]) {
		return errors.New("runs only a test, check, lint or build script (npm test, npm run lint)")
	}
	return nil
}

// taskFlags are the task-runner flags why_failed passes; -j and --jobs may carry a
// count (-j4, --jobs=4).
var taskFlags = map[string]bool{
	"-k": true, "-s": true, "-q": true, "-B": true, "-j": true, "--jobs": true, "--keep-going": true, "--silent": true,
	"--quiet": true, "--offline": true, "--continue": true, "--stacktrace": true, "--info": true, "--no-daemon": true,
	"--batch-mode": true,
}

// checkTasks admits a task runner (make, just, task, gradle, mvn) running check-named
// tasks with the flags in taskFlags: no build file, directory, include or variable
// (-f, -C, -I, VAR=value), and no default target.
func checkTasks(args []string) error {
	tasks := 0
	for _, a := range args {
		task := a
		if strings.HasPrefix(a, ":") { // a gradle project path, :app:test
			task = a[strings.LastIndexByte(a, ':')+1:]
		}
		switch {
		case strings.HasPrefix(a, "-"):
			if !taskFlags[strings.TrimSuffix(strings.TrimRight(a, "0123456789"), "=")] {
				return fmt.Errorf("takes no %s (only task names and -k, -s, -q, -B, -jN)", a)
			}
		case isCheckName(task):
			tasks++
		default:
			return fmt.Errorf("runs only test, check, lint or build tasks, not %s", a)
		}
	}
	if tasks == 0 {
		return errors.New("needs a test, check, lint or build task named (its default target could be anything)")
	}
	return nil
}

// checkNames head the task and script names why_failed runs.
var checkNames = []string{"test", "tests", "check", "lint", "build", "vet", "verify", "typecheck", "compile", "e2e"}

// isCheckName reports whether name is a check name, or one continued by a separator or
// a capital (test-unit, lint:fix, check_all, testDebugUnitTest), but not a longer word
// (testdata, checkout) or a variable assignment.
func isCheckName(name string) bool {
	if strings.ContainsAny(name, "=/") {
		return false
	}
	return slices.ContainsFunc(checkNames, func(w string) bool {
		rest, ok := strings.CutPrefix(name, w)
		return ok && (rest == "" || !unicode.IsLower(rune(rest[0])))
	})
}

// --- paths ----------------------------------------------------------------------------

// confineCommandDir resolves the repo-relative working directory inside root.
func confineCommandDir(root, cwd string) (dir, rel string, err error) {
	if strings.TrimSpace(cwd) == "" || strings.TrimSpace(cwd) == "." {
		return root, "", nil
	}
	rel, err = ConfineWorkspacePath(root, cwd)
	if err != nil {
		return "", "", Invalid("cwd must be a directory inside the workspace root").WithCause(err)
	}
	dir, err = resolveWorkspacePath(root, rel)
	if err != nil {
		return "", "", Invalid("cwd must be a directory inside the workspace root").WithCause(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", "", Invalid("cwd " + rel + " is not a directory in the workspace")
	}
	return dir, rel, nil
}

// outsidePath finds an absolute or home path, or a parent-directory element, anywhere a
// program may read a path in an argument: at its start (after a one-letter flag such as
// -I), or after a character a value may be split at (= : , ; @ space quote). A program
// can take any argument as a path, so no argument may start outside the working
// directory or climb above it; cwd addresses another part of the tree.
var outsidePath = regexp.MustCompile(`(?:^(?:-[A-Za-z])?|[=:,;@\s'"])[/\\~]|(?:^(?:-[A-Za-z])?|[=:,;@\s'"/\\])\.\.(?:$|[=:,;@\s'"/\\])`)

// checkArgumentPaths refuses arguments that name a path outside the working directory,
// lexically or, for an argument or a flag's value, through a symlink in the repository.
func checkArgumentPaths(root, cwdRel string, args []string) error {
	for _, a := range args {
		if outsidePath.MatchString(a) {
			return Invalid(fmt.Sprintf("command argument %q names a path outside the working directory (absolute, ~ or ..); pass paths relative to cwd, or set cwd", a))
		}
		value := a
		if strings.HasPrefix(a, "-") {
			_, value, _ = strings.Cut(a, "=")
		}
		if value == "" || strings.HasPrefix(value, "-") {
			continue
		}
		if _, err := ConfineWorkspacePath(root, filepath.Join(cwdRel, value)); err != nil {
			return Invalid(fmt.Sprintf("command argument %q resolves outside the workspace root", a)).WithCause(err)
		}
	}
	return nil
}

// resolveProgram returns the file the runner executes: a program named by path,
// resolved from the working directory with its symlinks and confined to the root, or a
// bare name found on the daemon's PATH. A program that is not an executable file is
// refused here, so a command that could never start answers 400, not a retryable 503.
func resolveProgram(root, cwdRel, program string) (string, error) {
	if !strings.ContainsAny(program, `/\`) {
		path, err := lookPath(program)
		if err != nil {
			return "", Invalid("program " + program + " is not on the server's PATH").WithCause(err)
		}
		return path, nil
	}
	if filepath.IsAbs(program) {
		return "", Invalid("command program " + program + " must be a path relative to cwd inside the workspace root")
	}
	rel, err := ConfineWorkspacePath(root, filepath.Join(cwdRel, program))
	if err != nil {
		return "", Invalid("command program " + program + " is outside the workspace root").WithCause(err)
	}
	abs, err := resolveWorkspacePath(root, rel)
	if err != nil {
		return "", Invalid("command program " + program + " is outside the workspace root").WithCause(err)
	}
	if info, err := os.Stat(abs); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", Invalid("command program " + program + " is not an executable file in the workspace")
	}
	return abs, nil
}

// --- running --------------------------------------------------------------------------

// commandEnv is the environment a command runs with: the daemon's, without its own
// configuration (every XMUSTARD_* variable: tokens, the Postgres DSN, TLS keys, the data
// directory) and without the variables the redactor classifies as secrets. The command
// runs the repository's code, which must not be able to act as the daemon.
func commandEnv(environ []string) []string {
	secret := map[string]bool{}
	for _, s := range redact.SecretEnv(environ) {
		secret[s.Name] = true
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, "XMUSTARD_") && !secret[name] {
			out = append(out, kv)
		}
	}
	return out
}

// commandSlots bounds the why_failed commands running at once. A command holds one of
// the helper-child slots every Rust call shares (budget.Children, 4 by default) for its
// whole run, so one at a time leaves the others to tool calls; a second command is
// refused at once instead of queueing behind a long test run.
var commandSlots = make(chan struct{}, 1)

func acquireCommandSlot() (func(), error) {
	select {
	case commandSlots <- struct{}{}:
		return func() { <-commandSlots }, nil
	default:
		return nil, Unavailable("another why_failed command is running on this server; retry when it ends, or run the command in your shell and pass its output as log")
	}
}

// run executes the command through the bounded runner, detached from the request's
// cancellation. Cancelling would kill only the core, whose command runs in a process
// group of its own, and leave the command running without its timeout, outside the
// owned process tree and its slot. Detached, the core always ends the group itself (at
// the timeout at the latest: TERM, then KILL); a caller that left gets its context's
// error once the command has ended, and nothing is recorded.
func (c *preparedCommand) run(ctx context.Context) (*rustcore.ManagedCommandResult, error) {
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(c.timeout)*time.Second+planningRustCoreBuffer)
	defer cancel()
	res, err := runCheckCommand(runCtx, c.dir, c.timeout, c.argv, commandEnv(os.Environ()))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, Unavailable("the bounded command runner failed; the command's result is unknown").WithCause(err)
	}
	return res, nil
}

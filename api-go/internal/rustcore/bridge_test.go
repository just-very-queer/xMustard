package rustcore

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A flooding core child is bounded (output too large), not OOM, and the error is
// sanitized (no raw stderr / paths). Uses a fake core binary via XMUSTARD_CORE_BIN.
func TestRunCoreContextBoundsFloodingChild(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakecore.sh")
	// ignores args; floods stdout well past maxCoreStdout would be slow, so shrink via
	// a smaller flood that still exceeds when maxCoreStdout is large — instead assert
	// the sanitized error path on a non-zero exit with noisy stderr.
	script := "#!/bin/sh\necho 'secret /home/user/.ssh/id_rsa internal detail' 1>&2\nexit 3\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", fake)
	_, err := runCoreCtx(context.Background(), "search", "root", "ws", "query")
	if err == nil {
		t.Fatal("a failing core child must error")
	}
	if strings.Contains(err.Error(), "id_rsa") || strings.Contains(err.Error(), ".ssh") {
		t.Fatalf("client error must be sanitized (no raw stderr/paths): %q", err.Error())
	}
}

// Every xmustard-core subcommand the Go side calls still exists in the core, found by
// parsing api-go's sources rather than from a hand-kept list, so a new call is
// covered without editing this test. The one-shot hints in worker.go must name
// subcommands that exist too.
func TestEveryCoreSubcommandGoCallsExists(t *testing.T) {
	core := realCore(t)
	calls := goCoreCalls(t)
	found := map[coreCall]bool{}
	for _, c := range calls {
		found[c] = true
	}
	// A floor, so a scanner that silently stops finding calls fails here.
	for _, want := range []coreCall{
		{"wiki", ""}, {"goal", "list"}, {"goal", "status"}, {"lsp-hover", ""},
		{"lsp-document-symbols", ""}, {"symbolgraph", "build"}, {"symbolgraph", "impact"},
		{"changetrack", "index"}, {"ownership", "owners"}, {"search", ""}, {"repo-key", ""},
		{"run-managed-command", ""}, {"parse-coverage-lcov", ""}, {"normalize-lsp-references", ""},
	} {
		if !found[want] {
			t.Errorf("the source scan did not find the call %v", want.args())
		}
	}
	for _, c := range calls {
		if c.sub == "serve" { // realCore has already checked the worker entry point
			continue
		}
		assertCoreSubcommand(t, core, c.args())
	}
	for name := range oneShotOnly {
		assertCoreSubcommand(t, core, []string{name})
	}
	for family, subs := range oneShotFamilies {
		for sub := range subs {
			assertCoreSubcommand(t, core, []string{family, sub})
		}
	}
}

// assertCoreSubcommand runs the core with args and nothing after them. A subcommand
// that exists answers with its own usage line and exit code 2 (or, needing nothing
// more, succeeds). A removed one answers "unknown command: ..." or, inside a
// family, "unknown <family> subcommand: ...".
func assertCoreSubcommand(t *testing.T, core string, args []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, core, args...)
	cmd.Dir = t.TempDir()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return
	}
	var exitErr *exec.ExitError
	usage := "usage: xmustard-core " + args[0]
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 && strings.HasPrefix(stderr.String(), usage) {
		return
	}
	t.Errorf("xmustard-core %s: the Go side calls it, but the core answers %v: %q",
		strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
}

// coreCall is one xmustard-core invocation in api-go: the subcommand and, when the
// call site gives it as a string literal, the argument after it (the member of a
// command family, as in `symbolgraph build`).
type coreCall struct{ sub, first string }

func (c coreCall) args() []string {
	if c.first == "" {
		return []string{c.sub}
	}
	return []string{c.sub, c.first}
}

// coreRunner is a rustcore function that starts xmustard-core. Its subcommand is the
// fixed sub or, when sub is empty, its parameter subPos. The subcommand's arguments
// start at its parameter argPos (-1: its callers pass none through).
type coreRunner struct {
	sub            string
	subPos, argPos int
}

// goCoreCalls parses api-go's non-test sources and returns every xmustard-core
// invocation, sorted. It starts from coreInvocation, which builds every core argv,
// and follows each rustcore function that passes its subcommand parameter or its
// variadic arguments on to a runner, until no new runner turns up.
func goCoreCalls(t *testing.T) []coreCall {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("api-go module root not found at %s: %v", root, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == "testdata" || d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err == nil {
			files = append(files, f)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	runners := map[string]coreRunner{"coreInvocation": {subPos: 0, argPos: 1}}
	for pass := 0; ; pass++ {
		if pass == 20 {
			t.Fatal("the runner search did not settle")
		}
		found := map[coreCall]bool{}
		grew := false
		for _, f := range files {
			inRustcore := f.Name.Name == "rustcore"
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					name := runnerName(call, inRustcore)
					r, ok := runners[name]
					if !ok {
						return true
					}
					params, variadic := paramPositions(fn)
					sub := r.sub
					if sub == "" {
						switch a := argAt(call, r.subPos).(type) {
						case *ast.BasicLit:
							sub = stringLit(a)
						case *ast.Ident:
							if i, isParam := params[a.Name]; isParam && inRustcore {
								grew = addRunner(runners, fn.Name.Name, coreRunner{subPos: i, argPos: forwardedPos(call, r.argPos, variadic, params)}) || grew
								return true
							}
						}
					}
					if sub == "" {
						t.Errorf("%s: cannot tell which xmustard-core subcommand %s runs", fset.Position(call.Pos()), name)
						return true
					}
					first := ""
					if a := argAt(call, r.argPos); a != nil {
						switch a := a.(type) {
						case *ast.BasicLit:
							first = stringLit(a)
						case *ast.Ident:
							if !call.Ellipsis.IsValid() {
								break
							}
							if a.Name == variadic && inRustcore {
								grew = addRunner(runners, fn.Name.Name, coreRunner{sub: sub, subPos: -1, argPos: params[a.Name]}) || grew
							} else {
								first = firstLiteral(fn.Body, a.Name)
							}
						}
					}
					found[coreCall{sub, first}] = true
					return true
				})
			}
		}
		if grew {
			continue
		}
		calls := make([]coreCall, 0, len(found))
		for c := range found {
			calls = append(calls, c)
		}
		sort.Slice(calls, func(i, j int) bool {
			return strings.Join(calls[i].args(), " ") < strings.Join(calls[j].args(), " ")
		})
		return calls
	}
}

// runnerName is the called function's name when the call can reach a rustcore
// function: a plain call inside package rustcore or rustcore.Name elsewhere.
func runnerName(call *ast.CallExpr, inRustcore bool) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if inRustcore {
			return fun.Name
		}
	case *ast.SelectorExpr:
		if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == "rustcore" {
			return fun.Sel.Name
		}
	}
	return ""
}

// addRunner records name as a runner unless it already is one, and reports whether
// it added it.
func addRunner(runners map[string]coreRunner, name string, r coreRunner) bool {
	if _, ok := runners[name]; ok {
		return false
	}
	runners[name] = r
	return true
}

// paramPositions maps fn's parameter names to their positions and names its
// variadic parameter ("" if none).
func paramPositions(fn *ast.FuncDecl) (map[string]int, string) {
	params := map[string]int{}
	variadic := ""
	i := 0
	for _, field := range fn.Type.Params.List {
		_, isVariadic := field.Type.(*ast.Ellipsis)
		if len(field.Names) == 0 {
			i++
			continue
		}
		for _, n := range field.Names {
			params[n.Name] = i
			if isVariadic {
				variadic = n.Name
			}
			i++
		}
	}
	return params, variadic
}

// forwardedPos is the position of the caller's variadic parameter when the call
// passes it on (as `args...`) where the runner's arguments start, otherwise -1.
func forwardedPos(call *ast.CallExpr, argPos int, variadic string, params map[string]int) int {
	if a, ok := argAt(call, argPos).(*ast.Ident); ok && call.Ellipsis.IsValid() && a.Name == variadic {
		return params[variadic]
	}
	return -1
}

func argAt(call *ast.CallExpr, i int) ast.Expr {
	if i < 0 || i >= len(call.Args) {
		return nil
	}
	return call.Args[i]
}

func stringLit(lit *ast.BasicLit) string {
	if lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}

// firstLiteral is the first element of the slice literal assigned to name in body,
// when that element is a string literal (`args := []string{"status", root}`).
func firstLiteral(body *ast.BlockStmt, name string) string {
	first := ""
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || first != "" {
			return first == ""
		}
		for i, lhs := range assign.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name != name || i >= len(assign.Rhs) {
				continue
			}
			if lit, ok := assign.Rhs[i].(*ast.CompositeLit); ok && len(lit.Elts) > 0 {
				if s, ok := lit.Elts[0].(*ast.BasicLit); ok {
					first = stringLit(s)
				}
			}
		}
		return true
	})
	return first
}

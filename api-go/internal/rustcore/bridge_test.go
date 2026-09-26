package rustcore

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// rust-core/go-calls.txt lists every xmustard-core call that code reachable from an
// api-go main makes (`called`), and the commands kept although nothing reachable
// calls them (`kept`, with the reason). This test holds the list to the Go sources;
// cargo test reads the same file and fails when a listed command leaves the core. So
// removing a subcommand Go calls fails under both halves of make check-backend, and
// neither half needs a built core.
func TestCoreCallManifestMatchesGoSources(t *testing.T) {
	scan := scanCoreCalls(t, apiGoRoot(t))
	manifest := readCoreCallManifest(t)
	// A floor, so a scanner that silently stops finding calls fails here.
	for _, want := range []coreCall{
		{"wiki", ""}, {"goal", "list"}, {"goal", "status"}, {"lsp-document-symbols", ""},
		{"symbolgraph", "build"}, {"symbolgraph", "impact"}, {"changetrack", "index"},
		{"ownership", "owners"}, {"search", ""}, {"repo-key", ""}, {"run-managed-command", ""},
		{"normalize-lsp-references", ""}, {"serve", ""},
	} {
		if !scan.reachable[want] {
			t.Errorf("the source scan did not find the reachable call `%s`", want)
		}
	}
	// A family member the scan cannot read would leave the member unchecked.
	families := map[string]bool{}
	for c := range manifest {
		families[c.sub] = families[c.sub] || c.first != ""
	}
	for c := range scan.reachable {
		families[c.sub] = families[c.sub] || c.first != ""
	}
	for _, u := range scan.unresolved {
		if families[u.sub] {
			t.Errorf("%s: cannot tell which `%s` subcommand this call runs; pass it as a string literal or constant", u.pos, u.sub)
		}
	}
	// A family call without its member is the unresolved call reported above.
	skip := func(c coreCall) bool { return c.sub == "serve" || (c.first == "" && families[c.sub]) }
	for c := range scan.reachable {
		if skip(c) { // serve is the worker entry point, not a table command
			continue
		}
		switch manifest[c] {
		case "called":
		case "kept":
			t.Errorf("go-calls.txt keeps `%s` as having no caller, but %s calls it: make it `called`", c, scan.where[c])
		default:
			t.Errorf("%s calls `%s`, which rust-core/go-calls.txt does not list: add `called %s`", scan.where[c], c, c)
		}
	}
	for c, kind := range manifest {
		if kind == "called" && !scan.reachable[c] {
			t.Errorf("go-calls.txt lists `called %s`, but nothing reachable from an api-go main calls it: remove the subcommand with its Go wrapper, or keep it with a reason", c)
		}
	}
	// A wrapper nothing calls still keeps its subcommand (the next test runs it), so
	// the list names why the subcommand stays.
	for c := range scan.unreachable {
		if !skip(c) && !scan.reachable[c] && manifest[c] != "kept" {
			t.Errorf("only unreachable code (%s) calls `%s`: remove the wrapper and the subcommand, or add `kept %s -- <reason>` to go-calls.txt", scan.where[c], c, c)
		}
	}
}

// Every xmustard-core call in api-go, reachable or not, and every command the list
// names, runs against the core built from this checkout, together with the
// one-shot hints in worker.go.
func TestEveryCoreSubcommandGoCallsExists(t *testing.T) {
	core := bridgeCore(t)
	scan := scanCoreCalls(t, apiGoRoot(t))
	check := map[coreCall]bool{}
	for c := range scan.reachable {
		check[c] = true
	}
	for c := range scan.unreachable {
		check[c] = true
	}
	for c := range readCoreCallManifest(t) {
		check[c] = true
	}
	for name := range oneShotOnly {
		check[coreCall{name, ""}] = true
	}
	for family, subs := range oneShotFamilies {
		for sub := range subs {
			check[coreCall{family, sub}] = true
		}
	}
	delete(check, coreCall{"serve", ""}) // bridgeCore has checked the worker entry point
	for c := range check {
		assertCoreSubcommand(t, core, c.args())
	}
}

// The scan reads a family member given as a constant, reports one it cannot read,
// and tells calls in code reachable from a main from calls in dead code.
func TestCoreCallScanFollowsConstantsAndReachability(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"cmd/app/main.go": `package main

import "example.com/m/internal/ops"

func main() { ops.Live("x") }
`,
		"internal/rustcore/core.go": `package rustcore

func coreInvocation(sub string, args ...string) (string, []string, string) { return "", nil, "" }

func runCore(sub string, args ...string) { coreInvocation(sub, args...) }

func RunFamily(args ...string) { runCore("family", args...) }

func RunLive() { runCore("live") }

func RunDead() { runCore("dead") }

func RunHover(args ...string) { runCore("hover", args...) }
`,
		"internal/ops/ops.go": `package ops

import "example.com/m/internal/rustcore"

const flowMode = "flow"

type server struct{}

func Live(mode string) {
	const local = "local"
	rustcore.RunFamily(flowMode, "root")
	rustcore.RunFamily(local, "root")
	rustcore.RunFamily(mode, "root")
	args := []string{flowMode, "root"}
	rustcore.RunFamily(args...)
	rustcore.RunLive()
	server{}.handle()
}

func (server) handle() { rustcore.RunFamily("impact") }

func deadCaller() {
	rustcore.RunDead()
	rustcore.RunFamily("gone")
}
`,
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scan := scanCoreCalls(t, root)
	// {family ""} is RunFamily itself and the call at ops.go:13, whose member is a
	// parameter; RunHover is a wrapper nothing calls.
	wantReachable := map[coreCall]bool{
		{"family", "flow"}: true, {"family", "local"}: true, {"family", "impact"}: true,
		{"family", ""}: true, {"live", ""}: true,
	}
	wantUnreachable := map[coreCall]bool{{"dead", ""}: true, {"family", "gone"}: true, {"hover", ""}: true}
	if !maps.Equal(scan.reachable, wantReachable) || !maps.Equal(scan.unreachable, wantUnreachable) {
		t.Errorf("reachable %v, unreachable %v; want %v and %v", scan.reachable, scan.unreachable, wantReachable, wantUnreachable)
	}
	if len(scan.unresolved) != 1 || scan.unresolved[0].sub != "family" || !strings.HasSuffix(scan.unresolved[0].pos, "internal/ops/ops.go:13:2") {
		t.Errorf("want one unresolved family member at internal/ops/ops.go:13:2, got %+v", scan.unresolved)
	}
}

// bridgeCore is the core TestEveryCoreSubcommandGoCallsExists checks: XMUSTARD_CORE_BIN
// when set, otherwise the release binary cargo builds from this checkout's rust-core
// (a no-op when it is fresh). It never skips and never trusts a binary cargo has not
// just checked, so a clean checkout or a stale release build cannot pass for the
// current sources.
func bridgeCore(t *testing.T) string {
	t.Helper()
	bin := strings.TrimSpace(os.Getenv("XMUSTARD_CORE_BIN"))
	if bin == "" {
		cargo, err := exec.LookPath("cargo")
		if err != nil {
			t.Fatalf("cargo is not on PATH: build rust-core or set XMUSTARD_CORE_BIN (%v)", err)
		}
		ctx := context.Background()
		if deadline, ok := t.Deadline(); ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline.Add(-30*time.Second))
			defer cancel()
		}
		build := exec.CommandContext(ctx, cargo, "build", "--release", "--quiet", "--bin", "xmustard-core")
		build.Dir = rustCoreDir()
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("cargo build --release in %s: %v\n%s", build.Dir, err, out)
		}
		target := filepath.Join(rustCoreDir(), "target")
		if dir := os.Getenv("CARGO_TARGET_DIR"); dir != "" {
			target = dir
			if !filepath.IsAbs(dir) {
				target = filepath.Join(rustCoreDir(), dir)
			}
		}
		bin = filepath.Join(target, "release", "xmustard-core")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
	}
	// serve's own usage error proves the binary has the worker entry point.
	out, _ := exec.Command(bin, "serve", "--bogus=1").CombinedOutput()
	if !strings.HasPrefix(string(out), "usage: xmustard-core serve") {
		t.Fatalf("%s has no `serve` worker entry point: %q", bin, strings.TrimSpace(string(out)))
	}
	return bin
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

// coreCall is one xmustard-core invocation: the subcommand and, for a command
// family, the member after it (as in `symbolgraph build`).
type coreCall struct{ sub, first string }

func (c coreCall) args() []string {
	if c.first == "" {
		return []string{c.sub}
	}
	return []string{c.sub, c.first}
}

func (c coreCall) String() string { return strings.Join(c.args(), " ") }

func apiGoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// readCoreCallManifest parses rust-core/go-calls.txt into call -> "called" | "kept".
func readCoreCallManifest(t *testing.T) map[coreCall]string {
	t.Helper()
	path := filepath.Join(rustCoreDir(), "go-calls.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[coreCall]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		spec, reason, hasReason := strings.Cut(line, " -- ")
		words := strings.Fields(spec)
		bad := len(words) < 2 || len(words) > 3 ||
			(words[0] == "called" && hasReason) ||
			(words[0] == "kept" && strings.TrimSpace(reason) == "") ||
			(words[0] != "called" && words[0] != "kept")
		if bad {
			t.Fatalf("%s:%d: want `called <sub> [member]` or `kept <sub> [member] -- <reason>`, got %q", path, i+1, line)
		}
		c := coreCall{sub: words[1]}
		if len(words) == 3 {
			c.first = words[2]
		}
		if _, dup := out[c]; dup {
			t.Fatalf("%s:%d: `%s` is listed twice", path, i+1, c)
		}
		out[c] = words[0]
	}
	return out
}

// coreScan is every xmustard-core invocation in a Go module's non-test sources.
type coreScan struct {
	reachable   map[coreCall]bool // made by code reachable from a main
	unreachable map[coreCall]bool // made by code no main reaches
	where       map[coreCall]string
	unresolved  []unresolvedMember
}

// unresolvedMember is a call whose family member the scan could not read.
type unresolvedMember struct{ sub, pos string }

// coreRunner is a rustcore function that starts xmustard-core. Its subcommand is the
// fixed sub or, when sub is empty, its parameter subPos. The subcommand's arguments
// start at its parameter argPos (-1: its callers pass none through).
type coreRunner struct {
	sub            string
	subPos, argPos int
}

// goFile is one parsed non-test source file.
type goFile struct {
	f       *ast.File
	dir     string            // package directory, relative to the module root
	imports map[string]string // local name -> package directory, for the module's own packages
	consts  map[string]string // the module's package-level string constants, by dir.Name
	pos     func(ast.Node) string
}

// scanCoreCalls parses the module's non-test sources under root and returns every
// xmustard-core invocation. It starts from coreInvocation, which builds every core
// argv, and follows each rustcore function that passes its subcommand parameter or
// its variadic arguments on to a runner, until no new runner turns up. A wrapper
// that forwards its variadic arguments counts as a call without a member, so a
// wrapper nothing calls still shows up as a call in dead code.
//
// A call counts as reachable when its function is reachable from a main or init
// function by name: a package function through its identifier, a method through
// any selector of its name, and a method nothing names (it may satisfy a standard
// library interface) always. Package-level initializers are roots too. The
// approximation errs toward reachable, never toward dead.
func scanCoreCalls(t *testing.T, root string) coreScan {
	t.Helper()
	files := parseModule(t, root)
	reached := reachableFuncs(files)
	scan := coreScan{
		reachable:   map[coreCall]bool{},
		unreachable: map[coreCall]bool{},
		where:       map[coreCall]string{},
	}
	runners := map[string]coreRunner{"coreInvocation": {subPos: 0, argPos: 1}}
	for pass := 0; ; pass++ {
		if pass == 20 {
			t.Fatal("the runner search did not settle")
		}
		scan.reachable, scan.unreachable, scan.where, scan.unresolved = map[coreCall]bool{}, map[coreCall]bool{}, map[coreCall]string{}, nil
		grew := false
		for _, g := range files {
			inRustcore := g.f.Name.Name == "rustcore"
			for _, decl := range g.f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				live := reached[declKey(g, fn)]
				params, variadic := paramPositions(fn)
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
					pos := g.pos(call)
					sub := r.sub
					if sub == "" {
						a := argAt(call, r.subPos)
						if id, ok := a.(*ast.Ident); ok && inRustcore {
							if i, isParam := params[id.Name]; isParam {
								grew = addRunner(runners, fn.Name.Name, coreRunner{subPos: i, argPos: forwardedPos(call, r.argPos, variadic, params)}) || grew
								return true
							}
						}
						sub, _ = g.constString(fn, a)
					}
					if sub == "" {
						t.Errorf("%s: cannot tell which xmustard-core subcommand %s runs", pos, name)
						return true
					}
					first, resolved := "", true
					if a := argAt(call, r.argPos); a != nil {
						id, isIdent := a.(*ast.Ident)
						if isIdent && call.Ellipsis.IsValid() && r.argPos == len(call.Args)-1 {
							if id.Name == variadic && inRustcore {
								// a wrapper: its callers name the member. The call
								// still counts, so a wrapper nothing calls shows up.
								grew = addRunner(runners, fn.Name.Name, coreRunner{sub: sub, subPos: -1, argPos: params[id.Name]}) || grew
							} else {
								first, resolved = g.sliceFirst(fn, id.Name)
							}
						} else {
							first, resolved = g.constString(fn, a)
						}
					}
					if !resolved {
						scan.unresolved = append(scan.unresolved, unresolvedMember{sub, pos})
					}
					c := coreCall{sub, first}
					if live {
						scan.reachable[c] = true
					} else {
						scan.unreachable[c] = true
					}
					if _, seen := scan.where[c]; !seen || live {
						scan.where[c] = pos
					}
					return true
				})
			}
		}
		if !grew {
			return scan
		}
	}
}

// parseModule parses every non-test .go file in the module rooted at root.
func parseModule(t *testing.T, root string) []*goFile {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("no Go module at %s: %v", root, err)
	}
	module := ""
	for _, line := range strings.Split(string(mod), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			module = strings.Trim(strings.TrimSpace(rest), `"`)
			break
		}
	}
	if module == "" {
		t.Fatalf("%s/go.mod names no module", root)
	}
	fset := token.NewFileSet()
	var files []*goFile
	consts := map[string]string{}
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
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		g := &goFile{f: f, dir: filepath.ToSlash(rel), imports: map[string]string{}, consts: consts}
		g.pos = func(n ast.Node) string {
			p := fset.Position(n.Pos())
			if r, err := filepath.Rel(root, p.Filename); err == nil {
				p.Filename = filepath.ToSlash(r)
			}
			return p.String()
		}
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			dir, ok := strings.CutPrefix(ip, module+"/")
			if !ok {
				continue
			}
			local := dir[strings.LastIndex(dir, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			g.imports[local] = dir
		}
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.CONST {
				collectConsts(gd, g.dir+".", consts)
			}
		}
		files = append(files, g)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// collectConsts records the string-literal constants of a const declaration.
func collectConsts(gd *ast.GenDecl, prefix string, into map[string]string) {
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vs.Names {
			if i < len(vs.Values) {
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					into[prefix+name.Name] = stringLit(lit)
				}
			}
		}
	}
}

// constString reads e as a string literal or a string constant: one declared in
// fn, in the file's package, or in another package of the module.
func (g *goFile) constString(fn *ast.FuncDecl, e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		s := stringLit(e)
		return s, e.Kind == token.STRING
	case *ast.Ident:
		local := map[string]string{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if ds, ok := n.(*ast.DeclStmt); ok {
				if gd, ok := ds.Decl.(*ast.GenDecl); ok && gd.Tok == token.CONST {
					collectConsts(gd, "", local)
				}
			}
			return true
		})
		if s, ok := local[e.Name]; ok {
			return s, true
		}
		s, ok := g.consts[g.dir+"."+e.Name]
		return s, ok
	case *ast.SelectorExpr:
		if pkg, ok := e.X.(*ast.Ident); ok {
			if dir, ok := g.imports[pkg.Name]; ok {
				s, ok := g.consts[dir+"."+e.Sel.Name]
				return s, ok
			}
		}
	}
	return "", false
}

// sliceFirst reads the first element of the slice literals assigned to name in fn
// (`args := []string{"status", root}`). Every assignment must agree.
func (g *goFile) sliceFirst(fn *ast.FuncDecl, name string) (string, bool) {
	first, found, ok := "", false, true
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, isAssign := n.(*ast.AssignStmt)
		if !isAssign {
			return true
		}
		for i, lhs := range assign.Lhs {
			id, isIdent := lhs.(*ast.Ident)
			if !isIdent || id.Name != name || i >= len(assign.Rhs) {
				continue
			}
			lit, isLit := assign.Rhs[i].(*ast.CompositeLit)
			if !isLit {
				continue // append(args, ...) keeps the first element
			}
			s, resolved := "", false
			if len(lit.Elts) > 0 {
				s, resolved = g.constString(fn, lit.Elts[0])
			}
			if !resolved || (found && s != first) {
				ok = false
			}
			first, found = s, true
		}
		return true
	})
	return first, found && ok
}

// declKey names a function declaration for reachableFuncs: dir.Name for a package
// function, "method Name" for every method of that name.
func declKey(g *goFile, fn *ast.FuncDecl) string {
	if fn.Recv != nil {
		return "method " + fn.Name.Name
	}
	return g.dir + "." + fn.Name.Name
}

// reachableFuncs returns the declKeys reachable from the module's main and init
// functions and package-level initializers (see scanCoreCalls).
func reachableFuncs(files []*goFile) map[string]bool {
	type body struct {
		g  *goFile
		fn *ast.FuncDecl
	}
	bodies := map[string][]body{}
	named := map[string]bool{} // every "method Name" some selector names
	for _, g := range files {
		for _, decl := range g.f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				bodies[declKey(g, fn)] = append(bodies[declKey(g, fn)], body{g, fn})
			}
		}
		references(g, g.f, func(key string) {
			if strings.HasPrefix(key, "method ") {
				named[key] = true
			}
		})
	}
	reached := map[string]bool{}
	var queue []string
	mark := func(key string) {
		if !reached[key] && len(bodies[key]) > 0 {
			reached[key] = true
			queue = append(queue, key)
		}
	}
	for _, g := range files {
		for _, decl := range g.f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				key := declKey(g, d)
				if (d.Recv == nil && (d.Name.Name == "init" || (d.Name.Name == "main" && g.f.Name.Name == "main"))) ||
					(d.Recv != nil && !named[key]) {
					mark(key)
				}
			case *ast.GenDecl:
				references(g, d, mark)
			}
		}
	}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		for _, b := range bodies[key] {
			references(b.g, b.fn.Body, mark)
		}
	}
	return reached
}

// references reports the declKeys n names: pkg.Name through an import, a bare
// identifier in the file's own package, and "method Name" for any other selector.
func references(g *goFile, n ast.Node, ref func(key string)) {
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				if dir, ok := g.imports[id.Name]; ok {
					ref(dir + "." + x.Sel.Name)
					return false
				}
			}
			ref("method " + x.Sel.Name)
			ast.Inspect(x.X, visit)
			return false
		case *ast.Ident:
			ref(g.dir + "." + x.Name)
		}
		return true
	}
	ast.Inspect(n, visit)
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

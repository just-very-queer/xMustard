//go:build ignore

// gen_go writes the go/types ground truth for the Go precision fixture: every call
// (caller symbol, callee declaration) whose callee is a function or method declared
// in the fixture. go/types is the type checker gopls uses.
//
//	go run gen_go.go ../go > go_calls.json
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	root, err := filepath.Abs(os.Args[1])
	check(err)
	module := modulePath(filepath.Join(root, "go.mod"))
	// the source importer resolves module paths from the working directory
	check(os.Chdir(root))
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "source", nil).(types.ImporterFrom)
	pairs := map[[2]string]bool{}
	check(filepath.Walk(root, func(dir string, fi os.FileInfo, err error) error {
		if err != nil || !fi.IsDir() {
			return err
		}
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		check(err)
		for _, p := range pkgs {
			var files []*ast.File
			for _, f := range p.Files {
				files = append(files, f)
			}
			rel, _ := filepath.Rel(root, dir)
			path := module
			if rel != "." {
				path += "/" + filepath.ToSlash(rel)
			}
			info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
			conf := types.Config{Importer: importerAt{imp, dir}}
			_, err := conf.Check(path, fset, files, info)
			check(err)
			for _, f := range files {
				collect(fset, root, f, info, pairs)
			}
		}
		return nil
	}))
	out := make([][2]string, 0, len(pairs))
	for p := range pairs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0]+out[i][1] < out[j][0]+out[j][1] })
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	check(enc.Encode(out))
}

type importerAt struct {
	imp types.ImporterFrom
	dir string
}

func (i importerAt) Import(path string) (*types.Package, error) {
	return i.imp.ImportFrom(path, i.dir, 0)
}

func collect(fset *token.FileSet, root string, f *ast.File, info *types.Info, pairs map[[2]string]bool) {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		caller := site(fset, root, fd.Pos()) + "::" + declName(fd)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var id *ast.Ident
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				id = fn
			case *ast.SelectorExpr:
				id = fn.Sel
			}
			if id == nil {
				return true
			}
			fn, ok := info.Uses[id].(*types.Func)
			if !ok || !strings.HasPrefix(fset.Position(fn.Pos()).Filename, root) {
				return true
			}
			pairs[[2]string{caller, site(fset, root, fn.Pos()) + "::" + funcName(fn)}] = true
			return true
		})
	}
}

func site(fset *token.FileSet, root string, pos token.Pos) string {
	rel, _ := filepath.Rel(root, fset.Position(pos).Filename)
	return filepath.ToSlash(rel)
}

func declName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	return t.(*ast.Ident).Name + "." + fd.Name.Name
}

func funcName(fn *types.Func) string {
	sig := fn.Type().(*types.Signature)
	if sig.Recv() == nil {
		return fn.Name()
	}
	t := sig.Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name() + "." + fn.Name()
	}
	// an interface method: its receiver is the interface type
	for _, name := range fn.Pkg().Scope().Names() {
		obj := fn.Pkg().Scope().Lookup(name)
		if it, ok := obj.Type().Underlying().(*types.Interface); ok {
			for i := 0; i < it.NumMethods(); i++ {
				if it.Method(i) == fn {
					return name + "." + fn.Name()
				}
			}
		}
	}
	return fn.Name()
}

func modulePath(gomod string) string {
	b, err := os.ReadFile(gomod)
	check(err)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(l, "module "))
		}
	}
	panic("no module directive")
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

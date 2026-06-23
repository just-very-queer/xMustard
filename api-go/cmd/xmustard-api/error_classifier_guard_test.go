package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// All wording-coupled HTTP-status classifiers have been migrated onto the typed
// DomainError + respondError mapper (XM-PRO-013). This guard holds the count at ZERO and
// — unlike the prior literal strings.Count — walks the AST, so it also catches ALIASED
// forms the string match missed: `e := err; strings.Contains(strings.ToLower(e.Error()), …)`,
// `strings.Contains(someErr.Error(), …)`, `strings.Contains(fmt.Sprint(err), …)`, etc.
//
// The rule: a handler must never branch HTTP status on the TEXT of an error. Any
// strings.Contains / strings.HasPrefix / strings.HasSuffix / strings.Index whose haystack
// is derived from an error value's message (a `.Error()` call or fmt.Sprint(err)) is a
// wording classifier. New handlers must classify by typed DomainError + respondError.
func TestErrorClassifierCountDoesNotGrow(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	const ceiling = 0
	var offenders []string

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "strings" {
			return true
		}
		switch sel.Sel.Name {
		case "Contains", "HasPrefix", "HasSuffix", "Index", "ContainsAny":
		default:
			return true
		}
		// the haystack is the first argument; flag it if it is derived from an error
		// message in any form (direct, ToLower-wrapped, aliased identifier, fmt.Sprint).
		if len(call.Args) > 0 && exprDerivesFromErrorText(call.Args[0]) {
			offenders = append(offenders, fset.Position(call.Pos()).String())
		}
		return true
	})

	if len(offenders) > ceiling {
		t.Fatalf("found %d HTTP handlers classifying on error TEXT (ceiling %d) — classify by "+
			"typed DomainError + respondError, not substring matching:\n  %s",
			len(offenders), ceiling, strings.Join(offenders, "\n  "))
	}
}

// exprDerivesFromErrorText reports whether an expression's value comes from an error's
// message: an `x.Error()` call (any receiver, so aliases are caught), a fmt.Sprint*(err)
// style call, or a strings.ToLower/ToUpper/TrimSpace wrapper around one of those.
func exprDerivesFromErrorText(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			// any receiver's .Error() method — catches err.Error(), e.Error(),
			// someWrapped.Error(), etc. (aliasing).
			if sel.Sel.Name == "Error" && len(call.Args) == 0 {
				found = true
				return false
			}
			// fmt.Sprint(err) / fmt.Sprintf("%v", err): stringifying an error to match on.
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "fmt" &&
				strings.HasPrefix(sel.Sel.Name, "Sprint") {
				for _, a := range call.Args {
					if ai, ok := a.(*ast.Ident); ok && strings.Contains(strings.ToLower(ai.Name), "err") {
						found = true
						return false
					}
				}
			}
		}
		return true
	})
	return found
}

// Sanity check the guard actually fires on an aliased classifier the literal string match
// would have missed — so a green result means "no offenders," not "guard is broken."
func TestErrorClassifierGuardCatchesAliasedForm(t *testing.T) {
	const sample = `package p
import "strings"
func h(err error) int {
	e := err
	if strings.Contains(strings.ToLower(e.Error()), "not found") { return 404 }
	return 500
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", sample, 0)
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "strings" && sel.Sel.Name == "Contains" {
					if len(call.Args) > 0 && exprDerivesFromErrorText(call.Args[0]) {
						hits++
					}
				}
			}
		}
		return true
	})
	if hits != 1 {
		t.Fatalf("AST guard must catch the aliased e.Error() classifier (got %d hits)", hits)
	}
}

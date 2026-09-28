package workspaceops

import (
	"slices"
	"testing"
)

// Each impact form forwards its own parameters as core flags, in its table's order,
// and refuses another form's parameters and out-of-range values (WS-35).
func TestImpactFlagsTable(t *testing.T) {
	ok := []struct {
		form   ImpactForm
		params map[string]string
		want   []string
	}{
		{ImpactBlast, map[string]string{"depth": "5", "direction": "downstream", "relation_types": "calls,ACCESSES",
			"min_confidence": "0.9", "include_tests": "true", "limit": "10", "offset": "0", "summary_only": "false", "timeout_ms": "500"},
			[]string{"--relation-types=calls,ACCESSES", "--min-confidence=0.9", "--include-tests=true", "--limit=10",
				"--offset=0", "--summary-only=false", "--timeout-ms=500", "--max-depth=5", "--direction=downstream"}},
		{ImpactTrace, map[string]string{"depth": "30"}, []string{"--max-depth=30"}},
		{ImpactChanges, map[string]string{"scope": "compare", "base": "origin/main", "head": "HEAD~1", "worktree": "/tmp/wt", "symbols": "20"},
			[]string{"--scope=compare", "--base=origin/main", "--head=HEAD~1", "--worktree=/tmp/wt", "--symbols=20"}},
		{ImpactChanges, map[string]string{"scope": "workspace"}, []string{"--scope=workspace"}},
		{ImpactChanges, map[string]string{"scope": "commit", "head": "HEAD~1"}, []string{"--scope=commit", "--head=HEAD~1"}},
		{ImpactSinceIndex, map[string]string{"unrelated": "x"}, nil},
	}
	for _, c := range ok {
		got, err := ImpactFlags(c.form, c.params)
		if err != nil || !slices.Equal(got, c.want) {
			t.Fatalf("%s %v: got %v %v, want %v", c.form, c.params, got, err, c.want)
		}
	}
	bad := []struct {
		form   ImpactForm
		params map[string]string
	}{
		{ImpactBlast, map[string]string{"depth": "33"}},
		{ImpactBlast, map[string]string{"depth": "two"}},
		{ImpactBlast, map[string]string{"direction": "both"}},
		{ImpactBlast, map[string]string{"relation_types": "CALLS,NOPE"}},
		{ImpactBlast, map[string]string{"min_confidence": "1.01"}},
		{ImpactBlast, map[string]string{"min_confidence": "NaN"}},
		{ImpactBlast, map[string]string{"min_confidence": "-Inf"}},
		{ImpactBlast, map[string]string{"include_tests": "yes"}},
		{ImpactBlast, map[string]string{"scope": "all"}},
		{ImpactBlast, map[string]string{"from": "a"}},
		{ImpactTrace, map[string]string{"depth": "31"}},
		{ImpactTrace, map[string]string{"direction": "upstream"}},
		{ImpactTrace, map[string]string{"limit": "5"}},
		{ImpactTrace, map[string]string{"symbol": "x"}},
		{ImpactChanges, map[string]string{"scope": "sideways"}},
		{ImpactChanges, map[string]string{"scope": "range", "base": "-x"}},
		{ImpactChanges, map[string]string{"scope": "range", "base": "a b"}},
		{ImpactChanges, map[string]string{"scope": "all", "direction": "upstream"}},
		{ImpactChanges, map[string]string{"scope": "all", "symbols": "501"}},
		// the scope's revision rules (gitdiff.Scopes, rust-core's Scope::needs)
		{ImpactChanges, map[string]string{"scope": "compare"}},
		{ImpactChanges, map[string]string{"scope": "range", "head": "HEAD"}},
		{ImpactChanges, map[string]string{"scope": "staged", "base": "main"}},
		{ImpactChanges, map[string]string{"scope": "unstaged", "head": "HEAD"}},
		{ImpactChanges, map[string]string{"scope": "workspace", "head": "HEAD"}},
		{ImpactChanges, map[string]string{"scope": "commit", "base": "main"}},
		// a relative worktree would resolve against the core's working directory
		{ImpactChanges, map[string]string{"scope": "compare", "base": "main", "worktree": "../wt"}},
		{ImpactSinceIndex, map[string]string{"depth": "2"}},
		{ImpactSinceIndex, map[string]string{"base": "main"}},
		{"sideways", nil},
	}
	for _, c := range bad {
		if _, err := ImpactFlags(c.form, c.params); !IsInvalidInput(err) {
			t.Fatalf("%s %v: want invalid input, got %v", c.form, c.params, err)
		}
	}
}

func TestImpactFormOfTable(t *testing.T) {
	cases := []struct {
		params map[string]string
		want   ImpactForm
		bad    bool
	}{
		{map[string]string{"from": "a", "to": "b", "symbol": "s"}, ImpactTrace, false},
		{map[string]string{"symbol": "s", "scope": "all"}, ImpactBlast, false},
		{map[string]string{"scope": "all"}, ImpactChanges, false},
		{map[string]string{}, ImpactSinceIndex, false},
		{map[string]string{"from": "a"}, "", true},
		{map[string]string{"to": "b", "symbol": "s"}, "", true},
	}
	for _, c := range cases {
		got, err := ImpactFormOf(c.params)
		if (err != nil) != c.bad || got != c.want {
			t.Fatalf("%v: got %q %v", c.params, got, err)
		}
	}
}

// A whole request is checked against its form: the form's targets are kept out of the
// flags, and any parameter of another form refuses the request (never dropped).
func TestCheckImpactRequestRefusesAnotherFormsParameters(t *testing.T) {
	form, rest, err := CheckImpactRequest(map[string]string{"symbol": "X", "direction": "downstream", "depth": "2"})
	if err != nil || form != ImpactBlast || rest["symbol"] != "" || rest["direction"] != "downstream" {
		t.Fatalf("got %s %v %v", form, rest, err)
	}
	if form, rest, err := CheckImpactRequest(map[string]string{"from": "a", "to": "b", "depth": "12"}); err != nil ||
		form != ImpactTrace || rest["from"] != "" || rest["to"] != "" || rest["depth"] != "12" {
		t.Fatalf("trace: got %s %v %v", form, rest, err)
	}
	for _, bad := range []map[string]string{
		{"symbol": "X", "scope": "staged"},
		{"from": "a", "to": "b", "direction": "downstream", "limit": "5"},
		{"from": "a", "to": "b", "symbol": "s"},
		{"scope": "all", "direction": "downstream"},
		{"direction": "downstream"},
		{"from": "a"},
	} {
		if _, _, err := CheckImpactRequest(bad); !IsInvalidInput(err) {
			t.Fatalf("%v: want invalid input, got %v", bad, err)
		}
	}
}

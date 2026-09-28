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
		{ImpactChanges, map[string]string{"scope": "workspace", "base": "origin/main", "head": "HEAD~1", "worktree": "/tmp/wt", "symbols": "20"},
			[]string{"--scope=workspace", "--base=origin/main", "--head=HEAD~1", "--worktree=/tmp/wt", "--symbols=20"}},
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

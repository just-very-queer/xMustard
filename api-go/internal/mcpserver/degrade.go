package mcpserver

import (
	"fmt"
	"strconv"

	"xmustard/api-go/internal/toolbudget"
)

// The output budget of search, explain and impact (WS-36, PAR-CTX-05): the arguments
// they share and the documentation, both from toolbudget's tables. The API fits the
// result (the tool budget middleware); the MCP tools always send max_chars, like ground.

// budgetSpec is a budgeted tool's contract; a tool name without one is a programming
// error caught when the tool table is built.
func budgetSpec(tool string) *toolbudget.Spec {
	spec, ok := toolbudget.For(tool)
	if !ok {
		panic("no output budget for tool " + tool)
	}
	return spec
}

// budgetArgs are a budgeted tool's Advanced output-budget arguments: max_chars, and
// limit and cursor where the budget layer pages the tool's list.
func budgetArgs(tool string) []Arg {
	spec := budgetSpec(tool)
	args := []Arg{{Name: "max_chars", Type: typeInteger, Min: toolbudget.MinMaxChars, Max: toolbudget.MaxMaxChars,
		Desc: fmt.Sprintf("result budget (default %d)", spec.DefaultMaxChars)}}
	if !spec.Paged {
		return args
	}
	return append(args,
		Arg{Name: "limit", Type: typeInteger, Min: 1, Max: toolbudget.MaxLimit, Desc: "items per page"},
		Arg{Name: "cursor", Type: typeString, MaxLen: maxSearchCursorLen, Desc: "next_cursor of the previous page"})
}

// budgetQuery is the budget part of a budgeted tool's API query: the caller's
// max_chars or the tool's default, and the paging arguments.
func budgetQuery(tool string, a map[string]string) []string {
	spec := budgetSpec(tool)
	maxChars := a["max_chars"]
	if maxChars == "" {
		maxChars = strconv.Itoa(spec.DefaultMaxChars)
	}
	kv := []string{"max_chars", maxChars}
	if spec.Paged {
		kv = append(kv, "limit", a["limit"], "cursor", a["cursor"])
	}
	return kv
}

// argQuery is the query of the named arguments, each under its own name.
func argQuery(a map[string]string, names []string) []string {
	kv := make([]string, 0, 2*len(names))
	for _, n := range names {
		kv = append(kv, n, a[n])
	}
	return kv
}

// maxSearchCursorLen bounds a cursor argument before the API decodes it.
const maxSearchCursorLen = 64
